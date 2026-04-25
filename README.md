# postgres-provisioner

[![CI](https://github.com/datarocks-ag/postgres-provisioner/actions/workflows/ci.yaml/badge.svg)](https://github.com/datarocks-ag/postgres-provisioner/actions/workflows/ci.yaml)
![coverage](https://raw.githubusercontent.com/datarocks-ag/postgres-provisioner/badges/.badges/develop/coverage.svg)

A Go CLI tool that idempotently provisions PostgreSQL resources (roles, databases, extensions, schemas, grants, migrations) from a YAML config file. Designed as a Docker Compose init container that runs before your application starts.

## Quick Start

```yaml
# docker-compose.yaml
services:
  postgres-provisioner:
    image: ghcr.io/datarocks-ag/postgres-provisioner:latest
    environment:
      POSTGRES_USER: admin
      POSTGRES_PASSWORD: adminpass
      POSTGRES_HOST: postgres
      PGHELPER_CONFIG_PATH: /config.yaml
    volumes:
      - ./config.yaml:/config.yaml:ro
```

## Configuration

Create a `config.yaml` (see [config.example.yaml](config.example.yaml)):

```yaml
roles:
  - name: "app_user"
    password: "${APP_DB_PASSWORD}"    # env var interpolation
    options:
      login: true
      superuser: false

databases:
  - name: "myapp"
    owner: "app_user"
    extensions: ["uuid-ossp", "pgcrypto"]
    schemas:
      - name: "app"
        owner: "app_user"
    grants:
      - role: "app_user"
        privileges: ["ALL"]
        on_schema: "app"
      - role: "readonly_user"
        privileges: ["CONNECT"]
        on_database: true
      - role: "readonly_user"
        privileges: ["SELECT"]
        on_tables_in_schema: "app"
    migrations:
      directory: "./migrations/myapp"
```

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `POSTGRES_USER` | yes | - | Admin user |
| `POSTGRES_PASSWORD` | yes | - | Admin password |
| `POSTGRES_HOST` | no | `localhost` | PostgreSQL host |
| `POSTGRES_PORT` | no | `5432` | PostgreSQL port |
| `POSTGRES_DB` | no | `postgres` | Admin database |
| `POSTGRES_SSLMODE` | no | `disable` | SSL mode |
| `PGHELPER_CONFIG_PATH` | no | `./config.yaml` | Path to YAML config |
| `MIGRATIONS_ENABLED` | no | `true` | Set to `false` to skip all migrations at runtime |
| `DRY_RUN` | no | `false` | Set to `true` to log every mutation as a preview without applying it |
| `LOG_LEVEL` | no | `info` | Log level (debug/info/warn/error) |

## Strategy

Control whether existing resources are updated or skipped using the `strategy` field:

- `update` (default) — create resources if missing, update if they already exist
- `create` — create resources if missing, skip if they already exist. For databases,
  this also skips all sub-resources (extensions, schemas, grants) if the database
  already exists.

Strategy can be set globally or per resource. Per-resource strategy overrides the global setting.

```yaml
strategy: "create"           # global default: skip existing resources

roles:
  - name: "app_user"
    strategy: "update"       # override: always reconcile this role
    password: "${APP_DB_PASSWORD}"
```

## Environment Variable Expansion

String values support `${VAR}` syntax. If the variable is set in the environment, it is replaced; if unset, the placeholder is preserved as-is (useful for detecting misconfigurations).

```yaml
password: "${APP_DB_PASSWORD}"    # replaced with env var value at load time
```

## Provisioning Order

1. **Roles** — created or updated idempotently
2. **Databases** — created idempotently, owner set
3. **Extensions** — `CREATE EXTENSION IF NOT EXISTS` (per-database)
4. **Schemas** — `CREATE SCHEMA IF NOT EXISTS` with owner (per-database). Owner defaults to database owner if not specified.
5. **Grants** — `GRANT` statements are inherently idempotent (per-database). `on_tables_in_schema` also sets `ALTER DEFAULT PRIVILEGES` for future tables.
6. **Migrations** — Flyway-style SQL files executed per-database (if configured and enabled).

## Grant Types

- `on_database: true` — grants privileges on the database itself (e.g., `CONNECT`)
- `on_schema: "name"` — grants privileges on a schema (e.g., `ALL`, `USAGE`)
- `on_tables_in_schema: "name"` — grants on all existing tables + sets `ALTER DEFAULT PRIVILEGES` for future tables

## Migrations

Flyway-style SQL migrations can be configured per database. Migration files are discovered from a directory and executed in order.

```yaml
databases:
  - name: "myapp"
    owner: "app_user"
    migrations:
      directory: "./migrations/myapp"   # supports ${VAR} expansion
```

**File naming:**

- `V0001__create_users.sql` — **Versioned**: run once, immutable. Checksum is verified on subsequent runs; a mismatch causes an error.
- `R0001__seed_data.sql` — **Repeatable**: re-run whenever the file content changes.

Migrations are tracked in a `_schema_migrations` table (created automatically) with SHA-256 checksums. Versioned migrations run first (sorted by version), then repeatable migrations.

**Disabling migrations at runtime:**

Migrations can be disabled without changing the config file using the `--migrations=false` CLI flag or `MIGRATIONS_ENABLED=false` environment variable. The CLI flag takes precedence over the env var.

## CLI Flags

| Flag | Default | Description |
|---|---|---|
| `--migrations` | `true` (or `MIGRATIONS_ENABLED` env var) | Enable/disable migrations |
| `--dry-run` | `false` (or `DRY_RUN` env var) | Log mutations as a preview without executing them. Read-only queries (existence checks, applied-migration lookups) still run so the preview reflects live state. The CLI flag takes precedence over the env var. |
| `--version` | — | Print version and exit |

## SSL Mode

Set `POSTGRES_SSLMODE` to control connection encryption. Defaults to `disable` for local development. For production, use `require`, `verify-ca`, or `verify-full`. See [PostgreSQL SSL documentation](https://www.postgresql.org/docs/current/libpq-ssl.html).

## Connection Retry

On startup, the tool retries connecting to PostgreSQL with exponential backoff (1s initial, 30s cap, 15 retries, 5min total timeout). This handles Docker Compose startup ordering without requiring `wait-for-it` scripts.

## Development

```bash
make build            # Build binary
make test             # Run unit tests
make test-integration # Run integration tests (requires Docker)
make lint             # Run golangci-lint
make vet              # Run go vet
make docker           # Build Docker image
```

## Docker Compose Usage

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: admin
      POSTGRES_PASSWORD: adminpass
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U admin"]
      interval: 2s
      timeout: 5s
      retries: 10

  postgres-provisioner:
    image: ghcr.io/datarocks-ag/postgres-provisioner:latest
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      POSTGRES_USER: admin
      POSTGRES_PASSWORD: adminpass
      POSTGRES_HOST: postgres
      PGHELPER_CONFIG_PATH: /config.yaml
      APP_DB_PASSWORD: appsecret
    volumes:
      - ./config.yaml:/config.yaml:ro
      - ./migrations:/migrations:ro

  app:
    image: your-app
    depends_on:
      postgres-provisioner:
        condition: service_completed_successfully
```
