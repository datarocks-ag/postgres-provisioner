# CLAUDE.md

## Project Overview

A Go CLI tool that idempotently provisions PostgreSQL resources (roles, databases, extensions, schemas, grants, migrations) from a YAML config file. Designed as a Docker Compose init container.

## Build & Run

```bash
go build -o postgres-provisioner ./cmd/postgres-provisioner
make build          # same via Makefile
make test           # unit tests
make test-integration  # integration tests (requires Docker)
make lint           # golangci-lint
```

## Required Environment Variables

- `POSTGRES_USER` — PostgreSQL admin username
- `POSTGRES_PASSWORD` — PostgreSQL admin password

## Optional Environment Variables

- `POSTGRES_HOST` (default: `localhost`)
- `POSTGRES_PORT` (default: `5432`)
- `POSTGRES_DB` (default: `postgres`)
- `POSTGRES_SSLMODE` (default: `disable`)
- `PGHELPER_CONFIG_PATH` (default: `./config.yaml`)
- `MIGRATIONS_ENABLED` (default: `true`) — set to `false` to skip all migrations at runtime
- `LOG_LEVEL` (default: `info`)

## Architecture

```
cmd/postgres-provisioner/main.go       # CLI entrypoint, env vars, slog setup
internal/
  config/config.go                    # YAML config structs, loader, env var expansion, validation
  config/config_test.go               # Unit tests for config parsing
  db/connection.go                    # Connection factory with exponential backoff retry
  provisioner/
    provisioner.go                    # Top-level orchestrator + SQL quoting helpers
    roles.go                          # Idempotent role creation/update
    databases.go                      # Idempotent database creation
    extensions.go                     # CREATE EXTENSION IF NOT EXISTS
    schemas.go                        # CREATE SCHEMA IF NOT EXISTS + owner
    grants.go                         # GRANT statements + ALTER DEFAULT PRIVILEGES
    migrations.go                     # Flyway-style SQL migration discovery, tracking, execution
    provisioner_test.go               # Unit tests with go-sqlmock
    migrations_test.go                # Unit tests for migration logic
    integration_test.go               # Integration tests with testcontainers-go (build tag: integration)
```

## Dependencies

Go 1.25 module using:
- `github.com/lib/pq` — PostgreSQL driver
- `gopkg.in/yaml.v3` — YAML config parsing
- `github.com/DATA-DOG/go-sqlmock` — SQL mock for unit tests
- `github.com/testcontainers/testcontainers-go` — integration tests with real PostgreSQL

## Key Design Decisions

- **Order**: Roles → Databases → Extensions → Schemas → Grants → Migrations
- **Idempotency**: Check pg_roles/pg_database before CREATE; IF NOT EXISTS; GRANT is inherently idempotent
- **Per-database connections**: Extensions/schemas/grants connect to each target database
- **SQL injection prevention**: `quoteIdentifier()` and `quoteLiteral()` helpers
- **Structured logging**: `log/slog` with JSON output
- **Strategy**: `update` (default) or `create` (skip existing). Per-resource overrides global.
- **Connection retry**: Exponential backoff (1s–30s, 15 retries, 5min timeout)
- **Migrations**: Flyway-style SQL files (`V0001__desc.sql` versioned, `R0001__desc.sql` repeatable) with SHA-256 checksum tracking in `_schema_migrations` table. Versioned are immutable; repeatable re-run on change.
- **Migrations toggle**: `--migrations` flag / `MIGRATIONS_ENABLED` env var (default `true`). CLI flag overrides env var.
