package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestICloudStorage_WritesBothLocations(t *testing.T) {
	localDir := t.TempDir()
	icloudDir := t.TempDir()

	s, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	data := []byte("test content")
	if err := s.WriteFile("2025-01-15/abc.txt", data); err != nil {
		t.Fatal(err)
	}

	// Verify local copy.
	got, err := os.ReadFile(filepath.Join(localDir, "2025-01-15/abc.txt"))
	if err != nil {
		t.Fatal("local file missing:", err)
	}
	if string(got) != "test content" {
		t.Fatalf("local content = %q, want %q", got, "test content")
	}

	// Verify iCloud copy.
	got, err = os.ReadFile(filepath.Join(icloudDir, "2025-01-15/abc.txt"))
	if err != nil {
		t.Fatal("icloud file missing:", err)
	}
	if string(got) != "test content" {
		t.Fatalf("icloud content = %q, want %q", got, "test content")
	}
}

func TestICloudStorage_WriteJSON_BothLocations(t *testing.T) {
	localDir := t.TempDir()
	icloudDir := t.TempDir()

	s, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	v := map[string]string{"key": "value"}
	if err := s.WriteJSON("data.json", v); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{localDir, icloudDir} {
		if _, err := os.Stat(filepath.Join(dir, "data.json")); err != nil {
			t.Fatalf("file missing in %s: %v", dir, err)
		}
	}
}

func TestICloudStorage_IncrementalSkip(t *testing.T) {
	localDir := t.TempDir()
	icloudDir := t.TempDir()

	s, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}

	data := []byte(`{"id":"abc","title":"Test"}`)
	if err := s.WriteFile("2025-01-15/abc.json", data); err != nil {
		t.Fatal(err)
	}

	// Record the iCloud file's mod time.
	icloudPath := filepath.Join(icloudDir, "2025-01-15/abc.json")
	info1, _ := os.Stat(icloudPath)

	// Write the same data again — should be skipped in iCloud.
	if err := s.WriteFile("2025-01-15/abc.json", data); err != nil {
		t.Fatal(err)
	}

	info2, _ := os.Stat(icloudPath)
	if info2.ModTime() != info1.ModTime() {
		t.Fatal("iCloud file was rewritten despite identical content")
	}
}

