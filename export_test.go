package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── refsFromAPI ─────────────────────────────────────────────────────────────

func TestRefsFromAPI(t *testing.T) {
	recs := []GrainRecording{
		{ID: "r1", Title: "First", CreatedAt: "2025-01-15T10:00:00Z"},
		{ID: "r2", Name: "Second (name fallback)", StartTime: "2025-02-20T14:00:00Z"},
		{ID: "r3"}, // minimal
	}

	refs := refsFromAPI(recs)
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %d", len(refs))
	}

	// First: normal fields
	if refs[0].Title != "First" {
		t.Errorf("refs[0].Title = %q", refs[0].Title)
	}
	if refs[0].Date != "2025-01-15T10:00:00Z" {
		t.Errorf("refs[0].Date = %q", refs[0].Date)
	}
	if refs[0].URL != "https://grain.com/app/meetings/r1" {
		t.Errorf("refs[0].URL = %q", refs[0].URL)
	}

	// Second: name fallback
	if refs[1].Title != "Second (name fallback)" {
		t.Errorf("refs[1].Title = %q, want name fallback", refs[1].Title)
	}

	// Third: minimal defaults
	if refs[2].Title != "Untitled" {
		t.Errorf("refs[2].Title = %q, want Untitled", refs[2].Title)
	}
	if refs[2].APIData == nil {
		t.Error("refs[2].APIData should not be nil")
	}
}

// ── relPath ─────────────────────────────────────────────────────────────────

func TestRelPath(t *testing.T) {
	e := &Exporter{cfg: &Config{OutputDir: "/data/recordings"}}

	got := e.relPath("/data/recordings/2025-01-15/abc.json")
	if got != "2025-01-15/abc.json" {
		t.Errorf("relPath = %q, want relative", got)
	}

	// If rel fails (different root), returns absolute
	got = e.relPath("/other/path/file.json")
	if got == "" {
		t.Error("relPath should return something even on failure")
	}
}

// ── exportOne integration ───────────────────────────────────────────────────

func TestExportOneWithAPI(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(GrainRecording{
			ID: "test-id", Title: "Test Meeting", CreatedAt: "2025-06-01T10:00:00Z",
			Transcript:     "Hello world transcript",
			TranscriptText: "Hello world transcript",
		})
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL

	ref := MeetingRef{
		ID:    "test-id",
		Title: "Test Meeting",
		Date:  "2025-06-01T10:00:00Z",
		URL:   "https://grain.com/app/meetings/test-id",
		APIData: &GrainRecording{
			ID: "test-id", Title: "Test Meeting", CreatedAt: "2025-06-01T10:00:00Z",
		},
	}

	r := e.exportOne(context.Background(), ref)

	if r.Status != "ok" {
		t.Errorf("status = %q, want ok (error: %s)", r.Status, r.ErrorMsg)
	}

	// Metadata file should exist
	metaPath := filepath.Join(dir, r.MetadataPath)
	if !fileExists(metaPath) {
		t.Errorf("metadata file missing: %s", metaPath)
	}

	// Verify metadata content
	raw, _ := os.ReadFile(metaPath)
	var meta Metadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("metadata unmarshal: %v", err)
	}
	if meta.ID != "test-id" {
		t.Errorf("meta.ID = %q", meta.ID)
	}

	// SEC-8: paths should be relative
	if filepath.IsAbs(r.MetadataPath) {
		t.Errorf("MetadataPath should be relative, got %q", r.MetadataPath)
	}

	// SEC-11: file should be 0o600
	info, _ := os.Stat(metaPath)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("metadata perms = %04o, want 0600", info.Mode().Perm())
	}
}

func TestExportOneSkipExisting(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:   dir,
		SkipVideo:   true,
		Overwrite:   false,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}

	ref := MeetingRef{ID: "existing", Title: "Old", Date: "2025-01-01"}

	// Pre-create the metadata file
	dateDir := filepath.Join(dir, "2025-01-01")
	os.MkdirAll(dateDir, 0o755)
	os.WriteFile(filepath.Join(dateDir, "existing.json"), []byte("{}"), 0o600)

	r := e.exportOne(context.Background(), ref)
	if r.Status != "skipped" {
		t.Errorf("status = %q, want skipped", r.Status)
	}
}

