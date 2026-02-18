# Implementation Plan: iCloud Storage Feature

## Overview

Add a one-way "write to iCloud Drive" capability to graindl so exported meetings, transcripts, metadata, and videos are persisted in iCloud. This is **not** two-way sync — it writes to iCloud Drive's local filesystem path, and Apple's daemon handles the cloud upload transparently.

The feature includes **content-hash–based conflict resolution** and **incremental updates** so that:
- Files identical to what's already in iCloud are never rewritten (critical for multi-GB videos)
- Metadata that changed upstream (re-scraped with richer data) replaces the old version
- Two independent runs (different machines, watch mode cycles) can target the same iCloud directory without corruption

---

## Architecture

### Storage Interface

Introduce a `Storage` interface that abstracts all file I/O the `Exporter` performs. The existing bare function calls (`writeFile`, `writeJSON`, `fileExists`, `ensureDir`) become the default `LocalStorage` implementation. A new `ICloudStorage` wraps `LocalStorage` and adds a second write target with dedup/conflict logic.

```
┌──────────┐      ┌───────────┐      ┌──────────────────┐
│ Exporter │─────▶│  Storage   │─────▶│  LocalStorage    │  (current behavior)
│          │      │ interface  │      └──────────────────┘
│          │      │            │─────▶│  ICloudStorage   │  (new)
│          │      └───────────┘      │  ├─ local write   │
│          │                         │  ├─ icloud write  │
│          │                         │  ├─ sync state    │
│          │                         │  └─ conflict res  │
│          │                         └──────────────────┘
└──────────┘
```

When `--icloud` is enabled, `ICloudStorage` performs **two writes**:
1. Normal write to `OutputDir` (preserving existing behavior)
2. Conditional write to the iCloud Drive directory (with incremental/conflict checks)

This means users keep a fast local copy and iCloud acts as durable cloud backup.

### Storage Interface Definition

```go
// Storage abstracts file operations for the export pipeline.
type Storage interface {
    // WriteFile writes raw bytes to a path relative to the output root.
    WriteFile(relPath string, data []byte) error
    // WriteJSON marshals v as indented JSON and writes to relPath.
    WriteJSON(relPath string, v any) error
    // FileExists checks if relPath exists under the output root.
    FileExists(relPath string) bool
    // EnsureDir creates a directory at relPath under the output root.
    EnsureDir(relPath string) error
    // AbsPath returns the absolute path for a relative path.
    AbsPath(relPath string) string
    // Close persists any state (e.g., sync state file). Called at shutdown.
    Close() error
}
```

### Sync State

A `.graindl-sync-state.json` file lives in the iCloud target directory and tracks every file written:

```json
{
  "version": 1,
  "updated_at": "2026-02-18T12:00:00Z",
  "files": {
    "2025-01-15/abc123.json": {
      "sha256": "e3b0c44298fc...",
      "size": 2048,
      "modified_at": "2026-02-18T12:00:00Z",
      "content_type": "metadata"
    },
    "2025-01-15/abc123.mp4": {
      "sha256": "a7ffc6f8bf1e...",
      "size": 104857600,
      "modified_at": "2026-02-18T12:00:00Z",
      "content_type": "video"
    }
  }
}
```

This allows:
- **Skip unchanged files** — compute SHA-256 of new data, compare to state → identical means skip
- **Detect stale files** — file exists on disk but not in state → orphan from another source
- **Resume interrupted syncs** — state only updates after successful write

### Conflict Resolution Strategy

| Content Type | Same Hash | Different Hash | File Missing from State |
|---|---|---|---|
| **Metadata (JSON)** | Skip | Overwrite (newer scrape is authoritative) | Write + record |
| **Transcript** | Skip | Overwrite (latest scrape wins) | Write + record |
| **Highlights** | Skip | Overwrite (latest scrape wins) | Write + record |
| **Markdown** | Skip | Overwrite (derived from metadata) | Write + record |
| **Video (MP4)** | Skip | Keep existing if same size ±1%; overwrite if substantially different | Write + record |
| **Audio (M4A)** | Skip | Overwrite (re-extraction is authoritative) | Write + record |
| **Manifest** | Always overwrite (summary of latest run) | — | — |

