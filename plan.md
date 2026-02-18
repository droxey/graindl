# Google Drive Save Feature — Implementation Plan

## Summary

Add the ability to upload exported meeting data (metadata, transcripts, highlights, markdown, video, audio) to Google Drive after local export completes. The feature works as a **post-export upload** rather than a storage abstraction, because ffmpeg and Rod's browser download APIs fundamentally require local filesystem paths.

---

## Design Decisions

### Why Post-Export Upload (Not a Storage Interface)

The codebase has two file I/O bottleneck functions (`writeJSON`, `writeFile` in `models.go`), which initially suggests a clean interface abstraction. However, three constraints make a storage interface impractical:

1. **Rod's `MustWaitDownload`** writes video data to a local path via browser DevTools — no streaming API.
2. **`fetchViaJS`** decodes base64 video data in-process and writes locally.
3. **ffmpeg** (`audio.go`) requires local input/output paths for audio extraction.

A post-export upload is the correct architecture: export locally as today, then upload the resulting files to Google Drive.

### Authentication: OAuth2 User Flow + Service Account

Support two authentication modes:
- **Service Account** (headless/Docker): JSON key file, no user interaction. Preferred for `--headless` and watch mode.
- **User OAuth2** (interactive): Browser-based consent flow with token caching. Natural for interactive use since graindl already launches a browser.

### Incremental Sync Strategy

A hybrid approach that balances speed and correctness:

1. **Local sync state file** (`.grain-session/gdrive-sync.json`) tracks every uploaded file's Drive ID, MD5 checksum, size, and timestamps. This is the fast path — no API calls needed to determine what's changed.
2. **Drive-side verification** (`--gdrive-verify` flag) queries the Drive API to reconcile state against reality. Handles edge cases like files deleted from Drive externally or uploaded by another instance.

The local state file handles 99% of runs efficiently. The verify flag is for when the user suspects drift.

### Conflict Resolution Strategy

Three modes controlled by `--gdrive-conflict`:

| Mode | Behavior | Use Case |
|------|----------|----------|
| `local-wins` (default) | Local version always overwrites Drive | Standard export tool behavior — graindl is the source of truth |
| `skip` | Skip upload if file already exists on Drive | Preserve manual edits made on Drive |
| `newer-wins` | Compare local mtime vs last upload time, upload only if local is newer | Balanced approach for shared workflows |

For an export tool, `local-wins` is the correct default — the scraped data from Grain is authoritative.

### New Dependency

Adding the official Google Drive SDK:
- `google.golang.org/api/drive/v3`
- `golang.org/x/oauth2/google`

These are well-maintained, official Google packages. This brings the external dependency count from 1 to 3 (plus transitive deps).

---

## New Files

### `gdrive.go` — Google Drive Client

```
~450-550 lines
```

**Structs:**
```go
type DriveUploader struct {
    srv       *drive.Service
    folderID  string              // root target folder on Drive
    folderMap map[string]string   // cache: local relative dir → Drive folder ID
    state     *SyncState          // incremental sync state
    statePath string              // path to gdrive-sync.json
    conflict  string              // "local-wins" | "skip" | "newer-wins"
    mu        sync.Mutex          // protects folderMap and state
}

// Persisted to .grain-session/gdrive-sync.json
type SyncState struct {
    Version  int                   `json:"version"`    // Schema version (1) for forward compat
    LastSync string                `json:"last_sync"`  // RFC3339 timestamp of last completed sync
    FolderID string                `json:"folder_id"`  // Root Drive folder — detect if user changes target
    Files    map[string]*SyncEntry `json:"files"`      // key: relative path from OutputDir
}

type SyncEntry struct {
    DriveFileID  string `json:"drive_file_id"`   // Drive file ID for updates
    MD5Checksum  string `json:"md5_checksum"`     // MD5 of local file at upload time
    Size         int64  `json:"size"`             // File size in bytes at upload time
    LocalModTime string `json:"local_mod_time"`   // RFC3339 mtime of local file at upload time
    UploadedAt   string `json:"uploaded_at"`       // RFC3339 when upload completed
}
```

