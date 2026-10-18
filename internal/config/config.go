// Package config loads and validates all runtime configuration from
// environment variables. It follows the 12-factor pattern: nothing is read
// from files at runtime, and secrets never touch the repository.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrMissing is returned when a required environment variable is not set.
var ErrMissing = errors.New("config: required variable missing")

// Config is the fully validated application configuration.
// Every field is populated by Load; no defaults are applied lazily at use sites.
type Config struct {
	Env  string
	HTTP HTTPConfig

	DB      DBConfig
	Redis   RedisConfig
	Secrets Secrets
}

// HTTPConfig configures the public webhook server.
type HTTPConfig struct {
	Addr         string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
	// ShutdownTimeout bounds graceful shutdown on SIGINT/SIGTERM.
	ShutdownTimeout time.Duration
}

// DBConfig configures the Postgres connection pool.
type DBConfig struct {
	URL      string
	MaxConns int32
	MinConns int32
}

// RedisConfig configures the asynq broker connection.
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

// Secrets holds every credential. Nothing in this struct is ever logged.
type Secrets struct {
	WhatsAppAccessToken string
	WhatsAppVerifyToken string
	WhatsAppAppSecret   string
	WhatsAppPhoneID     string
	AnthropicAPIKey     string
}

// Load reads configuration from the process environment and validates it.
//
// Required variables are listed in requiredVars. In the "development"
// environment the WhatsApp and Anthropic credentials are optional so that
// tests and local tooling can run without them; any other environment
// requires them to be present.
func Load() (Config, error) {
	env := getEnv("APP_ENV", "development")

	cfg := Config{
		Env: env,
		HTTP: HTTPConfig{
			Addr:            getEnv("HTTP_ADDR", ":8080"),
			ReadTimeout:     getDuration("HTTP_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:    getDuration("HTTP_WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:     getDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
			ShutdownTimeout: getDuration("HTTP_SHUTDOWN_TIMEOUT", 20*time.Second),
		},
		DB: DBConfig{
			URL:      os.Getenv("DATABASE_URL"),
			MaxConns: int32(getInt("DB_MAX_CONNS", 10)),
			MinConns: int32(getInt("DB_MIN_CONNS", 2)),
		},
		Redis: RedisConfig{
			Addr:     getEnv("REDIS_ADDR", "localhost:6379"),
			Password: os.Getenv("REDIS_PASSWORD"),
			DB:       getInt("REDIS_DB", 0),
		},
		Secrets: Secrets{
			WhatsAppAccessToken: os.Getenv("WHATSAPP_ACCESS_TOKEN"),
			WhatsAppVerifyToken: os.Getenv("WHATSAPP_VERIFY_TOKEN"),
			WhatsAppAppSecret:   os.Getenv("WHATSAPP_APP_SECRET"),
			WhatsAppPhoneID:     os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
			AnthropicAPIKey:     os.Getenv("ANTHROPIC_API_KEY"),
		},
	}

	if err := cfg.validate(env); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// IsProduction reports whether the app runs in the production environment.
func (c Config) IsProduction() bool { return c.Env == "production" }

func (c Config) validate(env string) error {
	var missing []string

	for _, v := range requiredVars(env) {
		if os.Getenv(v) == "" {
			missing = append(missing, v)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrMissing, strings.Join(missing, ", "))
	}

	if env == "production" {
		// A weak verify token is an authentication bypass, so it is a hard
		// failure in production rather than a warning.
		if len(c.Secrets.WhatsAppVerifyToken) < 16 {
			return errors.New("config: WHATSAPP_VERIFY_TOKEN must be at least 16 characters in production")
		}
		if c.HTTP.Addr == ":8080" {
			return errors.New("config: HTTP_ADDR must not be the default in production")
		}
	}
	return nil
}

// requiredVars returns the variables that must be set for the given
// environment. Credentials are only mandatory in production because unit and
// contract tests run without them.
func requiredVars(env string) []string {
	base := []string{"DATABASE_URL"}
	if env == "production" {
		return append(base,
			"WHATSAPP_ACCESS_TOKEN",
			"WHATSAPP_VERIFY_TOKEN",
			"WHATSAPP_APP_SECRET",
			"WHATSAPP_PHONE_NUMBER_ID",
			"ANTHROPIC_API_KEY",
		)
	}
	return base
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
