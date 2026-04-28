# open-bbcd Base Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the base open-bbcd Golang service with REST API for Alpha Agent Generation flow, storing agents and resources in PostgreSQL.

**Architecture:** Single-binary HTTP daemon following golang-standards/project-layout. Uses standard library for HTTP (net/http with Go 1.22+ routing). Clean import hierarchy: types → repository → handler. Migrations via goose CLI (no auto-migrate). Config via caarlos0/env with .env file support.

**Tech Stack:** Go 1.22+, PostgreSQL, goose (migrations), caarlos0/env + godotenv (config), lib/pq (driver)

---

## File Structure

```
open-bbcd/
├── cmd/
│   └── open-bbcd/
│       └── main.go                      # Entry point, wires dependencies
├── migrations/                          # Root level, goose SQL migrations
│   ├── 001_create_agents.sql
│   └── 002_create_resources.sql
├── internal/
│   ├── config/
│   │   └── config.go                    # Env loading via caarlos0/env
│   ├── database/
│   │   └── postgres.go                  # Connection pool only
│   ├── types/
│   │   ├── agent.go                     # Agent struct + NewAgent() + methods
│   │   ├── resource.go                  # Resource struct + NewResource() + methods
│   │   └── errors.go                    # Domain errors
│   ├── repository/
│   │   ├── agent.go                     # Agent data access
│   │   └── resource.go                  # Resource data access
│   └── handler/
│       ├── handler.go                   # Common utilities (JSON, errors)
│       ├── health.go                    # Health check endpoint
│       ├── agent.go                     # Agent HTTP handlers
│       └── resource.go                  # Resource HTTP handlers
├── go.mod
├── go.sum
├── Makefile                             # Build, test, run, migrate commands
├── .env.example                         # Example environment file
└── README.md
```

**Import Hierarchy (left-to-right, top-to-bottom):**
```
config     database     types
                          │
                          ▼
                      repository
                          │
                          ▼
                       handler
```

---

## Task 1: Initialize Go Module and Project Structure

**Files:**
- Create: `open-bbcd/go.mod`
- Create: `open-bbcd/Makefile`
- Create: `open-bbcd/.env.example`
- Create: `open-bbcd/cmd/open-bbcd/main.go`

- [ ] **Step 1: Create directories**

```bash
mkdir -p open-bbcd/cmd/open-bbcd
mkdir -p open-bbcd/migrations
mkdir -p open-bbcd/internal/{config,database,types,repository,handler}
```

- [ ] **Step 2: Initialize Go module**

```bash
cd open-bbcd && go mod init github.com/DACdigital/OpenBBC/open-bbcd
```

- [ ] **Step 3: Create minimal main.go**

```go
// cmd/open-bbcd/main.go
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fmt.Println("open-bbcd starting...")
	return nil
}
```

- [ ] **Step 4: Create .env.example**

```bash
# Database
DATABASE_URL=postgres://openbbcd:openbbcd@localhost:5432/openbbcd?sslmode=disable

# Server
SERVER_HOST=0.0.0.0
SERVER_PORT=8080
```

- [ ] **Step 5: Create Makefile**

```makefile
.PHONY: build run test clean migrate-up migrate-down migrate-status migrate-create

BINARY_NAME=open-bbcd
BUILD_DIR=bin

# Build
build:
	go build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/open-bbcd

run:
	go run ./cmd/open-bbcd

test:
	go test -v -race ./...

clean:
	rm -rf $(BUILD_DIR)

# Migrations (requires goose: go install github.com/pressly/goose/v3/cmd/goose@latest)
migrate-up:
	goose -dir migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir migrations postgres "$(DATABASE_URL)" down

migrate-status:
	goose -dir migrations postgres "$(DATABASE_URL)" status

migrate-create:
	goose -dir migrations create $(name) sql

.DEFAULT_GOAL := build
```

- [ ] **Step 6: Verify build works**

Run: `cd open-bbcd && make build`
Expected: Binary created at `bin/open-bbcd`

- [ ] **Step 7: Commit**

```bash
git add open-bbcd/
git commit -m "$(cat <<'EOF'
feat(open-bbcd): initialize Go module and project structure

- Set up golang-standards/project-layout structure
- Add minimal main.go entry point
- Add Makefile with build/test/migrate commands
- Add .env.example for local development

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Implement Configuration Loading

**Files:**
- Create: `open-bbcd/internal/config/config.go`
- Create: `open-bbcd/internal/config/config_test.go`

- [ ] **Step 1: Add dependencies**

```bash
cd open-bbcd && go get github.com/caarlos0/env/v10 github.com/joho/godotenv
```

- [ ] **Step 2: Write failing test for config loading**

```go
// internal/config/config_test.go
package config

