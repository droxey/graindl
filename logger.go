package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// GO-2: Custom slog.Handler with color output for terminals.
// Supports structured fields, level filtering, and could be swapped
// for slog.NewJSONHandler(os.Stderr, opts) for machine-readable output.

const (
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cRed    = "\033[31m"
	cDim    = "\033[2m"
	cReset  = "\033[0m"
)

type ColorHandler struct {
	w     io.Writer
	level slog.Level
	mu    sync.Mutex
	attrs []slog.Attr
	group string
}

func NewColorHandler(w io.Writer, level slog.Level) *ColorHandler {
	return &ColorHandler{w: w, level: level}
}

func (h *ColorHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *ColorHandler) Handle(_ context.Context, r slog.Record) error {
	var prefix, color string
	switch {
	case r.Level >= slog.LevelError:
		prefix, color = "✗", cRed
	case r.Level >= slog.LevelWarn:
		prefix, color = "⚠", cYellow
	case r.Level >= slog.LevelInfo:
		prefix, color = "✓", cGreen
	default:
		prefix, color = " ", cDim
	}

	var b strings.Builder
	b.WriteString(color)
	b.WriteString(prefix)
	b.WriteByte(' ')
	b.WriteString(r.Message)
	b.WriteString(cReset)

	// Collect all attrs: inherited + record-level.
	attrs := h.collectAttrs(r)
	if len(attrs) > 0 {
		b.WriteString(cDim)
		for _, a := range attrs {
			fmt.Fprintf(&b, " %s=%s", a.Key, a.Value.String())
		}
		b.WriteString(cReset)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := fmt.Fprintln(h.w, b.String())
	return err
}

// collectAttrs merges inherited attrs with the record's attrs,
// filtering out any with empty string values.
func (h *ColorHandler) collectAttrs(r slog.Record) []slog.Attr {
	var out []slog.Attr
	for _, a := range h.attrs {
		if a.Value.String() != "" {
			out = append(out, a)
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Value.String() != "" {
			out = append(out, a)
		}
		return true
	})
	return out
}

func (h *ColorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ColorHandler{
		w:     h.w,
		level: h.level,
		attrs: append(h.attrs[:len(h.attrs):len(h.attrs)], attrs...),
		group: h.group,
	}
}

func (h *ColorHandler) WithGroup(name string) slog.Handler {
	return &ColorHandler{
		w:     h.w,
		level: h.level,
		attrs: h.attrs,
		group: name,
	}
}
