# Grant access when the application runs its own migrations

After this you have a setup where the first deploy works on an empty database and every later
deploy keeps the grants complete, even though the application creates its own tables.

## When you need this

Your service doesn't use the provisioner's SQL migrations. It brings its own tool (Flyway,
Liquibase, Alembic, Prisma, golang-migrate…) and migrates on startup. The provisioner runs
first, as an [init container](../deployment.md#as-an-init-container), a
[Kubernetes Job](../deployment.md#as-a-job) or a [Compose service](../deployment.md#docker-compose),
to set up roles, databases and grants. That raises a chicken-and-egg question: how do you
grant on tables that don't exist yet when the provisioner runs?

## What you need to know first

- **Default privileges.** `ALTER DEFAULT PRIVILEGES` tells PostgreSQL which grants to add
  automatically to objects that a given role creates later. They apply only to objects
  created by the role they were set for, so they work on an empty schema. See
  [ALTER DEFAULT PRIVILEGES](https://www.postgresql.org/docs/current/sql-alterdefaultprivileges.html).
- **`*_in_schema` grants set them for you.** An `on_tables_in_schema` grant grants on the
  existing tables and sets default privileges for the admin user and for the configured
  schema owner: `schemas[].owner`, or else the database's `owner`. See
  [Grants](../configuration.md#grants).
- **Run order.** The provisioner applies `*_in_schema` grants before migrations and
  `on_tables` grants after them. See [The provisioning order](../concepts.md#the-provisioning-order).

### Specific tables: how `on_tables` behaves

Grants on named tables (`on_tables`) can't use default privileges: they need the table to
exist. What happens when it doesn't depends on whether the provisioner ran migrations for
that database in this run:

| Provisioner migrations for the database | A table in `on_tables` is missing |
|---|---|
| configured and enabled | **The run fails**, because the provisioner's own migrations should have created it. With `--dry-run`, the provisioner only logs the missing tables at INFO level and previews the `GRANT`. |
| not configured, or disabled with `MIGRATIONS_ENABLED=false` | **Warning.** The provisioner applies the grant to the tables that exist; it grants on the rest in a later run. |

[Publications](cdc-publications.md) follow the same rule for their tables.

## Steps

The names `shop`, `app`, `shop_app` and `reporting` in the examples are placeholders; replace
them with your own database, schema and role names.

### 1. Let the application own its schema

The cleanest setup: the application's role owns the schema, and its migration tool connects
as that role. Everything it creates belongs to it, so the application never needs grants on
its own tables.

```yaml
roles:
  - name: shop_app
    password: ${SHOP_APP_PASSWORD}
    options:
      login: true

databases:
  - name: shop
    owner: shop_app
    schemas:
      - name: app                   # owned by shop_app (the database owner)
```

Leave out `migrations:`, since the application handles them.

### 2. Grant other roles access through default privileges

Other roles, like a reporting user, need access to tables the application creates. Declare
the role and use an `*_in_schema` grant:

```yaml
roles:
  - name: shop_app
    password: ${SHOP_APP_PASSWORD}
    options:
      login: true
  - name: reporting
    password: ${REPORTING_PASSWORD}
    options:
      login: true

databases:
  - name: shop
    owner: shop_app
    schemas:
      - name: app
    grants:
      - role: reporting
        privileges: [USAGE]
        on_schema: app
      - role: reporting
        privileges: [SELECT]
        on_tables_in_schema: app
```

Besides granting on existing tables, this sets default privileges **for the configured
owner** of `app` (here `shop_app`, the database owner, because the schema sets no `owner` of
its own). Every table the application creates in `app` from now on is readable by
`reporting` immediately, even on the first deploy when the schema is still empty. This works
because the application's migrations run as that owner. If they run as a different role, the
next provisioning run grants on its new tables instead.

### 3. Add grants on named tables only where you need them

Use `on_tables` only for what `on_tables_in_schema` can't express, such as column-level grants
(`columns` works only together with `on_tables`). Because the tables don't exist on the very
first deploy of an application that migrates itself, the
[rule above](#specific-tables-how-on_tables-behaves) plays out like this:

1. The provisioner runs, finds no tables yet, logs a warning, and succeeds.
2. The application starts and creates its tables.
3. On the **next** provisioning run (the next deploy), the provisioner completes the grant.

If a consumer needs its `on_tables` grants on day one, run the provisioner once more after the
application's first start, for example as a post-deploy [Job](../deployment.md#as-a-job)
(with Helm, the `post-install,post-upgrade` hooks). If the consumer may read whole tables,
use an `on_tables_in_schema` grant instead, which covers future tables immediately.

## Check that it works

After the provisioning run, list the default privileges in the database as the admin user
(`admin` here, the `POSTGRES_USER` the provisioner logs in as):

```console
$ psql -h localhost -U admin -d shop -c '\ddp'
            Default access privileges
  Owner   | Schema | Type  |  Access privileges
----------+--------+-------+----------------------
 admin    | app    | table | reporting=r/admin
 shop_app | app    | table | reporting=r/shop_app
(2 rows)
```

The `shop_app` row is the one that covers the application's tables; `r` stands for `SELECT`.
If it is missing, no owner is configured for the schema or the database.

After the application's first start, connect as `reporting` and read one of its tables
(`<table>` is any table the application created). The count depends on your data:

```console
$ psql -h localhost -U reporting -d shop -c "SELECT count(*) FROM app.<table>"
 count
-------
     0
(1 row)
```

`ERROR:  permission denied for table <table>` instead means that a role
other than the configured owner created the table; see the first pitfall.

## Pitfalls

- **Default privileges are per creating role.** `ALTER DEFAULT PRIVILEGES` only applies to
  objects created by the role it was set for. The provisioner sets it for itself (the admin
  user) and for the configured owner (`schemas[].owner`, or else the database `owner`). If your
  migration tool connects as a third role, either make it that owner or accept that grants
  catch up on the next run. To keep the migration role separate from the role the application
  serves requests with, see
  [Separate the migration owner from the application role](separate-migration-owner.md).
- **Disabled vs. not configured.** `MIGRATIONS_ENABLED=false` (or `--migrations=false`) skips
  the provisioner's migrations at runtime without changing the config. For missing tables it
  behaves like "not configured": warnings, not errors.
- **Dry-run doesn't prove the real run passes.** With migrations configured, `--dry-run`
  reports missing `on_tables` tables at INFO level only; the real run fails unless its
  migrations create them.

## Further reading

- [Tables and columns (`on_tables`)](../configuration.md#tables-and-columns) in the
  configuration reference.
- [The provisioning order](../concepts.md#the-provisioning-order).
- [Create read-only users](read-only-users.md) for the levels of read access.
- [Manage access with group roles](manage-access-with-group-roles.md).