func TestICloudStorage_IncrementalUpdate(t *testing.T) {
	localDir := t.TempDir()
	icloudDir := t.TempDir()

	s, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// First write.
	if err := s.WriteFile("2025-01-15/abc.json", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}

	// Second write with different content.
	if err := s.WriteFile("2025-01-15/abc.json", []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(filepath.Join(icloudDir, "2025-01-15/abc.json"))
	if string(got) != `{"v":2}` {
		t.Fatalf("icloud content = %q, want updated version", got)
	}
}

func TestICloudStorage_ConflictResolution_Video_SimilarSize(t *testing.T) {
	localDir := t.TempDir()
	icloudDir := t.TempDir()

	s, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Simulate an existing video in sync state.
	video1 := make([]byte, 10000)
	if err := s.WriteFile("2025-01-15/abc.mp4", video1); err != nil {
		t.Fatal(err)
	}

	// Write a video with slightly different content but similar size (within 1%).
	video2 := make([]byte, 10050) // 0.5% larger
	video2[0] = 1                 // different content
	if err := s.WriteFile("2025-01-15/abc.mp4", video2); err != nil {
		t.Fatal(err)
	}

	// iCloud should still have the original (skip due to similar size).
	got, _ := os.ReadFile(filepath.Join(icloudDir, "2025-01-15/abc.mp4"))
	if len(got) != 10000 {
		t.Fatalf("icloud video size = %d, want 10000 (should keep existing)", len(got))
	}
}

func TestICloudStorage_ConflictResolution_Video_DifferentSize(t *testing.T) {
	localDir := t.TempDir()
	icloudDir := t.TempDir()

	s, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// First video.
	video1 := make([]byte, 10000)
	if err := s.WriteFile("2025-01-15/abc.mp4", video1); err != nil {
		t.Fatal(err)
	}

	// Second video with substantially different size (>1%).
	video2 := make([]byte, 20000)
	video2[0] = 1
	if err := s.WriteFile("2025-01-15/abc.mp4", video2); err != nil {
		t.Fatal(err)
	}

	// iCloud should have the new version (overwrite due to different size).
	got, _ := os.ReadFile(filepath.Join(icloudDir, "2025-01-15/abc.mp4"))
	if len(got) != 20000 {
		t.Fatalf("icloud video size = %d, want 20000 (should overwrite)", len(got))
	}
}

func TestICloudStorage_ConflictResolution_Metadata(t *testing.T) {
	localDir := t.TempDir()
	icloudDir := t.TempDir()

	s, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.WriteFile("2025-01-15/abc.json", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteFile("2025-01-15/abc.json", []byte(`{"v":2,"extra":"field"}`)); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(filepath.Join(icloudDir, "2025-01-15/abc.json"))
	if string(got) != `{"v":2,"extra":"field"}` {
		t.Fatalf("metadata not updated: %q", got)
	}
}

func TestICloudStorage_SyncStatePersistedOnClose(t *testing.T) {
	localDir := t.TempDir()
	icloudDir := t.TempDir()

	s, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.WriteFile("test.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(icloudDir, syncStateFile)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatal("sync state file not written on Close:", err)
	}

	// Reopen and verify state persisted.
	s2, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	if s2.TrackedFiles() != 1 {
		t.Fatalf("tracked files = %d, want 1", s2.TrackedFiles())
	}
}

func TestICloudStorage_EnsureDir(t *testing.T) {
	localDir := t.TempDir()
	icloudDir := t.TempDir()

	s, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.EnsureDir("2025-01-15"); err != nil {
		t.Fatal(err)
	}

	// Both directories should exist.
	for _, dir := range []string{localDir, icloudDir} {
		info, err := os.Stat(filepath.Join(dir, "2025-01-15"))
		if err != nil {
			t.Fatalf("dir missing in %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("not a directory in %s", dir)
		}
	}
}

func TestICloudStorage_FilePermissions(t *testing.T) {
	localDir := t.TempDir()
	icloudDir := t.TempDir()

	s, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.WriteFile("test.txt", []byte("data")); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{localDir, icloudDir} {
		info, _ := os.Stat(filepath.Join(dir, "test.txt"))
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("perm in %s = %o, want 0600", dir, perm)
		}
	}
}

func TestICloudStorage_TrackedSize(t *testing.T) {
	localDir := t.TempDir()
	icloudDir := t.TempDir()

	s, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_ = s.WriteFile("a.txt", []byte("hello"))      // 5 bytes
	_ = s.WriteFile("b.txt", []byte("world!!!!!")) // 10 bytes

	if got := s.TrackedSize(); got != 15 {
		t.Fatalf("tracked size = %d, want 15", got)
	}
}

func TestCopyFileWithHash(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	data := []byte("test data for hashing")
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}

	hash, err := copyFileWithHash(dst, src)
	if err != nil {
		t.Fatal(err)
	}

	// Verify content was copied.
	got, _ := os.ReadFile(dst)
	if string(got) != "test data for hashing" {
		t.Fatalf("dst content = %q", got)
	}

	// Verify hash matches what computeSHA256 gives.
	expected := computeSHA256(data)
	if hash != expected {
		t.Fatalf("hash = %q, want %q", hash, expected)
	}

	// Verify 0o600 permissions on destination.
	info, _ := os.Stat(dst)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 0600", perm)
	}
}

func TestHashFileOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")

	data := []byte("content to hash")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := hashFileOnDisk(path)
	if err != nil {
		t.Fatal(err)
	}

	want := computeSHA256(data)
	if got != want {
		t.Fatalf("hash = %q, want %q", got, want)
	}
}

func TestDetectICloudPath_NonDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("test only runs on non-darwin")
	}
	_, err := detectICloudPath()
	if err == nil {
		t.Fatal("expected error on non-darwin platform")
	}
}

