# Stream table changes with publications

After this you have a publication and a change data capture (CDC) user in every environment,
and adding a table to the stream is a one-line config change.

## When you need this

The shop's order events should flow into Kafka (or Redpanda) so other services can react to
them. Instead of a dual write in the application, you want a CDC connector such as
[Debezium](https://debezium.io/documentation/reference/stable/connectors/postgresql.html) or
[Redpanda Connect](https://docs.redpanda.com/redpanda-connect/components/inputs/postgres_cdc/)
to read every change from the database. You want the database side of that, the user and the
list of streamed tables, to be part of the provisioned setup instead of a manual step.

## What you need to know first

- **Write-ahead log (WAL):** PostgreSQL writes every change to this log before it applies it
  to the tables. CDC connectors read their changes from it.
  [WAL introduction](https://www.postgresql.org/docs/current/wal-intro.html).
- **Logical replication:** decodes the WAL into row-level changes (insert, update, delete,
  truncate) per table, instead of raw disk blocks. It needs the server setting
  `wal_level=logical`.
  [Logical replication](https://www.postgresql.org/docs/current/logical-replication.html).
- **Publication:** a named set of tables, and of the operations on them, whose changes
  logical replication sends out. The connector subscribes by publication name. This is what
  the provisioner manages.
  [Publication](https://www.postgresql.org/docs/current/logical-replication-publication.html).
- **Replication slot:** the connector's bookmark in the WAL. The server keeps all WAL the
  slot hasn't confirmed yet. The connector creates its own slot; the provisioner doesn't
  manage slots.
  [Replication slots](https://www.postgresql.org/docs/current/logicaldecoding-explanation.html#LOGICALDECODING-REPLICATION-SLOTS).
- **`REPLICA IDENTITY`:** which columns identify the old row in an update or delete event.
  The default is the primary key; a table without one can't publish updates or deletes.
  [REPLICA IDENTITY](https://www.postgresql.org/docs/current/sql-altertable.html#SQL-ALTERTABLE-REPLICA-IDENTITY).

## Steps

### 1. Enable logical replication on the server

This is the one part the provisioner can't do, because it requires a server restart. Start
Postgres with `wal_level=logical`:

```yaml
  postgres:
    image: postgres:16-alpine
    command: ["postgres", "-c", "wal_level=logical"]
```

On managed services this is a parameter group or flag (e.g. `rds.logical_replication=1` on
Amazon RDS). If it's missing, the provisioner still creates the publication but logs a warning,
because nothing flows until the server is restarted with it.

### 2. Create a CDC user

The connector logs in with the `REPLICATION` attribute, which allows it to open a replication
connection and create its slot:

```yaml
roles:
  - name: cdc
    password: ${CDC_PASSWORD}
    options:
      login: true
      replication: true
```

The admin the provisioner logs in as must be allowed to grant `REPLICATION`. See
[Pitfalls](#pitfalls) if it isn't a superuser or you're on a managed service.

### 3. Declare the publication and the grants

The connector reads the published tables once for its initial snapshot, so it needs
`USAGE` on the schema and `SELECT` on the tables. The schema `app` and the tables
`app.orders` and `app.order_items` come from your setup: here the schema is declared in the
same database and the tables are created by its migrations.

```yaml
databases:
  - name: shop
    schemas:
      - name: app                      # already there if you followed Getting started
    grants:
      - role: cdc
        privileges: [USAGE]
        on_schema: app
      - role: cdc
        privileges: [SELECT]           # for the connector's initial snapshot
        on_tables: ["app.orders", "app.order_items"]
    publications:
      - name: shop_cdc
        tables: ["app.orders", "app.order_items"]
        publish: [insert, update, delete]   # optional; default: insert, update, delete, truncate
```

Publications are provisioned **after migrations**, so they can include tables a migration
creates in the same run. Point the connector at the publication and stop it from creating its
own, e.g. in Debezium with `publication.name=shop_cdc` and
`publication.autocreate.mode=disabled`.

### 4. Grow the stream

Later, payments should be streamed too. Add the table to both the grant and the publication:

```yaml
        on_tables: ["app.orders", "app.order_items", "app.payments"]
    # …
        tables: ["app.orders", "app.order_items", "app.payments"]
```

The next run executes `ALTER PUBLICATION "shop_cdc" ADD TABLE "app"."payments"`.

A changed `publish` list is applied with
`ALTER PUBLICATION "shop_cdc" SET (publish = '<operations>')`. Removing `publish:` altogether
resets the publication to all four operations, **including `truncate`**.

What about removing a table? The provisioner **doesn't** drop it from the publication; it logs
a warning instead. A consumer may depend on that table, and cutting it off silently is worse
than a stale entry. When you're sure, drop it yourself:

```sql
ALTER PUBLICATION <publication> DROP TABLE <schema>.<table>;
```

## Check that it works

The server runs with logical replication, and the CDC user has the `REPLICATION` attribute:

```console
$ psql -U admin -d shop -c 'SHOW wal_level' -c "SELECT rolname, rolreplication FROM pg_roles WHERE rolname = 'cdc'"
 wal_level
-----------
 logical
(1 row)

 rolname | rolreplication
---------+----------------
 cdc     | t
(1 row)
```

The publication lists the operations and tables from step 4:

```console
$ psql -U admin -d shop -c '\dRp+ shop_cdc'
                    Publication shop_cdc
 Owner | All tables | Inserts | Updates | Deletes | Truncates | Via root
-------+------------+---------+---------+---------+-----------+----------
 admin | f          | t       | t       | t       | f         | f
Tables:
    "app.order_items"
    "app.orders"
    "app.payments"
```

## Pitfalls

- **The run fails on `REPLICATION`.** Only a superuser, or on PostgreSQL 16 and later a role
  that has `REPLICATION` itself, may give a role that attribute
  ([CREATE ROLE](https://www.postgresql.org/docs/current/sql-createrole.html)). The
  provisioner sends the clause only when the role doesn't have the attribute yet, so an admin
  without that right fails when it creates the role or turns the attribute on. On
  managed services that don't hand out `REPLICATION` (e.g. Amazon RDS), leave out
  `replication: true` and use the provider's role instead: `member_of: [rds_replication]`.
  See [Privileges the admin user needs](../deployment.md#privileges-the-admin-user-needs).
- **Updates and deletes fail on the source.** A table published for `update` or `delete`
  needs a primary key or a `REPLICA IDENTITY`. Without one, PostgreSQL rejects those statements
  on that table, which breaks the application, not the connector. Add primary keys before
  publishing.
- **Disk fills up.** Replication slots are created by the connector, not the provisioner. A
  slot whose consumer is gone retains WAL indefinitely. Monitor `pg_replication_slots` and
  drop slots you no longer need.
- **Only tables.** Publications accept tables and partitioned tables (a partitioned table is
  published with its partitions). Views and other relations are rejected before anything is
  changed.
- **`all_tables: true`** publishes every table, including future ones, but requires a
  superuser, and can't be switched on or off for an existing publication.
- **Tables that don't exist yet** follow the same rule as `on_tables` grants. See
  [Grant access when the application runs its own migrations](app-migrations.md#specific-tables-how-on_tables-behaves).

## Further reading

- [Publications](../configuration.md#publications) in the configuration reference.
- [Logical replication](https://www.postgresql.org/docs/current/logical-replication.html) in
  the PostgreSQL documentation.
- [Debezium PostgreSQL connector](https://debezium.io/documentation/reference/stable/connectors/postgresql.html)
  and [Redpanda Connect `postgres_cdc` input](https://docs.redpanda.com/redpanda-connect/components/inputs/postgres_cdc/).
