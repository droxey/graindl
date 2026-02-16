package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const (
	grainSearchURL    = "https://grain.com/app/search?q="
	searchResultSel   = `div[role="link"]`  // broad — UUID filter is the real gate
	titleWithinSel    = `[dir="auto"]`      // used within a result element
	noResultsSel      = `text="No results"` // early exit when search has no matches
	searchTimeout     = 30 * time.Second
	resultLoadTimeout = 10 * time.Second
)

// SearchResult holds a meeting found via Grain's search UI.
type SearchResult struct {
	ID    string // extracted from the link href
	Title string
	URL   string
}

// Search navigates to Grain's search page and scrapes matching meetings.
// Returns a slice of SearchResults containing meeting IDs that can be
// fed into the export pipeline.
func (b *Browser) Search(ctx context.Context, query string) ([]SearchResult, error) {
	if query == "" {
		return nil, fmt.Errorf("search query cannot be empty")
	}

	searchURL := grainSearchURL + url.QueryEscape(query)
	slog.Info("searching grain", "query", query, "url", searchURL)

	page, err := b.newPage(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating search page: %w", err)
	}
	defer page.Close()

	// Navigate with context-aware timeout.
	if err := b.navigate(ctx, page, searchURL, searchTimeout); err != nil {
		return nil, fmt.Errorf("navigating to search: %w", err)
	}

	// Wait for results to render — or timeout if no results.
	if err := b.waitForResults(ctx, page); err != nil {
		slog.Warn("no search results found", "query", query, "err", err)
		return nil, nil // no results is not an error
	}

	// Scroll to load all results (Grain likely uses infinite scroll).
	if err := b.scrollToEnd(ctx, page); err != nil {
		slog.Warn("scroll incomplete", "err", err)
		// Continue with what we have.
	}

	return b.extractResults(ctx, page)
}

// waitForResults waits for at least one search result to appear,
// or returns early if Grain shows a "no results" message.
func (b *Browser) waitForResults(ctx context.Context, page *rod.Page) error {
	ctx, cancel := context.WithTimeout(ctx, resultLoadTimeout)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for results: %w", ctx.Err())
		case <-ticker.C:
			has, _, err := page.Has(searchResultSel)
			if err != nil {
				return fmt.Errorf("checking for results: %w", err)
			}
			if has {
				return nil
			}
			// Early exit if Grain rendered a "no results" state.
			empty, _, _ := page.Has(noResultsSel)
			if empty {
				return fmt.Errorf("grain returned no results")
			}
		}
	}
}

// scrollToEnd scrolls the page until no new results appear, handling
// infinite scroll / lazy loading.
func (b *Browser) scrollToEnd(ctx context.Context, page *rod.Page) error {
	const maxScrolls = 50

	prevCount := 0
	stableRounds := 0

	for i := range maxScrolls {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Scroll to bottom.
		_, err := page.Eval(`() => window.scrollTo(0, document.body.scrollHeight)`)
		if err != nil {
			return fmt.Errorf("scroll eval failed at iteration %d: %w", i, err)
		}

		_ = b.throttle.Wait(ctx) // single wait point — no additional sleep

		elements, err := page.Elements(searchResultSel)
		if err != nil {
			return fmt.Errorf("counting results after scroll %d: %w", i, err)
		}

		count := len(elements)
		if count == prevCount {
			stableRounds++
			if stableRounds >= 3 {
				slog.Debug("scroll complete", "total_results", count, "scrolls", i+1)
				return nil
			}
		} else {
			stableRounds = 0
			prevCount = count
		}
	}

	slog.Warn("hit max scroll limit", "max", maxScrolls, "results", prevCount)
	return nil
}

// extractResults pulls meeting IDs and titles from the rendered search page.
func (b *Browser) extractResults(ctx context.Context, page *rod.Page) ([]SearchResult, error) {
	links, err := page.Elements(searchResultSel)
	if err != nil {
		return nil, fmt.Errorf("querying result elements: %w", err)
	}

	var results []SearchResult
	seen := make(map[string]bool)

	for _, link := range links {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}

		href, err := link.Attribute("href")
		if err != nil || href == nil {
			continue
		}

		id := extractMeetingID(*href)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true

		title := ""
		titleEl, err := link.Element(titleWithinSel)
		if err == nil && titleEl != nil {
			title, _ = titleEl.Text()
		}

		results = append(results, SearchResult{
			ID:    id,
			Title: strings.TrimSpace(title),
			URL:   *href,
		})
	}

	slog.Info("search complete", "query_results", len(results))
	return results, nil
}

// extractMeetingID parses a meeting ID from a Grain URL path.
// Expected formats:
//
//	/share/recording/<uuid>/…
//	/app/recording/<uuid>/…
//	/recordings/<uuid>
func extractMeetingID(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		// "recording" catches both /app/recording/ and /share/recording/.
		// "recordings" catches /recordings/<uuid>.
		if (p == "recording" || p == "recordings") && i+1 < len(parts) {
			candidate := parts[i+1]
			if looksLikeUUID(candidate) {
				return candidate
			}
		}
	}

	// Fallback: last path segment if it looks like a UUID.
	if len(parts) > 0 && looksLikeUUID(parts[len(parts)-1]) {
		return parts[len(parts)-1]
	}
	return ""
}

// looksLikeUUID does a quick structural check — 36 chars, 4 hyphens.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// newPage creates a browser page with stealth settings applied.
func (b *Browser) newPage(ctx context.Context) (*rod.Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	page, err := b.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, err
	}

	// Suppress automation markers (ANON-1 from audit).
	_, err = page.EvalOnNewDocument(`() => {
		Object.defineProperty(navigator, 'webdriver', {get: () => false});
	}`)
	if err != nil {
		page.Close()
		return nil, fmt.Errorf("applying stealth settings: %w", err)
	}

	return page, nil
}

// navigate goes to a URL with context-aware timeout.
func (b *Browser) navigate(ctx context.Context, page *rod.Page, targetURL string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err := page.Context(ctx).Navigate(targetURL)
	if err != nil {
		return err
	}
	return page.Context(ctx).WaitLoad()
}
