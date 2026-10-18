package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ankitnegi-dev/Sahayak/internal/config"
)

// clearEnv unsets every variable Load reads so each test starts from a known
// state. t.Setenv restores the original values on cleanup.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"APP_ENV", "HTTP_ADDR", "HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT",
		"HTTP_IDLE_TIMEOUT", "HTTP_SHUTDOWN_TIMEOUT",
		"DATABASE_URL", "DB_MAX_CONNS", "DB_MIN_CONNS",
		"REDIS_ADDR", "REDIS_PASSWORD", "REDIS_DB",
		"WHATSAPP_ACCESS_TOKEN", "WHATSAPP_VERIFY_TOKEN", "WHATSAPP_APP_SECRET",
		"WHATSAPP_PHONE_NUMBER_ID", "ANTHROPIC_API_KEY",
	} {
		t.Setenv(k, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/parentbot")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if cfg.Env != "development" {
		t.Errorf("Env = %q, want %q", cfg.Env, "development")
	}
	if cfg.HTTP.Addr != ":8080" {
		t.Errorf("HTTP.Addr = %q, want %q", cfg.HTTP.Addr, ":8080")
	}
	if cfg.Redis.Addr != "localhost:6379" {
		t.Errorf("Redis.Addr = %q, want %q", cfg.Redis.Addr, "localhost:6379")
	}
	if cfg.DB.MaxConns != 10 {
		t.Errorf("DB.MaxConns = %d, want 10", cfg.DB.MaxConns)
	}
	if cfg.HTTP.ReadTimeout.String() != "10s" {
		t.Errorf("HTTP.ReadTimeout = %v, want 10s", cfg.HTTP.ReadTimeout)
	}
	if cfg.IsProduction() {
		t.Error("IsProduction() = true, want false")
	}
}

func TestLoad_OverridesFromEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://db:5432/pb")
	t.Setenv("HTTP_ADDR", ":9999")
	t.Setenv("REDIS_DB", "3")
	t.Setenv("DB_MAX_CONNS", "42")
	t.Setenv("HTTP_READ_TIMEOUT", "45s")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"HTTP.Addr", cfg.HTTP.Addr, ":9999"},
		{"Redis.DB", cfg.Redis.DB, 3},
		{"DB.MaxConns", int(cfg.DB.MaxConns), 42},
		{"HTTP.ReadTimeout", cfg.HTTP.ReadTimeout.String(), "45s"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// A malformed duration or integer must not crash the process; it falls back
// to the safe default instead.
func TestLoad_InvalidValuesFallBackToDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/parentbot")
	t.Setenv("HTTP_READ_TIMEOUT", "not-a-duration")
	t.Setenv("DB_MAX_CONNS", "many")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.HTTP.ReadTimeout.String() != "10s" {
		t.Errorf("HTTP.ReadTimeout = %v, want 10s default", cfg.HTTP.ReadTimeout)
	}
	if cfg.DB.MaxConns != 10 {
		t.Errorf("DB.MaxConns = %d, want 10 default", cfg.DB.MaxConns)
	}
}

func TestLoad_MissingDatabaseURL(t *testing.T) {
	clearEnv(t)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want ErrMissing")
	}
	if !errors.Is(err, config.ErrMissing) {
		t.Errorf("error = %v, want ErrMissing", err)
	}
}

func TestLoad_ProductionRequiresAllSecrets(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/parentbot")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want missing-secret error")
	}

	for _, want := range []string{
		"WHATSAPP_ACCESS_TOKEN",
		"WHATSAPP_VERIFY_TOKEN",
		"WHATSAPP_APP_SECRET",
		"WHATSAPP_PHONE_NUMBER_ID",
		"ANTHROPIC_API_KEY",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestLoad_ProductionRejectsWeakVerifyToken(t *testing.T) {
	clearEnv(t)
	setProductionEnv(t)
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "short")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want weak verify token error")
	}
	if !strings.Contains(err.Error(), "WHATSAPP_VERIFY_TOKEN") {
		t.Errorf("error %q does not mention WHATSAPP_VERIFY_TOKEN", err)
	}
}

func TestLoad_ProductionRejectsDefaultAddr(t *testing.T) {
	clearEnv(t)
	setProductionEnv(t)
	t.Setenv("HTTP_ADDR", ":8080")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want default addr error")
	}
	if !strings.Contains(err.Error(), "HTTP_ADDR") {
		t.Errorf("error %q does not mention HTTP_ADDR", err)
	}
}

func TestLoad_ProductionSuccess(t *testing.T) {
	clearEnv(t)
	setProductionEnv(t)
	t.Setenv("HTTP_ADDR", ":8081")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !cfg.IsProduction() {
		t.Error("IsProduction() = false, want true")
	}
}

func setProductionEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/parentbot")
	t.Setenv("WHATSAPP_ACCESS_TOKEN", "token")
	t.Setenv("WHATSAPP_VERIFY_TOKEN", "a-sufficiently-long-verify-token")
	t.Setenv("WHATSAPP_APP_SECRET", "appsecret")
	t.Setenv("WHATSAPP_PHONE_NUMBER_ID", "1234567890")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
}
