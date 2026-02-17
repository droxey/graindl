# Code Review: graindl

**Reviewers:** Go Engineering & Ops Engineering
**Date:** 2026-02-17
**Scope:** Full codebase review — architecture, code quality, security, testing, and operational readiness.
**Build status:** `go vet` clean, `go test -race ./...` all passing.

---

## Executive Summary

graindl is a well-structured, security-conscious Go CLI tool for exporting Grain meeting data. The codebase demonstrates strong fundamentals: proper context propagation, crypto-secure randomness, restrictive file permissions, input sanitization, and a thorough test suite. The flat single-package layout is appropriate for the project's scope.

That said, several areas warrant attention — most are improvements rather than critical defects. The findings below are organized by severity and domain.

---

## Go Engineering Findings

### Critical

**1. Shared `*rod.Page` across concurrent operations — `browser.go:26`, `export.go:578`**

`Browser` holds a single `page *rod.Page` field that is reused by all operations — `ScrapeMeetingPage`, `DownloadVideo`, `FindVideoSource`, `DiscoverMeetings`. When `--parallel > 1`, multiple goroutines call `e.lazyBrowser()` (which returns the same `*Browser`), then call methods like `ScrapeMeetingPage` that navigate the shared page to different URLs. This is a data race on the browser page state.

`lazyBrowser()` is correctly mutex-protected for *initialization*, but the `Browser` methods themselves are not safe for concurrent use — they all call `b.page.MustNavigate(...)` on the same page.

**Impact:** With `--parallel > 1` and browser-based operations (video downloads, scraping), page navigations will collide, producing corrupted results or panics.

**Recommendation:** Either (a) create a new page per `exportOne` call via a pool, or (b) use `browserMu` to serialize all browser operations (converting `--parallel` into sequential for browser paths), or (c) maintain a page pool. Option (b) is simplest and matches the current architecture.

---

### High

**2. `MustEval` / `MustNavigate` / `MustWaitStable` panics are partially unguarded — `browser.go`**

Some call sites wrap `Must*` calls in `rod.Try(func() { ... })` (e.g., `Login`, `DiscoverMeetings` navigation), but many `MustEval` calls are not guarded:

- `browser.go:63` — `page.MustEvalOnNewDocument`
- `browser.go:159-163` — `b.page.MustEval` in `DiscoverMeetings` scroll loop
- `browser.go:166-176` — `b.page.MustEval` to extract meeting links
- `browser.go:191` — `countLinks()`
- `browser.go:293` — `extractVideoURL()`
- `browser.go:316` — video play trigger
- `browser.go:440` — `scrapeParticipants()`
- `browser.go:469` — `scrapeTranscript()`
- `browser.go:525` — `scrapeHighlights()`

If any of these JavaScript evaluations fail (network timeout, page crash, navigation race), the process panics with no recovery.

**Recommendation:** Replace `MustEval` with `Eval` and handle errors, or wrap the callers in `rod.Try`. The scraping functions (`ScrapeMeetingPage` and children) should be especially resilient since they run against external HTML that may change.

**3. In-process video download via JS `fetch` + base64 — `browser.go:339-355`**

`fetchViaJS` downloads an entire video file into the browser's JS heap as a `Uint8Array`, converts it to a string character-by-character, then base64-encodes it, sends the base64 string over CDP to Go, and Go decodes it. For large videos (hundreds of MB), this will:

- Exhaust the renderer's memory (Chromium tabs have per-process limits).
- Produce massive CDP messages that may time out.
- Use ~4x the memory of the video file (raw + string + base64 + decoded).

**Recommendation:** Use Rod's download API or Go's `http.Client` with session cookies exported from the browser. If `fetchViaJS` must remain as a fallback, add a size check in JS (`r.headers.get('content-length')`) and bail out above a threshold (e.g., 50MB).

---

### Medium

**4. `ColorHandler` ignores the `group` field — `logger.go:103-109`**

`WithGroup` stores the group name but `Handle` never uses it. If code like `slog.With("component", "browser").WithGroup("scraper")` is used, the group prefix will be silently dropped. This violates the `slog.Handler` contract.

**Recommendation:** Prepend the group name to attribute keys in `collectAttrs`, e.g., `group.key=value`.

**5. `Exporter.discover` is hardcoded to browser-only — `export.go:317-319`**

