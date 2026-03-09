package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/stopwatch"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Color palette (Dracula-inspired) ────────────────────────────────────────

const (
	clrBg      = "#282A36"
	clrCurrent = "#44475A"
	clrFg      = "#F8F8F2"
	clrComment = "#6272A4"
	clrCyan    = "#8BE9FD"
	clrGreen   = "#50FA7B"
	clrOrange  = "#FFB86C"
	clrPink    = "#FF79C6"
	clrPurple  = "#BD93F9"
	clrRed     = "#FF5555"
	clrYellow  = "#F1FA8C"
)

// ── Styles ──────────────────────────────────────────────────────────────────

var (
	// Header
	tuiTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(clrBg)).
			Background(lipgloss.Color(clrPurple)).
			Padding(0, 1)

	tuiElapsedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(clrComment))

	// Status indicators
	tuiOK      = lipgloss.NewStyle().Foreground(lipgloss.Color(clrGreen))
	tuiSkipped = lipgloss.NewStyle().Foreground(lipgloss.Color(clrYellow))
	tuiErr     = lipgloss.NewStyle().Foreground(lipgloss.Color(clrRed))
	tuiHLS     = lipgloss.NewStyle().Foreground(lipgloss.Color(clrCyan))
	tuiActive  = lipgloss.NewStyle().Foreground(lipgloss.Color(clrPink)).Bold(true)
	tuiDim     = lipgloss.NewStyle().Foreground(lipgloss.Color(clrComment))

	// Pane chrome
	tuiPaneTitleStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(clrPurple))
	tuiActivePaneTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(clrPink))

	tuiDivider = lipgloss.NewStyle().Foreground(lipgloss.Color(clrCurrent))

	// Stats bar
	tuiStatsBarStyle = lipgloss.NewStyle().
				Background(lipgloss.Color(clrCurrent)).
				Foreground(lipgloss.Color(clrFg)).
				Padding(0, 1)

	// Help bar
	tuiHelpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(clrComment)).
			Padding(0, 1)
)

// ── TUI messages ────────────────────────────────────────────────────────────

// tuiLogMsg carries a rendered log line from the TUI slog handler.
type tuiLogMsg struct{ line string }

// tuiDoneMsg signals the export goroutine has finished.
type tuiDoneMsg struct{ err error }

// tuiTotalMsg communicates how many meetings will be exported.
type tuiTotalMsg struct{ n int }

// tuiStartMsg signals that a specific meeting has started exporting.
type tuiStartMsg struct {
	index int
	title string
}

// tuiResultMsg communicates a single meeting export result.
type tuiResultMsg struct {
	index  int
	title  string
	status string
}

// ── Key bindings ─────────────────────────────────────────────────────────────

type tuiKeyMap struct {
	Up       key.Binding
	Down     key.Binding
	HalfUp   key.Binding
	HalfDown key.Binding
	Tab      key.Binding
	Quit     key.Binding
}

func (k tuiKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Tab, k.Quit}
}

func (k tuiKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.HalfUp, k.HalfDown},
		{k.Tab, k.Quit},
	}
}

var defaultTUIKeys = tuiKeyMap{
	Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "scroll up")),
	Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "scroll down")),
	HalfUp:   key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "½ page up")),
	HalfDown: key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "½ page dn")),
	Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "switch pane")),
	Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c", "esc"), key.WithHelp("q", "quit")),
}

// ── Per-meeting state ────────────────────────────────────────────────────────

type tuiMeeting struct {
	index  int
	title  string
	status string // "pending" | "active" | "ok" | "skipped" | "error" | "hls_pending"
}

// ── TUI Model ────────────────────────────────────────────────────────────────

type tuiModel struct {
	// widgets
	spinner   spinner.Model
	progress  progress.Model
	viewport  viewport.Model
	stopwatch stopwatch.Model
	help      help.Model
	keys      tuiKeyMap

	// meetings list
	meetings   []tuiMeeting
	listOffset int // first visible meeting row

	// log state
	lines []string

	// counters
	total   int
	done    int
	ok      int
	skipped int
	errors  int
	hls     int

	// state
	finished bool
	exitErr  error

	// layout
	width      int
	height     int
	ready      bool
	activePane int // 0 = meetings list, 1 = log viewport
}

