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

// Browser wraps Rod. Only used for: login → cookie export, meeting discovery,
// and video downloads. All other HTTP goes through Scraper.
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

	page.MustEvalOnNewDocument(`() => {
		Object.defineProperty(navigator, 'webdriver', {get: () => false});
	}`)

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

	pageURL := b.page.MustInfo().URL
	if containsAny(pageURL, "login", "signin", "oauth") {
		fmt.Println("\n━━━ LOGIN REQUIRED ━━━")
		fmt.Println("Complete login in the browser window. (120s timeout)")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━")

		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("cancelled during login: %w", err)
			}
			if strings.Contains(b.page.MustInfo().URL, "/app/") {
				slog.Info("Login successful")
				break
			}
			time.Sleep(2 * time.Second)
		}
		if !strings.Contains(b.page.MustInfo().URL, "/app/") {
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
		b.page.MustEval(`() => {
			const el = document.querySelector('main, [role="main"]') || window;
			el === window ? window.scrollBy(0, 1000) : (el.scrollTop += 1000);
		}`)
		time.Sleep(1500 * time.Millisecond)
	}

	result := b.page.MustEval(`() => {
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

	var meetings []MeetingRef
	for _, item := range result.Arr() {
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
	return b.page.MustEval(`() => new Set([...document.querySelectorAll('a[href*="/app/meetings/"]')] .map(a => a.href).filter(h => /\/app\/meetings\/[a-f0-9-]+/i.test(h))).size `).Int()
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
	return b.page.MustEval(`() => { const v = document.querySelector('video'); if (v?.src?.startsWith('http')) return v.src; const s = document.querySelector('video source'); if (s?.src) return s.src; for (const sc of document.querySelectorAll('script')) { const m = (sc.textContent||'').match(/(https?:\/\/[^"'\s]+\.(mp4|webm|m3u8)[^"'\s]*)/i); if (m) return m[1]; } const el = document.querySelector('[data-video-url],[data-src]'); if (el) return el.getAttribute('data-video-url') || el.getAttribute('data-src') || ''; return ''; }`).Str()
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
	b.page.MustEval(`() => { const v = document.querySelector('video'); if(v) v.play().catch(()=>{}); }`)
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

func (b *Browser) fetchViaJS(videoURL, outputPath string) bool {
	// SEC: Use json.Marshal for correct JavaScript string escaping (not Go's %q).
	urlJSON, err := json.Marshal(videoURL)
	if err != nil {
		return false
	}
	r := b.page.MustEval(fmt.Sprintf(`async () => { try { const r = await fetch(%s); if (!r.ok) return ''; const buf = await r.arrayBuffer(); const b = new Uint8Array(buf); let s = ''; for (let i = 0; i < b.length; i++) s += String.fromCharCode(b[i]); return btoa(s); } catch { return ''; } }`, urlJSON))
	b64 := r.Str()
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
