# Configuration reference

The complete reference for the YAML config. For a guided introduction, start with
[Getting started](getting-started.md). A commented example with most fields is in
[config.example.yaml](../config.example.yaml).

- [Top level](#top-level)
- [Roles](#roles): [options](#role-options), [memberships](#role-memberships), [settings](#settings)
- [Databases](#databases): [create options](#database-options), [schemas](#schemas),
  [extensions](#extensions), [grants](#grants), [revokes](#revokes),
  [migrations](#migrations), [publications](#publications)
- [Environment variable expansion](#environment-variable-expansion)
- [Validation](#validation)

## Top level

```yaml
strategy: update        # optional: "update" (default) or "create"
roles: [...]
databases: [...]
```

`strategy` sets the default for all roles and databases; each can override it. See
[Strategy](concepts.md#strategy-update-or-create).

## Roles

```yaml
roles:
  - name: shop_app                     # required
    password: ${SHOP_APP_PASSWORD}     # optional
    options: { login: true }           # optional, see below
    member_of: [pg_monitor]            # optional
    settings: { statement_timeout: 30s }  # optional
    strategy: update                   # optional
```

| Field | Description |
|---|---|
| `name` | Role name, used verbatim (case-sensitive). `public` in any case is reserved. |
| `password` | Set on creation and, with `strategy: update`, on every run. Omit it to create a role without a password, or to leave an existing password alone. Must not resolve to an empty string. |
| `options` | Role attributes, see [Role options](#role-options). |
| `member_of` | Roles this role becomes a member of, see [Role memberships](#role-memberships). |
| `settings` | Session defaults (`ALTER ROLE ... SET`), see [Settings](#settings). |
| `strategy` | `update` or `create`, overrides the top-level strategy. |

### Role options

Every option is optional. An option you don't set is left at PostgreSQL's default on
creation and is not changed on existing roles.

| Option | Type | SQL | PostgreSQL default |
|---|---|---|---|
| `login` | bool | `LOGIN` / `NOLOGIN` | `false` |
| `superuser` | bool | `SUPERUSER` / `NOSUPERUSER` | `false` |
| `createdb` | bool | `CREATEDB` / `NOCREATEDB` | `false` |
| `createrole` | bool | `CREATEROLE` / `NOCREATEROLE` | `false` |
| `inherit` | bool | `INHERIT` / `NOINHERIT` | `true` |
| `replication` | bool | `REPLICATION` / `NOREPLICATION` | `false` |
| `bypassrls` | bool | `BYPASSRLS` / `NOBYPASSRLS` | `false` |
| `connection_limit` | int | `CONNECTION LIMIT n` (`-1` = unlimited) | `-1` |
| `valid_until` | string | `VALID UNTIL '...'`, password expiry, e.g. `"2030-01-01"` or `"infinity"` | no expiry |

### Role memberships

`member_of` grants the role membership in other roles (`GRANT <group> TO <role>`):

```yaml
roles:
  - name: postgres_exporter
    password: ${POSTGRES_EXPORTER_PASSWORD}
    member_of: [pg_monitor]
    options: { login: true }
```

- Entries are roles declared in `roles`, or PostgreSQL's
  [predefined roles](https://www.postgresql.org/docs/current/predefined-roles.html) such as
  `pg_monitor`, `pg_read_all_stats`, `pg_read_all_data` or `pg_write_all_data`, which need not
  be declared.
- Memberships are granted after all roles exist, so the order of roles doesn't matter.
- Existing memberships are skipped; memberships not listed are never revoked.
- With `strategy: create`, memberships are only granted to newly created roles.
- Circular memberships among declared roles are rejected at validation.

**PostgreSQL 16+:**

- A non-superuser admin needs `ADMIN OPTION` on each group role it grants
  (`GRANT pg_monitor TO admin WITH ADMIN OPTION`); `CREATEROLE` alone no longer suffices.
- A membership's `INHERIT` flag is fixed when it is granted, taken from the member's
  `inherit` option at that moment. Changing `inherit` later doesn't update existing
  memberships, and the provisioner sees them as granted. Fix by hand with
  `GRANT <group> TO <role> WITH INHERIT TRUE`, or revoke the membership and re-run.

See [Monitor Postgres with postgres_exporter](howto/monitoring.md).

### Settings

Run-time parameter defaults for roles, databases, and roles within one database. They take
effect for new sessions.

```yaml
roles:
  - name: shop_app
    settings:                                    # ALTER ROLE shop_app SET ...
      statement_timeout: "30s"
      search_path: ["app", "$user", "public"]    # list settings: one element per entry
      work_mem: null                             # null resets (ALTER ROLE ... RESET)

databases:
  - name: shop
    settings:                                    # ALTER DATABASE shop SET ...
      TimeZone: "UTC"
    role_settings:                               # ALTER ROLE ... IN DATABASE shop SET ...
      shop_app:
        statement_timeout: "5min"
```

- **Values** are scalars (strings, numbers, booleans) or lists. Write list settings such as
  `search_path` as a YAML list: `search_path: "app, public"` would name a single schema called
  `app, public`. Values support `${VAR}` expansion.
- **Only differing values are changed.** Current values are read from `pg_db_role_setting`.
- **Removing a setting from the config leaves it in place.** Use `null` to reset it. Only
  the literal `null` resets: an empty value (`work_mem:`) or `~` is rejected as a likely
  forgotten value, as are null or empty list elements.
- **Names** are case-insensitive and validated (`[A-Za-z_][A-Za-z0-9_$]*`, optionally with a
  `prefix.` for custom parameters). Whether PostgreSQL accepts a parameter for a role or
  database (e.g. `shared_buffers` can't be set per role) is only checked when it's applied.
- **Use canonical names** as listed in `pg_settings`. Obsolete aliases such as `sort_mem`
  are stored under the canonical name (`work_mem`), so they never compare as up to date and
  are re-applied on every run.
- **Strategy:** with `strategy: create`, role `settings` are only applied to newly created
  roles. Database `settings` and `role_settings` follow the *database's* strategy.
- `role_settings` keys must be roles declared in `roles`.
- Settings don't affect the provisioner's own sessions for timeouts and read-only mode; see
  [Sessions the provisioner opens](concepts.md#sessions-the-provisioner-opens).

## Databases

```yaml
databases:
  - name: shop                   # required
    owner: shop_app              # optional, must be declared in roles
    strategy: update             # optional
    options: {...}               # create-time options
    settings: {...}              # ALTER DATABASE ... SET, see Settings
    role_settings: {...}         # ALTER ROLE ... IN DATABASE ... SET, see Settings
    schemas: [...]
    extensions: [...]
    revokes: [...]
    grants: [...]
    migrations: {...}
    publications: [...]
```

| Field | Description |
|---|---|
| `name` | Database name. `postgres`, `template0` and `template1` are reserved. |
| `owner` | Owner on creation and, with `strategy: update`, on every run. Without it, the admin user owns the database. |
| `strategy` | `update` or `create`. With `create`, an existing database is skipped **including everything inside it**. |

### Database options

Attributes fixed at `CREATE DATABASE` time. They only apply when the database is created;
for an existing database they are ignored and a warning is logged.

```yaml
databases:
  - name: synapse
    owner: synapse
    options:
      encoding: "UTF8"       # character set encoding
      lc_collate: "C"        # string sort order
      lc_ctype: "C"          # character classification
      template: "template0"  # template database to clone
      # locale: "C"          # sets lc_collate + lc_ctype together;
      #                        mutually exclusive with lc_collate / lc_ctype
```

A different encoding or locale than the template's usually requires `template: template0`.

### Schemas

```yaml
    schemas:
      - name: app
        owner: shop_app      # optional, defaults to the database owner
```

Created with `CREATE SCHEMA IF NOT EXISTS`. With `strategy: update`, the owner is set on every
run. The owner must be declared in `roles`.

### Extensions

Listed by name, or as a mapping with options:

```yaml
    schemas:
      - name: crypto
    extensions:
      - "uuid-ossp"                    # CREATE EXTENSION IF NOT EXISTS "uuid-ossp"
      - name: pgcrypto
        schema: crypto                 # install into a (provisioned) schema
      - name: pg_stat_statements
        version: "1.10"                # pin; updated via ALTER EXTENSION ... UPDATE TO
        cascade: true                  # also install required extensions
```

- Missing extensions are created with the given `schema`, `version` and `cascade`.
- **Without `schema`, PostgreSQL picks it:** the schema fixed by the extension's control file,
  if any. Otherwise it's the first existing schema in the admin connection's `search_path`.
  With the default `search_path` (`"$user", public`), a schema named like the admin login
  role (e.g. a provisioned schema `admin` when `POSTGRES_USER=admin`) is chosen over
  `public`. Schemas are provisioned before extensions, so such a schema already exists. Set
  `schema: public` explicitly if that matters.
- **Version:** an installed extension at a different pinned `version` is updated. Without
  `version`, installed extensions are never updated, and `strategy: create` never updates. If
  the server has no update path (a downgrade, or a version it doesn't ship, per
  `pg_extension_update_paths`), a warning is logged and the installed version is kept.
- **Schema of an installed extension:** it is **not** moved, because
  `ALTER EXTENSION ... SET SCHEMA` breaks references to its objects. A warning is logged
  instead. Its hint suggests `SET SCHEMA` only for relocatable extensions; others must be
  dropped and recreated.
- **`cascade: true`** installs missing dependencies at their default versions, into the
  dependent extension's configured `schema` (or each dependency's default schema). To give a
  dependency its own `schema` or `version`, list it separately **before** the dependent
  extension.
- **Preload check:** `pg_stat_statements`, `pg_cron` and `timescaledb` need their library in
  the server's `shared_preload_libraries` (which needs a restart). Before touching a
  database, the provisioner reads the setting once per run:
  - `pg_cron`, `timescaledb`: `CREATE EXTENSION` fails without preloading, so if the extension
    isn't installed yet the run **fails** with an error naming the server flag to add. If
    it is installed, a warning is logged.
  - `pg_stat_statements`: installs fine but fails when used, so it is installed and a warning
    is logged.

  If the admin can't read the setting (not a superuser and no `pg_read_all_settings`), the
  check is skipped and logged at info level.
- Duplicate extensions and unknown keys in a mapping (e.g. a misspelled `shema`) are rejected.

### Grants

Each grant gives one role privileges on exactly one target:

```yaml
    grants:
      - role: shop_app                 # must be declared in roles
        privileges: [SELECT, INSERT]
        on_tables_in_schema: app       # exactly one target
```

| Target | Applies to | Allowed privileges |
|---|---|---|
| `on_database: true` | the database itself | `CONNECT`, `CREATE`, `TEMPORARY`/`TEMP`, `ALL` |
| `on_schema: "name"` | the schema | `USAGE`, `CREATE`, `ALL` |
| `on_tables_in_schema: "name"` | all tables, views, materialized views and foreign tables in the schema | `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`, `REFERENCES`, `TRIGGER`, `ALL` |
| `on_sequences_in_schema: "name"` | all sequences in the schema | `USAGE`, `SELECT`, `UPDATE`, `ALL` |
| `on_functions_in_schema: "name"` | all functions and procedures in the schema | `EXECUTE`, `ALL` |
| `on_tables: ["schema.table", ...]` | specific relations, optionally `columns` | table privileges; with `columns`: `SELECT`, `INSERT`, `UPDATE`, `REFERENCES`, `ALL` |

Privileges are case-insensitive and validated per target when the config is loaded. `ALL`
must be the only privilege in its grant. `GRANT` is idempotent, so grants are issued on every
run.

**Future objects:** the `*_in_schema` targets grant on all existing objects and set
`ALTER DEFAULT PRIVILEGES` for objects created later by the provisioning user (e.g. through
migrations) and by the schema owner (the schema's `owner`, or the database owner). If
neither owner is configured, only the provisioning user's default privileges are set.
Objects created by any other role get the grant on the next run.

**Sequences:** tables with `serial` columns also need sequence privileges, or inserts fail
with `permission denied for sequence`. Identity columns (`GENERATED ... AS IDENTITY`) don't:

```yaml
    grants:
      - { role: app, privileges: [USAGE], on_schema: app }
      - { role: app, privileges: [SELECT, INSERT, UPDATE, DELETE], on_tables_in_schema: app }
      - { role: app, privileges: [USAGE, SELECT], on_sequences_in_schema: app }
```

**Functions:** PostgreSQL grants `EXECUTE` to `PUBLIC` by default, so
`on_functions_in_schema` matters mainly for functions whose `EXECUTE` was revoked from
`PUBLIC`.

#### Tables and columns

```yaml
    grants:
      - role: reporting
        privileges: [SELECT]
        on_tables: ["app.users", "app.orders"]   # schema-qualified, case-sensitive
        columns: ["id", "email"]                 # optional: GRANT SELECT ("id", "email") ON TABLE ...
```

- `on_tables` grants run **after migrations**, so they can target tables the migrations
  create in the same run. They don't set default privileges.
- Accepted relations: tables (including partitioned tables), views, materialized views and
  foreign tables. Others, such as indexes or sequences, are rejected with an error naming their
  kind.
- Names are quoted for you and matched **case-sensitively**: `App.Orders` targets
  `"App"."Orders"`. Write them unquoted; names containing `"` are rejected. If a table isn't
  found but exists with different case, the error suggests the matching name.
- **Missing tables:**

  | Migrations for the database in this run | Missing table |
  |---|---|
  | configured and enabled | provisioning fails, naming the missing tables (dry-run only logs them) |
  | disabled (`MIGRATIONS_ENABLED=false`) or not configured | warning; the grant is applied to the existing tables, the next run picks up the rest |

  See [When the application runs its own migrations](howto/app-migrations.md).

### Revokes

The provisioner is otherwise additive. `revokes` explicitly removes privileges, typically
PostgreSQL's defaults for `PUBLIC`:

```yaml
    revokes:
      - { role: PUBLIC, privileges: [ALL], on_database: true }    # CONNECT + TEMPORARY + CREATE
      - { role: PUBLIC, privileges: [CREATE], on_schema: public }
    grants:
      - { role: shop_app, privileges: [CONNECT], on_database: true }
```

- Same shape as grants. Supported targets: `on_database` and `on_schema`.
- `role` is a declared role or `PUBLIC` (case-insensitive), the pseudo-role covering every
  role. A declared role named `public` in any case is therefore a config error.
- Revokes run **before** grants, so revoking from `PUBLIC` and granting to specific roles
  converges.
- Only privileges the grantee currently holds are revoked; otherwise the revoke is logged
  as already applied.
- A privilege both granted and revoked for the same role and object is a config error, as
  are duplicate revokes.
- `REVOKE` only removes grants made by a specific grantor. As a superuser, the provisioner
  revokes as the object owner, removing the owner's grants, including the defaults. A
  non-superuser revokes as itself, or as the owner if it is a member of the owning role. If a
  privilege survives because another grantor granted it, a warning is logged.
- Revoking from a role only removes privileges granted to that role directly. Privileges
  through `PUBLIC` or other roles remain.
- Revokes never use `CASCADE`. If the grantee passed on a privilege it holds
  `WITH GRANT OPTION`, PostgreSQL rejects the revoke ("dependent privileges exist"), and the
  run fails with a hint.
- In dry-run, a revoke on a schema created in the same run is previewed against a new
  schema's defaults: only its owner holds privileges.
- With `strategy: create`, revokes only apply to newly created databases.

See [Harden a database](howto/harden-database.md).

### Migrations

```yaml
    migrations:
      directory: /migrations/shop      # supports ${VAR} expansion
```

SQL files in `directory` are applied in order. Files are matched by name; anything else
(subdirectories, `README.md`, …) is ignored.

| Pattern | Kind | Behavior |
|---|---|---|
| `V<number>__<description>.sql` | versioned | Runs once. Its SHA-256 checksum is recorded; if an applied file changes, the run fails (versioned migrations are immutable). |
| `R<number>__<description>.sql` | repeatable | Runs again whenever its content changes. Useful for views, functions, seed data. |

- **Order:** all versioned migrations by number, then all repeatable migrations by number.
  `V2` runs before `V10`; the number of leading zeros doesn't matter. Two files with the same
  kind and number are an error.
- **Transactions:** each file runs in its own transaction, together with its tracking
  record. A failing file is rolled back and the run stops; files before it stay applied.
  Statements that can't run in a transaction (`CREATE INDEX CONCURRENTLY`,
  `ALTER TYPE ... ADD VALUE` on older versions) aren't supported.
- **Tracking:** in a `_schema_migrations` table created in the database (in the first schema of
  the `search_path`, usually `public`).
- **Ownership:** migrations run as the admin user, which therefore owns the objects they
  create. Grant access through [grants](#grants), or change ownership in the migration
  (`ALTER TABLE ... OWNER TO shop_app`).
- **Relative paths** resolve from the working directory: `/` in the Docker image.
- Empty files are rejected. There are no down migrations.
- Disable migrations at runtime with `--migrations=false` or `MIGRATIONS_ENABLED=false`.

### Publications

Logical replication publications, e.g. as the source for CDC with Debezium or Redpanda
Connect:

```yaml
    publications:
      - name: cdc
        tables: ["app.orders", "billing.invoices"]   # schema-qualified
        publish: [insert, update, delete]           # optional; default: all incl. truncate
      - name: everything
        all_tables: true                            # FOR ALL TABLES (requires superuser)
```

- Each publication sets exactly one of `tables` or `all_tables`.
- Provisioned **after migrations** (and `on_tables` grants). Missing tables are handled like
  for `on_tables` grants. With migrations disabled or not configured, the publication is
  created with, or extended by, the existing tables (creation is skipped if none exist yet).
- `tables` must be tables or partitioned tables (published with their partitions). Views and
  other relations are rejected before the publication is created or changed.
- Existing publications are reconciled **additively**: a changed `publish` list is applied
  and missing tables are added. Tables that are no longer configured are **not** dropped; a
  warning is logged. `all_tables` can't be changed on an existing publication (warning). With
  `strategy: create`, existing publications are left as-is.
- Prerequisites the provisioner can't set: `wal_level=logical` on the server (a warning is
  logged otherwise), and a primary key or `REPLICA IDENTITY` on tables published for
  `update`/`delete`.
- Replication slots are not managed: CDC consumers create their own.

See [Change data capture with publications](howto/cdc-publications.md).

## Environment variable expansion

String values support `${VAR}` references, resolved when the config is loaded:

| Syntax | Behavior |
|---|---|
| `${VAR}` | Value of `VAR`; falls back to the contents of the file named by `VAR_FILE`. **Fails if neither is set.** |
| `${VAR:-default}` | Like `${VAR}`, but uses `default` when the variable is unset **or empty**. |
| `${VAR:-}` | Optional value: empty string when unset or empty. |
| `$${` | A literal `${`, not expanded: `pa$${ss` → `pa${ss`. |

```yaml
password: "${APP_DB_PASSWORD}"          # from APP_DB_PASSWORD or the file in APP_DB_PASSWORD_FILE
name: "${APP_DB_NAME:-myapp}"           # with a default
```

- **Unresolved references fail loading.** Every missing variable is reported in one error,
  with its config path (`roles[0].password: environment variable APP_DB_PASSWORD is not set (set APP_DB_PASSWORD or APP_DB_PASSWORD_FILE)`).
- **`*_FILE` secrets:** the file content is used with trailing newlines trimmed (max 64 KiB).
  Setting both `VAR` and `VAR_FILE` is an error.
- **Passwords must not resolve to empty.** That would silently create the role without a
  password.
- Error messages never contain resolved values or file contents, and never quote the raw text
  of a `password` field.
- Defaults can't contain `}` or another reference: `${A:-${B}}` is rejected.
- Expansion applies to all string fields: names, owners, passwords, settings, paths, table
  names.

See [Manage secrets](howto/secrets.md).

## Validation

The whole config is validated before the provisioner connects, and errors name the exact
path, e.g. `databases[0].grants[1].privileges[0]: privilege "SELECT" is not valid for
on_database (allowed: ALL, CONNECT, CREATE, TEMP, TEMPORARY)`. Among other things, it checks:

- required fields (`name`, `role`, `privileges`, a grant target, `migrations.directory`),
- that owners, grantees and `role_settings` keys are declared in `roles`,
- duplicate roles, databases, schemas, extensions and revokes,
- privileges per grant target, `ALL` combined with others, grant/revoke conflicts,
- circular and self memberships,
- reserved names (`public` roles; `postgres`, `template0`, `template1` databases),
- setting names and values, publication fields, `strategy` values, null bytes.
