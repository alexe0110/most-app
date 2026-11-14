package docsbot_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
)

func TestParseSource(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
		ok      bool
	}{
		{"letters", "channel", "channel", true},
		{"digits and underscore", "ad_2026_11", "ad_2026_11", true},
		{"max length", strings.Repeat("a", 32), strings.Repeat("a", 32), true},
		{"empty", "", "", false},
		{"too long", strings.Repeat("a", 33), "", false},
		{"upper case", "Channel", "", false},
		{"dash", "ad-1", "", false},
		{"space", "ad 1", "", false},
		{"cyrillic", "канал", "", false},
		{"sql", "a';--", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := docsbot.ParseSource(tt.payload)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestService_Track(t *testing.T) {
	tests := []struct {
		name      string
		eventsErr error
		want      []docsbot.Event
		wantLog   bool
	}{
		{"recorded", nil, []docsbot.Event{{
			User: user, Name: docsbot.EventConvertDone, Props: docsbot.EventProps{Operation: docsbot.ConvertPhotoPDF},
		}}, false},
		{"write error is only logged", errors.New("db down"), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			repo.EventsErr = tt.eventsErr
			var logs bytes.Buffer
			svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{},
				&docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo},
				docsbot.WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))

			svc.Track(t.Context(), user, docsbot.EventConvertDone, docsbot.EventProps{Operation: docsbot.ConvertPhotoPDF})

			require.Equal(t, tt.want, repo.Events)
			if !tt.wantLog {
				require.Empty(t, logs.String())
				return
			}
			require.Contains(t, logs.String(), `"level":"WARN"`)
			require.Contains(t, logs.String(), `"event":"convert_done"`)
			require.NotContains(t, logs.String(), "photo_pdf", "props are not logged")
		})
	}
}

func TestService_Track_withoutEventsPort(t *testing.T) {
	svc := docsbot.NewService(docsbot.Ports{})
	require.NotPanics(t, func() {
		svc.Track(t.Context(), user, docsbot.EventSearch, docsbot.EventProps{})
	})
}
