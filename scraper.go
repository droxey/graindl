package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"time"
)

const (
	apiBase         = "https://api.grain.com/_/public-api"
	maxResponseSize = 50 << 20 // 50MB
	maxPages        = 100
	userAgent       = "graindl/1.0"
)

// validID matches alphanumeric IDs with hyphens and underscores.
// Rejects path traversal (../) and URL-special chars (?, &, #, /).
var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

type Scraper struct {
	client   *http.Client
	jar      *cookiejar.Jar
	token    string
	baseURL  string // defaults to apiBase; override in tests
	throttle *Throttle
}

func NewScraper(cfg *Config) (*Scraper, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookiejar: %w", err)
	}
	return &Scraper{
		client:  &http.Client{Timeout: 30 * time.Second, Jar: jar},
		jar:     jar,
		token:   cfg.Token,
		baseURL: apiBase,
		throttle: &Throttle{
			Min: time.Duration(cfg.MinDelaySec * float64(time.Second)),
			Max: time.Duration(cfg.MaxDelaySec * float64(time.Second)),
		},
	}, nil
}

func (s *Scraper) InjectCookies(cookies []*http.Cookie) {
	u, _ := url.Parse("https://grain.com")
	s.jar.SetCookies(u, cookies)
	slog.Debug("Injected cookies", "count", len(cookies))
}

// ── Raw HTTP ────────────────────────────────────────────────────────────────

// apiGetRaw returns raw bytes from an authenticated API call.
func (s *Scraper) apiGetRaw(ctx context.Context, path string, params map[string]string) ([]byte, error) {
	if s.token == "" {
		return nil, fmt.Errorf("no API token")
	}

	reqURL := s.baseURL + path
	if len(params) > 0 {
		q := make(url.Values)
		for k, v := range params {
			q.Set(k, v)
		}
		reqURL += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	if err := s.throttle.Wait(ctx); err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("404: %s", path)
	}
	if resp.StatusCode >= 400 {
		snip := string(body)
		if len(snip) > 200 {
			snip = snip[:200]
		}
		return nil, fmt.Errorf("HTTP %d: %s — %s", resp.StatusCode, path, snip)
	}
	return body, nil
}

// apiGet fetches and unmarshals into the provided typed target.
func (s *Scraper) apiGet(ctx context.Context, path string, params map[string]string, target any) error {
	body, err := s.apiGetRaw(ctx, path, params)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// ── Typed API Methods ───────────────────────────────────────────────────────

func (s *Scraper) Me(ctx context.Context) (*GrainUser, error) {
	var user GrainUser
	if err := s.apiGet(ctx, "/me", nil, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

func (s *Scraper) ListRecordings(ctx context.Context) ([]GrainRecording, error) {
	var all []GrainRecording
	cursor := ""

	for page := 1; page <= maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return all, fmt.Errorf("cancelled: %w", err)
		}

		slog.Debug("Fetching recordings page", "page", page)
		params := map[string]string{"limit": "50"}
		if cursor != "" {
			params["cursor"] = cursor
		}

		var rp RecordingsPage
		if err := s.apiGet(ctx, "/recordings", params, &rp); err != nil {
			return nil, err
		}
		if len(rp.Recordings) == 0 {
			break
		}
		all = append(all, rp.Recordings...)
		slog.Debug("Recordings fetched", "page_count", len(rp.Recordings), "total", len(all))

		cursor = rp.Cursor
		if cursor == "" {
			break
		}
	}
	return all, nil
}

func (s *Scraper) GetRecording(ctx context.Context, id, tfmt string) (*GrainRecording, error) {
	if !validID.MatchString(id) {
		return nil, fmt.Errorf("invalid recording ID: %q", id)
	}
	params := map[string]string{"intelligence_notes_format": "json"}
	if tfmt != "" {
		params["transcript_format"] = tfmt
	}
	var rec GrainRecording
	if err := s.apiGet(ctx, "/recordings/"+id, params, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *Scraper) GetTranscriptRaw(ctx context.Context, id, format string) (string, error) {
	rec, err := s.GetRecording(ctx, id, format)
	if err != nil {
		return "", err
	}
	return rec.GetTranscript(), nil
}
