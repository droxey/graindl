# Software Design & Infrastructure Code Review

**Project:** graindl
**Date:** 2026-02-17
**Scope:** Architecture, code quality, infrastructure, security, test suite

---

## Executive Summary

graindl is a well-structured Go CLI with clear separation of concerns, strong security practices, and a comprehensive test suite. The flat single-package layout is appropriate for the project's size. Key strengths include disciplined API response flexibility handling, crypto-random throttling, and consistent file permission enforcement. The review identifies several issues ranging from compilation errors to design improvements.

---

## 1. Critical: Compilation Errors

The codebase does not currently compile. `go vet` reports errors and manual inspection reveals multiple issues:

### 1.1 Duplicate struct fields in `Config` (`models.go:16-52`)

The `Config` struct declares fields twice. Lines 17-33 define `Token`, `TokenFile`, `OutputDir`, `SessionDir`, `MaxMeetings`, `MeetingID`, `DryRun`, `SkipVideo`, `Overwrite`, `Headless`, `CleanSession`, `Verbose`, `MinDelaySec`, `MaxDelaySec`, `SearchQuery`, `Watch`, and `WatchInterval`. Then lines 34-51 re-declare most of these with different alignment, plus add `Parallel`, `AudioOnly`, and `OutputFormat`. This is a merge conflict artifact — the two blocks need to be unified into a single struct definition.

### 1.2 Unclosed braces in `main.go:170-172`

The `--watch` + `--overwrite` validation block is missing a closing brace and `os.Exit(1)` before the `--output-format` validation:

```go
if cfg.Overwrite {
    slog.Error("--watch cannot be used with --overwrite ...")
// missing: os.Exit(1) and closing }
if cfg.OutputFormat != "" {
```

Similarly, the `if cfg.Watch {` block at line 200 appears to be missing its closing brace before the `if cfg.OutputFormat != ""` block at line 202.

### 1.3 Duplicate `SkipVideo` field in test (`export_test.go:1088-1089`)

```go
AudioOnly:   true,
SkipVideo:   false,
SkipVideo:   true,  // duplicate
```

### 1.4 Duplicate method call in `exportOne` (`export.go:412-421`)

`writeMetadata` is called twice with different signatures — once with `(rec, ref, metaPath, r)` at line 412 and once with `(meta, metaPath, r)` at line 421. The first call uses a signature that doesn't match the method definition at line 440. This is another merge conflict artifact.

### 1.5 Interleaved function bodies in `export_test.go`

Several test functions have their closing braces and body text interleaved with the start of other functions (e.g., `TestParallelExportBasic` at line 1063 bleeds into audio-only test code, and `TestExportOneAudioOnlyAndSkipVideoMutualExclusion` at line 1133 bleeds into parallel export skip logic). This makes the file uncompilable.

**Recommendation:** These must be resolved before any other work. They appear to be artifacts of an incomplete merge that introduced `AudioOnly`, `OutputFormat`, `Watch`, and `Parallel` features simultaneously.

---

## 2. Architecture & Design

### 2.1 Strengths

- **Clear data flow:** `main` → `Exporter.Run` → `discover` → `exportOne` → `writeManifest` is easy to follow. The orchestrator pattern in `export.go` cleanly separates discovery from per-meeting export.

- **API response flexibility:** The `GrainRecording` accessor pattern (`GetTitle()`, `GetDate()`, etc.) with `coalesce`/`firstNonNil` helpers is a pragmatic solution for an API that returns inconsistent field names across versions. The custom `UnmarshalJSON` on `RecordingsPage` reinforces this.

- **Lazy browser initialization:** `lazyBrowser()` with `sync.Mutex` avoids launching Chromium when only API-mode is needed. Good resource management.

- **Two independent throttle instances:** Separating Exporter (inter-meeting) and Scraper (inter-API-call) throttles allows different components to rate-limit independently without interference.

- **Context propagation:** Context threading through all operations enables clean cancellation from signal handlers. The `Throttle.Wait` method's `select` on both timer and `ctx.Done()` is correct.

### 2.2 Design Concerns

**2.2.1 Flat package with no interfaces**

All components are concrete types with no interfaces. This makes the browser a hard dependency in `Exporter` — there's no way to unit test `writeVideo` or `writeAudio` without a real Chromium instance. Defining a `VideoDownloader` interface would allow mock injection in tests.

**2.2.2 `any`-typed fields in `GrainRecording`**

Fields like `Duration any`, `Participants any`, `Tags any`, `Highlights any` are typed as `any` to accommodate API variation. While pragmatic, this pushes type assertions deep into business logic (`toFloat64`, `flattenStringSlice`, `parseHighlights`). Consider using `json.RawMessage` for these fields and providing typed accessors that unmarshal on demand — this preserves flexibility while keeping the type boundary explicit.

**2.2.3 Search results use `div[role="link"]` selector**

