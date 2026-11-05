// Package config loads application settings from environment variables.
package config

import (
	"fmt"
	"os"
)

// Config holds all application settings.
type Config struct {
	Env         string // dev | prod
	LogLevel    string // debug | info | warn | error
	HTTPAddr    string // listen address, e.g. ":8080"
	DatabaseURL string // Postgres DSN

	// Telegram
	TelegramToken         string // bot API token, required
	TelegramMode          string // polling | webhook
	PublicURL             string // public base URL, required in webhook mode
	TelegramWebhookSecret string // X-Telegram-Bot-Api-Secret-Token, required in webhook mode
}

// Load reads settings from env, applies defaults and validates them.
func Load() (Config, error) {
	cfg := Config{
		Env:         getenv("APP_ENV", "dev"),
		LogLevel:    getenv("LOG_LEVEL", "info"),
		HTTPAddr:    ":" + getenv("PORT", "8080"),
		DatabaseURL: getenv("DATABASE_URL", "postgres://most:most@localhost:5432/most?sslmode=disable"),

		TelegramToken:         os.Getenv("TELEGRAM_TOKEN"),
		TelegramMode:          getenv("TELEGRAM_MODE", "polling"),
		PublicURL:             os.Getenv("PUBLIC_URL"),
		TelegramWebhookSecret: os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	switch c.Env {
	case "dev", "prod":
	default:
		return fmt.Errorf("APP_ENV: unknown value %q", c.Env)
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL: unknown value %q", c.LogLevel)
	}

	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL: must not be empty")
	}

	if c.TelegramToken == "" {
		return fmt.Errorf("TELEGRAM_TOKEN: must not be empty")
	}

	switch c.TelegramMode {
	case "polling":
	case "webhook":
		if c.PublicURL == "" {
			return fmt.Errorf("PUBLIC_URL: required when TELEGRAM_MODE=webhook")
		}
		if c.TelegramWebhookSecret == "" {
			return fmt.Errorf("TELEGRAM_WEBHOOK_SECRET: required when TELEGRAM_MODE=webhook")
		}
	default:
		return fmt.Errorf("TELEGRAM_MODE: unknown value %q", c.TelegramMode)
	}

	return nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
