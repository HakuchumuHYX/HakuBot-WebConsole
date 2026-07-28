package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hakubot-webconsole/internal/config"
	"hakubot-webconsole/internal/csrf"
	"hakubot-webconsole/internal/database"
	"hakubot-webconsole/internal/query"
	"hakubot-webconsole/internal/server"
	"hakubot-webconsole/internal/storage"
	"hakubot-webconsole/internal/stream"
	"hakubot-webconsole/internal/watchdog"
	webassets "hakubot-webconsole/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("webconsole stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.SpoolPath, 0o750); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()
	db, err := database.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()

	cursor := query.NewCursorCodec(cfg.TokenSecret)
	repository := query.NewRepository(db, cursor)
	storageManager := storage.NewManager(
		db,
		cfg.DatabasePath,
		cfg.SpoolPath,
		cfg.RetentionDays,
	)
	streamHandler := stream.NewHandler(db, repository)
	handler := server.New(
		db,
		repository,
		cursor,
		server.Options{
			Storage: storageManager,
			CSRF: csrf.NewProtector(
				cfg.TokenSecret,
				cfg.PublicOrigin,
			),
			Confirmations: csrf.NewConfirmationStore(),
			Stream:        streamHandler,
			Static: http.FileServer(
				http.FS(webassets.Files),
			),
		},
	)
	httpServer := &http.Server{
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       90 * time.Second,
		// SSE and complete diagnostic downloads intentionally have no
		// server-wide write deadline.
		WriteTimeout: 0,
	}
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()

	go watchdog.Start(ctx)
	go runAutomaticRetention(ctx, storageManager)
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- httpServer.Serve(listener)
	}()
	if err := watchdog.Notify("READY=1"); err != nil {
		slog.Warn("systemd ready notification failed", "error", err)
	}
	slog.Info(
		"HakuBot WebConsole started",
		"listen",
		cfg.ListenAddress,
		"retention_days",
		cfg.RetentionDays,
	)

	select {
	case <-ctx.Done():
	case serveErr := <-serverErrors:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
	}

	_ = watchdog.Notify("STOPPING=1")
	shutdownContext, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()
	return httpServer.Shutdown(shutdownContext)
}

func runAutomaticRetention(
	ctx context.Context,
	manager *storage.Manager,
) {
	for {
		next := storage.NextAutomaticRun(time.Now())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		result, enabled, err := manager.RunAutomaticRetention(
			ctx,
			time.Now(),
		)
		if err != nil {
			slog.Error("automatic log retention failed", "error", err)
			continue
		}
		if enabled {
			slog.Info(
				"automatic log retention completed",
				"cutoff",
				result.CutoffDisplay,
				"responses_deleted",
				result.ResponsesDeleted,
				"diagnostics_deleted",
				result.DiagnosticsDeleted,
			)
		}
	}
}
