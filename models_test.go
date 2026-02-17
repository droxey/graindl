package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ── coalesce ────────────────────────────────────────────────────────────────

func TestCoalesce(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"first wins", []string{"a", "b"}, "a"},
		{"skips empty", []string{"", "b", "c"}, "b"},
		{"all empty", []string{"", "", ""}, ""},
		{"single", []string{"x"}, "x"},
		{"none", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := coalesce(tt.in...); got != tt.want {
				t.Errorf("coalesce(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCoalesceSlice(t *testing.T) {
	a := []int{1, 2}
	b := []int{3, 4}

	if got := coalesceSlice(a, b); len(got) != 2 || got[0] != 1 {
		t.Errorf("expected first non-empty slice, got %v", got)
	}
	if got := coalesceSlice([]int{}, b); len(got) != 2 || got[0] != 3 {
		t.Errorf("expected second slice, got %v", got)
	}
	if got := coalesceSlice([]int{}, []int{}); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

// ── firstNonNil ─────────────────────────────────────────────────────────────

func TestFirstNonNil(t *testing.T) {
	if got := firstNonNil(nil, "hello", 42); got != "hello" {
		t.Errorf("expected 'hello', got %v", got)
	}
	if got := firstNonNil(nil, nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	if got := firstNonNil(0, nil); got != 0 {
		t.Errorf("expected 0 (not nil), got %v", got)
	}
}

// ── dateFromISO ─────────────────────────────────────────────────────────────

func TestDateFromISO(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"2025-01-15T10:30:00Z", "2025-01-15"},
		{"2025-01-15", "2025-01-15"},
		{"short", "unknown-date"},
		{"", "unknown-date"},
	}
	for _, tt := range tests {
		if got := dateFromISO(tt.in); got != tt.want {
			t.Errorf("dateFromISO(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ── sanitize ────────────────────────────────────────────────────────────────

func TestSanitize(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"clean", "hello-world", "hello-world"},
		{"slashes", "path/to\\file", "path-to-file"},
		{"special chars", `a?b*c|d"e<f>g`, "a-b-c-d-e-f-g"},
		{"path traversal", "../../etc/passwd", "etc-passwd"},
		{"leading dots", "...hidden", "hidden"},
		{"empty after strip", "///", "unnamed"},
		{"null bytes", string(make([]byte, 300)), "unnamed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitize(tt.in)
			if got != tt.want {
				t.Errorf("sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeTruncates(t *testing.T) {
	long := ""
	for i := 0; i < 250; i++ {
		long += "a"
	}
	got := sanitize(long)
	if len(got) > 200 {
		t.Errorf("sanitize should truncate to 200 chars, got %d", len(got))
	}
}

// ── containsAny ─────────────────────────────────────────────────────────────

func TestContainsAny(t *testing.T) {
	if !containsAny("https://grain.com/login", "login", "signin") {
		t.Error("expected match on 'login'")
	}
	if containsAny("https://grain.com/app/meetings", "login", "signin") {
		t.Error("expected no match")
	}
	if containsAny("anything") {
		t.Error("no subs should return false")
	}
}

// ── meetingURL ──────────────────────────────────────────────────────────────

func TestMeetingURL(t *testing.T) {
	got := meetingURL("abc-123")
	want := "https://grain.com/app/meetings/abc-123"
	if got != want {
		t.Errorf("meetingURL = %q, want %q", got, want)
	}
}

// ── file helpers ────────────────────────────────────────────────────────────

func TestWriteJSONAndFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	// Doesn't exist yet
	if fileExists(path) {
		t.Error("file should not exist yet")
	}

	// Write
	data := map[string]string{"key": "value"}
	if err := writeJSON(path, data); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}

	// Now exists
	if !fileExists(path) {
		t.Error("file should exist after write")
	}

	// SEC-11: verify permissions are 0o600
	info, _ := os.Stat(path)
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("expected 0600, got %04o", perm)
	}

	// Verify content is valid JSON
	raw, _ := os.ReadFile(path)
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if out["key"] != "value" {
		t.Errorf("expected value, got %q", out["key"])
	}
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")

	if err := writeFile(path, []byte("hello")); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600, got %04o", info.Mode().Perm())
	}
}

func TestEnsureDirPermissions(t *testing.T) {
	base := t.TempDir()

	pubDir := filepath.Join(base, "public")
	if err := ensureDir(pubDir); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(pubDir)
	if info.Mode().Perm() != 0o755 {
		t.Errorf("ensureDir: expected 0755, got %04o", info.Mode().Perm())
	}

	privDir := filepath.Join(base, "private")
	if err := ensureDirPrivate(privDir); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(privDir)
	if info.Mode().Perm() != 0o700 {
		t.Errorf("ensureDirPrivate: expected 0700, got %04o", info.Mode().Perm())
	}
}

// ── GrainRecording accessors ────────────────────────────────────────────────

func TestGrainRecordingAccessors(t *testing.T) {
	rec := GrainRecording{
		Title: "", Name: "Fallback Name",
		CreatedAt: "", StartTime: "2025-03-01T10:00:00Z",
		ShareURL: "", PublicURL: "https://share.grain.com/abc",
		Transcript: "", TranscriptText: "Hello world",
		Participants: nil, Attendees: []any{"Alice"},
		Tags: nil, Labels: []any{"tag1"},
		IntelligenceNotes: nil, Notes: "some notes",
	}

	if got := rec.GetTitle(); got != "Fallback Name" {
		t.Errorf("GetTitle = %q, want 'Fallback Name'", got)
	}
	if got := rec.GetDate(); got != "2025-03-01T10:00:00Z" {
		t.Errorf("GetDate = %q", got)
	}
	if got := rec.GetShareURL(); got != "https://share.grain.com/abc" {
		t.Errorf("GetShareURL = %q", got)
	}
	if got := rec.GetTranscript(); got != "Hello world" {
		t.Errorf("GetTranscript = %q", got)
	}
	if got := rec.GetParticipants(); got == nil {
		t.Error("GetParticipants should return Attendees fallback")
	}
	if got := rec.GetTags(); got == nil {
		t.Error("GetTags should return Labels fallback")
	}
	if got := rec.GetNotes(); got != "some notes" {
		t.Errorf("GetNotes = %v", got)
	}
}

func TestGrainRecordingDefaults(t *testing.T) {
	rec := GrainRecording{} // all zero values
	if got := rec.GetTitle(); got != "Untitled" {
		t.Errorf("empty GetTitle = %q, want 'Untitled'", got)
	}
	if got := rec.GetDate(); got != "" {
		t.Errorf("empty GetDate should be empty, got %q", got)
	}
}

// ── RecordingsPage UnmarshalJSON ────────────────────────────────────────────

func TestRecordingsPageUnmarshal(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantN   int
		wantCur string
	}{
		{
			"recordings key",
			`{"recordings":[{"id":"a","title":"A"}],"cursor":"cur1"}`,
			1, "cur1",
		},
		{
			"data key",
			`{"data":[{"id":"b"},{"id":"c"}],"next_cursor":"cur2"}`,
			2, "cur2",
		},
		{
			"items key + camelCase cursor",
			`{"items":[{"id":"d"}],"nextCursor":"cur3"}`,
			1, "cur3",
		},
		{
			"empty",
			`{}`,
			0, "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var page RecordingsPage
			if err := json.Unmarshal([]byte(tt.json), &page); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(page.Recordings) != tt.wantN {
				t.Errorf("recordings count = %d, want %d", len(page.Recordings), tt.wantN)
			}
			if page.Cursor != tt.wantCur {
				t.Errorf("cursor = %q, want %q", page.Cursor, tt.wantCur)
			}
		})
	}
}

// ── buildMetadata / minimalMetadata ─────────────────────────────────────────

func TestBuildMetadata(t *testing.T) {
	rec := &GrainRecording{
		ID: "rec-1", Title: "Sprint Review", CreatedAt: "2025-06-01",
		Duration: 3600.0, VideoURL: "https://cdn.grain.com/v.mp4",
		ShareURL: "https://share.grain.com/rec-1",
	}
	meta := buildMetadata(rec, "https://grain.com/app/meetings/rec-1")

	if meta.ID != "rec-1" {
		t.Errorf("ID = %q", meta.ID)
	}
	if meta.Title != "Sprint Review" {
		t.Errorf("Title = %q", meta.Title)
	}
	if meta.Links.Grain != "https://grain.com/app/meetings/rec-1" {
		t.Errorf("Links.Grain = %q", meta.Links.Grain)
	}
	if meta.Links.Video != "https://cdn.grain.com/v.mp4" {
		t.Errorf("Links.Video = %q", meta.Links.Video)
	}
}

func TestMinimalMetadata(t *testing.T) {
	meta := minimalMetadata("id-1", "Test", "https://grain.com/app/meetings/id-1")
	if meta.ID != "id-1" || meta.Title != "Test" {
		t.Errorf("minimal metadata wrong: %+v", meta)
	}
	if meta.Date != "" || meta.DurationSeconds != nil {
		t.Error("minimal should have no date/duration")
	}
}

// ── parseHighlights ────────────────────────────────────────────────────────

func TestParseHighlightsArray(t *testing.T) {
	input := []any{
		map[string]any{
			"id": "h1", "text": "Great point", "speaker": "Alice",
			"start_time": 10.5, "end_time": 25.0,
		},
		map[string]any{
			"id": "h2", "text": "Action item", "speaker_name": "Bob",
			"timestamp": 60.0, "duration": 15.0,
		},
	}
	highlights := parseHighlights(input)
	if len(highlights) != 2 {
		t.Fatalf("expected 2 highlights, got %d", len(highlights))
	}
	if highlights[0].ID != "h1" {
		t.Errorf("highlights[0].ID = %q, want h1", highlights[0].ID)
	}
	if highlights[0].Text != "Great point" {
		t.Errorf("highlights[0].Text = %q", highlights[0].Text)
	}
	if highlights[1].SpeakerName != "Bob" {
		t.Errorf("highlights[1].SpeakerName = %q", highlights[1].SpeakerName)
	}
}

func TestParseHighlightsSingleObject(t *testing.T) {
	input := map[string]any{
		"id": "solo", "title": "Key insight", "text": "We should pivot",
	}
	highlights := parseHighlights(input)
	if len(highlights) != 1 {
		t.Fatalf("expected 1 highlight, got %d", len(highlights))
	}
	if highlights[0].ID != "solo" {
		t.Errorf("ID = %q", highlights[0].ID)
	}
}

func TestParseHighlightsWrapper(t *testing.T) {
	input := map[string]any{
		"highlights": []any{
			map[string]any{"id": "w1", "text": "Wrapped highlight"},
		},
	}
	highlights := parseHighlights(input)
	if len(highlights) != 1 {
		t.Fatalf("expected 1 highlight from wrapper, got %d", len(highlights))
	}
	if highlights[0].Text != "Wrapped highlight" {
		t.Errorf("Text = %q", highlights[0].Text)
	}
}

func TestParseHighlightsClipsWrapper(t *testing.T) {
	input := map[string]any{
		"clips": []any{
			map[string]any{"id": "c1", "content": "Clip content"},
			map[string]any{"id": "c2", "content": "Another clip"},
		},
	}
	highlights := parseHighlights(input)
	if len(highlights) != 2 {
		t.Fatalf("expected 2 highlights from clips wrapper, got %d", len(highlights))
	}
}

func TestParseHighlightsNil(t *testing.T) {
	if got := parseHighlights(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

func TestParseHighlightsEmptyArray(t *testing.T) {
	if got := parseHighlights([]any{}); got != nil {
		t.Errorf("expected nil for empty array, got %v", got)
	}
}

func TestParseHighlightsNonObject(t *testing.T) {
	// A string or number should not parse as highlights.
	if got := parseHighlights("just a string"); got != nil {
		t.Errorf("expected nil for string input, got %v", got)
	}
}

// ── normalizeHighlight ────────────────────────────────────────────────────

func TestNormalizeHighlightBasic(t *testing.T) {
	h := Highlight{
		ID:        "h1",
		Title:     "Key Decision",
		Text:      "We decided to use Go",
		Speaker:   "Alice",
		StartTime: 120.5,
		EndTime:   135.0,
		URL:       "https://grain.com/highlight/h1",
		Tags:      []any{"decision"},
		CreatedAt: "2025-01-15T10:00:00Z",
	}
	clip := normalizeHighlight(h, 0)

	if clip.ID != "h1" {
		t.Errorf("ID = %q", clip.ID)
	}
	if clip.Title != "Key Decision" {
		t.Errorf("Title = %q", clip.Title)
	}
	if clip.Text != "We decided to use Go" {
		t.Errorf("Text = %q", clip.Text)
	}
	if clip.Speaker != "Alice" {
		t.Errorf("Speaker = %q", clip.Speaker)
	}
	if clip.StartSec != 120.5 {
		t.Errorf("StartSec = %f, want 120.5", clip.StartSec)
	}
	if clip.EndSec != 135.0 {
		t.Errorf("EndSec = %f, want 135.0", clip.EndSec)
	}
	if clip.DurationSec != 14.5 {
		t.Errorf("DurationSec = %f, want 14.5", clip.DurationSec)
	}
	if clip.URL != "https://grain.com/highlight/h1" {
		t.Errorf("URL = %q", clip.URL)
	}
	if clip.CreatedAt != "2025-01-15T10:00:00Z" {
		t.Errorf("CreatedAt = %q", clip.CreatedAt)
	}
}

func TestNormalizeHighlightFallbacks(t *testing.T) {
	h := Highlight{
		Name:        "Fallback Title",
		Content:     "Fallback text",
		SpeakerName: "Bob",
		ShareURL:    "https://share.grain.com/h2",
		Labels:      []any{"label1"},
		Timestamp:   30.0,
		Duration:    10.0,
	}
	clip := normalizeHighlight(h, 5)

	if clip.ID != "highlight-5" {
		t.Errorf("ID should use index fallback, got %q", clip.ID)
	}
	if clip.Title != "Fallback Title" {
		t.Errorf("Title = %q, want Name fallback", clip.Title)
	}
	if clip.Text != "Fallback text" {
		t.Errorf("Text = %q, want Content fallback", clip.Text)
	}
	if clip.Speaker != "Bob" {
		t.Errorf("Speaker = %q, want SpeakerName fallback", clip.Speaker)
	}
	if clip.URL != "https://share.grain.com/h2" {
		t.Errorf("URL = %q, want ShareURL fallback", clip.URL)
	}
	if clip.StartSec != 30.0 {
		t.Errorf("StartSec = %f, want Timestamp fallback 30.0", clip.StartSec)
	}
	if clip.EndSec != 40.0 {
		t.Errorf("EndSec = %f, want inferred 40.0", clip.EndSec)
	}
	if clip.DurationSec != 10.0 {
		t.Errorf("DurationSec = %f", clip.DurationSec)
	}
}

func TestNormalizeHighlightDurationInference(t *testing.T) {
	// Duration inferred from start/end.
	h := Highlight{StartTime: 100.0, EndTime: 130.0}
	clip := normalizeHighlight(h, 0)
	if clip.DurationSec != 30.0 {
		t.Errorf("DurationSec = %f, want 30.0 (inferred)", clip.DurationSec)
	}

	// End inferred from start + duration.
	h2 := Highlight{Timestamp: 50.0, Duration: 20.0}
	clip2 := normalizeHighlight(h2, 0)
	if clip2.EndSec != 70.0 {
		t.Errorf("EndSec = %f, want 70.0 (inferred)", clip2.EndSec)
	}
}

func TestNormalizeHighlightEmptyFields(t *testing.T) {
	h := Highlight{} // all zero
	clip := normalizeHighlight(h, 3)

	if clip.ID != "highlight-3" {
		t.Errorf("ID = %q, want index-based fallback", clip.ID)
	}
	if clip.StartSec != 0 || clip.EndSec != 0 || clip.DurationSec != 0 {
		t.Error("all times should be 0 for empty highlight")
	}
}

// ── toFloat64 ──────────────────────────────────────────────────────────────

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want float64
	}{
		{"float64", float64(42.5), 42.5},
		{"float32", float32(3.14), 3.140000104904175},
		{"int", int(10), 10.0},
		{"int64", int64(100), 100.0},
		{"string", "123.45", 123.45},
		{"bad string", "abc", 0},
		{"nil", nil, 0},
		{"bool", true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toFloat64(tt.in)
			if got != tt.want {
				t.Errorf("toFloat64(%v) = %f, want %f", tt.in, got, tt.want)
			}
		})
	}
}
