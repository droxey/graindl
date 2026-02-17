package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ── Config ──────────────────────────────────────────────────────────────────

type Config struct {
	OutputDir     string
	SessionDir    string
	MaxMeetings   int
	MeetingID     string
	Parallel      int
	DryRun        bool
	SkipVideo     bool
	AudioOnly     bool
	Overwrite     bool
	Headless      bool
	CleanSession  bool
	Verbose       bool
	MinDelaySec   float64
	MaxDelaySec   float64
	SearchQuery   string
	OutputFormat  string // "", "obsidian", "notion"
	Watch         bool
	WatchInterval time.Duration
}

// ── Export Types ─────────────────────────────────────────────────────────────

type MeetingRef struct {
	ID    string
	Title string
	Date  string
	URL   string
}

type ExportResult struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	DateDir         string            `json:"date_dir"`
	Status          string            `json:"status"`
	MetadataPath    string            `json:"metadata_path,omitempty"`
	MarkdownPath    string            `json:"markdown_path,omitempty"`
	TranscriptPaths map[string]string `json:"transcript_paths,omitempty"`
	HighlightsPath  string            `json:"highlights_path,omitempty"`
	VideoPath       string            `json:"video_path,omitempty"`
	VideoMethod     string            `json:"video_method,omitempty"`
	AudioPath       string            `json:"audio_path,omitempty"`
	AudioMethod     string            `json:"audio_method,omitempty"`
	ErrorMsg        string            `json:"error_msg,omitempty"`
}

type ExportManifest struct {
	ExportedAt string          `json:"exported_at"`
	Total      int             `json:"total"`
	OK         int             `json:"ok"`
	Skipped    int             `json:"skipped"`
	Errors     int             `json:"errors"`
	HLSPending int             `json:"hls_pending"`
	Meetings   []*ExportResult `json:"meetings"`
}

// ── Highlight Types ─────────────────────────────────────────────────────────

// Highlight represents a single highlight/clip scraped from Grain.
// Multiple field names are supported because the data shape varies.
type Highlight struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Name       string `json:"name"`
	Text       string `json:"text"`
	Content    string `json:"content"`
	Transcript string `json:"transcript"`
	Timestamp  any    `json:"timestamp"`
	StartTime  any    `json:"start_time"`
	Start      any    `json:"start"`
	EndTime    any    `json:"end_time"`
	End        any    `json:"end"`
	Duration   any    `json:"duration"`
	Speaker    string `json:"speaker"`
	SpeakerName string `json:"speaker_name"`
	URL        string `json:"url"`
	ShareURL   string `json:"share_url"`
	Tags       any    `json:"tags"`
	Labels     any    `json:"labels"`
	CreatedAt  string `json:"created_at"`
}

// HighlightClip is the normalized output format for an individual highlight.
type HighlightClip struct {
	ID          string  `json:"id,omitempty"`
	Title       string  `json:"title,omitempty"`
	Text        string  `json:"text,omitempty"`
	Speaker     string  `json:"speaker,omitempty"`
	StartSec    float64 `json:"start_sec"`
	EndSec      float64 `json:"end_sec"`
	DurationSec float64 `json:"duration_sec"`
	URL         string  `json:"url,omitempty"`
	Tags        any     `json:"tags,omitempty"`
	CreatedAt   string  `json:"created_at,omitempty"`
}

// parseHighlights extracts typed highlights from a raw value.
// Handles: array of objects, single object, or wrapper like {"highlights":[...]}.
func parseHighlights(v any) []Highlight {
	if v == nil {
		return nil
	}

	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}

	// Try direct array.
	var arr []Highlight
	if json.Unmarshal(data, &arr) == nil && len(arr) > 0 {
		return arr
	}

	// Try wrapper object with known list keys (before single-object,
	// because a wrapper like {"title":"Q4","highlights":[...]} would
	// falsely match the single-object check on "title").
	var wrapper struct {
		Highlights []Highlight `json:"highlights"`
		Clips      []Highlight `json:"clips"`
		Items      []Highlight `json:"items"`
	}
	if json.Unmarshal(data, &wrapper) == nil {
		if list := coalesceSlice(wrapper.Highlights, wrapper.Clips, wrapper.Items); len(list) > 0 {
			return list
		}
	}

	// Try single object (after wrapper to avoid misidentification).
	var single Highlight
	if json.Unmarshal(data, &single) == nil && (single.ID != "" || single.Text != "" || single.Title != "" || single.Content != "") {
		return []Highlight{single}
	}

	return nil
}

