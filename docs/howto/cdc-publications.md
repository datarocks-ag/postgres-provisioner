# Change data capture with publications

**The situation:** the shop's order events should flow into Kafka (or Redpanda) so other
services can react to them. Instead of a dual write in the application, you want change data
capture: a connector such as Debezium or Redpanda Connect reads the database's
write-ahead log. On the database side, that needs logical replication: a `wal_level` setting,
a replication user, and a **publication** listing the tables to stream.

**The goal:** the publication and the CDC user are part of the provisioned setup, so every
environment has them, and adding a table to the stream is a one-line config change.

## 1. Enable logical replication on the server

This is the one part the provisioner can't do, because it requires a server restart. Start
Postgres with `wal_level=logical`:

```yaml
  postgres:
    image: postgres:16-alpine
    command: ["postgres", "-c", "wal_level=logical"]
```

On managed services this is a parameter group or flag (e.g. `rds.logical_replication=1` on
Amazon RDS). If it's missing, the provisioner still creates the publication but logs a warning,
because nothing will flow until the server is restarted with it.

## 2. Create a CDC user

The connector logs in with the `REPLICATION` attribute and reads the published tables:

```yaml
roles:
  - name: cdc
    password: ${CDC_PASSWORD}
    options:
      login: true
      replication: true
```

## 3. Declare the publication and the grants

```yaml
databases:
  - name: shop
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
creates in the same run. Point the connector at it, e.g. Debezium with
`publication.name=shop_cdc` and `publication.autocreate.mode=disabled`, so the connector
doesn't create its own.

## 4. Grow the stream

Later, payments should be streamed too. Add the table:

```yaml
        tables: ["app.orders", "app.order_items", "app.payments"]
```

The next run executes `ALTER PUBLICATION shop_cdc ADD TABLE "app"."payments"`. A changed
`publish` list is applied the same way.

What about removing a table? The provisioner **doesn't** drop it from the publication; it logs
a warning instead. A consumer may depend on that table, and cutting it off silently is worse
than a stale entry. Run `ALTER PUBLICATION ... DROP TABLE` yourself when you're sure.

## 5. Verify

```console
$ psql -U admin -d shop -c '\dRp+ shop_cdc'
                    Publication shop_cdc
 Owner | All tables | Inserts | Updates | Deletes | Truncates | Via root
-------+------------+---------+---------+---------+-----------+----------
 admin | f          | t       | t       | t       | f         | f
Tables:
    "app.order_items"
    "app.orders"
```

## Pitfalls

- **Updates and deletes fail on the source.** A table published for `update` or `delete`
  needs a primary key or a `REPLICA IDENTITY`. Without one, PostgreSQL rejects those statements
  on that table, which breaks the application, not the connector. Add primary keys before
  publishing.
- **Disk fills up.** Replication slots are created by the connector, not the provisioner. A
  slot whose consumer is gone retains WAL indefinitely. Monitor
  `pg_replication_slots` and drop slots you no longer need.
- **Only tables.** Publications accept tables and partitioned tables (a partitioned table is
  published with its partitions). Views and other relations are rejected before anything is
  changed.
- **`all_tables: true`** publishes every table, including future ones, but requires a
  superuser, and can't be switched on or off for an existing publication.
- **Tables that don't exist yet** follow the same rule as `on_tables` grants. See
  [When the application runs its own migrations](app-migrations.md#specific-tables-how-on_tables-behaves).

## See also

- [Publications](../configuration.md#publications) in the configuration reference.
