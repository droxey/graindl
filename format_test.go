package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── flattenStringSlice ──────────────────────────────────────────────────────

func TestFlattenStringSlice(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want []string
	}{
		{"nil", nil, nil},
		{"string", "hello", []string{"hello"}},
		{"empty string", "", nil},
		{"[]string", []string{"a", "b"}, []string{"a", "b"}},
		{"[]any strings", []any{"x", "y"}, []string{"x", "y"}},
		{"[]any maps with name", []any{
			map[string]any{"name": "Alice", "email": "a@b.com"},
			map[string]any{"name": "Bob"},
		}, []string{"Alice", "Bob"}},
		{"[]any maps with email fallback", []any{
			map[string]any{"email": "a@b.com"},
		}, []string{"a@b.com"}},
		{"[]any maps with label", []any{
			map[string]any{"label": "tag1"},
		}, []string{"tag1"}},
		{"unsupported type", 42, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := flattenStringSlice(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("flattenStringSlice = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ── formatDuration ──────────────────────────────────────────────────────────

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"zero float", float64(0), ""},
		{"seconds only", float64(45), "45s"},
		{"minutes", float64(125), "2m05s"},
		{"hours", float64(3661), "1h01m01s"},
		{"int", int(90), "1m30s"},
		{"int64", int64(7200), "2h00m00s"},
		{"negative", float64(-1), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.in)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ── formatAny ───────────────────────────────────────────────────────────────

func TestFormatAny(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"string", "some notes", "some notes"},
		{"string with whitespace", "  trimmed  ", "trimmed"},
		{"[]any strings", []any{"Note 1", "Note 2"}, "- Note 1\n- Note 2"},
		{"[]any maps text", []any{
			map[string]any{"text": "First"},
			map[string]any{"content": "Second"},
		}, "- First\n- Second"},
		{"map text", map[string]any{"text": "hello"}, "hello"},
		{"map content", map[string]any{"content": "world"}, "world"},
		{"map fallback sorted", map[string]any{"zebra": "z", "alpha": "a"}, "**alpha:** a\n**zebra:** z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatAny(tt.in)
			if got != tt.want {
				t.Errorf("formatAny = %q, want %q", got, tt.want)
			}
		})
	}
}

// ── YAML helpers ────────────────────────────────────────────────────────────

