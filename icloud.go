package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// iCloudSubdir is the subdirectory name used inside the iCloud Drive root.
const iCloudSubdir = "graindl"

// syncStateFile is the filename for the incremental sync state.
const syncStateFile = ".graindl-sync-state.json"

// ── ICloudStorage ──────────────────────────────────────────────────────────

// ICloudStorage writes files to both a local output directory and an iCloud
// Drive directory. The local write always happens first. The iCloud write is
// conditional: files are skipped when the content hash matches what is already
// tracked in the sync state, and conflict resolution applies for files with
// changed content.
type ICloudStorage struct {
	local      *LocalStorage
	icloudRoot string // resolved iCloud Drive directory (e.g. ~/Library/.../graindl)
	state      *SyncState
	mu         sync.Mutex // protects state
}

// NewICloudStorage creates a storage backend that writes to both localRoot
// and icloudRoot. It loads any existing sync state from the iCloud directory.
func NewICloudStorage(localRoot, icloudRoot string) (*ICloudStorage, error) {
	if err := os.MkdirAll(icloudRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create icloud dir: %w", err)
	}

	statePath := filepath.Join(icloudRoot, syncStateFile)
	state := loadSyncState(statePath)

	slog.Debug("iCloud sync state loaded", "files", len(state.Files), "path", statePath)

	return &ICloudStorage{
		local:      NewLocalStorage(localRoot),
		icloudRoot: icloudRoot,
		state:      state,
	}, nil
}

func (s *ICloudStorage) WriteFile(relPath string, data []byte) error {
	// Always write locally first.
	if err := s.local.WriteFile(relPath, data); err != nil {
		return err
	}

	// Attempt iCloud write (non-fatal on failure).
	if err := s.writeToICloud(relPath, data); err != nil {
		slog.Warn("iCloud write failed, local copy preserved", "path", relPath, "error", err)
	}
	return nil
}

func (s *ICloudStorage) WriteJSON(relPath string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	// Write marshaled bytes to both targets.
	if err := s.local.WriteFile(relPath, data); err != nil {
		return err
	}
	if err := s.writeToICloud(relPath, data); err != nil {
		slog.Warn("iCloud JSON write failed, local copy preserved", "path", relPath, "error", err)
	}
	return nil
}

func (s *ICloudStorage) FileExists(relPath string) bool {
	return s.local.FileExists(relPath)
}

func (s *ICloudStorage) EnsureDir(relPath string) error {
	if err := s.local.EnsureDir(relPath); err != nil {
		return err
	}
	// Mirror directory structure in iCloud.
	icloudDir := filepath.Join(s.icloudRoot, relPath)
	if err := os.MkdirAll(icloudDir, 0o755); err != nil {
		slog.Warn("iCloud dir creation failed", "path", icloudDir, "error", err)
	}
	return nil
}

func (s *ICloudStorage) AbsPath(relPath string) string {
	return s.local.AbsPath(relPath)
}

// Close persists the sync state to the iCloud directory.
func (s *ICloudStorage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	statePath := filepath.Join(s.icloudRoot, syncStateFile)
	if err := saveSyncState(statePath, s.state); err != nil {
		return fmt.Errorf("save icloud sync state: %w", err)
	}
	slog.Debug("iCloud sync state saved", "files", len(s.state.Files))
	return nil
}

// ICloudRoot returns the resolved iCloud Drive directory path.
func (s *ICloudStorage) ICloudRoot() string { return s.icloudRoot }

// TrackedFiles returns the number of files in the sync state.
func (s *ICloudStorage) TrackedFiles() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.state.Files)
}

// TrackedSize returns the total size of all tracked files in bytes.
func (s *ICloudStorage) TrackedSize() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, e := range s.state.Files {
		total += e.Size
	}
	return total
}

// ── Internal ────────────────────────────────────────────────────────────────

// writeToICloud conditionally writes data to the iCloud directory.
// It skips the write if the content hash matches the sync state entry.
func (s *ICloudStorage) writeToICloud(relPath string, data []byte) error {
	hash := computeSHA256(data)
	contentType := classifyContent(relPath)

	s.mu.Lock()
	existing := s.state.Files[relPath]
	s.mu.Unlock()

	if existing != nil && existing.SHA256 == hash {
		slog.Debug("iCloud skip (unchanged)", "path", relPath)
		return nil
	}

	// Conflict resolution for files with changed content.
	if existing != nil {
		action := resolveConflict(contentType, existing, data)
		switch action {
		case conflictSkip:
			slog.Debug("iCloud skip (conflict: keep existing)", "path", relPath, "type", contentType)
			return nil
		case conflictWarn:
			slog.Warn("iCloud overwriting with different content", "path", relPath, "type", contentType,
				"old_size", existing.Size, "new_size", len(data))
		case conflictOverwrite:
			slog.Debug("iCloud updating", "path", relPath, "type", contentType)
		}
	}

	dst := filepath.Join(s.icloudRoot, relPath)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("icloud mkdir: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return fmt.Errorf("icloud write: %w", err)
	}

	s.mu.Lock()
	s.state.Files[relPath] = &SyncFileEntry{
		SHA256:      hash,
		Size:        int64(len(data)),
		ModifiedAt:  time.Now().UTC().Format(time.RFC3339),
		ContentType: contentType,
	}
	s.mu.Unlock()

	slog.Debug("iCloud written", "path", relPath, "size", len(data))
	return nil
}