func TestValidateICloudPath_Relative(t *testing.T) {
	err := validateICloudPath("relative/path")
	if err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestValidateICloudPath_Traversal(t *testing.T) {
	err := validateICloudPath("/tmp/../etc/passwd")
	if err == nil {
		t.Fatal("expected error for traversal path")
	}
}

func TestValidateICloudPath_Valid(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "icloud-test")
	if err := validateICloudPath(target); err != nil {
		t.Fatal("unexpected error for valid path:", err)
	}
	// Should have created the directory.
	if _, err := os.Stat(target); err != nil {
		t.Fatal("directory not created:", err)
	}
}

func TestSizeSimilar(t *testing.T) {
	tests := []struct {
		a, b      int64
		tolerance float64
		want      bool
	}{
		{0, 0, 0.01, true},
		{0, 100, 0.01, false},
		{100, 0, 0.01, false},
		{10000, 10050, 0.01, true},  // 0.5% diff
		{10000, 10100, 0.01, true},  // 100/10100 = 0.99% ≤ 1%
		{10000, 10200, 0.01, false}, // 200/10200 = 1.96% > 1%
		{10000, 20000, 0.01, false}, // 50% diff
		{100, 100, 0.01, true},      // identical
	}
	for _, tc := range tests {
		got := sizeSimilar(tc.a, tc.b, tc.tolerance)
		if got != tc.want {
			t.Errorf("sizeSimilar(%d, %d, %f) = %v, want %v", tc.a, tc.b, tc.tolerance, got, tc.want)
		}
	}
}

func TestResolveConflict(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		existing    *SyncFileEntry
		newData     []byte
		want        conflictAction
	}{
		{
			name:        "metadata overwrite",
			contentType: "metadata",
			existing:    &SyncFileEntry{Size: 100},
			newData:     make([]byte, 200),
			want:        conflictOverwrite,
		},
		{
			name:        "transcript overwrite",
			contentType: "transcript",
			existing:    &SyncFileEntry{Size: 500},
			newData:     make([]byte, 600),
			want:        conflictOverwrite,
		},
		{
			name:        "manifest overwrite",
			contentType: "manifest",
			existing:    &SyncFileEntry{Size: 1000},
			newData:     make([]byte, 2000),
			want:        conflictOverwrite,
		},
		{
			name:        "video similar size skip",
			contentType: "video",
			existing:    &SyncFileEntry{Size: 10000},
			newData:     make([]byte, 10050),
			want:        conflictSkip,
		},
		{
			name:        "video different size warn",
			contentType: "video",
			existing:    &SyncFileEntry{Size: 10000},
			newData:     make([]byte, 20000),
			want:        conflictWarn,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveConflict(tc.contentType, tc.existing, tc.newData)
			if got != tc.want {
				t.Errorf("resolveConflict(%q) = %d, want %d", tc.contentType, got, tc.want)
			}
		})
	}
}

func TestICloudStorage_CopyFileToICloud(t *testing.T) {
	localDir := t.TempDir()
	icloudDir := t.TempDir()

	s, err := NewICloudStorage(localDir, icloudDir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Write a file locally (simulating a browser download).
	relPath := "2025-01-15/video.mp4"
	localPath := filepath.Join(localDir, relPath)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("fake video content for testing streaming copy")
	if err := os.WriteFile(localPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// Copy to iCloud.
	if err := s.CopyFileToICloud(relPath); err != nil {
		t.Fatal(err)
	}

	// Verify iCloud copy.
	icloudPath := filepath.Join(icloudDir, relPath)
	got, err := os.ReadFile(icloudPath)
	if err != nil {
		t.Fatal("icloud copy missing:", err)
	}
	if string(got) != string(data) {
		t.Fatalf("icloud content mismatch")
	}

	// Verify tracked in sync state.
	if s.TrackedFiles() != 1 {
		t.Fatalf("tracked files = %d, want 1", s.TrackedFiles())
	}
}
