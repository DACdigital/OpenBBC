package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/database"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/handler"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/storage"
)

const (
	ShutdownTimeout = 10 * time.Second
)

func main() {
	sub := ""
	if len(os.Args) > 1 {
		sub = os.Args[1]
	}
	var err error
	switch sub {
	case "", "serve":
		err = run()
	case "migrate":
		err = runMigrate()
	case "healthcheck":
		err = runHealthcheck()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q (valid: serve, migrate, healthcheck)\n", sub)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger.Info("connecting to database")
	db, err := database.NewPostgres(cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer db.Close()
	logger.Info("database connected")

	logger.Info("applying migrations")
	if err := database.Migrate(db); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	logger.Info("migrations applied")

	store, err := storage.NewLocalDisk(cfg.Discovery.StorageDir)
	if err != nil {
		return fmt.Errorf("init storage: %w", err)
	}
	logger.Info("discovery storage rooted", slog.String("dir", cfg.Discovery.StorageDir))

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      handler.NewAPI(db, store, cfg, logger),
		ReadTimeout:  handler.ReadTimeout,
		WriteTimeout: handler.WriteTimeout,
		IdleTimeout:  handler.IdleTimeout,
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("open-bbcd listening", slog.String("addr", addr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	<-done
	logger.Info("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()

	return server.Shutdown(ctx)
}

func runMigrate() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	db, err := database.NewPostgres(cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer db.Close()
	logger.Info("applying migrations")
	if err := database.Migrate(db); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	logger.Info("migrations applied")
	return nil
}

func runHealthcheck() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// Probe on localhost — healthcheck runs in the same container as the server.
	url := fmt.Sprintf("http://127.0.0.1:%d/health", cfg.Server.Port)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("healthcheck GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("healthcheck: status %d", resp.StatusCode)
	}
	return nil
}