**Video special case**: Videos are large and expensive to download. If an existing video in iCloud has a different hash but similar size (within 1%), it's likely the same content re-encoded. In that case, we log a warning and keep the existing file to avoid unnecessary multi-GB writes. If sizes differ substantially, the new version wins.

### iCloud Drive Path Detection

On macOS, iCloud Drive is a well-known directory:
```
~/Library/Mobile Documents/com~apple~CloudDocs/
```

graindl creates a subdirectory within it:
```
~/Library/Mobile Documents/com~apple~CloudDocs/graindl/
```

On Linux/Docker, there is no native iCloud Drive. Users must provide `--icloud-path` explicitly (useful for testing or NAS-mounted iCloud directories).

Detection logic:
1. If `--icloud-path` is set → use it directly
2. If `runtime.GOOS == "darwin"` → auto-detect `~/Library/Mobile Documents/com~apple~CloudDocs/`
3. Verify the path exists and is writable → fail fast with clear error if not

---

## New Files

### 1. `storage.go` — Storage interface, LocalStorage, SyncState types

**Contents:**
- `Storage` interface (as defined above)
- `LocalStorage` struct — wraps existing `writeFile`/`writeJSON`/`fileExists`/`ensureDir` functions, scoped to a root directory
- `SyncState` / `SyncFileEntry` structs for serialization
- `computeSHA256(data []byte) string` helper
- `loadSyncState(path string) (*SyncState, error)`
- `saveSyncState(path string, state *SyncState) error`

### 2. `storage_test.go` — Unit tests for storage layer

**Tests:**
- `TestLocalStorage_WriteFile` — writes, reads back, verifies content and 0o600 perms
- `TestLocalStorage_WriteJSON` — marshals, reads back, verifies structure
- `TestLocalStorage_FileExists` — true for existing, false for missing
- `TestSyncState_LoadSave` — roundtrip serialization
- `TestSyncState_EmptyFile` — graceful handling of missing/empty state file
- `TestComputeSHA256` — known test vectors

### 3. `icloud.go` — ICloudStorage implementation, path detection, conflict resolution

**Contents:**
- `ICloudStorage` struct — embeds `LocalStorage` (for local writes) + iCloud target path + sync state
- `NewICloudStorage(localRoot, icloudRoot string) (*ICloudStorage, error)` — loads sync state, validates paths
- Implements all `Storage` interface methods
- `detectICloudPath() (string, error)` — platform-aware path detection
- `resolveConflict(relPath string, newData []byte, entry *SyncFileEntry) ConflictAction` — determines skip/overwrite/warn
- `classifyContent(relPath string) string` — maps file extension to content type (metadata, video, transcript, etc.)
- `iCloudAvailable() bool` — checks if iCloud Drive directory exists and is writable

### 4. `icloud_test.go` — Unit tests for iCloud storage

**Tests:**
- `TestDetectICloudPath_Darwin` — validates path construction on macOS (uses build tag or mock)
- `TestDetectICloudPath_Linux` — expects error without explicit path
- `TestICloudStorage_IncrementalSkip` — writes file, writes same data again → second write is skipped
- `TestICloudStorage_IncrementalUpdate` — writes file, writes different data → file is updated
- `TestICloudStorage_ConflictResolution_Video` — video with similar size kept, different size overwritten
- `TestICloudStorage_ConflictResolution_Metadata` — newer metadata always wins
- `TestICloudStorage_SyncStatePersistedOnClose` — state file written on Close()
- `TestICloudStorage_BothLocationsWritten` — verifies file exists in both local and iCloud dirs
- `TestClassifyContent` — extension → content type mapping

---

## Modified Files

### 5. `models.go` — Config and type additions

