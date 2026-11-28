# FAQ

After reading this page you can answer the questions that come up once the provisioner runs
for real. The provisioner reads a YAML config and brings PostgreSQL roles, databases, schemas,
grants and migrations in line with it on every run; it only adds and changes, it never
deletes. [How it works](concepts.md) explains the run in full, and the
[configuration reference](configuration.md) lists every field.

Log output on this page is trimmed to the relevant fields; real lines also carry `time`.

- [Day-to-day behavior](#day-to-day-behavior)
  - [I removed a role from the config. Why does it still exist?](#i-removed-a-role-from-the-config-why-does-it-still-exist)
  - [Then how do I take away a privilege?](#then-how-do-i-take-away-a-privilege)
  - [Is it safe to run on every deploy?](#is-it-safe-to-run-on-every-deploy)
  - [The dry-run shows `ALTER ROLE` and `GRANT` statements although nothing changed. Why?](#the-dry-run-shows-alter-role-and-grant-statements-although-nothing-changed-why)
  - [When should I use `strategy: create`?](#when-should-i-use-strategy-create)
  - [Why doesn't changing encoding, locale or template take effect?](#why-doesnt-changing-encoding-locale-or-template-take-effect)
  - [Can I validate a config without a database?](#can-i-validate-a-config-without-a-database)
- [Configuration errors](#configuration-errors)
  - [Why does the run exit with "Required environment variable not set"?](#why-does-the-run-exit-with-required-environment-variable-not-set)
  - [Where does the provisioner read its config from?](#where-does-the-provisioner-read-its-config-from)
  - [Why do I get `role "<name>" is not declared in roles`?](#why-do-i-get-role-name-is-not-declared-in-roles)
  - [Can I manage the `postgres` database?](#can-i-manage-the-postgres-database)
- [Passwords and secrets](#passwords-and-secrets)
  - [How do I rotate a password?](#how-do-i-rotate-a-password)
  - [I removed `password` from a role. Is the password gone now?](#i-removed-password-from-a-role-is-the-password-gone-now)
  - [Why does the run fail with `environment variable <NAME> is not set` after upgrading from 1.x?](#why-does-the-run-fail-with-environment-variable-name-is-not-set-after-upgrading-from-1x)
  - [My password contains `${`. How do I write it?](#my-password-contains--how-do-i-write-it)
  - [Can passwords end up in logs?](#can-passwords-end-up-in-logs)
  - [How do I give someone access that expires?](#how-do-i-give-someone-access-that-expires)
- [Permissions](#permissions)
  - [Why does my application get `permission denied for table` on tables from migrations?](#why-does-my-application-get-permission-denied-for-table-on-tables-from-migrations)
  - [And `permission denied for sequence`?](#and-permission-denied-for-sequence)
  - [Why does a user who can read the tables get `permission denied for schema`?](#why-does-a-user-who-can-read-the-tables-get-permission-denied-for-schema)
  - [Why isn't a table created after provisioning covered by my grant?](#why-isnt-a-table-created-after-provisioning-covered-by-my-grant)
  - [Does the provisioner need a superuser?](#does-the-provisioner-need-a-superuser)
  - [Does it work with Amazon RDS, Cloud SQL, Azure, …?](#does-it-work-with-amazon-rds-cloud-sql-azure-)
- [Migrations](#migrations)
  - [Why was my migration file ignored?](#why-was-my-migration-file-ignored)
  - [Can I edit a migration that already ran?](#can-i-edit-a-migration-that-already-ran)
  - [How do I fix "checksum mismatch for versioned migration"?](#how-do-i-fix-checksum-mismatch-for-versioned-migration)
  - [How do I roll back a migration?](#how-do-i-roll-back-a-migration)
  - [Why is my migrations directory not found in the container?](#why-is-my-migrations-directory-not-found-in-the-container)
  - [Can I use `CREATE INDEX CONCURRENTLY` in a migration?](#can-i-use-create-index-concurrently-in-a-migration)
  - [My application runs its own migrations. Do I need the provisioner's?](#my-application-runs-its-own-migrations-do-i-need-the-provisioners)
- [Operations](#operations)
  - [Can two provisioners run at the same time?](#can-two-provisioners-run-at-the-same-time)
  - [What happens if the database isn't up yet?](#what-happens-if-the-database-isnt-up-yet)
  - [What do the exit codes mean?](#what-do-the-exit-codes-mean)
  - [Which PostgreSQL versions are supported?](#which-postgresql-versions-are-supported)
  - [Which image tag should I use?](#which-image-tag-should-i-use)

## Day-to-day behavior

### I removed a role from the config. Why does it still exist?

Because the provisioner never deletes anything; you drop the role yourself.

Removing a line must never be able to drop a database or lock out a service. The same holds
for databases, schemas, extensions, grants and memberships. To remove a role, first take it
out of the config (otherwise the next run creates it again), then run as the admin user in
`psql`. `REASSIGN OWNED` and `DROP OWNED` act on one database at a time, so run them in
**every** database where the role owns objects or holds privileges:

```sql
-- in each database:
REASSIGN OWNED BY <role> TO <new_owner>;
DROP OWNED BY <role>;
-- then once, from any database:
DROP ROLE <role>;
```

`psql` answers `REASSIGN OWNED`, `DROP OWNED` and `DROP ROLE`. This can't be undone: try it
on a staging server first.

Further reading: [Additive by design](concepts.md#additive-by-design),
[DROP ROLE](https://www.postgresql.org/docs/current/sql-droprole.html)

### Then how do I take away a privilege?

With a `revokes` entry. Removing a grant from the config only stops the provisioner from
granting it again; it doesn't revoke what was granted.

Revokes cover databases and schemas. For tables, revoke in a migration.

```yaml
databases:
  - name: shop
    revokes:
      - { role: <role>, privileges: [CREATE], on_schema: app }
```

Further reading: [Revokes](configuration.md#revokes)

### Is it safe to run on every deploy?

Yes, that's what it's built for.

Every step checks the current state or uses idempotent statements, so a second run on an
unchanged config changes nothing. It's equally safe to re-run after a failure: whatever
succeeded stays, and the next run continues from there.

Further reading: [What "update" means for each resource](concepts.md#what-update-means-for-each-resource)

### The dry-run shows `ALTER ROLE` and `GRANT` statements although nothing changed. Why?

Dry-run lists what a run **executes**, and some statements are executed on every run because
PostgreSQL treats them as no-ops when nothing changes.

These appear in every preview: `ALTER ROLE` for a role with a `password`, `ALTER … OWNER TO`,
`CREATE SCHEMA IF NOT EXISTS`, `GRANT`, and, for each database with migrations, the line
`[DRY RUN] would create migration tracking table if missing`. Role attributes, settings,
memberships, revokes, extensions, migrations and publications are compared first and only
show up when they differ.

Further reading: [Dry-run](concepts.md#dry-run)

### When should I use `strategy: create`?

When the provisioner should only *bootstrap* a resource and then leave it to someone else.

Typical cases are a role whose password Vault rotates, or a database an operator tunes by
hand. With `create`, existing resources are skipped entirely; for a database, that includes
everything inside it. Everywhere else, keep the default `update`.

```yaml
roles:
  - name: <role>
    password: ${<NAME>}
    strategy: create
```

Further reading: [Strategy](concepts.md#strategy-update-or-create)

### Why doesn't changing encoding, locale or template take effect?

Because PostgreSQL only accepts them in `CREATE DATABASE`; the provisioner applies
`options` when it creates the database and never afterwards.

On an existing database the run logs a warning and leaves the options as they are:

```json
{"level":"WARN","msg":"database options (encoding/locale/lc_collate/lc_ctype/template) cannot be altered on an existing database; leaving as-is","database":"shop"}
```

To change them, create a new database and move the data.

Further reading: [Database options](configuration.md#database-options)

### Can I validate a config without a database?

Yes: run with `--validate`. It loads and validates the config, checks every migrations
directory and its files, and exits with `0` or `1` without connecting to a server.

It needs no `POSTGRES_*` variables, only the variables the config itself references.
Validation stops at the first error, except that all unresolved `${VAR}` references are
reported together. It is read-only and safe to run anywhere, for example in CI before a
deploy:

```console
$ docker run --rm \
    -e PGHELPER_CONFIG_PATH=/config.yaml \
    -v "$PWD/config.yaml:/config.yaml:ro" \
    -v "$PWD/migrations:/migrations:ro" \
    ghcr.io/datarocks-ag/postgres-provisioner:<version> --validate
```

A valid config ends with:

```json
{"level":"INFO","msg":"Configuration is valid"}
```

An invalid one exits with `1`:

```json
{"level":"ERROR","msg":"Configuration is invalid","error":"validating config: databases[0].owner: role \"shop\" is not declared in roles"}
```

To also preview the changes against a real server, use `--dry-run`.

Further reading: [CLI flags](deployment.md#cli-flags), [Validation](configuration.md#validation), [Dry-run](concepts.md#dry-run)

## Configuration errors

### Why does the run exit with "Required environment variable not set"?

Because `POSTGRES_USER` and `POSTGRES_PASSWORD` (or `POSTGRES_USER_FILE` and
`POSTGRES_PASSWORD_FILE`) are mandatory, and one of them is unset or empty.

The provisioner reads them first and stops at the first one missing:

```json
{"level":"ERROR","msg":"Required environment variable not set","variable":"POSTGRES_PASSWORD","file_variable":"POSTGRES_PASSWORD_FILE"}
```

The other connection variables have defaults: `POSTGRES_HOST` `localhost`, `POSTGRES_PORT`
`5432`, `POSTGRES_DB` `postgres`, `POSTGRES_SSLMODE` `disable`.

Further reading: [Environment variables](deployment.md#environment-variables)

### Where does the provisioner read its config from?

From the path in `PGHELPER_CONFIG_PATH`, by default `./config.yaml`.

The image's working directory is `/`, so the default there is `/config.yaml`. If nothing is
at that path, the run stops:

```json
{"level":"ERROR","msg":"Failed to load configuration","error":"reading config file: open /config.yaml: no such file or directory"}
```

Mount the file and set the path explicitly, as in the Compose example:

```yaml
    environment:
      PGHELPER_CONFIG_PATH: /config.yaml
    volumes:
      - ./config.yaml:/config.yaml:ro
```

Further reading: [Docker Compose](deployment.md#docker-compose)

### Why do I get `role "<name>" is not declared in roles`?

Because every role a database refers to must be listed under `roles`: database and schema
owners, grantees, revoke targets and `role_settings` keys.

Two exceptions: `member_of` may name roles that aren't declared (such as `pg_monitor`), and a
revoke may target `PUBLIC`. To refer to a role that exists already and is managed elsewhere,
declare it with `strategy: create`; the provisioner then leaves an existing role alone:

```yaml
roles:
  - name: <name>
    strategy: create
```

Further reading: [Validation](configuration.md#validation), [Roles](configuration.md#roles)

### Can I manage the `postgres` database?

No. `postgres`, `template0` and `template1` are reserved, and a config that lists one of them
is rejected:

```json
{"level":"ERROR","msg":"Failed to load configuration","error":"validating config: databases[0]: \"postgres\" is a reserved database name"}
```

Create a database of your own for application data.

Further reading: [Validation](configuration.md#validation)

## Passwords and secrets

### How do I rotate a password?

Change the secret and run the provisioner; with `strategy: update` it sets every role's
password on every run.

Then restart the application with the new secret. Existing sessions stay connected.

Further reading: [Rotating a password](howto/secrets.md#5-rotate-a-password)

### I removed `password` from a role. Is the password gone now?

No. Without `password`, the provisioner doesn't set one, and the existing password stays.

To remove a password, run as the admin user in `psql`; it answers `ALTER ROLE`:

```sql
ALTER ROLE <role> PASSWORD NULL;
```

Further reading: [Roles](configuration.md#roles)

### Why does the run fail with `environment variable <NAME> is not set` after upgrading from 1.x?

Since 2.0, a `${<NAME>}` that can't be resolved is an error. Before, it was left in the
config as literal text, so a missing `APP_DB_PASSWORD` silently set the role's password to
the string `${APP_DB_PASSWORD}`.

Set the variable or `<NAME>_FILE`. For a non-secret field you can give a default instead:
`${<NAME>:-<default>}`. For a password that doesn't help: a password reference that resolves
to empty is rejected (`environment variable reference resolved to an empty value`). If the
role should have no password, remove the `password` field.

```yaml
roles:
  - name: shop_app
    password: ${SHOP_APP_PASSWORD}          # must be set
databases:
  - name: ${SHOP_DB_NAME:-shop}             # non-secret: default allowed
```

Further reading: [Upgrading to 2.0](upgrading-to-2.0.md),
[Environment variable expansion](configuration.md#environment-variable-expansion)

### My password contains `${`. How do I write it?

As `$${`: `pa$${ss` becomes `pa${ss`.

Better still, keep passwords out of the config and use a `${<NAME>}` reference; values coming
from variables are never expanded again.

```yaml
roles:
  - name: <role>
    password: "pa$${ss"
```

Further reading: [Environment variable expansion](configuration.md#environment-variable-expansion)

### Can passwords end up in logs?

No.

A normal run logs actions ("Creating role"), not SQL. Only dry-run logs SQL, and there
`PASSWORD '…'` literals are replaced by `'***REDACTED***'`. Connection errors are stripped of
credentials, and config errors name variables, never their values.

Further reading: [Logs and secrets](concepts.md#logs-and-secrets)

### How do I give someone access that expires?

Set `options.valid_until` on the role; after that time its password no longer works.

`valid_until` only limits password logins. With `strategy: update` it is applied whenever it
differs from the stored date, so changing the date in the config and re-running extends or
shortens it. Removing the
field leaves the last date in place. `connection_limit` caps parallel sessions as well:

```yaml
roles:
  - name: <role>
    password: ${<NAME>}
    options:
      login: true
      valid_until: "2027-03-31"
      connection_limit: 2
```

Further reading: [Role options](configuration.md#role-options),
[CREATE ROLE](https://www.postgresql.org/docs/current/sql-createrole.html)

## Permissions

### Why does my application get `permission denied for table` on tables from migrations?

Because migrations run as the admin user, so the admin owns the tables they create; add an
`on_tables_in_schema` grant for the application role.

Owning the database or schema doesn't grant anything on tables inside it. The grant also
covers tables created by later migrations:

```yaml
    grants:
      - { role: shop_app, privileges: [SELECT, INSERT, UPDATE, DELETE], on_tables_in_schema: app }
```

Further reading: [Getting started, step 2](getting-started.md#step-2-a-schema-and-the-first-migration),
[Grants](configuration.md#grants)

### And `permission denied for sequence`?

The table has a `serial` column, whose sequence needs its own grant: add
`on_sequences_in_schema` with `USAGE, SELECT`.

Identity columns (`GENERATED … AS IDENTITY`) don't need it.

```yaml
    grants:
      - { role: shop_app, privileges: [USAGE, SELECT], on_sequences_in_schema: app }
```

Further reading: [Grants](configuration.md#grants)

### Why does a user who can read the tables get `permission denied for schema`?

Reading a table also needs `USAGE` on its schema; add an `on_schema` grant with `USAGE`.

```yaml
    grants:
      - { role: <role>, privileges: [USAGE], on_schema: app }
```

Further reading: [Grants](configuration.md#grants)

### Why isn't a table created after provisioning covered by my grant?

Because default privileges only apply to objects created by the roles they were set for: the
admin user, and the schema owner if one is configured.

The schema owner is the schema's `owner`, or else the database's `owner`; with neither, only
the admin user's default privileges are set. If another role created the table, the next run
with `strategy: update` grants on it again. With `strategy: create` an existing database is
skipped entirely, so grant by hand.

```yaml
databases:
  - name: shop
    owner: shop_app
    schemas:
      - { name: app, owner: shop_app }   # tables shop_app creates are covered too
    grants:
      - { role: <role>, privileges: [SELECT], on_tables_in_schema: app }
```

Further reading: [Grants](configuration.md#grants),
[Read-only and reporting users](howto/read-only-users.md#pitfalls)

### Does the provisioner need a superuser?

No, but it's the easiest option.

A non-superuser admin needs, depending on the features you use, `CREATEROLE`, `CREATEDB`,
`ADMIN OPTION` on roles it grants memberships in, and ownership of the objects it grants on.

Further reading: [Privileges the admin user needs](deployment.md#privileges-the-admin-user-needs)

### Does it work with Amazon RDS, Cloud SQL, Azure, …?

Yes, with the non-superuser caveats above: the master user of a managed service is not a real
superuser.

In practice, extensions are limited to those the service allows, `all_tables` publications
aren't possible, and `shared_preload_libraries` and `wal_level` are set through the service's
parameter settings. `POSTGRES_SSLMODE` defaults to `disable`, and managed services usually
require TLS, so set `require` or stronger (`verify-full` in production):

```yaml
    environment:
      POSTGRES_SSLMODE: verify-full
```

Further reading: [Connect over TLS](howto/connect-over-tls.md),
[Privileges the admin user needs](deployment.md#privileges-the-admin-user-needs)

## Migrations

### Why was my migration file ignored?

Because its name doesn't match `V<n>__<description>.sql` or `R<n>__<description>.sql`. A
`.sql` file that doesn't match is skipped with a warning; other files are skipped without
one.

Note the two underscores and the upper-case `V` or `R`: `V1_create.sql` and
`v1__create.sql` are ignored. Files that do match are checked: an empty file and two files
of the same kind with the same number are errors that stop the run.

```text
migrations/shop/
├── V0001__create_products.sql     # applied once
├── R0001__product_view.sql        # re-applied when it changes
└── V0002_add_price.sql            # ignored: one underscore
```

The skipped file shows up as a warning in the run's log:

```json
{"level":"WARN","msg":"Ignoring SQL file that doesn't match the migration naming pattern","directory":"/migrations/shop","file":"V0002_add_price.sql","expected":"V<version>__<description>.sql or R<version>__<description>.sql"}
```

Further reading: [Migrations](configuration.md#migrations)

### Can I edit a migration that already ran?

Not a versioned one (`V…`): its checksum is recorded, and a changed file fails the run.

Add a new migration instead. Repeatable migrations (`R…`) are meant to change: they re-run
whenever their content changes.

Further reading: [Migrations](configuration.md#migrations)

### How do I fix "checksum mismatch for versioned migration"?

Restore the file's original content, and put the change in a new versioned migration.

The checksum of every applied migration is stored per database in the `_schema_migrations`
table, and the run fails when a file's SHA-256 differs. To see which content is expected,
compare the recorded checksum with the file's; this is read-only and safe in any environment:

```console
$ psql -d <database> -c "SELECT filename, checksum FROM _schema_migrations WHERE type = 'versioned' ORDER BY applied_at"
$ sha256sum migrations/<database>/<file>.sql
```

The file whose two checksums differ is the one to restore, for example from version control.

Further reading: [Migrations](configuration.md#migrations)

### How do I roll back a migration?

There are no down migrations: write a new versioned migration that reverts the change.

Further reading: [Migrations](configuration.md#migrations)

### Why is my migrations directory not found in the container?

Because relative paths resolve from the working directory, which is `/` in the image; use an
absolute path that matches your volume mount.

Mount `./migrations` to `/migrations` and set `directory: /migrations/shop`:

```yaml
  postgres-provisioner:
    volumes:
      - ./config.yaml:/config.yaml:ro
      - ./migrations:/migrations:ro
```

Further reading: [Migrations](configuration.md#migrations),
[Docker Compose](deployment.md#docker-compose)

### Can I use `CREATE INDEX CONCURRENTLY` in a migration?

No. Each migration runs in a transaction, and PostgreSQL doesn't allow that statement in one.

Further reading: [Migrations](configuration.md#migrations),
[CREATE INDEX](https://www.postgresql.org/docs/current/sql-createindex.html)

### My application runs its own migrations. Do I need the provisioner's?

No. Leave out `migrations:`; grants then work slightly differently.

Further reading: [When the application runs its own migrations](howto/app-migrations.md)

## Operations

### Can two provisioners run at the same time?

Yes, but they run one after the other: a run takes an advisory lock on the server, and a
second run waits until the first one finishes.

Without the lock, two runs could both try to create the same role or apply the same
migration. The waiting run logs one line and then blocks; `SIGTERM` or a Kubernetes Job
deadline still stops it. If a run is killed, PostgreSQL releases its lock with the session.

```json
{"level":"INFO","msg":"Waiting for another provisioner run to finish"}
{"level":"INFO","msg":"Acquired the provisioner run lock"}
```

The lock lives in the database the provisioner connects to (`POSTGRES_DB`), so runs only
wait for each other when they use the same `POSTGRES_DB`. To see which session holds it:

```sql
SELECT pid, usename, application_name, backend_start
FROM pg_locks JOIN pg_stat_activity USING (pid)
WHERE locktype = 'advisory' AND classid = 28775 AND objid = 1886547830;
```

Safe to run anywhere; it only reads. `classid` and `objid` are the upper and lower 32 bits of the lock key.

Further reading: [Kubernetes](deployment.md#kubernetes)

### What happens if the database isn't up yet?

The provisioner retries with backoff for up to 5 minutes before giving up.

Each retry is logged as a warning:

```json
{"level":"WARN","msg":"PostgreSQL not ready, retrying","attempt":1,"max_retries":15,"delay":1000000000,"error":"…"}
```

Further reading: [Connection retry](deployment.md#connection-retry)

### What do the exit codes mean?

`0`: everything was provisioned. `1`: something failed, whether config, connection or a
statement.

The last log line says what. Compose's `service_completed_successfully` and Kubernetes Jobs
rely on this.

Further reading: [Docker Compose](deployment.md#docker-compose)

### Which PostgreSQL versions are supported?

PostgreSQL 16, 17, 18 and 19: CI runs the integration tests against each of them. Older
versions aren't tested.

Version-specific behavior, such as the membership rules in 16+, is called out in the
reference.

Further reading: [integration tests](../internal/provisioner/integration_test.go)

### Which image tag should I use?

A pinned release (`2.1.0`) or minor version (`2.1`).

`latest` follows releases, `develop` the development branch.

Further reading: [Image tags](deployment.md#image-tags)
