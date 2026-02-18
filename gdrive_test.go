package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── md5File ─────────────────────────────────────────────────────────────────

func TestMD5File(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.txt")
	os.WriteFile(p, []byte("hello world"), 0o600)

	got, err := md5File(p)
	if err != nil {
		t.Fatalf("md5File: %v", err)
	}
	// MD5("hello world") = 5eb63bbbe01eeed093cb22bb8f5acdc3
	want := "5eb63bbbe01eeed093cb22bb8f5acdc3"
	if got != want {
		t.Errorf("md5File = %q, want %q", got, want)
	}
}

func TestMD5FileMissing(t *testing.T) {
	_, err := md5File("/nonexistent/file")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestMD5FileEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.txt")
	os.WriteFile(p, []byte{}, 0o600)

	got, err := md5File(p)
	if err != nil {
		t.Fatalf("md5File: %v", err)
	}
	// MD5 of empty string = d41d8cd98f00b204e9800998ecf8427e
	want := "d41d8cd98f00b204e9800998ecf8427e"
	if got != want {
		t.Errorf("md5File empty = %q, want %q", got, want)
	}
}

// ── detectMIME ──────────────────────────────────────────────────────────────

func TestDetectMIME(t *testing.T) {
	tests := map[string]string{
		"file.json": "application/json",
		"file.txt":  "text/plain",
		"file.md":   "text/markdown",
		"file.mp4":  "video/mp4",
		"file.m4a":  "audio/mp4",
		"file.webm": "video/webm",
		"file.url":  "text/plain",
	}
	for path, want := range tests {
		if got := detectMIME(path); got != want {
			t.Errorf("detectMIME(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDetectMIMEUnknown(t *testing.T) {
	got := detectMIME("file.xyz123")
	if got != "application/octet-stream" {
		t.Errorf("detectMIME unknown ext = %q, want application/octet-stream", got)
	}
}

// ── DriveSyncState Load/Save ─────────────────────────────────────────────────

func TestDriveSyncState_LoadSave(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "sync.json")

	// Start empty.
	state, err := loadDriveSyncState(statePath)
	if err != nil {
		t.Fatalf("loadDriveSyncState: %v", err)
	}
	if state.Version != 1 {
		t.Errorf("Version = %d, want 1", state.Version)
	}
	if len(state.Files) != 0 {
		t.Errorf("Files should be empty, got %d", len(state.Files))
	}

	// Add entries.
	state.FolderID = "folder-123"
	state.Files["2025-01-15/meeting.json"] = &SyncEntry{
		DriveFileID:  "drive-abc",
		MD5Checksum:  "checksum123",
		Size:         4096,
		LocalModTime: "2025-01-15T10:00:00Z",
		UploadedAt:   "2025-01-15T10:01:00Z",
	}

	// Save via manual write (simulating saveSyncState without DriveUploader).
	data, _ := json.MarshalIndent(state, "", "  ")
	writeFile(statePath, data)

	// Reload.
	loaded, err := loadDriveSyncState(statePath)
	if err != nil {
		t.Fatalf("loadDriveSyncState after save: %v", err)
	}
	if loaded.FolderID != "folder-123" {
		t.Errorf("FolderID = %q, want folder-123", loaded.FolderID)
	}
	entry, ok := loaded.Files["2025-01-15/meeting.json"]
	if !ok {
		t.Fatal("expected entry for meeting.json")
	}
	if entry.DriveFileID != "drive-abc" {
		t.Errorf("DriveFileID = %q, want drive-abc", entry.DriveFileID)
	}
	if entry.MD5Checksum != "checksum123" {
		t.Errorf("MD5Checksum = %q", entry.MD5Checksum)
	}
	if entry.Size != 4096 {
		t.Errorf("Size = %d, want 4096", entry.Size)
	}
}

func TestDriveSyncState_MissingFile(t *testing.T) {
	state, err := loadDriveSyncState("/nonexistent/gdrive-sync.json")
	if err != nil {
		t.Fatalf("loadDriveSyncState should not error on missing file: %v", err)
	}
	if state.Version != 1 {
		t.Errorf("Version = %d, want 1", state.Version)
	}
	if len(state.Files) != 0 {
		t.Errorf("Files should be empty for missing state file")
	}
}

func TestDriveSyncState_FolderIDChange(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "sync.json")

	// Save state with folder-A.
	state := &DriveSyncState{
		Version:  1,
		FolderID: "folder-A",
		Files: map[string]*SyncEntry{
			"meeting.json": {DriveFileID: "old-id", MD5Checksum: "old-md5"},
		},
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	writeFile(statePath, data)

	// Load and detect folder change.
	loaded, err := loadDriveSyncState(statePath)
	if err != nil {
		t.Fatalf("loadDriveSyncState: %v", err)
	}

	// Simulate NewDriveUploader folder ID change detection.
	newFolderID := "folder-B"
	if loaded.FolderID != "" && loaded.FolderID != newFolderID {
		loaded = &DriveSyncState{Version: 1, Files: make(map[string]*SyncEntry)}
	}
	loaded.FolderID = newFolderID

	if len(loaded.Files) != 0 {
		t.Errorf("Files should be empty after folder change, got %d", len(loaded.Files))
	}
	if loaded.FolderID != "folder-B" {
		t.Errorf("FolderID = %q, want folder-B", loaded.FolderID)
	}
}

func TestDriveSyncState_NilFilesMap(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "sync.json")

	// Write state with null files map.
	writeFile(statePath, []byte(`{"version":1,"folder_id":"f1","files":null}`))

	state, err := loadDriveSyncState(statePath)
	if err != nil {
		t.Fatalf("loadDriveSyncState: %v", err)
	}
	if state.Files == nil {
		t.Error("Files should be initialized, not nil")
	}
}

// ── shouldUpload ────────────────────────────────────────────────────────────

func TestShouldUpload_NewFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "new.txt")
	os.WriteFile(p, []byte("new content"), 0o600)

	d := &DriveUploader{
		state:    &DriveSyncState{Version: 1, Files: make(map[string]*SyncEntry)},
		conflict: "local-wins",
	}

	action, entry := d.shouldUpload(p, "new.txt")
	if action != "create" {
		t.Errorf("action = %q, want create", action)
	}
	if entry != nil {
		t.Error("entry should be nil for new file")
	}
}

