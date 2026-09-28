package config

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"

	"hookrelay/internal/keyring"
	"hookrelay/internal/targetpolicy"
)

type Config struct {
	DatabaseURL   string
	ProducerToken string
	AdminToken    string
	ListenAddr    string
	TargetProfile targetpolicy.Profile
	ExternalKeys  map[string][]byte
}

type WorkerConfig struct {
	DatabaseURL   string
	HealthAddr    string
	TargetProfile targetpolicy.Profile
	ExternalKeys  map[string][]byte
	ComposeDemo   bool
}

func loadWorkerSettings() (WorkerConfig, error) {
	cfg := WorkerConfig{
		DatabaseURL:   strings.TrimSpace(os.Getenv("DATABASE_URL")),
		HealthAddr:    strings.TrimSpace(os.Getenv("WORKER_HEALTH_ADDR")),
		TargetProfile: targetpolicy.Profile(os.Getenv("TARGET_PROFILE")),
		ComposeDemo:   os.Getenv("HOOKRELAY_CONTAINER_MODE") == "compose",
	}
	if mode := os.Getenv("HOOKRELAY_CONTAINER_MODE"); mode != "" && mode != "compose" {
		return WorkerConfig{}, errors.New("HOOKRELAY_CONTAINER_MODE must be compose when set")
	}
	if cfg.DatabaseURL == "" {
		return WorkerConfig{}, errors.New("DATABASE_URL is required")
	}
	if cfg.TargetProfile == "" {
		cfg.TargetProfile = targetpolicy.ProfilePublic
	}
	if cfg.TargetProfile != targetpolicy.ProfilePublic && cfg.TargetProfile != targetpolicy.ProfileDemo {
		return WorkerConfig{}, errors.New("TARGET_PROFILE must be public or demo")
	}
	if cfg.HealthAddr != "" && !loopbackAddress(cfg.HealthAddr) {
		return WorkerConfig{}, errors.New("WORKER_HEALTH_ADDR must be a loopback host and port")
	}
	keys, err := keyring.Load(os.Getenv("EXTERNAL_KEY_DIR"))
	if err != nil {
		return WorkerConfig{}, err
	}
	cfg.ExternalKeys = keys
	return cfg, nil
}

func LoadWorker() (WorkerConfig, error) { return loadWorkerSettings() }

func Load() (Config, error) {
	worker, err := loadWorkerSettings()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		DatabaseURL:   worker.DatabaseURL,
		ProducerToken: os.Getenv("PRODUCER_TOKEN"),
		AdminToken:    os.Getenv("ADMIN_TOKEN"),
		ListenAddr:    os.Getenv("LISTEN_ADDR"),
		TargetProfile: worker.TargetProfile,
		ExternalKeys:  worker.ExternalKeys,
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:8080"
	}
	if len(cfg.ProducerToken) < 32 {
		return Config{}, errors.New("PRODUCER_TOKEN must have at least 32 bytes")
	}
	if len(cfg.AdminToken) < 32 {
		return Config{}, errors.New("ADMIN_TOKEN must have at least 32 bytes")
	}
	if cfg.AdminToken == cfg.ProducerToken {
		return Config{}, errors.New("ADMIN_TOKEN and PRODUCER_TOKEN must differ")
	}
	if !loopbackAddress(cfg.ListenAddr) && !(worker.ComposeDemo && cfg.ListenAddr == "0.0.0.0:8080") {
		return Config{}, errors.New("LISTEN_ADDR must be a loopback host and port in v0.1")
	}
	return cfg, nil
}

func loopbackAddress(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return false
	}
	number, err := strconv.Atoi(port)
	return err == nil && number >= 1 && number <= 65535
}
