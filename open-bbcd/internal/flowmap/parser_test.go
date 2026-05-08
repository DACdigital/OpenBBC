package flowmap_test

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/flowmap"
)

// zipDir walks dir and returns an in-memory zip with paths relative to dir.
// Mirrors what r.FormFile receives at runtime.
func zipDir(t *testing.T, dir string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		fw, err := w.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(fw, f)
		return err
	})
	if err != nil {
		t.Fatalf("zipDir: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zipDir close: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestParse_HappyPath(t *testing.T) {
	r := zipDir(t, "testdata/sample-flowmap")

	cfg, err := flowmap.Parse(r)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if cfg.Source.AppName != "sample-flowmap" {
		t.Errorf("AppName = %q, want sample-flowmap", cfg.Source.AppName)
	}
	if len(cfg.Flows) != 1 {
		t.Fatalf("Flows = %d, want 1", len(cfg.Flows))
	}
	flow := cfg.Flows[0]
	if flow.ID != "place-order" {
		t.Errorf("Flow.ID = %q, want place-order", flow.ID)
	}
	if !flow.Included {
		t.Error("Flow.Included should default to true")
	}
	if flow.Origin != "discovered" {
		t.Errorf("Flow.Origin = %q, want discovered", flow.Origin)
	}
	if !strings.HasPrefix(flow.Workflow.Mermaid, "flowchart TD") {
		t.Errorf("Flow.Workflow.Mermaid does not start with flowchart TD: %q", flow.Workflow.Mermaid)
	}
	if !strings.Contains(flow.ProseMD, "How the agent handles this") {
		t.Errorf("Flow.ProseMD should preserve the body: %q", flow.ProseMD[:80])
	}

	if len(cfg.Skills) != 1 || cfg.Skills[0].ID != "place-order" {
		t.Errorf("Skills = %+v", cfg.Skills)
	}
	if cfg.Skills[0].CapabilityRef != "orders" {
		t.Errorf("Skill.CapabilityRef = %q, want orders (resolved from capabilities/orders.md#orders-create)", cfg.Skills[0].CapabilityRef)
	}

	if len(cfg.Capabilities) != 1 || cfg.Capabilities[0].Name != "orders" {
		t.Errorf("Capabilities = %+v", cfg.Capabilities)
	}
}
