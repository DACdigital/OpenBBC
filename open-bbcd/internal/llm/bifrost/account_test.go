package bifrost

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestAccount_OneProviderOneKey(t *testing.T) {
	a := &account{provider: schemas.OpenAI, apiKey: "sk-1", baseURL: "https://p.example"}
	ps, _ := a.GetConfiguredProviders()
	if len(ps) != 1 || ps[0] != schemas.OpenAI {
		t.Fatalf("providers = %v", ps)
	}
	keys, err := a.GetKeysForProvider(context.Background(), schemas.OpenAI)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys = %v, err = %v", keys, err)
	}
	k := keys[0]
	if k.Value.Val != "sk-1" || !k.Models.IsUnrestricted() || k.Weight != 1 {
		t.Fatalf("key = %+v", k)
	}
	pc, err := a.GetConfigForProvider(schemas.OpenAI)
	if err != nil || pc.NetworkConfig.BaseURL != "https://p.example" || pc.ConcurrencyAndBufferSize.Concurrency == 0 {
		t.Fatalf("config = %+v, err = %v", pc, err)
	}
}

func TestAccount_KeyValueIsLiteral(t *testing.T) {
	// A key that looks like a Bifrost secret reference must not be resolved.
	a := &account{provider: schemas.OpenAI, apiKey: "env.HOME"}
	keys, _ := a.GetKeysForProvider(context.Background(), schemas.OpenAI)
	if keys[0].Value.Val != "env.HOME" {
		t.Fatalf("key resolved to %q", keys[0].Value.Val)
	}
}

func TestAccount_NoKeyAndOtherProviders(t *testing.T) {
	a := &account{provider: schemas.OpenAI}
	if keys, err := a.GetKeysForProvider(context.Background(), schemas.OpenAI); err != nil || len(keys) != 0 {
		t.Fatalf("keys = %v, err = %v", keys, err)
	}
	a.apiKey = "sk"
	if keys, _ := a.GetKeysForProvider(context.Background(), schemas.Groq); len(keys) != 0 {
		t.Fatalf("other provider keys = %v", keys)
	}
	if _, err := a.GetConfigForProvider(schemas.Groq); err == nil {
		t.Fatal("GetConfigForProvider(groq) should fail")
	}
}

func TestSlogLogger_FormatsAndTags(t *testing.T) {
	var buf bytes.Buffer
	l := newSlogLogger(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	l.Warn("retry %d of %s", 2, "openai")
	l.Fatal("boom")
	l.LogHTTPRequest(schemas.LogLevelInfo, "req").Str("path", "/v1").Int("status", 200).Int64("ms", 5).Send()
	out := buf.String()
	for _, want := range []string{`"msg":"retry 2 of openai"`, `"component":"bifrost"`, `"level":"WARN"`, `"msg":"boom"`, `"level":"ERROR"`, `"path":"/v1"`, `"status":200`, `"ms":5`} {
		if !strings.Contains(out, want) {
			t.Fatalf("log output missing %s:\n%s", want, out)
		}
	}
}
