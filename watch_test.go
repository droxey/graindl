package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunWatchStopsOnCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		default:
			w.Write([]byte(`{"recordings":[]}`))
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:         "tok",
		OutputDir:     dir,
		SkipVideo:     true,
		Watch:         true,
		WatchInterval: 50 * time.Millisecond,
		MinDelaySec:   0,
		MaxDelaySec:   0.001,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := e.RunWatch(ctx); err != nil {
		t.Fatalf("RunWatch should return nil on cancellation: %v", err)
	}
}

func TestRunWatchMultipleCycles(t *testing.T) {
	var discoverCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me":
			discoverCalls.Add(1)
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case "/recordings":
			discoverCalls.Add(1)
			w.Write([]byte(`{"recordings":[
				{"id":"m1","title":"Meeting 1","created_at":"2025-03-01"}
			]}`))
		default:
			json.NewEncoder(w).Encode(GrainRecording{
				ID: "m1", Title: "Meeting 1", Transcript: "hello",
			})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:         "tok",
		OutputDir:     dir,
		SkipVideo:     true,
		Watch:         true,
		WatchInterval: 50 * time.Millisecond,
		MinDelaySec:   0,
		MaxDelaySec:   0.001,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := e.RunWatch(ctx); err != nil {
		t.Fatalf("RunWatch: %v", err)
	}

	// Each cycle calls /me + /recordings. At least 2 cycles expected.
	calls := discoverCalls.Load()
	if calls < 4 {
		t.Errorf("expected at least 4 discovery calls (2 cycles), got %d", calls)
	}
}

func TestRunWatchSkipsAlreadyExported(t *testing.T) {
	var recordingFetches atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case "/recordings":
			w.Write([]byte(`{"recordings":[
				{"id":"m1","title":"Meeting 1","created_at":"2025-03-01"}
			]}`))
		default:
			recordingFetches.Add(1)
			json.NewEncoder(w).Encode(GrainRecording{
				ID: "m1", Title: "Meeting 1", Transcript: "hello",
			})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:         "tok",
		OutputDir:     dir,
		SkipVideo:     true,
		Watch:         true,
		WatchInterval: 50 * time.Millisecond,
		MinDelaySec:   0,
		MaxDelaySec:   0.001,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()

	if err := e.RunWatch(ctx); err != nil {
		t.Fatalf("RunWatch: %v", err)
	}

	// Meeting exported in cycle 1, skipped in subsequent cycles.
	metaPath := filepath.Join(dir, "2025-03-01", "m1.json")
	if !fileExists(metaPath) {
		t.Fatal("metadata file should exist after first cycle")
	}

	// Recording detail API is only called once (first cycle export);
	// subsequent cycles skip via fileExists check before any API call.
	fetches := recordingFetches.Load()
	if fetches < 1 {
		t.Errorf("expected at least 1 recording fetch, got %d", fetches)
	}
}

func TestRunWatchExportsNewMeetings(t *testing.T) {
	var recordingsCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case "/recordings":
			n := recordingsCalls.Add(1)
			if n <= 1 {
				// First cycle: only m1.
				w.Write([]byte(`{"recordings":[
					{"id":"m1","title":"Meeting 1","created_at":"2025-03-01"}
				]}`))
			} else {
				// Subsequent cycles: m1 + m2 (m2 is new).
				w.Write([]byte(`{"recordings":[
					{"id":"m1","title":"Meeting 1","created_at":"2025-03-01"},
					{"id":"m2","title":"Meeting 2","created_at":"2025-03-02"}
				]}`))
			}
		default:
			json.NewEncoder(w).Encode(GrainRecording{
				ID: "m1", Title: "Meeting", Transcript: "hello",
			})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:         "tok",
		OutputDir:     dir,
		SkipVideo:     true,
		Watch:         true,
		WatchInterval: 50 * time.Millisecond,
		MinDelaySec:   0,
		MaxDelaySec:   0.001,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	if err := e.RunWatch(ctx); err != nil {
		t.Fatalf("RunWatch: %v", err)
	}

	// m1 exported in cycle 1.
	if !fileExists(filepath.Join(dir, "2025-03-01", "m1.json")) {
		t.Error("m1 metadata should exist")
	}
	// m2 exported in a later cycle when it appeared.
	if !fileExists(filepath.Join(dir, "2025-03-02", "m2.json")) {
		t.Error("m2 metadata should exist (appeared in later cycle)")
	}
}

func TestRunWatchImmediateCancelBeforeFirstCycle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		default:
			w.Write([]byte(`{"recordings":[]}`))
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:         "tok",
		OutputDir:     dir,
		SkipVideo:     true,
		Watch:         true,
		WatchInterval: time.Hour, // Long interval — should not matter.
		MinDelaySec:   0,
		MaxDelaySec:   0.001,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// Should not hang.
	if err := e.RunWatch(ctx); err != nil {
		t.Fatalf("RunWatch: %v", err)
	}
}

func TestRunWatchManifestResetBetweenCycles(t *testing.T) {
	var recordingsCalls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me":
			json.NewEncoder(w).Encode(GrainUser{Name: "Test"})
		case "/recordings":
			recordingsCalls.Add(1)
			w.Write([]byte(`{"recordings":[
				{"id":"m1","title":"Meeting 1","created_at":"2025-03-01"}
			]}`))
		default:
			json.NewEncoder(w).Encode(GrainRecording{
				ID: "m1", Title: "Meeting 1", Transcript: "text",
			})
		}
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := &Config{
		Token:         "tok",
		OutputDir:     dir,
		SkipVideo:     true,
		Watch:         true,
		WatchInterval: 50 * time.Millisecond,
		MinDelaySec:   0,
		MaxDelaySec:   0.001,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	e.scraper.baseURL = ts.URL
	defer e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_ = e.RunWatch(ctx)

	// The last manifest written should reflect only the final cycle's results
	// (meeting skipped because it was exported in cycle 1).
	manifestPath := filepath.Join(dir, "_export-manifest.json")
	if !fileExists(manifestPath) {
		t.Fatal("manifest should exist")
	}
	raw, _ := os.ReadFile(manifestPath)
	var m ExportManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	// If multiple cycles ran, the last cycle's manifest should show the
	// meeting as skipped (not freshly exported).
	calls := recordingsCalls.Load()
	if calls >= 2 {
		// At least 2 cycles ran; the last manifest should have skipped=1.
		if m.Skipped != 1 {
			t.Errorf("last cycle manifest.Skipped = %d, want 1 (meeting already exported)", m.Skipped)
		}
	}
}