import (
	"os"
	"testing"
)

func TestLoad_DefaultValues(t *testing.T) {
	// Clear env to test defaults
	os.Unsetenv("SERVER_HOST")
	os.Unsetenv("SERVER_PORT")
	os.Unsetenv("DATABASE_URL")

	cfg, err := Load()
	if err == nil {
		t.Fatal("Load() should error when DATABASE_URL is missing")
	}
	_ = cfg
}

func TestLoad_FromEnv(t *testing.T) {
	os.Setenv("SERVER_PORT", "9000")
	os.Setenv("SERVER_HOST", "127.0.0.1")
	os.Setenv("DATABASE_URL", "postgres://localhost/test")
	defer func() {
		os.Unsetenv("SERVER_PORT")
		os.Unsetenv("SERVER_HOST")
		os.Unsetenv("DATABASE_URL")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 9000 {
		t.Errorf("Server.Port = %d, want 9000", cfg.Server.Port)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("Server.Host = %q, want \"127.0.0.1\"", cfg.Server.Host)
	}
	if cfg.Database.URL != "postgres://localhost/test" {
		t.Errorf("Database.URL = %q, want \"postgres://localhost/test\"", cfg.Database.URL)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd open-bbcd && go test ./internal/config/...`
Expected: FAIL - Load function not defined

- [ ] **Step 4: Implement config loading**

```go
// internal/config/config.go
package config

import (
	"github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"
)

type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
}

type ServerConfig struct {
	Host string `env:"SERVER_HOST" envDefault:"0.0.0.0"`
	Port int    `env:"SERVER_PORT" envDefault:"8080"`
}

type DatabaseConfig struct {
	URL string `env:"DATABASE_URL,required"`
}

func Load() (*Config, error) {
	// Load .env if exists (ignore error if missing)
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd open-bbcd && go test ./internal/config/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add open-bbcd/internal/config/ open-bbcd/go.mod open-bbcd/go.sum
git commit -m "$(cat <<'EOF'
feat(open-bbcd): add configuration loading with caarlos0/env

- Support DATABASE_URL (required), SERVER_HOST, SERVER_PORT
- Load from .env file if present (godotenv)
- Sensible defaults (0.0.0.0:8080)

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Implement Database Connection

**Files:**
- Create: `open-bbcd/internal/database/postgres.go`
- Create: `open-bbcd/internal/database/postgres_test.go`

- [ ] **Step 1: Add lib/pq dependency**

```bash
cd open-bbcd && go get github.com/lib/pq
```

- [ ] **Step 2: Write failing test for database connection**

```go
// internal/database/postgres_test.go
package database

import (
	"testing"
)

func TestNewPostgres_InvalidURL(t *testing.T) {
	_, err := NewPostgres("")
	if err == nil {
		t.Error("NewPostgres(\"\") should return error for empty URL")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd open-bbcd && go test ./internal/database/...`
Expected: FAIL - NewPostgres not defined

- [ ] **Step 4: Implement database connection**

```go
// internal/database/postgres.go
package database

import (
	"database/sql"
	"errors"
	"time"

	_ "github.com/lib/pq"
)

func NewPostgres(url string) (*sql.DB, error) {
	if url == "" {
		return nil, errors.New("database URL is required")
	}

	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd open-bbcd && go test ./internal/database/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add open-bbcd/internal/database/ open-bbcd/go.mod open-bbcd/go.sum
git commit -m "$(cat <<'EOF'
feat(open-bbcd): add PostgreSQL connection pool

- Connection pool with sensible defaults (25 max, 5 idle, 5min lifetime)
- Returns *sql.DB for use by repositories
- Validation for empty URL

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Create Goose Migrations

**Files:**
- Create: `open-bbcd/migrations/001_create_agents.sql`
- Create: `open-bbcd/migrations/002_create_resources.sql`

- [ ] **Step 1: Create agents migration**

```sql
-- migrations/001_create_agents.sql

-- +goose Up
CREATE TABLE agents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    description TEXT,
    prompt TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_agents_name ON agents(name);

-- +goose Down
DROP TABLE IF EXISTS agents;
```

- [ ] **Step 2: Create resources migration**

```sql
-- migrations/002_create_resources.sql

-- +goose Up
CREATE TABLE resources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    description TEXT,
    prompt TEXT NOT NULL,
    mcp_endpoint VARCHAR(512),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_resources_agent_id ON resources(agent_id);
CREATE INDEX idx_resources_name ON resources(name);

-- +goose Down
DROP TABLE IF EXISTS resources;
```

- [ ] **Step 3: Verify migrations syntax**

Run: `cd open-bbcd && goose -dir migrations validate`
Expected: No errors (or goose doesn't have validate, just check file syntax)

- [ ] **Step 4: Commit**

```bash
git add open-bbcd/migrations/
git commit -m "$(cat <<'EOF'
feat(open-bbcd): add goose migrations for agents and resources

- 001: agents table (id, name, description, prompt, timestamps)
- 002: resources table (id, agent_id, name, description, prompt, mcp_endpoint)
- Proper up/down migrations for rollback support

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Implement Domain Types

**Files:**
- Create: `open-bbcd/internal/types/errors.go`
- Create: `open-bbcd/internal/types/agent.go`
- Create: `open-bbcd/internal/types/resource.go`

- [ ] **Step 1: Create domain errors**

```go
// internal/types/errors.go
package types

import "errors"

var (
	ErrNameRequired   = errors.New("name is required")
	ErrPromptRequired = errors.New("prompt is required")
	ErrAgentRequired  = errors.New("agent_id is required")
	ErrNotFound       = errors.New("not found")
)
```

- [ ] **Step 2: Create Agent type with constructor**

```go
// internal/types/agent.go
package types

import (
	"time"
)

type Agent struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Prompt      string    `json:"prompt"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateAgentInput struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt"`
}

func NewAgent(input CreateAgentInput) (*Agent, error) {
	if input.Name == "" {
		return nil, ErrNameRequired
	}
	if input.Prompt == "" {
		return nil, ErrPromptRequired
	}
	return &Agent{
		Name:        input.Name,
		Description: input.Description,
		Prompt:      input.Prompt,
	}, nil
}
```

- [ ] **Step 3: Create Resource type with constructor**

```go
// internal/types/resource.go
package types

import (
	"time"
)

type Resource struct {
	ID          string    `json:"id"`
	AgentID     string    `json:"agent_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Prompt      string    `json:"prompt"`
	MCPEndpoint string    `json:"mcp_endpoint,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateResourceInput struct {
	AgentID     string `json:"agent_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt"`
	MCPEndpoint string `json:"mcp_endpoint,omitempty"`
}

func NewResource(input CreateResourceInput) (*Resource, error) {
	if input.AgentID == "" {
		return nil, ErrAgentRequired
	}
	if input.Name == "" {
		return nil, ErrNameRequired
	}
	if input.Prompt == "" {
		return nil, ErrPromptRequired
	}
	return &Resource{
		AgentID:     input.AgentID,
		Name:        input.Name,
		Description: input.Description,
		Prompt:      input.Prompt,
		MCPEndpoint: input.MCPEndpoint,
	}, nil
}
```

- [ ] **Step 4: Write tests for constructors**

```go
// internal/types/agent_test.go
package types

import (
	"testing"
)

func TestNewAgent_Valid(t *testing.T) {
	input := CreateAgentInput{
		Name:   "Test Agent",
		Prompt: "You are a helpful assistant.",
	}

	agent, err := NewAgent(input)
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	if agent.Name != input.Name {
		t.Errorf("Name = %q, want %q", agent.Name, input.Name)
	}
}

func TestNewAgent_MissingName(t *testing.T) {
	input := CreateAgentInput{Prompt: "test"}
	_, err := NewAgent(input)
	if err != ErrNameRequired {
		t.Errorf("error = %v, want %v", err, ErrNameRequired)
	}
}

func TestNewAgent_MissingPrompt(t *testing.T) {
	input := CreateAgentInput{Name: "test"}
	_, err := NewAgent(input)
	if err != ErrPromptRequired {
		t.Errorf("error = %v, want %v", err, ErrPromptRequired)
	}
}
```

```go
// internal/types/resource_test.go
package types

import (
	"testing"
)

func TestNewResource_Valid(t *testing.T) {
	input := CreateResourceInput{
		AgentID: "agent-123",
		Name:    "get_users",
		Prompt:  "Fetches users from API",
	}

	resource, err := NewResource(input)
	if err != nil {
		t.Fatalf("NewResource() error = %v", err)
	}
	if resource.Name != input.Name {
		t.Errorf("Name = %q, want %q", resource.Name, input.Name)
	}
}

func TestNewResource_MissingAgentID(t *testing.T) {
	input := CreateResourceInput{Name: "test", Prompt: "test"}
	_, err := NewResource(input)
	if err != ErrAgentRequired {
		t.Errorf("error = %v, want %v", err, ErrAgentRequired)
	}
}
```

- [ ] **Step 5: Run tests**

Run: `cd open-bbcd && go test ./internal/types/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add open-bbcd/internal/types/
git commit -m "$(cat <<'EOF'
feat(open-bbcd): add domain types with constructors

- Agent: struct + NewAgent() with validation
- Resource: struct + NewResource() with validation
- Domain errors: ErrNameRequired, ErrPromptRequired, etc.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Implement Agent Repository

**Files:**
- Create: `open-bbcd/internal/repository/agent.go`
- Create: `open-bbcd/internal/repository/agent_test.go`

- [ ] **Step 1: Implement agent repository**

```go
// internal/repository/agent.go
package repository

import (
	"context"
	"database/sql"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type AgentRepository struct {
	db *sql.DB
}

func NewAgentRepository(db *sql.DB) *AgentRepository {
	return &AgentRepository{db: db}
}

func (r *AgentRepository) Create(ctx context.Context, input types.CreateAgentInput) (*types.Agent, error) {
	agent, err := types.NewAgent(input)
	if err != nil {
		return nil, err
	}

	err = r.db.QueryRowContext(ctx, `
		INSERT INTO agents (name, description, prompt)
		VALUES ($1, $2, $3)
		RETURNING id, name, description, prompt, created_at, updated_at
	`, agent.Name, agent.Description, agent.Prompt).Scan(
		&agent.ID,
		&agent.Name,
		&agent.Description,
		&agent.Prompt,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return agent, nil
}

func (r *AgentRepository) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	agent := &types.Agent{}
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, prompt, created_at, updated_at
		FROM agents WHERE id = $1
	`, id).Scan(
		&agent.ID,
		&agent.Name,
		&agent.Description,
		&agent.Prompt,
		&agent.CreatedAt,
		&agent.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, types.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return agent, nil
}

func (r *AgentRepository) List(ctx context.Context) ([]*types.Agent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, prompt, created_at, updated_at
		FROM agents ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []*types.Agent
	for rows.Next() {
		agent := &types.Agent{}
		if err := rows.Scan(
			&agent.ID,
			&agent.Name,
			&agent.Description,
			&agent.Prompt,
			&agent.CreatedAt,
			&agent.UpdatedAt,
		); err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}
```

- [ ] **Step 2: Write unit test**

```go
// internal/repository/agent_test.go
package repository

import (
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestAgentRepository_Create_ValidationError(t *testing.T) {
	repo := NewAgentRepository(nil) // nil db, won't reach it

	_, err := repo.Create(nil, types.CreateAgentInput{Name: "", Prompt: ""})
	if err != types.ErrNameRequired {
		t.Errorf("error = %v, want %v", err, types.ErrNameRequired)
	}
}
```

- [ ] **Step 3: Run tests**

Run: `cd open-bbcd && go test ./internal/repository/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add open-bbcd/internal/repository/agent.go open-bbcd/internal/repository/agent_test.go
git commit -m "$(cat <<'EOF'
feat(open-bbcd): add Agent repository

- Create, GetByID, List operations
- Uses types.NewAgent for validation
- Returns types.ErrNotFound for missing records

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Implement Resource Repository

**Files:**
- Create: `open-bbcd/internal/repository/resource.go`
- Create: `open-bbcd/internal/repository/resource_test.go`

- [ ] **Step 1: Implement resource repository**

```go
// internal/repository/resource.go
package repository

import (
	"context"
	"database/sql"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type ResourceRepository struct {
	db *sql.DB
}

func NewResourceRepository(db *sql.DB) *ResourceRepository {
	return &ResourceRepository{db: db}
}

func (r *ResourceRepository) Create(ctx context.Context, input types.CreateResourceInput) (*types.Resource, error) {
	resource, err := types.NewResource(input)
	if err != nil {
		return nil, err
	}

	err = r.db.QueryRowContext(ctx, `
		INSERT INTO resources (agent_id, name, description, prompt, mcp_endpoint)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, agent_id, name, description, prompt, mcp_endpoint, created_at, updated_at
	`, resource.AgentID, resource.Name, resource.Description, resource.Prompt, resource.MCPEndpoint).Scan(
		&resource.ID,
		&resource.AgentID,
		&resource.Name,
		&resource.Description,
		&resource.Prompt,
		&resource.MCPEndpoint,
		&resource.CreatedAt,
		&resource.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return resource, nil
}

func (r *ResourceRepository) GetByID(ctx context.Context, id string) (*types.Resource, error) {
	resource := &types.Resource{}
	err := r.db.QueryRowContext(ctx, `
		SELECT id, agent_id, name, description, prompt, mcp_endpoint, created_at, updated_at
		FROM resources WHERE id = $1
	`, id).Scan(
		&resource.ID,
		&resource.AgentID,
		&resource.Name,
		&resource.Description,
		&resource.Prompt,
		&resource.MCPEndpoint,
		&resource.CreatedAt,
		&resource.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, types.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return resource, nil
}

func (r *ResourceRepository) ListByAgentID(ctx context.Context, agentID string) ([]*types.Resource, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, agent_id, name, description, prompt, mcp_endpoint, created_at, updated_at
		FROM resources WHERE agent_id = $1 ORDER BY created_at DESC
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var resources []*types.Resource
	for rows.Next() {
		resource := &types.Resource{}
		if err := rows.Scan(
			&resource.ID,
			&resource.AgentID,
			&resource.Name,
			&resource.Description,
			&resource.Prompt,
			&resource.MCPEndpoint,
			&resource.CreatedAt,
			&resource.UpdatedAt,
		); err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	return resources, rows.Err()
}
```

- [ ] **Step 2: Write unit test**

```go
// internal/repository/resource_test.go
package repository

import (
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

func TestResourceRepository_Create_ValidationError(t *testing.T) {
	repo := NewResourceRepository(nil)

	_, err := repo.Create(nil, types.CreateResourceInput{})
	if err != types.ErrAgentRequired {
		t.Errorf("error = %v, want %v", err, types.ErrAgentRequired)
	}
}
```

- [ ] **Step 3: Run tests**

Run: `cd open-bbcd && go test ./internal/repository/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add open-bbcd/internal/repository/resource.go open-bbcd/internal/repository/resource_test.go
git commit -m "$(cat <<'EOF'
feat(open-bbcd): add Resource repository

- Create, GetByID, ListByAgentID operations
- Uses types.NewResource for validation
- Returns types.ErrNotFound for missing records

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Implement HTTP Handler Utilities

**Files:**
- Create: `open-bbcd/internal/handler/handler.go`
- Create: `open-bbcd/internal/handler/health.go`

- [ ] **Step 1: Create common handler utilities**

```go
// internal/handler/handler.go
package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type ErrorResponse struct {
	Error string `json:"error"`
}

func JSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("error encoding response: %v", err)
	}
}

func DecodeJSON(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return err
	}
	return nil
}

