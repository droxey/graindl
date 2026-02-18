package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalStorage_WriteFile(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStorage(dir)

	data := []byte("hello world")
	if err := s.WriteFile("test.txt", data); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Fatalf("got %q, want %q", got, "hello world")
	}

	// Verify 0o600 permissions.
	info, _ := os.Stat(filepath.Join(dir, "test.txt"))
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 0600", perm)
	}
}

func TestLocalStorage_WriteJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStorage(dir)

	v := map[string]string{"key": "value"}
	if err := s.WriteJSON("data.json", v); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if m["key"] != "value" {
		t.Fatalf("got %v, want key=value", m)
	}

	info, _ := os.Stat(filepath.Join(dir, "data.json"))
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 0600", perm)
	}
}

func TestLocalStorage_FileExists(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStorage(dir)

	if s.FileExists("missing.txt") {
		t.Fatal("FileExists returned true for missing file")
	}

	_ = os.WriteFile(filepath.Join(dir, "exists.txt"), []byte("x"), 0o600)
	if !s.FileExists("exists.txt") {
		t.Fatal("FileExists returned false for existing file")
	}
}

func TestLocalStorage_EnsureDir(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStorage(dir)

	if err := s.EnsureDir("a/b/c"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "a/b/c"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}
}

func TestLocalStorage_AbsPath(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStorage(dir)

	got := s.AbsPath("2025-01-15/abc.json")
	want := filepath.Join(dir, "2025-01-15/abc.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLocalStorage_Close(t *testing.T) {
	s := NewLocalStorage(t.TempDir())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncState_LoadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	state := NewSyncState()
	state.Files["test.json"] = &SyncFileEntry{
		SHA256:      "abc123",
		Size:        1024,
		ModifiedAt:  "2026-02-18T12:00:00Z",
		ContentType: "metadata",
	}

	if err := saveSyncState(path, state); err != nil {
		t.Fatal(err)
	}

	loaded := loadSyncState(path)
	if loaded.Version != 1 {
		t.Fatalf("version = %d, want 1", loaded.Version)
	}
	entry := loaded.Files["test.json"]
	if entry == nil {
		t.Fatal("missing file entry after load")
	}
	if entry.SHA256 != "abc123" {
		t.Fatalf("sha256 = %q, want %q", entry.SHA256, "abc123")
	}
	if entry.Size != 1024 {
		t.Fatalf("size = %d, want 1024", entry.Size)
	}
	if entry.ContentType != "metadata" {
		t.Fatalf("content_type = %q, want %q", entry.ContentType, "metadata")
	}
}

func TestSyncState_LoadMissing(t *testing.T) {
	state := loadSyncState("/nonexistent/path/state.json")
	if state.Version != 1 {
		t.Fatal("expected fresh state for missing file")
	}
	if len(state.Files) != 0 {
		t.Fatal("expected empty files map")
	}
}

func TestSyncState_LoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	_ = os.WriteFile(path, []byte("{invalid json"), 0o600)

	state := loadSyncState(path)
	if state.Version != 1 {
		t.Fatal("expected fresh state for corrupt file")
	}
}

func TestComputeSHA256(t *testing.T) {
	// SHA-256 of empty string.
	got := computeSHA256([]byte{})
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// SHA-256 of "hello".
	got = computeSHA256([]byte("hello"))
	want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestClassifyContent(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"2025-01-15/abc.json", "metadata"},
		{"2025-01-15/abc.highlights.json", "highlights"},
		{"2025-01-15/abc.transcript.txt", "transcript"},
		{"2025-01-15/abc.md", "markdown"},
		{"2025-01-15/abc.mp4", "video"},
		{"2025-01-15/abc.webm", "video"},
		{"2025-01-15/abc.m4a", "audio"},
		{"_export-manifest.json", "manifest"},
		{"other.xyz", "other"},
	}
	for _, tc := range tests {
		got := classifyContent(tc.path)
		if got != tc.want {
			t.Errorf("classifyContent(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestSyncState_SavePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := saveSyncState(path, NewSyncState()); err != nil {
		t.Fatal(err)
	}

	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 0600", perm)
	}
}
