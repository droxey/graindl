package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// RunWatch runs the exporter in a continuous loop, polling for new meetings
// at the configured interval. The browser session is reused across cycles,
// and meetings that were already exported (metadata file exists) are
// automatically skipped.
func (e *Exporter) RunWatch(ctx context.Context) error {
	interval := e.cfg.WatchInterval
	slog.Info(fmt.Sprintf("Watch mode: polling every %s (Ctrl-C to stop)", interval))

	var totalOK, totalSkipped, totalErrors int
	cycle := 0

	for {
		cycle++
		slog.Info(fmt.Sprintf("── watch cycle %d ─────────────────────────────────────", cycle))

		// Fresh manifest per cycle.
		e.manifest = &ExportManifest{ExportedAt: time.Now().UTC().Format(time.RFC3339)}
		e.searchFilter = nil

		err := e.Run(ctx)
		totalOK += e.manifest.OK
		totalSkipped += e.manifest.Skipped
		totalErrors += e.manifest.Errors

		// Shutdown requested during export.
		if ctx.Err() != nil {
			break
		}

		if err != nil {
			slog.Error("Cycle failed (will retry)", "cycle", cycle, "error", err)
		}

		slog.Info(fmt.Sprintf("── cycle %d done (exported=%d skipped=%d errors=%d) — next poll in %s ──",
			cycle, e.manifest.OK, e.manifest.Skipped, e.manifest.Errors, interval))

		select {
		case <-ctx.Done():
			// Shutdown requested while waiting.
		case <-time.After(interval):
			continue
		}
		break
	}

	slog.Info("Watch mode stopped",
		"cycles", cycle,
		"total_exported", totalOK,
		"total_skipped", totalSkipped,
		"total_errors", totalErrors,
	)
	return nil
}
