package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Browser wraps Rod for all Grain interactions: login, meeting discovery,
// search filtering, and video downloads.
type Browser struct {
	browser  *rod.Browser
	page     *rod.Page
	cfg      *Config
	throttle *Throttle
}

func NewBrowser(cfg *Config, throttle *Throttle) (*Browser, error) {
	profileDir := filepath.Join(cfg.SessionDir, "chromium-profile")

	if cfg.CleanSession {
		_ = os.RemoveAll(profileDir)
		slog.Debug("Wiped browser session data")
	}

	if err := ensureDirPrivate(profileDir); err != nil {
		return nil, fmt.Errorf("session dir: %w", err)
	}

	u, err := launcher.New().
		Headless(cfg.Headless).
		UserDataDir(profileDir).
		Set("disable-blink-features", "AutomationControlled").
		Launch()
	if err != nil {
		return nil, fmt.Errorf("launch chromium: %w", err)
	}

	b := rod.New().ControlURL(u)
	if err := b.Connect(); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("page: %w", err)
	}

	if _, err := page.EvalOnNewDocument(`() => {
		Object.defineProperty(navigator, 'webdriver', {get: () => false});
	}`); err != nil {
		page.Close()
		return nil, fmt.Errorf("stealth setup: %w", err)
	}

	return &Browser{browser: b, page: page, cfg: cfg, throttle: throttle}, nil
}

func (b *Browser) Close() {
	if b.page != nil {
		b.page.Close()
	}
	if b.browser != nil {
		b.browser.Close()
	}
}

// ── Login + Cookie Export ───────────────────────────────────────────────────

func (b *Browser) Login(ctx context.Context) ([]*http.Cookie, error) {
	if err := rod.Try(func() {
		b.page.Timeout(20 * time.Second).
			MustNavigate("https://grain.com/app/meetings").
			MustWaitStable()
	}); err != nil {
		return nil, fmt.Errorf("navigate: %w", err)
	}

	info, err := b.page.Info()
	if err != nil {
		return nil, fmt.Errorf("page info: %w", err)
	}
	pageURL := info.URL
	if containsAny(pageURL, "login", "signin", "oauth") {
		fmt.Println("\n━━━ LOGIN REQUIRED ━━━")
		fmt.Println("Complete login in the browser window. (120s timeout)")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━")

		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("cancelled during login: %w", err)
			}
			info, err := b.page.Info()
			if err == nil && strings.Contains(info.URL, "/app/") {
				slog.Info("Login successful")
				break
			}
			time.Sleep(2 * time.Second)
		}
		info, err = b.page.Info()
		if err != nil || !strings.Contains(info.URL, "/app/") {
			return nil, fmt.Errorf("login timed out (120s)")
		}
	}

	return b.exportCookies()
}

func (b *Browser) exportCookies() ([]*http.Cookie, error) {
	rodCookies, err := b.browser.GetCookies()
	if err != nil {
		return nil, fmt.Errorf("get cookies: %w", err)
	}
	var cookies []*http.Cookie
	for _, c := range rodCookies {
		cookies = append(cookies, &http.Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HttpOnly: c.HTTPOnly,
		})
	}
	return cookies, nil
}

// ── Meeting Discovery ───────────────────────────────────────────────────────

func (b *Browser) DiscoverMeetings(ctx context.Context) ([]MeetingRef, error) {
	if err := rod.Try(func() {
		b.page.Timeout(20 * time.Second).
			MustNavigate("https://grain.com/app/meetings").
			MustWaitStable()
	}); err != nil {
		return nil, fmt.Errorf("navigate: %w", err)
	}
	time.Sleep(2 * time.Second)

	prevCount, stable := 0, 0
	for stable < 3 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("cancelled during scroll: %w", err)
		}
		count := b.countLinks()
		if count == prevCount {
			stable++
		} else {
			stable = 0
			prevCount = count
		}
		slog.Debug("Scrolling meeting list", "loaded", count)
		_, _ = b.page.Eval(`() => {
			const el = document.querySelector('main, [role="main"]') || window;
			el === window ? window.scrollBy(0, 1000) : (el.scrollTop += 1000);
		}`)
		time.Sleep(1500 * time.Millisecond)
	}

	result, err := b.page.Eval(`() => {
		const seen = new Set(), out = [];
		document.querySelectorAll('a[href*="/app/meetings/"]').forEach(a => {
			const m = a.href.match(/\/app\/meetings\/([a-f0-9-]+)/i);
			if (m && !seen.has(m[1])) {
				seen.add(m[1]);
				out.push({id: m[1], title: a.textContent?.trim() || '', url: a.href});
			}
		});
		return out;
	}`)
	if err != nil {
		return nil, fmt.Errorf("extract meeting links: %w", err)
	}

	var meetings []MeetingRef
	for _, item := range result.Value.Arr() {
		m := item.Map()
		meetings = append(meetings, MeetingRef{
			ID:    m["id"].Str(),
			Title: m["title"].Str(),
			URL:   m["url"].Str(),
		})
	}
	return meetings, nil
}

