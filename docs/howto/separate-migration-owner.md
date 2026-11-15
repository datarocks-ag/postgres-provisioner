# Separate the migration owner from the application role

After this your application runs with a role that can read and write data but cannot change
the schema, while a separate role owns the tables and runs the migrations.

## When you need this

Your application connects as one role that also owns its tables. That role can drop or alter
any table, so a bug or an injected statement in the running service can do the same. You want
the running service limited to data access (`SELECT`, `INSERT`, `UPDATE`, `DELETE`), and
schema changes reserved for the migration step.

## What you need to know first

- **Schema owner.** `schemas[].owner` sets who owns a schema; without it, the database's
  `owner` applies. The owner may create tables in the schema; other roles need explicit
  grants. See [Schemas](../configuration.md#schemas).
- **Default privileges.** An `on_tables_in_schema` grant also runs `ALTER DEFAULT PRIVILEGES`
  for the admin user and for the schema owner, so tables either of them creates later are
  granted automatically. See [Grants](../configuration.md#grants).
- **Who runs migrations.** The provisioner's own [migrations](../configuration.md#migrations)
  run as the admin user. If your application migrates itself (Flyway, Liquibase, Alembic…),
  its tool connects as whatever role you give it.

## Steps

The example uses a database `orders` with schema `app`, an owner role `orders_owner` and an
application role `orders_app`; replace these names with your own.

### 1. Declare two login roles and make one the schema owner

`orders_owner` is the role your migration tool logs in as. `orders_app` is the role the
running service uses. Both only need `login: true`.

```yaml
roles:
  - name: orders_owner               # owns the schema; the migration tool logs in as this role
    password: ${ORDERS_OWNER_PASSWORD}
    options:
      login: true
  - name: orders_app                 # the running service: data access only
    password: ${ORDERS_APP_PASSWORD}
    options:
      login: true

databases:
  - name: orders
    owner: orders_owner
    schemas:
      - name: app
        owner: orders_owner
```

### 2. Grant the application role data access only

Add the grants under the same database. `USAGE` on the schema lets `orders_app` see into it;
the table grant gives it DML; the sequence grant lets `serial` and `bigserial` columns
generate values on `INSERT`.

```yaml
    grants:
      - role: orders_app
        privileges: [USAGE]
        on_schema: app
      - role: orders_app
        privileges: [SELECT, INSERT, UPDATE, DELETE]
        on_tables_in_schema: app
      - role: orders_app
        privileges: [USAGE, SELECT]
        on_sequences_in_schema: app
```

For the table grant, the provisioner runs these statements in one transaction:

```sql
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA "app" TO "orders_app"
ALTER DEFAULT PRIVILEGES IN SCHEMA "app" GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "orders_app"
ALTER DEFAULT PRIVILEGES FOR ROLE "orders_owner" IN SCHEMA "app" GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "orders_app"
```

The last statement is what makes this setup work on the first deploy: every table
`orders_owner` creates later is usable by `orders_app` at once.

### 3. Point each process at its role

- **The application migrates itself:** give the migration tool the `orders_owner`
  credentials and the running service the `orders_app` credentials. Leave out `migrations:`
  in the provisioner config. [Grant access when the application runs its own migrations](app-migrations.md)
  covers the ordering on the first deploy.
- **The provisioner runs the migrations:** they run as the admin user, which then owns the
  tables. The default privileges for the admin user still grant `orders_app` its DML. To hand
  the tables to `orders_owner`, end the migration with `ALTER TABLE app.<table> OWNER TO orders_owner`.

## Check that it works

Create a table as the owner, then use it as the application role (here with `psql`
against the Compose setup):

```console
$ psql -h localhost -U orders_owner -d orders -c "CREATE TABLE app.orders (id bigserial PRIMARY KEY, total numeric)"
CREATE TABLE
$ psql -h localhost -U orders_app -d orders -c "INSERT INTO app.orders (total) VALUES (42)"
INSERT 0 1
$ psql -h localhost -U orders_app -d orders -c "ALTER TABLE app.orders ADD COLUMN note text"
ERROR:  must be owner of table orders
$ psql -h localhost -U orders_app -d orders -c "CREATE TABLE app.scratch (id int)"
ERROR:  permission denied for schema app
```

`\ddp app` as the admin user lists the default privileges; the row for the owner (output
trimmed to it) is the one that covers the migration tool's tables:

```text
    Owner     | Schema |   Type   |      Access privileges
--------------+--------+----------+------------------------------
 orders_owner | app    | table    | orders_app=arwd/orders_owner
```

## Pitfalls

- **`strategy: create` never changes an existing database.** The schema owner is set when
  the provisioner creates the database. If the database already existed, nothing inside it
  is touched, so run `ALTER SCHEMA "app" OWNER TO "orders_owner"` once by hand or use
  `strategy: update`. See [Strategy](../concepts.md#strategy-update-or-create).
- **A third role creates tables.** Default privileges only cover the admin user and the
  schema owner. Tables created by any other role are granted on the next provisioning run.
- **`permission denied for sequence orders_id_seq`:** `orders_app` inserts into a `serial` column without
  the `on_sequences_in_schema` grant.
- **Granting `ALL` defeats the purpose.** `ALL` on tables includes `TRUNCATE`, `REFERENCES`
  and `TRIGGER`. List the DML privileges explicitly.

## Further reading

- [Grants](../configuration.md#grants) and [Schemas](../configuration.md#schemas) in the
  configuration reference.
- [The provisioning order](../concepts.md#the-provisioning-order): grants run before
  migrations.
- [ALTER DEFAULT PRIVILEGES](https://www.postgresql.org/docs/current/sql-alterdefaultprivileges.html)
  in the PostgreSQL documentation.