func TestNeedsYAMLQuoting(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"simple", false},
		{"has:colon", true},
		{"has#hash", true},
		{"true", true},
		{"false", true},
		{"null", true},
		{" leading-space", true},
		{"trailing-space ", true},
		{"normal-value-123", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := needsYAMLQuoting(tt.in); got != tt.want {
				t.Errorf("needsYAMLQuoting(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestEscapeYAMLString(t *testing.T) {
	if got := escapeYAMLString(`say "hello"`); got != `say \"hello\"` {
		t.Errorf("got %q", got)
	}
	if got := escapeYAMLString(`path\to`); got != `path\\to` {
		t.Errorf("got %q", got)
	}
	if got := escapeYAMLString("line1\nline2"); got != `line1\nline2` {
		t.Errorf("newline escape: got %q", got)
	}
	if got := escapeYAMLString("col1\tcol2"); got != `col1\tcol2` {
		t.Errorf("tab escape: got %q", got)
	}
}

func TestNeedsYAMLQuotingNewline(t *testing.T) {
	if !needsYAMLQuoting("has\nnewline") {
		t.Error("newline should require quoting")
	}
	if !needsYAMLQuoting("has\ttab") {
		t.Error("tab should require quoting")
	}
}

// ── renderFormattedMarkdown ─────────────────────────────────────────────────

func TestRenderObsidianBasic(t *testing.T) {
	meta := &Metadata{
		ID:    "meeting-123",
		Title: "Sprint Review",
		Date:  "2025-06-01T10:00:00Z",
		Links: Links{
			Grain: "https://grain.com/app/meetings/meeting-123",
			Share: "https://share.grain.com/meeting-123",
		},
		DurationSeconds: float64(3600),
		Participants:    []any{"Alice", "Bob"},
		Tags:            []any{"sprint", "review"},
		AINotes:         "Key decisions were made.",
		Highlights:      []any{"Decision on Q3 roadmap"},
	}

	md := renderFormattedMarkdown("obsidian", meta, "Hello world transcript")

	// Frontmatter
	if !strings.HasPrefix(md, "---\n") {
		t.Error("should start with YAML frontmatter delimiter")
	}
	if !strings.Contains(md, "title: Sprint Review\n") {
		t.Error("missing title in frontmatter")
	}
	if !strings.Contains(md, "date: 2025-06-01") {
		t.Error("missing date in frontmatter")
	}
	if !strings.Contains(md, "grain_id: meeting-123") {
		t.Error("missing grain_id")
	}
	if !strings.Contains(md, "  - grain\n") {
		t.Error("missing default 'grain' tag")
	}
	if !strings.Contains(md, "  - sprint\n") {
		t.Error("missing 'sprint' tag")
	}
	if !strings.Contains(md, "  - Alice\n") {
		t.Error("missing participant Alice")
	}
	if !strings.Contains(md, "duration: 1h00m00s") {
		t.Error("missing duration")
	}
	if !strings.Contains(md, "aliases:\n  - Sprint Review\n") {
		t.Error("missing aliases")
	}

	// Body sections
	if !strings.Contains(md, "# Sprint Review\n") {
		t.Error("missing title heading")
	}
	if !strings.Contains(md, "## AI Notes\n") {
		t.Error("missing AI Notes section")
	}
	if !strings.Contains(md, "Key decisions were made.") {
		t.Error("missing notes content")
	}
	if !strings.Contains(md, "## Highlights\n") {
		t.Error("missing Highlights section")
	}
	if !strings.Contains(md, "## Transcript\n") {
		t.Error("missing Transcript section")
	}
	if !strings.Contains(md, "Hello world transcript") {
		t.Error("missing transcript content")
	}
}

func TestRenderNotionBasic(t *testing.T) {
	meta := &Metadata{
		ID:    "meeting-456",
		Title: "Weekly Standup",
		Date:  "2025-07-15T09:00:00Z",
		Links: Links{
			Grain: "https://grain.com/app/meetings/meeting-456",
			Video: "https://cdn.grain.com/v.mp4",
		},
		DurationSeconds: float64(1800),
		Participants:    []any{"Carol", "Dave"},
	}

	md := renderFormattedMarkdown("notion", meta, "Standup transcript")

	// Frontmatter
	if !strings.HasPrefix(md, "---\n") {
		t.Error("should start with YAML frontmatter delimiter")
	}
	if !strings.Contains(md, "type: Meeting") {
		t.Error("missing type field")
	}
	if !strings.Contains(md, "status: Exported") {
		t.Error("missing status field")
	}
	if !strings.Contains(md, "date: 2025-07-15") {
		t.Error("missing date")
	}

	// Summary callout
	if !strings.Contains(md, "> **Date:** 2025-07-15") {
		t.Error("missing summary callout date")
	}
	if !strings.Contains(md, "**Duration:** 30m00s") {
		t.Error("missing summary callout duration")
	}
	if !strings.Contains(md, "**Participants:** Carol, Dave") {
		t.Error("missing summary callout participants")
	}

	// Links
	if !strings.Contains(md, "[Grain](https://grain.com/app/meetings/meeting-456)") {
		t.Error("missing Grain link")
	}
	if !strings.Contains(md, "[Video](https://cdn.grain.com/v.mp4)") {
		t.Error("missing Video link")
	}

	// Transcript
	if !strings.Contains(md, "## Transcript\n") {
		t.Error("missing Transcript section")
	}
}

func TestRenderMinimalMetadata(t *testing.T) {
	meta := minimalMetadata("id-1", "Minimal", "https://grain.com/app/meetings/id-1")

	// Should not panic, should produce valid output.
	obsidian := renderFormattedMarkdown("obsidian", meta, "")
	if !strings.Contains(obsidian, "title: Minimal") {
		t.Error("obsidian: missing title")
	}
	if strings.Contains(obsidian, "## Transcript") {
		t.Error("obsidian: should not have transcript section when empty")
	}

	notion := renderFormattedMarkdown("notion", meta, "")
	if !strings.Contains(notion, "title: Minimal") {
		t.Error("notion: missing title")
	}
}

func TestRenderObsidianEmptyTitle(t *testing.T) {
	meta := &Metadata{
		ID:    "no-title",
		Title: "",
		Links: Links{Grain: "https://grain.com/app/meetings/no-title"},
	}
	md := renderFormattedMarkdown("obsidian", meta, "")

	// Should not contain an aliases field when title is empty.
	if strings.Contains(md, "aliases:") {
		t.Error("should not write aliases when title is empty")
	}
	// Heading should fall back to ID.
	if !strings.Contains(md, "# no-title\n") {
		t.Error("heading should fall back to ID when title is empty")
	}
}

func TestRenderUnknownFormat(t *testing.T) {
	meta := &Metadata{ID: "x", Title: "X"}
	if got := renderFormattedMarkdown("unknown", meta, "text"); got != "" {
		t.Errorf("unknown format should return empty, got %q", got)
	}
	if got := renderFormattedMarkdown("", meta, "text"); got != "" {
		t.Errorf("empty format should return empty, got %q", got)
	}
}

func TestRenderObsidianSpecialCharsInTitle(t *testing.T) {
	meta := &Metadata{
		ID:    "special",
		Title: `Meeting: "Q3 Planning" & Review`,
		Links: Links{Grain: "https://grain.com/app/meetings/special"},
	}

	md := renderFormattedMarkdown("obsidian", meta, "")

	// Title should be quoted in YAML due to special chars.
	if !strings.Contains(md, `title: "Meeting`) {
		t.Error("title with special chars should be quoted")
	}
}

// ── Integration: exportOne with --output-format ─────────────────────────────

func TestExportOneObsidianFormat(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:    dir,
		SkipVideo:    true,
		OutputFormat: "obsidian",
		MinDelaySec:  0,
		MaxDelaySec:  0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}

	ref := MeetingRef{
		ID:    "fmt-id",
		Title: "Formatted Meeting",
		Date:  "2025-06-01T10:00:00Z",
		URL:   "https://grain.com/app/meetings/fmt-id",
	}

	r := e.exportOne(context.Background(), ref)

	if r.Status != "ok" {
		t.Errorf("status = %q, want ok (error: %s)", r.Status, r.ErrorMsg)
	}

	// Markdown file should exist.
	if r.MarkdownPath == "" {
		t.Fatal("MarkdownPath should be set")
	}
	mdPath := filepath.Join(dir, r.MarkdownPath)
	if !fileExists(mdPath) {
		t.Fatalf("markdown file missing: %s", mdPath)
	}

	// Verify it ends with .md.
	if !strings.HasSuffix(r.MarkdownPath, ".md") {
		t.Errorf("MarkdownPath should end with .md, got %q", r.MarkdownPath)
	}

	// Verify content is Obsidian-formatted.
	data, _ := os.ReadFile(mdPath)
	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		t.Error("should start with YAML frontmatter")
	}
	if !strings.Contains(content, "aliases:") {
		t.Error("Obsidian format should have aliases")
	}

	// SEC-11: file should be 0o600.
	info, _ := os.Stat(mdPath)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("markdown perms = %04o, want 0600", info.Mode().Perm())
	}

	// Path should be relative.
	if filepath.IsAbs(r.MarkdownPath) {
		t.Errorf("MarkdownPath should be relative, got %q", r.MarkdownPath)
	}
}