**Functions:**

| Function | Purpose |
|----------|---------|
| `NewDriveUploader(ctx, cfg) (*DriveUploader, error)` | Initializes auth, creates `drive.Service`, loads sync state |
| `authServiceAccount(ctx, credPath) (*drive.Service, error)` | Service account auth from JSON key file |
| `authUserOAuth2(ctx, credPath, tokenPath) (*drive.Service, error)` | User OAuth2 with token caching |
| `saveToken(path, token) error` | Persist OAuth2 token to disk |
| `(d *DriveUploader) Upload(ctx, localPath, remotePath) (string, error)` | Upload single file with sync-aware logic, returns Drive file ID |
| `(d *DriveUploader) shouldUpload(localPath, relPath) (action string, entry *SyncEntry)` | Decides: `"create"`, `"update"`, or `"skip"` based on sync state + conflict mode |
| `(d *DriveUploader) EnsureFolder(ctx, relativePath) (string, error)` | Create folder hierarchy on Drive, returns folder ID |
| `(d *DriveUploader) UploadExportResult(ctx, outputDir, result) error` | Upload all files from an ExportResult (sync-aware) |
| `(d *DriveUploader) UploadManifest(ctx, outputDir, manifestPath) error` | Upload the export manifest |
| `(d *DriveUploader) Verify(ctx, outputDir) (*VerifyReport, error)` | Query Drive API, reconcile with sync state, re-upload as needed |
| `loadSyncState(path) (*SyncState, error)` | Load state from JSON file (or return empty state if not found) |
| `(d *DriveUploader) saveSyncState() error` | Persist state to disk via `writeFile()` with 0o600 |
| `md5File(path) (string, error)` | Compute MD5 hex digest of a local file |

**Upload decision logic (`shouldUpload`):**
```
1. Compute MD5 of local file
2. Look up relative path in sync state
3. If not in state → return "create" (new file)
4. If in state AND md5 matches → return "skip" (unchanged)
5. If in state AND md5 differs → apply conflict strategy:
   - "local-wins" → return "update"
   - "skip"       → return "skip"
   - "newer-wins" → compare local mtime vs entry.UploadedAt:
       local is newer  → return "update"
       local is older  → return "skip"
6. After successful upload → update sync state entry in memory
7. saveSyncState() called once after all uploads in a batch complete
```

**Drive-side verification (`Verify`):**
```
1. List all files in Drive target folder (recursive, paginated)
2. Build map: filename → {driveID, md5Checksum}
3. For each entry in sync state:
   - Not found on Drive → mark "deleted-remotely", re-upload
   - Found, md5 matches → confirmed in sync
   - Found, md5 differs → "modified-remotely", apply conflict strategy
4. For each file on Drive not in sync state:
   - Log as "untracked" (informational only, no action)
5. Return VerifyReport with counts of each category
```

