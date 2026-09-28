package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hookrelay/internal/logsafe"
	"hookrelay/internal/postgres"
	"hookrelay/internal/receiver"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18080", "loopback listener address")
	keyID := flag.String("key-id", "demo/a-v1", "expected signing key ID")
	mode := flag.String("mode", "ok", "ok, error, slow, flaky, or drop-after-commit")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	secret := []byte(os.Getenv("DEMO_RECEIVER_SECRET"))
	databaseURL := os.Getenv("DATABASE_URL")
	containerDemo := os.Getenv("HOOKRELAY_CONTAINER_MODE") == "compose"
	if err != nil || host != "127.0.0.1" && host != "::1" && !(containerDemo && host == "0.0.0.0") || len(secret) < 32 || databaseURL == "" {
		slog.Error("loopback listener, DATABASE_URL, and 32-byte DEMO_RECEIVER_SECRET required")
		os.Exit(1)
	}
	if *mode != "ok" && *mode != "error" && *mode != "slow" && *mode != "flaky" && *mode != "drop-after-commit" {
		slog.Error("invalid demo mode")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		slog.Error("PostgreSQL connection failed", "error_kind", logsafe.Kind(err))
		os.Exit(1)
	}
	defer db.Close()
	handler := &receiver.Handler{
		Store:  postgres.ReceiverStore{DB: db},
		Secret: secret,
		KeyID:  *keyID,
		Mode:   *mode,
	}
	mux := http.NewServeMux()
	mux.Handle("POST /hook", handler)
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		checkCtx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := db.PingContext(checkCtx); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("demo receiver shutdown failed", "error_kind", logsafe.Kind(err))
		}
	}()
	slog.Info("demo receiver listening", "address", *listen, "mode", *mode)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("demo receiver stopped", "error_kind", logsafe.Kind(err))
		os.Exit(1)
	}
}
