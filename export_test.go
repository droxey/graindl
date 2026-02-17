package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// ── exportOne ───────────────────────────────────────────────────────────────

func TestExportOneMinimalMetadata(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:   dir,
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}

	ref := MeetingRef{
		ID:    "test-id",
		Title: "Test Meeting",
		Date:  "2025-06-01T10:00:00Z",
		URL:   "https://grain.com/app/meetings/test-id",
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
	if meta.Title != "Test Meeting" {
		t.Errorf("meta.Title = %q", meta.Title)
	}
	if meta.Date != "2025-06-01T10:00:00Z" {
		t.Errorf("meta.Date = %q", meta.Date)
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
	dir := t.TempDir()
	cfg := &Config{
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

	ref := MeetingRef{
		ID: "ow-id", Title: "Overwritten", Date: "2025-01-01",
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
	dir := t.TempDir()
	cfg := &Config{
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

	// Metadata file should exist.
	metaPath := filepath.Join(dir, m.Meetings[0].MetadataPath)
	if !fileExists(metaPath) {
		t.Fatalf("metadata file missing: %s", metaPath)
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

func TestRunSingleMeetingSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
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
	defer e.Close()

	// Pre-create the metadata file at today's date directory (runSingle
	// falls back to today's date when no date enrichment is available).
	today := time.Now().Format("2006-01-02")
	dateDir := filepath.Join(dir, today)
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
	dir := t.TempDir()
	cfg := &Config{
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
	defer e.Close()

	// Pre-create the file at today's date directory (runSingle falls back
	// to today's date when no date enrichment is available).
	today := time.Now().Format("2006-01-02")
	dateDir := filepath.Join(dir, today)
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
	if meta.ID != "ow-single" {
		t.Errorf("meta.ID = %q, expected overwritten content", meta.ID)
	}
}

func TestRunSingleMeetingCancellation(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
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
	defer e.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// Should not panic or hang.
	_ = e.Run(ctx)
}

// ── Dry-run tests ────────────────────────────────────────────────────────────

func TestDryRunSingleMeeting(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
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

// ── Audio-only export ────────────────────────────────────────────────────────

func TestExportOneAudioOnlyMode(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:   dir,
		AudioOnly:   true,
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}

	ref := MeetingRef{
		ID:    "audio-id",
		Title: "Audio Meeting",
		Date:  "2025-06-01T10:00:00Z",
		URL:   "https://grain.com/app/meetings/audio-id",
	}

	r := e.exportOne(context.Background(), ref)

	// Should produce metadata.
	if r.MetadataPath == "" {
		t.Error("expected metadata to be written")
	}

	// Video path should be empty — audio-only mode with skip-video doesn't write video.
	if r.VideoPath != "" {
		t.Errorf("VideoPath should be empty in audio-only mode, got %q", r.VideoPath)
	}
	if r.VideoMethod != "" {
		t.Errorf("VideoMethod should be empty in audio-only mode, got %q", r.VideoMethod)
	}

	// Audio extraction will fail without a real browser, but the metadata
	// should still succeed.
	if r.Status != "ok" {
		t.Errorf("status = %q, want ok", r.Status)
	}
}

func TestExportOneAudioOnlyAndSkipVideoMutualExclusion(t *testing.T) {
	// When both --audio-only and --skip-video are set, skip-video takes
	// precedence: no media is downloaded at all.
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:   dir,
		AudioOnly:   true,
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}

	ref := MeetingRef{
		ID: "both-flags", Title: "Both Flags", Date: "2025-06-01",
	}

	r := e.exportOne(context.Background(), ref)
	if r.VideoPath != "" {
		t.Errorf("VideoPath should be empty, got %q", r.VideoPath)
	}
}

func TestRunSingleMeetingAudioOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:   dir,
		MeetingID:   "audio-single",
		AudioOnly:   true,
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer e.Close()

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run with --id --audio-only: %v", err)
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
	if m.Total != 1 {
		t.Errorf("manifest.Total = %d, want 1", m.Total)
	}
	if m.OK != 1 {
		t.Errorf("manifest.OK = %d, want 1", m.OK)
	}
	// Video should not be present.
	if m.Meetings[0].VideoPath != "" {
		t.Errorf("VideoPath should be empty in audio-only mode, got %q", m.Meetings[0].VideoPath)
	}
}

// ── Parallel export (direct exportParallel testing) ─────────────────────────

func TestExportParallelDirect(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
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

	meetings := []MeetingRef{
		{ID: "p1", Title: "Parallel 1", Date: "2025-03-01"},
		{ID: "p2", Title: "Parallel 2", Date: "2025-03-02"},
		{ID: "p3", Title: "Parallel 3", Date: "2025-03-03"},
		{ID: "p4", Title: "Parallel 4", Date: "2025-03-04"},
	}

	e.manifest.Total = len(meetings)
	e.exportParallel(context.Background(), meetings)

	if e.manifest.OK != 4 {
		t.Errorf("manifest.OK = %d, want 4", e.manifest.OK)
	}
	if len(e.manifest.Meetings) != 4 {
		t.Fatalf("manifest.Meetings length = %d, want 4", len(e.manifest.Meetings))
	}

	// All meetings should have results (no nil slots).
	for i, meeting := range e.manifest.Meetings {
		if meeting == nil {
			t.Errorf("manifest.Meetings[%d] is nil", i)
			continue
		}
		if meeting.Status != "ok" {
			t.Errorf("manifest.Meetings[%d].Status = %q, want ok", i, meeting.Status)
		}
	}
}

func TestExportParallelPreservesOrder(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
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

	meetings := []MeetingRef{
		{ID: "a1", Title: "Alpha", Date: "2025-01-01"},
		{ID: "b2", Title: "Bravo", Date: "2025-01-02"},
		{ID: "c3", Title: "Charlie", Date: "2025-01-03"},
	}

	e.manifest.Total = len(meetings)
	e.exportParallel(context.Background(), meetings)

	// Results should appear in the original order.
	expectedIDs := []string{"a1", "b2", "c3"}
	for i, want := range expectedIDs {
		if e.manifest.Meetings[i] == nil {
			t.Fatalf("manifest.Meetings[%d] is nil", i)
		}
		if e.manifest.Meetings[i].ID != want {
			t.Errorf("manifest.Meetings[%d].ID = %q, want %q", i, e.manifest.Meetings[i].ID, want)
		}
	}
}

func TestExportParallelCancellation(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
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

	meetings := []MeetingRef{
		{ID: "c1", Title: "Cancel 1", Date: "2025-06-01"},
		{ID: "c2", Title: "Cancel 2", Date: "2025-06-02"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	e.manifest.Total = len(meetings)
	e.exportParallel(ctx, meetings)

	// Manifest should have no nil entries (compaction removes undispatched slots).
	for i, m := range e.manifest.Meetings {
		if m == nil {
			t.Errorf("manifest.Meetings[%d] is nil after cancellation", i)
		}
	}
}

func TestExportSequentialBasic(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:   dir,
		SkipVideo:   true,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}

	meetings := []MeetingRef{
		{ID: "s1", Title: "Seq 1", Date: "2025-08-01"},
		{ID: "s2", Title: "Seq 2", Date: "2025-08-02"},
	}

	e.manifest.Total = len(meetings)
	e.exportSequential(context.Background(), meetings)

	if e.manifest.OK != 2 {
		t.Errorf("manifest.OK = %d, want 2", e.manifest.OK)
	}
	if len(e.manifest.Meetings) != 2 {
		t.Fatalf("manifest.Meetings length = %d, want 2", len(e.manifest.Meetings))
	}
}

func TestExportSequentialSkipsExisting(t *testing.T) {
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

	// Pre-create the first meeting's metadata file.
	dateDir := filepath.Join(dir, "2025-05-01")
	os.MkdirAll(dateDir, 0o755)
	os.WriteFile(filepath.Join(dateDir, "e1.json"), []byte("{}"), 0o600)

	meetings := []MeetingRef{
		{ID: "e1", Title: "Existing", Date: "2025-05-01"},
		{ID: "n1", Title: "New", Date: "2025-05-02"},
	}

	e.manifest.Total = len(meetings)
	e.exportSequential(context.Background(), meetings)

	if e.manifest.Skipped != 1 {
		t.Errorf("manifest.Skipped = %d, want 1", e.manifest.Skipped)
	}
	if e.manifest.OK != 1 {
		t.Errorf("manifest.OK = %d, want 1", e.manifest.OK)
	}
}

// ── validID ─────────────────────────────────────────────────────────────────

func TestValidIDRejectsInvalid(t *testing.T) {
	badIDs := []string{
		"../me",
		"../../etc/passwd",
		"id?evil=true",
		"id&inject=1",
		"id#fragment",
		"id/nested",
		"",
		"a b c",
	}
	for _, id := range badIDs {
		if validID.MatchString(id) {
			t.Errorf("validID should reject %q", id)
		}
	}
}

func TestValidIDAcceptsValid(t *testing.T) {
	goodIDs := []string{"abc123", "meeting-id-456", "REC_2025_01", "a"}
	for _, id := range goodIDs {
		if !validID.MatchString(id) {
			t.Errorf("validID should accept %q", id)
		}
	}
}
