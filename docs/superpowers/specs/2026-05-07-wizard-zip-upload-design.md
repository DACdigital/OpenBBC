# Wizard Discovery Upload — Zip Blob to Local Disk

**Date:** 2026-05-07
**Status:** Approved (pending implementation plan)
**Component:** `open-bbcd`

## Background

The agent-creation wizard has a `discovery_file` step that today accepts a single `.yaml`/`.yml` file. The handler reads the file's bytes into a string and stores them inside the `agents.wizard_input` JSONB column alongside the textual answers.

The discovery output is no longer a single YAML — it is a `.flow-map/` directory tree (markdown, YAML, JSON across `flows/`, `capabilities/`, `skills/`, plus `AGENTS.md`, `APP.md`, `glossary.md`, `tools-proposed.json`). See `.test-project/frontend/.flow-map/` for the canonical shape.

## Goal

Change the wizard to accept a **zip archive** of a `.flow-map/` directory, store the zip as a blob on local disk under a configurable root, and reference it from the agent row by relative key. S3 is the eventual target; the local-disk implementation must sit behind an interface that maps cleanly to an `S3Storage` later.

## Non-goals

- Unzipping or reading the archive contents at upload time. A future wizard step will validate contents and surface errors there.
- Lifecycle/retention/cleanup of orphan files. The S3 lifecycle policy will handle this once we cut over.
- Auth, signed URLs, content-addressed storage, or dedup.
- Migrating existing agents — the column is nullable; older rows simply have no discovery file.

## Architecture

A new `internal/storage` package introduces a single-verb `Storage` interface. The wizard handler depends on the interface, not the concrete type. Today the only implementation is `LocalDisk`, backed by a directory; an `S3Storage` will replace it without handler changes.

```
handler.WizardHandler ──► storage.Storage ──► storage.LocalDisk      (today)
                                            └─► storage.S3Storage    (future)

                       ──► repository.AgentRepository
                              (writes new discovery_file_path column)
```

Wiring in `cmd/open-bbcd/main.go`:

```
config.Load → sql.Open(DB) → storage.NewLocalDisk(cfg.Discovery.StorageDir)
            → handler.NewAPI(db, store, cfg.Discovery)
```

`handler.NewAPI` gains two arguments: the storage and the discovery config block (so the handler can read `MaxUploadMB`).

## Components

### `internal/storage/storage.go` (new)

```go
package storage

type Storage interface {
    // Put writes data at the given relative key. Implementations must be
    // atomic with respect to readers — partial writes must not be observable
    // under the final key.
    Put(ctx context.Context, key string, r io.Reader) error
}

type LocalDisk struct{ Root string }

func NewLocalDisk(root string) (*LocalDisk, error) // os.MkdirAll(root, 0o755)
func (s *LocalDisk) Put(ctx context.Context, key string, r io.Reader) error
```

`Put` is the only verb today. `Get`/`Delete` are deferred until a caller needs them. This keeps the future S3 surface small.

`LocalDisk.Put` writes to `Root/key.tmp`, fsyncs, closes, then `os.Rename` to `Root/key` for atomicity.

### `internal/config/config.go` (extend)

```go
type Config struct {
    Server    ServerConfig
    Database  DatabaseConfig
    Discovery DiscoveryConfig
}

type DiscoveryConfig struct {
    StorageDir  string `env:"DISCOVERY_STORAGE_DIR" envDefault:"./data/discovery"`
    MaxUploadMB int    `env:"DISCOVERY_MAX_UPLOAD_MB" envDefault:"50"`
}
```

`.env.example` gains both keys.

### `internal/types/agent.go` (extend)

```go
type Agent struct {
    // ... existing fields
    DiscoveryFilePath string `json:"discovery_file_path,omitempty"`
}

type CreateAgentFromWizardOpts struct {
    ID                string // pre-generated UUID
    Name              string
    WizardInput       map[string]string // file field is NOT included here
    SchemaVersion     string
    DiscoveryFilePath string
}
```

### `internal/types/errors.go` (extend)

New sentinels (all map to HTTP 400 in `handler.Error`):

- `ErrDiscoveryFileRequired`
- `ErrDiscoveryFileTooLarge`
- `ErrDiscoveryFileBadExtension`

### `internal/handler/wizard.go` (rewrite the file branch)

1. If `r.ContentLength > maxBytes` → 400 (`ErrDiscoveryFileTooLarge`). This pre-check avoids string-matching stdlib's parse error.
2. `r.ParseMultipartForm(maxBytes)`.
3. For each ordered field:
   - text/textarea: same as today (required check, store into `wizardInput`).
   - **file** (single field — `discovery_file`): retrieve via `r.FormFile`. If missing and required → `ErrDiscoveryFileRequired`. If `strings.ToLower(filepath.Ext(header.Filename)) != ".zip"` → `ErrDiscoveryFileBadExtension`. **Do not** read into `wizardInput`.
4. `agentID := uuid.NewString()`.
5. `key := agentID + ".zip"`; `store.Put(ctx, key, file)`. On error → log + 500.
6. `repo.CreateFromWizard(ctx, opts{ID: agentID, DiscoveryFilePath: key, WizardInput: wizardInput, ...})`. On error → log the orphan key explicitly (`wizard: orphan discovery file %s after insert failure`) + 500.
7. `303 → /agents/ui`.

