# graindl — Security Audit

**Date**: 2026-02-14
**Scope**: Full codebase (962 lines, 6 Go files + bash)
**Categories**: Security, Anonymity/Fingerprinting, Go Best Practices (2026)

-----

## Critical

### SEC-1: Query Parameter Injection (scraper.go:46-52)

URL built via string concatenation without encoding. A cursor value or recording ID containing `&`, `=`, or `#` corrupts the URL. A malicious API response could inject parameters.

```go
// CURRENT — injectable
parts = append(parts, k+"="+v)
reqURL += "?" + strings.Join(parts, "&")

// FIX — use net/url.Values
q := make(url.Values)
for k, v := range params { q.Set(k, v) }
reqURL += "?" + q.Encode()
```

**Confidence**: High

### SEC-2: Token Visible in Process List (main.go:81)

`--token gk_...` appears in `ps aux`, `/proc/*/cmdline`, shell history, and process monitors. Any local user can read it.

```go
// FIX — read from file
flag.StringVar(&cfg.TokenFile, "token-file", "", "Path to file containing API token")

// In config resolution:
if cfg.TokenFile != "" {
    data, err := os.ReadFile(cfg.TokenFile)
    // ...
    cfg.Token = strings.TrimSpace(string(data))
}
```

Also: print a warning if `--token` is used directly.

### SEC-3: Session Directory World-Readable (browser.go:29, models.go:141)

`ensureDir` creates all directories at `0o755`. The `.grain-session/chromium-profile/` directory contains session cookies, auth tokens, and browsing state. Any local user can read them.

```go
// FIX — restrictive perms for sensitive dirs
func ensureDirPrivate(dir string) error { return os.MkdirAll(dir, 0o700) }
```

Apply to `SessionDir` and its children. Keep `0o755` for `OutputDir`.

### SEC-4: Unbounded io.ReadAll (scraper.go:68)

`io.ReadAll(resp.Body)` with no size limit. A compromised or malicious API endpoint returns a multi-GB response → OOM.

```go
// FIX — cap at 50MB
const maxResponseSize = 50 << 20
body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
```

-----

## High

### SEC-5: Data Race in Network Intercept (browser.go:263-282)

`found` is written from the hijack callback goroutine and read from the calling goroutine with no synchronization. This is a data race — `go test -race` will flag it.

```go
// FIX — use atomic or channel
var found atomic.Value // stores string

router.MustAdd("*", func(ctx *rod.Hijack) {
    u := ctx.Request.URL().String()
    if videoRe.MatchString(u) {
        found.Store(u)
    }
    ctx.ContinueRequest(&proto.FetchContinueRequest{})
})
// ...
if v := found.Load(); v != nil {
    return v.(string)
}
```

### SEC-6: Goroutine Leak on Download Timeout (browser.go:227-239)

If the 5-minute timeout fires, the goroutine running `wait()` blocks forever — it's never cancelled. Over multiple failed downloads, this leaks goroutines and browser resources.

```go
// FIX — context cancellation
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
defer cancel()

ch := make(chan []byte, 1)
go func() {
    ch <- wait()
}()

select {
case data := <-ch:
    // handle
case <-ctx.Done():
    // The goroutine still leaks, but at minimum log it.
    // Rod's WaitDownload doesn't support context — document this limitation.
}
```

Rod's `MustWaitDownload` has no cancellation. This is a known limitation — document it and consider a watchdog that kills the browser page on timeout.

### SEC-7: No Signal Handling / Graceful Shutdown (main.go)

SIGINT/SIGTERM during export leaves: orphaned Chromium processes, partially-written files, no manifest update. The `defer exp.Close()` only runs on normal exit.

```go
// FIX
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

// Pass ctx through pipeline, check ctx.Err() between meetings
```

### SEC-8: Manifest Leaks Absolute Paths (export.go)

`ExportResult.MetadataPath`, `VideoPath`, `TranscriptPaths` contain absolute filesystem paths. If the manifest is shared or committed, it leaks directory structure (`/home/dani/...`).

```go
// FIX — store relative paths
r.MetadataPath, _ = filepath.Rel(e.cfg.OutputDir, path)
```

### ANON-1: No Chromium Stealth Mode (browser.go:34-37)

Rod's default Chromium exposes `navigator.webdriver = true` and other automation markers. Grain's frontend or CDN (Cloudflare, etc.) can detect and block automated access.

```go
// FIX
u, err := launcher.New().
    Headless(cfg.Headless).
    UserDataDir(profileDir).
    Set("disable-blink-features", "AutomationControlled").
    Launch()

// After page creation:
page.MustEvalOnNewDocument(`() => {
    Object.defineProperty(navigator, 'webdriver', {get: () => false});
}`)
```

-----

## Medium

### SEC-9: Sanitize Doesn't Block Path Traversal Components (models.go:130-138)

