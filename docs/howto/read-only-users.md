# Read-only and reporting users

**The situation:** sooner or later someone besides the application needs the data: finance
wants a BI dashboard, support needs to look up orders, a data team wants a nightly export.
Handing out the application's credentials gives them write access to everything. Creating
users by hand in `psql` works until the next environment, when nobody remembers what was
granted.

**The goal:** read-only accounts at exactly the level each consumer needs, declared next to
the rest of the database setup.

There are three levels. Pick the narrowest one that does the job.

## Level 1: everything in one schema

The usual case: a reporting user that may read all tables in the application's schema,
including tables added by future migrations.

```yaml
roles:
  - name: reporting
    password: ${REPORTING_PASSWORD}
    options:
      login: true
      connection_limit: 3
    settings:
      statement_timeout: "60s"        # a runaway dashboard query is cancelled, not your app

databases:
  - name: shop
    grants:
      - role: reporting
        privileges: [USAGE]
        on_schema: app                # may look into the schema...
      - role: reporting
        privileges: [SELECT]
        on_tables_in_schema: app      # ...and read every table in it
```

Both grants are needed: PostgreSQL checks `USAGE` on the schema before it looks at a table.

`on_tables_in_schema` also sets **default privileges**, so tables created later get the
`SELECT` grant automatically. That covers tables created by the provisioner's migrations
(they run as the admin user) and by the schema owner. Tables created by any other role are
picked up on the next provisioning run.

Views count as tables here, so reporting views your migrations create are covered too.

## Level 2: specific tables and columns

Support needs to look up customers by email, but shouldn't see password hashes or addresses.
Grant on individual tables, and optionally individual columns:

```yaml
    grants:
      - role: support
        privileges: [USAGE]
        on_schema: app
      - role: support
        privileges: [SELECT]
        on_tables: ["app.orders", "app.order_items"]
      - role: support
        privileges: [SELECT]
        on_tables: ["app.customers"]
        columns: ["id", "email", "created_at"]
```

With `columns`, `SELECT * FROM app.customers` fails for `support`, while
`SELECT id, email FROM app.customers` works.

`on_tables` grants are applied **after migrations**, so they can name tables that a migration
creates in the same run. Table names are schema-qualified and case-sensitive
(`App.Orders` is not `app.orders`). Unlike `on_tables_in_schema`, they don't cover future
tables: each table is named explicitly.

If the application runs its own migrations, the tables may not exist yet when the provisioner
runs. [When the application runs its own migrations](app-migrations.md) explains how that
plays out.

## Level 3: every table in every database

A backup or data-export tool may need to read *everything*. PostgreSQL 14 and later have a
predefined role for that:

```yaml
roles:
  - name: exporter
    password: ${EXPORTER_PASSWORD}
    member_of: [pg_read_all_data]
    options:
      login: true
```

`pg_read_all_data` acts as if the role had `SELECT` on all tables and `USAGE` on all schemas,
in every database, including ones created later. It's convenient and very broad, so reserve
it for tools that genuinely need it.

## Verify

Connect as the new user and try both a read and a write:

```console
$ psql -h localhost -U reporting -d shop -c "SELECT count(*) FROM app.orders"
$ psql -h localhost -U reporting -d shop -c "DELETE FROM app.orders"
ERROR:  permission denied for table orders
```

## Pitfalls

- **`permission denied for schema app`:** the `USAGE` grant on the schema is missing.
- **A table created by the application isn't readable:** default privileges only apply to
  tables created by the admin user and the schema owner. If a third role creates tables, the
  next provisioning run grants on them. To make it immediate, let that role create the tables
  as the schema owner, or run the provisioner after its migrations.
- **Read-only doesn't mean harmless:** a read-only user can still run expensive queries. Use
  `connection_limit` and a `statement_timeout` setting, as above.
- **Can't connect at all:** if you revoked `CONNECT` from `PUBLIC` (see
  [Harden a database](harden-database.md)), add an `on_database` `CONNECT` grant for the user.

## See also

- [Grant targets](../configuration.md#grants) in the configuration reference.
- [Monitor Postgres with postgres_exporter](monitoring.md) for statistics instead of data.