func TestShouldUpload_Unchanged(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "same.txt")
	os.WriteFile(p, []byte("same content"), 0o600)

	checksum, _ := md5File(p)

	d := &DriveUploader{
		state: &DriveSyncState{Version: 1, Files: map[string]*SyncEntry{
			"same.txt": {DriveFileID: "id-1", MD5Checksum: checksum},
		}},
		conflict: "local-wins",
	}

	action, entry := d.shouldUpload(p, "same.txt")
	if action != "skip" {
		t.Errorf("action = %q, want skip", action)
	}
	if entry == nil || entry.DriveFileID != "id-1" {
		t.Error("expected existing entry")
	}
}

func TestShouldUpload_Modified_LocalWins(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "changed.txt")
	os.WriteFile(p, []byte("new version"), 0o600)

	d := &DriveUploader{
		state: &DriveSyncState{Version: 1, Files: map[string]*SyncEntry{
			"changed.txt": {DriveFileID: "id-2", MD5Checksum: "old-checksum"},
		}},
		conflict: "local-wins",
	}

	action, entry := d.shouldUpload(p, "changed.txt")
	if action != "update" {
		t.Errorf("action = %q, want update", action)
	}
	if entry == nil || entry.DriveFileID != "id-2" {
		t.Error("expected existing entry")
	}
}

func TestShouldUpload_Modified_Skip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "changed.txt")
	os.WriteFile(p, []byte("new version"), 0o600)

	d := &DriveUploader{
		state: &DriveSyncState{Version: 1, Files: map[string]*SyncEntry{
			"changed.txt": {DriveFileID: "id-3", MD5Checksum: "old-checksum"},
		}},
		conflict: "skip",
	}

	action, _ := d.shouldUpload(p, "changed.txt")
	if action != "skip" {
		t.Errorf("action = %q, want skip", action)
	}
}

func TestShouldUpload_Modified_NewerWins_LocalNewer(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "newer.txt")
	os.WriteFile(p, []byte("newer version"), 0o600)

	now := time.Now()
	os.Chtimes(p, now, now)

	d := &DriveUploader{
		state: &DriveSyncState{Version: 1, Files: map[string]*SyncEntry{
			"newer.txt": {
				DriveFileID: "id-4",
				MD5Checksum: "old-checksum",
				UploadedAt:  now.Add(-1 * time.Hour).UTC().Format(time.RFC3339),
			},
		}},
		conflict: "newer-wins",
	}

	action, _ := d.shouldUpload(p, "newer.txt")
	if action != "update" {
		t.Errorf("action = %q, want update (local is newer)", action)
	}
}