func (b *Browser) countLinks() int {
	result, err := b.page.Eval(`() => {
		const links = document.querySelectorAll('a[href*="/app/meetings/"]');
		const unique = new Set(
			[...links].map(a => a.href).filter(h => /\/app\/meetings\/[a-f0-9-]+/i.test(h))
		);
		return unique.size;
	}`)
	if err != nil {
		return 0
	}
	return result.Value.Int()
}

// ── Video Source Discovery ──────────────────────────────────────────────────

// FindVideoSource navigates to a meeting page and tries to locate a video URL
// without downloading the file. Used by --audio-only to let ffmpeg stream
// audio directly from the source, saving bandwidth.
func (b *Browser) FindVideoSource(ctx context.Context, pageURL string) string {
	if err := rod.Try(func() {
		b.page.Timeout(20 * time.Second).MustNavigate(pageURL).MustWaitStable()
	}); err != nil {
		return ""
	}
	time.Sleep(2 * time.Second)

	if u := b.extractVideoURL(); u != "" {
		return u
	}
	if u := b.interceptNetwork(pageURL); u != "" {
		return u
	}
	return ""
}

// ── Video Download ──────────────────────────────────────────────────────────

func (b *Browser) DownloadVideo(ctx context.Context, pageURL, outputPath string) (method, result string) {
	if err := rod.Try(func() {
		b.page.Timeout(20 * time.Second).MustNavigate(pageURL).MustWaitStable()
	}); err != nil {
		return "failed", ""
	}
	time.Sleep(2 * time.Second)

	if p := b.tryDownloadBtn(ctx, outputPath); p != "" {
		return "button", p
	}
	if u := b.extractVideoURL(); u != "" {
		return b.resolveURL(u, outputPath)
	}
	if u := b.interceptNetwork(pageURL); u != "" {
		return b.resolveURL(u, outputPath)
	}
	return "failed", ""
}

var menuSels = []string{
	`[data-testid="more-menu"]`,
	`[aria-label="More"]`,
	`[aria-label="More options"]`,
}

func (b *Browser) tryDownloadBtn(ctx context.Context, outputPath string) string {
	for _, sel := range menuSels {
		el, err := b.page.Timeout(2 * time.Second).Element(sel)
		if err != nil {
			continue
		}
		if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
			continue
		}
		time.Sleep(500 * time.Millisecond)

		dlEl, err := b.page.Timeout(2 * time.Second).ElementR("button, a, div, span", "Download")
		if err != nil {
			b.pressEscape()
			continue
		}

		wait := b.browser.MustWaitDownload()
		if err := dlEl.Click(proto.InputMouseButtonLeft, 1); err != nil {
			b.pressEscape()
			continue
		}

		// NOTE: Rod's MustWaitDownload has no cancellation support.
		// A stalled download leaks one goroutine — this is a known Rod limitation.
		ch := make(chan []byte, 1)
		go func() { ch <- wait() }()

		select {
		case data := <-ch:
			if len(data) > 1000 {
				_ = ensureDir(filepath.Dir(outputPath))
				if writeFile(outputPath, data) == nil {
					return outputPath
				}
			}
		case <-ctx.Done():
			slog.Warn("Download cancelled by signal")
			b.pressEscape()
			return ""
		case <-time.After(5 * time.Minute):
			slog.Warn("Download timed out (5m) — goroutine leaked (Rod limitation)")
		}
		b.pressEscape()
	}
	return ""
}

