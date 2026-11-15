# Create read-only users

After this you can give each consumer of your data a read-only login at exactly the level it
needs, declared next to the rest of the database setup.

## When you need this

Sooner or later someone besides the application needs the data: finance wants a business
intelligence (BI) dashboard, support needs to look up orders, a data team wants a nightly
export. Handing out the application's credentials gives them write access to everything.
Creating users by hand in `psql` works until the next environment, when nobody remembers what
was granted.

## What you need to know first

- **Schema `USAGE`.** PostgreSQL checks `USAGE` on a schema before it looks at any table in
  it, so reading a table always takes two grants: one on the schema and one on the table. See
  [Privileges](https://www.postgresql.org/docs/current/ddl-priv.html) in the PostgreSQL docs.
- **Default privileges.** `ALTER DEFAULT PRIVILEGES` tells PostgreSQL which grants to add
  automatically to objects that a given role creates later. They apply only to objects
  created by the role they were set for. See
  [ALTER DEFAULT PRIVILEGES](https://www.postgresql.org/docs/current/sql-alterdefaultprivileges.html)
  and [Grants](../configuration.md#grants) in the configuration reference.
- **Schema owner.** The provisioner sets default privileges for itself (the admin user it
  logs in as) and for the schema's configured owner: `schemas[].owner`, or else the
  database's `owner`. With no owner configured, only the admin user's default privileges are
  set. See [Schemas](../configuration.md#schemas).
- **Predefined roles.** PostgreSQL ships roles such as `pg_read_all_data` that carry a
  bundle of privileges; you make a login a member of one with `member_of`. See
  [Predefined roles](https://www.postgresql.org/docs/current/predefined-roles.html) and
  [Role memberships](../configuration.md#role-memberships).

The names `shop`, `app`, `shop_app`, `reporting`, `support` and `exporter` in the examples
are placeholders; replace them with your own database, schema and role names.

## Steps

There are three levels. Pick the narrowest one that does the job.

### Level 1: everything in one schema

The usual case: a reporting user that may read all tables in the application's schema,
including tables added by future migrations.

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
      connection_limit: 3
    settings:
      statement_timeout: "60s"        # a runaway dashboard query is cancelled, not your app

databases:
  - name: shop
    owner: shop_app                   # also owns schema app, unless the schema sets its own owner
    schemas:
      - name: app                     # created if missing, so the grants below find it
    grants:
      - role: reporting
        privileges: [USAGE]
        on_schema: app                # may look into the schema...
      - role: reporting
        privileges: [SELECT]
        on_tables_in_schema: app      # ...and read every table in it
```

Both grants are needed, because of the schema `USAGE` check described above.

`on_tables_in_schema` grants `SELECT` on the tables that exist now and also sets **default
privileges**, so tables created later get the grant automatically. That covers tables
created by the provisioner's migrations (they run as the admin user) and, because an owner is
configured, tables created by `shop_app`. Without `owner`, only the admin user's tables are
covered. Tables created by any other role get the grant on the next provisioning run.

Views count as tables here, so reporting views your migrations create are covered too.

### Level 2: specific tables and columns

Support needs to look up customers by email, but shouldn't see password hashes or addresses.
Grant on individual tables, and optionally on individual columns. Add this to the config from
Level 1:

```yaml
roles:
  # … shop_app and reporting from Level 1
  - name: support
    password: ${SUPPORT_PASSWORD}
    options:
      login: true

databases:
  - name: shop
    owner: shop_app
    schemas:
      - name: app
    grants:
      # … reporting's grants from Level 1
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

The provisioner applies `on_tables` grants **after migrations**, so they can name tables that
a migration creates in the same run. Table names are schema-qualified and case-sensitive
(`App.Orders` is not `app.orders`). Unlike `on_tables_in_schema`, they don't cover future
tables: you name each table explicitly.

If the application runs its own migrations, the tables may not exist yet when the provisioner
runs. [Grant access when the application runs its own migrations](app-migrations.md) explains
how that plays out.

### Level 3: every table in every database

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

## Check that it works

Run the provisioner, then connect as the new user and try a read. It returns a row count (the
number depends on your data):

```console
$ psql -h localhost -U reporting -d shop -c "SELECT count(*) FROM app.orders"
 count
-------
    42
(1 row)
```

A write fails:

```console
$ psql -h localhost -U reporting -d shop -c "DELETE FROM app.orders"
ERROR:  permission denied for table orders
```

For a Level 2 user, check that a hidden column stays hidden:

```console
$ psql -h localhost -U support -d shop -c "SELECT * FROM app.customers"
ERROR:  permission denied for table customers
```

## Pitfalls

- **`permission denied for schema app`:** the `USAGE` grant on the schema is missing.
- **A table created by the application isn't readable:** default privileges only apply to
  tables created by the admin user and by the configured schema owner (`schemas[].owner`, or
  else the database `owner`). If no owner is configured, or a third role creates the tables,
  the next provisioning run grants on them. To make it immediate, configure the role that
  creates the tables as the owner, or run the provisioner after its migrations. See
  [Separate the migration owner from the application role](separate-migration-owner.md).
- **Read-only doesn't mean harmless:** a read-only user can still run expensive queries. Use
  `connection_limit` and a `statement_timeout` setting, as in Level 1.
- **Can't connect at all:** if you revoked `CONNECT` from `PUBLIC`, the pseudo-role every
  role belongs to (see [Harden a database](harden-database.md)), add an `on_database`
  `CONNECT` grant for the user.
- **Many consumers with the same access:** repeating the same grants for each login drifts
  over time. Grant once to a group role and make the logins members, as in
  [Manage access with group roles](manage-access-with-group-roles.md).

## Further reading

- [Grants](../configuration.md#grants) and
  [Tables and columns](../configuration.md#tables-and-columns) in the configuration reference.
- [ALTER DEFAULT PRIVILEGES](https://www.postgresql.org/docs/current/sql-alterdefaultprivileges.html)
  in the PostgreSQL docs.
- [Monitor Postgres with postgres_exporter](monitoring.md) for statistics instead of data.
