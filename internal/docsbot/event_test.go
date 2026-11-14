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

// eventScenarios are the use cases that record a product event, each with the
// exact events it must leave behind.
func eventScenarios() []struct {
	name string
	run  func(t *testing.T, svc *docsbot.Service, repo *docsbottest.FakeRepo) error
	want []docsbot.Event
} {
	pending := func(repo *docsbottest.FakeRepo, unique string, group string) docsbot.Document {
		return repo.Seed(docsbot.Document{
			UserID: user, Status: docsbot.StatusPending, Kind: docsbot.KindPhoto, MediaGroupID: group,
			Title: "Секретное название", TgFileID: "file-id-secret", TgFileUniqueID: unique,
		})
	}
	saved := docsbot.Event{
		User: user, Name: docsbot.EventDocSaved,
		Props: docsbot.EventProps{Category: docsbot.CatPassport, Kind: docsbot.KindPhoto},
	}
	return []struct {
		name string
		run  func(t *testing.T, svc *docsbot.Service, repo *docsbottest.FakeRepo) error
		want []docsbot.Event
	}{
		{"doc saved once per document", func(t *testing.T, svc *docsbot.Service, repo *docsbottest.FakeRepo) error {
			doc := pending(repo, "u1", "")
			_, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)
			if err != nil {
				return err
			}
			_, err = svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport) // repeated tap
			return err
		}, []docsbot.Event{saved}},
		{"album saves every document", func(t *testing.T, svc *docsbot.Service, repo *docsbottest.FakeRepo) error {
			first := pending(repo, "a1", "alb")
			pending(repo, "a2", "alb")
			_, err := svc.ChooseCategoryGroup(t.Context(), user, first.ID, docsbot.CatPassport)
			return err
		}, []docsbot.Event{saved, saved}},
		{"derived copy is a saved document", func(t *testing.T, svc *docsbot.Service, repo *docsbottest.FakeRepo) error {
			src := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatPassport, Title: "Паспорт"})
			_, err := svc.SaveDerived(t.Context(), user, src.ID, docsbot.IncomingFile{
				Kind: docsbot.KindDocument, FileID: "f", FileUniqueID: "d1", Title: "x",
			}, " (PDF)")
			return err
		}, []docsbot.Event{{
			User: user, Name: docsbot.EventDocSaved,
			Props: docsbot.EventProps{Category: docsbot.CatPassport, Kind: docsbot.KindDocument},
		}}},
		{"card opened", func(t *testing.T, svc *docsbot.Service, repo *docsbottest.FakeRepo) error {
			doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "Секретное название"})
			_, err := svc.OpenCard(t.Context(), user, doc.ID)
			return err
		}, []docsbot.Event{{User: user, Name: docsbot.EventDocOpened}}},
		{"card of a missing document is not an open", func(t *testing.T, svc *docsbot.Service, _ *docsbottest.FakeRepo) error {
			_, err := svc.OpenCard(t.Context(), user, "missing")
			require.ErrorIs(t, err, docsbot.ErrNotFound)
			return nil
		}, nil},
		{"drive connected", func(t *testing.T, svc *docsbot.Service, _ *docsbottest.FakeRepo) error {
			return svc.OnDriveConnected(t.Context(), user)
		}, []docsbot.Event{{User: user, Name: docsbot.EventDriveConnected}}},
		{"drive disconnected", func(t *testing.T, svc *docsbot.Service, _ *docsbottest.FakeRepo) error {
			return svc.Disconnect(t.Context(), user)
		}, []docsbot.Event{{User: user, Name: docsbot.EventDriveDisconnected}}},
		{"export", func(t *testing.T, svc *docsbot.Service, _ *docsbottest.FakeRepo) error {
			_, err := svc.ExportAll(t.Context(), user)
			return err
		}, []docsbot.Event{{User: user, Name: docsbot.EventExport}}},
		{"data deleted is anonymous", func(t *testing.T, svc *docsbot.Service, _ *docsbottest.FakeRepo) error {
			return svc.DeleteAllData(t.Context(), user)
		}, []docsbot.Event{{Name: docsbot.EventDataDeleted}}},
	}
}

func TestService_recordsEvents(t *testing.T) {
	for _, tt := range eventScenarios() {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			svc, _ := edgeService(repo, docsbottest.FakeDrive{})

			require.NoError(t, tt.run(t, svc, repo))

			require.Equal(t, tt.want, repo.Events)
		})
	}
}

func TestService_eventWriteFailureChangesNothing(t *testing.T) {
	for _, tt := range eventScenarios() {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			repo.EventsErr = errors.New("db down")
			svc, _ := edgeService(repo, docsbottest.FakeDrive{})

			require.NoError(t, tt.run(t, svc, repo), "the scenario succeeds without its event")

			require.Empty(t, repo.Events)
		})
	}
}

func TestService_Start(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name     string
		payloads []string
		want     []docsbot.EventProps
	}{
		{"first start with a source", []string{"ad_1"}, []docsbot.EventProps{{Source: "ad_1", New: &yes}}},
		{"no payload", []string{""}, []docsbot.EventProps{{New: &yes}}},
		{"invalid payload is not a source", []string{"Ad 1; drop"}, []docsbot.EventProps{{New: &yes}}},
		{"second start is not new", []string{"ad_1", "ad_2"}, []docsbot.EventProps{
			{Source: "ad_1", New: &yes}, {Source: "ad_2", New: &no},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			svc, _ := edgeService(repo, docsbottest.FakeDrive{})

			for _, p := range tt.payloads {
				svc.Start(t.Context(), user, p)
			}

			var got []docsbot.EventProps
			for _, e := range repo.Events {
				require.Equal(t, docsbot.EventStart, e.Name)
				require.Equal(t, user, e.User)
				got = append(got, e.Props)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

func TestService_Start_writeFailureIsLogged(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	repo.EventsErr = errors.New("db down")
	var logs bytes.Buffer
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{},
		&docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo},
		docsbot.WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))

	svc.Start(t.Context(), user, "ad_1")

	require.Empty(t, repo.Events)
	require.Contains(t, logs.String(), `"event":"start"`)
	require.NotContains(t, logs.String(), "ad_1")
}