func TestExportOneOverwrite(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(GrainRecording{ID: "ow-id", Title: "Overwritten"})
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		SkipVideo:   true,
		Overwrite:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL

	ref := MeetingRef{
		ID: "ow-id", Title: "Overwritten", Date: "2025-01-01",
		APIData: &GrainRecording{ID: "ow-id", Title: "Overwritten"},
	}

	// Pre-create
	dateDir := filepath.Join(dir, "2025-01-01")
	os.MkdirAll(dateDir, 0o755)
	os.WriteFile(filepath.Join(dateDir, "ow-id.json"), []byte("{}"), 0o600)

	r := e.exportOne(context.Background(), ref)
	if r.Status != "ok" {
		t.Errorf("overwrite status = %q, want ok", r.Status)
	}
}

// ── runSingle (--id flag) ────────────────────────────────────────────────────

func TestRunSingleMeeting(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The scraper fetches the recording to enrich the ref, then again for transcripts.
		json.NewEncoder(w).Encode(GrainRecording{
			ID: "single-id", Title: "Solo Meeting", CreatedAt: "2025-08-10T09:00:00Z",
			Transcript:     "This is the transcript",
			TranscriptText: "This is the transcript",
		})
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		MeetingID:   "single-id",
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run with --id: %v", err)
	}

	// Manifest should exist with exactly 1 meeting.
	manifestPath := filepath.Join(dir, "_export-manifest.json")
	if !fileExists(manifestPath) {
		t.Fatal("manifest missing")
	}
	raw, _ := os.ReadFile(manifestPath)
	var m ExportManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if m.Total != 1 {
		t.Errorf("manifest.Total = %d, want 1", m.Total)
	}
	if m.OK != 1 {
		t.Errorf("manifest.OK = %d, want 1", m.OK)
	}
	if len(m.Meetings) != 1 {
		t.Fatalf("manifest.Meetings length = %d, want 1", len(m.Meetings))
	}
	if m.Meetings[0].ID != "single-id" {
		t.Errorf("meeting ID = %q, want single-id", m.Meetings[0].ID)
	}
	if m.Meetings[0].Status != "ok" {
		t.Errorf("meeting status = %q, want ok", m.Meetings[0].Status)
	}

	// Metadata file should exist with correct content.
	metaPath := filepath.Join(dir, m.Meetings[0].MetadataPath)
	if !fileExists(metaPath) {
		t.Fatalf("metadata file missing: %s", metaPath)
	}
	metaRaw, _ := os.ReadFile(metaPath)
	var meta Metadata
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("metadata unmarshal: %v", err)
	}
	if meta.Title != "Solo Meeting" {
		t.Errorf("meta.Title = %q, want 'Solo Meeting'", meta.Title)
	}

	// Paths should be relative (SEC-8).
	if filepath.IsAbs(m.Meetings[0].MetadataPath) {
		t.Errorf("MetadataPath should be relative, got %q", m.Meetings[0].MetadataPath)
	}
}

func TestRunSingleMeetingInvalidID(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:   dir,
		MeetingID:   "../etc/passwd",
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer e.Close()

	err = e.Run(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid meeting ID")
	}
	if !strings.Contains(err.Error(), "invalid meeting ID") {
		t.Errorf("error = %q, want 'invalid meeting ID'", err.Error())
	}
}

func TestRunSingleMeetingNoToken(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:   dir,
		MeetingID:   "valid-meeting-id",
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer e.Close()

	// Should not error — falls back to minimal metadata with no API.
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run with --id (no token): %v", err)
	}

	manifestPath := filepath.Join(dir, "_export-manifest.json")
	raw, _ := os.ReadFile(manifestPath)
	var m ExportManifest
	json.Unmarshal(raw, &m)
	if m.Total != 1 {
		t.Errorf("manifest.Total = %d, want 1", m.Total)
	}
	if m.OK != 1 {
		t.Errorf("manifest.OK = %d, want 1", m.OK)
	}
}

func TestRunSingleMeetingAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("server error"))
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		MeetingID:   "some-meeting-id",
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	// API error should not be fatal — falls back to minimal export.
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run should not fail on API error: %v", err)
	}

	manifestPath := filepath.Join(dir, "_export-manifest.json")
	raw, _ := os.ReadFile(manifestPath)
	var m ExportManifest
	json.Unmarshal(raw, &m)
	if m.Total != 1 {
		t.Errorf("manifest.Total = %d, want 1", m.Total)
	}
	// Meeting should still be exported (with minimal metadata).
	if m.OK != 1 {
		t.Errorf("manifest.OK = %d, want 1", m.OK)
	}
}

func TestRunSingleMeetingSkipsExisting(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(GrainRecording{
			ID: "existing-id", Title: "Existing", CreatedAt: "2025-05-01",
		})
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		MeetingID:   "existing-id",
		SkipVideo:   true,
		Overwrite:   false,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	// Pre-create the metadata file at the date-based path so it gets skipped.
	dateDir := filepath.Join(dir, "2025-05-01")
	os.MkdirAll(dateDir, 0o755)
	os.WriteFile(filepath.Join(dateDir, "existing-id.json"), []byte("{}"), 0o600)

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	manifestPath := filepath.Join(dir, "_export-manifest.json")
	raw, _ := os.ReadFile(manifestPath)
	var m ExportManifest
	json.Unmarshal(raw, &m)
	if m.Skipped != 1 {
		t.Errorf("manifest.Skipped = %d, want 1", m.Skipped)
	}
}

func TestRunSingleMeetingOverwrite(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(GrainRecording{
			ID: "ow-single", Title: "Overwrite Single", CreatedAt: "2025-04-01",
		})
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		MeetingID:   "ow-single",
		SkipVideo:   true,
		Overwrite:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	// Pre-create the file.
	dateDir := filepath.Join(dir, "2025-04-01")
	os.MkdirAll(dateDir, 0o755)
	os.WriteFile(filepath.Join(dateDir, "ow-single.json"), []byte(`{"old": true}`), 0o600)

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	manifestPath := filepath.Join(dir, "_export-manifest.json")
	raw, _ := os.ReadFile(manifestPath)
	var m ExportManifest
	json.Unmarshal(raw, &m)
	if m.OK != 1 {
		t.Errorf("manifest.OK = %d, want 1", m.OK)
	}

	// Verify file was overwritten with new content.
	metaRaw, _ := os.ReadFile(filepath.Join(dateDir, "ow-single.json"))
	var meta Metadata
	json.Unmarshal(metaRaw, &meta)
	if meta.Title != "Overwrite Single" {
		t.Errorf("meta.Title = %q, expected overwritten content", meta.Title)
	}
}

func TestRunSingleMeetingCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slow server to allow cancellation to happen first.
		json.NewEncoder(w).Encode(GrainRecording{ID: "cancel-id"})
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		MeetingID:   "cancel-id",
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// Should not panic or hang.
	_ = e.Run(ctx)
}

func TestRunSingleDoesNotCallDiscover(t *testing.T) {
	apiCalls := make(map[string]int)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls[r.URL.Path]++
		json.NewEncoder(w).Encode(GrainRecording{
			ID: "direct-id", Title: "Direct", CreatedAt: "2025-09-01",
		})
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		MeetingID:   "direct-id",
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// --id mode should NOT call /me or /recordings (discovery endpoints).
	if apiCalls["/me"] > 0 {
		t.Errorf("/me was called %d times, should be 0 in --id mode", apiCalls["/me"])
	}
	if apiCalls["/recordings"] > 0 {
		t.Errorf("/recordings was called %d times, should be 0 in --id mode", apiCalls["/recordings"])
	}

	// Should only call /recordings/<id> for the single meeting.
	recPath := "/recordings/direct-id"
	if apiCalls[recPath] == 0 {
		t.Errorf("expected at least one call to %s", recPath)
	}
}

// ── Full Run (API-only, skip video) ─────────────────────────────────────────

