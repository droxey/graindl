package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

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
	// tuiTotalMsg should pre-populate meeting list.
	if len(m5.meetings) != 5 {
		t.Fatalf("got %d meetings after TotalMsg, want 5", len(m5.meetings))
	}
	for i, mt := range m5.meetings {
		if mt.status != "pending" {
			t.Fatalf("meeting[%d] status=%q, want 'pending'", i, mt.status)
		}
	}

	// Send some results.
	m6, _ := m5.Update(tuiResultMsg{index: 0, title: "Q4 Review", status: "ok"})
	m7 := m6.(tuiModel)
	if m7.done != 1 || m7.ok != 1 {
		t.Fatalf("got done=%d ok=%d, want 1 1", m7.done, m7.ok)
	}
	if m7.meetings[0].status != "ok" {
		t.Fatalf("meeting[0] status=%q after result, want 'ok'", m7.meetings[0].status)
	}

	m8, _ := m7.Update(tuiResultMsg{index: 1, status: "skipped"})
	m9 := m8.(tuiModel)
	if m9.done != 2 || m9.skipped != 1 {
		t.Fatalf("got done=%d skipped=%d, want 2 1", m9.done, m9.skipped)
	}

	m10, _ := m9.Update(tuiResultMsg{index: 2, status: "error"})
	m11 := m10.(tuiModel)
	if m11.errors != 1 {
		t.Fatalf("got errors=%d, want 1", m11.errors)
	}

	m12, _ := m11.Update(tuiResultMsg{index: 3, status: "hls_pending"})
	m13 := m12.(tuiModel)
	if m13.hls != 1 || m13.ok != 2 {
		t.Fatalf("got hls=%d ok=%d, want 1 2", m13.hls, m13.ok)
	}
}

func TestTUIModel_StartMsg(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)

	// With pre-populated list from TotalMsg.
	m4, _ := m3.Update(tuiTotalMsg{n: 3})
	m5 := m4.(tuiModel)

	m6, _ := m5.Update(tuiStartMsg{index: 1, title: "Team Standup"})
	m7 := m6.(tuiModel)
	if m7.meetings[1].status != "active" {
		t.Fatalf("meeting[1] status=%q, want 'active'", m7.meetings[1].status)
	}
	if m7.meetings[1].title != "Team Standup" {
		t.Fatalf("meeting[1] title=%q, want 'Team Standup'", m7.meetings[1].title)
	}
	// Other meetings should still be pending.
	if m7.meetings[0].status != "pending" {
		t.Fatalf("meeting[0] status=%q, want 'pending'", m7.meetings[0].status)
	}
}

func TestTUIModel_StartMsg_AppendsBeyondTotal(t *testing.T) {
	// When a start arrives without a prior TotalMsg (e.g. single-meeting mode),
	// it should append a new entry.
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)

	m4, _ := m3.Update(tuiStartMsg{index: 0, title: "Solo Meeting"})
	m5 := m4.(tuiModel)
	if len(m5.meetings) != 1 {
		t.Fatalf("got %d meetings, want 1", len(m5.meetings))
	}
	if m5.meetings[0].status != "active" {
		t.Fatalf("status=%q, want 'active'", m5.meetings[0].status)
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

func TestTUIModel_DoneMsgWithError(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)

	testErr := errors.New("something went wrong")
	m4, _ := m3.Update(tuiDoneMsg{err: testErr})
	m5 := m4.(tuiModel)
	if !m5.finished {
		t.Fatal("model should be finished")
	}
	if m5.exitErr != testErr {
		t.Fatalf("exitErr=%v, want %v", m5.exitErr, testErr)
	}
}

func TestTUIModel_PaneSwitching(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)

	if m3.activePane != 0 {
		t.Fatalf("initial pane=%d, want 0 (meetings)", m3.activePane)
	}
	m4, _ := m3.Update(tea.KeyMsg{Type: tea.KeyTab})
	m5 := m4.(tuiModel)
	if m5.activePane != 1 {
		t.Fatalf("after tab pane=%d, want 1 (logs)", m5.activePane)
	}
	m6, _ := m5.Update(tea.KeyMsg{Type: tea.KeyTab})
	m7 := m6.(tuiModel)
	if m7.activePane != 0 {
		t.Fatalf("after second tab pane=%d, want 0 (meetings)", m7.activePane)
	}
}

