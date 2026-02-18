package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// validID matches alphanumeric IDs with hyphens and underscores.
// Rejects path traversal (../) and URL-special chars (?, &, #, /).
var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

type Exporter struct {
	browser      *Browser
	browserMu    sync.Mutex
	cfg          *Config
	throttle     *Throttle
	manifest     *ExportManifest
	storage      Storage
	searchFilter map[string]bool // nil = export all, non-nil = only matched IDs
}

func NewExporter(cfg *Config) (*Exporter, error) {
	var storage Storage
	if cfg.ICloud && cfg.ICloudPath != "" {
		s, err := NewICloudStorage(cfg.OutputDir, cfg.ICloudPath)
		if err != nil {
			return nil, fmt.Errorf("icloud storage: %w", err)
		}
		storage = s
	} else {
		storage = NewLocalStorage(cfg.OutputDir)
	}

	return &Exporter{
		cfg: cfg,
		throttle: &Throttle{
			Min: time.Duration(cfg.MinDelaySec * float64(time.Second)),
			Max: time.Duration(cfg.MaxDelaySec * float64(time.Second)),
		},
		manifest: &ExportManifest{ExportedAt: time.Now().UTC().Format(time.RFC3339)},
		storage:  storage,
	}, nil
}

func (e *Exporter) Run(ctx context.Context) error {
	if err := e.storage.EnsureDir(""); err != nil {
		return fmt.Errorf("output dir: %w", err)
	}

	// Single meeting mode: --id skips discovery entirely.
	if e.cfg.MeetingID != "" {
		return e.runSingle(ctx)
	}

	// Search filter: if --search is set, resolve matching IDs before discovery.
	if e.cfg.SearchQuery != "" {
		if err := e.buildSearchFilter(ctx); err != nil {
			return fmt.Errorf("search: %w", err)
		}
	}

	meetings, err := e.discover(ctx)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	if len(meetings) == 0 {
		slog.Warn("No meetings found")
		return nil
	}

	// Apply search filter.
	if e.searchFilter != nil {
		filtered := meetings[:0]
		for _, m := range meetings {
			if e.searchFilter[m.ID] {
				filtered = append(filtered, m)
			} else {
				slog.Debug("Skipping (not in search results)", "id", m.ID)
			}
		}
		meetings = filtered
		if len(meetings) == 0 {
			slog.Warn("No meetings matched search filter after discovery")
			return nil
		}
		slog.Info("Search filter applied", "matched", len(meetings))
	}

	if e.cfg.MaxMeetings > 0 && len(meetings) > e.cfg.MaxMeetings {
		meetings = meetings[:e.cfg.MaxMeetings]
	}

	// Dry-run: list what would be exported and exit.
	if e.cfg.DryRun {
		e.printDryRun(meetings)
		return nil
	}

	slog.Info("Exporting meetings", "count", len(meetings), "output", absPath(e.cfg.OutputDir))
	e.manifest.Total = len(meetings)

	if e.cfg.Parallel > 1 {
		e.exportParallel(ctx, meetings)
	} else {
		e.exportSequential(ctx, meetings)
	}

	if err := e.storage.WriteJSON("_export-manifest.json", e.manifest); err != nil {
		slog.Error("Manifest write failed", "error", err)
	}
	slog.Info("Done",
		"ok", e.manifest.OK,
		"skipped", e.manifest.Skipped,
		"errors", e.manifest.Errors,
		"hls_pending", e.manifest.HLSPending,
	)
	if e.manifest.HLSPending > 0 {
		fmt.Println("  Run ./convert_hls.sh to convert HLS streams to MP4")
	}
	return nil
}

// exportSequential exports meetings one at a time (the default).
func (e *Exporter) exportSequential(ctx context.Context, meetings []MeetingRef) {
	for i, m := range meetings {
		if err := ctx.Err(); err != nil {
			slog.Warn("Cancelled", "completed", i, "total", len(meetings))
			break
		}
		slog.Info(fmt.Sprintf("[%d/%d] %s", i+1, len(meetings), coalesce(m.Title, m.ID)))
		r := e.exportOne(ctx, m)
		e.manifest.Meetings = append(e.manifest.Meetings, r)
		switch r.Status {
		case "ok":
			e.manifest.OK++
		case "skipped":
			e.manifest.Skipped++
		case "hls_pending":
			e.manifest.HLSPending++
			e.manifest.OK++
		default:
			e.manifest.Errors++
		}
		if i < len(meetings)-1 {
			_ = e.throttle.Wait(ctx)
		}
	}
}