func Error(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError

	switch {
	case errors.Is(err, types.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, types.ErrNameRequired),
		errors.Is(err, types.ErrPromptRequired),
		errors.Is(err, types.ErrAgentRequired):
		status = http.StatusBadRequest
	}

	JSON(w, status, ErrorResponse{Error: err.Error()})
}
```

- [ ] **Step 2: Create health check handler**

```go
// internal/handler/health.go
package handler

import (
	"net/http"
)

type HealthResponse struct {
	Status string `json:"status"`
}

func Health(w http.ResponseWriter, r *http.Request) {
	JSON(w, http.StatusOK, HealthResponse{Status: "ok"})
}
```

- [ ] **Step 3: Verify compilation**

Run: `cd open-bbcd && go build ./...`
Expected: Build succeeds

- [ ] **Step 4: Commit**

```bash
git add open-bbcd/internal/handler/handler.go open-bbcd/internal/handler/health.go
git commit -m "$(cat <<'EOF'
feat(open-bbcd): add HTTP handler utilities and health endpoint

- JSON response helper with proper Content-Type
- Error response with domain-aware status codes
- Health check endpoint returning {"status": "ok"}

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Implement Agent HTTP Handlers

**Files:**
- Create: `open-bbcd/internal/handler/agent.go`
- Create: `open-bbcd/internal/handler/agent_test.go`

