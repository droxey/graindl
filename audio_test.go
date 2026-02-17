package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCheckFFmpeg(t *testing.T) {
	// This test depends on ffmpeg being installed in the test environment.
	// It verifies the detection logic either way.
	err := checkFFmpeg()
	if _, lookErr := exec.LookPath("ffmpeg"); lookErr != nil {
		// ffmpeg not installed — checkFFmpeg should return an error.
		if err == nil {
			t.Error("checkFFmpeg should fail when ffmpeg is not on PATH")
		}
	} else {
		// ffmpeg is installed — checkFFmpeg should succeed.
		if err != nil {
			t.Errorf("checkFFmpeg failed unexpectedly: %v", err)
		}
	}
}

func TestExtractAudioRequiresFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available, skipping extraction test")
	}

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.mp4")
	outputPath := filepath.Join(dir, "output.m4a")

	// Write an invalid file — ffmpeg should fail gracefully.
	os.WriteFile(inputPath, []byte("not a real video"), 0o600)

	err := extractAudio(context.Background(), inputPath, outputPath, false)
	if err == nil {
		t.Error("extractAudio should fail on invalid input")
	}
}

func TestExtractAudioRespectsContext(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available, skipping context test")
	}

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.mp4")
	outputPath := filepath.Join(dir, "output.m4a")
	os.WriteFile(inputPath, []byte("not a real video"), 0o600)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// Should not hang — context cancellation propagates to ffmpeg.
	_ = extractAudio(ctx, inputPath, outputPath, false)
}

func TestFixPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.m4a")
	// Create file with default permissions.
	os.WriteFile(path, []byte("audio data"), 0o644)

	if err := fixPerms(path); err != nil {
		t.Fatalf("fixPerms: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// SEC-11: output files must be 0o600.
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600, got %04o", info.Mode().Perm())
	}
}
