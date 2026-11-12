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
  - name: "postgres_exporter"
    password: "${POSTGRES_EXPORTER_PASSWORD}"
    member_of: ["pg_monitor"]          # role memberships (see below)
    options:
      login: true

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

### Database options

Databases accept an optional `options:` block for attributes that are fixed at
`CREATE DATABASE` time and cannot be changed afterward. They apply only when the
database is first created; if it already exists the options are ignored and a
warning is logged.

```yaml
databases:
  - name: "synapse"
    owner: "synapse"
    options:
      encoding: "UTF8"       # character set encoding
      lc_collate: "C"        # string sort order
      lc_ctype: "C"          # character classification
      template: "template0"  # template database to clone
      # locale: "C"          # sets lc_collate + lc_ctype together;
      #                        mutually exclusive with lc_collate / lc_ctype
```

### Role memberships

`roles[].member_of` grants a role membership in other roles (`GRANT <group> TO <role>`).
Entries can be roles declared in `roles` or PostgreSQL's
[predefined roles](https://www.postgresql.org/docs/current/predefined-roles.html)
such as `pg_monitor`, `pg_read_all_stats`, or `pg_read_all_data`. Memberships are
applied after all roles exist, so ordering in the config doesn't matter. Existing
memberships are skipped, and memberships not listed are never revoked (additive only).
With `strategy: create`, memberships are only applied to newly created roles.
Circular memberships among declared roles (e.g. `a` member of `b` and `b` member
of `a`) are rejected at config validation.

**Example: a read-only monitoring user for `postgres_exporter`** — replaces the
admin/superuser account in the exporter's `DATA_SOURCE_NAME`:

```yaml
roles:
  - name: "postgres_exporter"
    password: "${POSTGRES_EXPORTER_PASSWORD}"
    member_of: ["pg_monitor"]   # pg_read_all_settings + pg_read_all_stats + pg_stat_scan_tables
    options:
      login: true
      connection_limit: 5
```

`pg_monitor` can read all statistics views (`pg_stat_activity` including other
sessions' queries, `pg_stat_replication`, `pg_stat_statements`, WAL/size functions)
but cannot read or modify table data. Databases grant `CONNECT` to `PUBLIC` by
default; if you revoked it, add an `on_database` `CONNECT` grant for the exporter role
on each database it should scrape.

**PostgreSQL 16+ notes:**

- The connecting admin user must hold `ADMIN OPTION` on each group role it grants
  (e.g. `GRANT pg_monitor TO admin WITH ADMIN OPTION`) unless it is a superuser;
  `CREATEROLE` alone no longer suffices.
- A membership's `INHERIT` flag is fixed when it is granted, taken from the member
  role's `inherit` attribute at that moment. A role created with `inherit: false`
  gets a non-inheriting membership (it must `SET ROLE` to use the group's
  privileges), and changing it to `inherit: true` later does **not** update the
  existing membership — the provisioner sees it as already granted. Fix it manually
  with `GRANT <group> TO <role> WITH INHERIT TRUE`, or revoke it and re-run.

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `POSTGRES_USER` | yes | - | Admin user (or `POSTGRES_USER_FILE`) |
| `POSTGRES_PASSWORD` | yes | - | Admin password (or `POSTGRES_PASSWORD_FILE`) |
| `POSTGRES_HOST` | no | `localhost` | PostgreSQL host |
| `POSTGRES_PORT` | no | `5432` | PostgreSQL port |
| `POSTGRES_DB` | no | `postgres` | Admin database |
| `POSTGRES_SSLMODE` | no | `disable` | SSL mode |
| `PGHELPER_CONFIG_PATH` | no | `./config.yaml` | Path to YAML config |
| `MIGRATIONS_ENABLED` | no | `true` | Set to `false` to skip all migrations at runtime |
| `DRY_RUN` | no | `false` | Set to `true` to log every mutation as a preview without applying it |
| `LOG_LEVEL` | no | `info` | Log level (debug/info/warn/error) |

Every variable above can alternatively be read from a file by setting `<NAME>_FILE` to its
path (trailing newlines trimmed). Setting both `<NAME>` and `<NAME>_FILE` is an error.

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

String values support `${VAR}` references, resolved when the config is loaded:

| Syntax | Behavior |
|---|---|
| `${VAR}` | Value of `VAR`; falls back to the contents of the file named by `VAR_FILE`. **Fails if neither is set.** |
| `${VAR:-default}` | Like `${VAR}`, but uses `default` when neither `VAR` nor `VAR_FILE` is set. |
| `${VAR:-}` | Optional value: empty string when unset. |

```yaml
password: "${APP_DB_PASSWORD}"          # from APP_DB_PASSWORD or the file in APP_DB_PASSWORD_FILE
name: "${APP_DB_NAME:-myapp}"           # with a default
```

Rules:

- **Unresolved references fail loading.** Every missing variable is reported in one error,
  with its config path (e.g. `roles[0].password: environment variable APP_DB_PASSWORD is not set`).
- **`*_FILE` secrets** (Docker secrets / Kubernetes secret volumes): the file content is used
  with trailing newlines trimmed (max 64 KiB). Setting both `VAR` and `VAR_FILE` is an error.
- **Passwords must not resolve to empty.** A `password` containing a reference that expands
  to `""` fails, since an empty password would silently provision the role without one.
- Error messages never contain resolved values or file contents.
- Default values cannot contain `}` or another reference: nested references such as
  `${A:-${B}}` are rejected with an error.

```yaml
# docker-compose.yaml
services:
  postgres-provisioner:
    environment:
      POSTGRES_PASSWORD_FILE: /run/secrets/pg_admin_password
      APP_DB_PASSWORD_FILE: /run/secrets/app_db_password
    secrets: [pg_admin_password, app_db_password]
```

## Provisioning Order

1. **Roles** — created or updated idempotently
2. **Role memberships** — `GRANT <group> TO <role>` for each `member_of` entry not yet granted
3. **Databases** — created idempotently, owner set
4. **Extensions** — `CREATE EXTENSION IF NOT EXISTS` (per-database)
5. **Schemas** — `CREATE SCHEMA IF NOT EXISTS` with owner (per-database). Owner defaults to database owner if not specified.
6. **Grants** — `GRANT` statements are inherently idempotent (per-database). `on_tables_in_schema` also sets `ALTER DEFAULT PRIVILEGES` for future tables.
7. **Migrations** — Flyway-style SQL files executed per-database (if configured and enabled).

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