`search.go:17` uses a broad CSS selector that depends on Grain's specific DOM structure. Any UI redesign breaks this. The code acknowledges this with the comment "broad — UUID filter is the real gate," which is a reasonable mitigation, but the selector should be documented as a known fragility.

**2.2.4 Parallel export status counting is single-writer but could drift**

`exportParallel` uses a channel-based single-writer pattern for manifest updates, which is correct. However, if the producer goroutine panics, the `results` channel is never closed and the consumer goroutine hangs. A `defer close(results)` inside the producer goroutine (after `wg.Wait()`) handles this, but a panic before `wg.Wait()` would still leak. Consider wrapping the producer in a recovery.

**2.2.5 `writeMetadata` called before `meta` is constructed**

In `exportOne` (after the merge is resolved), ensure the single `writeMetadata` call receives the properly constructed `*Metadata` value, not the raw `*GrainRecording`.

---

## 3. Security

### 3.1 Strengths

The security posture is strong for a CLI tool:

- **File permissions:** Consistent 0o600 for files, 0o700 for session dirs, enforced via `writeJSON`, `writeFile`, `ensureDirPrivate`, and `fixPerms`.
- **Token handling:** `--token-file` preferred over `--token`, with a warning when the flag is used. Token never logged.
- **Input validation:** `validID` regex blocks path traversal and URL injection in recording IDs. `sanitize()` strips dangerous characters from filenames.
- **Response limits:** `io.LimitReader` caps API responses at 50MB. Pagination circuit breaker at 100 pages.
- **URL encoding:** Parameters always encoded via `url.Values.Encode()`.
- **Manifest paths:** Always relative via `relPath()`.
- **Browser stealth:** `navigator.webdriver` suppressed.

### 3.2 Concerns

**3.2.1 `convert_hls.sh` does not validate HLS URLs**

The script reads URLs from `.m3u8.url` files and passes them directly to ffmpeg. If a file were tampered with, ffmpeg would connect to an arbitrary URL. Since these files are written with 0o600 permissions and the script runs locally, the risk is low but worth noting.

**3.2.2 `fetchViaJS` in `browser.go:338` passes video URL into JS via `%q`**

```go
b.page.MustEval(fmt.Sprintf(`async () => { ... fetch(%q) ...}`, videoURL))
```

Go's `%q` quotes the string safely for Go, but the resulting string is being injected into JavaScript. If `videoURL` contained specific sequences, this could break the JS context. `%q` happens to produce a valid JS string literal for most inputs, but this is a coincidental safety property, not a deliberate one. Use `json.Marshal(videoURL)` for correct JS escaping.

**3.2.3 `loadDotEnv` does not handle multiline values**

Values with embedded `=` signs (e.g., base64 tokens) parse correctly due to `SplitN(line, "=", 2)`, but values spanning multiple lines are not supported. The 4096-byte line limit is documented, which is sufficient for API tokens.

---

## 4. Infrastructure

### 4.1 Dockerfile

**Strengths:**
- Multi-stage build minimizes image size
- Non-root `exporter` user
- `ROD_BROWSER` env var set correctly for Alpine's Chromium path
- Dependencies (ffmpeg, jq, bash) included for `convert_hls.sh`

**Concerns:**

- **No pinned package versions** — `apk add --no-cache chromium` installs whatever version is current. Consider pinning or at least documenting the expected Chromium version range for Rod compatibility.
- **`COPY convert_hls.sh`** copies the script but doesn't `chmod +x` it. If the file loses its executable bit in transit, it won't be runnable. Add `RUN chmod +x /usr/local/bin/convert_hls.sh` or use `COPY --chmod=755`.
- **No HEALTHCHECK** — not critical for a CLI, but useful if run as a long-lived `--watch` container.
- **Alpine 3.20 + Go 1.23** — both are reasonable current versions.

### 4.2 docker-compose.yml

Clean and minimal. The `.env` file is mounted read-only, which is correct. `GRAIN_API_TOKEN` is passed via environment (from the host), which is fine for local development.

**Missing:** No `restart` policy. For `--watch` mode, `restart: unless-stopped` would be appropriate.

### 4.3 Makefile

Correct and minimal. `ldflags` properly inject version/commit. `golangci-lint` gracefully degrades when not installed.

**Missing:** No `make run` target. A convenience target like `run: build; ./graindl $(ARGS)` would be useful during development.

### 4.4 No CI/CD

There are no GitHub Actions workflows. For a project with this level of test coverage, adding a CI pipeline would protect against regressions. A minimal workflow running `make test` and `make vet` on push would be sufficient.

---

## 5. Test Suite

### 5.1 Strengths

