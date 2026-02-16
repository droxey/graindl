package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestScraper creates a Scraper pointed at the given httptest.Server.
func newTestScraper(t *testing.T, ts *httptest.Server, token string) *Scraper {
	t.Helper()
	s, err := NewScraper(&Config{
		Token:       token,
		MinDelaySec: 0,
		MaxDelaySec: 0.01,
	})
	if err != nil {
		t.Fatalf("NewScraper: %v", err)
	}
	s.baseURL = ts.URL
	return s
}

// ── Me ──────────────────────────────────────────────────────────────────────

func TestMe(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth = %q, want Bearer test-token", got)
		}
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Errorf("ua = %q, want %q", got, userAgent)
		}
		json.NewEncoder(w).Encode(GrainUser{ID: "u1", Name: "Alice", Email: "alice@example.com"})
	}))
	defer ts.Close()

	s := newTestScraper(t, ts, "test-token")
	user, err := s.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if user.Name != "Alice" {
		t.Errorf("Name = %q, want Alice", user.Name)
	}
	if user.Email != "alice@example.com" {
		t.Errorf("Email = %q", user.Email)
	}
}

func TestMeNoToken(t *testing.T) {
	s, err := NewScraper(&Config{MinDelaySec: 0, MaxDelaySec: 0.01})
	if err != nil {
		t.Fatalf("NewScraper: %v", err)
	}
	_, err = s.Me(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no API token") {
		t.Errorf("expected 'no API token' error, got: %v", err)
	}
}

// ── ListRecordings ──────────────────────────────────────────────────────────

func TestListRecordingsPagination(t *testing.T) {
	page := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		switch page {
		case 1:
			w.Write([]byte(`{ "recordings": [ {"id": "r1", "title": "Meeting 1"}, {"id": "r2", "title": "Meeting 2"} ], "cursor": "page2" }`))
		case 2:
			w.Write([]byte(`{ "recordings": [{"id": "r3", "title": "Meeting 3"}] }`))
		default:
			t.Error("should not request page 3")
			w.Write([]byte(`{}`))
		}
	}))
	defer ts.Close()

	s := newTestScraper(t, ts, "tok")
	recs, err := s.ListRecordings(context.Background())
	if err != nil {
		t.Fatalf("ListRecordings: %v", err)
	}
	if len(recs) != 3 {
		t.Errorf("expected 3 recordings, got %d", len(recs))
	}
	if recs[2].ID != "r3" {
		t.Errorf("third recording ID = %q", recs[2].ID)
	}
}

func TestListRecordingsAlternateKeys(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"d1","name":"Alt Name"}],"next_cursor":""}`))
	}))
	defer ts.Close()

	s := newTestScraper(t, ts, "tok")
	recs, err := s.ListRecordings(context.Background())
	if err != nil {
		t.Fatalf("ListRecordings: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "d1" {
		t.Errorf("expected 1 recording with id d1, got %v", recs)
	}
}

func TestListRecordingsContextCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"recordings":[{"id":"r1"}],"cursor":"next"}`))
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	s := newTestScraper(t, ts, "tok")

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := s.ListRecordings(ctx)
	if err == nil {
		t.Error("expected cancellation error")
	}
}

// ── GetRecording ────────────────────────────────────────────────────────────

func TestGetRecording(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("intelligence_notes_format") != "json" {
			t.Error("missing intelligence_notes_format")
		}
		if r.URL.Query().Get("transcript_format") != "vtt" {
			t.Error("missing transcript_format")
		}
		json.NewEncoder(w).Encode(GrainRecording{
			ID: "rec-1", Title: "Test Meeting",
			Transcript: "Hello from the transcript",
		})
	}))
	defer ts.Close()

	s := newTestScraper(t, ts, "tok")
	rec, err := s.GetRecording(context.Background(), "rec-1", "vtt")
	if err != nil {
		t.Fatalf("GetRecording: %v", err)
	}
	if rec.Title != "Test Meeting" {
		t.Errorf("Title = %q", rec.Title)
	}
}

func TestGetTranscriptRaw(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(GrainRecording{
			ID: "rec-1", TranscriptText: "Fallback transcript",
		})
	}))
	defer ts.Close()

	s := newTestScraper(t, ts, "tok")
	text, err := s.GetTranscriptRaw(context.Background(), "rec-1", "text")
	if err != nil {
		t.Fatalf("GetTranscriptRaw: %v", err)
	}
	if text != "Fallback transcript" {
		t.Errorf("transcript = %q", text)
	}
}

// ── Error Cases ─────────────────────────────────────────────────────────────

func TestAPIGet404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte("not found"))
	}))
	defer ts.Close()

	s := newTestScraper(t, ts, "tok")
	_, err := s.Me(context.Background())
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404, got: %v", err)
	}
}

func TestAPIGet500(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("internal server error"))
	}))
	defer ts.Close()

	s := newTestScraper(t, ts, "tok")
	_, err := s.Me(context.Background())
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500, got: %v", err)
	}
}

func TestAPIGetTruncatesErrorBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(strings.Repeat("x", 500)))
	}))
	defer ts.Close()

	s := newTestScraper(t, ts, "tok")
	_, err := s.Me(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if len(err.Error()) > 300 {
		t.Errorf("error too long (%d chars), body should be truncated", len(err.Error()))
	}
}

// ── SEC-1: URL Encoding ─────────────────────────────────────────────────────

func TestURLEncoding(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("cursor"); got != "abc=def&ghi" {
			t.Errorf("cursor not properly encoded/decoded: %q", got)
		}
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	s := newTestScraper(t, ts, "tok")
	var result map[string]any
	err := s.apiGet(context.Background(), "/test", map[string]string{
		"cursor": "abc=def&ghi",
	}, &result)
	if err != nil {
		t.Fatalf("apiGet: %v", err)
	}
}

func TestURLEncodingHashFragment(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("val"); got != "before#after" {
			t.Errorf("hash not encoded: %q", got)
		}
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	s := newTestScraper(t, ts, "tok")
	var result map[string]any
	err := s.apiGet(context.Background(), "/test", map[string]string{
		"val": "before#after",
	}, &result)
	if err != nil {
		t.Fatalf("apiGet: %v", err)
	}
}

// ── SEC-1: ID Validation ────────────────────────────────────────────────────

func TestGetRecordingRejectsInvalidID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called for invalid IDs")
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	s := newTestScraper(t, ts, "tok")
	badIDs := []string{
		"../me",
		"../../etc/passwd",
		"id?evil=true",
		"id&inject=1",
		"id#fragment",
		"id/nested",
		"",
		"a b c",
	}
	for _, id := range badIDs {
		_, err := s.GetRecording(context.Background(), id, "text")
		if err == nil {
			t.Errorf("GetRecording(%q) should reject invalid ID", id)
		}
		if !strings.Contains(err.Error(), "invalid recording ID") {
			t.Errorf("GetRecording(%q) error = %v, want 'invalid recording ID'", id, err)
		}
	}
}

func TestGetRecordingAcceptsValidID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(GrainRecording{ID: "good-id_123"})
	}))
	defer ts.Close()

	s := newTestScraper(t, ts, "tok")
	validIDs := []string{"abc123", "meeting-id-456", "REC_2025_01", "a"}
	for _, id := range validIDs {
		rec, err := s.GetRecording(context.Background(), id, "text")
		if err != nil {
			t.Errorf("GetRecording(%q) should accept valid ID, got: %v", id, err)
		}
		if rec == nil {
			t.Errorf("GetRecording(%q) returned nil", id)
		}
	}
}
