package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeFetcher struct {
	delivery int
	body     string
	signURL  string
	ttl      time.Duration
}

func (f *fakeFetcher) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.body)), nil
}
func (f *fakeFetcher) Sign(_ context.Context, _ string, ttl time.Duration) (string, error) {
	f.ttl = ttl
	if f.signURL == "" {
		return "", errors.New("no url")
	}
	return f.signURL, nil
}
func (f *fakeFetcher) PreferredDelivery() int                     { return f.delivery }
func (f *fakeFetcher) Stat(context.Context, string) (bool, error) { return true, nil }

func TestFetchBytes_BytesDelivery(t *testing.T) {
	got, err := FetchBytes(context.Background(), "sha256/x", &fakeFetcher{delivery: 0, body: "png!"})
	if err != nil || string(got) != "png!" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestFetchBytes_SignedURLDelivery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "signed!")
	}))
	defer srv.Close()
	f := &fakeFetcher{delivery: 1, signURL: srv.URL}
	got, err := FetchBytes(context.Background(), "sha256/x", f)
	if err != nil || string(got) != "signed!" {
		t.Fatalf("got %q, err %v", got, err)
	}
	if f.ttl != 60*time.Second {
		t.Fatalf("ttl = %v, want 60s", f.ttl)
	}
}

func TestFetchBytes_SignedURLNon2xxHasNeutralPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := FetchBytes(context.Background(), "sha256/x", &fakeFetcher{delivery: 1, signURL: srv.URL})
	if err == nil || !strings.HasPrefix(err.Error(), "llm: signed URL fetch returned status 403") {
		t.Fatalf("err = %v", err)
	}
}