- **Comprehensive coverage:** Tests cover helpers, API parsing, security properties (file permissions, path traversal, URL encoding), integration flows (export pipeline with httptest servers), cancellation, parallel execution, and watch mode.
- **Security-focused assertions:** Tests explicitly verify 0o600 permissions, relative paths, and ID validation. This is excellent.
- **Table-driven tests:** Used consistently (`TestCoalesce`, `TestSanitize`, `TestToFloat64`, etc.).
- **httptest servers:** API tests mock the Grain API correctly, including pagination, error codes, and alternate response shapes.
- **Edge cases:** Cancellation mid-export, already-cancelled contexts, empty inputs, malformed IDs.
- **Watch mode tests:** Time-based tests with atomic counters verify multi-cycle behavior.

### 5.2 Concerns

**5.2.1 No tests for browser components**

`browser.go` and the browser portions of `search.go` are untested. This is understandable given that Rod requires a real Chromium instance, but it means the video download strategy (4-method fallback chain) and login flow are completely unverified by automated tests. Consider either:
- Introducing an interface for the browser dependency to enable mocking
- Adding integration tests behind a build tag (e.g., `//go:build integration`)

**5.2.2 Compilation errors in test files**

As noted in Section 1, `export_test.go` has interleaved function bodies that prevent compilation. This means the entire test suite currently cannot run.

**5.2.3 Test helpers don't use `t.Helper()`**

`newTestScraper` in `scraper_test.go` correctly uses `t.Helper()`, but there's no equivalent helper for the commonly repeated pattern of "create httptest server + create config + create exporter + override baseURL" that appears in every export test. Extracting this would reduce boilerplate significantly.

**5.2.4 Race condition in `TestListRecordingsContextCancel`**

The test cancels after 50ms and expects an error, but the server responds immediately. Depending on timing, the first page fetch could complete before cancellation, and the test would only fail if the second page fetch is reached. This is a flaky test risk.

---

## 6. Code Quality

### 6.1 Strengths

- **Consistent error wrapping:** `fmt.Errorf("context: %w", err)` used throughout.
- **Explicit resource cleanup:** `defer` for `Close()`, `Stop()`, `timer.Stop()`.
- **Single dependency:** Only `go-rod/rod` as a direct dependency. Everything else is stdlib.
- **Crypto-random throttle:** Using `crypto/rand` instead of `math/rand` prevents predictable timing patterns.
- **Audit cross-references:** Code comments like `GO-6`, `SEC-2`, `PERF-1` reference a (missing) `AUDIT.md`.

### 6.2 Concerns

**6.2.1 `AUDIT.md` referenced but does not exist**

The `CLAUDE.md` mentions `AUDIT.md` as a "Security audit report with categorized findings," and code comments reference tags like `GO-1`, `SEC-1`, `PERF-1`. However, `AUDIT.md` is not present in the repository.

**6.2.2 `extractVideoURL` is a single unreadable line**

`browser.go:292` packs an entire DOM traversal strategy into one JavaScript string. This should be broken into a readable multi-line template string or a separate `.js` file.

**6.2.3 `countLinks` similarly unreadable**

`browser.go:190` is a single-line JavaScript evaluation. Same recommendation.

**6.2.4 `ColorHandler.WithGroup` does not concatenate groups**

`logger.go:103-109` stores the group name but never prefixes it to attribute keys. The `slog.Handler` contract expects `WithGroup("g").Handle()` to prefix all subsequent attrs with `g.`. This is a conformance issue, though unlikely to matter since the codebase doesn't use `WithGroup`.

**6.2.5 `firstNonNil` has a subtle behavior with typed nils**

`firstNonNil` checks `v != nil`, but in Go, a typed nil (e.g., `(*GrainRecording)(nil)`) is not `== nil` when passed as `any`. The current callers only pass untyped values from JSON unmarshaling, so this isn't a bug today, but the function name is misleading.

---

## 7. Summary of Recommendations

| Priority | Item | Section |
|----------|------|---------|
| **P0** | Fix compilation errors (duplicate fields, unclosed braces, interleaved test bodies) | 1.x |
| **P1** | Use `json.Marshal` for JS string escaping in `fetchViaJS` | 3.2.2 |
| **P1** | Create `AUDIT.md` or remove stale references | 6.2.1 |
| **P2** | Add CI/CD pipeline (GitHub Actions for `make test && make vet`) | 4.4 |
| **P2** | Introduce a browser interface for testability | 2.2.1, 5.2.1 |
| **P2** | Pin Chromium version in Dockerfile or document compatibility | 4.1 |
| **P3** | Add `chmod +x` for `convert_hls.sh` in Dockerfile | 4.1 |
| **P3** | Add `restart: unless-stopped` to docker-compose for watch mode | 4.2 |
| **P3** | Break up inline JavaScript strings in `browser.go` | 6.2.2 |
| **P3** | Fix `ColorHandler.WithGroup` conformance | 6.2.4 |
| **P3** | Extract test helper for export test setup | 5.2.3 |