The handler signature changes to `NewWizardHandler(repo, schema, store, maxUploadBytes)`.

### `internal/repository/agent.go` (extend)

- `agentColumns` adds `discovery_file_path`.
- `scanAgent` reads it into `Agent.DiscoveryFilePath` (nullable).
- `CreateFromWizard` accepts the pre-generated `ID` and the `DiscoveryFilePath`. It **inserts with an explicit id** rather than relying on the column default. The existing `id` column has `DEFAULT gen_random_uuid()` (migration 001) — providing an explicit value overrides it.

### `migrations/005_add_agent_discovery_file_path.sql` (new)

```sql
-- +goose Up
ALTER TABLE agents
  ADD COLUMN discovery_file_path VARCHAR(255);

-- +goose Down
ALTER TABLE agents
  DROP COLUMN IF EXISTS discovery_file_path;
```

Nullable. Existing rows are unaffected.

### `web/schemas/wizard-v1.yaml`

`discovery_file.label`: "Upload discovery file" → "Upload discovery zip".

### `web/templates/wizard/step.html`

```html
<p>Upload the .flow-map zip from discovery.</p>
<input type="file" name="{{.Field.Key}}" accept=".zip"
       {{if .Field.Field.Required}}required{{end}}>
```

### `cmd/open-bbcd/main.go`

Construct `LocalDisk` and pass it (plus the discovery config) into `NewAPI`.

### Dependency

Add `github.com/google/uuid` as a direct dependency (`go get github.com/google/uuid`). Not currently in `go.mod`/`go.sum`.

## Data flow

### Happy path

1. Browser POSTs multipart form to `POST /agents/wizard`.
2. Handler validates `Content-Length`, parses form with `maxBytes` cap.
3. Handler validates required fields; checks file extension.
4. `agentID := uuid.NewString()`.
5. `store.Put(ctx, agentID+".zip", file)` — atomic write via tmp+rename.
6. `repo.CreateFromWizard(...)` inserts with explicit ID and `discovery_file_path = "<agentID>.zip"`. `wizard_input` JSONB contains only the textual answers.
7. `303 → /agents/ui`.

### Failure modes

| Failure | Outcome |
|---|---|
| `Content-Length` exceeds cap | 400 `ErrDiscoveryFileTooLarge`, no side effects |
| Required text field missing | 400 (existing sentinels), no side effects |
| File missing (required) | 400 `ErrDiscoveryFileRequired` |
| File extension ≠ `.zip` | 400 `ErrDiscoveryFileBadExtension` |
| `store.Put` fails | 500, no DB row, no file at final key (tmp file may remain — acceptable, named `*.tmp`) |
| DB insert fails after `Put` | 500, **orphan zip remains on disk**, key is logged. Cleanup is deferred to S3 lifecycle when we cut over. |

The order is **file-then-DB**, deliberately. The reverse leaves a row pointing at a non-existent file (worse failure mode for downstream readers).

## Error handling convention

Domain errors live in `internal/types/errors.go`. `handler.Error` (in `handler/handler.go`) maps them to HTTP statuses. The wizard handler returns sentinels via `handler.Error(w, err)` rather than ad-hoc `http.Error` calls — matches the existing pattern (`ErrNameRequired`, `ErrPromptRequired`) and means future JSON API endpoints get the same status mapping for free.

I/O errors from `store.Put` and `repo.CreateFromWizard` are non-sentinel; they get logged with context and produce 500.

## Testing

All tests colocated, `make test` runs everything with `-race`. No new integration layer.

### `internal/storage/storage_test.go` (new)

- `LocalDisk.Put` writes a file at the expected path; contents match.
- `NewLocalDisk` creates the root dir if missing (`os.Stat` after construction).
- A non-trivial blob never appears at the final key as a partial write — write 1 MB, race against a second goroutine `os.Stat`-ing in a tight loop and asserting that whenever the final name exists, its size equals the full payload. (Single-writer atomicity check.)

### `internal/handler/wizard_test.go` (extend)

A `mockStorage` is added alongside `mockWizardRepo`, satisfying `storage.Storage`. New test cases:

- Happy path: text fields + valid `.zip` part → `store.Put` invoked with key `<uuid>.zip`, `repo.CreateFromWizard` invoked with matching `DiscoveryFilePath` and `ID`, response is 303 to `/agents/ui`.
- Missing file (required) → 400.
- Wrong extension (`foo.tar`) → 400.
- Oversized via `Content-Length` → 400, neither store nor repo called.
- `store.Put` fails → 500, repo not called.
- `Put` succeeds, repo fails → 500, log captured (`log.SetOutput(&buf)`) contains the orphan key.

### `internal/repository/agent_test.go` (extend)

`CreateFromWizard` round-trips `discovery_file_path`. `GetByID` returns it.

### `internal/config/config_test.go` (extend)

Defaults for `DISCOVERY_STORAGE_DIR` and `DISCOVERY_MAX_UPLOAD_MB`; env overrides work.

## Out of scope (explicitly)

- Reading or unpacking the zip server-side.
- Validating the archive contains an expected `.flow-map/` shape.
- A `Get`/`Delete` verb on `Storage`.
- Cleanup of orphan files.
- S3 implementation. The interface is shaped to support it; the work is deferred.