**Upload behavior:**
- Resumable uploads for files > 5MB (uses Drive API's resumable upload)
- MIME type detection from file extension
- Retry with exponential backoff (3 attempts) for transient errors
- Creates new file on Drive when `shouldUpload` returns `"create"`
- Updates existing file (by `DriveFileID` from sync state) when `shouldUpload` returns `"update"`

**Folder structure on Drive mirrors local:**
```
<Target Folder>/
├── 2025-01-15/
│   ├── meeting-id-1.json
│   ├── meeting-id-1.transcript.txt
│   ├── meeting-id-1.highlights.json
│   ├── meeting-id-1.md
│   └── meeting-id-1.mp4
├── 2025-01-14/
│   └── ...
└── _export-manifest.json
```

### `gdrive_test.go` — Tests

```
~350-400 lines
```

**Sync state tests:**
- `TestShouldUpload_NewFile` — file not in state returns `"create"`
- `TestShouldUpload_Unchanged` — matching MD5 returns `"skip"`
- `TestShouldUpload_Modified_LocalWins` — different MD5 with `local-wins` returns `"update"`
- `TestShouldUpload_Modified_Skip` — different MD5 with `skip` returns `"skip"`
- `TestShouldUpload_Modified_NewerWins_LocalNewer` — local mtime > uploadedAt returns `"update"`
- `TestShouldUpload_Modified_NewerWins_LocalOlder` — local mtime < uploadedAt returns `"skip"`
- `TestSyncState_LoadSave` — round-trip serialize/deserialize
- `TestSyncState_FolderIDChange` — detects target folder change, resets state
- `TestSyncState_MissingFile` — missing state file returns empty state (not error)

**Verify tests:**
- `TestVerify_DeletedRemotely` — file in state but not on Drive triggers re-upload
- `TestVerify_ModifiedRemotely_LocalWins` — Drive md5 differs, re-uploads local
- `TestVerify_ModifiedRemotely_Skip` — Drive md5 differs, skips with `skip` mode
- `TestVerify_UntrackedOnDrive` — file on Drive not in state, logged as informational

**Core tests:**
- Test folder creation logic (cache hits, nested paths)
- Test retry behavior on transient errors
- Test MIME type detection
- Mock Drive API via `httptest` server (same pattern as `export_test.go`)
- Test auth mode selection (service account vs user OAuth2)
- Test `md5File` correctness

---

## Modified Files

### `models.go` — Config + ExportResult Changes

**Config struct additions** (8 new fields):
```go
type Config struct {
    // ... existing fields ...

    // Google Drive upload
    GDrive            bool   // Enable Google Drive upload
    GDriveFolderID    string // Target folder ID on Drive
    GDriveCredentials string // Path to credentials JSON (OAuth2 client or service account)
    GDriveTokenFile   string // Path to cached OAuth2 token (user flow only)
    GDriveCleanLocal  bool   // Remove local files after successful upload
    GDriveServiceAcct bool   // Use service account auth (vs user OAuth2)
    GDriveConflict    string // Conflict resolution: "local-wins" (default), "skip", "newer-wins"
    GDriveVerify      bool   // Force Drive-side verification before uploading
}
```

**ExportResult additions** (4 new fields):
```go
type ExportResult struct {
    // ... existing fields ...
    DriveUploaded bool   `json:"drive_uploaded,omitempty"`
    DriveSkipped  int    `json:"drive_skipped,omitempty"`  // Files skipped (already in sync)
    DriveUpdated  int    `json:"drive_updated,omitempty"`  // Files updated (conflict resolved)
    DriveError    string `json:"drive_error,omitempty"`
}
```

### `main.go` — CLI Flags + Env Vars

**New flags** (lines ~93-112, extend the flag block):
```
-gdrive                 Enable Google Drive upload after export
-gdrive-folder-id       Target Google Drive folder ID
-gdrive-credentials     Path to Google OAuth2/service-account credentials JSON
-gdrive-token           Path to cached OAuth2 token file (default: .grain-session/gdrive-token.json)
-gdrive-clean-local     Remove local files after successful Drive upload
-gdrive-service-account Use service account authentication
-gdrive-conflict        Conflict resolution mode: "local-wins" (default), "skip", "newer-wins"
-gdrive-verify          Force Drive-side verification before uploading (slower, more accurate)
```

**Env var equivalents:**
```
GRAIN_GDRIVE              → GDrive
GRAIN_GDRIVE_FOLDER_ID    → GDriveFolderID
GRAIN_GDRIVE_CREDENTIALS  → GDriveCredentials
GRAIN_GDRIVE_TOKEN        → GDriveTokenFile
GRAIN_GDRIVE_CLEAN_LOCAL  → GDriveCleanLocal
GRAIN_GDRIVE_SERVICE_ACCT → GDriveServiceAcct
GRAIN_GDRIVE_CONFLICT     → GDriveConflict
GRAIN_GDRIVE_VERIFY       → GDriveVerify
```

**Validation** (in config resolution block):
- If `--gdrive` is set, `--gdrive-folder-id` and `--gdrive-credentials` are required.
- `--gdrive-conflict` must be one of `"local-wins"`, `"skip"`, `"newer-wins"` (default: `"local-wins"`).
- Error early with clear message if missing or invalid.

### `export.go` — Integration Points

**Exporter struct** (add field):
```go
type Exporter struct {
    // ... existing fields ...
    drive *DriveUploader // nil when --gdrive is not set
}
```

**`NewExporter()`** — Initialize DriveUploader if `--gdrive` is enabled:
```go
if cfg.GDrive {
    d, err := NewDriveUploader(context.Background(), cfg)
    if err != nil {
        return nil, fmt.Errorf("google drive init: %w", err)
    }
    exp.drive = d
}
```

**`Run()`** — Verify + upload lifecycle:
```go
// Before export loop, if --gdrive-verify:
if e.drive != nil && e.cfg.GDriveVerify {
    report, err := e.drive.Verify(ctx, e.cfg.OutputDir)
    if err != nil {
        slog.Warn("Drive verification failed", "error", err)
    } else {
        slog.Info("Drive verification complete",
            "in_sync", report.InSync,
            "re_uploaded", report.ReUploaded,
            "deleted_remotely", report.DeletedRemotely,
            "modified_remotely", report.ModifiedRemotely,
            "untracked", report.Untracked)
    }
}
```

**`exportOne()`** — After all local writes complete, sync-aware upload to Drive:
```go
// At end of exportOne(), after all writeMetadata/writeTranscript/etc:
if e.drive != nil {
    stats, err := e.drive.UploadExportResult(ctx, e.cfg.OutputDir, r)
    if err != nil {
        slog.Warn("Drive upload failed", "id", ref.ID, "error", err)
        r.DriveError = err.Error()
    } else {
        r.DriveUploaded = true
        r.DriveSkipped = stats.Skipped
        r.DriveUpdated = stats.Updated
        slog.Info("Synced to Google Drive", "id", ref.ID,
            "created", stats.Created, "updated", stats.Updated, "skipped", stats.Skipped)
        if e.cfg.GDriveCleanLocal {
            e.cleanLocalFiles(r)
        }
    }
}
```

**`Run()`** — After manifest is written, upload it + persist sync state:
```go
// After writeJSON for manifest:
if e.drive != nil {
    manifestPath := filepath.Join(e.cfg.OutputDir, "_export-manifest.json")
    if err := e.drive.UploadManifest(ctx, e.cfg.OutputDir, manifestPath); err != nil {
        slog.Warn("Drive manifest upload failed", "error", err)
    }
    // Persist sync state to disk after all uploads complete
    if err := e.drive.saveSyncState(); err != nil {
        slog.Warn("Failed to save Drive sync state", "error", err)
    }
}
```

**New helper** `cleanLocalFiles(r *ExportResult)`:
- Remove all local files referenced in ExportResult paths
- Remove empty date directories after cleanup
- Log each removal

### `Makefile` — No Changes Required

The existing `make build` and `make test` targets use `./...` which will pick up new files automatically.

### `go.mod` — New Dependencies

```
require (
    github.com/go-rod/rod v0.114.8
    google.golang.org/api v0.x.x
    golang.org/x/oauth2 v0.x.x
)
```

---

## Implementation Order

1. **`models.go`** — Add Config fields and ExportResult fields
2. **`main.go`** — Add CLI flags, env vars, validation (including `--gdrive-conflict` enum check)
3. **`gdrive.go`** — Implement in layers:
   - a. `SyncState` / `SyncEntry` types, `loadSyncState`, `saveSyncState`, `md5File`
   - b. Auth (`authServiceAccount`, `authUserOAuth2`, `saveToken`)
   - c. Folder management (`EnsureFolder` with cache)
   - d. Core upload (`Upload`, `shouldUpload` with conflict resolution)
   - e. Batch operations (`UploadExportResult`, `UploadManifest`)
   - f. Verification (`Verify` with Drive API listing + reconciliation)
4. **`gdrive_test.go`** — Tests for each layer (sync state, conflict modes, verify)
5. **`export.go`** — Wire DriveUploader into Exporter lifecycle (verify, upload, state persist)
6. **`main_test.go`** — Add tests for new config resolution and conflict mode validation
7. Run `make test` to verify everything passes
8. Run `make vet` and `make lint`

---

## Security Considerations

- **Credentials file permissions**: Warn if credentials JSON has permissions wider than `0o600`
- **Token storage**: Saved OAuth2 tokens written with `0o600` via `writeFile()`
- **Token file location**: Default inside `.grain-session/` (already in `.gitignore`)
- **Sync state file**: Written with `0o600` inside `.grain-session/` — contains Drive file IDs (not sensitive, but keeping private is good practice)
- **Atomic state writes**: `saveSyncState` writes to a temp file then renames, preventing corruption if the process is killed mid-write
- **No credentials in logs**: Never log credential contents, only file paths
- **Folder ID validation**: Validate `--gdrive-folder-id` format before API calls
- **Scopes**: Request minimal scope — `drive.file` (access only files created by the app) rather than full `drive` scope. Note: `drive.file` scope is sufficient for incremental sync since we only read/write files created by this app. The `--gdrive-verify` listing only sees files created by this app's OAuth client.

---

## Error Handling Strategy

- Drive upload failures **never** fail the overall export — data is safe on local disk
- Each ExportResult tracks `drive_uploaded` (bool) and `drive_error` (string)
- Transient API errors (429, 500, 503) retry 3 times with exponential backoff
- Auth failures fail fast at startup (in `NewExporter`) — don't wait until first upload
- Context cancellation (SIGINT/SIGTERM) aborts in-progress uploads gracefully

---

## User Experience

### Basic usage (interactive):
```bash
graindl --output ./recordings \
        --gdrive \
        --gdrive-folder-id "1ABC...xyz" \
        --gdrive-credentials ./client_secret.json
# First run: opens browser for OAuth2 consent, caches token
# Subsequent runs: uses cached token
```

### Headless/Docker usage (service account):
```bash
graindl --output /data \
        --headless \
        --gdrive \
        --gdrive-service-account \
        --gdrive-folder-id "1ABC...xyz" \
        --gdrive-credentials ./service-account.json
```

### With local cleanup:
```bash
graindl --output ./recordings \
        --gdrive \
        --gdrive-folder-id "1ABC...xyz" \
        --gdrive-credentials ./creds.json \
        --gdrive-clean-local
# Uploads to Drive, then removes local copies
```

### Incremental sync (default — only uploads changed files):
```bash
graindl --output ./recordings \
        --gdrive \
        --gdrive-folder-id "1ABC...xyz" \
        --gdrive-credentials ./creds.json
# First run: uploads everything, creates sync state
# Second run: skips files with matching MD5, uploads only new/changed files
# Log output: "Synced to Google Drive  id=abc123  created=2  updated=0  skipped=5"
```

### Preserve manual edits on Drive:
```bash
graindl --output ./recordings \
        --gdrive \
        --gdrive-folder-id "1ABC...xyz" \
        --gdrive-credentials ./creds.json \
        --gdrive-conflict skip
# If someone renamed/edited a file on Drive, the local version won't overwrite it
```

### Force verification against Drive:
```bash
graindl --output ./recordings \
        --gdrive \
        --gdrive-folder-id "1ABC...xyz" \
        --gdrive-credentials ./creds.json \
        --gdrive-verify
# Queries Drive API to find deleted/modified files, re-uploads as needed
# Log output: "Drive verification complete  in_sync=42  re_uploaded=1  deleted_remotely=1  ..."
```

### Env-based (Docker Compose):
```yaml
environment:
  - GRAIN_GDRIVE=true
  - GRAIN_GDRIVE_SERVICE_ACCT=true
  - GRAIN_GDRIVE_FOLDER_ID=1ABC...xyz
  - GRAIN_GDRIVE_CREDENTIALS=/run/secrets/gdrive-sa.json
  - GRAIN_GDRIVE_CONFLICT=local-wins
```

---

## Sync State Lifecycle

### State file: `.grain-session/gdrive-sync.json`

```json
{
  "version": 1,
  "last_sync": "2025-01-15T10:30:00Z",
  "folder_id": "1ABC...xyz",
  "files": {
    "2025-01-15/meeting-abc123.json": {
      "drive_file_id": "1DEF...uvw",
      "md5_checksum": "d41d8cd98f00b204e9800998ecf8427e",
      "size": 4096,
      "local_mod_time": "2025-01-15T10:28:00Z",
      "uploaded_at": "2025-01-15T10:29:30Z"
    },
    "2025-01-15/meeting-abc123.transcript.txt": {
      "drive_file_id": "1GHI...rst",
      "md5_checksum": "098f6bcd4621d373cade4e832627b4f6",
      "size": 12800,
      "local_mod_time": "2025-01-15T10:28:01Z",
      "uploaded_at": "2025-01-15T10:29:31Z"
    }
  }
}
```

### State management rules:

1. **Load at startup**: `NewDriveUploader` loads state from disk (or starts empty if file doesn't exist)
2. **Folder ID change detection**: If `--gdrive-folder-id` doesn't match `state.FolderID`, log a warning and reset state (user switched target folders — all files need re-upload)
3. **Update in memory**: Each successful upload updates the corresponding `SyncEntry` in memory
4. **Persist once per run**: `saveSyncState()` called at end of `Run()`, not after every file (avoids excessive disk writes)
5. **Atomic write**: Write to temp file + rename to prevent corruption on crash
6. **Location**: Inside `.grain-session/` which is already in `.gitignore` and created with `0o700` permissions

### Watch mode considerations:

In `--watch` mode, sync state is persisted after each polling cycle (each call to `Run()`). This means if the process crashes mid-cycle, at most one cycle's worth of uploads might be re-uploaded on the next run — harmless since duplicates are detected by name and updated rather than creating new files.

---

## Verify Report

The `--gdrive-verify` flag produces a `VerifyReport`:

```go
type VerifyReport struct {
    InSync           int // Files confirmed in sync (local state matches Drive)
    ReUploaded       int // Files re-uploaded (deleted from Drive, restored)
    DeletedRemotely  int // Files that were missing from Drive
    ModifiedRemotely int // Files with different content on Drive
    Untracked        int // Files on Drive not in local sync state
}
```

This is logged as a structured `slog.Info` message. The report is informational — verification automatically takes corrective action (re-upload deleted files, apply conflict strategy for modified files).

---

## Scope Boundaries (What This Does NOT Include)

- **Downloading from Drive**: This is upload-only. No pulling content from Drive to local disk.
- **Content-level merge**: Conflict resolution operates at the file level. There is no merging of JSON fields or transcript lines. A file is either uploaded or skipped as a whole.
- **Shared Drive support**: Uses regular Drive. Shared Drive (Team Drive) support can be added later via `supportsAllDrives` parameter.
- **Bidirectional sync**: Changes on Drive are detected (via `--gdrive-verify`) but never pulled down. Drive is a destination, not a source.
- **Other cloud providers**: Only Google Drive. The architecture is clean enough that S3/Azure/etc. could be added later following the same pattern, but that's out of scope.
