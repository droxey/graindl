package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

type Exporter struct {
	scraper      *Scraper
	browser      *Browser
	browserMu    sync.Mutex
	cfg          *Config
	throttle     *Throttle
	manifest     *ExportManifest
	searchFilter map[string]bool // nil = export all, non-nil = only matched IDs
}

func NewExporter(cfg *Config) (*Exporter, error) {
	scraper, err := NewScraper(cfg)
	if err != nil {
		return nil, fmt.Errorf("scraper init: %w", err)
	}
	return &Exporter{
		scraper: scraper,
		cfg:     cfg,
		throttle: &Throttle{
			Min: time.Duration(cfg.MinDelaySec * float64(time.Second)),
			Max: time.Duration(cfg.MaxDelaySec * float64(time.Second)),
		},
		manifest: &ExportManifest{ExportedAt: time.Now().UTC().Format(time.RFC3339)},
	}, nil
}

func (e *Exporter) Run(ctx context.Context) error {
	if err := ensureDir(e.cfg.OutputDir); err != nil {
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

	if err := writeJSON(filepath.Join(e.cfg.OutputDir, "_export-manifest.json"), e.manifest); err != nil {
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
// Each worker independently calls exportOne (which makes its own API calls
// and writes to per-meeting files). Results are collected via a channel
// so that manifest updates happen in a single goroutine (no mutex needed).
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

	// Try to enrich from API.
	if e.cfg.Token != "" {
		if rec, err := e.scraper.GetRecording(ctx, id, "text"); err == nil {
			ref.Title = rec.GetTitle()
			ref.Date = rec.GetDate()
			ref.APIData = rec
		} else {
			slog.Warn("API fetch failed, continuing with ID only", "id", id, "error", err)
		}
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

	if err := writeJSON(filepath.Join(e.cfg.OutputDir, "_export-manifest.json"), e.manifest); err != nil {
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
	if e.cfg.Token != "" {
		slog.Debug("Validating API token")
		if user, err := e.scraper.Me(ctx); err == nil {
			slog.Info("API authenticated", "user", coalesce(user.Name, user.Email, "unknown"))
			recs, err := e.scraper.ListRecordings(ctx)
			if err == nil && len(recs) > 0 {
				slog.Info("API recordings found", "count", len(recs))
				return refsFromAPI(recs), nil
			}
			slog.Warn("API list failed, falling back to browser", "error", err)
		} else {
			slog.Warn("API auth failed, falling back to browser", "error", err)
		}
	}
	return e.discoverViaBrowser(ctx)
}

func (e *Exporter) discoverViaBrowser(ctx context.Context) ([]MeetingRef, error) {
	slog.Info("Launching browser")
	b, err := e.lazyBrowser()
	if err != nil {
		return nil, err
	}
	cookies, err := b.Login(ctx)
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	e.scraper.InjectCookies(cookies)
	meetings, err := b.DiscoverMeetings(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover: %w", err)
	}
	slog.Info("Browser discovery complete", "count", len(meetings))
	return meetings, nil
}

func refsFromAPI(recs []GrainRecording) []MeetingRef {
	refs := make([]MeetingRef, 0, len(recs))
	for i := range recs {
		r := &recs[i]
		refs = append(refs, MeetingRef{
			ID:      r.ID,
			Title:   r.GetTitle(),
			Date:    r.GetDate(),
			URL:     meetingURL(r.ID),
			APIData: r,
		})
	}
	return refs
}

// ── Per-meeting Export ──────────────────────────────────────────────────────

func (e *Exporter) exportOne(ctx context.Context, ref MeetingRef) *ExportResult {
	r := &ExportResult{ID: ref.ID, Title: ref.Title, TranscriptPaths: make(map[string]string)}
	dateStr := dateFromISO(coalesce(ref.Date, time.Now().Format("2006-01-02")))
	r.DateDir = dateStr
	dir := filepath.Join(e.cfg.OutputDir, dateStr)
	if err := ensureDir(dir); err != nil {
		r.Status = "error"
		r.ErrorMsg = err.Error()
		slog.Error("Dir creation failed", "error", err)
		return r
	}
	base := filepath.Join(dir, sanitize(ref.ID))
	metaPath, videoPath := base+".json", base+".mp4"

	if !e.cfg.Overwrite && fileExists(metaPath) {
		slog.Debug("Already exported, skipping", "id", ref.ID)
		r.Status = "skipped"
		return r
	}

	// PERF-1: Fetch recording once, reuse for metadata + text transcript.
	// Only make additional API calls for vtt/json transcript formats.
	rec := ref.APIData
	if rec == nil && e.cfg.Token != "" {
		if fetched, err := e.scraper.GetRecording(ctx, ref.ID, "text"); err == nil {
			rec = fetched
		}
	}

	e.writeHighlights(rec, ref.ID, base, r)
	var meta *Metadata
	if rec != nil {
		meta = buildMetadata(rec, ref.URL)
	} else {
		meta = minimalMetadata(ref.ID, ref.Title, ref.URL)
	}

	e.writeMetadata(meta, metaPath, r)
	e.writeTranscripts(ctx, rec, ref.ID, base, r)
	if e.cfg.OutputFormat != "" {
		// Use the cached transcript text if available to avoid re-reading from disk.
		var transcriptText string
		if rec != nil {
			transcriptText = rec.GetTranscript()
		}
		e.writeFormattedMarkdown(meta, transcriptText, base, r)
	}
	if !e.cfg.SkipVideo {
		if e.cfg.AudioOnly {
			audioPath := base + ".m4a"
			e.writeAudio(ctx, ref, audioPath, r)
		} else {
			e.writeVideo(ctx, ref, videoPath, r)
		}
	}
	if r.Status == "" {
		r.Status = "ok"
	}
	return r
}

func (e *Exporter) writeMetadata(meta *Metadata, path string, r *ExportResult) {
	if err := writeJSON(path, meta); err != nil {
		slog.Error("Metadata write failed", "error", err)
		return
	}
	r.MetadataPath = e.relPath(path)
	slog.Debug("Metadata written", "id", meta.ID)
}

func (e *Exporter) writeHighlights(rec *GrainRecording, id, base string, r *ExportResult) {
	if rec == nil {
		return
	}
	highlights := parseHighlights(rec.Highlights)
	if len(highlights) == 0 {
		return
	}

	clips := make([]HighlightClip, len(highlights))
	for i, h := range highlights {
		clips[i] = normalizeHighlight(h, i)
	}

	p := base + ".highlights.json"
	if err := writeJSON(p, clips); err != nil {
		slog.Error("Highlights write failed", "error", err, "id", id)
		return
	}
	r.HighlightsPath = e.relPath(p)
	slog.Info("Highlights exported", "id", id, "count", len(clips))
}

func (e *Exporter) writeTranscripts(ctx context.Context, rec *GrainRecording, id, base string, r *ExportResult) {
	if e.cfg.Token == "" {
		return
	}

	// Write text transcript from the already-fetched recording if available.
	if rec != nil {
		if text := rec.GetTranscript(); text != "" {
			p := base + ".transcript.txt"
			if writeFile(p, []byte(text)) == nil {
				r.TranscriptPaths["text"] = e.relPath(p)
				slog.Debug("Transcript written", "format", "text", "id", id, "source", "cached")
			}
		}
	}

	// Fetch remaining formats that require a separate API call.
	for fk, ext := range map[string]string{"vtt": ".transcript.vtt", "json": ".transcript.json"} {
		if err := ctx.Err(); err != nil {
			return
		}
		// Also fetch text via API if the cached recording didn't have it.
		if fk == "text" {
			continue // handled above
		}
		text, err := e.scraper.GetTranscriptRaw(ctx, id, fk)
		if err != nil || text == "" {
			continue
		}
		p := base + ext
		if writeFile(p, []byte(text)) == nil {
			r.TranscriptPaths[fk] = e.relPath(p)
			slog.Debug("Transcript written", "format", fk, "id", id)
		}
	}

	// If text wasn't available from cache, try fetching it.
	if _, ok := r.TranscriptPaths["text"]; !ok {
		if err := ctx.Err(); err != nil {
			return
		}
		text, err := e.scraper.GetTranscriptRaw(ctx, id, "text")
		if err == nil && text != "" {
			p := base + ".transcript.txt"
			if writeFile(p, []byte(text)) == nil {
				r.TranscriptPaths["text"] = e.relPath(p)
				slog.Debug("Transcript written", "format", "text", "id", id)
			}
		}
	}
}

func (e *Exporter) writeFormattedMarkdown(meta *Metadata, transcriptText, base string, r *ExportResult) {
	md := renderFormattedMarkdown(e.cfg.OutputFormat, meta, transcriptText)
	if md == "" {
		return
	}

	p := base + ".md"
	if writeFile(p, []byte(md)) == nil {
		r.MarkdownPath = e.relPath(p)
		slog.Debug("Formatted markdown written", "format", e.cfg.OutputFormat, "id", meta.ID)
	}
}

func (e *Exporter) writeVideo(ctx context.Context, ref MeetingRef, videoPath string, r *ExportResult) {
	b, err := e.lazyBrowser()
	if err != nil {
		slog.Error("Browser init failed", "error", err)
		return
	}
	slog.Debug("Downloading video", "id", ref.ID)
	method, path := b.DownloadVideo(ctx, coalesce(ref.URL, meetingURL(ref.ID)), videoPath)
	r.VideoMethod = method
	switch method {
	case "button", "direct":
		r.VideoPath = e.relPath(path)
		slog.Info("Video downloaded", "method", method, "id", ref.ID)
	case "hls":
		r.VideoPath = e.relPath(path)
		r.Status = "hls_pending"
		slog.Warn("HLS stream — run convert_hls.sh", "id", ref.ID)
	case "url-saved":
		r.VideoPath = e.relPath(path)
		slog.Warn("URL saved (manual download needed)", "id", ref.ID)
	default:
		slog.Warn("Video download failed", "id", ref.ID)
	}
}

func (e *Exporter) writeAudio(ctx context.Context, ref MeetingRef, audioPath string, r *ExportResult) {
	b, err := e.lazyBrowser()
	if err != nil {
		slog.Error("Browser init failed", "error", err)
		return
	}

	pageURL := coalesce(ref.URL, meetingURL(ref.ID))
	slog.Debug("Finding video source for audio extraction", "id", ref.ID)

	// Try to find a video URL without downloading — lets ffmpeg stream
	// just the audio track, saving bandwidth and disk.
	verbose := e.cfg.Verbose
	videoURL := b.FindVideoSource(ctx, pageURL)
	if videoURL != "" {
		if strings.Contains(videoURL, ".m3u8") {
			// HLS: ffmpeg can extract audio directly from the manifest.
			if err := extractAudio(ctx, videoURL, audioPath, verbose); err == nil {
				r.AudioPath = e.relPath(audioPath)
				r.AudioMethod = "ffmpeg-hls"
				slog.Info("Audio extracted from HLS stream", "id", ref.ID)
				return
			}
			slog.Warn("HLS audio extraction failed, saving URL", "id", ref.ID)
			p := strings.TrimSuffix(audioPath, ".m4a") + ".m3u8.url"
			_ = writeFile(p, []byte(videoURL))
			r.AudioPath = e.relPath(p)
			r.AudioMethod = "hls"
			r.Status = "hls_pending"
			return
		}

		// Direct URL: ffmpeg extracts audio from the remote file.
		if err := extractAudio(ctx, videoURL, audioPath, verbose); err == nil {
			r.AudioPath = e.relPath(audioPath)
			r.AudioMethod = "ffmpeg-direct"
			slog.Info("Audio extracted from direct URL", "id", ref.ID)
			return
		}
		slog.Warn("Direct URL audio extraction failed, trying button download", "id", ref.ID)
	}

	// Fallback: download the full video via button, extract audio, then delete.
	tmpVideo := audioPath + ".tmp.mp4"
	if p := b.tryDownloadBtn(ctx, tmpVideo); p != "" {
		if err := extractAudio(ctx, p, audioPath, verbose); err == nil {
			_ = os.Remove(tmpVideo)
			r.AudioPath = e.relPath(audioPath)
			r.AudioMethod = "ffmpeg-local"
			slog.Info("Audio extracted from downloaded video", "id", ref.ID)
			return
		}
		_ = os.Remove(tmpVideo)
	}

	slog.Warn("Audio extraction failed", "id", ref.ID)
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