- [ ] **Step 1: Implement agent handlers**

```go
// internal/handler/agent.go
package handler

import (
	"context"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type AgentRepository interface {
	Create(ctx context.Context, input types.CreateAgentInput) (*types.Agent, error)
	GetByID(ctx context.Context, id string) (*types.Agent, error)
	List(ctx context.Context) ([]*types.Agent, error)
}

type AgentHandler struct {
	repo AgentRepository
}

func NewAgentHandler(repo AgentRepository) *AgentHandler {
	return &AgentHandler{repo: repo}
}

func (h *AgentHandler) Create(w http.ResponseWriter, r *http.Request) {
	var input types.CreateAgentInput
	if err := DecodeJSON(r, &input); err != nil {
		Error(w, err)
		return
	}

	agent, err := h.repo.Create(r.Context(), input)
	if err != nil {
		Error(w, err)
		return
	}

	JSON(w, http.StatusCreated, agent)
}

func (h *AgentHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: "id is required"})
		return
	}

	agent, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}

	JSON(w, http.StatusOK, agent)
}

func (h *AgentHandler) List(w http.ResponseWriter, r *http.Request) {
	agents, err := h.repo.List(r.Context())
	if err != nil {
		Error(w, err)
		return
	}

	if agents == nil {
		agents = []*types.Agent{}
	}

	JSON(w, http.StatusOK, agents)
}
```

