# Harden a database

**The situation:** several services share one Postgres server, each with its own database and
role. A security review finds that the `billing` service's role can connect to the `shop`
database and create temporary tables there. Nobody granted that. PostgreSQL did, by default.

Every new database grants `CONNECT` and `TEMPORARY` to `PUBLIC`, the pseudo-role that every
role is implicitly a member of. On PostgreSQL 14 and older, every role can also `CREATE` in
the `public` schema of every database.

**The goal:** each database accepts connections only from the roles that are meant to use it.

## 1. Revoke the defaults from `PUBLIC`

The provisioner is additive: it never removes privileges on its own. Removing them is an
explicit act, through `revokes`:

```yaml
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

`PUBLIC` doesn't need to be declared under `roles`; it is written as the SQL keyword.
Revokes support `on_database` and `on_schema`.

## 2. Grant access back, explicitly

After step 1, only the database owner and superusers can connect. Grant `CONNECT` to each
role that needs it:

```yaml
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
converges reliably, and running it again changes nothing: a revoke is only executed while the
grantee still holds the privilege.

## 3. Preview, then apply to existing databases

Hardening usually targets databases that already exist. Preview first:

```console
$ postgres-provisioner --dry-run
{"level":"INFO","msg":"Revoking privileges","role":"PUBLIC","on":"database \"shop\"","privileges":"CONNECT, TEMPORARY"}
{"level":"INFO","msg":"[DRY RUN] would execute","sql":"REVOKE CONNECT, TEMPORARY ON DATABASE \"shop\" FROM PUBLIC"}
```

`ALL` was narrowed to the privileges `PUBLIC` actually holds. On the next run the revoke
finds nothing to do and logs `Privileges already revoked, skipping`.

Make sure the database uses `strategy: update` (the default). With `strategy: create`, an
existing database is skipped completely, revokes included.

## 4. Verify

```console
$ psql -h localhost -U billing_app -d shop
psql: error: connection to server ... failed: FATAL:  permission denied for database "shop"
DETAIL:  User does not have CONNECT privilege.
```

## Pitfalls

- **Revoking from a role doesn't block it if `PUBLIC` still grants it.** Revoking `CONNECT`
  from `billing_app` alone changes nothing while `PUBLIC` holds `CONNECT`. Roles also keep
  privileges they inherit from other roles. Revoke from `PUBLIC` (or from those roles) as well.
- **A conflicting grant is a config error.** Revoking `CONNECT` from a role and granting it
  `CONNECT` on the same database is rejected when the config is loaded, as is a duplicate
  revoke.
- **Grants made by someone else survive.** `REVOKE` only removes grants made by a specific
  grantor. As a superuser the provisioner revokes as the object owner, which covers the
  defaults. If a privilege survives because another role granted it, a warning names it.
- **"dependent privileges exist":** the grantee received the privilege `WITH GRANT OPTION`
  and passed it on. The provisioner never uses `CASCADE`, so the run fails with a hint. Revoke
  the dependent grants first, or run `REVOKE ... CASCADE` by hand if that's what you want.

## See also

- [Revokes](../configuration.md#revokes) in the configuration reference.
- [Read-only and reporting users](read-only-users.md) to grant access back in finer steps.
