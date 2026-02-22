# CLAUDE.md

This file provides guidance for AI assistants working with the graindl codebase.

## Project Overview

**graindl** is a Go CLI tool that exports meetings, transcripts, metadata, and videos from [Grain](https://grain.com). It uses browser-based automation (Chromium via the Rod library) for login, meeting discovery, search filtering, page scraping, and video downloads.

## Repository Structure

All source code lives in the root directory as a single `main` package:

```
main.go        - CLI entry point, flag parsing, .env loading, signal handling
models.go      - Type definitions (Config, MeetingRef, ExportResult, Metadata, Highlight)
export.go      - Exporter orchestrator: discovery, per-meeting export, manifest generation
browser.go     - Rod/Chromium wrapper: login, meeting discovery, page scraping, video download
search.go      - Browser-based search: navigates Grain search UI, extracts results
storage.go     - Storage interface + LocalStorage; SyncState for incremental cloud sync
gdrive.go      - Google Drive REST API client (stdlib-only, no SDK); OAuth2 + service account
icloud.go      - iCloud Drive storage backend (macOS only); copies exports to iCloud folder
logger.go      - Custom slog.Handler with ANSI color output (also supports JSON via --log-format)
throttle.go    - Rate limiter using crypto/rand for random delays in [Min, Max)
audio.go       - Audio extraction via ffmpeg (used by --audio-only mode)
format.go      - Markdown output formatting for Obsidian/Notion export
watch.go       - Watch mode: continuous polling loop with healthcheck support
```

Test files follow the `_test.go` convention and mirror source files:

```
main_test.go       - .env loading, config resolution
models_test.go     - Sanitization, metadata building, highlight parsing
export_test.go     - Integration tests for export pipeline (httptest servers)
storage_test.go    - Storage interface, LocalStorage, SyncState round-trip tests
gdrive_test.go     - DriveUploader: auth, upload, sync state, conflict resolution
icloud_test.go     - ICloudStorage: write, conflict, sync state, path detection
logger_test.go     - Color formatting
search_test.go     - UUID parsing, search result extraction
throttle_test.go   - Random delay distribution
audio_test.go      - Audio extraction tests
format_test.go     - Markdown formatting tests
watch_test.go      - Watch mode polling loop tests
```

Other key files:

```
Makefile             - Build automation (build, test, vet, lint, verify, clean, docker)
Dockerfile           - Multi-stage build (golang:1.23-alpine -> alpine:3.20)
docker-compose.yml   - Docker Compose service definition with resource limits
convert_hls.sh       - HLS-to-MP4 conversion script (post-export)
go.mod / go.sum      - Go module (github.com/droxey/graindl, Go 1.23)
README.md            - User-facing documentation
REVIEW.md            - Code review report (Go engineering + ops findings; security and architecture)
LICENSE              - MIT
.gitignore           - Excludes binary, recordings/, .grain-session/, .env, media
.dockerignore        - Minimizes Docker build context
.github/workflows/   - CI pipeline (go vet, go test -race, go mod verify)
```

## Build & Development Commands

```bash
make build     # Build static binary (CGO_ENABLED=0) with git version/commit via ldflags
make test      # Run tests with -race detector (go test -count=1 -race ./...)
make vet       # Run go vet
make lint      # Run golangci-lint (optional, graceful fallback if not installed)
make verify    # Run go mod verify (dependency integrity)
make clean     # Remove binary
make docker    # Build Docker image tagged with git version
```

The binary is built with `-ldflags "-X main.version=... -X main.commit=..."` to embed version info from git.

## Testing

Run all tests:

```bash
make test
# or equivalently:
go test -count=1 -race ./...
```

For verbose output:

```bash
go test -v -count=1 ./...
```

Tests use Go's standard `testing` package with `httptest` servers for API mocking. The test suite verifies both functional behavior and security properties:

- File permissions (0o600 for files, 0o700 for session directories)
- Relative paths in manifests (no absolute path leaks)
- Input sanitization (meeting titles and IDs)
- Highlight parsing with fallback field handling

## Architecture & Key Patterns

### Component Responsibilities

- **Config** (`models.go`): Holds all CLI flags and env vars. Priority: CLI flags > env vars > .env file > defaults.
- **Exporter** (`export.go`): Top-level orchestrator. Handles discovery, per-meeting export, and manifest writing. Browser operations are serialized via `browserMu` to prevent concurrent page navigations when `--parallel > 1`. Writes all files through the `Storage` interface.
- **Browser** (`browser.go`, `search.go`): Rod/Chromium automation. Used for login/cookie export, meeting list discovery, page scraping (transcript, highlights, metadata), search filtering, and video downloads. All methods use `Eval` (not `MustEval`) for crash resilience.
- **Storage** (`storage.go`): `Storage` interface with `WriteFile`, `WriteJSON`, `FileExists`, `EnsureDir`, `AbsPath`, `SyncExternalFile`, and `Close`. `LocalStorage` is the default implementation. `SyncState` / `SyncFileEntry` track incremental state for cloud backends.
- **DriveUploader** (`gdrive.go`): Google Drive upload client using only the stdlib (`net/http`). Supports OAuth2 user flow and service account auth. Implements incremental sync with MD5-based change detection and three conflict modes (`local-wins`, `skip`, `newer-wins`).
- **ICloudStorage** (`icloud.go`): `Storage` implementation that writes to both a local directory and a macOS iCloud Drive folder. iCloud writes are non-fatal; the local copy is always preserved.
- **Throttle** (`throttle.go`): Crypto-random rate limiter with one instance for inter-meeting delays.
- **ColorHandler** (`logger.go`): Custom `slog.Handler` with ANSI color prefixes for terminal output. Supports group prefixing. Use `--log-format json` for machine-readable output.

### Data Flow

1. `main()` parses config from flags/env/.env, sets up signal handling
2. `Exporter.Run()` creates output dir via `Storage`, discovers meetings via browser
3. Optional `--search` filter narrows meetings via browser-based search
4. For each meeting: scrape page metadata, write JSON + transcripts + highlights + markdown via `Storage`
5. Optionally download video/audio; externally-written files are synced via `Storage.SyncExternalFile`
6. If `--gdrive` is set: upload all exported files to Google Drive via `DriveUploader`
7. Writes `_export-manifest.json` summarizing results (ok/skipped/errors/hls_pending)

### Highlight Flexibility

Grain returns varying field names for highlights. The codebase handles this with:
- Multiple JSON tags on `Highlight` fields (e.g., `text` vs `content` vs `transcript`)
- `coalesce()` / `coalesceSlice()` / `firstNonNil()` helpers
- `parseHighlights` handles arrays, single objects, and wrapper objects

### Video Download Strategy

`Browser.DownloadVideo()` tries methods in order:
1. Click "Download" button via the meeting page menu
2. Extract video URL from `<video>` element or inline scripts
3. Network interception to capture `.mp4`/`.webm`/`.m3u8` URLs
4. Falls back to saving the URL to a text file for manual download

In-browser fetch (`fetchViaJS`) is bounded to 50MB to prevent browser heap exhaustion for large videos.

## Security Conventions

This codebase is security-conscious. Maintain these practices:

- **File permissions**: Output files at `0o600`, session directories at `0o700`. Use `Storage.WriteFile()` / `Storage.WriteJSON()` (which enforce 0o600 via all implementations) and `ensureDirPrivate()` for session dirs. The legacy `writeFile()` / `writeJSON()` helpers in `models.go` are retained for browser.go paths that write directly to absolute paths (video downloads, URL fallback files).
- **Input sanitization**: All meeting IDs validated against `validID` regex before use in URLs. Titles sanitized via `sanitize()` before use as filenames (strips path separators, traversal sequences, control chars). Truncation is rune-safe.
- **URL encoding**: Always use `url.QueryEscape()` for query parameters. Never interpolate user input into URLs. JavaScript strings escaped via `json.Marshal`.
- **Manifest paths**: Always relative (via `Exporter.relPath()`), never absolute.
- **Browser stealth**: Suppress `navigator.webdriver` and `AutomationControlled` blink feature.
- **Credentials**: OAuth2 tokens and service-account key files are written with 0o600 permissions. Credentials paths must be supplied via flags/env — never hardcoded.

## Code Style

- Single-package (`main`) flat layout -- all `.go` files in root
- Go 1.23 with `log/slog` for structured logging
- Error wrapping with context: `fmt.Errorf("description: %w", err)`
- Explicit resource cleanup via `defer` patterns
- `context.Context` threaded through all operations for cancellation
- No external linter config; `golangci-lint` used when available
- Typed structs for data (no `map[string]any`)
- `crypto/rand` (not `math/rand`) for throttle delays
- All Rod `Eval` calls use the non-panicking form (`Eval` not `MustEval`)

## Dependencies

The only direct external dependency is:

- `github.com/go-rod/rod` v0.114.8 -- Chromium DevTools protocol driver for browser automation

The Google Drive client (`gdrive.go`) uses only Go's standard library (`net/http`, `encoding/json`, `crypto/...`) — no Google SDK is pulled in. All other imports are from Go's standard library.

## Docker

Multi-stage build:
- **Builder**: `golang:1.23-alpine` compiles a static binary (CGO_ENABLED=0)
- **Runtime**: `alpine:3.20` with Chromium, runs as non-root `exporter` user
- Default CMD: headless mode (`--output /data --headless --skip-video`)
- `docker-compose.yml` mounts `./recordings:/data` and `.env` read-only
- Resource limits: 2GB memory, 1 CPU
- Restart policy: `unless-stopped`

## Configuration Hierarchy

Config values resolve in this order (highest priority first):

1. CLI flags (`--output`, `--headless`, etc.)
2. Environment variables (`GRAIN_OUTPUT_DIR`, `GRAIN_HEADLESS`, etc.)
3. `.env` file (parsed by `loadDotEnv()`, returns map without mutating `os.Setenv`)
4. Built-in defaults

## Known Limitations

- Rod's `MustWaitDownload` has no cancellation API. A stalled video download leaks one goroutine until process exit (mitigated by a 5-minute timeout).
- The `.env` parser is minimal: 4096-byte max line, basic `KEY=VALUE` parsing with quote stripping. Inline comments (`KEY=value # comment`) are not stripped.
- Browser operations are serialized via mutex, so `--parallel` only parallelizes file I/O and ffmpeg work, not browser interactions.
- `--icloud` is macOS-only. On Linux/Windows, path auto-detection will fail; supply `--icloud-path` explicitly or the flag is silently ignored.
- `--gdrive-clean-local` permanently removes local files after upload. Ensure the Drive upload succeeded before relying on this flag in production.