```go
func (e *Exporter) discover(ctx context.Context) ([]MeetingRef, error) {
    return e.discoverViaBrowser(ctx)
}
```

The `CLAUDE.md` documentation describes "API-only (using a Grain API token)" mode and references a `scraper.go` HTTP API client for `/me`, `/recordings`, and `/recordings/:id` endpoints. However, `scraper.go` does not exist in the repository, `discover()` unconditionally calls `discoverViaBrowser`, and there is no API-based discovery path. Either the API client was removed without updating the documentation, or it was planned but never implemented.

**Recommendation:** Update `CLAUDE.md` to reflect the current browser-only architecture. Remove references to `scraper.go`, `Scraper`, API token-based operation, `--token`/`--token-file` flags, response size limits (`io.LimitReader`), pagination circuit breakers, and other API-only features that don't exist in the code.

**6. Error in `writeTranscript` / `writeHighlights` is silently swallowed — `export.go:449,466`**

```go
if writeFile(p, []byte(scraped.Transcript)) == nil {
```

When `writeFile` fails (disk full, permissions), the error is silently ignored and the transcript path is simply not set on the result. The highlights writer logs the error but doesn't mark the export result as degraded.

**Recommendation:** At minimum, log all write failures. Consider adding a `Warnings []string` field to `ExportResult` to track partial failures.

**7. `loadDotEnv` doesn't handle inline comments or escaped quotes — `main.go:25-51`**

The .env parser strips both single and double quotes but doesn't handle:

