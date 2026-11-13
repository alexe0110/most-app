// Package config loads application settings from environment variables.
package config

import (
	"fmt"
	"net/mail"
	"os"

	"github.com/alexe0110/most-app/internal/platform/gdrive"
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
	TelegramWebhookSecret string // X-Telegram-Bot-Api-Secret-Token, required in webhook mode
	TelegramBotUsername   string // bot @username, for the post-OAuth redirect

	// PublicURL is the base for the webhook and the OAuth callback. Defaults to
	// http://localhost:8080 in dev; required in prod.
	PublicURL string

	// Google OAuth
	GoogleClientID     string // required
	GoogleClientSecret string // required
	TokenEncKey        string // base64-encoded 32-byte key, required

	DriveRootFolder string // Drive root folder name, default gdrive.DefaultRootFolder

	ContactEmail string // public contact on the web pages; required in prod
}

// Load reads settings from env, applies defaults and validates them.
func Load() (Config, error) {
	env := getenv("APP_ENV", "dev")

	publicURL := os.Getenv("PUBLIC_URL")
	if publicURL == "" && env == "dev" {
		publicURL = "http://localhost:8080"
	}

	contactEmail := os.Getenv("CONTACT_EMAIL")
	if contactEmail == "" && env == "dev" {
		contactEmail = "contact@example.com"
	}

	cfg := Config{
		Env:         env,
		LogLevel:    getenv("LOG_LEVEL", "info"),
		HTTPAddr:    ":" + getenv("PORT", "8080"),
		DatabaseURL: getenv("DATABASE_URL", "postgres://most:most@localhost:5432/most?sslmode=disable"),

		DriveRootFolder: getenv("DRIVE_ROOT_FOLDER", gdrive.DefaultRootFolder),

		TelegramToken:         os.Getenv("TELEGRAM_TOKEN"),
		TelegramMode:          getenv("TELEGRAM_MODE", "polling"),
		TelegramWebhookSecret: os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
		TelegramBotUsername:   os.Getenv("TELEGRAM_BOT_USERNAME"),

		PublicURL: publicURL,

		ContactEmail: contactEmail,

		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		TokenEncKey:        os.Getenv("TOKEN_ENC_KEY"),
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
		if c.TelegramWebhookSecret == "" {
			return fmt.Errorf("TELEGRAM_WEBHOOK_SECRET: required when TELEGRAM_MODE=webhook")
		}
	default:
		return fmt.Errorf("TELEGRAM_MODE: unknown value %q", c.TelegramMode)
	}

	// PublicURL has a dev default; in prod it must be set explicitly.
	if c.Env == "prod" && c.PublicURL == "" {
		return fmt.Errorf("PUBLIC_URL: required in prod")
	}

	if c.Env == "prod" && c.ContactEmail == "" {
		return fmt.Errorf("CONTACT_EMAIL: required in prod")
	}
	// The address goes into a public mailto link, so a typo must fail the start.
	if c.ContactEmail != "" {
		if a, err := mail.ParseAddress(c.ContactEmail); err != nil || a.Address != c.ContactEmail {
			return fmt.Errorf("CONTACT_EMAIL: invalid address %q", c.ContactEmail)
		}
	}

	if c.GoogleClientID == "" {
		return fmt.Errorf("GOOGLE_CLIENT_ID: must not be empty")
	}
	if c.GoogleClientSecret == "" {
		return fmt.Errorf("GOOGLE_CLIENT_SECRET: must not be empty")
	}
	if c.TokenEncKey == "" {
		return fmt.Errorf("TOKEN_ENC_KEY: must not be empty")
	}

	return nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