func newTUIModel() tuiModel {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(clrPink))

	prog := progress.New(
		progress.WithGradient(clrCyan, clrPurple),
		progress.WithoutPercentage(),
	)

	sw := stopwatch.NewWithInterval(time.Second)

	h := help.New()
	h.Styles.ShortKey = lipgloss.NewStyle().Foreground(lipgloss.Color(clrPurple))
	h.Styles.ShortDesc = lipgloss.NewStyle().Foreground(lipgloss.Color(clrComment))
	h.Styles.ShortSeparator = lipgloss.NewStyle().Foreground(lipgloss.Color(clrCurrent))

	return tuiModel{
		spinner:   sp,
		progress:  prog,
		stopwatch: sw,
		help:      h,
		keys:      defaultTUIKeys,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.stopwatch.Init())
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit

		case key.Matches(msg, m.keys.Tab):
			m.activePane = 1 - m.activePane // toggle 0↔1

		case key.Matches(msg, m.keys.Up):
			if m.activePane == 0 {
				if m.listOffset > 0 {
					m.listOffset--
				}
			}
		case key.Matches(msg, m.keys.Down):
			if m.activePane == 0 {
				m.listOffset++
			}
		case key.Matches(msg, m.keys.HalfUp):
			if m.activePane == 1 && m.ready {
				m.viewport.HalfViewUp()
			}
		case key.Matches(msg, m.keys.HalfDown):
			if m.activePane == 1 && m.ready {
				m.viewport.HalfViewDown()
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalcLayout()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case stopwatch.TickMsg, stopwatch.StartStopMsg:
		var cmd tea.Cmd
		m.stopwatch, cmd = m.stopwatch.Update(msg)
		cmds = append(cmds, cmd)

	case progress.FrameMsg:
		pm, cmd := m.progress.Update(msg)
		m.progress = pm.(progress.Model)
		cmds = append(cmds, cmd)

	case tuiLogMsg:
		m.lines = append(m.lines, msg.line)
		if m.ready {
			m.viewport.SetContent(strings.Join(m.lines, "\n"))
			if m.activePane == 1 {
				m.viewport.GotoBottom()
			}
		}

	case tuiTotalMsg:
		m.total = msg.n
		// Pre-populate with pending placeholders so the list shows
		// all slots immediately.
		m.meetings = make([]tuiMeeting, msg.n)
		for i := range m.meetings {
			m.meetings[i] = tuiMeeting{
				index:  i,
				title:  fmt.Sprintf("Meeting %d", i+1),
				status: "pending",
			}
		}

	case tuiStartMsg:
		if msg.index < len(m.meetings) {
			if msg.title != "" {
				m.meetings[msg.index].title = msg.title
			}
			m.meetings[msg.index].status = "active"
		} else {
			m.meetings = append(m.meetings, tuiMeeting{
				index:  msg.index,
				title:  coalesce(msg.title, fmt.Sprintf("Meeting %d", msg.index+1)),
				status: "active",
			})
		}
		// Auto-scroll the list to keep the active meeting in view.
		m.autoScrollList()

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
		// Update meeting entry.
		if msg.index < len(m.meetings) {
			if msg.title != "" {
				m.meetings[msg.index].title = msg.title
			}
			m.meetings[msg.index].status = msg.status
		}
		if m.total > 0 {
			cmds = append(cmds, m.progress.SetPercent(float64(m.done)/float64(m.total)))
		}

	case tuiDoneMsg:
		m.finished = true
		m.exitErr = msg.err
		if m.ready {
			m.viewport.SetContent(strings.Join(m.lines, "\n"))
			m.viewport.GotoBottom()
		}
	}

	// Forward scroll events to the log viewport when it's the active pane.
	if m.ready && m.activePane == 1 {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// autoScrollList adjusts listOffset so the most recently activated meeting is
// visible inside the meeting pane.
func (m *tuiModel) autoScrollList() {
	if !m.ready {
		return
	}
	_, _, _, listHeight := m.paneGeometry()
	visibleRows := listHeight - 2 // subtract pane header + separator
	if visibleRows < 1 {
		return
	}
	// Find the index of the active meeting.
	activeIdx := -1
	for i, mt := range m.meetings {
		if mt.status == "active" {
			activeIdx = i
		}
	}
	if activeIdx < 0 {
		return
	}
	// Scroll down if active is below visible window.
	if activeIdx >= m.listOffset+visibleRows {
		m.listOffset = activeIdx - visibleRows + 1
	}
	// Scroll up if active is above visible window.
	if activeIdx < m.listOffset {
		m.listOffset = activeIdx
	}
}

// paneGeometry returns left-pane width, divider width, right-pane width, and
// the shared content height used for both panes.
func (m *tuiModel) paneGeometry() (leftW, divW, rightW, contentH int) {
	// Fixed chrome rows: header(1) + progress(1) + stats(1) + help(1) = 4
	const fixedRows = 4
	contentH = m.height - fixedRows
	if contentH < 3 {
		contentH = 3
	}
	divW = 1
	leftW = m.width * 35 / 100
	if leftW < 24 {
		leftW = 24
	}
	if leftW > 48 {
		leftW = 48
	}
	rightW = m.width - leftW - divW
	if rightW < 10 {
		rightW = 10
	}
	return
}

// recalcLayout recomputes the viewport dimensions from the current window size.
func (m *tuiModel) recalcLayout() {
	_, _, rightW, contentH := m.paneGeometry()

	if !m.ready {
		m.viewport = viewport.New(rightW, contentH)
		m.viewport.SetContent(strings.Join(m.lines, "\n"))
		m.ready = true
	} else {
		m.viewport.Width = rightW
		m.viewport.Height = contentH
	}

	m.progress.Width = m.width - 16
	if m.progress.Width < 10 {
		m.progress.Width = 10
	}
	m.help.Width = m.width
}

func (m tuiModel) View() string {
	if m.width == 0 {
		return "Initializing…"
	}

	leftW, divW, rightW, contentH := m.paneGeometry()
	_ = divW

	var b strings.Builder

	// ── Header row ──────────────────────────────────────────────────────────
	titleText := tuiTitleStyle.Render("🌾 graindl " + version)

	var statusText string
	if m.finished {
		if m.exitErr != nil {
			statusText = tuiErr.Render("✗ Export failed: " + m.exitErr.Error())
		} else {
			statusText = tuiOK.Render("✓ Export complete")
		}
	} else if m.total > 0 {
		statusText = m.spinner.View() + tuiActive.Render(fmt.Sprintf(" Exporting meeting %d of %d…", m.done+1, m.total))
	} else {
		statusText = m.spinner.View() + tuiDim.Render(" Discovering meetings…")
	}

	elapsedText := tuiElapsedStyle.Render("⏱ " + formatElapsed(m.stopwatch.Elapsed()))
	// Right-align elapsed; pad the middle.
	headerUsed := lipgloss.Width(titleText) + lipgloss.Width(statusText) + lipgloss.Width(elapsedText)
	headerPad := m.width - headerUsed - 2 // -2 for spacing
	if headerPad < 1 {
		headerPad = 1
	}
	b.WriteString(titleText)
	b.WriteString("  ")
	b.WriteString(statusText)
	b.WriteString(strings.Repeat(" ", headerPad))
	b.WriteString(elapsedText)
	b.WriteByte('\n')

	// ── Two-pane content ─────────────────────────────────────────────────────
	leftContent := m.renderMeetingList(leftW, contentH)
	rightContent := m.renderLogPane(rightW, contentH)

	// Build a vertical divider.
	dividerLines := make([]string, contentH)
	for i := range dividerLines {
		dividerLines[i] = tuiDivider.Render("│")
	}
	dividerStr := strings.Join(dividerLines, "\n")

	panes := lipgloss.JoinHorizontal(lipgloss.Top, leftContent, dividerStr, rightContent)
	b.WriteString(panes)
	b.WriteByte('\n')

	// ── Progress row ─────────────────────────────────────────────────────────
	if m.total > 0 {
		pct := float64(m.done) / float64(m.total)
		b.WriteString(m.progress.ViewAs(pct))
		b.WriteString(tuiDim.Render(fmt.Sprintf("  %d/%d", m.done, m.total)))
	} else {
		b.WriteString(tuiDim.Render("waiting for meetings…"))
	}
	b.WriteByte('\n')

	// ── Stats bar ────────────────────────────────────────────────────────────
	statsContent := fmt.Sprintf("ok:%s  skipped:%s  errors:%s  hls:%s",
		tuiOK.Render(fmt.Sprintf("%d", m.ok)),
		tuiSkipped.Render(fmt.Sprintf("%d", m.skipped)),
		tuiErr.Render(fmt.Sprintf("%d", m.errors)),
		tuiHLS.Render(fmt.Sprintf("%d", m.hls)),
	)
	b.WriteString(tuiStatsBarStyle.Width(m.width).Render(statsContent))
	b.WriteByte('\n')

	// ── Help bar ─────────────────────────────────────────────────────────────
	b.WriteString(tuiHelpStyle.Render(m.help.View(m.keys)))

	return b.String()
}

// renderMeetingList renders the left pane (meeting list) as a fixed-height
// block of exactly `height` lines, each truncated/padded to `width` columns.
func (m *tuiModel) renderMeetingList(width, height int) string {
	var titleStyle, sepColor lipgloss.Style
	if m.activePane == 0 {
		titleStyle = tuiActivePaneTitleStyle
		sepColor = lipgloss.NewStyle().Foreground(lipgloss.Color(clrPink))
	} else {
		titleStyle = tuiPaneTitleStyle
		sepColor = tuiDivider
	}

	hdr := titleStyle.Render("MEETINGS")
	sep := sepColor.Render(strings.Repeat("─", width))

	rows := []string{
		lipgloss.NewStyle().Width(width).Render(hdr),
		sep,
	}

	if len(m.meetings) == 0 {
		rows = append(rows, tuiDim.Render("  ○ Discovering…"))
	} else {
		visibleRows := height - len(rows)
		end := m.listOffset + visibleRows
		if end > len(m.meetings) {
			end = len(m.meetings)
		}
		start := m.listOffset
		if start > len(m.meetings) {
			start = len(m.meetings)
		}
		for _, mt := range m.meetings[start:end] {
			rows = append(rows, m.renderMeetingRow(mt, width))
		}
		// Scroll indicators.
		if m.listOffset > 0 {
			rows[2] = tuiDim.Render(strings.Repeat("─", width/2) + " ↑ more")
		}
		if end < len(m.meetings) {
			last := tuiDim.Render(strings.Repeat("─", width/2) + " ↓ more")
			if len(rows) < height {
				rows = append(rows, last)
			} else {
				rows[len(rows)-1] = last
			}
		}
	}

	// Pad to exact height.
	empty := lipgloss.NewStyle().Width(width).Render("")
	for len(rows) < height {
		rows = append(rows, empty)
	}
	return strings.Join(rows[:height], "\n")
}

// renderMeetingRow renders a single meeting row with status icon and title.
func (m *tuiModel) renderMeetingRow(mt tuiMeeting, width int) string {
	var icon string
	var rowStyle lipgloss.Style

	switch mt.status {
	case "active":
		icon = m.spinner.View()
		rowStyle = tuiActive
	case "ok":
		icon = "✓"
		rowStyle = tuiOK
	case "skipped":
		icon = "⊘"
		rowStyle = tuiSkipped
	case "error":
		icon = "✗"
		rowStyle = tuiErr
	case "hls_pending":
		icon = "↓"
		rowStyle = tuiHLS
	default: // pending
		icon = "○"
		rowStyle = tuiDim
	}

	maxTitle := width - 5 // "  X " prefix (2 spaces + icon + space)
	if maxTitle < 1 {
		maxTitle = 1
	}
	title := mt.title
	if runes := []rune(title); len(runes) > maxTitle {
		title = string(runes[:maxTitle-1]) + "…"
	}

	return rowStyle.Render(fmt.Sprintf("  %s %s", icon, title))
}

// renderLogPane renders the right pane (activity log viewport).
func (m *tuiModel) renderLogPane(width, height int) string {
	var titleStyle, sepColor lipgloss.Style
	if m.activePane == 1 {
		titleStyle = tuiActivePaneTitleStyle
		sepColor = lipgloss.NewStyle().Foreground(lipgloss.Color(clrPink))
	} else {
		titleStyle = tuiPaneTitleStyle
		sepColor = tuiDivider
	}

	hdr := titleStyle.Render("ACTIVITY LOG")
	sep := sepColor.Render(strings.Repeat("─", width))

	vpHeight := height - 2 // subtract header row + separator
	if vpHeight < 1 {
		vpHeight = 1
	}
	if m.ready {
		m.viewport.Width = width
		m.viewport.Height = vpHeight
	}

	vpContent := ""
	if m.ready {
		vpContent = m.viewport.View()
	}

	return strings.Join([]string{
		lipgloss.NewStyle().Width(width).Render(hdr),
		sep,
		vpContent,
	}, "\n")
}

// formatElapsed formats a duration as mm:ss (or h:mm:ss for ≥1 hour).
func formatElapsed(d time.Duration) string {
	d = d.Truncate(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
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
	var prefixStyle lipgloss.Style
	switch {
	case r.Level >= slog.LevelError:
		prefix = "✗"
		prefixStyle = tuiErr
	case r.Level >= slog.LevelWarn:
		prefix = "⚠"
		prefixStyle = tuiSkipped
	case r.Level >= slog.LevelInfo:
		prefix = "✓"
		prefixStyle = tuiOK
	default:
		prefix = "·"
		prefixStyle = tuiDim
	}

	var b strings.Builder
	b.WriteString(prefixStyle.Render(prefix))
	b.WriteByte(' ')
	b.WriteString(r.Message)

	attrs := h.collectAttrs(r)
	if len(attrs) > 0 {
		for _, a := range attrs {
			fmt.Fprintf(&b, " %s=%s", tuiDim.Render(a.Key), a.Value.String())
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

		exp.tuiSendTotal = func(n int) { p.Send(tuiTotalMsg{n: n}) }
		exp.tuiSendStart = func(i int, title string) { p.Send(tuiStartMsg{index: i, title: title}) }
		exp.tuiSendResult = func(i int, title string, status string) {
			p.Send(tuiResultMsg{index: i, title: title, status: status})
		}

		var err2 error
		if cfg.Watch {
			err2 = exp.RunWatch(ctx)
		} else {
			err2 = exp.Run(ctx)
		}
		p.Send(tuiDoneMsg{err: err2})
	}()

	_, err := p.Run()
	return err
}