// normalizeHighlight converts a raw Highlight into a clean HighlightClip.
func normalizeHighlight(h Highlight, index int) HighlightClip {
	id := coalesce(h.ID, fmt.Sprintf("highlight-%d", index))
	text := coalesce(h.Text, h.Content, h.Transcript)
	title := coalesce(h.Title, h.Name)
	speaker := coalesce(h.Speaker, h.SpeakerName)
	hlURL := coalesce(h.URL, h.ShareURL)
	tags := firstNonNil(h.Tags, h.Labels)

	startSec := toFloat64(firstNonNil(h.StartTime, h.Start, h.Timestamp))
	endSec := toFloat64(firstNonNil(h.EndTime, h.End))
	durSec := toFloat64(h.Duration)

	// Infer missing values.
	if durSec == 0 && endSec > startSec {
		durSec = endSec - startSec
	}
	if endSec == 0 && durSec > 0 {
		endSec = startSec + durSec
	}

	return HighlightClip{
		ID:          id,
		Title:       title,
		Text:        text,
		Speaker:     speaker,
		StartSec:    startSec,
		EndSec:      endSec,
		DurationSec: durSec,
		URL:         hlURL,
		Tags:        tags,
		CreatedAt:   h.CreatedAt,
	}
}

// toFloat64 attempts to convert a numeric any value to float64.
func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
}

// ── Output Metadata ─────────────────────────────────────────────────────────

type Metadata struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Date            string `json:"date,omitempty"`
	DurationSeconds any    `json:"duration_seconds,omitempty"`
	Participants    any    `json:"participants,omitempty"`
	Tags            any    `json:"tags,omitempty"`
	Links           Links  `json:"links"`
	AINotes         any    `json:"ai_notes,omitempty"`
	Highlights      any    `json:"highlights,omitempty"`
}

type Links struct {
	Grain string `json:"grain"`
	Share string `json:"share,omitempty"`
	Video string `json:"video,omitempty"`
}

func minimalMetadata(id, title, pageURL string) *Metadata {
	return &Metadata{ID: id, Title: title, Links: Links{Grain: pageURL}}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func coalesce(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func coalesceSlice[T any](slices ...[]T) []T {
	for _, s := range slices {
		if len(s) > 0 {
			return s
		}
	}
	return nil
}

func firstNonNil(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

func dateFromISO(iso string) string {
	if len(iso) < 10 {
		return "unknown-date"
	}
	return iso[:10]
}

var unsafeRe = regexp.MustCompile(`[/\\?%*:|"<>\x00-\x1f\x7f]`)
var multiDash = regexp.MustCompile(`-{2,}`)

func sanitize(s string) string {
	s = unsafeRe.ReplaceAllString(s, "-")
	s = strings.ReplaceAll(s, "..", "")
	s = multiDash.ReplaceAllString(s, "-")
	s = strings.TrimLeft(s, ".-")
	s = strings.TrimRight(s, ".- ")
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200]
	}
	if s == "" {
		s = "unnamed"
	}
	return s
}

func ensureDir(dir string) error        { return os.MkdirAll(dir, 0o755) }
func ensureDirPrivate(dir string) error { return os.MkdirAll(dir, 0o700) }
func fileExists(path string) bool       { _, err := os.Stat(path); return err == nil }
func meetingURL(id string) string       { return "https://grain.com/app/meetings/" + id }

func absPath(rel string) string {
	a, err := filepath.Abs(rel)
	if err != nil {
		return rel
	}
	return a
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
