# CLAUDE.md

This file provides guidance for AI assistants working with the graindl codebase.

## Project Overview

**graindl** is a Go CLI tool that exports meetings, transcripts, metadata, and videos from [Grain](https://grain.com). It supports two modes: API-only (using a Grain API token) and browser-based (using Chromium via the Rod library for login, meeting discovery, search filtering, and video downloads).

## Repository Structure

All source code lives in the root directory as a single `main` package:

```
main.go        - CLI entry point, flag parsing, .env loading, signal handling
models.go      - Type definitions (Config, GrainRecording, ExportResult, Metadata)
export.go      - Exporter orchestrator: discovery, per-meeting export, manifest generation
scraper.go     - HTTP API client: /me, /recordings, /recordings/:id endpoints
browser.go     - Rod/Chromium wrapper: login, meeting discovery, video download
search.go      - Browser-based search: navigates Grain search UI, extracts results
logger.go      - Custom slog.Handler with ANSI color output
throttle.go    - Rate limiter using crypto/rand for random delays in [Min, Max)
```

Test files follow the `_test.go` convention and mirror source files:

```
main_test.go       - .env loading, config resolution
models_test.go     - Sanitization, metadata building, API response parsing
export_test.go     - Integration tests for export pipeline (httptest servers)
scraper_test.go    - API calls, error handling, input validation
logger_test.go     - Color formatting
search_test.go     - UUID parsing, search result extraction
throttle_test.go   - Random delay distribution
```

Other key files:

```
Makefile             - Build automation (build, test, vet, lint, clean, docker)
Dockerfile           - Multi-stage build (golang:1.23-alpine -> alpine:3.20)
docker-compose.yml   - Docker Compose service definition
go.mod / go.sum      - Go module (github.com/droxey/graindl, Go 1.23)
README.md            - User-facing documentation
AUDIT.md             - Security audit report with categorized findings
LICENSE              - MIT
.gitignore           - Excludes binary, recordings/, .grain-session/, .env, media
.dockerignore        - Minimizes Docker build context
```

## Build & Development Commands

```bash
make build     # Build binary with git version/commit via ldflags
make test      # Run tests with -race detector (go test -count=1 -race ./...)
make vet       # Run go vet
make lint      # Run golangci-lint (optional, graceful fallback if not installed)
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
- URL parameter encoding (injection prevention)
- Input sanitization (meeting titles and IDs)
- API response parsing with fallback field handling

## Architecture & Key Patterns

### Component Responsibilities

- **Config** (`models.go`): Holds all CLI flags and env vars. Priority: CLI flags > env vars > .env file > defaults.
- **Exporter** (`export.go`): Top-level orchestrator. Handles discovery (API or browser fallback), per-meeting export, and manifest writing.
- **Scraper** (`scraper.go`): Typed HTTP client for the Grain API (`https://api.grain.com/_/public-api`). Handles auth, pagination, throttling, and response size limits.
- **Browser** (`browser.go`, `search.go`): Rod/Chromium automation. Used for login/cookie export, meeting list discovery, search filtering, and video downloads.
- **Throttle** (`throttle.go`): Crypto-random rate limiter. Two independent instances exist: one for inter-meeting delays (Exporter), one for inter-API-call delays (Scraper).
- **ColorHandler** (`logger.go`): Custom `slog.Handler` with ANSI color prefixes for terminal output.

### Data Flow

1. `main()` parses config from flags/env/.env, sets up signal handling
2. `Exporter.Run()` creates output dir, discovers meetings (API with browser fallback)
3. Optional `--search` filter narrows meetings via browser-based search
4. For each meeting: fetch metadata, write JSON + transcripts, optionally download video
5. Writes `_export-manifest.json` summarizing results (ok/skipped/errors/hls_pending)

### API Response Flexibility

The Grain API returns varying field names across versions. The codebase handles this with:
- Multiple JSON tags on `GrainRecording` fields (e.g., `title` vs `name`)
- Accessor methods with fallbacks (`GetTitle()`, `GetDate()`, `GetShareURL()`, etc.)
- `coalesce()` / `coalesceSlice()` / `firstNonNil()` helpers
- Custom `UnmarshalJSON` on `RecordingsPage` to handle different list/cursor key names

### Video Download Strategy

`Browser.DownloadVideo()` tries methods in order:
1. Click "Download" button via the meeting page menu
2. Extract video URL from `<video>` element or inline scripts
3. Network interception to capture `.mp4`/`.webm`/`.m3u8` URLs
4. Falls back to saving the URL to a text file for manual download

## Security Conventions

This codebase is security-conscious. Maintain these practices:

- **File permissions**: Output files at `0o600`, session directories at `0o700`. Use `writeJSON()` / `writeFile()` (which enforce 0o600) and `ensureDirPrivate()` for session dirs.
- **Token handling**: Prefer `--token-file` over `--token` to keep secrets out of process listings. Never log token values.
- **Input sanitization**: All meeting IDs validated against `validID` regex before use in URLs. Titles sanitized via `sanitize()` before use as filenames (strips path separators, traversal sequences, control chars).
- **Response limits**: API responses bounded to 50MB via `io.LimitReader`. Pagination circuit breaker at 100 pages.
- **URL encoding**: Always use `url.Values.Encode()` for query parameters. Never interpolate user input into URLs.
- **Manifest paths**: Always relative (via `Exporter.relPath()`), never absolute.
- **Browser stealth**: Suppress `navigator.webdriver` and `AutomationControlled` blink feature.
- **Audit references**: Code comments use tags like `GO-1`, `SEC-1`, `PERF-1` referencing findings in `AUDIT.md`.

## Code Style

- Single-package (`main`) flat layout -- all `.go` files in root
- Go 1.23 with `log/slog` for structured logging
- Error wrapping with context: `fmt.Errorf("description: %w", err)`
- Explicit resource cleanup via `defer` patterns
- `context.Context` threaded through all operations for cancellation
- No external linter config; `golangci-lint` used when available
- Typed structs for API responses (no `map[string]any`)
- `crypto/rand` (not `math/rand`) for throttle delays

## Dependencies

The only direct dependency is:

- `github.com/go-rod/rod` v0.114.8 -- Chromium DevTools protocol driver for browser automation

All other imports are from Go's standard library.

## Docker

Multi-stage build:
- **Builder**: `golang:1.23-alpine` compiles the binary
- **Runtime**: `alpine:3.20` with Chromium, runs as non-root `exporter` user
- Default CMD: API-only mode (`--output /data --headless --skip-video`)
- `docker-compose.yml` mounts `./recordings:/data` and `.env` read-only

## Configuration Hierarchy

Config values resolve in this order (highest priority first):

1. CLI flags (`--token`, `--output`, etc.)
2. Environment variables (`GRAIN_API_TOKEN`, `GRAIN_OUTPUT_DIR`, etc.)
3. `.env` file (parsed by `loadDotEnv()`, returns map without mutating `os.Setenv`)
4. Built-in defaults

## Known Limitations

- Rod's `MustWaitDownload` has no cancellation API. A stalled video download leaks one goroutine until process exit (mitigated by a 5-minute timeout).
- No CI/CD pipeline configured (no `.github/workflows/` directory).
- The `.env` parser is minimal: 4096-byte max line, basic `KEY=VALUE` parsing with quote stripping.
