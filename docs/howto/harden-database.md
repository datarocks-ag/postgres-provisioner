# Harden a database

After this each database accepts connections only from the roles that are meant to use it.

## When you need this

Several services share one Postgres server, each with its own database and role. A security
review finds that the `billing` service's role can connect to the `shop` database and create
temporary tables there. Nobody granted that. PostgreSQL did, by default.

## What you need to know first

- **`PUBLIC`.** `PUBLIC` is the pseudo-role that every role is implicitly a member of.
  Every new database grants `CONNECT` and `TEMPORARY` to `PUBLIC`. On PostgreSQL 14 and
  older, `PUBLIC` can also `CREATE` in the `public` schema of every database. See
  [Privileges](https://www.postgresql.org/docs/current/ddl-priv.html) in the PostgreSQL docs.
- **Additive by design.** The provisioner never removes privileges on its own; you remove
  them explicitly with `revokes`. See [Additive by design](../concepts.md#additive-by-design).
- **Grantor and `WITH GRANT OPTION`.** PostgreSQL records who granted each privilege (the
  grantor), and `REVOKE` only removes grants made by a specific grantor. A role that holds a
  privilege `WITH GRANT OPTION` may pass it on, and that grant then depends on its own. See
  [REVOKE](https://www.postgresql.org/docs/current/sql-revoke.html).
- **Strategy.** A database's `strategy` decides whether the provisioner reconciles it when
  it already exists (`update`, the default) or leaves it alone (`create`). See
  [Strategy: `update` or `create`](../concepts.md#strategy-update-or-create).

## Steps

The names `shop`, `shop_app`, `shop_reporting` and `billing_app` in the examples are
placeholders; replace them with your own database and role names.

### 1. Revoke the defaults from `PUBLIC`

Remove the defaults explicitly, through `revokes`:

```yaml
roles:
  - name: shop_app
    password: ${SHOP_APP_PASSWORD}
    options:
      login: true

databases:
  - name: shop
    owner: shop_app
    revokes:
      - role: PUBLIC
        privileges: [ALL]          # CONNECT, TEMPORARY and CREATE on the database
        on_database: true
      - role: PUBLIC
        privileges: [CREATE]       # needed on PostgreSQL 14 and older
        on_schema: public
```

You don't declare `PUBLIC` under `roles`; you write it as the SQL keyword.
Revokes support `on_database` and `on_schema`.

### 2. Grant access back, explicitly

After step 1, only the database owner and superusers can connect. Declare each role that
needs access and grant it `CONNECT`:

```yaml
roles:
  # … shop_app from step 1
  - name: shop_reporting
    password: ${SHOP_REPORTING_PASSWORD}
    options:
      login: true
  - name: postgres_exporter        # see Monitor Postgres with postgres_exporter
    password: ${POSTGRES_EXPORTER_PASSWORD}
    member_of: [pg_monitor]
    options:
      login: true

databases:
  - name: shop
    owner: shop_app
    revokes:
      # … from step 1
    grants:
      - role: shop_reporting
        privileges: [CONNECT]
        on_database: true
      - role: postgres_exporter
        privileges: [CONNECT]
        on_database: true
```

The owner (`shop_app`) doesn't need a grant: owners always hold all privileges on their
database.

Revokes run **before** grants in every run. So "revoke from everyone, grant to these roles"
converges reliably, and running it again changes nothing: the provisioner only executes a
revoke while the grantee still holds the privilege.

### 3. Preview, then apply to existing databases

Hardening usually targets databases that already exist, so preview first. The output below
is abridged: it omits the `time` key and unrelated lines.

```console
$ postgres-provisioner --dry-run
{"level":"INFO","msg":"Revoking privileges","role":"PUBLIC","on":"database \"shop\"","privileges":"CONNECT, TEMPORARY"}
{"level":"INFO","msg":"[DRY RUN] would execute","sql":"REVOKE CONNECT, TEMPORARY ON DATABASE \"shop\" FROM PUBLIC"}
```

The provisioner narrowed `ALL` to the privileges `PUBLIC` actually holds. On PostgreSQL 14
and older, `PUBLIC` also holds `CREATE` on the `public` schema, so two more lines follow:

```console
{"level":"INFO","msg":"Revoking privileges","role":"PUBLIC","on":"schema \"public\"","privileges":"CREATE"}
{"level":"INFO","msg":"[DRY RUN] would execute","sql":"REVOKE CREATE ON SCHEMA \"public\" FROM PUBLIC"}
```

On PostgreSQL 15 and later, the schema revoke logs `Privileges already revoked, skipping`
instead. Run the provisioner without `--dry-run` to apply the revokes. On the next run, each
revoke finds nothing to do and logs `Privileges already revoked, skipping`.

Make sure the database uses `strategy: update` (the default). With `strategy: create`, the
provisioner skips an existing database completely, revokes included.

## Check that it works

A role without a grant can no longer connect:

```console
$ psql -h localhost -U billing_app -d shop
psql: error: connection to server ... failed: FATAL:  permission denied for database "shop"
DETAIL:  User does not have CONNECT privilege.
```

A role you granted `CONNECT` still can:

```console
$ psql -h localhost -U shop_reporting -d shop -c "SELECT current_user"
  current_user
----------------
 shop_reporting
(1 row)
```

The database's access control list shows the result. Run this as the admin user:

```console
$ psql -h localhost -U admin -d shop -c "SELECT datacl FROM pg_database WHERE datname = 'shop'"
                                     datacl
--------------------------------------------------------------------------------
 {shop_app=CTc/shop_app,shop_reporting=c/shop_app,postgres_exporter=c/shop_app}
(1 row)
```

Each entry reads `grantee=privileges/grantor`, with `C` for `CREATE`, `T` for `TEMPORARY` and
`c` for `CONNECT`. An entry without a grantee, `=Tc/shop_app`, would be `PUBLIC`; it is gone.
The order of the entries may differ.

## Pitfalls

- **Revoking from a role doesn't block it if `PUBLIC` still grants it.** Revoking `CONNECT`
  from `billing_app` alone changes nothing while `PUBLIC` holds `CONNECT`. Roles also keep
  privileges they inherit from other roles. Revoke from `PUBLIC` (or from those roles) as well.
- **A conflicting grant is a config error.** Revoking `CONNECT` from a role and granting it
  `CONNECT` on the same database makes the provisioner reject the config when it loads it, as
  does a duplicate revoke.
- **Grants made by someone else survive.** `REVOKE` only removes grants made by a specific
  grantor. As a superuser the provisioner revokes as the object owner, which covers the
  defaults. If a privilege survives because another role granted it, a warning names it.
- **"dependent privileges exist":** the grantee received the privilege `WITH GRANT OPTION`
  and passed it on. The provisioner never uses `CASCADE`, so the run fails with a hint. Revoke
  the dependent grants first, or run `REVOKE ... CASCADE` by hand if that's what you want.

## Further reading

- [Revokes](../configuration.md#revokes) in the configuration reference.
- [Create read-only users](read-only-users.md) to grant access back in finer steps.
- [Manage access with group roles](manage-access-with-group-roles.md) to grant `CONNECT` once
  to a group instead of to each login.
- [Monitor Postgres with postgres_exporter](monitoring.md) for the `postgres_exporter` role.
