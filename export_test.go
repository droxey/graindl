package main

import (
	"context"
	"encoding/json"
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
