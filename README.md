# postgres-provisioner

A Go CLI tool that idempotently provisions PostgreSQL resources (roles, databases, extensions, schemas, grants) from a YAML config file. Designed as a Docker Compose init container that runs before your application starts.

## Quick Start

```bash
# Build
go build -o postgres-provisioner ./cmd/postgres-provisioner

# Or use Docker Compose
docker compose up
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
| `LOG_LEVEL` | no | `info` | Log level (debug/info/warn/error) |

## Provisioning Order

1. **Roles** — created or updated idempotently
2. **Databases** — created idempotently, owner set
3. **Extensions** — `CREATE EXTENSION IF NOT EXISTS` (per-database)
4. **Schemas** — `CREATE SCHEMA IF NOT EXISTS` with owner (per-database)
5. **Grants** — `GRANT` statements are inherently idempotent (per-database)

## Grant Types

- `on_database: true` — grants privileges on the database itself (e.g., `CONNECT`)
- `on_schema: "name"` — grants privileges on a schema (e.g., `ALL`, `USAGE`)
- `on_tables_in_schema: "name"` — grants on all existing tables + sets `ALTER DEFAULT PRIVILEGES` for future tables

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
    build: .
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

  app:
    image: your-app
    depends_on:
      postgres-provisioner:
        condition: service_completed_successfully
```
