package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
)

// checkFFmpeg verifies that ffmpeg is available on PATH.
func checkFFmpeg() error {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("ffmpeg not found in PATH (required for --audio-only): %w", err)
	}
	slog.Debug("ffmpeg found", "path", path)
	return nil
}

// extractAudio uses ffmpeg to extract the audio track from input (file path or URL)
// and writes it to outputPath (.m4a). It first tries a codec copy (fast, lossless)
// and falls back to re-encoding to AAC if the copy fails.
func extractAudio(ctx context.Context, input, outputPath string) error {
	// Try codec copy first — fast, no quality loss.
	// -vn drops video, -c:a copy keeps original audio codec.
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", input,
		"-vn",
		"-c:a", "copy",
		"-y",
		outputPath,
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err == nil {
		return nil
	}
	slog.Debug("Codec copy failed, re-encoding to AAC", "input", input)

	// Fall back to re-encoding to AAC at 192 kbps.
	cmd = exec.CommandContext(ctx, "ffmpeg",
		"-i", input,
		"-vn",
		"-c:a", "aac",
		"-b:a", "192k",
		"-y",
		outputPath,
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg audio extraction failed: %w", err)
	}
	return nil
}
