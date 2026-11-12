# FAQ

Questions that come up once the provisioner runs for real. If yours isn't here, the
[configuration reference](configuration.md) or [How it works](concepts.md) probably answers
it.

- [Day-to-day behavior](#day-to-day-behavior)
- [Passwords and secrets](#passwords-and-secrets)
- [Permissions](#permissions)
- [Migrations](#migrations)
- [Operations](#operations)

## Day-to-day behavior

### I removed a role from the config. Why does it still exist?

Because the provisioner never deletes anything. Removing a line must never be able to drop a
database or lock out a service. To really remove a role, run `DROP ROLE` yourself (or in a
migration) after removing it from the config. The same holds for databases, schemas,
extensions, grants and memberships. See [Additive by design](concepts.md#additive-by-design).

### Then how do I take away a privilege?

With [`revokes`](configuration.md#revokes). Removing a grant from the config only stops the
provisioner from granting it again; it doesn't revoke what was granted. Revokes cover
databases and schemas. For tables, revoke in a migration.

### Is it safe to run on every deploy?

Yes, that's what it's built for. Every step checks the current state or uses idempotent
statements, so a second run on an unchanged config changes nothing. It's just as safe to
re-run after a failure: whatever succeeded stays, and the next run continues from there.

### The dry-run shows `ALTER ROLE` and `GRANT` statements although nothing changed. Why?

Dry-run lists what a run **executes**, and some statements are executed on every run because
PostgreSQL treats them as no-ops when nothing changes: role attributes and passwords,
ownership, and grants. Settings, memberships, revokes, extensions, migrations and
publications are compared first and only show up when they differ. See
[Dry-run](concepts.md#dry-run).

### When should I use `strategy: create`?

When the provisioner should only *bootstrap* a resource and then leave it to someone else:
a role whose password is rotated by Vault, or a database an operator tunes by hand. With
`create`, existing resources are skipped entirely; for a database, that includes everything
inside it. Everywhere else, keep the default `update`. See
[Strategy](concepts.md#strategy-update-or-create).

### Can I validate a config without a database?

Not with a separate command, but the provisioner loads and validates the whole config before
it connects. If the config is invalid, the first lines of output are the error, and the
run exits with `1` without touching anything. To also preview the changes, use `--dry-run`
against a real server.

## Passwords and secrets

### How do I rotate a password?

Change the secret and run the provisioner. With `strategy: update` it sets every role's
password on every run. Then restart the application with the new secret. Existing sessions
stay connected. See [Rotating a password](howto/secrets.md#rotating-a-password).

### I removed `password` from a role. Is the password gone now?

No. Without `password`, the provisioner simply doesn't set one, and the existing password
stays. To remove a password, run `ALTER ROLE ... PASSWORD NULL` by hand.

### The run fails with `environment variable X is not set`. It worked in 1.x!

Since 2.0, a `${VAR}` that can't be resolved is an error. Before, it was left in the config
as literal text, so a missing `APP_DB_PASSWORD` silently set the role's password to the
string `${APP_DB_PASSWORD}`. Set the variable (or `X_FILE`), or make it optional with
`${X:-}` or `${X:-default}`. See [Upgrading to 2.0](upgrading-to-2.0.md).

### My password contains `${`. How do I write it?

As `$${`: `pa$${ss` becomes `pa${ss`. Better still, keep passwords out of the config and use a
`${VAR}` reference; values coming from variables are never expanded again.

### Can passwords end up in logs?

No. `PASSWORD '…'` literals are redacted in every log line (also in dry-run output),
connection errors are stripped of credentials, and config errors name variables, never their
values.

## Permissions

### Why does my application get `permission denied for table` on tables from migrations?

Migrations run as the admin user, so the admin owns the tables they create. Owning the
database or schema doesn't grant anything on tables inside it. Add an `on_tables_in_schema`
grant for the application role; it also covers tables created by later migrations. The
[getting started guide](getting-started.md#step-2-a-schema-and-the-first-migration) walks
through exactly this.

### And `permission denied for sequence`?

The table has a `serial` column, whose sequence needs its own grant: add
`on_sequences_in_schema` with `USAGE, SELECT`. Identity columns don't need it. See
[Grants](configuration.md#grants).

### A user can read the tables but gets `permission denied for schema`.

Reading a table also needs `USAGE` on its schema. Add an `on_schema` grant with `USAGE`.

### A table created after provisioning isn't covered by my grant.

Default privileges only apply to objects created by the roles they were set for: the admin
user and the schema owner. If a third role created the table, the next provisioning run
grants on it. See
[Read-only and reporting users](howto/read-only-users.md#pitfalls).

### Does the provisioner need a superuser?

No, but it's the easiest option. A non-superuser admin needs, depending on the features you
use, `CREATEROLE`, `CREATEDB`, `ADMIN OPTION` on roles it grants memberships in, and
ownership of the objects it grants on. See
[Privileges the admin user needs](deployment.md#privileges-the-admin-user-needs).

### Does it work with Amazon RDS, Cloud SQL, Azure, …?

Yes, with the non-superuser caveats above: the master user of a managed service is not a real
superuser. In practice, extensions are limited to those the service allows, `all_tables`
publications aren't possible, and `shared_preload_libraries` and `wal_level` are set
through the service's parameter settings.

## Migrations

### Can I edit a migration that already ran?

Not a versioned one (`V…`). Its checksum is recorded, and a changed file fails the run. Add
a new migration instead. Repeatable migrations (`R…`) are meant to change: they re-run
whenever their content changes.

### How do I roll back a migration?

There are no down migrations. Write a new versioned migration that reverts the change.

### Why is my migrations directory not found in the container?

Relative paths resolve from the working directory, which is `/` in the image. Use an
absolute path that matches your volume mount, e.g. mount `./migrations` to `/migrations` and
use `directory: /migrations/shop`.

### Can I use `CREATE INDEX CONCURRENTLY` in a migration?

No. Each migration runs in a transaction, and PostgreSQL doesn't allow that statement in one.

### My application runs its own migrations. Do I need the provisioner's?

No. Leave out `migrations:` and see
[When the application runs its own migrations](howto/app-migrations.md) for how grants work
then.

## Operations

### Can two provisioners run at the same time?

Avoid it. There is no lock between runs, so two concurrent runs may both try to create the
same role or apply the same migration, and one of them fails. Run a single instance per
server: one Compose service, or one Kubernetes Job rather than an init container in a
Deployment with several replicas. A failed run can simply be retried.

### What happens if the database isn't up yet?

The provisioner retries with backoff for up to 5 minutes before giving up. See
[Connection retry](deployment.md#connection-retry).

### What do the exit codes mean?

`0`: everything was provisioned. `1`: something failed, whether config, connection or a
statement. The last log line says what. Compose's `service_completed_successfully` and
Kubernetes Jobs rely on this.

### Which PostgreSQL versions are supported?

The integration tests run against PostgreSQL 16, so that's the tested version. PostgreSQL 14
and later are expected to work but aren't covered by CI. Version-specific behavior, such as
the membership rules in 16+, is called out in the reference.

### Which image tag should I use?

A pinned release (`2.0.0`) or minor version (`2.0`). `latest` follows releases, `develop` the
development branch. See [Image tags](deployment.md#image-tags).
