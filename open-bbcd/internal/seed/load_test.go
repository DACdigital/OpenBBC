package seed

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_FileAndDir(t *testing.T) {
	s, err := Load(filepath.Join("testdata", "one.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Backends) != 1 || len(s.Agents) != 1 || len(s.Versions) != 1 {
		t.Fatalf("unexpected set: %+v", s)
	}
	if s.Versions[0].Source != filepath.Join("testdata", "one.yaml") {
		t.Errorf("source = %q", s.Versions[0].Source)
	}
	tl := s.Agents[0].Architecture["tools"].([]any)[0]
	if _, ok := tl.(map[string]any); !ok {
		t.Errorf("nested map is %T", tl)
	}

	s, err = Load(filepath.Join("testdata", "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Backends) != 2 || s.Backends[0].Name != "crm-mcp" || s.Backends[1].Name != "crm-api" {
		t.Fatalf("dir merge wrong (lexical order, .txt ignored): %+v", s.Backends)
	}
	if !strings.HasSuffix(s.Backends[1].Source, "b.yml") {
		t.Errorf("source = %q", s.Backends[1].Source)
	}
}

func TestLoad_UnknownKeyFails(t *testing.T) {
	if _, err := Load(filepath.Join("testdata", "unknown.yaml")); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoad_BadFormatFails(t *testing.T) {
	if _, err := Load(filepath.Join("testdata", "badformat.yaml")); err == nil || !strings.Contains(err.Error(), "openbbc-seed-v2") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoad_DuplicateAcrossFiles(t *testing.T) {
	_, err := Load(filepath.Join("testdata", "dupdir"))
	if err == nil || !strings.Contains(err.Error(), "a.yaml") || !strings.Contains(err.Error(), "b.yaml") || !strings.Contains(err.Error(), "crm-api") {
		t.Fatalf("err = %v", err)
	}
}