func TestShouldUpload_Modified_NewerWins_LocalOlder(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "older.txt")
	os.WriteFile(p, []byte("older version"), 0o600)

	past := time.Now().Add(-2 * time.Hour)
	os.Chtimes(p, past, past)

	d := &DriveUploader{
		state: &DriveSyncState{Version: 1, Files: map[string]*SyncEntry{
			"older.txt": {
				DriveFileID: "id-5",
				MD5Checksum: "old-checksum",
				UploadedAt:  time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
			},
		}},
		conflict: "newer-wins",
	}

	action, _ := d.shouldUpload(p, "older.txt")
	if action != "skip" {
		t.Errorf("action = %q, want skip (local is older)", action)
	}
}

// ── collectResultPaths ──────────────────────────────────────────────────────

func TestCollectResultPaths(t *testing.T) {
	r := &ExportResult{
		MetadataPath:    "2025-01-15/abc.json",
		TranscriptPaths: map[string]string{"text": "2025-01-15/abc.transcript.txt"},
		HighlightsPath:  "2025-01-15/abc.highlights.json",
		MarkdownPath:    "2025-01-15/abc.md",
		VideoPath:       "2025-01-15/abc.mp4",
		AudioPath:       "",
	}

	paths := collectResultPaths(r)

	expected := map[string]bool{
		"2025-01-15/abc.json":            true,
		"2025-01-15/abc.transcript.txt":  true,
		"2025-01-15/abc.highlights.json": true,
		"2025-01-15/abc.md":              true,
		"2025-01-15/abc.mp4":             true,
	}

	found := 0
	for _, p := range paths {
		if p != "" {
			if !expected[p] {
				t.Errorf("unexpected path: %q", p)
			}
			found++
		}
	}
	if found != len(expected) {
		t.Errorf("found %d non-empty paths, want %d", found, len(expected))
	}
}

func TestCollectResultPathsEmpty(t *testing.T) {
	r := &ExportResult{TranscriptPaths: make(map[string]string)}
	paths := collectResultPaths(r)
	for _, p := range paths {
		if p != "" {
			t.Errorf("expected all empty paths, got %q", p)
		}
	}
}

// ── SaveSyncState atomic write ──────────────────────────────────────────────

func TestSaveSyncStateAtomic(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "gdrive-sync.json")

	d := &DriveUploader{
		state: &DriveSyncState{
			Version:  1,
			FolderID: "test-folder",
			Files: map[string]*SyncEntry{
				"test.json": {DriveFileID: "abc", MD5Checksum: "def"},
			},
		},
		statePath: statePath,
	}

	if err := d.saveSyncState(); err != nil {
		t.Fatalf("saveSyncState: %v", err)
	}

	// Verify file exists and is valid JSON.
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}

	var loaded DriveSyncState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.FolderID != "test-folder" {
		t.Errorf("FolderID = %q", loaded.FolderID)
	}
	if loaded.LastSync == "" {
		t.Error("LastSync should be set")
	}

	// Temp file should not exist.
	if fileExists(statePath + ".tmp") {
		t.Error("temp file should be cleaned up after rename")
	}

	// Permissions should be 0o600.
	info, _ := os.Stat(statePath)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state file perms = %04o, want 0600", info.Mode().Perm())
	}
}

// ── isTransientCode ─────────────────────────────────────────────────────────

func TestIsTransientCode(t *testing.T) {
	transientCodes := []int{
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
	}
	for _, code := range transientCodes {
		if !isTransientCode(code) {
			t.Errorf("isTransientCode(%d) = false, want true", code)
		}
	}

	nonTransient := []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
	}
	for _, code := range nonTransient {
		if isTransientCode(code) {
			t.Errorf("isTransientCode(%d) = true, want false", code)
		}
	}
}

// ── driveAPIError ───────────────────────────────────────────────────────────

func TestDriveAPIError(t *testing.T) {
	err := &driveAPIError{Code: 403, Body: "forbidden"}
	if err.Error() != "drive API error (403): forbidden" {
		t.Errorf("Error() = %q", err.Error())
	}
}

// ── base64URLEncode ─────────────────────────────────────────────────────────

func TestBase64URLEncode(t *testing.T) {
	got := base64URLEncode([]byte("test"))
	if got != "dGVzdA" {
		t.Errorf("base64URLEncode = %q, want dGVzdA", got)
	}
}