**Changes:**
- Add to `Config` struct:
  ```go
  ICloud     bool   // --icloud: enable iCloud storage
  ICloudPath string // --icloud-path: override auto-detected iCloud Drive path
  ```
- Add `SyncState` and `SyncFileEntry` types (or these go in `storage.go`)

### 6. `main.go` — Flag registration and validation

**Changes:**
- Register new flags:
  ```go
  flag.BoolVar(&cfg.ICloud, "icloud", envBool(dotenv, "GRAIN_ICLOUD"), "Copy exports to iCloud Drive")
  flag.StringVar(&cfg.ICloudPath, "icloud-path", envGet(dotenv, "GRAIN_ICLOUD_PATH"), "Custom iCloud Drive path (auto-detected on macOS)")
  ```
- Validation block after flag parsing:
  - If `--icloud` is set without `--icloud-path` on non-Darwin, emit error
  - Detect and validate iCloud path
  - Log resolved iCloud path at startup

### 7. `export.go` — Integrate Storage interface

**Changes:**
- Add `storage Storage` field to `Exporter` struct
- `NewExporter` initializes the appropriate `Storage` implementation based on config:
  - `--icloud` enabled → `NewICloudStorage(outputDir, icloudPath)`
  - Otherwise → `NewLocalStorage(outputDir)`
- Replace direct calls in `exportOne` and write helpers:
  - `writeJSON(path, v)` → `e.storage.WriteJSON(relPath, v)`
  - `writeFile(path, data)` → `e.storage.WriteFile(relPath, data)`
  - `fileExists(metaPath)` → `e.storage.FileExists(relPath)`
  - `ensureDir(dir)` → `e.storage.EnsureDir(relPath)`
- Update `relPath()` to delegate to `e.storage.AbsPath()` where needed
- `Exporter.Close()` calls `e.storage.Close()` to persist sync state
- Manifest write also goes through storage interface

### 8. `models_test.go` / `export_test.go` — Update existing tests

**Changes:**
- Tests that construct `Exporter` directly need to provide a `Storage` instance (use `LocalStorage`)
- Add test for iCloud-enabled exporter writing to both locations

---

## Incremental Sync Flow (per file)

```
exportOne() writes a file:
  │
  ├─ Compute SHA-256 of data in memory
  │
  ├─ LocalStorage.WriteFile(relPath, data)    ← always write locally
  │
  ├─ [iCloud enabled?]
  │   │
  │   ├─ Check SyncState for relPath
  │   │   │
  │   │   ├─ Entry exists AND hash matches → SKIP (log debug)
  │   │   │
  │   │   ├─ Entry exists AND hash differs → resolveConflict()
  │   │   │   ├─ Video: check size similarity → keep existing or overwrite
  │   │   │   └─ Other: overwrite (newer is authoritative)
  │   │   │
  │   │   └─ No entry → new file, write unconditionally
  │   │
  │   ├─ Write to iCloud path
  │   ├─ Update SyncState entry (hash, size, timestamp, content_type)
  │   └─ [On Close()] persist SyncState to disk
  │
  └─ Return
```

---

## Video-Specific Incremental Logic

Videos are the largest files (often 100MB–2GB) and the most expensive to write. Special handling:

1. **Hash check first**: If the video bytes hash matches what's in sync state, skip entirely.
2. **Size heuristic for existing files**: If the video on disk has a different hash but size is within 1% of the new file, treat as equivalent (encoding variance). Log a warning and skip.
3. **Large file streaming hash**: For videos written via `Browser.DownloadVideo()` (which writes directly to disk), compute SHA-256 by reading the file after download rather than holding bytes in memory.
4. **Post-download copy**: After local video download completes, copy to iCloud using buffered I/O (64KB chunks) rather than reading the entire file into memory.

```go
// copyFile streams src to dst without loading into memory.
func copyFile(dst, src string) error {
    in, err := os.Open(src)
    if err != nil {
        return err
    }
    defer in.Close()
    out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
    if err != nil {
        return err
    }
    defer out.Close()
    _, err = io.Copy(out, in)
    return err
}
```