func TestRunAPIDriven(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch {
		case r.URL.Path == "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case r.URL.Path == "/recordings" && r.URL.Query().Get("cursor") == "":
			w.Write([]byte(`{"recordings":[ {"id":"m1","title":"Meeting 1","created_at":"2025-03-01"}, {"id":"m2","title":"Meeting 2","created_at":"2025-03-02"} ]}`))
		default:
			// Individual recording fetches (for transcripts)
			json.NewEncoder(w).Encode(GrainRecording{
				ID: "m1", Transcript: "transcript content",
			})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		SkipVideo:   true,
		MaxMeetings: 2,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Manifest should exist
	manifestPath := filepath.Join(dir, "_export-manifest.json")
	if !fileExists(manifestPath) {
		t.Fatal("manifest missing")
	}

	raw, _ := os.ReadFile(manifestPath)
	var m ExportManifest
	json.Unmarshal(raw, &m)
	if m.Total != 2 {
		t.Errorf("manifest.Total = %d, want 2", m.Total)
	}
	if m.OK != 2 {
		t.Errorf("manifest.OK = %d, want 2", m.OK)
	}
}

// ── Dry-run tests ────────────────────────────────────────────────────────────

func TestDryRunAPIDriven(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case r.URL.Path == "/recordings":
			w.Write([]byte(`{"recordings":[
				{"id":"m1","title":"Meeting 1","created_at":"2025-03-01"},
				{"id":"m2","title":"Meeting 2","created_at":"2025-03-02"},
				{"id":"m3","title":"Meeting 3","created_at":"2025-03-03"}
			]}`))
		default:
			json.NewEncoder(w).Encode(GrainRecording{ID: "m1"})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		DryRun:      true,
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run --dry-run: %v", err)
	}

	// No manifest should be written in dry-run mode.
	manifestPath := filepath.Join(dir, "_export-manifest.json")
	if fileExists(manifestPath) {
		t.Error("manifest should NOT exist in dry-run mode")
	}

	// No date directories or metadata files should be created.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("unexpected directory in output: %s", e.Name())
		}
	}
}

func TestDryRunNoFiles(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case r.URL.Path == "/recordings":
			w.Write([]byte(`{"recordings":[{"id":"x1","title":"X","created_at":"2025-01-01"}]}`))
		default:
			json.NewEncoder(w).Encode(GrainRecording{ID: "x1", Transcript: "hello"})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		DryRun:      true,
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// No metadata, transcript, or video files should exist.
	matches, _ := filepath.Glob(filepath.Join(dir, "**", "*.json"))
	if len(matches) > 0 {
		t.Errorf("expected no JSON files, got %v", matches)
	}
	matches, _ = filepath.Glob(filepath.Join(dir, "**", "*.txt"))
	if len(matches) > 0 {
		t.Errorf("expected no transcript files, got %v", matches)
	}
}

func TestDryRunWithMaxMeetings(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case r.URL.Path == "/recordings":
			w.Write([]byte(`{"recordings":[
				{"id":"m1","title":"Meeting 1","created_at":"2025-03-01"},
				{"id":"m2","title":"Meeting 2","created_at":"2025-03-02"},
				{"id":"m3","title":"Meeting 3","created_at":"2025-03-03"}
			]}`))
		default:
			json.NewEncoder(w).Encode(GrainRecording{ID: "m1"})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		DryRun:      true,
		MaxMeetings: 2,
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	// Capture stdout to verify the listing is limited.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := e.Run(context.Background()); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("Run: %v", err)
	}
	w.Close()
	os.Stdout = old

	out, _ := io.ReadAll(r)
	output := string(out)

	// Should contain m1 and m2 but not m3 (capped at --max 2).
	if !strings.Contains(output, "m1") {
		t.Errorf("expected m1 in output, got:\n%s", output)
	}
	if !strings.Contains(output, "m2") {
		t.Errorf("expected m2 in output, got:\n%s", output)
	}
	if strings.Contains(output, "m3") {
		t.Errorf("m3 should not appear (--max 2), got:\n%s", output)
	}
}

