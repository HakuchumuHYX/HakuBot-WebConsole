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
	"hakubot-webconsole/internal/database"
	"hakubot-webconsole/internal/query"
	"hakubot-webconsole/internal/server"
	"hakubot-webconsole/internal/storage"
	"hakubot-webconsole/internal/sysmonitor"
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

	storageManager := storage.NewManager(db, cfg.DatabasePath, cfg.RetentionDays)
	sysCollector := sysmonitor.NewCollector(db, cfg.DatabasePath)

	handler := server.New(
		query.NewRepository(db),
		storageManager,
		sysCollector,
		cfg.PublicOrigin,
		http.FileServer(http.FS(webassets.Files)),
	)
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       90 * time.Second,
		// Complete diagnostic downloads can be large, so there is no
		// server-wide write deadline.
		WriteTimeout: 0,
	}
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()

	go sysCollector.Start(ctx)
	if cfg.RetentionDays > 0 {
		go runAutomaticRetention(ctx, storageManager)
	}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- httpServer.Serve(listener)
	}()
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
		result, err := manager.RunAutomaticRetention(ctx, time.Now())
		if err != nil {
			slog.Error("automatic log retention failed", "error", err)
			continue
		}
		slog.Info(
			"automatic log retention completed",
			"cutoff",
			result.Cutoff,
			"responses_deleted",
			result.ResponsesDeleted,
			"diagnostics_deleted",
			result.DiagnosticsDeleted,
		)
	}
}
