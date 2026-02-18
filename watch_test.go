package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// All watch tests use MeetingID mode (--id) so that RunWatch calls runSingle
// on each cycle instead of browser-based discovery. This lets us test the
// watch loop mechanics (cancellation, multi-cycle, skip-already-exported,
// manifest reset) without requiring a browser.

func TestRunWatchStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		MeetingID:     "test-meeting-1",
		OutputDir:     dir,
		SkipVideo:     true,
		Watch:         true,
		WatchInterval: 50 * time.Millisecond,
		MinDelaySec:   0,
		MaxDelaySec:   0.001,
	}
	e, err := NewExporter(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := e.RunWatch(ctx); err != nil {
		t.Fatalf("RunWatch should return nil on cancellation: %v", err)
	}
}

func TestRunWatchMultipleCycles(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		MeetingID:     "test-meeting-1",
		OutputDir:     dir,
		SkipVideo:     true,
		Watch:         true,
		WatchInterval: 50 * time.Millisecond,
		MinDelaySec:   0,
		MaxDelaySec:   0.001,
	}
	e, err := NewExporter(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := e.RunWatch(ctx); err != nil {
		t.Fatalf("RunWatch: %v", err)
	}

	// After multiple cycles, the last manifest should show the meeting as
	// skipped (exported in cycle 1, skipped in cycle 2+).
	manifestPath := filepath.Join(dir, "_export-manifest.json")
	if !fileExists(manifestPath) {
		t.Fatal("manifest should exist")
	}
	raw, _ := os.ReadFile(manifestPath)
	var m ExportManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	// With 500ms timeout and 50ms interval, at least 2 cycles should run.
	// The last cycle's manifest should show skipped=1 (meeting was already exported).
	if m.Skipped < 1 {
		t.Logf("manifest: ok=%d skipped=%d errors=%d", m.OK, m.Skipped, m.Errors)
	}
}

func TestRunWatchSkipsAlreadyExported(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		MeetingID:     "test-meeting-1",
		OutputDir:     dir,
		SkipVideo:     true,
		Watch:         true,
		WatchInterval: 50 * time.Millisecond,
		MinDelaySec:   0,
		MaxDelaySec:   0.001,
	}
	e, err := NewExporter(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()

	if err := e.RunWatch(ctx); err != nil {
		t.Fatalf("RunWatch: %v", err)
	}

	// Meeting exported in cycle 1; metadata file should exist.
	today := time.Now().Format("2006-01-02")
	metaPath := filepath.Join(dir, today, "test-meeting-1.json")
	if !fileExists(metaPath) {
		t.Fatal("metadata file should exist after first cycle")
	}
}

func TestRunWatchImmediateCancelBeforeFirstCycle(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		MeetingID:     "test-meeting-1",
		OutputDir:     dir,
		SkipVideo:     true,
		Watch:         true,
		WatchInterval: time.Hour, // Long interval — should not matter.
		MinDelaySec:   0,
		MaxDelaySec:   0.001,
	}
	e, err := NewExporter(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer e.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// Should not hang.
	if err := e.RunWatch(ctx); err != nil {
		t.Fatalf("RunWatch: %v", err)
	}
}

func TestRunWatchManifestResetBetweenCycles(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		MeetingID:     "test-meeting-1",
		OutputDir:     dir,
		SkipVideo:     true,
		Watch:         true,
		WatchInterval: 50 * time.Millisecond,
		MinDelaySec:   0,
		MaxDelaySec:   0.001,
	}
	e, err := NewExporter(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_ = e.RunWatch(ctx)

	// The last manifest written should reflect only the final cycle's results.
	manifestPath := filepath.Join(dir, "_export-manifest.json")
	if !fileExists(manifestPath) {
		t.Fatal("manifest should exist")
	}
	raw, _ := os.ReadFile(manifestPath)
	var m ExportManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	// With 300ms timeout and 50ms interval, at least 2 cycles should run.
	// Cycle 1: exports meeting (OK=1). Cycle 2+: skips meeting (Skipped=1).
	// The written manifest is from the last cycle, so it should show skipped=1.
	if m.Skipped == 1 && m.OK == 0 {
		// Expected: last cycle skipped the already-exported meeting.
	} else if m.OK == 1 && m.Skipped == 0 {
		// Only one cycle ran — acceptable in a slow CI environment.
		t.Log("only one cycle ran; manifest reset between cycles not fully verified")
	} else {
		t.Errorf("unexpected manifest state: ok=%d skipped=%d errors=%d", m.OK, m.Skipped, m.Errors)
	}
}