func TestDryRunSingleMeeting(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(GrainRecording{
			ID: "single-dry", Title: "Dry Single", CreatedAt: "2025-07-04T12:00:00Z",
		})
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		MeetingID:   "single-dry",
		DryRun:      true,
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err := e.Run(context.Background()); err != nil {
		w.Close()
		os.Stdout = old
		t.Fatalf("Run --id --dry-run: %v", err)
	}
	w.Close()
	os.Stdout = old

	out, _ := io.ReadAll(r)
	output := string(out)

	if !strings.Contains(output, "single-dry") {
		t.Errorf("expected meeting ID in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Dry Single") {
		t.Errorf("expected meeting title in output, got:\n%s", output)
	}

	// No files should be written.
	manifestPath := filepath.Join(dir, "_export-manifest.json")
	if fileExists(manifestPath) {
		t.Error("manifest should NOT exist in dry-run mode")
	}
}

func TestDryRunSingleMeetingInvalidID(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:   dir,
		MeetingID:   "../etc/passwd",
		DryRun:      true,
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer e.Close()

	err = e.Run(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid meeting ID")
	}
	if !strings.Contains(err.Error(), "invalid meeting ID") {
		t.Errorf("error = %q, want 'invalid meeting ID'", err.Error())
	}
}

func TestDryRunPrintDryRunOutput(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{OutputDir: dir, MinDelaySec: 0, MaxDelaySec: 0.01}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}

	meetings := []MeetingRef{
		{ID: "aaa", Title: "Alpha Meeting", Date: "2025-01-10T09:00:00Z"},
		{ID: "bbb", Title: "", Date: "2025-02-20"},
		{ID: "ccc", Title: "Gamma", Date: ""},
	}

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	e.printDryRun(meetings)

	w.Close()
	os.Stdout = old

	out, _ := io.ReadAll(r)
	output := string(out)

	// Header row.
	if !strings.Contains(output, "ID") || !strings.Contains(output, "TITLE") {
		t.Errorf("missing table header, got:\n%s", output)
	}
	// Row data.
	if !strings.Contains(output, "aaa") || !strings.Contains(output, "Alpha Meeting") {
		t.Errorf("missing meeting aaa, got:\n%s", output)
	}
	// Untitled fallback.
	if !strings.Contains(output, "(untitled)") {
		t.Errorf("expected (untitled) for meeting with no title, got:\n%s", output)
	}
	// Unknown date fallback.
	if !strings.Contains(output, "unknown-date") {
		t.Errorf("expected unknown-date for meeting with no date, got:\n%s", output)
	}
}

// ── Parallel export ──────────────────────────────────────────────────────────

func TestParallelExportBasic(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case r.URL.Path == "/recordings" && r.URL.Query().Get("cursor") == "":
			w.Write([]byte(`{"recordings":[
				{"id":"p1","title":"Parallel 1","created_at":"2025-03-01"},
				{"id":"p2","title":"Parallel 2","created_at":"2025-03-02"},
				{"id":"p3","title":"Parallel 3","created_at":"2025-03-03"},
				{"id":"p4","title":"Parallel 4","created_at":"2025-03-04"}
			]}`))
		default:
			json.NewEncoder(w).Encode(GrainRecording{
				ID: "p1", Transcript: "transcript",
			})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		SkipVideo:   true,
		Parallel:    3,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run --parallel 3: %v", err)
	}

	manifestPath := filepath.Join(dir, "_export-manifest.json")
	if !fileExists(manifestPath) {
		t.Fatal("manifest missing")
	}
	raw, _ := os.ReadFile(manifestPath)
	var m ExportManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if m.Total != 4 {
		t.Errorf("manifest.Total = %d, want 4", m.Total)
	}
	if m.OK != 4 {
		t.Errorf("manifest.OK = %d, want 4", m.OK)
	}
	if len(m.Meetings) != 4 {
		t.Fatalf("manifest.Meetings length = %d, want 4", len(m.Meetings))
	}

	// All meetings should have results (no nil slots).
	for i, meeting := range m.Meetings {
		if meeting == nil {
			t.Errorf("manifest.Meetings[%d] is nil", i)
			continue
		}
		if meeting.Status != "ok" {
			t.Errorf("manifest.Meetings[%d].Status = %q, want ok", i, meeting.Status)
		}
	}
}

func TestParallelExportPreservesOrder(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case r.URL.Path == "/recordings":
			w.Write([]byte(`{"recordings":[
				{"id":"a1","title":"Alpha","created_at":"2025-01-01"},
				{"id":"b2","title":"Bravo","created_at":"2025-01-02"},
				{"id":"c3","title":"Charlie","created_at":"2025-01-03"}
			]}`))
		default:
			json.NewEncoder(w).Encode(GrainRecording{ID: "a1", Transcript: "text"})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		SkipVideo:   true,
		Parallel:    3,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "_export-manifest.json"))
	var m ExportManifest
	json.Unmarshal(raw, &m)

	// Results should appear in the original discovery order.
	expectedIDs := []string{"a1", "b2", "c3"}
	for i, want := range expectedIDs {
		if m.Meetings[i] == nil {
			t.Fatalf("manifest.Meetings[%d] is nil", i)
		}
		if m.Meetings[i].ID != want {
			t.Errorf("manifest.Meetings[%d].ID = %q, want %q", i, m.Meetings[i].ID, want)
		}
	}
}

