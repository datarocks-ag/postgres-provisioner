# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
