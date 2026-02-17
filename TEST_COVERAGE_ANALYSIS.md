# Test Coverage Analysis

**Date:** 2026-02-17
**Overall Statement Coverage:** 59.2%

## Coverage Summary by File

| File | Functions Tested | Coverage | Notes |
|------|-----------------|----------|-------|
| `main.go` | `loadDotEnv`, `envGet`, `envFloat`, `envInt`, `envBool` | 100% (helpers), 0% (`main`) | Config helpers fully tested; `main()` untested |
| `models.go` | All helpers + accessors | 95-100% | Well-covered with table-driven tests |
| `scraper.go` | `Me`, `ListRecordings`, `GetRecording`, `GetTranscriptRaw`, `apiGetRaw` | 83-100% | Good HTTP mock coverage; `InjectCookies` untested |
| `export.go` | `exportOne`, `Run`, `refsFromAPI`, `relPath`, parallel/sequential paths | 57-100% | Good integration tests; browser-dependent paths untested |
| `format.go` | All rendering + YAML helpers | 86-100% | Strong coverage |
| `search.go` | `looksLikeUUID`, `extractMeetingID` | 91-100% (pure), 0% (browser) | Pure functions tested; browser interaction untested |
| `logger.go` | `Handle`, `Enabled`, `WithAttrs`, `collectAttrs` | 100% (most), 0% (`WithGroup`) | Nearly complete |
| `throttle.go` | `Wait`, `duration` | 85-87% | Solid including edge cases |
| `browser.go` | None | 0% (except 35% `NewBrowser` from side-effects) | Entirely untested |

## Areas Requiring Improved Coverage

### Priority 1: `browser.go` -- 0% coverage (334 lines)

This is the single largest gap. Every function in `browser.go` has 0% coverage:

- `Close()`, `Login()`, `exportCookies()`
- `DiscoverMeetings()`, `countLinks()`
- `DownloadVideo()`, `tryDownloadBtn()`, `extractVideoURL()`
- `interceptNetwork()`, `resolveURL()`, `fetchViaJS()`, `pressEscape()`

**Why it matters:** This file contains the most complex and error-prone code paths in the project (video download with 4 fallback strategies, network interception with `atomic.Value`, browser automation with timeouts). The security audit (AUDIT.md) specifically flagged SEC-5 (data race in video interception) as fixed here, but that fix has no test coverage.

**Recommended approach:**
1. **Extract testable logic from browser methods.** Functions like `resolveURL()` and `exportCookies()` contain pure logic that can be tested without a browser. Factor out URL resolution logic and cookie conversion into standalone functions.
2. **Add unit tests for `resolveURL()`** -- it branches on `.m3u8` vs direct download vs fallback `url-saved`, and all three paths can be tested with simple mocks if the method is refactored to not need `b.fetchViaJS`.
3. **Test `extractVideoURL()` by mocking page.MustEval** -- create an interface for the JS evaluation dependency to enable mock injection.
4. **Consider `rod`'s test utilities** -- Rod provides `rod.New().MustConnect()` with test helpers. A lightweight integration test that spins up a local HTTP server with a `<video>` tag could validate `extractVideoURL()` and `interceptNetwork()`.

### Priority 2: `search.go` browser methods -- 0% coverage (4 functions, ~100 lines)

The browser-dependent functions `Search()`, `waitForResults()`, `scrollToEnd()`, and `extractResults()` are untested:

| Function | Lines | Coverage |
|----------|-------|----------|
| `Search` | 34-66 | 0% |
| `waitForResults` | 70-96 | 0% |
| `scrollToEnd` | 100-141 | 0% |
| `extractResults` | 144-186 | 0% |

**Why it matters:** Search filtering is a user-facing feature (`--search` flag). If `extractResults` has a bug in its DOM parsing or `scrollToEnd` doesn't handle edge cases, users get silently missing results.

**Recommended approach:**
1. **`extractResults` can be partially unit-tested** by mocking the `page.Elements()` return. Factor out the href-to-SearchResult mapping into a pure function (e.g., `parseSearchResult(href, title string) (SearchResult, bool)`).
2. **Integration test with a local HTML fixture** -- serve a static HTML page with known `div[role="link"]` elements and validate that `extractResults` returns the correct IDs.

### Priority 3: `export.go` -- `buildSearchFilter` and `writeVideo` -- 0% coverage

| Function | Lines | Coverage |
|----------|-------|----------|
| `buildSearchFilter` | 301-324 | 0% |
| `writeVideo` | 511-534 | 0% |
| `discoverViaBrowser` | 346-363 | 30.8% |

**Why it matters:** `buildSearchFilter` orchestrates the `--search` flow and `writeVideo` is the video download entry point. Both are key user workflows.