`sanitize()` strips `/` and `\` but doesn't handle `..` explicitly. Since meeting IDs are UUIDs this is low-risk, but if a title or other user-controlled input flows through `sanitize` → filepath:

```go
// FIX — also strip dot sequences and leading dots
s = strings.ReplaceAll(s, "..", "")
s = strings.TrimLeft(s, ".")
```

### SEC-10: .env Parser Doesn't Handle Edge Cases (main.go:27-49)

1. No max line length → malicious `.env` could OOM the scanner (unlikely but sloppy)
1. `export KEY=val` syntax not handled (common in shell-compatible .env files)
1. Multi-line values not supported

```go
// FIX — strip "export " prefix, set scanner buffer limit
scanner.Buffer(make([]byte, 0, 4096), 4096)
key = strings.TrimPrefix(key, "export ")
```

### SEC-11: Output Files World-Readable (models.go:158, export.go)

All output files written at `0o644`. Meeting transcripts and metadata may contain sensitive business content. On shared machines, other users can read them.

```go
// FIX — 0o600 for all output files
os.WriteFile(path, data, 0o600)
```

### ANON-2: Hardcoded Static User-Agent (browser.go, scraper.go absent)

The Scraper doesn't set a User-Agent at all for API calls. The browser uses Rod's default Chromium UA. Neither rotates. The API Bearer token already identifies the user, so UA fingerprinting is moot for API calls — but the absence of UA on API requests is unusual and may flag rate limiters.

```go
// FIX — set a reasonable UA on API requests too
req.Header.Set("User-Agent", "graindl/1.0")
```

### ANON-3: Cookie Injection Logs Count to Stdout (scraper.go:39)

`"Injected 42 cookies"` is operational telemetry, not a security issue per se, but if stdout is piped to a log aggregator, cookie count is a session fingerprint.

```go
// FIX — gate behind verbose flag or use slog at Debug level
```

### GO-1: No context.Context Anywhere

Every HTTP request uses `http.NewRequest` (no context). Every Rod operation uses bare timeouts. The entire pipeline has no cancellation path. This is the single biggest Go anti-pattern in the codebase.

**Every function that does I/O should accept `context.Context` as first argument.** This is non-negotiable in 2026 Go.

```go
func (s *Scraper) apiGet(ctx context.Context, path string, ...) (map[string]any, error) {
    req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
    // ...
}
```

### GO-2: Should Use log/slog Instead of Ad-Hoc Color Functions

`info()`, `warn()`, `errorf()`, `dim()` are hand-rolled printf wrappers with no levels, no structured fields, no output control. Since Go 1.21, `log/slog` is stdlib.

```go
// FIX — slog with a color-aware handler
logger := slog.New(newColorHandler(os.Stderr, slog.LevelInfo))
logger.Info("metadata exported", "meeting", ref.ID, "path", metaPath)
```

Benefits: structured output, level filtering, JSON mode for piping, testable.

### GO-3: Untyped map[string]any for API Responses

Every API response is `map[string]any` with string-key lookups via `getStr()`. This is fragile, untestable, and loses compile-time safety. Define structs.

```go
type GrainRecording struct {
    ID        string          `json:"id"`
    Title     string          `json:"title"`
    CreatedAt string          `json:"created_at"`
    Duration  float64         `json:"duration"`
    // ...
}
```

### GO-4: Infinite Pagination Loop (scraper.go:92-105)

`ListRecordings` has no max-page guard. A buggy API returning the same cursor forever → infinite loop + infinite API calls + infinite rate-limit burn.

```go
// FIX
const maxPages = 100
for page := 1; page <= maxPages; page++ {
```

-----

## Low

### GO-5: Stale Comment (throttle.go:9)

```
// Used for Rod browser ops. Colly uses its own LimitRule.RandomDelay.
```

Colly was removed. Comment is wrong.

### GO-6: os.Setenv Mutates Global State (main.go:47)

`loadDotEnv` calls `os.Setenv` which is a global side effect. Fine for a CLI but would break if this code ever became a library. Better to return a `map[string]string` and merge in `main()`.

### GO-7: Error Value Not Checked (scraper.go:24)

```go
jar, _ := cookiejar.New(nil)
```

`cookiejar.New(nil)` never actually errors (the `*cookiejar.Options` param is optional), but ignoring errors is a lint violation.

### GO-8: go.mod Targets 1.22, Should Be 1.23+

Go 1.22 is past EOL. Target the latest stable release for security patches and toolchain support.

### GO-9: No -version Flag

CLI should print build version/commit for debugging. Use `go build -ldflags`.

### ANON-4: Chromium Profile Reuse Across Runs

Persistent `UserDataDir` means cached data, localStorage, and cookies accumulate across runs. This is intentional (session persistence) but creates a long-lived fingerprint. Document the trade-off; add a `--clean-session` flag.

-----

## Summary

|Severity|Count|Categories                                                           |
|--------|-----|---------------------------------------------------------------------|
|Critical|4    |Injection, credential exposure, permissions, DoS                     |
|High    |5    |Data race, resource leak, shutdown, path leak, detection             |
|Medium  |7    |Traversal, parsing, permissions, fingerprint, context, logging, types|
|Low     |5    |Comments, globals, lint, toolchain, UX                               |

**Top 3 fixes by impact:**

1. **Add `context.Context` throughout** (GO-1) — enables cancellation, timeout propagation, and graceful shutdown. Biggest structural improvement.
1. **Fix query parameter encoding** (SEC-1) — one-line fix, prevents injection.
1. **Restrict session dir permissions** (SEC-3) — one-line fix, prevents credential theft.