- [ ] **Step 2: Write tests**

```go
// internal/handler/agent_test.go
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type mockAgentRepo struct {
	createFn func(ctx context.Context, input types.CreateAgentInput) (*types.Agent, error)
	getFn    func(ctx context.Context, id string) (*types.Agent, error)
	listFn   func(ctx context.Context) ([]*types.Agent, error)
}

func (m *mockAgentRepo) Create(ctx context.Context, input types.CreateAgentInput) (*types.Agent, error) {
	return m.createFn(ctx, input)
}

func (m *mockAgentRepo) GetByID(ctx context.Context, id string) (*types.Agent, error) {
	return m.getFn(ctx, id)
}

func (m *mockAgentRepo) List(ctx context.Context) ([]*types.Agent, error) {
	return m.listFn(ctx)
}

func TestAgentHandler_Create_Success(t *testing.T) {
	h := NewAgentHandler(&mockAgentRepo{
		createFn: func(ctx context.Context, input types.CreateAgentInput) (*types.Agent, error) {
			return &types.Agent{
				ID:     "test-id",
				Name:   input.Name,
				Prompt: input.Prompt,
			}, nil
		},
	})

	body := `{"name":"test","prompt":"test prompt"}`
	req := httptest.NewRequest(http.MethodPost, "/agents", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
	}

	var resp types.Agent
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID != "test-id" {
		t.Errorf("ID = %q, want %q", resp.ID, "test-id")
	}
}

func TestAgentHandler_Create_ValidationError(t *testing.T) {
	h := NewAgentHandler(&mockAgentRepo{
		createFn: func(ctx context.Context, input types.CreateAgentInput) (*types.Agent, error) {
			return nil, types.ErrNameRequired
		},
	})

	body := `{"name":"","prompt":""}`
	req := httptest.NewRequest(http.MethodPost, "/agents", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
```

