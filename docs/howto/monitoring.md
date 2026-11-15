# Monitor Postgres with postgres_exporter

After this, Prometheus scrapes your database through a dedicated `postgres_exporter` login
that can read every statistic the exporter needs, and nothing else.

## When you need this

Prometheus scrapes your database through
[postgres_exporter](https://github.com/prometheus-community/postgres_exporter), and like in
most setups, its connection string contains the admin credentials. It works, but the
exporter's host now holds a superuser password. Anyone who can read its environment can drop
every database you have.

## What you need to know first

- **Predefined roles and `pg_monitor`:** PostgreSQL ships with built-in group roles for common
  privilege sets. `pg_monitor` bundles `pg_read_all_settings`, `pg_read_all_stats` and
  `pg_stat_scan_tables`, so its members can read `pg_stat_activity` (including other
  sessions' queries), replication status, database sizes and `pg_stat_statements`, but **no
  table data**. [Predefined roles](https://www.postgresql.org/docs/current/predefined-roles.html).
- **`pg_stat_statements`:** an extension that records statistics per query. Its view exists
  only in the database where you created the extension, so the exporter must connect to
  that database to read it.
  [pg_stat_statements](https://www.postgresql.org/docs/current/pgstatstatements.html).
- **The `postgres` database is off limits to the provisioner.** `postgres`, `template0` and
  `template1` are reserved names: a config that declares one of them is rejected. Put
  extensions and grants into a database the provisioner manages, such as `shop`.
- **How the provisioner reads secrets:** a `${VAR}` in the config is read from the
  environment variable `VAR` or from the file named by `VAR_FILE`.
  [Environment variable expansion](../configuration.md#environment-variable-expansion).

## Steps

### 1. Create the monitoring role

Make the exporter's login a member of `pg_monitor` with `member_of`:

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

Provision it, with `POSTGRES_EXPORTER_PASSWORD_FILE` pointing at a secret file as shown in
[Manage secrets](secrets.md), and the exporter account exists.

### 2. Point the exporter at the new account

The exporter reads its password from the same secret file through `DATA_SOURCE_PASS_FILE`, so
the password isn't in the Compose file. It connects to `shop`, the database that gets
`pg_stat_statements` in the next step. Server-wide statistics such as `pg_up` and
`pg_stat_database` are the same from any database.

```yaml
services:
  postgres-exporter:
    image: quay.io/prometheuscommunity/postgres-exporter
    command: ["--collector.stat_statements"]   # only needed for step 3
    environment:
      DATA_SOURCE_URI: postgres:5432/shop?sslmode=disable
      DATA_SOURCE_USER: postgres_exporter
      DATA_SOURCE_PASS_FILE: /run/secrets/postgres_exporter_password
    secrets: [postgres_exporter_password]
    ports:
      - "9187:9187"
    depends_on:
      postgres-provisioner:
        condition: service_completed_successfully

secrets:
  postgres_exporter_password:
    file: ./secrets/postgres_exporter_password
```

Once [Check that it works](#check-that-it-works) passes, remove the admin credentials from the
exporter's configuration.

### 3. Optional: query statistics with `pg_stat_statements`

The exporter's query metrics come from the `pg_stat_statements` extension. Install it in the
database the exporter connects to:

```yaml
databases:
  - name: shop
    extensions:
      - pg_stat_statements
```

The exporter's `stat_statements` collector is **off by default**; the `--collector.stat_statements`
flag in step 2 turns it on
([postgres_exporter flags](https://github.com/prometheus-community/postgres_exporter#flags)).

The extension only works if its library is loaded at server start. Since that needs a
restart, the provisioner can't do it for you, so it checks: if `pg_stat_statements` isn't in
`shared_preload_libraries`, the extension is still installed and a warning tells you what to
add. In Compose:

```yaml
  postgres:
    image: postgres:16-alpine
    command: ["postgres", "-c", "shared_preload_libraries=pg_stat_statements"]
```

### 4. If you revoked `CONNECT` from `PUBLIC`

New databases let every role connect. If you [hardened a database](harden-database.md) by
revoking that, grant it back to the exporter in the database it connects to:

```yaml
databases:
  - name: shop
    grants:
      - role: postgres_exporter
        privileges: [CONNECT]
        on_database: true
```

This works for managed databases like `shop` only. The provisioner can't revoke or grant
anything on the `postgres` database; if you hardened that one by hand, grant `CONNECT` there
by hand too.

## Check that it works

The exporter reaches the database with its own account:

```console
$ curl -s http://localhost:9187/metrics | grep '^pg_up'
pg_up 1
```

`pg_up 0` means the exporter can't connect; its logs name the reason. If you did step 3, the
same endpoint also has lines starting with `pg_stat_statements_`:

```console
$ curl -s http://localhost:9187/metrics | grep -c '^pg_stat_statements_'
```

Expected: a number greater than `0`.

The role is a member of `pg_monitor`, with an inheriting membership (the `inherit_option`
column exists on PostgreSQL 16 and later):

```console
$ psql -U admin -d shop -c "SELECT m.rolname AS member, g.rolname AS group_role, a.inherit_option
    FROM pg_auth_members a
    JOIN pg_roles m ON m.oid = a.member
    JOIN pg_roles g ON g.oid = a.roleid
    WHERE m.rolname = 'postgres_exporter'"
      member       | group_role | inherit_option
-------------------+------------+----------------
 postgres_exporter | pg_monitor | t
(1 row)
```

## Pitfalls

- **The provisioner isn't a superuser (PostgreSQL 16+).** Granting a membership requires
  `ADMIN OPTION` on the group role: `GRANT pg_monitor TO admin WITH ADMIN OPTION`. In
  PostgreSQL 16 and later, `CREATEROLE` alone is no longer enough.
- **`inherit: false` on the role.** A membership's `INHERIT` flag is fixed when it is granted
  (PostgreSQL 16+), taken from the member role's `inherit` option at that moment. A
  non-inheriting membership means the exporter would have to `SET ROLE pg_monitor` to use it,
  which it doesn't. Changing `inherit` later doesn't update the existing membership, and the
  provisioner sees it as already granted. You recognise it by `inherit_option` `f` in
  [Check that it works](#check-that-it-works). Fix it once by hand:
  `GRANT pg_monitor TO postgres_exporter WITH INHERIT TRUE`.
- **No query metrics.** Either the exporter connects to a database without the extension
  (such as `postgres`), the `--collector.stat_statements` flag is missing, or the library
  isn't preloaded (the provisioner logged a warning).
- **The admin can't read `shared_preload_libraries`.** Then the preload check is skipped and
  logged at info level. Give the admin `pg_read_all_settings` if you want the check.

## Further reading

- [Role memberships](../configuration.md#role-memberships) and
  [extensions](../configuration.md#extensions) in the configuration reference.
- [postgres_exporter README](https://github.com/prometheus-community/postgres_exporter) for
  its environment variables and collectors.
- [Manage secrets](secrets.md) for the secret files used here.
- [Connect to a managed or TLS-only server](connect-over-tls.md) when the exporter's
  `sslmode=disable` isn't an option.
- [Read-only and reporting users](read-only-users.md) for people who need to read table data.
