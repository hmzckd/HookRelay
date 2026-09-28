package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"hookrelay/internal/logsafe"
	"hookrelay/internal/postgres"
)

func main() {
	apply := flag.Bool("apply", false, "delete eligible records (default is read-only preview)")
	limit := flag.Int("limit", 100, "maximum events to delete in one run (1-1000)")
	flag.Parse()
	if *limit < 1 || *limit > postgres.MaxCleanupBatch || os.Getenv("DATABASE_URL") == "" {
		slog.Error("DATABASE_URL and a cleanup limit between 1 and 1000 are required")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		slog.Error("PostgreSQL connection failed", "error_kind", logsafe.Kind(err))
		os.Exit(1)
	}
	defer db.Close()
	store := postgres.EventStore{DB: db}
	if !*apply {
		count, err := store.CountExpired(ctx)
		if err != nil {
			slog.Error("retention preview failed", "error_kind", logsafe.Kind(err))
			os.Exit(1)
		}
		slog.Info("retention preview", "retention_days", postgres.RetentionDays, "eligible_events", count, "applied", false)
		return
	}
	removed, err := store.CleanupExpired(ctx, *limit)
	if err != nil {
		slog.Error("retention cleanup failed", "error_kind", logsafe.Kind(err))
		os.Exit(1)
	}
	slog.Info("retention cleanup complete", "retention_days", postgres.RetentionDays, "removed_events", removed, "applied", true)
}
