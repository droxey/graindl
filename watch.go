package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// RunWatch runs the exporter in a continuous loop, polling for new meetings
// at the configured interval. The browser session is reused across cycles,
// and meetings that were already exported (metadata file exists) are
// automatically skipped.
func (e *Exporter) RunWatch(ctx context.Context) error {
	interval := e.cfg.WatchInterval

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

		// Touch healthcheck file so external monitors can detect liveness.
		if e.cfg.HealthcheckFile != "" {
			if err := os.WriteFile(e.cfg.HealthcheckFile, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
				slog.Warn("Healthcheck file write failed", "error", err)
			}
		}

		slog.Info(fmt.Sprintf("── cycle %d done (exported=%d skipped=%d errors=%d) — next poll in %s ──",
			cycle, e.manifest.OK, e.manifest.Skipped, e.manifest.Errors, interval))

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			// Shutdown requested while waiting.
		case <-timer.C:
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
