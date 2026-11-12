//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/jobs"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
	"github.com/alexe0110/most-app/internal/platform/gdrive"
	pjobs "github.com/alexe0110/most-app/internal/platform/jobs"
)

var (
	qName   = regexp.MustCompile(`name='([^']*)'`)
	qParent = regexp.MustCompile(`'([^']*)' in parents`)
)

type folderRec struct{ id, name, parent string }

// statefulDrive is an in-memory Drive that records how many folders were
// created per name.
type statefulDrive struct {
	t       *testing.T
	mu      sync.Mutex
	folders map[string]folderRec
	created map[string]int
	nextID  int
}

func (d *statefulDrive) createdCount(name string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.created[name]
}

func (d *statefulDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/drive/v3/files/"):
		id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
		writeFake(w, map[string]any{"id": id, "trashed": false})
	case r.Method == http.MethodGet && r.URL.Path == "/drive/v3/files":
		q := r.URL.Query().Get("q")
		var name, parent string
		if m := qName.FindStringSubmatch(q); m != nil {
			name = m[1]
		}
		if m := qParent.FindStringSubmatch(q); m != nil {
			parent = m[1]
		}
		files := []map[string]string{}
		for _, f := range d.folders {
			if f.name == name && f.parent == parent {
				files = append(files, map[string]string{"id": f.id, "name": f.name})
			}
		}
		writeFake(w, map[string]any{"files": files})
	case r.Method == http.MethodPost && r.URL.Path == "/drive/v3/files":
		var in struct {
			Name    string   `json:"name"`
			Parents []string `json:"parents"`
		}
		require.NoError(d.t, json.NewDecoder(r.Body).Decode(&in))
		require.Len(d.t, in.Parents, 1)
		d.nextID++
		id := fmt.Sprintf("folder-%d", d.nextID)
		d.folders[id] = folderRec{id: id, name: in.Name, parent: in.Parents[0]}
		d.created[in.Name]++
		writeFake(w, map[string]string{"id": id})
	case r.Method == http.MethodPost && r.URL.Path == "/upload/drive/v3/files":
		d.nextID++
		writeFake(w, map[string]string{"id": fmt.Sprintf("file-%d", d.nextID), "webViewLink": "https://drive/x"})
	default:
		d.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}
}

func writeFake(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type plainClients struct{}

func (plainClients) Client(context.Context, string) (*http.Client, error) {
	return http.DefaultClient, nil
}

// raceDrive adapts gdrive to docsbot.Drive, like driveBridge in cmd/docsbot.
type raceDrive struct{ gd *gdrive.Service }

func (raceDrive) Connected(context.Context, docsbot.UserID) (bool, error) { return true, nil }
func (d raceDrive) Upload(ctx context.Context, user docsbot.UserID, in docsbot.DriveUpload) (docsbot.DriveFile, error) {
	f, err := d.gd.Upload(ctx, string(user), gdrive.Upload{
		CacheKey:   string(in.Category),
		FolderName: in.Category.FolderName(),
		Name:       in.Name,
		MIME:       in.MIME,
		Data:       in.Data,
		DocID:      in.DocID,
	})
	if err != nil {
		return docsbot.DriveFile{}, err
	}
	return docsbot.DriveFile{ID: f.ID, Link: f.Link}, nil
}
func (raceDrive) Rename(context.Context, docsbot.UserID, string, string) error { return nil }

func (raceDrive) Move(context.Context, docsbot.UserID, string, docsbot.Category) error { return nil }

func (raceDrive) Trash(context.Context, docsbot.UserID, string) error { return nil }

func (raceDrive) FileExists(context.Context, docsbot.UserID, string) (bool, error) {
	return true, nil
}

func (raceDrive) Replace(context.Context, docsbot.UserID, string, string, []byte) error { return nil }

func (raceDrive) Revoke(context.Context, docsbot.UserID) error { return nil }

func (raceDrive) Disconnect(context.Context, docsbot.UserID) error { return nil }

func TestUploadDocument_batchCreatesFoldersOnce(t *testing.T) {
	const total = 10
	ctx := t.Context()
	pool := dbtest.New(t)
	repo := postgres.NewRepo(pool)
	log := slog.New(slog.DiscardHandler)

	fd := &statefulDrive{t: t, folders: map[string]folderRec{}, created: map[string]int{}}
	srv := httptest.NewServer(fd)
	t.Cleanup(srv.Close)
	gd := gdrive.New(plainClients{}, repo, gdrive.WithBaseURL(srv.URL))

	var uid string
	require.NoError(t, pool.QueryRow(ctx, `insert into users (tg_user_id) values (1) returning id::text`).Scan(&uid))
	user := docsbot.UserID(uid)

	// 6 distinct categories, then 4 more in already used ones.
	used := docsbot.AllCategories[:6]
	ids := make([]docsbot.DocumentID, 0, total)
	for i := range total {
		cat := used[i%len(used)]
		var did string
		require.NoError(t, pool.QueryRow(ctx,
			`insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id)
			 values ($1::uuid, 'saved', 'document', $2, 'title', 'file', $3) returning id::text`,
			uid, string(cat), fmt.Sprintf("uniq-%d", i)).Scan(&did))
		ids = append(ids, docsbot.DocumentID(did))
	}

	workers := river.NewWorkers()
	bind := jobs.RegisterUploadDocument(workers, log)
	queue, err := pjobs.New(pool, log, workers, map[string]river.QueueConfig{jobs.UploadQueue: {MaxWorkers: 1}})
	require.NoError(t, err)
	enq := jobs.NewEnqueuer(repo, queue)
	svc := docsbot.NewService(repo, raceDrive{gd: gd}, fakeFiles{}, enq, repo, repo, enq)
	bind(svc, &spyNotifier{})

	require.NoError(t, queue.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, queue.Stop(stopCtx))
	})

	for _, id := range ids {
		require.NoError(t, enq.EnqueueUpload(ctx, user, id))
	}

	require.Eventually(t, func() bool {
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`select count(*) from documents where user_id = $1::uuid and backup_status = 'backed_up'`, uid).Scan(&n))
		return n == total
	}, 30*time.Second, 100*time.Millisecond)

	require.Equal(t, 1, fd.createdCount("Most Docs"))
	for _, cat := range used {
		require.Equal(t, 1, fd.createdCount(cat.FolderName()), string(cat))
	}
}
