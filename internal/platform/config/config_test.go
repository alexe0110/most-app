package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	// valid is the minimal set of env vars that passes validation; cases extend
	// or override it.
	valid := map[string]string{
		"TELEGRAM_TOKEN":       "t",
		"GOOGLE_CLIENT_ID":     "gid",
		"GOOGLE_CLIENT_SECRET": "gsecret",
		"TOKEN_ENC_KEY":        "key",
	}

	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr string
	}{
		{
			name: "defaults",
			env:  valid,
			want: Config{
				Env: "dev", LogLevel: "info", HTTPAddr: ":8080",
				DatabaseURL:   "postgres://most:most@localhost:5432/most?sslmode=disable",
				TelegramToken: "t", TelegramMode: "polling",
				PublicURL:          "http://localhost:8080",
				GoogleClientID:     "gid",
				GoogleClientSecret: "gsecret",
				TokenEncKey:        "key",
			},
		},
		{
			name: "custom values",
			env: map[string]string{
				"APP_ENV": "prod", "LOG_LEVEL": "debug", "PORT": "3000",
				"DATABASE_URL":   "postgres://u:p@db:5432/x",
				"TELEGRAM_TOKEN": "tok", "TELEGRAM_MODE": "webhook",
				"TELEGRAM_WEBHOOK_SECRET": "sec", "TELEGRAM_BOT_USERNAME": "most_files_bot",
				"PUBLIC_URL":       "https://most.example",
				"GOOGLE_CLIENT_ID": "gid", "GOOGLE_CLIENT_SECRET": "gsecret", "TOKEN_ENC_KEY": "key",
			},
			want: Config{
				Env: "prod", LogLevel: "debug", HTTPAddr: ":3000", DatabaseURL: "postgres://u:p@db:5432/x",
				TelegramToken: "tok", TelegramMode: "webhook",
				TelegramWebhookSecret: "sec", TelegramBotUsername: "most_files_bot",
				PublicURL:          "https://most.example",
				GoogleClientID:     "gid",
				GoogleClientSecret: "gsecret",
				TokenEncKey:        "key",
			},
		},
		{
			name:    "unknown env",
			env:     map[string]string{"APP_ENV": "stage"},
			wantErr: "APP_ENV",
		},
		{
			name:    "unknown log level",
			env:     map[string]string{"LOG_LEVEL": "trace"},
			wantErr: "LOG_LEVEL",
		},
		{
			name:    "missing telegram token",
			env:     map[string]string{},
			wantErr: "TELEGRAM_TOKEN",
		},
		{
			name:    "unknown telegram mode",
			env:     map[string]string{"TELEGRAM_TOKEN": "t", "TELEGRAM_MODE": "bogus"},
			wantErr: "TELEGRAM_MODE",
		},
		{
			name:    "webhook without secret",
			env:     map[string]string{"TELEGRAM_TOKEN": "t", "TELEGRAM_MODE": "webhook"},
			wantErr: "TELEGRAM_WEBHOOK_SECRET",
		},
		{
			name:    "prod without public url",
			env:     map[string]string{"APP_ENV": "prod", "TELEGRAM_TOKEN": "t"},
			wantErr: "PUBLIC_URL",
		},
		{
			name:    "missing google client id",
			env:     map[string]string{"TELEGRAM_TOKEN": "t"},
			wantErr: "GOOGLE_CLIENT_ID",
		},
		{
			name:    "missing google client secret",
			env:     map[string]string{"TELEGRAM_TOKEN": "t", "GOOGLE_CLIENT_ID": "gid"},
			wantErr: "GOOGLE_CLIENT_SECRET",
		},
		{
			name: "missing token enc key",
			env: map[string]string{
				"TELEGRAM_TOKEN": "t", "GOOGLE_CLIENT_ID": "gid", "GOOGLE_CLIENT_SECRET": "gsecret",
			},
			wantErr: "TOKEN_ENC_KEY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{
				"APP_ENV", "LOG_LEVEL", "PORT", "DATABASE_URL",
				"TELEGRAM_TOKEN", "TELEGRAM_MODE", "TELEGRAM_WEBHOOK_SECRET", "TELEGRAM_BOT_USERNAME",
				"PUBLIC_URL", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "TOKEN_ENC_KEY",
			} {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, err := Load()

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