- [ ] **Step 3: Run tests**

Run: `cd open-bbcd && go test ./internal/handler/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add open-bbcd/internal/handler/agent.go open-bbcd/internal/handler/agent_test.go
git commit -m "$(cat <<'EOF'
feat(open-bbcd): add Agent HTTP handlers

- POST /agents - create agent with prompt
- GET /agents/{id} - get agent by ID
- GET /agents - list all agents
- Interface-based repo for testability

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Implement Resource HTTP Handlers

**Files:**
- Create: `open-bbcd/internal/handler/resource.go`
- Create: `open-bbcd/internal/handler/resource_test.go`

- [ ] **Step 1: Implement resource handlers**

```go
// internal/handler/resource.go
package handler

import (
	"context"
	"net/http"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type ResourceRepository interface {
	Create(ctx context.Context, input types.CreateResourceInput) (*types.Resource, error)
	GetByID(ctx context.Context, id string) (*types.Resource, error)
	ListByAgentID(ctx context.Context, agentID string) ([]*types.Resource, error)
}

type ResourceHandler struct {
	repo ResourceRepository
}

func NewResourceHandler(repo ResourceRepository) *ResourceHandler {
	return &ResourceHandler{repo: repo}
}

func (h *ResourceHandler) Create(w http.ResponseWriter, r *http.Request) {
	var input types.CreateResourceInput
	if err := DecodeJSON(r, &input); err != nil {
		Error(w, err)
		return
	}

	resource, err := h.repo.Create(r.Context(), input)
	if err != nil {
		Error(w, err)
		return
	}

	JSON(w, http.StatusCreated, resource)
}

func (h *ResourceHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: "id is required"})
		return
	}

	resource, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		Error(w, err)
		return
	}

	JSON(w, http.StatusOK, resource)
}

