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

### Settings

Run-time parameter defaults (`ALTER ROLE/DATABASE ... SET`) for roles, databases, and
roles within one database. They take effect for new sessions.

```yaml
roles:
  - name: "app_user"
    settings:
      statement_timeout: "30s"
      search_path: ["app", "$user", "public"]   # list settings: one element per schema
      work_mem: null                             # null resets (ALTER ROLE ... RESET)

databases:
  - name: "myapp"
    settings:
      TimeZone: "UTC"                            # ALTER DATABASE myapp SET timezone = 'UTC'
    role_settings:
      app_user:                                  # ALTER ROLE app_user IN DATABASE myapp SET ...
        statement_timeout: "5min"
```

- Values are scalars (strings, numbers, booleans) or lists. Write list settings such as
  `search_path` as a YAML list: `search_path: "app, public"` would name a single schema
  called `app, public`. Values support `${VAR}` expansion.
- Only settings whose stored value differs are changed. Removing a setting from the config
  leaves it in place (additive); use `null` to reset it explicitly. Only the literal
  `null` resets: an empty value (`work_mem:`) or `~` is rejected as a likely forgotten
  value, and so are null or empty list elements.
- Names are case-insensitive and validated (`[A-Za-z_][A-Za-z0-9_$]*`, optionally with a
  `prefix.` for custom parameters). Whether PostgreSQL accepts a parameter for a role or
  database (e.g. `shared_buffers` can't be set per role) is only checked when it's applied.
- Use canonical parameter names as listed in `pg_settings`. Obsolete aliases such as
  `sort_mem` are accepted by PostgreSQL but stored under the canonical name (`work_mem`),
  so they never compare as up to date and are re-applied on every run.
- With `strategy: create`, role `settings` are only applied to newly created roles.
  Database `settings` and `role_settings` follow the database's strategy: they are only
  applied when the database is newly created, regardless of the role's strategy.
- Settings also apply to the provisioner's own sessions (it reconnects to each database as
  the admin user). To keep them from breaking schema creation or migrations, every
  provisioner connection starts with `statement_timeout=0`, `lock_timeout=0`,
  `idle_in_transaction_session_timeout=0` and `default_transaction_read_only=off`
  (connection startup options, which take precedence over role and database defaults).
  `search_path` is deliberately not overridden: a database `search_path`, or one set for
  the admin user, changes how unqualified names in migrations resolve. Schema-qualify
  names in migrations or `SET search_path` at their top if that matters.

### Extensions

Extensions are listed by name, or as a mapping with options:

```yaml
databases:
  - name: "myapp"
    schemas:
      - name: "crypto"
    extensions:
      - "uuid-ossp"                              # CREATE EXTENSION IF NOT EXISTS "uuid-ossp"
      - name: "pgcrypto"
        schema: "crypto"                         # install into a (provisioned) schema
      - name: "pg_stat_statements"
        version: "1.10"                          # pin; updated via ALTER EXTENSION ... UPDATE TO
        cascade: true                            # also install required extensions
```

- Missing extensions are created with the given `schema`, `version` and `cascade`.
- An installed extension at a different pinned `version` is updated (`ALTER EXTENSION ... UPDATE TO`).
  Without `version`, installed extensions are never updated. `strategy: create` never updates.
- An installed extension in a different `schema` is **not** moved — `ALTER EXTENSION ... SET SCHEMA`
  breaks references to its objects — a warning with the command to run is logged instead.
- **Preload check:** `pg_stat_statements`, `pg_cron` and `timescaledb` only work when their
  library is in the server's `shared_preload_libraries` (which needs a restart). The provisioner
  installs them either way and logs a warning if the library isn't loaded. For postgres_exporter's
  `pg_stat_statements` metrics, start the server with it, e.g. in Docker Compose:

  ```yaml
  postgres:
    image: postgres:16-alpine
    command: ["postgres", "-c", "shared_preload_libraries=pg_stat_statements"]
  ```

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
  this also skips all sub-resources (settings, `role_settings`, extensions, schemas,
  grants) if the database already exists.

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
| `${VAR:-default}` | Like `${VAR}`, but uses `default` when the variable is unset **or empty** (POSIX/Compose semantics). |
| `${VAR:-}` | Optional value: empty string when unset or empty. |
| `$${` | Escape: a literal `${` (not expanded, not validated), e.g. `pa$${ss` → `pa${ss`. |

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
- Error messages never contain resolved values or file contents, and never quote the raw
  text of a `password` field.
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

1. **Roles** — created or updated idempotently, then role `settings`
2. **Role memberships** — `GRANT <group> TO <role>` for each `member_of` entry not yet granted
3. **Databases** — created idempotently, owner set, then database `settings` and `role_settings`
4. **Schemas** — `CREATE SCHEMA IF NOT EXISTS` with owner (per-database). Owner defaults to database owner if not specified.
5. **Extensions** — `CREATE EXTENSION IF NOT EXISTS` (per-database). Runs after schemas, so extensions can be installed into a provisioned schema.
6. **Revokes** — `REVOKE` for listed privileges the grantee currently holds (per-database). Runs before grants.
7. **Grants** — `GRANT` statements are inherently idempotent (per-database). `on_tables_in_schema` also sets `ALTER DEFAULT PRIVILEGES` for future tables.
8. **Migrations** — Flyway-style SQL files executed per-database (if configured and enabled).
9. **Object grants** — `on_tables` grants (per-database), after migrations so they can target tables migrations create.

## Grant Types

Each grant sets exactly one target. Privileges are validated per target when the config is loaded,
and `ALL` must be the only privilege in its grant:

| Target | Applies to | Allowed privileges |
|---|---|---|
| `on_database: true` | the database itself | `CONNECT`, `CREATE`, `TEMPORARY`/`TEMP`, `ALL` |
| `on_schema: "name"` | the schema | `USAGE`, `CREATE`, `ALL` |
| `on_tables_in_schema: "name"` | all tables in the schema | `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`, `REFERENCES`, `TRIGGER`, `ALL` |
| `on_sequences_in_schema: "name"` | all sequences in the schema | `USAGE`, `SELECT`, `UPDATE`, `ALL` |
| `on_functions_in_schema: "name"` | all functions and procedures in the schema | `EXECUTE`, `ALL` |
| `on_tables: ["schema.table", ...]` | specific tables (optionally `columns`) | table privileges; with `columns`: `SELECT`, `INSERT`, `UPDATE`, `REFERENCES`, `ALL` |

The `*_in_schema` targets grant on all existing objects and set `ALTER DEFAULT PRIVILEGES`
for objects created later, both by the provisioning user (e.g. via migrations) and by the
schema owner (the schema's `owner`, falling back to the database owner). If neither a
schema owner nor a database owner is configured, only the provisioning user's default
privileges are set. Objects created by any other role are not covered by the default
privileges; they get the grant on the next run.

**Tables with `serial` columns** also need sequence privileges, or inserts fail with
`permission denied for sequence`. Identity columns (`GENERATED ... AS IDENTITY`) don't:

```yaml
grants:
  - { role: app, privileges: [USAGE], on_schema: app }
  - { role: app, privileges: [SELECT, INSERT, UPDATE, DELETE], on_tables_in_schema: app }
  - { role: app, privileges: [USAGE, SELECT], on_sequences_in_schema: app }
```

**Specific tables and columns:**

```yaml
grants:
  - role: reporting
    privileges: [SELECT]
    on_tables: ["app.users", "app.orders"]   # schema-qualified, used verbatim (case-sensitive)
    columns: ["id", "email"]                 # optional: GRANT SELECT ("id", "email") ON TABLE ...
```

`on_tables` grants run **after migrations**, so they can target tables created by them in
the same run. Unlike the `*_in_schema` targets they don't set default privileges.
What happens to a table that still doesn't exist depends on whether this run ran migrations
for the database:

| Migrations for the database | Missing table |
|---|---|
| configured and enabled | provisioning fails, naming the missing tables (dry-run only logs them, since migrations are only previewed) |
| disabled (`MIGRATIONS_ENABLED=false`) or not configured | warning naming the missing tables; the grant is applied to the tables that exist, and the next run picks up the rest |

The second case covers applications that run their own migrations, e.g. with the provisioner
as an init container: on a fresh database the tables don't exist yet, so the grant is
completed by the next provisioning run after the application created them.

Besides tables (including partitioned tables), `on_tables` accepts **views, materialized
views and foreign tables**. Other relations such as indexes, sequences or composite types
are rejected with an error naming their kind (use `on_sequences_in_schema` for sequences).

Table and column names are quoted for you and matched **case-sensitively**: `App.Orders`
targets `"App"."Orders"`, not `app.orders`. Write them unquoted — names containing `"` are
rejected at load time. If a table isn't found but exists with different case, the error
suggests the matching name.

**Functions:** PostgreSQL grants `EXECUTE` to `PUBLIC` by default, so `on_functions_in_schema`
matters mainly for functions whose `EXECUTE` was revoked from `PUBLIC`.

## Revokes

The provisioner is otherwise additive, but `revokes` explicitly removes privileges —
typically PostgreSQL's defaults for `PUBLIC` (every role), to harden a database:

```yaml
databases:
  - name: "myapp"
    revokes:
      - { role: PUBLIC, privileges: [ALL], on_database: true }    # CONNECT + TEMPORARY + CREATE
      - { role: PUBLIC, privileges: [CREATE], on_schema: public }
    grants:
      - { role: app_user, privileges: [CONNECT], on_database: true }
```

- Same shape as grants. Supported targets: `on_database` and `on_schema`.
- `role` is a declared role or `PUBLIC` (case-insensitive), the pseudo-role covering every role.
  New databases grant `CONNECT` and `TEMPORARY` to `PUBLIC` by default. Because any case means
  the pseudo-role, declaring a role named `public` in any case (PostgreSQL allows quoted
  `"PUBLIC"` or `"Public"`) is a config error.
- Revokes run **before** grants, so revoking from `PUBLIC` and granting to specific roles converges.
- Only privileges the grantee currently holds are revoked; otherwise the revoke is logged as
  already applied and skipped.
- A privilege both granted and revoked for the same role and object is a config error, as
  are duplicate revokes.
- `REVOKE` only removes grants made by a specific grantor. As a superuser, the provisioner
  revokes as the object owner, removing the owner's grants (including the defaults). A
  non-superuser revokes as itself (or as the owner, if it is a member of the owning role) and
  removes only those grants; for others, PostgreSQL only emits a server `WARNING`, not an
  error. Either way, if a privilege survives because another grantor granted it, the
  provisioner logs a warning.
- Revoking from a role only removes privileges granted to that role directly. The role still
  has whatever it gets through `PUBLIC` or through membership in other roles: revoke from
  `PUBLIC` (or from those roles) as well.
- Revokes never use `CASCADE`. If the grantee holds a privilege `WITH GRANT OPTION` and has
  granted it on to other roles, PostgreSQL rejects the `REVOKE` with "dependent privileges
  exist" and the run fails with a hint. Revoke the dependent grants first (or run
  `REVOKE ... CASCADE` manually if removing them is intended).
- In dry-run, a revoke on a schema created in the same run is previewed against a new
  schema's defaults: only its owner holds privileges, so revoking from `PUBLIC` would do nothing.
- With `strategy: create`, revokes (like other per-database resources) only apply to newly
  created databases. Use `strategy: update` to harden an existing database.

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
