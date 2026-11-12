# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Role memberships (`member_of`)** — `roles[].member_of` grants a role membership in other roles (`GRANT <group> TO <role>`), including PostgreSQL predefined roles such as `pg_monitor`. This enables a least-privilege, non-superuser account for `postgres_exporter` instead of the admin user. Memberships are applied in a separate pass after all roles are provisioned (config order doesn't matter), already-granted memberships are skipped, nothing is ever revoked, and `strategy: create` skips memberships for pre-existing roles. Circular memberships among declared roles are rejected at validation. Supported in dry-run mode.
- **`*_FILE` secret support** — every `${VAR}` reference falls back to the contents of the file named by `VAR_FILE` (Docker secrets / Kubernetes secret volumes; trailing newlines trimmed, max 64 KiB). The provisioner's own environment variables (`POSTGRES_USER`, `POSTGRES_PASSWORD`, …) accept `<NAME>_FILE` as well. Setting both `VAR` and `VAR_FILE` is an error.
- **Default values** — `${VAR:-default}` uses `default` when the variable is unset or empty (POSIX/Docker Compose semantics); `${VAR:-}` marks a value as optional.
- **Escape for a literal `${`** — `$${` yields `${` without expansion or validation (e.g. a password containing `${`).
- **Explicit revokes** — `databases[].revokes` (same shape as grants, `on_database` / `on_schema` targets) removes listed privileges, e.g. `REVOKE CONNECT ON DATABASE ... FROM PUBLIC` to harden a database. `PUBLIC` (case-insensitive) is emitted as the bare keyword and doesn't need to be declared. Revokes run before grants, only revoke privileges the grantee actually holds (checked via `aclexplode`), warn if a privilege survives because another grantor granted it, and support dry-run. Revokes that conflict with a grant or duplicate another revoke are rejected at load time. Declaring a role named `public` in any case is now rejected, since grants and revokes would treat it as the pseudo-role. Revokes never cascade: "dependent privileges exist" (the grantee re-granted a privilege it holds `WITH GRANT OPTION`) fails the run with a hint. In dry-run, revokes on a schema created in the same run are previewed against a new schema's default privileges (owner only).
- **Table- and column-level grants** — new grant target `on_tables: ["schema.table", ...]` with optional `columns: [...]` (`GRANT SELECT ("id", "email") ON TABLE ...`). Targets may be tables, partitioned tables, views, materialized views or foreign tables; other relation kinds (indexes, sequences, composite types) are rejected. These grants run in a new phase **after migrations**, so they can target tables created by migrations in the same run; a table still missing at that point fails provisioning if this run applied the database's migrations (dry-run only logs it). With migrations disabled (`MIGRATIONS_ENABLED=false`, e.g. the app runs its own) or not configured, missing tables only produce a warning and the grant covers the existing tables, so an init container doesn't deadlock on a fresh database; the next run grants the rest. Table names must be schema-qualified and are used verbatim as quoted identifiers (case-sensitive; pre-quoted names containing `"` are rejected, and a missing table's error suggests a case-insensitive match).
- **Sequence and function grants** — new grant targets `on_sequences_in_schema` (`GRANT ... ON ALL SEQUENCES IN SCHEMA` + `ALTER DEFAULT PRIVILEGES ... ON SEQUENCES`) and `on_functions_in_schema` (`ON ALL ROUTINES`, covering functions and procedures, + default privileges `ON FUNCTIONS`). Like `on_tables_in_schema`, both run transactionally and cover objects created later by the provisioning user and the schema owner. Sequence grants are required for `INSERT` into tables with `serial` columns.
- **Extension options** — `databases[].extensions` entries can be a mapping with `name`, `schema`, `version` and `cascade` (plain names keep working). Missing extensions are created with `SCHEMA` / `VERSION` / `CASCADE`; an installed extension whose pinned `version` differs is updated with `ALTER EXTENSION ... UPDATE TO` (not with `strategy: create`; skipped with a warning when `pg_extension_update_paths` has no path, e.g. a downgrade); a differing `schema` is only warned about, since relocating an extension can break dependent objects (the hint suggests `SET SCHEMA` only for relocatable extensions, otherwise drop and recreate). `cascade` installs dependencies at their default versions, into the extension's configured `schema` if given; list a dependency first to give it its own `schema`/`version`.
- **`shared_preload_libraries` check** — when `pg_stat_statements`, `pg_cron` or `timescaledb` is configured, the server's `shared_preload_libraries` is read once per run, before any extension is created (entries like `$libdir/pg_stat_statements` or `pg_stat_statements.so` are normalized). `pg_cron` and `timescaledb` can't be created without their library preloaded: if one isn't installed yet, the run fails early with an error naming the server flag to add. `pg_stat_statements` installs fine but fails when used (e.g. postgres_exporter's metrics), so it is installed and a warning is logged once per library. If the admin role can't read the setting, the check is skipped and logged at info level.

### Changed

