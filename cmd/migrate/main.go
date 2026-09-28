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
	dir := flag.String("dir", "migrations", "directory containing numbered SQL migrations")
	flag.Parse()
	if os.Getenv("DATABASE_URL") == "" {
		slog.Error("DATABASE_URL is required")
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
	if err := postgres.Migrate(ctx, db, *dir); err != nil {
		slog.Error("migration failed", "error_kind", logsafe.Kind(err))
		os.Exit(1)
	}
	slog.Info("migrations applied")
}
