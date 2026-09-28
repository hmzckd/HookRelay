package config

import (
	"strings"
	"testing"

	"hookrelay/internal/targetpolicy"
)

func TestLoadRequiresDistinctAdminAuthority(t *testing.T) {
	producer := strings.Repeat("p", 32)
	admin := strings.Repeat("a", 32)
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("LISTEN_ADDR", "127.0.0.1:8080")
	t.Setenv("PRODUCER_TOKEN", producer)
	t.Setenv("ADMIN_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("missing admin token accepted")
	}
	t.Setenv("ADMIN_TOKEN", producer)
	if _, err := Load(); err == nil {
		t.Fatal("shared producer/admin token accepted")
	}
	t.Setenv("ADMIN_TOKEN", admin)
	t.Setenv("TARGET_PROFILE", "")
	if cfg, err := Load(); err != nil || cfg.AdminToken != admin || cfg.TargetProfile != targetpolicy.ProfilePublic {
		t.Fatalf("distinct admin token rejected: %v", err)
	}
	t.Setenv("TARGET_PROFILE", "demo")
	if cfg, err := Load(); err != nil || cfg.TargetProfile != targetpolicy.ProfileDemo {
		t.Fatalf("explicit demo profile rejected: %v", err)
	}
}

func TestWorkerDoesNotRequireAPIAuthorityTokens(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("PRODUCER_TOKEN", "")
	t.Setenv("ADMIN_TOKEN", "")
	t.Setenv("TARGET_PROFILE", "public")
	t.Setenv("EXTERNAL_KEY_DIR", "")
	if cfg, err := LoadWorker(); err != nil || cfg.TargetProfile != targetpolicy.ProfilePublic {
		t.Fatalf("worker required API tokens: %v", err)
	}
}

func TestWorkerHealthListenerIsLoopbackOnly(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("TARGET_PROFILE", "public")
	t.Setenv("EXTERNAL_KEY_DIR", "")
	for _, address := range []string{"0.0.0.0:8082", "example.com:8082", "127.0.0.1:0", "127.0.0.1:bad"} {
		t.Setenv("WORKER_HEALTH_ADDR", address)
		if _, err := LoadWorker(); err == nil {
			t.Fatalf("unsafe health address accepted: %s", address)
		}
	}
	t.Setenv("WORKER_HEALTH_ADDR", "127.0.0.1:8082")
	if cfg, err := LoadWorker(); err != nil || cfg.HealthAddr != "127.0.0.1:8082" {
		t.Fatalf("valid health listener rejected: cfg=%+v err=%v", cfg, err)
	}
}

func TestContainerAPIBindRequiresExplicitMode(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("PRODUCER_TOKEN", strings.Repeat("p", 32))
	t.Setenv("ADMIN_TOKEN", strings.Repeat("a", 32))
	t.Setenv("TARGET_PROFILE", "demo")
	t.Setenv("EXTERNAL_KEY_DIR", "")
	t.Setenv("LISTEN_ADDR", "0.0.0.0:8080")
	t.Setenv("HOOKRELAY_CONTAINER_MODE", "")
	if _, err := Load(); err == nil {
		t.Fatal("wildcard API bind accepted outside container mode")
	}
	t.Setenv("HOOKRELAY_CONTAINER_MODE", "compose")
	if cfg, err := Load(); err != nil || cfg.ListenAddr != "0.0.0.0:8080" {
		t.Fatalf("explicit compose bind rejected: cfg=%+v err=%v", cfg, err)
	}
	if worker, err := LoadWorker(); err != nil || !worker.ComposeDemo {
		t.Fatalf("compose demo routing disabled: cfg=%+v err=%v", worker, err)
	}
	t.Setenv("HOOKRELAY_CONTAINER_MODE", "unexpected")
	if _, err := Load(); err == nil {
		t.Fatal("unknown container mode accepted")
	}
}