---

## CLI Interface

### New Flags

| Flag | Env Var | Default | Description |
|---|---|---|---|
| `--icloud` | `GRAIN_ICLOUD` | `false` | Enable copying exports to iCloud Drive |
| `--icloud-path` | `GRAIN_ICLOUD_PATH` | auto-detect | Custom iCloud Drive directory path |

### Examples

```bash
# Auto-detect iCloud Drive on macOS
graindl --icloud --output ./recordings

# Custom iCloud path (Linux, NAS, testing)
graindl --icloud --icloud-path /mnt/nas/icloud-mirror --output ./recordings

# Watch mode with iCloud (incremental across cycles)
graindl --watch --icloud --interval 30m

# Env vars
GRAIN_ICLOUD=true GRAIN_ICLOUD_PATH=/custom/path graindl
```

### Startup Output

```
✓ graindl v1.2.0
✓ Output: /Users/droxey/recordings
✓ iCloud: /Users/droxey/Library/Mobile Documents/com~apple~CloudDocs/graindl
✓ iCloud sync state: 142 files tracked, 3.2 GB synced
```

---

## Error Handling

| Scenario | Behavior |
|---|---|
| iCloud path doesn't exist | Fatal error at startup with clear message |
| iCloud path not writable | Fatal error at startup (permission check) |
| iCloud write fails mid-export | Log error, continue with local-only (non-fatal) |
| Sync state file corrupted | Log warning, start fresh state (treat all files as new) |
| Sync state file locked | Retry with backoff (another graindl instance may be writing) |
| Disk full on iCloud volume | Log error per file, continue local exports |

iCloud write failures are **non-fatal** — local export always succeeds. This ensures iCloud issues never block the primary export workflow.

---

## Security Considerations

- **File permissions**: All iCloud files written with `0o600` (matching existing convention)
- **Sync state file**: Written with `0o600`, contains only paths and hashes (no secrets)
- **Path validation**: iCloud path validated against directory traversal (must be absolute, no `..`)
- **No credentials**: No Apple ID or iCloud API credentials needed — we write to the filesystem; Apple's daemon handles cloud sync
- **Manifest paths**: iCloud manifest uses relative paths (same as local, per SEC-8)

---

## Compatibility

- **macOS**: Full auto-detection of iCloud Drive path. Requires iCloud Drive enabled in System Settings.
- **Linux/Docker**: Requires explicit `--icloud-path`. Useful for NAS mounts or testing.
- **Existing behavior**: Zero changes when `--icloud` is not set. All existing tests pass unmodified.
- **Watch mode**: Fully compatible. Each cycle's incremental logic benefits from sync state — previously synced files are skipped instantly.
- **Parallel mode**: Safe. `ICloudStorage` uses a mutex for sync state updates. File writes to different paths don't conflict.

---

## Implementation Order

1. **`storage.go`** — Interface + `LocalStorage` + SHA-256 helper + sync state types
2. **`storage_test.go`** — Tests for `LocalStorage` and sync state serialization
3. **`icloud.go`** — `ICloudStorage` implementation with conflict resolution
4. **`icloud_test.go`** — Tests for incremental/conflict/detection logic
5. **`models.go`** — Add `ICloud`/`ICloudPath` to `Config`
6. **`main.go`** — Register flags, validation, startup logging
7. **`export.go`** — Replace bare file ops with `Storage` interface calls
8. **`export_test.go`** / **`models_test.go`** — Update tests for new `Storage` field
9. **Final pass**: `make test`, `make vet`, verify all tests pass

---

## Dependencies

**None**. This feature uses only Go standard library:
- `crypto/sha256` — content hashing
- `encoding/hex` — hash string encoding
- `io` — streaming file copy
- `os` — filesystem operations
- `runtime` — platform detection (`runtime.GOOS`)
- `sync` — mutex for concurrent sync state access
- `path/filepath` — path manipulation

No new external dependencies required — consistent with the project's single-dependency philosophy.
