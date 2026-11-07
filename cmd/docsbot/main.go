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
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/config"
	"github.com/alexe0110/most-app/internal/platform/db"
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
	queue, err := jobs.New(pool, log, workers, purge)
	if err != nil {
		return err
	}
	if err := queue.Start(ctx); err != nil {
		return fmt.Errorf("start queue: %w", err)
	}
	log.Info("job queue started")

	repo := postgres.NewRepo(pool)
	// Drive upload is #11; until then the stub reports "not connected", so saved
	// documents stay in Telegram.
	svc := docsbot.NewService(repo, driveOff{})

	// oauth.Service and the bot each need the other (the bot shows the consent
	// link; oauth notifies the bot when Drive connects). tgbot is filled just
	// below, and both closures run only after startup, so the cycle is safe.
	var tgbot *tg.Bot
	oauthSvc, err := oauth.New(oauth.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		TokenEncKey:  cfg.TokenEncKey,
		PublicURL:    cfg.PublicURL,
		BotURL:       botURL(cfg.TelegramBotUsername),
	}, repo, func(ctx context.Context, userID string) error {
		return telegram.NotifyConnected(tgbot, repo)(ctx, userID)
	}, log)
	if err != nil {
		return fmt.Errorf("new oauth service: %w", err)
	}

	tgbot, err = tg.New(tg.Config{
		Token:         cfg.TelegramToken,
		Mode:          tg.Mode(cfg.TelegramMode),
		PublicURL:     cfg.PublicURL,
		WebhookSecret: cfg.TelegramWebhookSecret,
	}, repo, log, func(b *bot.Bot) { telegram.Register(b, log, svc, oauthSvc) })
	if err != nil {
		return err
	}
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

// driveOff is the placeholder Drive port: Drive is never connected until #11
// wires the real implementation (integrations check + upload enqueue), so saved
// documents stay in Telegram.
type driveOff struct{}

func (driveOff) Connected(context.Context, docsbot.UserID) (bool, error) { return false, nil }