- **BREAKING: unresolved `${VAR}` references now fail config loading.** Previously an unset variable was left in place as literal text, so e.g. a missing `APP_DB_PASSWORD` silently set the role's password to the string `${APP_DB_PASSWORD}`. Loading now fails and reports every unresolved reference with its config path. Migration: for values that are intentionally optional, use `${VAR:-}` or `${VAR:-default}`. Role passwords are the exception, because they must not resolve to empty (see below): either omit the `password` field or provide a value.
- **BREAKING: role passwords that resolve to an empty string are rejected.** A `password` containing a reference that expands to `""` would otherwise provision the role without a password.
- **BREAKING: setting both `<NAME>` and `<NAME>_FILE` for the provisioner's own environment variables is an error.** E.g. a deployment that sets both `POSTGRES_PASSWORD` and `POSTGRES_PASSWORD_FILE` (previously the `_FILE` variable was ignored) now fails at startup. Migration: remove one of the two.
- `$${` in config values now yields a literal `${` (escape) instead of `$` followed by an expanded reference.
- Invalid references such as `${foo bar}` or `${}` are now errors instead of being left as literal text.
- Nested references in defaults (`${A:-${B}}`) are rejected; they are not supported and previously produced wrong values (a literal `${B}` or a stray `}`).
- `roles[].options.valid_until` now supports `${VAR}` expansion.
- **Grant privileges are validated per target** — e.g. `SELECT` with `on_database` or `USAGE` with `on_tables_in_schema` is now rejected when the config is loaded, instead of failing partway through provisioning with `invalid privilege type`. Only configs that could never apply successfully are affected.
- A grant with both an invalid privilege and no (or multiple) targets now reports the target error first, since privileges are validated against the resolved target.
- Null bytes in `on_schema` / `on_tables_in_schema` are now rejected during validation, like all other identifier fields.
- **`ALL` can no longer be combined with other privileges** in the same grant or revoke (e.g. `[ALL, SELECT]`). PostgreSQL rejects such a `GRANT` with a syntax error, so these configs could never apply; they now fail at load time with a path-qualified error.
- **Role and database settings** — `roles[].settings` (`ALTER ROLE ... SET`), `databases[].settings` (`ALTER DATABASE ... SET`) and `databases[].role_settings` (`ALTER ROLE ... IN DATABASE ... SET`) set run-time parameter defaults such as `statement_timeout` or `search_path`. Lists are quoted per element (needed for `search_path`), an explicit `null` resets a setting (an empty value or `~` is rejected), and removing a setting from the config leaves it untouched. Current values are read from `pg_db_role_setting`, so unchanged settings are skipped. Supports dry-run and `${VAR}` expansion in values; `strategy: create` skips role settings for existing roles, and database settings and `role_settings` for existing databases (`role_settings` follow the database's strategy, not the role's). Use canonical parameter names: obsolete aliases such as `sort_mem` are stored as `work_mem` and therefore re-applied on every run.
- **Provisioner sessions ignore risky role/database defaults** — every provisioner connection sets `statement_timeout=0`, `lock_timeout=0`, `idle_in_transaction_session_timeout=0` and `default_transaction_read_only=off` as connection startup options, so settings such as a read-only database default or a short `statement_timeout` can't break schema creation or migrations. `search_path` is not overridden.
- **Extension options** — `databases[].extensions` entries can be a mapping with `name`, `schema`, `version` and `cascade` (plain names keep working). Missing extensions are created with `SCHEMA` / `VERSION` / `CASCADE`; an installed extension whose pinned `version` differs is updated with `ALTER EXTENSION ... UPDATE TO` (not with `strategy: create`); a differing `schema` is only warned about, since relocating an extension can break dependent objects.
- **Duplicate extensions in a database are rejected** at load time, as are unknown keys in an extension mapping (e.g. a misspelled `shema`).
- **Schemas are now provisioned before extensions** (per database), so an extension can be installed into a schema the provisioner creates. `CREATE SCHEMA` never depends on an extension, but an extension without `schema` lands in the first existing schema of the admin connection's `search_path`: with the default `"$user", public`, a provisioned schema named like the admin login role now exists at that point and is chosen over `public`. Set `schema: public` explicitly to keep the previous placement.

## [1.3.0] - 2026-07-16

### Added

- **`bypassrls` role option** — `roles[].options.bypassrls` now emits `BYPASSRLS`/`NOBYPASSRLS` on `CREATE ROLE`/`ALTER ROLE`, allowing a role to bypass row-level security policies.
- **`inherit`, `replication`, and `valid_until` role options** — `roles[].options` now also supports `inherit` (`INHERIT`/`NOINHERIT`), `replication` (`REPLICATION`/`NOREPLICATION`), and `valid_until` (`VALID UNTIL '<timestamp>'`, password expiry), applied on `CREATE ROLE`/`ALTER ROLE`.

## [1.2.0] - 2026-07-12

### Added