func TestExportOneNotionFormat(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:    dir,
		SkipVideo:    true,
		OutputFormat: "notion",
		MinDelaySec:  0,
		MaxDelaySec:  0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}

	ref := MeetingRef{
		ID:    "notion-id",
		Title: "Notion Meeting",
		Date:  "2025-08-01T10:00:00Z",
		URL:   "https://grain.com/app/meetings/notion-id",
	}

	r := e.exportOne(context.Background(), ref)

	if r.Status != "ok" {
		t.Errorf("status = %q, want ok", r.Status)
	}
	if r.MarkdownPath == "" {
		t.Fatal("MarkdownPath should be set")
	}

	data, _ := os.ReadFile(filepath.Join(dir, r.MarkdownPath))
	content := string(data)

	if !strings.Contains(content, "type: Meeting") {
		t.Error("Notion format should have type field")
	}
	if !strings.Contains(content, "status: Exported") {
		t.Error("Notion format should have status field")
	}
}

func TestExportOneNoFormatNoMarkdown(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:    dir,
		SkipVideo:    true,
		OutputFormat: "", // no format
		MinDelaySec:  0,
		MaxDelaySec:  0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}

	ref := MeetingRef{ID: "no-fmt", Title: "No Format", Date: "2025-01-01"}

	r := e.exportOne(context.Background(), ref)

	if r.MarkdownPath != "" {
		t.Errorf("MarkdownPath should be empty when no format, got %q", r.MarkdownPath)
	}

	// No .md file should exist.
	matches, _ := filepath.Glob(filepath.Join(dir, "**", "*.md"))
	if len(matches) > 0 {
		t.Errorf("no .md files should exist without --output-format, got %v", matches)
	}
}

