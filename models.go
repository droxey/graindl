package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ── Config ──────────────────────────────────────────────────────────────────

type Config struct {
	Token        string
	TokenFile    string
	OutputDir    string
	SessionDir   string
	MaxMeetings  int
	SkipVideo    bool
	Overwrite    bool
	Headless     bool
	CleanSession bool
	Verbose      bool
	MinDelaySec  float64
	MaxDelaySec  float64
	SearchQuery  string
}

// ── Grain API Types (GO-3) ──────────────────────────────────────────────────

// GrainUser is the /me response.
type GrainUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// GrainRecording represents a single recording from the API.
// Multiple field names handled via json tags + accessor methods
// because the Grain API has returned different shapes historically.
type GrainRecording struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Name              string `json:"name"`
	CreatedAt         string `json:"created_at"`
	StartTime         string `json:"start_time"`
	Date              string `json:"date"`
	Duration          any    `json:"duration"`
	ShareURL          string `json:"share_url"`
	PublicURL         string `json:"public_url"`
	VideoURL          string `json:"video_url"`
	Transcript        string `json:"transcript"`
	TranscriptText    string `json:"transcript_text"`
	Participants      any    `json:"participants"`
	Attendees         any    `json:"attendees"`
	Speakers          any    `json:"speakers"`
	Tags              any    `json:"tags"`
	Labels            any    `json:"labels"`
	IntelligenceNotes any    `json:"intelligence_notes"`
	Notes             any    `json:"notes"`
	Highlights        any    `json:"highlights"`
}

func (r *GrainRecording) GetTitle() string {
	return coalesce(r.Title, r.Name, "Untitled")
}

func (r *GrainRecording) GetDate() string {
	return coalesce(r.CreatedAt, r.StartTime, r.Date)
}

func (r *GrainRecording) GetShareURL() string {
	return coalesce(r.ShareURL, r.PublicURL)
}

func (r *GrainRecording) GetTranscript() string {
	return coalesce(r.Transcript, r.TranscriptText)
}

func (r *GrainRecording) GetParticipants() any {
	return firstNonNil(r.Participants, r.Attendees, r.Speakers)
}

func (r *GrainRecording) GetTags() any {
	return firstNonNil(r.Tags, r.Labels)
}

func (r *GrainRecording) GetNotes() any {
	return firstNonNil(r.IntelligenceNotes, r.Notes)
}

// RecordingsPage handles the paginated list response.
// Custom unmarshal because the list key and cursor key vary.
type RecordingsPage struct {
	Recordings []GrainRecording
	Cursor     string
}

func (p *RecordingsPage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Recordings    []GrainRecording `json:"recordings"`
		Data          []GrainRecording `json:"data"`
		Items         []GrainRecording `json:"items"`
		Cursor        string           `json:"cursor"`
		NextCursor    string           `json:"next_cursor"`
		NextCursorAlt string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Recordings = coalesceSlice(raw.Recordings, raw.Data, raw.Items)
	p.Cursor = coalesce(raw.Cursor, raw.NextCursor, raw.NextCursorAlt)
	return nil
}

// ── Export Types ─────────────────────────────────────────────────────────────

type MeetingRef struct {
	ID      string
	Title   string
	Date    string
	URL     string
	APIData *GrainRecording // typed, not map[string]any
}

type ExportResult struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	DateDir         string            `json:"date_dir"`
	Status          string            `json:"status"`
	MetadataPath    string            `json:"metadata_path,omitempty"`
	TranscriptPaths map[string]string `json:"transcript_paths,omitempty"`
	VideoPath       string            `json:"video_path,omitempty"`
	VideoMethod     string            `json:"video_method,omitempty"`
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

func buildMetadata(rec *GrainRecording, pageURL string) *Metadata {
	return &Metadata{
		ID:              rec.ID,
		Title:           rec.GetTitle(),
		Date:            rec.GetDate(),
		DurationSeconds: rec.Duration,
		Participants:    rec.GetParticipants(),
		Tags:            rec.GetTags(),
		Links: Links{
			Grain: pageURL,
			Share: rec.GetShareURL(),
			Video: rec.VideoURL,
		},
		AINotes:    rec.GetNotes(),
		Highlights: rec.Highlights,
	}
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
