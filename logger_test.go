package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestColorHandlerLevels(t *testing.T) {
	var buf bytes.Buffer
	h := NewColorHandler(&buf, slog.LevelInfo)
	logger := slog.New(h)

	// Debug should be filtered out at Info level
	logger.Debug("should not appear")
	if buf.Len() > 0 {
		t.Error("debug message should be filtered at Info level")
	}

	// Info should appear
	logger.Info("hello info")
	if !strings.Contains(buf.String(), "hello info") {
		t.Errorf("info missing from output: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "\u2713") {
		t.Error("info should have \u2713 prefix")
	}
	buf.Reset()

	// Warn
	logger.Warn("caution")
	if !strings.Contains(buf.String(), "\u26a0") {
		t.Error("warn should have \u26a0 prefix")
	}
	buf.Reset()

	// Error
	logger.Error("failure")
	if !strings.Contains(buf.String(), "\u2717") {
		t.Error("error should have \u2717 prefix")
	}
}

func TestColorHandlerDebugLevel(t *testing.T) {
	var buf bytes.Buffer
	h := NewColorHandler(&buf, slog.LevelDebug)
	logger := slog.New(h)

	logger.Debug("debug visible")
	if !strings.Contains(buf.String(), "debug visible") {
		t.Error("debug should appear at Debug level")
	}
}

func TestColorHandlerAttrs(t *testing.T) {
	var buf bytes.Buffer
	h := NewColorHandler(&buf, slog.LevelInfo)
	logger := slog.New(h)

	logger.Info("test", "key", "value", "count", 42)
	out := buf.String()
	if !strings.Contains(out, "key=value") {
		t.Errorf("expected key=value in output: %q", out)
	}
	if !strings.Contains(out, "count=42") {
		t.Errorf("expected count=42 in output: %q", out)
	}
}

func TestColorHandlerWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	h := NewColorHandler(&buf, slog.LevelInfo)
	child := h.WithAttrs([]slog.Attr{slog.String("component", "scraper")})
	logger := slog.New(child)

	logger.Info("fetch")
	out := buf.String()
	if !strings.Contains(out, "component=scraper") {
		t.Errorf("expected inherited attr in output: %q", out)
	}
}

func TestColorHandlerEnabled(t *testing.T) {
	h := NewColorHandler(nil, slog.LevelWarn)
	ctx := context.Background()

	if h.Enabled(ctx, slog.LevelDebug) {
		t.Error("debug should not be enabled at Warn level")
	}
	if h.Enabled(ctx, slog.LevelInfo) {
		t.Error("info should not be enabled at Warn level")
	}
	if !h.Enabled(ctx, slog.LevelWarn) {
		t.Error("warn should be enabled")
	}
	if !h.Enabled(ctx, slog.LevelError) {
		t.Error("error should be enabled")
	}
}