func (h *ResourceHandler) ListByAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if agentID == "" {
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: "agent_id is required"})
		return
	}

	resources, err := h.repo.ListByAgentID(r.Context(), agentID)
	if err != nil {
		Error(w, err)
		return
	}

	if resources == nil {
		resources = []*types.Resource{}
	}

	JSON(w, http.StatusOK, resources)
}
```

- [ ] **Step 2: Write tests**

```go
// internal/handler/resource_test.go
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/types"
)

type mockResourceRepo struct {
	createFn func(ctx context.Context, input types.CreateResourceInput) (*types.Resource, error)
	getFn    func(ctx context.Context, id string) (*types.Resource, error)
	listFn   func(ctx context.Context, agentID string) ([]*types.Resource, error)
}

func (m *mockResourceRepo) Create(ctx context.Context, input types.CreateResourceInput) (*types.Resource, error) {
	return m.createFn(ctx, input)
}

func (m *mockResourceRepo) GetByID(ctx context.Context, id string) (*types.Resource, error) {
	return m.getFn(ctx, id)
}

func (m *mockResourceRepo) ListByAgentID(ctx context.Context, agentID string) ([]*types.Resource, error) {
	return m.listFn(ctx, agentID)
}

func TestResourceHandler_Create_Success(t *testing.T) {
	h := NewResourceHandler(&mockResourceRepo{
		createFn: func(ctx context.Context, input types.CreateResourceInput) (*types.Resource, error) {
			return &types.Resource{
				ID:      "res-id",
				AgentID: input.AgentID,
				Name:    input.Name,
				Prompt:  input.Prompt,
			}, nil
		},
	})

	body := `{"agent_id":"agent-123","name":"get_users","prompt":"Fetches users"}`
	req := httptest.NewRequest(http.MethodPost, "/resources", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
	}

	var resp types.Resource
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID != "res-id" {
		t.Errorf("ID = %q, want %q", resp.ID, "res-id")
	}
}
```

- [ ] **Step 3: Run tests**

Run: `cd open-bbcd && go test ./internal/handler/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add open-bbcd/internal/handler/resource.go open-bbcd/internal/handler/resource_test.go
git commit -m "$(cat <<'EOF'
feat(open-bbcd): add Resource HTTP handlers

- POST /resources - create resource with prompt
- GET /resources/{id} - get resource by ID
- GET /agents/{agent_id}/resources - list resources for agent

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Wire Everything in main.go

**Files:**
- Modify: `open-bbcd/cmd/open-bbcd/main.go`

- [ ] **Step 1: Update main.go**

```go
// cmd/open-bbcd/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DACdigital/OpenBBC/open-bbcd/internal/config"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/database"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/handler"
	"github.com/DACdigital/OpenBBC/open-bbcd/internal/repository"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log.Printf("connecting to database...")
	db, err := database.NewPostgres(cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer db.Close()
	log.Printf("database connected")

	agentRepo := repository.NewAgentRepository(db)
	resourceRepo := repository.NewResourceRepository(db)

	agentHandler := handler.NewAgentHandler(agentRepo)
	resourceHandler := handler.NewResourceHandler(resourceRepo)

	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("GET /health", handler.Health)

	// Agents
	mux.HandleFunc("POST /agents", agentHandler.Create)
	mux.HandleFunc("GET /agents", agentHandler.List)
	mux.HandleFunc("GET /agents/{id}", agentHandler.Get)

	// Resources
	mux.HandleFunc("POST /resources", resourceHandler.Create)
	mux.HandleFunc("GET /resources/{id}", resourceHandler.Get)
	mux.HandleFunc("GET /agents/{agent_id}/resources", resourceHandler.ListByAgent)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("open-bbcd listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-done
	log.Printf("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return server.Shutdown(ctx)
}
```

- [ ] **Step 2: Verify build**

Run: `cd open-bbcd && make build`
Expected: Build succeeds

- [ ] **Step 3: Commit**

```bash
git add open-bbcd/cmd/open-bbcd/main.go
git commit -m "$(cat <<'EOF'
feat(open-bbcd): wire HTTP server with all handlers

- Load config (requires DATABASE_URL)
- Connect to PostgreSQL
- Register all routes (health, agents, resources)
- Graceful shutdown on SIGINT/SIGTERM

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Add README and Docker Compose

**Files:**
- Create: `open-bbcd/README.md`
- Create: `open-bbcd/docker-compose.yml`

- [ ] **Step 1: Create README**

```markdown
# open-bbcd

Core platform service for OpenBBC - hosts agent runtime and provides backoffice REST API.