- **Per-database create options** ([#32](https://github.com/datarocks-ag/postgres-provisioner/pull/32)) — the `databases[].options` block now supports `encoding`, `locale`, `lc_collate`, `lc_ctype`, and `template`, applied at `CREATE DATABASE` time (e.g. `LC_COLLATE 'C' TEMPLATE template0`, required by collation-sensitive apps like Synapse). Options are create-time only: on an existing database they are ignored and a warning is logged, since Postgres cannot `ALTER` them. `locale` is rejected when combined with `lc_collate`/`lc_ctype`.

### Fixed

- **Connection DSN credentials are now percent-encoded** ([#32](https://github.com/datarocks-ag/postgres-provisioner/pull/32)) — `ConnConfig.DSN()` builds the connection URL via `net/url` instead of raw string concatenation. Admin/user passwords containing URL-special characters (e.g. `@` or `:`) previously corrupted the parsed host/port (`invalid port`), making the provisioner unable to connect. IPv6 hosts are bracketed and the database name is escaped (including `/`) so it round-trips intact.
- **Connection errors no longer leak credentials into logs** ([#32](https://github.com/datarocks-ag/postgres-provisioner/pull/32)) — the connection-retry `WARN` line now passes the driver error through `redactConnError`, which masks the userinfo section of any embedded connection URL and the raw/escaped password value before logging. The escaped-value match uses the same userinfo encoding as the DSN (e.g. a space as `%20`), so it also catches truncated parse errors that don't include the full URL.

## [1.1.0] - 2026-04-25

### Added

- **`--dry-run` mode** ([#24](https://github.com/datarocks-ag/postgres-provisioner/pull/24)) — new CLI flag `--dry-run` and env var `DRY_RUN` that logs every mutation as a preview without executing it. Read-only queries (existence checks, applied-migration lookups) still run so the preview reflects live state.
  - Migrations report their full plan at INFO level (`would apply`, `would re-execute repeatable migration`, `migration already applied — would skip`) without creating the tracking table or applying any files. A missing `_schema_migrations` table (Postgres SQLSTATE `42P01`) is treated as zero applied migrations; any other error from the tracking-table query propagates so a misleading plan can't be produced from a hidden permission or connectivity failure.
  - When dry-run previews a database creation, `Run()` skips the per-database `ConnectToDatabase` step (which would otherwise block on the 5-minute connection-retry timeout) and logs a summary of the extensions/schemas/grants/migrations that would be provisioned. Existing-database dry-runs continue to preview sub-resources via the real connection.
  - `PASSWORD '...'` literals in the logged SQL are redacted to `'***REDACTED***'` (case-insensitive, handles Postgres-style `''` escaping inside string literals).

### Changed

- **`grantOnTablesInSchema` is now transactional** ([#24](https://github.com/datarocks-ag/postgres-provisioner/pull/24)) — the three coupled statements (`GRANT ... ON ALL TABLES IN SCHEMA` plus two `ALTER DEFAULT PRIVILEGES`) run inside a single `BeginTx`/`Commit`. A failure in statement 2 or 3 no longer leaves the schema with grants on existing tables but missing default privileges for newly created ones; the transaction is rolled back instead.
- **Go dependencies updated** ([#23](https://github.com/datarocks-ag/postgres-provisioner/pull/23)) — direct: `github.com/lib/pq` 1.11.2 → 1.12.3, `github.com/testcontainers/testcontainers-go` and `/modules/postgres` 0.40.0 → 0.42.0. Indirect highlights: OpenTelemetry 1.38 → 1.43, golangci-lint tool 2.8.0 → 2.11.4. `denis-tingaikin/go-header` and `go-simpler.org/sloglint` are pinned at the versions golangci-lint v2.11.4 expects (later releases break its source-level imports).
- **GitHub Actions updated** ([#23](https://github.com/datarocks-ag/postgres-provisioner/pull/23)):
  - `codecov/codecov-action` v4 → v6
  - `actions/upload-artifact` v4 → v7
  - `aquasecurity/trivy-action` `@master` → `@v0.36.0` (pinned)
  - `docker/setup-buildx-action` v3 → v4
  - `docker/build-push-action` v6 → v7
  - `docker/login-action` v3 → v4
  - `docker/metadata-action` v5 → v6
  - `goreleaser/goreleaser-action` v6 → v7

### Internal

- Package-level mutators (`createRole`, `alterRole`, `createDatabase`, `alterDatabaseOwner`, `grantOn*`) refactored to methods on `*Provisioner` and routed through a single `execMutation` helper that switches on `Options.DryRun`. Reads remain package-level functions.

## [1.0.0] - 2026-02-20

Initial release.

[Unreleased]: https://github.com/datarocks-ag/postgres-provisioner/compare/v1.2.0...HEAD
[1.2.0]: https://github.com/datarocks-ag/postgres-provisioner/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/datarocks-ag/postgres-provisioner/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/datarocks-ag/postgres-provisioner/releases/tag/v1.0.0
