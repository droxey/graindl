package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// ── Storage Interface ───────────────────────────────────────────────────────

// Storage abstracts file operations for the export pipeline.
// Implementations must enforce 0o600 permissions on all written files.
type Storage interface {
	// WriteFile writes raw bytes to relPath under the output root.
	WriteFile(relPath string, data []byte) error
	// WriteJSON marshals v as indented JSON and writes to relPath.
	WriteJSON(relPath string, v any) error
	// FileExists reports whether relPath exists under the output root.
	FileExists(relPath string) bool
	// EnsureDir creates a directory at relPath under the output root.
	EnsureDir(relPath string) error
	// AbsPath returns the absolute filesystem path for relPath.
	AbsPath(relPath string) string
	// SyncExternalFile syncs an externally-written file (e.g., browser
	// video download, ffmpeg audio extraction) to any secondary storage
	// targets. The file must already exist at AbsPath(relPath).
	// No-op for backends without secondary targets. Non-fatal on failure.
	SyncExternalFile(relPath string)
	// Close persists any internal state (e.g., sync state). Called at shutdown.
	Close() error
}

// ── LocalStorage ────────────────────────────────────────────────────────────

// LocalStorage implements Storage by writing directly to a root directory.
// This preserves the existing graindl behavior with 0o600 file permissions.
type LocalStorage struct {
	root string
}

// NewLocalStorage returns a LocalStorage rooted at dir.
func NewLocalStorage(dir string) *LocalStorage {
	return &LocalStorage{root: dir}
}

func (s *LocalStorage) WriteFile(relPath string, data []byte) error {
	abs := filepath.Join(s.root, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(abs, data, 0o600)
}

func (s *LocalStorage) WriteJSON(relPath string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	abs := filepath.Join(s.root, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(abs, data, 0o600)
}

func (s *LocalStorage) FileExists(relPath string) bool {
	_, err := os.Stat(filepath.Join(s.root, relPath))
	return err == nil
}

func (s *LocalStorage) EnsureDir(relPath string) error {
	return os.MkdirAll(filepath.Join(s.root, relPath), 0o755)
}

func (s *LocalStorage) AbsPath(relPath string) string {
	return filepath.Join(s.root, relPath)
}

func (s *LocalStorage) SyncExternalFile(_ string) {} // no secondary target
func (s *LocalStorage) Close() error               { return nil }

// Root returns the storage root directory.
func (s *LocalStorage) Root() string { return s.root }

// ── Sync State ──────────────────────────────────────────────────────────────

// SyncState tracks files that have been written to a target directory,
// enabling incremental updates and conflict resolution.
type SyncState struct {
	Version   int                       `json:"version"`
	UpdatedAt string                    `json:"updated_at"`
	Files     map[string]*SyncFileEntry `json:"files"`
}

// SyncFileEntry records the hash, size, and classification of a synced file.
type SyncFileEntry struct {
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	ModifiedAt  string `json:"modified_at"`
	ContentType string `json:"content_type"` // metadata, transcript, highlights, markdown, video, audio, manifest
}

// NewSyncState creates an empty sync state.
func NewSyncState() *SyncState {
	return &SyncState{
		Version:   1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Files:     make(map[string]*SyncFileEntry),
	}
}

// loadSyncState reads a sync state file from disk.
// Returns a fresh state if the file does not exist. Logs a warning if the
// file exists but contains corrupt JSON, rather than silently resetting.
func loadSyncState(path string) *SyncState {
	data, err := os.ReadFile(path)
	if err != nil {
		return NewSyncState()
	}
	var state SyncState
	if err := json.Unmarshal(data, &state); err != nil {
		slog.Warn("Corrupt sync state file, resetting", "path", path, "error", err)
		return NewSyncState()
	}
	if state.Files == nil {
		state.Files = make(map[string]*SyncFileEntry)
	}
	return &state
}

// saveSyncState writes the sync state to disk with 0o600 permissions.
// Uses atomic temp-file + rename to avoid corruption on crash.
func saveSyncState(path string, state *SyncState) error {
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sync state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp sync state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename sync state: %w", err)
	}
	return nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// computeSHA256 returns the hex-encoded SHA-256 digest of data.
func computeSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// classifyContent maps a file's relative path to a content type string
// based on its extension and name patterns.
func classifyContent(relPath string) string {
	base := filepath.Base(relPath)
	ext := filepath.Ext(relPath)

	if base == "_export-manifest.json" {
		return "manifest"
	}

	switch ext {
	case ".json":
		if containsAny(base, ".highlights") {
			return "highlights"
		}
		return "metadata"
	case ".txt":
		if containsAny(base, ".transcript") {
			return "transcript"
		}
		return "metadata"
	case ".md":
		return "markdown"
	case ".mp4", ".webm":
		return "video"
	case ".m4a":
		return "audio"
	default:
		return "other"
	}
}