// indexedResult pairs an export result with its original index so the
// manifest stays ordered even when meetings finish out of order.
type indexedResult struct {
	index  int
	result *ExportResult
}

// exportParallel exports up to cfg.Parallel meetings concurrently.
// Each worker independently calls exportOne (which writes to per-meeting files).
// Results are collected via a channel so that manifest updates happen in a
// single goroutine (no mutex needed).
func (e *Exporter) exportParallel(ctx context.Context, meetings []MeetingRef) {
	n := e.cfg.Parallel
	total := len(meetings)

	// Pre-allocate manifest slots so results can be placed by index.
	e.manifest.Meetings = make([]*ExportResult, total)

	sem := make(chan struct{}, n)
	results := make(chan indexedResult, n)

	var wg sync.WaitGroup

	// Producer: dispatch meetings to workers, limited by semaphore.
	go func() {
		for i, m := range meetings {
			if err := ctx.Err(); err != nil {
				break
			}

			sem <- struct{}{} // acquire slot (blocks when N workers are active)
			wg.Add(1)

			go func(idx int, ref MeetingRef) {
				defer wg.Done()
				defer func() { <-sem }() // release slot

				slog.Info(fmt.Sprintf("[%d/%d] %s", idx+1, total, coalesce(ref.Title, ref.ID)))
				r := e.exportOne(ctx, ref)
				results <- indexedResult{index: idx, result: r}
			}(i, m)
		}

		wg.Wait()
		close(results)
	}()

	// Consumer: collect results in the main goroutine (single-writer).
	for ir := range results {
		e.manifest.Meetings[ir.index] = ir.result
		switch ir.result.Status {
		case "ok":
			e.manifest.OK++
		case "skipped":
			e.manifest.Skipped++
		case "hls_pending":
			e.manifest.HLSPending++
			e.manifest.OK++
		default:
			e.manifest.Errors++
		}
	}

	// Compact: remove nil slots left by meetings that were never dispatched
	// (e.g. context cancelled mid-dispatch). Keeps manifest consistent with
	// the sequential path which uses append.
	compacted := make([]*ExportResult, 0, len(e.manifest.Meetings))
	for _, r := range e.manifest.Meetings {
		if r != nil {
			compacted = append(compacted, r)
		}
	}
	e.manifest.Meetings = compacted
}

// printDryRun lists the meetings that would be exported without doing it.
func (e *Exporter) printDryRun(meetings []MeetingRef) {
	slog.Info(fmt.Sprintf("Dry run: %d meeting(s) would be exported", len(meetings)))

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "#\tID\tDATE\tTITLE")
	for i, m := range meetings {
		date := dateFromISO(m.Date)
		title := coalesce(m.Title, "(untitled)")
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", i+1, m.ID, date, title)
	}
	w.Flush()
}

func (e *Exporter) Close() {
	if e.browser != nil {
		e.browser.Close()
	}
	if e.storage != nil {
		if err := e.storage.Close(); err != nil {
			slog.Error("Storage close failed", "error", err)
		}
	}
}

// runSingle exports a single meeting by ID, skipping discovery.
func (e *Exporter) runSingle(ctx context.Context) error {
	id := e.cfg.MeetingID
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid meeting ID: %q", id)
	}

	slog.Info("Single meeting export", "id", id)

	ref := MeetingRef{
		ID:  id,
		URL: meetingURL(id),
	}

	// Dry-run: show what would be exported and exit.
	if e.cfg.DryRun {
		e.printDryRun([]MeetingRef{ref})
		return nil
	}

	e.manifest.Total = 1
	slog.Info(fmt.Sprintf("[1/1] %s", coalesce(ref.Title, ref.ID)))
	r := e.exportOne(ctx, ref)
	e.manifest.Meetings = append(e.manifest.Meetings, r)

	switch r.Status {
	case "ok":
		e.manifest.OK++
	case "skipped":
		e.manifest.Skipped++
	case "hls_pending":
		e.manifest.HLSPending++
		e.manifest.OK++
	default:
		e.manifest.Errors++
	}

	if err := e.storage.WriteJSON("_export-manifest.json", e.manifest); err != nil {
		slog.Error("Manifest write failed", "error", err)
	}

	slog.Info("Done",
		"ok", e.manifest.OK,
		"skipped", e.manifest.Skipped,
		"errors", e.manifest.Errors,
	)
	return nil
}

