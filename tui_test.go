package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ── TUI Model Tests ─────────────────────────────────────────────────────────

func TestTUIModel_Init(t *testing.T) {
	m := newTUIModel()
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init should return a spinner tick command")
	}
}

func TestTUIModel_WindowSize(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)
	if m3.width != 80 || m3.height != 24 {
		t.Fatalf("got %dx%d, want 80x24", m3.width, m3.height)
	}
	if !m3.ready {
		t.Fatal("viewport should be ready after WindowSizeMsg")
	}
}

func TestTUIModel_LogMsg(t *testing.T) {
	m := newTUIModel()
	// Init viewport.
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)

	m4, _ := m3.Update(tuiLogMsg{line: "✓ Hello world"})
	m5 := m4.(tuiModel)
	if len(m5.lines) != 1 || m5.lines[0] != "✓ Hello world" {
		t.Fatalf("got lines=%v, want [✓ Hello world]", m5.lines)
	}
}

func TestTUIModel_TotalAndResults(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)

	m4, _ := m3.Update(tuiTotalMsg{n: 5})
	m5 := m4.(tuiModel)
	if m5.total != 5 {
		t.Fatalf("got total=%d, want 5", m5.total)
	}

	// Send some results.
	m6, _ := m5.Update(tuiResultMsg{status: "ok"})
	m7 := m6.(tuiModel)
	if m7.done != 1 || m7.ok != 1 {
		t.Fatalf("got done=%d ok=%d, want 1 1", m7.done, m7.ok)
	}

	m8, _ := m7.Update(tuiResultMsg{status: "skipped"})
	m9 := m8.(tuiModel)
	if m9.done != 2 || m9.skipped != 1 {
		t.Fatalf("got done=%d skipped=%d, want 2 1", m9.done, m9.skipped)
	}

	m10, _ := m9.Update(tuiResultMsg{status: "error"})
	m11 := m10.(tuiModel)
	if m11.errors != 1 {
		t.Fatalf("got errors=%d, want 1", m11.errors)
	}

	m12, _ := m11.Update(tuiResultMsg{status: "hls_pending"})
	m13 := m12.(tuiModel)
	if m13.hls != 1 || m13.ok != 2 {
		t.Fatalf("got hls=%d ok=%d, want 1 2", m13.hls, m13.ok)
	}
}

func TestTUIModel_DoneMsg(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)

	m4, _ := m3.Update(tuiDoneMsg{err: nil})
	m5 := m4.(tuiModel)
	if !m5.finished {
		t.Fatal("model should be finished after tuiDoneMsg")
	}
}

func TestTUIModel_ViewInitializing(t *testing.T) {
	m := newTUIModel()
	v := m.View()
	if !strings.Contains(v, "Initializing") {
		t.Fatalf("expected 'Initializing' in initial view, got: %q", v)
	}
}

func TestTUIModel_ViewDiscovering(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)
	v := m3.View()
	if !strings.Contains(v, "Discovering meetings") {
		t.Fatalf("expected 'Discovering meetings' in view, got: %q", v)
	}
}

func TestTUIModel_ViewExporting(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)
	m4, _ := m3.Update(tuiTotalMsg{n: 3})
	m5 := m4.(tuiModel)
	v := m5.View()
	if !strings.Contains(v, "Exporting meeting") {
		t.Fatalf("expected 'Exporting meeting' in view, got: %q", v)
	}
}

func TestTUIModel_ViewFinished(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)
	m4, _ := m3.Update(tuiDoneMsg{err: nil})
	m5 := m4.(tuiModel)
	v := m5.View()
	if !strings.Contains(v, "Export complete") {
		t.Fatalf("expected 'Export complete' in view, got: %q", v)
	}
}

func TestTUIModel_ViewStatBar(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)
	v := m3.View()
	if !strings.Contains(v, "ok:") || !strings.Contains(v, "errors:") {
		t.Fatalf("status bar should show ok:/errors: counters, got: %q", v)
	}
}

// ── TUI slog Handler Tests ─────────────────────────────────────────────────

func TestTUIHandler_Enabled(t *testing.T) {
	h := &TUIHandler{level: slog.LevelInfo}
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("Info should be enabled at Info level")
	}
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("Debug should not be enabled at Info level")
	}
}

func TestTUIHandler_WithAttrs(t *testing.T) {
	h := &TUIHandler{level: slog.LevelInfo}
	h2 := h.WithAttrs([]slog.Attr{slog.String("key", "val")})
	th := h2.(*TUIHandler)
	if len(th.attrs) != 1 || th.attrs[0].Key != "key" {
		t.Fatalf("WithAttrs should copy attrs; got %v", th.attrs)
	}
	// Original unchanged.
	if len(h.attrs) != 0 {
		t.Fatal("original handler should be unchanged")
	}
}

func TestTUIHandler_WithGroup(t *testing.T) {
	h := &TUIHandler{level: slog.LevelInfo}
	h2 := h.WithGroup("grp")
	th := h2.(*TUIHandler)
	if th.group != "grp" {
		t.Fatalf("got group=%q, want 'grp'", th.group)
	}

	h3 := th.WithGroup("sub")
	th2 := h3.(*TUIHandler)
	if th2.group != "grp.sub" {
		t.Fatalf("got group=%q, want 'grp.sub'", th2.group)
	}
}

func TestTUIHandler_Handle_NilProgram(t *testing.T) {
	// Handle with nil program should not panic.
	h := &TUIHandler{level: slog.LevelInfo}
	err := h.Handle(context.Background(), slog.Record{})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
}

func TestTUIHandler_Prefixes(t *testing.T) {
	// Verify collectAttrs with group prefix.
	h := &TUIHandler{
		level: slog.LevelInfo,
		group: "mygroup",
		attrs: []slog.Attr{slog.String("inherited", "val")},
	}
	r := slog.Record{}
	attrs := h.collectAttrs(r)
	if len(attrs) != 1 || attrs[0].Key != "mygroup.inherited" {
		t.Fatalf("expected prefixed attr 'mygroup.inherited', got %v", attrs)
	}
}
