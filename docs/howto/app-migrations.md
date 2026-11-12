# When the application runs its own migrations

**The situation:** your service doesn't use the provisioner's SQL migrations. It brings its
own tool (Flyway, Liquibase, Alembic, Prisma, golang-migrate…) and migrates on startup. The
provisioner runs first, as an init container or a Compose service, to set up roles,
databases and grants. That raises a chicken-and-egg question: how do you grant on tables
that don't exist yet when the provisioner runs?

**The goal:** a setup where the first deploy works on an empty database and every later
deploy keeps the grants complete.

## Let the application own its schema

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

## Grant other roles access through default privileges

Other roles, like a reporting user, need access to tables the application creates. Use a
`*_in_schema` grant:

```yaml
    grants:
      - role: reporting
        privileges: [USAGE]
        on_schema: app
      - role: reporting
        privileges: [SELECT]
        on_tables_in_schema: app
```

Besides granting on existing tables, this sets default privileges **for the schema owner**
(here `shop_app`). Every table the application creates in `app` from now on is readable by
`reporting` immediately, even on the first deploy when the schema is still empty. This works
because the application's migrations run as the schema owner. If they run as a different
role, its new tables are picked up by the next provisioning run instead.

## Specific tables: how `on_tables` behaves

Grants on named tables (`on_tables`) can't use default privileges: they need the table to
exist. What happens when it doesn't depends on whether the provisioner ran migrations for
that database in this run:

| Provisioner migrations for the database | A table in `on_tables` is missing |
|---|---|
| configured and enabled | **The run fails.** The provisioner's own migrations should have created it, so this is a real error. |
| not configured, or disabled with `MIGRATIONS_ENABLED=false` | **Warning.** The grant is applied to the tables that exist; the rest follow on a later run. |

So on the very first deploy of an application that migrates itself:

1. The provisioner runs, finds no tables yet, logs a warning, and succeeds.
2. The application starts and creates its tables.
3. On the **next** provisioning run (the next deploy), the grant is completed.

If a consumer needs those column-level grants on day one, run the provisioner once more after
the application's first start, e.g. as a post-deploy Job, or use an `on_tables_in_schema`
grant, which covers future tables immediately.

[Publications](cdc-publications.md) follow the same rule for their tables.

## Pitfalls

- **Default privileges are per creating role.** `ALTER DEFAULT PRIVILEGES` only applies to
  objects created by the role it was set for. The provisioner sets it for itself (the admin
  user) and for the schema owner. If your migration tool connects as a third role, either make
  it the schema owner or accept that grants catch up on the next run.
- **Disabled vs. not configured.** `MIGRATIONS_ENABLED=false` (or `--migrations=false`) skips
  the provisioner's migrations at runtime without changing the config. For missing tables it
  behaves like "not configured": warnings, not errors.

## See also

- [Object grants (`on_tables`)](../configuration.md#tables-and-columns) in the configuration
  reference.
- [The provisioning order](../concepts.md#the-provisioning-order).
