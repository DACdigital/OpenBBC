package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/database"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/handler"
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
	// The level is a LevelVar so LOG_LEVEL can be applied after config.Load,
	// which is what loads .env.
	level := new(slog.LevelVar)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	lvl, err := parseLogLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return err
	}
	level.Set(lvl)

	llmClient, closeLLM, err := handler.NewLLM(cfg, logger)
	if err != nil {
		return fmt.Errorf("init llm: %w", err)
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

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      handler.NewAPIWithLLM(db, cfg, logger, llmClient),
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

	err = server.Shutdown(ctx)
	closeWithin(ctx, closeLLM, logger)
	return err
}

// closeWithin runs closeFn but returns no later than ctx's deadline, so a
// slow LLM adapter shutdown cannot stretch the exit past ShutdownTimeout.
// On timeout closeFn is left running; the process is exiting anyway.
func closeWithin(ctx context.Context, closeFn func(), logger *slog.Logger) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		closeFn()
	}()
	select {
	case <-done:
	case <-ctx.Done():
		logger.Warn("llm adapter shutdown did not finish before the shutdown deadline", slog.Any("error", ctx.Err()))
	}
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

// runHealthcheck probes localhost:$SERVER_PORT/health. Reads SERVER_PORT
// directly (default 8080) rather than going through config.Load() so the
// probe stays dependency-minimal — a broken DATABASE_URL must not fail the
// healthcheck. Intentionally silent: exit code is the healthcheck signal.
func runHealthcheck() error {
	port := 8080
	if v := os.Getenv("SERVER_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid SERVER_PORT: %w", err)
		}
		port = n
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
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

// parseLogLevel maps LOG_LEVEL (debug|info|warn|error, case-insensitive;
// empty means info) to a slog level.
func parseLogLevel(v string) (slog.Level, error) {
	var l slog.Level
	if v == "" {
		return slog.LevelInfo, nil
	}
	if err := l.UnmarshalText([]byte(v)); err != nil {
		return 0, fmt.Errorf("LOG_LEVEL: expected debug, info, warn or error, got %q", v)
	}
	return l, nil
}
