package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hookrelay/internal/config"
	"hookrelay/internal/httpapi"
	"hookrelay/internal/logsafe"
	"hookrelay/internal/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error_kind", logsafe.Kind(err))
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := postgres.NewPool(cfg.DatabaseURL)
	if err != nil {
		logger.Error("PostgreSQL pool configuration failed", "error_kind", logsafe.Kind(err))
		os.Exit(1)
	}
	defer db.Close()
	keyIDs := make(map[string]struct{}, len(cfg.ExternalKeys))
	for id := range cfg.ExternalKeys {
		keyIDs[id] = struct{}{}
	}

	eventStore := postgres.EventStore{DB: db}
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpapi.NewWithAdminAndHealth(eventStore, postgres.EndpointStore{DB: db, Profile: cfg.TargetProfile, KeyIDs: keyIDs}, cfg.ProducerToken, cfg.AdminToken, cfg.TargetProfile, eventStore, eventStore),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	logger.Info("HookRelay API listening", "address", cfg.ListenAddr)
	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped", "error_kind", logsafe.Kind(err))
			os.Exit(1)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP shutdown failed", "error_kind", logsafe.Kind(err))
			os.Exit(1)
		}
	}
}
