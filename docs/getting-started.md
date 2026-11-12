# Getting started

This guide follows one small project from an empty Postgres to a database an application can
use in production. Along the way you'll meet everything you need day to day: roles, databases,
schemas, migrations, grants, a read-only user, and dry-run.

**The project:** you're building `shop`, a small web shop. The API service needs its own
login and database, the schema is managed with SQL migrations, and the finance team wants
read-only access for their reports. Everything runs in Docker Compose.

You'll need Docker with Compose v2. Each step builds on the previous one, so keep the files
you create.

> **Versions:** this guide uses image tag `2.0`. All tags are listed under
> [Deployment → Image tags](deployment.md#image-tags).

## Step 1: A role and a database

Start with the smallest useful config: a login role for the API and a database it owns.

```yaml
# config.yaml
roles:
  - name: shop_app
    password: ${SHOP_APP_PASSWORD}
    options:
      login: true

databases:
  - name: shop
    owner: shop_app
```

The password isn't in the file. `${SHOP_APP_PASSWORD}` is read from the environment when the
config is loaded, and if the variable is missing the run fails before touching the database.
A config file with no secrets in it can be committed, reviewed and shared.

Now the Compose file. Postgres runs with a superuser called `admin`. The provisioner logs in
as that user, waits until the server is healthy, applies the config, and exits.

```yaml
# docker-compose.yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: admin
      POSTGRES_PASSWORD: adminpass
    ports:
      - "5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U admin"]
      interval: 2s
      timeout: 5s
      retries: 10

  postgres-provisioner:
    image: ghcr.io/datarocks-ag/postgres-provisioner:2.0
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      POSTGRES_HOST: postgres
      POSTGRES_USER: admin
      POSTGRES_PASSWORD: adminpass
      PGHELPER_CONFIG_PATH: /config.yaml
      SHOP_APP_PASSWORD: shop-app-secret
    volumes:
      - ./config.yaml:/config.yaml:ro
```

Run it:

```console
$ docker compose run --rm postgres-provisioner
{"level":"INFO","msg":"Starting postgres-provisioner","version":"v2.0.0"}
{"level":"INFO","msg":"Configuration loaded","roles":1,"databases":1}
{"level":"INFO","msg":"Connected to PostgreSQL","host":"postgres","port":"5432","database":"postgres"}
{"level":"INFO","msg":"Starting provisioning"}
{"level":"INFO","msg":"Creating role","role":"shop_app"}
{"level":"INFO","msg":"Creating database","database":"shop"}
{"level":"INFO","msg":"Provisioning complete"}
{"level":"INFO","msg":"postgres-provisioner finished successfully"}
```

(Log lines are JSON. The `time` field is left out here for readability.)

Now run the same command again:

```console
$ docker compose run --rm postgres-provisioner
...
{"level":"INFO","msg":"Role already exists, updating","role":"shop_app"}
{"level":"INFO","msg":"Database already exists","database":"shop"}
{"level":"INFO","msg":"postgres-provisioner finished successfully"}
```

Nothing was recreated. The role was *updated* (its password and options set to what the
config says) and the database was left alone. This is the property everything else builds
on: **you can run the provisioner on every deploy**, and it converges the server to the
config instead of failing on things that already exist.

## Step 2: A schema and the first migration

The API keeps its tables in a schema called `app`, and the schema evolves through numbered
SQL files. Create the first migration:

```sql
-- migrations/shop/V0001__create_products.sql
CREATE TABLE app.products (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        text    NOT NULL,
    price_cents integer NOT NULL
);
```

`V0001__` makes it a *versioned* migration. It runs once, and its checksum is recorded so a
later edit to an applied file is caught instead of silently ignored.
[Migrations](configuration.md#migrations) covers the naming rules and *repeatable*
`R` migrations.

Add the schema and the migration directory to the database:

```yaml
databases:
  - name: shop
    owner: shop_app
    schemas:
      - name: app            # owner defaults to the database owner, shop_app
    migrations:
      directory: /migrations/shop
```

…and mount the directory into the container, next to the config:

```yaml
    volumes:
      - ./config.yaml:/config.yaml:ro
      - ./migrations:/migrations:ro
```

Run the provisioner again. It creates the schema, then the migration tracking table
`_schema_migrations`, and applies the file:

```console
$ docker compose run --rm postgres-provisioner
...
{"level":"INFO","msg":"Ensuring schema","schema":"app","owner":"shop_app","strategy":"update"}
{"level":"INFO","msg":"Running migrations","database":"shop","directory":"/migrations/shop"}
{"level":"INFO","msg":"Applying migration","file":"V0001__create_products.sql","type":"versioned","database":"shop"}
{"level":"INFO","msg":"Migrations complete","database":"shop","total_files":1}
```

Now try to use the table as the application would:

```console
$ docker compose exec -e PGPASSWORD=shop-app-secret postgres \
    psql -h localhost -U shop_app -d shop \
    -c "INSERT INTO app.products (name, price_cents) VALUES ('Mug', 1200)"
ERROR:  permission denied for table products
```

The application owns the database and the schema, so why is the table off limits? Look at
who owns it:

```console
$ docker compose exec postgres psql -U admin -d shop -c '\dt app.*'
 Schema |   Name   | Type  | Owner
--------+----------+-------+-------
 app    | products | table | admin
```

**Migrations run as the admin user, so the admin owns every table they create.** Owning the
schema doesn't give `shop_app` any rights on tables inside it. This is the most common
surprise for new users, and the fix is the next step.

## Step 3: Grants

Grant the application what it needs on all tables in the schema:

```yaml
databases:
  - name: shop
    owner: shop_app
    schemas:
      - name: app
    grants:
      - role: shop_app
        privileges: [SELECT, INSERT, UPDATE, DELETE]
        on_tables_in_schema: app
    migrations:
      directory: /migrations/shop
```

Run the provisioner, then repeat the insert:

```console
$ docker compose run --rm postgres-provisioner
...
{"level":"INFO","msg":"Granting on tables in schema","schema":"app","privileges":"SELECT, INSERT, UPDATE, DELETE","schema_owner":"shop_app"}
$ docker compose exec -e PGPASSWORD=shop-app-secret postgres \
    psql -h localhost -U shop_app -d shop \
    -c "INSERT INTO app.products (name, price_cents) VALUES ('Mug', 1200)"
INSERT 0 1
```

`on_tables_in_schema` does two things. It grants on every table that exists now, and it sets
**default privileges** so tables created later, by your next migration, get the same grant
automatically. Grants are applied before migrations in each run, so a table added by
`V0002` is covered the moment it is created.

> The identity column needs no extra grant. Tables with `serial` columns do: their sequence
> needs `USAGE` through an `on_sequences_in_schema` grant. See
> [Grant targets](configuration.md#grants).

## Step 4: A read-only user for finance

Finance wants to query the shop data with their BI tool, without being able to change
anything. Add a second role:

```yaml
roles:
  - name: shop_app
    password: ${SHOP_APP_PASSWORD}
    options:
      login: true

  - name: shop_reporting
    password: ${SHOP_REPORTING_PASSWORD}
    options:
      login: true
      connection_limit: 3        # a dashboard shouldn't exhaust the connection pool
    settings:
      statement_timeout: "60s"   # nor run a query for an hour
```

…and give it read access. Reading a table takes two grants, because PostgreSQL checks the
schema before it looks at the table:

```yaml
    grants:
      - role: shop_app
        privileges: [SELECT, INSERT, UPDATE, DELETE]
        on_tables_in_schema: app
      - role: shop_reporting
        privileges: [USAGE]
        on_schema: app
      - role: shop_reporting
        privileges: [SELECT]
        on_tables_in_schema: app
```

Add `SHOP_REPORTING_PASSWORD: reporting-secret` to the provisioner's `environment`.

## Step 5: Preview before you apply

This time, look before you leap. `--dry-run` connects and reads the current state, but only
**logs** each statement it would execute:

```console
$ docker compose run --rm postgres-provisioner --dry-run
{"level":"INFO","msg":"Starting provisioning in DRY RUN mode — no changes will be applied"}
{"level":"INFO","msg":"[DRY RUN] would execute","sql":"ALTER ROLE \"shop_app\" WITH PASSWORD '***REDACTED***' LOGIN"}
{"level":"INFO","msg":"[DRY RUN] would execute","sql":"CREATE ROLE \"shop_reporting\" WITH PASSWORD '***REDACTED***' LOGIN CONNECTION LIMIT 3"}
{"level":"INFO","msg":"[DRY RUN] would execute","sql":"ALTER ROLE \"shop_reporting\" SET statement_timeout = '60s'"}
...
{"level":"INFO","msg":"[DRY RUN] would execute","sql":"GRANT USAGE ON SCHEMA \"app\" TO \"shop_reporting\""}
...
{"level":"INFO","msg":"[DRY RUN] migration already applied — would skip","file":"V0001__create_products.sql","database":"shop"}
```

Two things to notice:

- Passwords are redacted. They are redacted in every log line, not only in dry-run.
- The preview includes statements that change nothing, like `ALTER ROLE "shop_app"`.
  Statements that are idempotent on the server (role attributes, ownership, `GRANT`) are
  issued on every run. Settings, memberships, revokes, extensions, migrations and publications
  are compared with the current state first. So a dry-run shows what *will be executed*, which
  is a superset of what will *change*. [How it works](concepts.md#dry-run) explains which is
  which.

Happy with it? Apply it for real and check as the new user:

```console
$ docker compose run --rm postgres-provisioner
$ docker compose exec -e PGPASSWORD=reporting-secret postgres \
    psql -h localhost -U shop_reporting -d shop \
    -c "SELECT name FROM app.products" -c "DELETE FROM app.products"
 name
------
 Mug
(1 row)

ERROR:  permission denied for table products
```

Reading works, writing doesn't. That's exactly what was asked for.

## Step 6: Start the application after the provisioner

The last piece is ordering. The application must not start before its role and tables
exist. Compose expresses that with `service_completed_successfully`:

```yaml
  shop-api:
    image: your-registry/shop-api
    depends_on:
      postgres-provisioner:
        condition: service_completed_successfully
    environment:
      DATABASE_URL: postgres://shop_app:shop-app-secret@postgres:5432/shop
```

`docker compose up` now starts Postgres, waits for it to be healthy, runs the provisioner,
and only starts `shop-api` if the provisioner exited with code `0`. If the config is broken
or a migration fails, the API never starts against a half-provisioned database, and the
provisioner's logs tell you why.

## Where you are now

Your `shop` stack has:

- a least-privilege application role that owns its database and schema,
- versioned migrations that run once and are checksummed,
- grants that also cover tables future migrations create,
- a read-only, rate-limited reporting user,
- a provisioning step you can re-run on every deploy and preview with `--dry-run`.

The final files:

<details>
<summary><code>config.yaml</code></summary>

```yaml
roles:
  - name: shop_app
    password: ${SHOP_APP_PASSWORD}
    options:
      login: true

  - name: shop_reporting
    password: ${SHOP_REPORTING_PASSWORD}
    options:
      login: true
      connection_limit: 3
    settings:
      statement_timeout: "60s"

databases:
  - name: shop
    owner: shop_app
    schemas:
      - name: app
    grants:
      - role: shop_app
        privileges: [SELECT, INSERT, UPDATE, DELETE]
        on_tables_in_schema: app
      - role: shop_reporting
        privileges: [USAGE]
        on_schema: app
      - role: shop_reporting
        privileges: [SELECT]
        on_tables_in_schema: app
    migrations:
      directory: /migrations/shop
```

</details>

## Next steps

- **Understand what a run does.** [How it works](concepts.md) explains the order of
  steps, what the provisioner never touches, and when to use `strategy: create`.
- **Keep secrets out of Compose files.** [Manage secrets](howto/secrets.md) moves the
  passwords into Docker or Kubernetes secrets.
- **Add monitoring.** [Monitor Postgres with postgres_exporter](howto/monitoring.md) adds a
  `pg_monitor` account to this setup.
- **Deploy to Kubernetes.** [Deployment](deployment.md#kubernetes) shows the Job and init
  container setups.
