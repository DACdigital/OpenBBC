package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestCloseWithin_ReturnsWhenCloseFinishes(t *testing.T) {
	ran := false
	closeWithin(context.Background(), func() { ran = true }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !ran {
		t.Fatal("close func not run")
	}
}

func TestCloseWithin_BoundedByDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	start := time.Now()
	closeWithin(ctx, func() { <-release }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if d := time.Since(start); d > time.Second {
		t.Fatalf("closeWithin took %v, want bounded by the ctx deadline", d)
	}
}

func TestParseLogLevel(t *testing.T) {
	cases := map[string]slog.Level{"": slog.LevelInfo, "info": slog.LevelInfo, "debug": slog.LevelDebug, "DEBUG": slog.LevelDebug, "warn": slog.LevelWarn, "error": slog.LevelError}
	for in, want := range cases {
		got, err := parseLogLevel(in)
		if err != nil || got != want {
			t.Errorf("parseLogLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseLogLevel("verbose"); err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Errorf("parseLogLevel(verbose) err = %v, want error naming LOG_LEVEL", err)
	}
}
