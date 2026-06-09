// scripts/seed_bundle.go — seed a bundle YAML into agents.bundle JSONB.
//
// Usage (from monorepo root):
//   DATABASE_URL=... go run ./scripts seed-bundle \
//     --agent-id <uuid> --bundle path/to/bundle.yaml [--force]
//
// Without --force, refuses to overwrite a non-NULL bundle. With --force,
// overrides (dev escape hatch when no sessions exist).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq"
	"gopkg.in/yaml.v3"
)

func main() {
	agentID := flag.String("agent-id", "", "UUID of the agent (version row) to seed")
	bundlePath := flag.String("bundle", "", "Path to bundle YAML file")
	force := flag.Bool("force", false, "Overwrite a non-NULL bundle")
	flag.Parse()

	if *agentID == "" || *bundlePath == "" {
		fmt.Fprintln(os.Stderr, "usage: seed_bundle --agent-id <uuid> --bundle <path> [--force]")
		os.Exit(2)
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL not set")
		os.Exit(2)
	}

	raw, err := os.ReadFile(*bundlePath)
	if err != nil {
		die(err)
	}

	var asAny any
	if err := yaml.Unmarshal(raw, &asAny); err != nil {
		die(fmt.Errorf("parse YAML: %w", err))
	}
	asAny = normalizeYAMLMaps(asAny)
	jsonBytes, err := json.Marshal(asAny)
	if err != nil {
		die(fmt.Errorf("marshal JSON: %w", err))
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		die(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var query string
	if *force {
		query = `UPDATE agents SET bundle = $2, updated_at = now() WHERE id = $1::uuid`
	} else {
		query = `UPDATE agents SET bundle = $2, updated_at = now() WHERE id = $1::uuid AND bundle IS NULL`
	}
	res, err := db.ExecContext(ctx, query, *agentID, jsonBytes)
	if err != nil {
		die(err)
	}

	n, _ := res.RowsAffected()
	if n == 0 {
		// Either agent doesn't exist, or bundle is already set (and force is false).
		var exists, hasBundle bool
		_ = db.QueryRowContext(ctx, `
            SELECT
                EXISTS(SELECT 1 FROM agents WHERE id = $1::uuid),
                COALESCE((SELECT bundle IS NOT NULL FROM agents WHERE id = $1::uuid), false)
        `, *agentID).Scan(&exists, &hasBundle)
		switch {
		case !exists:
			die(errors.New("agent not found"))
		case hasBundle && !*force:
			die(errors.New("bundle already set; use --force to overwrite"))
		default:
			die(errors.New("update affected 0 rows (unknown reason)"))
		}
	}

	fmt.Printf("✓ bundle written for agent %s (%d bytes)\n", *agentID, len(jsonBytes))
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

// normalizeYAMLMaps converts map[any]any (from yaml.v3 generic decode)
// to map[string]any so encoding/json can serialize. yaml.v3 produces
// map[string]any for typical YAML, but defensive normalization handles
// edge cases (non-string keys would otherwise panic json.Marshal).
func normalizeYAMLMaps(v any) any {
	switch x := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, vv := range x {
			m[fmt.Sprintf("%v", k)] = normalizeYAMLMaps(vv)
		}
		return m
	case map[string]any:
		for k, vv := range x {
			x[k] = normalizeYAMLMaps(vv)
		}
		return x
	case []any:
		for i, vv := range x {
			x[i] = normalizeYAMLMaps(vv)
		}
		return x
	default:
		return v
	}
}
