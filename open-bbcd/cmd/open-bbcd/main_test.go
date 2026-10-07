package main

import (
	"context"
	"io"
	"log/slog"
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
