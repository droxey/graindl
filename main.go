package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	version = "dev"
	commit  = "none"
)

// ── .env ────────────────────────────────────────────────────────────────────
// GO-6: returns a map instead of mutating global os.Setenv.

func loadDotEnv(path string) map[string]string {
	env := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		return env
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 4096), 4096)

	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		env[key] = val
	}
	return env
}

// envGet returns the first non-empty value: real env var, then dotenv map.
func envGet(dotenv map[string]string, key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dotenv[key]
}

func envFloat(dotenv map[string]string, key string, fb float64) float64 {
	if s := envGet(dotenv, key); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			return v
		}
	}
	return fb
}

func envInt(dotenv map[string]string, key string, fb int) int {
	if s := envGet(dotenv, key); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			return v
		}
	}
	return fb
}

func envBool(dotenv map[string]string, key string) bool {
	s := strings.ToLower(envGet(dotenv, key))
	return s == "true" || s == "1" || s == "yes"
}

// ── Main ────────────────────────────────────────────────────────────────────

func main() {
	dotenv := loadDotEnv(".env")

	var cfg Config
	showVersion := false
	intervalStr := coalesce(envGet(dotenv, "GRAIN_WATCH_INTERVAL"), "30m")

	flag.StringVar(&cfg.OutputDir, "output", coalesce(envGet(dotenv, "GRAIN_OUTPUT_DIR"), "./recordings"), "Output directory")
	flag.StringVar(&cfg.SessionDir, "session-dir", coalesce(envGet(dotenv, "GRAIN_SESSION_DIR"), "./.grain-session"), "Browser session dir")
	flag.IntVar(&cfg.MaxMeetings, "max", envInt(dotenv, "GRAIN_MAX_MEETINGS", 0), "Max meetings (0=all)")
	flag.StringVar(&cfg.MeetingID, "id", envGet(dotenv, "GRAIN_MEETING_ID"), "Export a single meeting by ID")
	flag.BoolVar(&cfg.DryRun, "dry-run", envBool(dotenv, "GRAIN_DRY_RUN"), "List meetings that would be exported without exporting")
	flag.BoolVar(&cfg.SkipVideo, "skip-video", envBool(dotenv, "GRAIN_SKIP_VIDEO"), "Skip video downloads")
	flag.BoolVar(&cfg.AudioOnly, "audio-only", envBool(dotenv, "GRAIN_AUDIO_ONLY"), "Export audio track only (requires ffmpeg)")
	flag.BoolVar(&cfg.Overwrite, "overwrite", envBool(dotenv, "GRAIN_OVERWRITE"), "Overwrite existing")
	flag.BoolVar(&cfg.Headless, "headless", envBool(dotenv, "GRAIN_HEADLESS"), "Headless browser")
	flag.BoolVar(&cfg.CleanSession, "clean-session", false, "Wipe browser session before run")
	flag.BoolVar(&cfg.Verbose, "verbose", envBool(dotenv, "GRAIN_VERBOSE"), "Verbose output")
	flag.Float64Var(&cfg.MinDelaySec, "min-delay", envFloat(dotenv, "GRAIN_MIN_DELAY", 2.0), "Min delay (seconds)")
	flag.Float64Var(&cfg.MaxDelaySec, "max-delay", envFloat(dotenv, "GRAIN_MAX_DELAY", 6.0), "Max delay (seconds)")
	flag.IntVar(&cfg.Parallel, "parallel", envInt(dotenv, "GRAIN_PARALLEL", 1), "Number of meetings to export concurrently")
	flag.StringVar(&cfg.SearchQuery, "search", envGet(dotenv, "GRAIN_SEARCH"), "Search query to filter meetings")
	flag.BoolVar(&cfg.Watch, "watch", envBool(dotenv, "GRAIN_WATCH"), "Run continuously, polling for new meetings")
	flag.StringVar(&intervalStr, "interval", intervalStr, "Polling interval for watch mode (e.g. 5m, 30m, 1h)")
	flag.StringVar(&cfg.OutputFormat, "output-format", envGet(dotenv, "GRAIN_OUTPUT_FORMAT"), "Export format: obsidian, notion (adds frontmatter markdown)")
	flag.StringVar(&cfg.HealthcheckFile, "healthcheck-file", envGet(dotenv, "GRAIN_HEALTHCHECK_FILE"), "File to touch after each watch cycle (for monitoring)")
	flag.StringVar(&cfg.LogFormat, "log-format", envGet(dotenv, "GRAIN_LOG_FORMAT"), "Log format: color (default), json")
	flag.BoolVar(&cfg.GDrive, "gdrive", envBool(dotenv, "GRAIN_GDRIVE"), "Enable Google Drive upload after export")
	flag.StringVar(&cfg.GDriveFolderID, "gdrive-folder-id", envGet(dotenv, "GRAIN_GDRIVE_FOLDER_ID"), "Target Google Drive folder ID")
	flag.StringVar(&cfg.GDriveCredentials, "gdrive-credentials", envGet(dotenv, "GRAIN_GDRIVE_CREDENTIALS"), "Path to Google OAuth2/service-account credentials JSON")
	flag.StringVar(&cfg.GDriveTokenFile, "gdrive-token", envGet(dotenv, "GRAIN_GDRIVE_TOKEN"), "Path to cached OAuth2 token file")
	flag.BoolVar(&cfg.GDriveCleanLocal, "gdrive-clean-local", envBool(dotenv, "GRAIN_GDRIVE_CLEAN_LOCAL"), "Remove local files after successful Drive upload")
	flag.BoolVar(&cfg.GDriveServiceAcct, "gdrive-service-account", envBool(dotenv, "GRAIN_GDRIVE_SERVICE_ACCT"), "Use service account authentication")
	flag.StringVar(&cfg.GDriveConflict, "gdrive-conflict", coalesce(envGet(dotenv, "GRAIN_GDRIVE_CONFLICT"), "local-wins"), "Conflict resolution: local-wins (default), skip, newer-wins")
	flag.BoolVar(&cfg.GDriveVerify, "gdrive-verify", envBool(dotenv, "GRAIN_GDRIVE_VERIFY"), "Force Drive-side verification before uploading")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("graindl %s (%s)\n", version, commit)
		os.Exit(0)
	}

	// GO-2: set up slog with color handler or JSON, level gated by --verbose
	logLevel := slog.LevelInfo
	if cfg.Verbose {
		logLevel = slog.LevelDebug
	}
	if strings.ToLower(cfg.LogFormat) == "json" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))
	} else {
		slog.SetDefault(slog.New(NewColorHandler(os.Stderr, logLevel)))
	}

	if cfg.Parallel < 1 {
		cfg.Parallel = 1
	}
	if cfg.MinDelaySec < 0 {
		cfg.MinDelaySec = 0
	}
	if cfg.MaxDelaySec < cfg.MinDelaySec {
		cfg.MaxDelaySec = cfg.MinDelaySec + 1
	}

	// Watch mode: parse interval and validate flag combinations.
	if cfg.Watch {
		dur, err := time.ParseDuration(intervalStr)
		if err != nil {
			slog.Error("Invalid --interval value", "value", intervalStr, "error", err)
			os.Exit(1)
		}
		if dur < 1*time.Minute {
			slog.Error("--interval must be at least 1m", "value", dur)
			os.Exit(1)
		}
		cfg.WatchInterval = dur
		if cfg.MeetingID != "" {
			slog.Error("--watch cannot be used with --id")
			os.Exit(1)
		}
		if cfg.DryRun {
			slog.Error("--watch cannot be used with --dry-run")
			os.Exit(1)
		}
		if cfg.Overwrite {
			slog.Error("--watch cannot be used with --overwrite (would re-export every meeting every cycle)")
			os.Exit(1)
		}
	}

	if cfg.OutputFormat != "" {
		cfg.OutputFormat = strings.ToLower(cfg.OutputFormat)
		if cfg.OutputFormat != "obsidian" && cfg.OutputFormat != "notion" {
			slog.Error("Invalid --output-format. Must be 'obsidian' or 'notion'.")
			os.Exit(1)
		}
	}

	if cfg.GDrive {
		if cfg.GDriveFolderID == "" {
			slog.Error("--gdrive requires --gdrive-folder-id")
			os.Exit(1)
		}
		if cfg.GDriveCredentials == "" {
			slog.Error("--gdrive requires --gdrive-credentials")
			os.Exit(1)
		}
		switch cfg.GDriveConflict {
		case "local-wins", "skip", "newer-wins":
			// valid
		default:
			slog.Error("Invalid --gdrive-conflict. Must be 'local-wins', 'skip', or 'newer-wins'.")
			os.Exit(1)
		}
		if cfg.GDriveTokenFile == "" {
			cfg.GDriveTokenFile = filepath.Join(cfg.SessionDir, "gdrive-token.json")
		}
	}

	slog.Info(fmt.Sprintf("graindl %s", version))
	slog.Info(fmt.Sprintf("Output: %s", absPath(cfg.OutputDir)))
	slog.Info(fmt.Sprintf("Throttle: %.1f–%.1fs random delay", cfg.MinDelaySec, cfg.MaxDelaySec))
	if cfg.Parallel > 1 {
		slog.Info(fmt.Sprintf("Parallel: %d workers", cfg.Parallel))
	}
	if cfg.AudioOnly {
		if err := checkFFmpeg(); err != nil {
			slog.Error("--audio-only requires ffmpeg", "error", err)
			os.Exit(1)
		}
		slog.Info("Audio: extracting audio only (ffmpeg)")
	} else if cfg.SkipVideo {
		slog.Info("Video: skipped")
	}
	if cfg.Watch {
		slog.Info(fmt.Sprintf("Watch: polling every %s (Ctrl-C to stop)", cfg.WatchInterval))
	}
	if cfg.OutputFormat != "" {
		slog.Info(fmt.Sprintf("Format: %s", cfg.OutputFormat))
	}
	if cfg.GDrive {
		slog.Info(fmt.Sprintf("Google Drive: enabled (folder=%s, conflict=%s)", cfg.GDriveFolderID, cfg.GDriveConflict))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	exp, err := NewExporter(&cfg)
	if err != nil {
		slog.Error("Init failed", "error", err)
		os.Exit(1)
	}
	defer exp.Close()

	if cfg.Watch {
		if err := exp.RunWatch(ctx); err != nil {
			slog.Error("Fatal", "error", err)
			os.Exit(1)
		}
	} else {
		if err := exp.Run(ctx); err != nil {
			slog.Error("Fatal", "error", err)
			os.Exit(1)
		}
	}
}