**Recommended approach:**
1. **`writeVideo` testing:** The function itself is a thin wrapper around `Browser.DownloadVideo`. A mock/interface for the Browser would allow testing the status mapping logic (the `switch method` block at lines 520-533) without needing a real browser.
2. **`buildSearchFilter` testing:** This depends on `lazyBrowser()` and `Browser.Search()`. Introducing an interface for the search capability (e.g., `type Searcher interface { Search(ctx, query) ([]SearchResult, error) }`) would allow injecting a mock and testing the filter population logic.

### Priority 4: `scraper.go` -- `InjectCookies` -- 0% coverage

```
scraper.go:52:  InjectCookies  0.0%
```

**Recommended approach:** Simple unit test -- create a Scraper, call `InjectCookies` with test cookies, verify the jar contains them by making a request to an `httptest.Server` and checking the `Cookie` header.

### Priority 5: `logger.go` -- `WithGroup` -- 0% coverage

```
logger.go:103:  WithGroup  0.0%
```

**Recommended approach:** Add a test similar to `TestColorHandlerWithAttrs` that calls `h.WithGroup("mygroup")` and logs through the returned handler, verifying output includes the group context.

### Priority 6: `main.go` -- `main()` -- 0% coverage

The `main()` function (lines 85-185) is 0% covered. This is somewhat expected since `main()` is the CLI entry point, but it contains 100 lines of flag parsing, validation, and config normalization logic.

**Recommended approach:**
1. **Extract the config-building logic** into a testable function: `func buildConfig(args []string) (*Config, error)`. This would cover flag parsing, `--token-file` resolution, `--output-format` validation, and delay normalization.
2. **Test the validation rules** -- e.g., `Parallel < 1` clamped to 1, `MinDelaySec < 0` clamped to 0, `MaxDelaySec < MinDelaySec` adjusted, invalid `OutputFormat` rejected.

## Existing Test Strengths

The current test suite has several notable strengths worth preserving:

1. **Table-driven tests** -- used consistently in `models_test.go`, `format_test.go`, `search_test.go`, producing readable, extensible test cases.
2. **httptest mocking** -- `scraper_test.go` and `export_test.go` use `httptest.NewServer` effectively to test API interactions without network calls.
3. **Security regression tests** -- Tests explicitly verify SEC-1 (ID validation), SEC-8 (relative paths), SEC-11 (file permissions `0o600`), and injection resistance.
4. **Context cancellation tests** -- `throttle_test.go`, `scraper_test.go`, and `export_test.go` all verify that cancellation propagates correctly.
5. **Race detection** -- `make test` runs with `-race`, catching concurrency bugs in parallel export.
6. **Integration tests** -- `export_test.go` tests the full `Run()` pipeline (API discovery -> export -> manifest writing).

## Recommended Test Infrastructure Improvements

### 1. Add coverage reporting to the Makefile

```makefile
cover:
	go test -count=1 -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	@echo "---"
	@go tool cover -func=coverage.out | grep total:

cover-html: cover
	go tool cover -html=coverage.out -o coverage.html
```

### 2. Add a Browser interface for testability

The single highest-impact refactoring for test coverage would be introducing an interface for browser operations:

```go
type BrowserActions interface {
    Login(ctx context.Context) ([]*http.Cookie, error)
    DiscoverMeetings(ctx context.Context) ([]MeetingRef, error)
    DownloadVideo(ctx context.Context, pageURL, outputPath string) (method, result string)
    Search(ctx context.Context, query string) ([]SearchResult, error)
    Close()
}
```

This would allow `export.go` tests to inject a mock browser, enabling coverage of `writeVideo`, `buildSearchFilter`, `discoverViaBrowser`, and `lazyBrowser` without needing Chromium.

### 3. Add CI with coverage gates

The project has no CI. Adding a GitHub Actions workflow that runs `go test -race -coverprofile` and fails if coverage drops below a threshold (e.g., 55%) would prevent regression.

### 4. Add coverage to `.gitignore`

```
coverage.out
coverage.html
```

## Summary

| Category | Current | Target | Gap |
|----------|---------|--------|-----|
| Overall statement coverage | 59.2% | 75%+ | ~16% |
| `browser.go` | 0% | 40%+ | Pure logic extraction + interface |
| `search.go` browser funcs | 0% | 50%+ | Interface + HTML fixtures |
| `export.go` browser-dependent funcs | 0-30% | 70%+ | Browser interface mock |
| `main.go` config logic | 0% | 80%+ | Extract `buildConfig()` |
| `scraper.go` `InjectCookies` | 0% | 100% | Simple unit test |
| `logger.go` `WithGroup` | 0% | 100% | Simple unit test |

The most impactful single change would be introducing a `BrowserActions` interface, which would unlock testability for `browser.go`, `search.go`, and the browser-dependent paths in `export.go`, collectively covering ~450 lines of currently untested code.