- Inline comments: `KEY=value # comment` parses as `value # comment`.
- Escaped quotes: `KEY="value with \"quotes\""` parses as `value with \`.

**Impact:** Low — env values in this project are simple tokens/paths/booleans, and the CLAUDE.md documents the parser as minimal. Still worth noting for anyone extending it.

**8. `filtered := meetings[:0]` reuses backing array — `export.go:68`, `browser.go:572`**

The in-place filter pattern is a valid Go idiom and safe here (the original is immediately overwritten), but `slices.DeleteFunc` (available since Go 1.21) would be clearer and less error-prone for future maintainers.

**9. `RunWatch` test path bypasses main() validation — `watch.go`, `watch_test.go`**

`main()` rejects `--watch --id` as an invalid combination, but the watch tests use `MeetingID` + `Watch` directly by constructing a `Config` struct. This means the tests exercise a code path that production can never reach. The tests are useful for verifying watch loop mechanics, but the discrepancy should be documented.

---

### Low

**10. `writeJSON` / `writeFile` don't `fsync` — `models.go:302-312`**

`os.WriteFile` does not guarantee data is flushed to disk. For a CLI tool this is acceptable, but for `--watch` mode running unattended for hours/days, consider `Sync()` for the manifest file at minimum.

**11. `sanitize` truncates by bytes, not runes — `models.go:280`**

`len(s) > 200` counts bytes, not runes. A string with multibyte Unicode characters could be truncated mid-rune, producing invalid UTF-8 in the filename. Use `utf8.RuneCountInString` or `[]rune` conversion.

**12. `meetingURL` does not validate input — `models.go:292`**

```go
func meetingURL(id string) string { return "https://grain.com/app/meetings/" + id }
```

All current call sites use pre-validated IDs, but the function itself offers no protection against injection. A `validID` check or documentation of the precondition would harden it.

**13. Config validation is scattered — `main.go:126-168`**

19 config fields with validation logic spread across `main()`. A `Config.Validate() error` method would centralize this, make it testable independently, and prevent invalid configs in test construction.

**14. Context shadowing in `waitForResults` — `search.go:71`**

```go
func (b *Browser) waitForResults(ctx context.Context, page *rod.Page) error {
    ctx, cancel := context.WithTimeout(ctx, resultLoadTimeout)
```

The parameter `ctx` is shadowed by the timeout context. This is correct behavior but can confuse readers. Consider `tCtx, cancel := ...`.

**15. `extractVideoURL` and `countLinks` are unreadable single-line JS — `browser.go:191,293`**

These pack entire DOM traversal strategies into single-line JavaScript strings. Breaking them into multi-line template strings (like `scrapeTranscript` already does) would improve maintainability.

---

## Ops Engineering Findings

### High

**16. No CI/CD pipeline**

No GitHub Actions workflows, no automated quality gates. `make test` and `make lint` exist but run only manually. For a tool that handles auth tokens and browser sessions, automated testing on push is important.

**Recommendation:** Add `.github/workflows/ci.yml`:
```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23' }
      - run: go vet ./...
      - run: go test -count=1 -race ./...
```

**17. `convert_hls.sh` referenced but absent — `Dockerfile:43`, `export.go:114`**

```dockerfile
COPY convert_hls.sh /usr/local/bin/convert_hls.sh
```

This file is referenced in the Dockerfile and in user-facing messages ("Run ./convert_hls.sh to convert HLS streams to MP4"), but does not exist in the repository. The Docker build will fail at this COPY step.

**Recommendation:** Create the script, remove the COPY line, or guard it with a conditional.

**18. No health monitoring in watch mode — `watch.go`**

`--watch` mode runs indefinitely, but there's no:

- Healthcheck file/endpoint for Docker `HEALTHCHECK` or external monitoring.
- Metrics (export count, error rate, cycle duration).
- Exponential backoff on repeated failures — a cycle that fails immediately retries at the full interval, but there's no circuit breaker for persistent failures.

**Recommendation:** Add a `--healthcheck-file` flag that touch-updates a file after each successful cycle.

---

### Medium

**19. No container resource limits in `docker-compose.yml`**

No `mem_limit`, `cpus`, or `pids_limit`. Chromium is memory-hungry; without limits, a stalled browser can consume all host memory.

**Recommendation:**
```yaml
deploy:
  resources:
    limits:
      memory: 2G
      cpus: '1.0'
```

**20. No `restart` policy in `docker-compose.yml`**

For `--watch` mode running as a persistent service, `restart: unless-stopped` is appropriate.

**21. No structured logging option**

The `ColorHandler` outputs ANSI-colored text, which is problematic for log aggregation (Splunk, CloudWatch, ELK strip or mangle ANSI escapes). A `--log-format json` flag that swaps to `slog.NewJSONHandler` would enable production log pipelines.

**22. `.dockerignore` is incomplete**

Missing `*_test.go`, `AUDIT.md`, `REVIEW.md`, `CLAUDE.md`, `README.md`, `Makefile`, `.gitignore`, etc. These get copied into the builder stage unnecessarily. While they don't affect the final image, they increase build context transfer time.

**23. Docker image Chromium security**

The Dockerfile correctly uses a non-root `exporter` user, but Chromium in containers typically requires `--no-sandbox` (Rod does this by default). Without a seccomp profile, this increases the attack surface if processing untrusted content.

**Recommendation:** Add Chrome-specific seccomp profile guidance in docker-compose.yml:
```yaml
security_opt:
  - seccomp:chrome.json
```

---

### Low

**24. Makefile `build` doesn't set `CGO_ENABLED=0`**

The binary links against libc by default. For Alpine Docker deployment this works (musl), but cross-compilation or scratch image deployment would fail. Consider `CGO_ENABLED=0` for static binaries.

**25. No `go mod verify` in build pipeline**

Without this check, a compromised dependency cache won't be detected.

**26. Unpinned Alpine package versions in Dockerfile**

`apk add --no-cache chromium` installs whatever version is current. A Chromium version incompatible with Rod v0.114.8 could break browser automation silently.

---

## Testing Assessment

### Strengths

- **Security-focused assertions:** File permissions (0o600), relative paths, input validation, and injection prevention are explicitly tested throughout.
- **Good edge case coverage:** Nil inputs, empty strings, cancellation, pre-cancelled contexts, zero values, overwrite vs. skip semantics.
- **Test isolation:** `t.TempDir()` for all filesystem operations — no cross-test contamination.
- **No external test dependencies:** Standard `testing` package only.
- **Watch mode tested without browser:** Clever use of `--id` mode to exercise the watch loop mechanics.
- **Table-driven tests:** Used consistently (`TestCoalesce`, `TestSanitize`, `TestToFloat64`, `TestLooksLikeUUID`, `TestExtractMeetingID`).
- **Parallel export correctness:** Tests verify order preservation, cancellation, and nil-slot compaction.
- **Regression guards:** `TestNormalizeHighlightZeroTimestamp` and `TestParseHighlightsWrapperWithTitle` are clearly written to prevent specific regressions.

### Gaps

- **No browser tests:** `browser.go` (594 lines) and the browser portions of `search.go` are entirely untested. The video download strategy (4-method fallback chain), login flow, transcript scraping, and highlight extraction have zero automated coverage. Consider introducing an interface for the browser dependency to enable mocking, or adding integration tests behind a `//go:build integration` tag.

- **No fuzz testing:** `sanitize`, `loadDotEnv`, `extractMeetingID`, and `parseHighlights` are parsing-heavy functions that would benefit from `testing.F` fuzz targets. Given the security sensitivity of `sanitize` (filename safety), this is especially valuable.

- **No `t.Parallel()`:** Tests run sequentially within the package. For a 165-second test suite (largely due to watch tests' wall-clock waits), parallelizing non-conflicting tests would meaningfully reduce CI time.

- **Watch test timing sensitivity:** Tests like `TestRunWatchMultipleCycles` depend on goroutine scheduling within 500ms windows and use wall-clock assertions. The code handles slow CI gracefully with log messages, but these tests will flake under load. Consider using explicit synchronization (channels, waitgroups) instead of time-based expectations.

- **`formatDuration` string branch untested:** The `default` case in `formatDuration` (`format_test.go`) handles `string` input via `fmt.Sprintf("%v", v)`, but no test covers this branch.

---

## Architecture Observations

### What works well

1. **Flat package layout** — For a single-binary CLI of this size (~1.5K lines of application code), a flat `main` package avoids premature abstraction. Every type and function is in scope, reducing indirection.

2. **Context propagation** — `context.Context` is threaded correctly through all operations, enabling clean shutdown via `signal.NotifyContext`. The `Throttle.Wait` method's `select` on both timer and `ctx.Done()` is correct.

3. **Throttle design** — Using `crypto/rand` instead of `math/rand` prevents detectable patterns in request timing. The independent instances for exporter and scraper throttles are a thoughtful design.

4. **API response flexibility** — The `coalesce` / `firstNonNil` / `coalesceSlice` helpers and multi-field JSON tags on `Highlight` handle API instability gracefully without overcomplicating the type system.

5. **Security-first file operations** — `writeJSON` / `writeFile` enforce 0o600 everywhere, and `ensureDirPrivate` is used for session data. `fixPerms` in `audio.go` catches ffmpeg's default umask. This is consistently applied and well-tested.

6. **Export manifest design** — Relative paths, typed status codes, and per-meeting error messages make the manifest both machine-parseable and debuggable.

### What could be improved

1. **Browser lifecycle management** — The single shared `Browser` with a single `Page` is the biggest architectural constraint. It prevents safe parallelism for browser operations and makes the scraping methods fragile to navigation races.

2. **Error propagation** — Several write failures are silently swallowed (`writeTranscript`, `writeHighlights`, HLS URL fallback writes). The export result should track partial failures explicitly.

3. **Testability** — The tight coupling between `Exporter` and `Browser` (via `lazyBrowser`) makes it impossible to test export logic involving browser calls without a real Chromium instance. An interface like `PageScraper` or `VideoDownloader` would allow mock injection in tests, which would dramatically improve coverage of the export pipeline.

4. **Documentation drift** — `CLAUDE.md` describes an architecture with API-based discovery, `scraper.go`, token-based auth, response size limits, and pagination — none of which exist in the current code. This will mislead anyone reading the docs before the code.

---

## Summary of Recommendations by Priority

| Priority | # | Finding | Effort |
|----------|---|---------|--------|
| Critical | 1 | Fix shared `*rod.Page` for parallel mode | Medium |
| High | 2 | Guard `MustEval` calls with error handling | Medium |
| High | 3 | Bound `fetchViaJS` memory usage | Medium |
| High | 16 | Add CI/CD pipeline | Low |
| High | 17 | Fix or remove `convert_hls.sh` COPY in Dockerfile | Low |
| High | 18 | Add healthcheck for watch mode | Medium |
| Medium | 4 | Fix `ColorHandler.WithGroup` conformance | Low |
| Medium | 5 | Update CLAUDE.md to match actual codebase | Low |
| Medium | 6 | Log/track write failures in export pipeline | Low |
| Medium | 19 | Add Docker resource limits | Low |
| Medium | 20 | Add restart policy to docker-compose | Low |
| Medium | 21 | Add structured JSON logging option | Low |
| Low | 11 | Fix Unicode truncation in `sanitize` | Low |
| Low | 13 | Centralize config validation | Low |
| Low | 15 | Reformat inline JS in browser.go | Low |
| Low | 24 | Set `CGO_ENABLED=0` in Makefile | Low |
