# graindl

Export meetings, transcripts, metadata, and videos from [Grain](https://grain.com).

## Quick Start

```bash
# 1. Clone and build
make build

# 2. Set your token
echo "your-grain-api-token" > .grain-token

# 3. Run
./graindl --token-file .grain-token
```

## Docker

```bash
echo "your-token" > .grain-token
docker compose up
```

Recordings persist on the host via the `./recordings` volume mount. The token is mounted read-only and never baked into the image.

## Usage

```
graindl [flags]
```

|Flag             |Env Var             |Default           |Description                                        |
|-----------------|--------------------|------------------|---------------------------------------------------|
|`--token`        |`GRAIN_API_TOKEN`   |                  |API token (visible in `ps` — prefer `--token-file`)|
|`--token-file`   |`GRAIN_TOKEN_FILE`  |                  |Path to file containing token                      |
|`--output`       |`GRAIN_OUTPUT_DIR`  |`./recordings`    |Output directory                                   |
|`--session-dir`  |`GRAIN_SESSION_DIR` |`./.grain-session`|Browser profile dir                                |
|`--max`          |`GRAIN_MAX_MEETINGS`|`0` (all)         |Max meetings to export                             |
|`--search`       |`GRAIN_SEARCH`      |                  |Search query to filter meetings                    |
|`--skip-video`   |`GRAIN_SKIP_VIDEO`  |`false`           |Skip video downloads                               |
|`--overwrite`    |`GRAIN_OVERWRITE`   |`false`           |Overwrite existing exports                         |
|`--headless`     |`GRAIN_HEADLESS`    |`false`           |Headless browser mode                              |
|`--clean-session`|                    |`false`           |Wipe browser session before run                    |
|`--verbose`      |`GRAIN_VERBOSE`     |`false`           |Debug-level logging                                |
|`--version`      |                    |                  |Print version and exit                             |
|`--min-delay`    |`GRAIN_MIN_DELAY`   |`2.0`             |Min throttle delay (seconds)                       |
|`--max-delay`    |`GRAIN_MAX_DELAY`   |`6.0`             |Max throttle delay (seconds)                       |

Config is read from `.env` if present, with real environment variables taking priority. Flags override both.

### Search Filtering

Use `--search` to export only meetings matching a query:

```bash
./graindl --token-file .grain-token --search "Q4 planning"
```

This navigates Grain's search UI, extracts matching meeting IDs, then exports only those meetings. The search handles infinite scroll to capture all results. Combine with `--max` to limit output.

## Development

```bash
make build    # Build binary with version/commit from git
make test     # Run tests with -race detector
make lint     # Run golangci-lint (install: https://golangci-lint.run/usage/install/)
make clean    # Remove binary
make docker   # Build Docker image
```

### Testing

The test suite covers models, scraper (with `httptest` servers), logger, throttle, and export pipeline. Tests verify security properties from the audit including file permissions (`0o600`/`0o700`), relative paths in manifests, URL encoding, and input sanitization.

```bash
go test -v -count=1 ./...
```

## Security

This tool handles authentication credentials and outputs sensitive meeting data. The security model follows the principle of least privilege at every layer.

**Credentials.** The `--token-file` flag is the recommended method. It reads the token from a file at startup, keeping it out of the process argument list (where `--token` would be visible via `ps`). File-based tokens also work cleanly in Docker via read-only volume mounts.

**File Permissions.** Session directories (`.grain-session/`) are created at `0o700` (owner-only). All output files — metadata JSON, transcripts, manifests — are written at `0o600`. Output directories use standard `0o755`.

**Network Safeguards.** API responses are bounded to 50MB via `io.LimitReader`. Pagination has a 100-page circuit breaker. All HTTP operations thread `context.Context` for cancellation and timeout propagation. URL parameters use `url.Values.Encode()` to prevent injection.

**Input Sanitization.** Meeting IDs and titles are sanitized before use as filenames: path separators, traversal sequences (`..`), control characters, leading dots, and special characters are all stripped or replaced. The manifest stores relative paths only.

**Browser Stealth.** Automation markers (`navigator.webdriver`, `AutomationControlled` blink feature) are suppressed. The `--clean-session` flag wipes the browser profile for a fresh fingerprint.

**Known Limitations.** Rod's `MustWaitDownload` has no cancellation API — a stalled video download leaks one goroutine until the process exits. This is documented in the code and mitigated by a 5-minute timeout.

## License

MIT — see [LICENSE](LICENSE).
