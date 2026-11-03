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
			env:  map[string]string{},
			want: Config{
				Env: "dev", LogLevel: "info", HTTPAddr: ":8080",
				DatabaseURL: "postgres://most:most@localhost:5432/most?sslmode=disable",
			},
		},
		{
			name: "custom values",
			env: map[string]string{
				"APP_ENV": "prod", "LOG_LEVEL": "debug", "PORT": "3000",
				"DATABASE_URL": "postgres://u:p@db:5432/x",
			},
			want: Config{Env: "prod", LogLevel: "debug", HTTPAddr: ":3000", DatabaseURL: "postgres://u:p@db:5432/x"},
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"APP_ENV", "LOG_LEVEL", "PORT", "DATABASE_URL"} {
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
