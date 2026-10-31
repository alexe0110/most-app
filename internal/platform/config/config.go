package config

import (
	"fmt"
	"os"
)

type Config struct {
	Env      string // dev | prod
	LogLevel string // debug | info | warn | error
	HTTPAddr string // listen address, e.g. ":8080"
}

func Load() (Config, error) {
	cfg := Config{
		Env:      getenv("APP_ENV", "dev"),
		LogLevel: getenv("LOG_LEVEL", "info"),
		HTTPAddr: ":" + getenv("PORT", "8080"),
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

	return nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
