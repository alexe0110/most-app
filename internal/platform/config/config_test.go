package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr string
	}{
		{
			name: "defaults",
			env:  map[string]string{"TELEGRAM_TOKEN": "t"},
			want: Config{
				Env: "dev", LogLevel: "info", HTTPAddr: ":8080",
				DatabaseURL:   "postgres://most:most@localhost:5432/most?sslmode=disable",
				TelegramToken: "t", TelegramMode: "polling",
			},
		},
		{
			name: "custom values",
			env: map[string]string{
				"APP_ENV": "prod", "LOG_LEVEL": "debug", "PORT": "3000",
				"DATABASE_URL":   "postgres://u:p@db:5432/x",
				"TELEGRAM_TOKEN": "tok", "TELEGRAM_MODE": "webhook",
				"PUBLIC_URL": "https://most.example", "TELEGRAM_WEBHOOK_SECRET": "sec",
			},
			want: Config{
				Env: "prod", LogLevel: "debug", HTTPAddr: ":3000", DatabaseURL: "postgres://u:p@db:5432/x",
				TelegramToken: "tok", TelegramMode: "webhook",
				PublicURL: "https://most.example", TelegramWebhookSecret: "sec",
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
			name:    "webhook without public url",
			env:     map[string]string{"TELEGRAM_TOKEN": "t", "TELEGRAM_MODE": "webhook"},
			wantErr: "PUBLIC_URL",
		},
		{
			name: "webhook without secret",
			env: map[string]string{
				"TELEGRAM_TOKEN": "t", "TELEGRAM_MODE": "webhook", "PUBLIC_URL": "https://x",
			},
			wantErr: "TELEGRAM_WEBHOOK_SECRET",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{
				"APP_ENV", "LOG_LEVEL", "PORT", "DATABASE_URL",
				"TELEGRAM_TOKEN", "TELEGRAM_MODE", "PUBLIC_URL", "TELEGRAM_WEBHOOK_SECRET",
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
