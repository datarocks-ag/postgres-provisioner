# How it works

The [getting started guide](getting-started.md) showed *what* the provisioner does. This page
explains *how* a run works, so its behavior holds no surprises when you rely on it in
production.

## A run, from start to finish

1. **Load the config.** The YAML file is parsed, `${VAR}` references are resolved, and the
   whole config is validated: unknown roles in grants, invalid privileges for a target,
   circular memberships, duplicate names and so on. Every problem is reported with its path
   (e.g. `databases[0].grants[2].privileges[0]`). **Nothing has touched the database yet**,
   so a broken config never leaves a half-provisioned server.
2. **Connect** as the admin user, retrying with backoff until Postgres accepts connections
   (up to 5 minutes). See [Connection retry](deployment.md#connection-retry).
3. **Provision** in a fixed order (below). Each step reads the current state and only does
   what's needed.
4. **Exit** with code `0` on success, or `1` at the first error. A failed run can be fixed
   and simply re-run; there's no state to clean up.

## The provisioning order

Server-wide resources come first, then each database in config order:

| # | Step | Scope | Why here |
|---|---|---|---|
| 1 | **Roles**, then their `settings` | server | Everything else refers to roles. |
| 2 | **Role memberships** (`member_of`) | server | A separate pass, so a role can be a member of a role declared later in the file. |
| 3 | **Databases** (create, set owner), database `settings` and `role_settings` | server | Must exist before the provisioner can connect to them. |
| 4 | `shared_preload_libraries` **check** | database | Fails early for extensions that can't be created, before anything changes. |
| 5 | **Schemas** | database | Extensions may be installed into them. |
| 6 | **Extensions** | database | Migrations may use them (`gen_random_uuid()`, `citext`, …). |
| 7 | **Revokes** | database | Before grants, so "revoke from `PUBLIC`, grant to one role" ends with the grant in place. |
| 8 | **Grants** on databases, schemas and all objects in a schema | database | Before migrations, so the default privileges they set cover tables the migrations create. |
| 9 | **Migrations** | database | Create the application's tables. |
| 10 | **Object grants** (`on_tables`) | database | After migrations, because they target specific tables that migrations usually create. |
| 11 | **Publications** | database | After migrations, for the same reason. |

For each database the provisioner opens a separate connection (as the admin user) for steps
4–11.

## Additive by design

The provisioner reconciles what's **in** the config and ignores everything else:

| You remove… | What happens |
|---|---|
| a role, database, schema or extension | Nothing. It stays. |
| a grant | Nothing. The privilege stays (use [`revokes`](configuration.md#revokes) to remove it). |
| a `member_of` entry | Nothing. The membership stays. |
| a setting | Nothing. The value stays (set it to `null` to reset it). |
| a table from a publication | Nothing. A warning is logged, because dropping it could cut off a consumer. |
| a migration file | Nothing. Applied migrations aren't rolled back. |

This is deliberate. A typo or a forgotten line must never drop a database or lock out a
service in production. Removing things is an explicit act: `revokes` for privileges, `null`
for settings, and SQL (or a migration) for everything else.

The other side of the coin: the config describes a **minimum**, not the exact state. Anything
created by hand or by another tool is left alone.

## What "update" means for each resource

With the default `strategy: update`, existing resources are reconciled like this:

| Resource | On every run |
|---|---|
| Role | `ALTER ROLE` with the configured password and options. Options you don't set are left unchanged. Without a `password`, the existing password is kept. |
| Role settings, database settings, `role_settings` | Compared with `pg_db_role_setting`. Only differing values are changed. |
| Membership | Granted if missing. |
| Database | Owner set to `owner`. Create-time `options` (encoding, locale, template) can't change and only produce a warning. |
| Schema | Created if missing, owner set to its `owner` (or the database owner). |
| Extension | Created if missing. Updated if a pinned `version` differs. Never moved to another schema (warning). |
| Revoke | Executed only for privileges the grantee currently holds. |
| Grant | `GRANT` re-issued, which PostgreSQL treats as a no-op when already granted. |
| Migration | Versioned: skipped if applied, checksum verified. Repeatable: re-run if its content changed. |
| Publication | Created if missing. Missing tables added, `publish` list updated. |

## Strategy: `update` or `create`

`strategy` decides what happens to resources that **already exist**:

- **`update`** (default): reconcile them as described above.
- **`create`**: leave them exactly as they are. Only missing resources are created.

`create` is for resources the provisioner should set up once and then hand over, for example
a role whose password is rotated by another system, or a database that an operator tunes by
hand after creation.

The strategy can be set globally and overridden per role or database:

```yaml
strategy: create          # default for everything below

roles:
  - name: vault_managed   # created once; Vault rotates its password afterwards
    password: ${INITIAL_PASSWORD}
  - name: shop_app
    strategy: update      # always reconciled
    password: ${SHOP_APP_PASSWORD}
```

A database's strategy covers **everything inside it**. With `strategy: create`, an existing
database is skipped entirely: its settings, schemas, extensions, revokes, grants, migrations
and publications. For an existing role, `create` also skips its `settings` and memberships.

## Dry-run

`--dry-run` (or `DRY_RUN=true`) runs the full sequence against the live server, but every
statement that would change something is **logged** instead of executed:

```json
{"level":"INFO","msg":"[DRY RUN] would execute","sql":"CREATE ROLE \"shop_reporting\" WITH PASSWORD '***REDACTED***' LOGIN"}
```

Read queries still run (does the role exist, which migrations are applied, which privileges
are held), so the preview reflects the real state. Keep in mind:

- **It previews what will be executed, not a minimal diff.** Statements that PostgreSQL treats
  as idempotent (`ALTER ROLE` with password and options, `ALTER … OWNER TO`, `GRANT`) are
  issued on every update run and therefore appear in every preview. Settings, memberships,
  revokes, extensions, migrations and publications are compared first and only appear when
  something differs.
- **A new database can't be looked into.** If the database doesn't exist yet, the preview
  logs a summary (how many schemas, grants, migrations… it would provision) instead of
  connecting to it.
- **Migrations are listed, not run**, so object grants and publications can't know about
  tables those migrations would create. Missing tables are logged instead of failing.

## Sessions the provisioner opens

Every connection the provisioner makes sets `statement_timeout=0`, `lock_timeout=0`,
`idle_in_transaction_session_timeout=0` and `default_transaction_read_only=off` as startup
options. That way a timeout or read-only default you configure for a role or database (or
the admin role) can't break schema creation or a long migration. `search_path` is *not*
overridden: a `search_path` setting on the database or admin role changes how unqualified
names in your migrations resolve.

## Logs and secrets

Logs are JSON lines on stdout, one per action ("Creating role", "Granting on schema",
"Setting unchanged, skipping", …). Set `LOG_LEVEL=debug` for more, `warn` for less.

Secrets never appear in logs: `PASSWORD '…'` literals are redacted, connection errors are
stripped of credentials, and config errors name the missing variable, never its value.

## Next steps

- [How-to guides](../README.md#documentation) apply this to concrete tasks.
- [Configuration reference](configuration.md) lists every field.
- [FAQ](faq.md) answers the questions that come up once it runs in production.