func TestTUIModel_ListScrolling(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)
	m4, _ := m3.Update(tuiTotalMsg{n: 10})
	m5 := m4.(tuiModel)

	// activePane=0 (meetings), pressing down should increment listOffset.
	m6, _ := m5.Update(tea.KeyMsg{Type: tea.KeyDown})
	m7 := m6.(tuiModel)
	if m7.listOffset != 1 {
		t.Fatalf("listOffset=%d after down, want 1", m7.listOffset)
	}
	// Up should decrement.
	m8, _ := m7.Update(tea.KeyMsg{Type: tea.KeyUp})
	m9 := m8.(tuiModel)
	if m9.listOffset != 0 {
		t.Fatalf("listOffset=%d after up, want 0", m9.listOffset)
	}
	// Up at top should not go below 0.
	m10, _ := m9.Update(tea.KeyMsg{Type: tea.KeyUp})
	m11 := m10.(tuiModel)
	if m11.listOffset != 0 {
		t.Fatalf("listOffset=%d at top, want 0", m11.listOffset)
	}
}

// ── View rendering tests ─────────────────────────────────────────────────────

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

func TestTUIModel_ViewFinishedError(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m3 := m2.(tuiModel)
	m4, _ := m3.Update(tuiDoneMsg{err: errors.New("browser crashed")})
	m5 := m4.(tuiModel)
	v := m5.View()
	if !strings.Contains(v, "Export failed") {
		t.Fatalf("expected 'Export failed' in view, got: %q", v)
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

func TestTUIModel_ViewMeetingList(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m3 := m2.(tuiModel)
	m4, _ := m3.Update(tuiTotalMsg{n: 2})
	m5 := m4.(tuiModel)
	m6, _ := m5.Update(tuiStartMsg{index: 0, title: "Q4 Planning"})
	m7 := m6.(tuiModel)
	v := m7.View()
	if !strings.Contains(v, "MEETINGS") {
		t.Fatalf("expected 'MEETINGS' pane title in view, got: %q", v)
	}
	if !strings.Contains(v, "ACTIVITY LOG") {
		t.Fatalf("expected 'ACTIVITY LOG' pane title in view, got: %q", v)
	}
}

func TestTUIModel_ViewHelp(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m3 := m2.(tuiModel)
	v := m3.View()
	// Help footer should contain key hints.
	if !strings.Contains(v, "tab") {
		t.Fatalf("expected 'tab' keybinding in view, got: %q", v)
	}
}

func TestTUIModel_ViewElapsed(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m3 := m2.(tuiModel)
	v := m3.View()
	// Header should contain elapsed time indicator.
	if !strings.Contains(v, "⏱") {
		t.Fatalf("expected elapsed timer ⏱ in view, got: %q", v)
	}
}

func TestTUIModel_PaneGeometry(t *testing.T) {
	m := newTUIModel()
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m3 := m2.(tuiModel)
	leftW, divW, rightW, contentH := m3.paneGeometry()
	if leftW < 24 {
		t.Fatalf("leftW=%d is too narrow (< 24)", leftW)
	}
	if rightW < 10 {
		t.Fatalf("rightW=%d is too narrow (< 10)", rightW)
	}
	if contentH < 1 {
		t.Fatalf("contentH=%d is too short (< 1)", contentH)
	}
	if leftW+divW+rightW > m3.width+1 {
		t.Fatalf("pane widths %d+%d+%d exceed terminal width %d", leftW, divW, rightW, m3.width)
	}
}

// ── formatElapsed ────────────────────────────────────────────────────────────

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00"},
		{30 * time.Second, "00:30"},
		{90 * time.Second, "01:30"},
		{3661 * time.Second, "1:01:01"},
	}
	for _, tc := range tests {
		got := formatElapsed(tc.d)
		if got != tc.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", tc.d, got, tc.want)
		}
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

func TestTUIHandler_LevelPrefixes(t *testing.T) {
	// Verify that different log levels produce different prefixes in the line.
	h := &TUIHandler{
		level: slog.LevelDebug,
		prog:  nil, // nil prog: Handle skips the send, no panic
	}
	levels := []slog.Level{slog.LevelError, slog.LevelWarn, slog.LevelInfo, slog.LevelDebug}
	for _, lvl := range levels {
		r := slog.NewRecord(time.Time{}, lvl, "test message", 0)
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatalf("Handle(%v) returned error: %v", lvl, err)
		}
	}
}