func (b *Browser) extractVideoURL() string {
	result, err := b.page.Eval(`() => {
		// 1. <video src="...">
		const v = document.querySelector('video');
		if (v?.src?.startsWith('http')) return v.src;

		// 2. <video><source src="..."></video>
		const s = document.querySelector('video source');
		if (s?.src) return s.src;

		// 3. Inline script containing a video URL.
		for (const sc of document.querySelectorAll('script')) {
			const m = (sc.textContent || '').match(
				/(https?:\/\/[^"'\s]+\.(mp4|webm|m3u8)[^"'\s]*)/i
			);
			if (m) return m[1];
		}

		// 4. Data attributes.
		const el = document.querySelector('[data-video-url],[data-src]');
		if (el) return el.getAttribute('data-video-url') || el.getAttribute('data-src') || '';

		return '';
	}`)
	if err != nil {
		return ""
	}
	return result.Value.Str()
}

var videoRe = regexp.MustCompile(`\.(mp4|webm|m3u8)`)

func (b *Browser) interceptNetwork(pageURL string) string {
	var found atomic.Value

	router := b.page.HijackRequests()
	router.MustAdd("*", func(ctx *rod.Hijack) {
		u := ctx.Request.URL().String()
		if videoRe.MatchString(u) {
			found.Store(u)
		}
		ctx.ContinueRequest(&proto.FetchContinueRequest{})
	})
	go router.Run()
	defer router.Stop()

	rod.Try(func() {
		b.page.Timeout(20 * time.Second).MustNavigate(pageURL).MustWaitStable()
	})
	time.Sleep(2 * time.Second)
	// Trigger video playback to provoke network requests.
	_, _ = b.page.Eval(`() => {
		const v = document.querySelector('video');
		if (v) v.play().catch(() => {});
	}`)
	time.Sleep(4 * time.Second)

	if v := found.Load(); v != nil {
		return v.(string)
	}
	return ""
}

func (b *Browser) resolveURL(videoURL, outputPath string) (string, string) {
	if strings.Contains(videoURL, ".m3u8") {
		p := strings.TrimSuffix(outputPath, ".mp4") + ".m3u8.url"
		_ = writeFile(p, []byte(videoURL))
		return "hls", p
	}
	if b.fetchViaJS(videoURL, outputPath) {
		return "direct", outputPath
	}
	p := strings.TrimSuffix(outputPath, ".mp4") + ".video-url.txt"
	_ = writeFile(p, []byte(videoURL))
	return "url-saved", p
}

// maxFetchViaJSBytes is the maximum video size fetchViaJS will attempt.
// Larger files should be downloaded via Go's http.Client or Rod's download API
// to avoid exhausting the browser's JS heap.
const maxFetchViaJSBytes = 50 * 1024 * 1024 // 50 MB

func (b *Browser) fetchViaJS(videoURL, outputPath string) bool {
	// SEC: Use json.Marshal for correct JavaScript string escaping (not Go's %q).
	urlJSON, err := json.Marshal(videoURL)
	if err != nil {
		return false
	}
	result, err := b.page.Eval(fmt.Sprintf(`async () => {
		try {
			const r = await fetch(%s);
			if (!r.ok) return '';
			// Bail out if the response is too large for in-browser download.
			const cl = parseInt(r.headers.get('content-length') || '0', 10);
			if (cl > %d) return 'TOO_LARGE';
			const buf = await r.arrayBuffer();
			if (buf.byteLength > %d) return 'TOO_LARGE';
			const b = new Uint8Array(buf);
			let s = '';
			for (let i = 0; i < b.length; i++) s += String.fromCharCode(b[i]);
			return btoa(s);
		} catch { return ''; }
	}`, urlJSON, maxFetchViaJSBytes, maxFetchViaJSBytes))
	if err != nil {
		return false
	}
	b64 := result.Value.Str()
	if b64 == "TOO_LARGE" {
		slog.Warn("Video too large for in-browser fetch, skipping", "url", videoURL)
		return false
	}
	if len(b64) < 100 {
		return false
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) < 1000 {
		return false
	}
	return writeFile(outputPath, data) == nil
}

