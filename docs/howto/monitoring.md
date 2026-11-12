# Monitor Postgres with postgres_exporter

**The situation:** Prometheus scrapes your database through
[postgres_exporter](https://github.com/prometheus-community/postgres_exporter), and like in
most setups, its `DATA_SOURCE_NAME` contains the admin credentials. It works, but the
exporter's host now holds a superuser password. Anyone who can read its environment can
drop every database you have.

**The goal:** a dedicated `postgres_exporter` login that can read every statistic the
exporter needs, and nothing else.

## 1. Create the monitoring role

PostgreSQL ships with a predefined role for exactly this: `pg_monitor`. It bundles
`pg_read_all_settings`, `pg_read_all_stats` and `pg_stat_scan_tables`, so its members can read
`pg_stat_activity` (including other sessions' queries), replication status, database sizes
and `pg_stat_statements`, but **no table data**.

Make the exporter's login a member with `member_of`:

```yaml
roles:
  - name: postgres_exporter
    password: ${POSTGRES_EXPORTER_PASSWORD}
    member_of: [pg_monitor]
    options:
      login: true
      connection_limit: 5        # scrapes are short; this caps the damage of a misconfiguration
```

Predefined roles like `pg_monitor` don't need to be declared under `roles`. The membership
is granted after all roles exist, so the order in the file doesn't matter, and a membership
that already exists is skipped.

Provision it and the exporter account exists.

## 2. Point the exporter at the new account

```yaml
  postgres-exporter:
    image: quay.io/prometheuscommunity/postgres-exporter
    environment:
      DATA_SOURCE_URI: postgres:5432/postgres?sslmode=disable
      DATA_SOURCE_USER: postgres_exporter
      DATA_SOURCE_PASS: exportersecret
    depends_on:
      postgres-provisioner:
        condition: service_completed_successfully
```

Check its `/metrics` endpoint: `pg_up 1` and the `pg_stat_*` series should be there. Then
remove the admin credentials from the exporter's configuration.

## 3. Optional: query statistics with `pg_stat_statements`

The exporter's query metrics come from the `pg_stat_statements` extension. Install it in the
database the exporter connects to:

```yaml
databases:
  - name: shop
    extensions:
      - pg_stat_statements
```

The extension only works if its library is loaded at server start. Since that needs a
restart, the provisioner can't do it for you, so it checks: if `pg_stat_statements` isn't in
`shared_preload_libraries`, the extension is still installed and a warning tells you what to
add. In Compose:

```yaml
  postgres:
    image: postgres:16-alpine
    command: ["postgres", "-c", "shared_preload_libraries=pg_stat_statements"]
```

## 4. If you revoked `CONNECT` from `PUBLIC`

New databases let every role connect. If you [hardened a database](harden-database.md) by
revoking that, grant it back to the exporter for each database it scrapes:

```yaml
    grants:
      - role: postgres_exporter
        privileges: [CONNECT]
        on_database: true
```

## Pitfalls

- **The provisioner isn't a superuser (PostgreSQL 16+).** Granting a membership requires
  `ADMIN OPTION` on the group role: `GRANT pg_monitor TO admin WITH ADMIN OPTION`. In
  PostgreSQL 16 and later, `CREATEROLE` alone is no longer enough.
- **`inherit: false` on the role.** A membership's `INHERIT` flag is fixed when it is granted
  (PostgreSQL 16+), taken from the member role's `inherit` option at that moment. A
  non-inheriting membership means the exporter would have to `SET ROLE pg_monitor` to use it,
  which it doesn't. Changing `inherit` later doesn't update the existing membership, and the
  provisioner sees it as already granted. Fix it once by hand:
  `GRANT pg_monitor TO postgres_exporter WITH INHERIT TRUE`.
- **The admin can't read `shared_preload_libraries`.** Then the preload check is skipped and
  logged at info level. Give the admin `pg_read_all_settings` if you want the check.

## See also

- [Role memberships](../configuration.md#role-memberships) and
  [extensions](../configuration.md#extensions) in the configuration reference.
- [Read-only and reporting users](read-only-users.md) for people who need to read table data.
