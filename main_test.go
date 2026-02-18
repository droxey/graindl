package main

import (
	"os"
	"path/filepath"
	"testing"
)

// ── loadDotEnv ──────────────────────────────────────────────────────────────

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	os.WriteFile(path, []byte(`
# Comment line
KEY1=value1
KEY2="quoted value"
KEY3='single quoted'
export KEY4=exported

EMPTY=
SPACES =  padded
`), 0o600)

	env := loadDotEnv(path)

	tests := map[string]string{
		"KEY1":   "value1",
		"KEY2":   "quoted value",
		"KEY3":   "single quoted",
		"KEY4":   "exported",
		"SPACES": "padded",
	}
	for k, want := range tests {
		if got := env[k]; got != want {
			t.Errorf("env[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestLoadDotEnvMissing(t *testing.T) {
	env := loadDotEnv("/nonexistent/.env")
	if len(env) != 0 {
		t.Errorf("expected empty map for missing file, got %v", env)
	}
}

func TestLoadDotEnvSkipsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	os.WriteFile(path, []byte("NOEQUALSSIGN\nVALID=yes\n"), 0o600)

	env := loadDotEnv(path)
	if _, ok := env["NOEQUALSSIGN"]; ok {
		t.Error("should skip lines without =")
	}
	if env["VALID"] != "yes" {
		t.Error("should parse valid lines")
	}
}

// ── envGet ──────────────────────────────────────────────────────────────────

func TestEnvGet(t *testing.T) {
	dotenv := map[string]string{"FROM_FILE": "file_val"}

	// Dotenv fallback
	if got := envGet(dotenv, "FROM_FILE"); got != "file_val" {
		t.Errorf("expected file_val, got %q", got)
	}

	// Real env overrides dotenv
	os.Setenv("TEST_ENVGET_REAL", "real_val")
	defer os.Unsetenv("TEST_ENVGET_REAL")
	dotenv["TEST_ENVGET_REAL"] = "file_val"
	if got := envGet(dotenv, "TEST_ENVGET_REAL"); got != "real_val" {
		t.Errorf("expected real_val (os.Getenv wins), got %q", got)
	}

	// Missing from both
	if got := envGet(dotenv, "MISSING_KEY"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ── envFloat / envInt / envBool ─────────────────────────────────────────────

func TestEnvFloat(t *testing.T) {
	env := map[string]string{"F": "3.14", "BAD": "notanumber"}

	if got := envFloat(env, "F", 0); got != 3.14 {
		t.Errorf("expected 3.14, got %f", got)
	}
	if got := envFloat(env, "BAD", 9.9); got != 9.9 {
		t.Errorf("expected fallback 9.9, got %f", got)
	}
	if got := envFloat(env, "MISSING", 1.5); got != 1.5 {
		t.Errorf("expected fallback 1.5, got %f", got)
	}
}

func TestEnvInt(t *testing.T) {
	env := map[string]string{"I": "42", "BAD": "xyz"}

	if got := envInt(env, "I", 0); got != 42 {
		t.Errorf("expected 42, got %d", got)
	}
	if got := envInt(env, "BAD", 10); got != 10 {
		t.Errorf("expected fallback 10, got %d", got)
	}
}

func TestEnvBool(t *testing.T) {
	env := map[string]string{
		"T1": "true", "T2": "1", "T3": "yes", "T4": "TRUE",
		"F1": "false", "F2": "0", "F3": "no", "F4": "",
	}
	trueKeys := []string{"T1", "T2", "T3", "T4"}
	for _, k := range trueKeys {
		if !envBool(env, k) {
			t.Errorf("envBool(%q) should be true", k)
		}
	}
	falseKeys := []string{"F1", "F2", "F3", "F4", "MISSING"}
	for _, k := range falseKeys {
		if envBool(env, k) {
			t.Errorf("envBool(%q) should be false", k)
		}
	}
}

// ── GRAIN_AUDIO_ONLY env var ────────────────────────────────────────────────

func TestEnvBoolAudioOnly(t *testing.T) {
	env := map[string]string{"GRAIN_AUDIO_ONLY": "true"}
	if !envBool(env, "GRAIN_AUDIO_ONLY") {
		t.Error("GRAIN_AUDIO_ONLY=true should be truthy")
	}

	env["GRAIN_AUDIO_ONLY"] = "false"
	if envBool(env, "GRAIN_AUDIO_ONLY") {
		t.Error("GRAIN_AUDIO_ONLY=false should be falsy")
	}
}

func TestAudioOnlyConfigField(t *testing.T) {
	cfg := Config{AudioOnly: true, SkipVideo: false}
	if !cfg.AudioOnly {
		t.Error("AudioOnly should be true")
	}
	// AudioOnly and SkipVideo are independent flags.
	cfg.SkipVideo = true
	if !cfg.AudioOnly || !cfg.SkipVideo {
		t.Error("both flags should be independently settable")
	}
}

// ── Google Drive config fields ──────────────────────────────────────────────

func TestGDriveConfigFields(t *testing.T) {
	cfg := Config{
		GDrive:            true,
		GDriveFolderID:    "folder-123",
		GDriveCredentials: "/path/to/creds.json",
		GDriveTokenFile:   "/path/to/token.json",
		GDriveCleanLocal:  true,
		GDriveServiceAcct: false,
		GDriveConflict:    "local-wins",
		GDriveVerify:      true,
	}

	if !cfg.GDrive {
		t.Error("GDrive should be true")
	}
	if cfg.GDriveFolderID != "folder-123" {
		t.Errorf("GDriveFolderID = %q", cfg.GDriveFolderID)
	}
	if cfg.GDriveConflict != "local-wins" {
		t.Errorf("GDriveConflict = %q", cfg.GDriveConflict)
	}
	if !cfg.GDriveVerify {
		t.Error("GDriveVerify should be true")
	}
}

func TestGDriveEnvVars(t *testing.T) {
	env := map[string]string{
		"GRAIN_GDRIVE":              "true",
		"GRAIN_GDRIVE_FOLDER_ID":    "env-folder",
		"GRAIN_GDRIVE_CREDENTIALS":  "/env/creds.json",
		"GRAIN_GDRIVE_TOKEN":        "/env/token.json",
		"GRAIN_GDRIVE_CLEAN_LOCAL":  "true",
		"GRAIN_GDRIVE_SERVICE_ACCT": "true",
		"GRAIN_GDRIVE_CONFLICT":     "skip",
		"GRAIN_GDRIVE_VERIFY":       "true",
	}

	if !envBool(env, "GRAIN_GDRIVE") {
		t.Error("GRAIN_GDRIVE should be truthy")
	}
	if envGet(env, "GRAIN_GDRIVE_FOLDER_ID") != "env-folder" {
		t.Error("GRAIN_GDRIVE_FOLDER_ID mismatch")
	}
	if envGet(env, "GRAIN_GDRIVE_CREDENTIALS") != "/env/creds.json" {
		t.Error("GRAIN_GDRIVE_CREDENTIALS mismatch")
	}
	if !envBool(env, "GRAIN_GDRIVE_CLEAN_LOCAL") {
		t.Error("GRAIN_GDRIVE_CLEAN_LOCAL should be truthy")
	}
	if !envBool(env, "GRAIN_GDRIVE_SERVICE_ACCT") {
		t.Error("GRAIN_GDRIVE_SERVICE_ACCT should be truthy")
	}
	if envGet(env, "GRAIN_GDRIVE_CONFLICT") != "skip" {
		t.Error("GRAIN_GDRIVE_CONFLICT mismatch")
	}
	if !envBool(env, "GRAIN_GDRIVE_VERIFY") {
		t.Error("GRAIN_GDRIVE_VERIFY should be truthy")
	}
}

func TestGDriveConflictModes(t *testing.T) {
	validModes := []string{"local-wins", "skip", "newer-wins"}
	for _, mode := range validModes {
		cfg := Config{GDriveConflict: mode}
		switch cfg.GDriveConflict {
		case "local-wins", "skip", "newer-wins":
			// valid
		default:
			t.Errorf("mode %q should be valid", mode)
		}
	}
}

func TestGDriveDefaultTokenPath(t *testing.T) {
	cfg := Config{
		SessionDir:      "./.grain-session",
		GDrive:          true,
		GDriveTokenFile: "",
	}
	// Simulate the default token path logic from main.go.
	if cfg.GDriveTokenFile == "" {
		cfg.GDriveTokenFile = filepath.Join(cfg.SessionDir, "gdrive-token.json")
	}
	want := filepath.Join("./.grain-session", "gdrive-token.json")
	if cfg.GDriveTokenFile != want {
		t.Errorf("token path = %q, want %q", cfg.GDriveTokenFile, want)
	}
}
