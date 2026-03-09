package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── TUI messages ────────────────────────────────────────────────────────────

// tuiLogMsg carries a rendered log line from the TUI slog handler.
type tuiLogMsg struct{ line string }

// tuiDoneMsg signals the export goroutine has finished.
type tuiDoneMsg struct{ err error }

// tuiTotalMsg communicates how many meetings will be exported.
type tuiTotalMsg struct{ n int }

// tuiResultMsg communicates a single meeting export result.
type tuiResultMsg struct{ status string }

// ── Styles ──────────────────────────────────────────────────────────────────

var (
	tuiTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("62")).
			Padding(0, 1)

	tuiStatusBar = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Padding(0, 1)

	tuiOK      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	tuiSkipped = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	tuiErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	tuiHLS     = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	tuiDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

// ── TUI Model ───────────────────────────────────────────────────────────────

type tuiModel struct {
	// widgets
	spinner  spinner.Model
	progress progress.Model
	viewport viewport.Model

	// state
	lines    []string
	total    int
	done     int
	ok       int
	skipped  int
	errors   int
	hls      int
	finished bool
	exitErr  error

	// layout
	width  int
	height int
	ready  bool
}

func newTUIModel() tuiModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	prog := progress.New(
		progress.WithDefaultGradient(),
		progress.WithoutPercentage(),
	)

	return tuiModel{
		spinner:  sp,
		progress: prog,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalcLayout()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case progress.FrameMsg:
		pm, cmd := m.progress.Update(msg)
		m.progress = pm.(progress.Model)
		cmds = append(cmds, cmd)

	case tuiLogMsg:
		m.lines = append(m.lines, msg.line)
		if m.ready {
			m.viewport.SetContent(strings.Join(m.lines, "\n"))
			m.viewport.GotoBottom()
		}

	case tuiTotalMsg:
		m.total = msg.n

	case tuiResultMsg:
		m.done++
		switch msg.status {
		case "ok":
			m.ok++
		case "skipped":
			m.skipped++
		case "hls_pending":
			m.hls++
			m.ok++
		default:
			m.errors++
		}
		if m.total > 0 {
			cmds = append(cmds, m.progress.SetPercent(float64(m.done)/float64(m.total)))
		}

	case tuiDoneMsg:
		m.finished = true
		m.exitErr = msg.err
		// Final viewport scroll.
		if m.ready {
			m.viewport.SetContent(strings.Join(m.lines, "\n"))
			m.viewport.GotoBottom()
		}
	}

	// Forward keys/scroll to viewport.
	if m.ready {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// recalcLayout sets the viewport size based on the terminal dimensions.
// Header (title + status line + progress) = 4 lines, footer = 2 lines.
func (m *tuiModel) recalcLayout() {
	headerHeight := 4
	footerHeight := 2

	vpHeight := m.height - headerHeight - footerHeight
	if vpHeight < 1 {
		vpHeight = 1
	}

	if !m.ready {
		m.viewport = viewport.New(m.width, vpHeight)
		m.viewport.SetContent(strings.Join(m.lines, "\n"))
		m.ready = true
	} else {
		m.viewport.Width = m.width
		m.viewport.Height = vpHeight
	}
	m.progress.Width = m.width - 12
	if m.progress.Width < 10 {
		m.progress.Width = 10
	}
}

func (m tuiModel) View() string {
	if m.width == 0 {
		return "Initializing…"
	}

	var b strings.Builder

	// Title bar.
	title := tuiTitle.Render(fmt.Sprintf(" 🌾 graindl %s ", version))
	b.WriteString(title)
	b.WriteByte('\n')

	// Status line.
	if m.finished {
		if m.exitErr != nil {
			b.WriteString(tuiErr.Render("✗ Export failed: " + m.exitErr.Error()))
		} else {
			b.WriteString(tuiOK.Render("✓ Export complete"))
		}
	} else if m.total > 0 {
		b.WriteString(m.spinner.View())
		b.WriteString(fmt.Sprintf(" Exporting meeting %d of %d…", m.done+1, m.total))
	} else {
		b.WriteString(m.spinner.View())
		b.WriteString(" Discovering meetings…")
	}
	b.WriteByte('\n')

	// Progress bar.
	if m.total > 0 {
		pct := float64(m.done) / float64(m.total)
		b.WriteString(m.progress.ViewAs(pct))
		b.WriteString(fmt.Sprintf(" %d/%d", m.done, m.total))
	}
	b.WriteByte('\n')
	b.WriteByte('\n')

	// Log viewport.
	if m.ready {
		b.WriteString(m.viewport.View())
	}

	b.WriteByte('\n')

	// Status bar.
	stats := fmt.Sprintf(" ok:%s  skipped:%s  errors:%s  hls:%s",
		tuiOK.Render(fmt.Sprintf("%d", m.ok)),
		tuiSkipped.Render(fmt.Sprintf("%d", m.skipped)),
		tuiErr.Render(fmt.Sprintf("%d", m.errors)),
		tuiHLS.Render(fmt.Sprintf("%d", m.hls)),
	)
	help := tuiDim.Render("q: quit • ↑/↓: scroll")
	gap := ""
	// Rough padding; lipgloss.Width handles ANSI.
	used := lipgloss.Width(stats) + lipgloss.Width(help)
	if pad := m.width - used; pad > 0 {
		gap = strings.Repeat(" ", pad)
	}
	b.WriteString(tuiStatusBar.Render(stats + gap + help))

	return b.String()
}

// ── TUI slog handler ────────────────────────────────────────────────────────

// TUIHandler implements slog.Handler, forwarding log records to the Bubble
// Tea program as tuiLogMsg messages.
type TUIHandler struct {
	level slog.Level
	mu    sync.Mutex
	prog  *tea.Program
	attrs []slog.Attr
	group string
}

func NewTUIHandler(prog *tea.Program, level slog.Level) *TUIHandler {
	return &TUIHandler{prog: prog, level: level}
}

func (h *TUIHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *TUIHandler) Handle(_ context.Context, r slog.Record) error {
	var prefix string
	switch {
	case r.Level >= slog.LevelError:
		prefix = "✗"
	case r.Level >= slog.LevelWarn:
		prefix = "⚠"
	case r.Level >= slog.LevelInfo:
		prefix = "✓"
	default:
		prefix = " "
	}

	var b strings.Builder
	b.WriteString(prefix)
	b.WriteByte(' ')
	b.WriteString(r.Message)

	attrs := h.collectAttrs(r)
	if len(attrs) > 0 {
		for _, a := range attrs {
			fmt.Fprintf(&b, " %s=%s", a.Key, a.Value.String())
		}
	}

	h.mu.Lock()
	p := h.prog
	h.mu.Unlock()

	if p != nil {
		p.Send(tuiLogMsg{line: b.String()})
	}
	return nil
}

func (h *TUIHandler) collectAttrs(r slog.Record) []slog.Attr {
	var out []slog.Attr
	for _, a := range h.attrs {
		if a.Value.String() != "" {
			out = append(out, h.prefixAttr(a))
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Value.String() != "" {
			out = append(out, h.prefixAttr(a))
		}
		return true
	})
	return out
}

func (h *TUIHandler) prefixAttr(a slog.Attr) slog.Attr {
	if h.group != "" {
		a.Key = h.group + "." + a.Key
	}
	return a
}

func (h *TUIHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TUIHandler{
		prog:  h.prog,
		level: h.level,
		attrs: append(h.attrs[:len(h.attrs):len(h.attrs)], attrs...),
		group: h.group,
	}
}

func (h *TUIHandler) WithGroup(name string) slog.Handler {
	newGroup := name
	if h.group != "" {
		newGroup = h.group + "." + name
	}
	return &TUIHandler{
		prog:  h.prog,
		level: h.level,
		attrs: h.attrs,
		group: newGroup,
	}
}

// ── TUI runner ──────────────────────────────────────────────────────────────

// runTUI starts the Bubble Tea TUI, runs the exporter in a goroutine, and
// blocks until the TUI exits.
func runTUI(ctx context.Context, cfg *Config) error {
	m := newTUIModel()
	p := tea.NewProgram(m, tea.WithAltScreen())

	// Wire up slog → TUI.
	logLevel := slog.LevelInfo
	if cfg.Verbose {
		logLevel = slog.LevelDebug
	}
	handler := NewTUIHandler(p, logLevel)
	slog.SetDefault(slog.New(handler))

	// Run exporter in the background.
	go func() {
		exp, err := NewExporter(ctx, cfg)
		if err != nil {
			p.Send(tuiDoneMsg{err: err})
			return
		}
		defer exp.Close()

		// Hook into manifest to send progress.
		origRun := func() error {
			if cfg.Watch {
				return exp.RunWatch(ctx)
			}
			return exp.Run(ctx)
		}

		// We wrap exportOne to send progress. But since we can't easily
		// intercept the export loop, we'll poll the manifest. Instead, let's
		// send the total count via a log-level hook. The "Exporting meetings"
		// log line includes "count=N" — we can detect that in the handler.
		// Alternatively, we simply inject a callback approach.
		//
		// For simplicity, we use a TUI-aware wrapper that sends counts.
		exp.tuiSendTotal = func(n int) { p.Send(tuiTotalMsg{n: n}) }
		exp.tuiSendResult = func(status string) { p.Send(tuiResultMsg{status: status}) }

		err = origRun()
		p.Send(tuiDoneMsg{err: err})
	}()

	_, err := p.Run()
	return err
}