// buildSearchFilter runs a Grain search and populates the filter map.
func (e *Exporter) buildSearchFilter(ctx context.Context) error {
	b, err := e.lazyBrowser()
	if err != nil {
		return fmt.Errorf("browser init for search: %w", err)
	}

	results, err := b.Search(ctx, e.cfg.SearchQuery)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		slog.Info("No meetings matched search query", "query", e.cfg.SearchQuery)
		e.searchFilter = make(map[string]bool) // empty = export nothing
		return nil
	}

	e.searchFilter = make(map[string]bool, len(results))
	for _, r := range results {
		e.searchFilter[r.ID] = true
		slog.Debug("Search match", "id", r.ID, "title", r.Title)
	}
	slog.Info("Search filter active", "query", e.cfg.SearchQuery, "matches", len(e.searchFilter))
	return nil
}

// ── Discovery ───────────────────────────────────────────────────────────────

func (e *Exporter) discover(ctx context.Context) ([]MeetingRef, error) {
	return e.discoverViaBrowser(ctx)
}

func (e *Exporter) discoverViaBrowser(ctx context.Context) ([]MeetingRef, error) {
	slog.Info("Launching browser")
	b, err := e.lazyBrowser()
	if err != nil {
		return nil, err
	}
	if _, err := b.Login(ctx); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	meetings, err := b.DiscoverMeetings(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	slog.Info("Browser discovery complete", "count", len(meetings))
	return meetings, nil
}

// ── Per-meeting Export ──────────────────────────────────────────────────────

func (e *Exporter) exportOne(ctx context.Context, ref MeetingRef) *ExportResult {
	r := &ExportResult{ID: ref.ID, Title: ref.Title, TranscriptPaths: make(map[string]string)}
	dateStr := dateFromISO(coalesce(ref.Date, time.Now().Format("2006-01-02")))
	r.DateDir = dateStr

	if err := e.storage.EnsureDir(dateStr); err != nil {
		r.Status = "error"
		r.ErrorMsg = err.Error()
		slog.Error("Dir creation failed", "error", err)
		return r
	}

	relBase := filepath.Join(dateStr, sanitize(ref.ID))
	metaRelPath := relBase + ".json"

	if !e.cfg.Overwrite && e.storage.FileExists(metaRelPath) {
		slog.Debug("Already exported, skipping", "id", ref.ID)
		r.Status = "skipped"
		return r
	}

	// Scrape meeting page for transcript, highlights, and extra metadata.
	// Browser operations are serialized via withBrowser to prevent
	// concurrent page navigations when --parallel > 1.
	pageURL := coalesce(ref.URL, meetingURL(ref.ID))
	var scraped *MeetingPageData
	_ = e.withBrowser(func(b *Browser) error {
		data, err := b.ScrapeMeetingPage(ctx, pageURL)
		if err != nil {
			slog.Warn("Meeting page scrape failed, continuing with minimal data", "id", ref.ID, "error", err)
			return nil // non-fatal
		}
		scraped = data
		return nil
	})

	meta := e.buildScrapedMetadata(ref, pageURL, scraped)

	e.writeMetadata(meta, metaRelPath, r)
	e.writeTranscript(scraped, ref.ID, relBase, r)
	e.writeHighlights(scraped, ref.ID, relBase, r)

	transcriptText := ""
	if scraped != nil {
		transcriptText = scraped.Transcript
	}
	if e.cfg.OutputFormat != "" {
		e.writeFormattedMarkdown(meta, transcriptText, relBase, r)
	}
	if !e.cfg.SkipVideo {
		if e.cfg.AudioOnly {
			e.writeAudio(ctx, ref, relBase+".m4a", r)
		} else {
			e.writeVideo(ctx, ref, relBase+".mp4", r)
		}
	}
	if r.Status == "" {
		r.Status = "ok"
	}
	return r
}

func (e *Exporter) writeMetadata(meta *Metadata, relPath string, r *ExportResult) {
	if err := e.storage.WriteJSON(relPath, meta); err != nil {
		slog.Error("Metadata write failed", "error", err)
		return
	}
	r.MetadataPath = relPath
	slog.Debug("Metadata written", "id", meta.ID)
}

// buildScrapedMetadata creates a Metadata struct enriched with browser-scraped
// page data when available, falling back to MeetingRef fields.
func (e *Exporter) buildScrapedMetadata(ref MeetingRef, pageURL string, scraped *MeetingPageData) *Metadata {
	meta := &Metadata{
		ID:    ref.ID,
		Title: coalesce(ref.Title, "Untitled"),
		Links: Links{Grain: pageURL},
	}
	if ref.Date != "" {
		meta.Date = ref.Date
	}

	if scraped == nil {
		return meta
	}

	// Enrich from scraped data.
	if meta.Title == "Untitled" && scraped.Title != "" {
		meta.Title = scraped.Title
	}
	if meta.Date == "" && scraped.Date != "" {
		meta.Date = scraped.Date
	}
	if scraped.Duration != "" {
		meta.DurationSeconds = scraped.Duration
	}
	if len(scraped.Participants) > 0 {
		meta.Participants = scraped.Participants
	}
	if len(scraped.Highlights) > 0 {
		meta.Highlights = scraped.Highlights
	}

	return meta
}

func (e *Exporter) writeTranscript(scraped *MeetingPageData, id, relBase string, r *ExportResult) {
	if scraped == nil || scraped.Transcript == "" {
		return
	}

	relPath := relBase + ".transcript.txt"
	if err := e.storage.WriteFile(relPath, []byte(scraped.Transcript)); err != nil {
		slog.Error("Transcript write failed", "error", err, "id", id)
		return
	}
	r.TranscriptPaths["text"] = relPath
	slog.Info("Transcript exported", "id", id)
}

func (e *Exporter) writeHighlights(scraped *MeetingPageData, id, relBase string, r *ExportResult) {
	if scraped == nil || len(scraped.Highlights) == 0 {
		return
	}

	clips := make([]HighlightClip, len(scraped.Highlights))
	for i, h := range scraped.Highlights {
		clips[i] = normalizeHighlight(h, i)
	}

	relPath := relBase + ".highlights.json"
	if err := e.storage.WriteJSON(relPath, clips); err != nil {
		slog.Error("Highlights write failed", "error", err, "id", id)
		return
	}
	r.HighlightsPath = relPath
	slog.Info("Highlights exported", "id", id, "count", len(clips))
}

func (e *Exporter) writeFormattedMarkdown(meta *Metadata, transcriptText, relBase string, r *ExportResult) {
	md := renderFormattedMarkdown(e.cfg.OutputFormat, meta, transcriptText)
	if md == "" {
		return
	}

	relPath := relBase + ".md"
	if err := e.storage.WriteFile(relPath, []byte(md)); err != nil {
		slog.Error("Markdown write failed", "error", err, "id", meta.ID)
		return
	}
	r.MarkdownPath = relPath
	slog.Debug("Formatted markdown written", "format", e.cfg.OutputFormat, "id", meta.ID)
}

func (e *Exporter) writeVideo(ctx context.Context, ref MeetingRef, relPath string, r *ExportResult) {
	absVideoPath := e.storage.AbsPath(relPath)
	slog.Debug("Downloading video", "id", ref.ID)
	_ = e.withBrowser(func(b *Browser) error {
		method, path := b.DownloadVideo(ctx, coalesce(ref.URL, meetingURL(ref.ID)), absVideoPath)
		r.VideoMethod = method
		resultRelPath := e.relPath(path)
		switch method {
		case "button", "direct":
			r.VideoPath = resultRelPath
			slog.Info("Video downloaded", "method", method, "id", ref.ID)
			e.copyToICloudIfEnabled(resultRelPath)
		case "hls":
			r.VideoPath = resultRelPath
			r.Status = "hls_pending"
			slog.Warn("HLS stream — run convert_hls.sh", "id", ref.ID)
			e.copyToICloudIfEnabled(resultRelPath)
		case "url-saved":
			r.VideoPath = resultRelPath
			slog.Warn("URL saved (manual download needed)", "id", ref.ID)
			e.copyToICloudIfEnabled(resultRelPath)
		default:
			slog.Warn("Video download failed", "id", ref.ID)
		}
		return nil
	})
}

func (e *Exporter) writeAudio(ctx context.Context, ref MeetingRef, relPath string, r *ExportResult) {
	absAudioPath := e.storage.AbsPath(relPath)
	pageURL := coalesce(ref.URL, meetingURL(ref.ID))
	slog.Debug("Finding video source for audio extraction", "id", ref.ID)

	// Find video URL under browser lock, then release for ffmpeg work.
	var videoURL string
	_ = e.withBrowser(func(b *Browser) error {
		videoURL = b.FindVideoSource(ctx, pageURL)
		return nil
	})

	verbose := e.cfg.Verbose
	if videoURL != "" {
		if strings.Contains(videoURL, ".m3u8") {
			// HLS: ffmpeg can extract audio directly from the manifest.
			if err := extractAudio(ctx, videoURL, absAudioPath, verbose); err == nil {
				r.AudioPath = relPath
				r.AudioMethod = "ffmpeg-hls"
				slog.Info("Audio extracted from HLS stream", "id", ref.ID)
				e.copyToICloudIfEnabled(relPath)
				return
			}
			slog.Warn("HLS audio extraction failed, saving URL", "id", ref.ID)
			urlRelPath := strings.TrimSuffix(relPath, ".m4a") + ".m3u8.url"
			if err := e.storage.WriteFile(urlRelPath, []byte(videoURL)); err != nil {
				slog.Error("Failed to write HLS URL file", "error", err)
			}
			r.AudioPath = urlRelPath
			r.AudioMethod = "hls"
			r.Status = "hls_pending"
			return
		}

		// Direct URL: ffmpeg extracts audio from the remote file.
		if err := extractAudio(ctx, videoURL, absAudioPath, verbose); err == nil {
			r.AudioPath = relPath
			r.AudioMethod = "ffmpeg-direct"
			slog.Info("Audio extracted from direct URL", "id", ref.ID)
			e.copyToICloudIfEnabled(relPath)
			return
		}
		slog.Warn("Direct URL audio extraction failed, trying button download", "id", ref.ID)
	}

	// Fallback: download the full video via button (under browser lock), extract audio, then delete.
	tmpVideo := absAudioPath + ".tmp.mp4"
	var btnPath string
	_ = e.withBrowser(func(b *Browser) error {
		btnPath = b.tryDownloadBtn(ctx, tmpVideo)
		return nil
	})
	if btnPath != "" {
		if err := extractAudio(ctx, btnPath, absAudioPath, verbose); err == nil {
			_ = os.Remove(tmpVideo)
			r.AudioPath = relPath
			r.AudioMethod = "ffmpeg-local"
			slog.Info("Audio extracted from downloaded video", "id", ref.ID)
			e.copyToICloudIfEnabled(relPath)
			return
		}
		_ = os.Remove(tmpVideo)
	}

	slog.Warn("Audio extraction failed", "id", ref.ID)
}

// copyToICloudIfEnabled copies a file from the local output directory to
// iCloud Drive when the storage backend supports it. This is used for files
// written externally (e.g., browser video downloads, ffmpeg audio extraction)
// that bypass the storage interface. Non-fatal on failure.
func (e *Exporter) copyToICloudIfEnabled(relPath string) {
	if ic, ok := e.storage.(*ICloudStorage); ok {
		if err := ic.CopyFileToICloud(relPath); err != nil {
			slog.Warn("iCloud copy failed", "path", relPath, "error", err)
		}
	}
}

func (e *Exporter) relPath(abs string) string {
	rel, err := filepath.Rel(e.cfg.OutputDir, abs)
	if err != nil {
		return abs
	}
	return rel
}

func (e *Exporter) lazyBrowser() (*Browser, error) {
	e.browserMu.Lock()
	defer e.browserMu.Unlock()
	return e.getBrowserLocked()
}

// getBrowserLocked initializes the browser if needed.
// Caller must hold e.browserMu.
func (e *Exporter) getBrowserLocked() (*Browser, error) {
	if e.browser != nil {
		return e.browser, nil
	}
	b, err := NewBrowser(e.cfg, e.throttle)
	if err != nil {
		return nil, err
	}
	e.browser = b
	return b, nil
}

// withBrowser serializes all browser operations via browserMu.
// This prevents concurrent page navigations when --parallel > 1,
// since Browser holds a single shared *rod.Page.
func (e *Exporter) withBrowser(fn func(b *Browser) error) error {
	e.browserMu.Lock()
	defer e.browserMu.Unlock()
	b, err := e.getBrowserLocked()
	if err != nil {
		return fmt.Errorf("browser init: %w", err)
	}
	return fn(b)
}