// CopyFileToICloud copies a file from the local output directory to the
// iCloud directory using streaming I/O. This avoids loading large files
// (e.g., videos) entirely into memory. It computes the SHA-256 hash
// during the copy for sync state tracking.
func (s *ICloudStorage) CopyFileToICloud(relPath string) error {
	srcPath := s.local.AbsPath(relPath)
	dstPath := filepath.Join(s.icloudRoot, relPath)

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	size := srcInfo.Size()
	contentType := classifyContent(relPath)

	// Check sync state for skip.
	s.mu.Lock()
	existing := s.state.Files[relPath]
	s.mu.Unlock()

	if existing != nil && existing.Size == size {
		// Same size — for large files (>50MB), use size heuristic to
		// avoid re-reading the entire file just to compute a hash.
		if size > 50*1024*1024 {
			slog.Debug("iCloud skip (large file, same size)", "path", relPath, "size", size)
			return nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("icloud mkdir: %w", err)
	}

	hash, err := copyFileWithHash(dstPath, srcPath)
	if err != nil {
		return fmt.Errorf("icloud copy: %w", err)
	}

	s.mu.Lock()
	s.state.Files[relPath] = &SyncFileEntry{
		SHA256:      hash,
		Size:        size,
		ModifiedAt:  time.Now().UTC().Format(time.RFC3339),
		ContentType: contentType,
	}
	s.mu.Unlock()

	slog.Debug("iCloud copied", "path", relPath, "size", size)
	return nil
}

// ── Conflict Resolution ────────────────────────────────────────────────────

type conflictAction int

const (
	conflictOverwrite conflictAction = iota
	conflictSkip
	conflictWarn
)

// resolveConflict determines what to do when a file's content has changed
// compared to what's already tracked in the sync state.
func resolveConflict(contentType string, existing *SyncFileEntry, newData []byte) conflictAction {
	newSize := int64(len(newData))

	switch contentType {
	case "video":
		// Videos are expensive to write. If sizes are within 1%, treat as
		// equivalent (encoding variance) and keep the existing file.
		if sizeSimilar(existing.Size, newSize, 0.01) {
			return conflictSkip
		}
		// Substantially different size: overwrite, but warn.
		return conflictWarn

	case "manifest":
		// Manifests are always overwritten (summary of the latest run).
		return conflictOverwrite

	default:
		// Metadata, transcripts, highlights, markdown: overwrite with
		// the newest version (latest scrape is authoritative).
		return conflictOverwrite
	}
}

// sizeSimilar reports whether two sizes are within the given fractional
// tolerance of each other. For example, tolerance=0.01 means within 1%.
func sizeSimilar(a, b int64, tolerance float64) bool {
	if a == 0 && b == 0 {
		return true
	}
	if a == 0 || b == 0 {
		return false
	}
	ratio := math.Abs(float64(a-b)) / math.Max(float64(a), float64(b))
	return ratio <= tolerance
}

// ── iCloud Drive Path Detection ────────────────────────────────────────────

// detectICloudPath returns the default iCloud Drive directory for graindl
// on the current platform. On macOS it uses the well-known iCloud Drive
// path; on other platforms it returns an error.
func detectICloudPath() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("iCloud Drive auto-detection is only supported on macOS; use --icloud-path to specify the directory")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}

	// iCloud Drive root on macOS.
	icloudDrive := filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs")
	if _, err := os.Stat(icloudDrive); err != nil {
		return "", fmt.Errorf("iCloud Drive not found at %s — is iCloud Drive enabled in System Settings?", icloudDrive)
	}

	return filepath.Join(icloudDrive, iCloudSubdir), nil
}

// validateICloudPath checks that a path is absolute, exists (or can be
// created), and is writable.
func validateICloudPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("icloud path must be absolute: %q", path)
	}
	// Check for traversal sequences.
	if containsAny(path, "..") {
		return fmt.Errorf("icloud path must not contain '..': %q", path)
	}
	// Ensure the parent exists and we can write to it.
	parent := filepath.Dir(path)
	if _, err := os.Stat(parent); err != nil {
		return fmt.Errorf("icloud parent dir not accessible: %w", err)
	}
	// Try creating the target dir to verify write permission.
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("icloud path not writable: %w", err)
	}
	return nil
}

// ── File Copy Helper ───────────────────────────────────────────────────────

// copyFileWithHash copies src to dst using streaming I/O and returns the
// hex-encoded SHA-256 hash of the content. The destination file is created
// with 0o600 permissions. This is used for large files (videos) to avoid
// loading the entire content into memory.
func copyFileWithHash(dst, src string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer out.Close()

	h := sha256.New()
	w := io.MultiWriter(out, h)
	if _, err := io.Copy(w, in); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashFileOnDisk computes the SHA-256 hash of a file without loading it
// into memory. Used to hash files that were written by external code
// (e.g., browser video downloads).
func hashFileOnDisk(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var h hash.Hash = sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
