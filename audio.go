package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
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
//
// When verbose is true, ffmpeg diagnostic output is forwarded to stderr.
func extractAudio(ctx context.Context, input, outputPath string, verbose bool) error {
	// Try codec copy first — fast, no quality loss.
	// -vn drops video, -c:a copy keeps original audio codec.
	if err := runFFmpeg(ctx, verbose, "-i", input, "-vn", "-c:a", "copy", "-y", outputPath); err == nil {
		return fixPerms(outputPath)
	}
	slog.Debug("Codec copy failed, re-encoding to AAC", "input", input)

	// Fall back to re-encoding to AAC at 192 kbps.
	if err := runFFmpeg(ctx, verbose, "-i", input, "-vn", "-c:a", "aac", "-b:a", "192k", "-y", outputPath); err != nil {
		return fmt.Errorf("ffmpeg audio extraction failed: %w", err)
	}
	return fixPerms(outputPath)
}

// runFFmpeg executes ffmpeg with the given args. Diagnostic output is forwarded
// to stderr when verbose is true, otherwise suppressed via -loglevel error.
func runFFmpeg(ctx context.Context, verbose bool, args ...string) error {
	if !verbose {
		args = append([]string{"-loglevel", "error"}, args...)
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdout = nil
	if verbose {
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stderr = io.Discard
	}
	return cmd.Run()
}

// fixPerms applies SEC-11 file permissions (0o600) to the output file.
// ffmpeg creates files using the process umask; this ensures consistency
// with all other output files in the export.
func fixPerms(path string) error {
	return os.Chmod(path, 0o600)
}