func TestRunSingleMeetingWithFormat(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		OutputDir:    dir,
		MeetingID:    "single-fmt",
		SkipVideo:    true,
		OutputFormat: "obsidian",
		MinDelaySec:  0,
		MaxDelaySec:  0.01,
	}
	e, err := NewExporter(cfg)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer e.Close()

	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Check manifest includes markdown_path.
	raw, _ := os.ReadFile(filepath.Join(dir, "_export-manifest.json"))
	var m ExportManifest
	json.Unmarshal(raw, &m)
	if len(m.Meetings) != 1 {
		t.Fatalf("expected 1 meeting, got %d", len(m.Meetings))
	}
	if m.Meetings[0].MarkdownPath == "" {
		t.Error("manifest should include markdown_path")
	}
	if !strings.HasSuffix(m.Meetings[0].MarkdownPath, ".md") {
		t.Errorf("markdown_path should end with .md, got %q", m.Meetings[0].MarkdownPath)
	}

	// Verify the file exists and has obsidian frontmatter.
	mdPath := filepath.Join(dir, m.Meetings[0].MarkdownPath)
	data, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read markdown: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "tags:") {
		t.Error("should have Obsidian tags field")
	}
}

// ── writeFormattedMarkdown with transcript content ──────────────────────────

func TestWriteFormattedMarkdownWithTranscript(t *testing.T) {
	dir := t.TempDir()
	e := &Exporter{cfg: &Config{OutputDir: dir, OutputFormat: "obsidian"}}
	r := &ExportResult{TranscriptPaths: make(map[string]string)}
	base := filepath.Join(dir, "tx-test")

	meta := &Metadata{
		ID:    "tx-test",
		Title: "Transcript Meeting",
		Date:  "2025-06-01",
		Links: Links{Grain: "https://grain.com/app/meetings/tx-test"},
	}

	e.writeFormattedMarkdown(meta, "Hello world transcript text", base, r)

	if r.MarkdownPath == "" {
		t.Fatal("MarkdownPath should be set")
	}

	mdPath := filepath.Join(dir, r.MarkdownPath)
	data, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read markdown: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "## Transcript") {
		t.Error("should have Transcript section")
	}
	if !strings.Contains(content, "Hello world transcript text") {
		t.Error("should include transcript content")
	}
}

func TestWriteFormattedMarkdownEmptyTranscript(t *testing.T) {
	dir := t.TempDir()
	e := &Exporter{cfg: &Config{OutputDir: dir, OutputFormat: "notion"}}
	r := &ExportResult{TranscriptPaths: make(map[string]string)}
	base := filepath.Join(dir, "no-tx")

	meta := &Metadata{
		ID:    "no-tx",
		Title: "No Transcript",
		Links: Links{Grain: "https://grain.com/app/meetings/no-tx"},
	}

	e.writeFormattedMarkdown(meta, "", base, r)

	if r.MarkdownPath == "" {
		t.Fatal("MarkdownPath should be set")
	}

	data, _ := os.ReadFile(filepath.Join(dir, r.MarkdownPath))
	content := string(data)

	if strings.Contains(content, "## Transcript") {
		t.Error("should NOT have Transcript section when transcript is empty")
	}
}