## Requirements

- Go 1.22+
- PostgreSQL 15+
- goose (`go install github.com/pressly/goose/v3/cmd/goose@latest`)

## Quick Start

### 1. Start PostgreSQL

```bash
docker-compose up -d
```

### 2. Set Environment

```bash
cp .env.example .env
# Edit .env if needed
```

### 3. Run Migrations

```bash
source .env
make migrate-up
```

### 4. Run Service

```bash
make run
```

## Commands

| Command | Description |
|---------|-------------|
| `make build` | Build binary to `bin/open-bbcd` |
| `make run` | Run service |
| `make test` | Run tests |
| `make migrate-up` | Run pending migrations |
| `make migrate-down` | Rollback last migration |
| `make migrate-status` | Show migration status |
| `make migrate-create name=<name>` | Create new migration |

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | /health | Health check |
| POST | /agents | Create agent |
| GET | /agents | List agents |
| GET | /agents/{id} | Get agent |
| POST | /resources | Create resource |
| GET | /resources/{id} | Get resource |
| GET | /agents/{agent_id}/resources | List agent resources |

## Configuration

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| DATABASE_URL | - | Yes | PostgreSQL connection URL |
| SERVER_HOST | 0.0.0.0 | No | Server bind host |
| SERVER_PORT | 8080 | No | Server bind port |
```

- [ ] **Step 2: Create docker-compose.yml**

```yaml
services:
  postgres:
    image: postgres:15-alpine
    environment:
      POSTGRES_USER: openbbcd
      POSTGRES_PASSWORD: openbbcd
      POSTGRES_DB: openbbcd
    ports:
      - "5432:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U openbbcd"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  postgres_data:
```

- [ ] **Step 3: Commit**

```bash
git add open-bbcd/README.md open-bbcd/docker-compose.yml
git commit -m "$(cat <<'EOF'
docs(open-bbcd): add README and docker-compose

- Quick start guide
- Make commands reference
- API endpoints documentation
- Docker Compose for local PostgreSQL

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: Final Verification

- [ ] **Step 1: Run all tests**

Run: `cd open-bbcd && make test`
Expected: All tests pass

- [ ] **Step 2: Build binary**

Run: `cd open-bbcd && make build`
Expected: Binary at `bin/open-bbcd`

- [ ] **Step 3: Start PostgreSQL**

Run: `cd open-bbcd && docker-compose up -d`
Expected: PostgreSQL running

- [ ] **Step 4: Run migrations**

```bash
cd open-bbcd
export DATABASE_URL="postgres://openbbcd:openbbcd@localhost:5432/openbbcd?sslmode=disable"
make migrate-up
```
Expected: Migrations applied

- [ ] **Step 5: Run service**

```bash
make run
```
Expected: "open-bbcd listening on 0.0.0.0:8080"

- [ ] **Step 6: Test endpoints**

```bash
# Health
curl http://localhost:8080/health

# Create agent
curl -X POST http://localhost:8080/agents \
  -H "Content-Type: application/json" \
  -d '{"name":"Alpha Agent","prompt":"You are a helpful assistant."}'

# List agents
curl http://localhost:8080/agents
```
Expected: All return valid JSON responses

- [ ] **Step 7: Final commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
feat(open-bbcd): complete base service implementation

Base open-bbcd service with:
- REST API for Alpha Agent Generation flow
- Agent storage with prompts
- Resource storage with prompts and MCP endpoints
- PostgreSQL with goose migrations (CLI only)
- Config via caarlos0/env with .env support
- Health check endpoint

Closes #2

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>
EOF
)"
```

---

## Summary

| Requirement | Implementation |
|-------------|----------------|
| In Golang | Go 1.22+ with standard library |
| Daemon/service | HTTP server with graceful shutdown |
| REST API for AAG flow | Endpoints for agents and resources |
| Store alpha agent prompt | agents table with prompt field |
| Store resources + prompts | resources table with prompt + mcp_endpoint |

**Architecture Decisions:**
- Migrations: goose CLI (no auto-migrate, state separate from runtime)
- Config: caarlos0/env + godotenv (no prefix)
- Structure: types → repository → handler (clean import hierarchy)
- No service layer (logic in types constructors/methods, cross-type in handler)

**Dependencies:**
- `github.com/lib/pq` - PostgreSQL driver
- `github.com/caarlos0/env/v10` - Config parsing
- `github.com/joho/godotenv` - .env file loading
- `github.com/pressly/goose/v3` - Migrations (CLI tool)