func TestParallelExportSkipsExisting(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case r.URL.Path == "/recordings":
			w.Write([]byte(`{"recordings":[
				{"id":"e1","title":"Existing","created_at":"2025-05-01"},
				{"id":"n1","title":"New","created_at":"2025-05-02"}
			]}`))
		default:
			json.NewEncoder(w).Encode(GrainRecording{ID: "n1", Transcript: "new content"})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		SkipVideo:   true,
		Parallel:    2,
		Overwrite:   false,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	// Pre-create the first meeting's metadata file.
	dateDir := filepath.Join(dir, "2025-05-01")
	os.MkdirAll(dateDir, 0o755)
	os.WriteFile(filepath.Join(dateDir, "e1.json"), []byte("{}"), 0o600)

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "_export-manifest.json"))
	var m ExportManifest
	json.Unmarshal(raw, &m)

	if m.Skipped != 1 {
		t.Errorf("manifest.Skipped = %d, want 1", m.Skipped)
	}
	if m.OK != 1 {
		t.Errorf("manifest.OK = %d, want 1", m.OK)
	}
}

func TestParallelExportCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case r.URL.Path == "/recordings":
			w.Write([]byte(`{"recordings":[
				{"id":"c1","title":"Cancel 1","created_at":"2025-06-01"},
				{"id":"c2","title":"Cancel 2","created_at":"2025-06-02"}
			]}`))
		default:
			json.NewEncoder(w).Encode(GrainRecording{ID: "c1"})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		SkipVideo:   true,
		Parallel:    2,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// Should not panic or hang.
	_ = e.Run(ctx)

	// Manifest should have no nil entries (compaction removes undispatched slots).
	for i, m := range e.manifest.Meetings {
		if m == nil {
			t.Errorf("manifest.Meetings[%d] is nil after cancellation", i)
		}
	}
}

func TestParallelExportWithMaxMeetings(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case r.URL.Path == "/recordings":
			w.Write([]byte(`{"recordings":[
				{"id":"m1","title":"Meet 1","created_at":"2025-07-01"},
				{"id":"m2","title":"Meet 2","created_at":"2025-07-02"},
				{"id":"m3","title":"Meet 3","created_at":"2025-07-03"},
				{"id":"m4","title":"Meet 4","created_at":"2025-07-04"},
				{"id":"m5","title":"Meet 5","created_at":"2025-07-05"}
			]}`))
		default:
			json.NewEncoder(w).Encode(GrainRecording{ID: "m1", Transcript: "x"})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		SkipVideo:   true,
		Parallel:    4,
		MaxMeetings: 3,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "_export-manifest.json"))
	var m ExportManifest
	json.Unmarshal(raw, &m)

	if m.Total != 3 {
		t.Errorf("manifest.Total = %d, want 3 (capped by --max)", m.Total)
	}
	if m.OK != 3 {
		t.Errorf("manifest.OK = %d, want 3", m.OK)
	}
}

func TestParallelOneIsSequential(t *testing.T) {
	// --parallel 1 should behave identically to sequential mode.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case r.URL.Path == "/recordings":
			w.Write([]byte(`{"recordings":[
				{"id":"s1","title":"Seq 1","created_at":"2025-08-01"},
				{"id":"s2","title":"Seq 2","created_at":"2025-08-02"}
			]}`))
		default:
			json.NewEncoder(w).Encode(GrainRecording{ID: "s1", Transcript: "text"})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:       "tok",
		OutputDir:   dir,
		SkipVideo:   true,
		Parallel:    1,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run --parallel 1: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "_export-manifest.json"))
	var m ExportManifest
	json.Unmarshal(raw, &m)

	if m.Total != 2 {
		t.Errorf("manifest.Total = %d, want 2", m.Total)
	}
	if m.OK != 2 {
		t.Errorf("manifest.OK = %d, want 2", m.OK)
	}
}