func (b *Browser) pressEscape() {
	rod.Try(func() {
		b.page.KeyActions().Press(input.Escape).MustDo()
	})
}

// ── Meeting Page Scraping ───────────────────────────────────────────────────

// MeetingPageData holds content scraped from a Grain meeting page.
type MeetingPageData struct {
	Title        string
	Date         string
	Duration     string
	Participants []string
	Transcript   string
	Highlights   []Highlight
}

// ScrapeMeetingPage navigates to a meeting page and extracts transcript text,
// highlights, and any additional metadata visible on the page.
func (b *Browser) ScrapeMeetingPage(ctx context.Context, pageURL string) (*MeetingPageData, error) {
	if err := rod.Try(func() {
		b.page.Timeout(20 * time.Second).MustNavigate(pageURL).MustWaitStable()
	}); err != nil {
		return nil, fmt.Errorf("navigate to meeting: %w", err)
	}
	time.Sleep(2 * time.Second)

	data := &MeetingPageData{}

	// Extract page metadata (title, date, duration, participants).
	data.Title = b.scrapeText(`h1, [data-testid="meeting-title"], .meeting-title`)
	data.Date = b.scrapeAttribute(`time[datetime]`, "datetime")
	if data.Date == "" {
		data.Date = b.scrapeText(`time, [data-testid="meeting-date"]`)
	}
	data.Duration = b.scrapeText(`[data-testid="meeting-duration"], .duration`)
	data.Participants = b.scrapeParticipants()

	// Click transcript tab/section if present.
	b.clickElement(`[data-testid="transcript-tab"], button:has-text("Transcript"), [role="tab"]:has-text("Transcript")`)
	time.Sleep(1 * time.Second)

	data.Transcript = b.scrapeTranscript()
	data.Highlights = b.scrapeHighlights(ctx)

	return data, nil
}

// scrapeText returns the trimmed text content of the first matching element.
func (b *Browser) scrapeText(selectors string) string {
	for _, sel := range strings.Split(selectors, ",") {
		sel = strings.TrimSpace(sel)
		el, err := b.page.Timeout(2 * time.Second).Element(sel)
		if err != nil || el == nil {
			continue
		}
		text, err := el.Text()
		if err != nil {
			continue
		}
		if t := strings.TrimSpace(text); t != "" {
			return t
		}
	}
	return ""
}

// scrapeAttribute returns an attribute value from the first matching element.
func (b *Browser) scrapeAttribute(sel, attr string) string {
	el, err := b.page.Timeout(2 * time.Second).Element(sel)
	if err != nil || el == nil {
		return ""
	}
	val, err := el.Attribute(attr)
	if err != nil || val == nil {
		return ""
	}
	return strings.TrimSpace(*val)
}

// scrapeParticipants extracts participant names from the meeting page.
func (b *Browser) scrapeParticipants() []string {
	result, err := b.page.Eval(`() => {
		const names = new Set();
		// Try participant list elements.
		document.querySelectorAll('[data-testid="participant"], .participant-name, .attendee-name').forEach(el => {
			const t = (el.textContent || '').trim();
			if (t) names.add(t);
		});
		// Try avatar tooltips / aria-labels.
		document.querySelectorAll('[aria-label*="participant"], [title]').forEach(el => {
			const label = el.getAttribute('aria-label') || el.getAttribute('title') || '';
			if (label && !label.includes('button') && !label.includes('menu') && label.length < 60) {
				// skip generic UI labels
			}
		});
		return Array.from(names);
	}`)
	if err != nil {
		return nil
	}
	var participants []string
	for _, item := range result.Value.Arr() {
		if s := item.Str(); s != "" {
			participants = append(participants, s)
		}
	}
	return participants
}

