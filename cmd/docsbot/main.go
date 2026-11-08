// Main package
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-telegram/bot"
	"github.com/riverqueue/river"

	"github.com/alexe0110/most-app/internal/docsbot"
	docjobs "github.com/alexe0110/most-app/internal/docsbot/jobs"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/config"
	"github.com/alexe0110/most-app/internal/platform/db"
	"github.com/alexe0110/most-app/internal/platform/gdrive"
	"github.com/alexe0110/most-app/internal/platform/jobs"
	"github.com/alexe0110/most-app/internal/platform/logging"
	"github.com/alexe0110/most-app/internal/platform/oauth"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer func() {
		fmt.Println("стопаю сервис")
		fmt.Println("пу пу пу")
		time.Sleep(2 * time.Second)
		stop()
	}()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.New(cfg.LogLevel)

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := jobs.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate river: %w", err)
	}
	log.Info("migrations applied")

	workers := river.NewWorkers()
	purge := jobs.RegisterPurgeUsers(workers, pool, log)
	bindUpload := docjobs.RegisterUploadDocument(workers, log)
	queue, err := jobs.New(pool, log, workers, map[string]river.QueueConfig{docjobs.UploadQueue: {MaxWorkers: 1}}, purge) // started below, once wired
	if err != nil {
		return err
	}

	repo := postgres.NewRepo(pool)
	enq := docjobs.NewEnqueuer(repo, queue)
	files := &tgFiles{} // bot is set once it is built

	// oauth.Service and the bot each need the other (the bot shows the consent
	// link; oauth notifies the bot when Drive connects). tgbot is filled just
	// below, and both closures run only after startup, so the cycle is safe.
	var (
		tgbot *tg.Bot
		svc   *docsbot.Service
	)
	oauthSvc, err := oauth.New(oauth.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		TokenEncKey:  cfg.TokenEncKey,
		PublicURL:    cfg.PublicURL,
		BotURL:       botURL(cfg.TelegramBotUsername),
	}, repo, func(ctx context.Context, userID string) error {
		return telegram.NotifyConnected(tgbot, repo, svc, log)(ctx, userID)
	}, log)
	if err != nil {
		return fmt.Errorf("new oauth service: %w", err)
	}

	gdriveSvc := gdrive.New(gdriveClients{oauth: oauthSvc}, repo, gdrive.WithLogger(log))
	driveAdapter := driveBridge{oauth: oauthSvc, gd: gdriveSvc}
	svc = docsbot.NewService(repo, driveAdapter, files, enq, repo, repo, docsbot.WithCategoryNamer(telegram.CategoryLabel))

	tgbot, err = tg.New(tg.Config{
		Token:         cfg.TelegramToken,
		Mode:          tg.Mode(cfg.TelegramMode),
		PublicURL:     cfg.PublicURL,
		WebhookSecret: cfg.TelegramWebhookSecret,
	}, repo, log, func(b *bot.Bot) { telegram.Register(b, log, svc, oauthSvc) })
	if err != nil {
		return err
	}
	files.bot = tgbot
	notifier := telegram.NewNotifier(tgbot, repo, oauthSvc, svc, log)
	bindUpload(svc, notifier)
	if err := tgbot.SetCommands(ctx, telegram.Commands()); err != nil {
		log.Error("set bot commands", "err", err) // not fatal
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET "+oauth.CallbackPath, oauthSvc.Callback())
	tgbot.RegisterRoutes(mux) // webhook route; no-op in polling mode
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux}

	if err := queue.Start(ctx); err != nil {
		return fmt.Errorf("start queue: %w", err)
	}
	log.Info("job queue started")

	go func() {
		log.Info("server started", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			stop()
		}
	}()

	botDone := make(chan struct{})
	go func() {
		defer close(botDone)
		if err := tgbot.Run(ctx); err != nil {
			log.Error("telegram bot failed", "err", err)
			stop()
		}
	}()
	log.Info("telegram bot started", "mode", cfg.TelegramMode)

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Stop intake first, then drain the workers, both within the same budget.
	srvErr := srv.Shutdown(shutdownCtx)
	queueErr := queue.Stop(shutdownCtx)
	// The bot loop exits once ctx is cancelled; wait for it within the budget.
	select {
	case <-botDone:
	case <-shutdownCtx.Done():
	}
	return errors.Join(srvErr, queueErr)
}

// botURL builds the t.me deep link the user is redirected to after OAuth.
func botURL(username string) string {
	if username == "" {
		return "https://t.me"
	}
	return "https://t.me/" + username
}

// gdriveClients adapts oauth.Service to gdrive.Clients, fixing the bot to "docs".
type gdriveClients struct{ oauth *oauth.Service }

func (c gdriveClients) Client(ctx context.Context, userID string) (*http.Client, error) {
	return c.oauth.Client(ctx, userID, "docs")
}

// driveBridge implements docsbot.Drive over oauth (connection) and gdrive (upload).
type driveBridge struct {
	oauth *oauth.Service
	gd    *gdrive.Service
}

func (d driveBridge) Connected(ctx context.Context, user docsbot.UserID) (bool, error) {
	return d.oauth.Connected(ctx, string(user), "docs")
}

func (d driveBridge) Upload(ctx context.Context, user docsbot.UserID, in docsbot.DriveUpload) (docsbot.DriveFile, error) {
	f, err := d.gd.Upload(ctx, string(user), gdrive.Upload{
		CacheKey:   string(in.Category),
		FolderName: telegram.CategoryLabel(in.Category),
		Name:       in.Name,
		MIME:       in.MIME,
		Data:       in.Data,
	})
	if errors.Is(err, gdrive.ErrAccessRevoked) || errors.Is(err, oauth.ErrAccessRevoked) {
		return docsbot.DriveFile{}, docsbot.ErrDriveRevoked
	}
	if err != nil {
		return docsbot.DriveFile{}, err
	}
	return docsbot.DriveFile{ID: f.ID, Link: f.Link}, nil
}

// Revoke best-effort revokes the user's Drive access at Google and marks the
// integration revoked locally even if Google is unreachable.
func (d driveBridge) Revoke(ctx context.Context, user docsbot.UserID) error {
	return d.oauth.RevokeBestEffort(ctx, string(user), "docs")
}

// tgFiles implements docsbot.FileSource over the Telegram bot. bot is set after
// the bot is built (before the queue starts), breaking the svc-bot cycle.
type tgFiles struct{ bot *tg.Bot }

func (f *tgFiles) Download(ctx context.Context, fileID string) ([]byte, string, error) {
	data, mime, err := f.bot.Download(ctx, fileID)
	if errors.Is(err, tg.ErrFileTooLarge) {
		return nil, "", docsbot.ErrFileTooLarge
	}
	if err != nil {
		return nil, "", err
	}
	return data, mime, nil
}
