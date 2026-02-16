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