// scrapeTranscript extracts transcript text from the meeting page.
// Grain typically renders transcript segments as individual elements.
func (b *Browser) scrapeTranscript() string {
	// Try structured transcript segments first.
	result, err := b.page.Eval(`() => {
		const segments = [];

		// Common transcript container selectors.
		const containers = document.querySelectorAll(
			'[data-testid="transcript-segment"], ' +
			'.transcript-segment, ' +
			'[class*="transcript"] [class*="segment"], ' +
			'[class*="transcript"] [class*="block"], ' +
			'[class*="Transcript"] [class*="Segment"]'
		);

		if (containers.length > 0) {
			containers.forEach(seg => {
				const speaker = (seg.querySelector('[class*="speaker"], [class*="Speaker"], [data-testid="speaker-name"]') || {}).textContent || '';
				const text = (seg.querySelector('[class*="text"], [class*="Text"], [class*="content"], p') || seg).textContent || '';
				const clean = text.replace(speaker, '').trim();
				if (clean) {
					segments.push(speaker.trim() ? (speaker.trim() + ': ' + clean) : clean);
				}
			});
			return segments.join('\n\n');
		}

		// Fallback: look for a transcript container and get all its text.
		const wrapper = document.querySelector(
			'[data-testid="transcript"], ' +
			'[class*="transcript-content"], ' +
			'[class*="TranscriptContent"], ' +
			'[role="article"][class*="transcript"]'
		);
		if (wrapper) {
			return wrapper.innerText.trim();
		}

		// Last resort: any large text block that looks like a transcript.
		const main = document.querySelector('main, [role="main"]');
		if (main) {
			const allText = main.innerText || '';
			// Only return if it's substantial (looks like actual transcript).
			if (allText.length > 200) {
				return allText.trim();
			}
		}

		return '';
	}`)
	if err != nil {
		return ""
	}
	return result.Value.Str()
}

// scrapeHighlights extracts highlights/clips from the meeting page.
func (b *Browser) scrapeHighlights(ctx context.Context) []Highlight {
	// Try clicking the highlights tab.
	b.clickElement(`[data-testid="highlights-tab"], button:has-text("Highlights"), [role="tab"]:has-text("Highlights"), button:has-text("Clips")`)
	time.Sleep(1 * time.Second)

	result, err := b.page.Eval(`() => {
		const highlights = [];
		const elements = document.querySelectorAll(
			'[data-testid="highlight"], ' +
			'[data-testid="clip"], ' +
			'[class*="highlight-card"], ' +
			'[class*="HighlightCard"], ' +
			'[class*="clip-card"], ' +
			'[class*="ClipCard"]'
		);

		elements.forEach((el, i) => {
			const text = (el.querySelector('[class*="text"], [class*="content"], [class*="quote"], p') || {}).textContent || '';
			const speaker = (el.querySelector('[class*="speaker"], [class*="Speaker"]') || {}).textContent || '';
			const title = (el.querySelector('[class*="title"], h3, h4') || {}).textContent || '';
			const timeEl = el.querySelector('[class*="time"], [class*="timestamp"], time');
			const timeText = timeEl ? (timeEl.getAttribute('datetime') || timeEl.textContent || '') : '';
			const link = el.querySelector('a[href*="highlight"], a[href*="clip"]');
			const url = link ? link.href : '';

			if (text.trim() || title.trim()) {
				highlights.push({
					id: 'highlight-' + i,
					text: text.trim(),
					title: title.trim(),
					speaker: speaker.trim(),
					timestamp: timeText.trim(),
					url: url
				});
			}
		});

		return JSON.stringify(highlights);
	}`)
	if err != nil {
		return nil
	}

	raw := result.Value.Str()
	if raw == "" || raw == "[]" {
		return nil
	}

	var highlights []Highlight
	if err := json.Unmarshal([]byte(raw), &highlights); err != nil {
		slog.Debug("Failed to parse scraped highlights", "error", err)
		return nil
	}

	// Filter out empty highlights.
	filtered := highlights[:0]
	for _, h := range highlights {
		if h.Text != "" || h.Title != "" || h.Content != "" {
			filtered = append(filtered, h)
		}
	}
	return filtered
}

// clickElement tries to click the first element matching any of the selectors.
func (b *Browser) clickElement(selectors string) {
	for _, sel := range strings.Split(selectors, ",") {
		sel = strings.TrimSpace(sel)
		el, err := b.page.Timeout(2 * time.Second).Element(sel)
		if err != nil || el == nil {
			continue
		}
		if err := el.Click(proto.InputMouseButtonLeft, 1); err == nil {
			return
		}
	}
}
