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
)

const (
	ShutdownTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
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

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      handler.NewAPI(db, logger),
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
