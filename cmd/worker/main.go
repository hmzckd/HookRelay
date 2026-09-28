package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"hookrelay/internal/config"
	"hookrelay/internal/delivery"
	"hookrelay/internal/logsafe"
	"hookrelay/internal/postgres"
	"hookrelay/internal/targetpolicy"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := config.LoadWorker()
	if err != nil {
		slog.Error("invalid worker configuration", "error_kind", logsafe.Kind(err))
		os.Exit(1)
	}
	secretA := os.Getenv("DEMO_A_SECRET")
	secretB := os.Getenv("DEMO_B_SECRET")
	if cfg.TargetProfile == targetpolicy.ProfileDemo && (len(secretA) < 32 || len(secretB) < 32) {
		slog.Error("both 32-byte demo secrets are required in demo profile")
		os.Exit(1)
	}
	secrets := cfg.ExternalKeys
	if cfg.TargetProfile == targetpolicy.ProfileDemo {
		secrets["demo/a-v1"] = []byte(secretA)
		secrets["demo/b-v1"] = []byte(secretB)
	}
	concurrency := 4
	if raw := os.Getenv("WORKER_CONCURRENCY"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 16 {
			slog.Error("WORKER_CONCURRENCY must be between 1 and 16")
			os.Exit(1)
		}
		concurrency = parsed
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := postgres.NewPool(cfg.DatabaseURL)
	if err != nil {
		slog.Error("PostgreSQL pool configuration failed", "error_kind", logsafe.Kind(err))
		os.Exit(1)
	}
	defer db.Close()
	progress := &delivery.Progress{}
	service := delivery.Service{
		Store:        postgres.EventStore{DB: db},
		Sender:       delivery.NewHTTPSenderForNetwork(secrets, cfg.TargetProfile, cfg.ComposeDemo),
		PollInterval: 500 * time.Millisecond,
		Concurrency:  concurrency,
		Progress:     progress,
	}
	var healthServer *http.Server
	healthError := make(chan error, 1)
	if cfg.HealthAddr != "" {
		listener, err := net.Listen("tcp", cfg.HealthAddr)
		if err != nil {
			slog.Error("worker health listener failed", "error_kind", logsafe.Kind(err))
			os.Exit(1)
		}
		healthServer = &http.Server{
			Handler:           delivery.HealthHandler(progress),
			ReadHeaderTimeout: 3 * time.Second,
			ReadTimeout:       5 * time.Second,
			WriteTimeout:      5 * time.Second,
		}
		go func() {
			if err := healthServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				healthError <- err
			}
		}()
		slog.Info("worker health listening", "address", cfg.HealthAddr)
	}
	slog.Info("HookRelay worker started", "concurrency", concurrency)
	serviceError := make(chan error, 1)
	go func() { serviceError <- service.Run(ctx) }()
	var runErr error
	select {
	case runErr = <-serviceError:
	case err := <-healthError:
		runErr = err
		stop()
		<-serviceError
	}
	if healthServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := healthServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("worker health shutdown failed", "error_kind", logsafe.Kind(err))
		}
		cancel()
	}
	if runErr != nil {
		slog.Error("worker stopped", "error_kind", logsafe.Kind(runErr))
		os.Exit(1)
	}
}
