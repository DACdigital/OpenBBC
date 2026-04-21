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
