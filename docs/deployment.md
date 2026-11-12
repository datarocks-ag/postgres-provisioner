# Deployment

The provisioner is a short-lived process. It connects, makes the server match the config, and
exits: `0` on success, `1` on any error. Run it wherever your application is deployed, as the
step that has to succeed before the application starts.

## Image tags

Images are published to `ghcr.io/datarocks-ag/postgres-provisioner` for `linux/amd64` and
`linux/arm64`:

| Tag | Points to | Use it for |
|---|---|---|
| `2.0.0` | exactly that release | production: reproducible |
| `2.0` | latest patch release of 2.0 | production: picks up fixes |
| `latest` | latest release | trying things out |
| `develop` | the `develop` branch | testing unreleased changes |
| `sha-<commit>` | a single commit | debugging |

The documentation on the `develop` branch may describe features that aren't released yet;
the [changelog](../CHANGELOG.md) lists them under *Unreleased*.

The image is built `FROM scratch`: it contains the static binary and CA certificates, and
nothing else, so there's no shell to `exec` into. Its working directory is `/`, so relative
paths in the config, such as a migrations `directory`, resolve from `/`.

## Docker Compose

The provisioner runs between the database and the application:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: admin
      POSTGRES_PASSWORD: adminpass
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
      - ./migrations:/migrations:ro

  shop-api:
    image: your-registry/shop-api
    depends_on:
      postgres-provisioner:
        condition: service_completed_successfully
```

- `service_healthy` isn't strictly needed (the provisioner [retries](#connection-retry) until
  Postgres is up), but it keeps the logs free of retry warnings.
- `service_completed_successfully` keeps the application from starting when provisioning
  failed.
- Don't set `restart: always` on the provisioner. It is supposed to exit.

Use [Docker secrets](howto/secrets.md#docker-compose-secrets) instead of plain-text passwords
outside of local development.

## Kubernetes

Ship the config (and migrations) as a ConfigMap and the credentials as a Secret:

```console
$ kubectl create configmap shop-db-config --from-file=config.yaml
$ kubectl create configmap shop-db-migrations --from-file=migrations/shop/
$ kubectl create secret generic postgres-admin --from-literal=username=admin --from-literal=password=...
$ kubectl create secret generic shop-db --from-literal=password=...
```

### As a Job

A Job fits deploy pipelines and Helm hooks (`helm.sh/hook: pre-install,pre-upgrade`). Each
deploy runs it once, and the application rollout waits for it:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: shop-db-provisioner
spec:
  backoffLimit: 2
  ttlSecondsAfterFinished: 3600
  template:
    spec:
      restartPolicy: Never
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
      containers:
        - name: postgres-provisioner
          image: ghcr.io/datarocks-ag/postgres-provisioner:2.0
          env:
            - { name: POSTGRES_HOST, value: postgres.database.svc }
            - { name: POSTGRES_SSLMODE, value: verify-full }
            - { name: POSTGRES_USER_FILE, value: /secrets/admin/username }
            - { name: POSTGRES_PASSWORD_FILE, value: /secrets/admin/password }
            - { name: SHOP_APP_PASSWORD_FILE, value: /secrets/shop/password }
            - { name: PGHELPER_CONFIG_PATH, value: /config/config.yaml }
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: [ALL] }
          volumeMounts:
            - { name: config, mountPath: /config, readOnly: true }
            - { name: migrations, mountPath: /migrations/shop, readOnly: true }
            - { name: admin-credentials, mountPath: /secrets/admin, readOnly: true }
            - { name: shop-credentials, mountPath: /secrets/shop, readOnly: true }
      volumes:
        - name: config
          configMap: { name: shop-db-config }
        - name: migrations
          configMap: { name: shop-db-migrations }
        - name: admin-credentials
          secret: { secretName: postgres-admin }
        - name: shop-credentials
          secret: { secretName: shop-db }
```

The config's migrations `directory` is then `/migrations/shop`.

### As an init container

To provision whenever the application's pod starts, add the same container as an
`initContainer` of the application's Deployment. The application container only starts after
it exits with `0`. Keep in mind that every replica runs it on every start, so the runs will
overlap (see the [FAQ](faq.md#can-two-provisioners-run-at-the-same-time)). For more than one
replica, prefer the Job.

## Standalone binary

Every release has binaries for Linux and macOS (`amd64`, `arm64`) on the
[releases page](https://github.com/datarocks-ag/postgres-provisioner/releases):

```console
$ export POSTGRES_HOST=db.internal POSTGRES_USER=admin POSTGRES_PASSWORD_FILE=~/.secrets/pg-admin
$ export PGHELPER_CONFIG_PATH=./config.yaml
$ postgres-provisioner --dry-run
$ postgres-provisioner
```

`go install` isn't supported: the module path (`postgres-provisioner`) is not a URL.

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `POSTGRES_USER` | **required** | Admin user the provisioner logs in as |
| `POSTGRES_PASSWORD` | **required** | Its password |
| `POSTGRES_HOST` | `localhost` | Server host |
| `POSTGRES_PORT` | `5432` | Server port |
| `POSTGRES_DB` | `postgres` | Database for the admin connection |
| `POSTGRES_SSLMODE` | `disable` | `disable`, `require`, `verify-ca` or `verify-full` ([details](https://www.postgresql.org/docs/current/libpq-ssl.html)) |
| `PGHELPER_CONFIG_PATH` | `./config.yaml` | Path to the YAML config |
| `MIGRATIONS_ENABLED` | `true` | `false` skips all migrations |
| `DRY_RUN` | `false` | `true` logs mutations instead of executing them |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

Each of these can be read from a file instead: set `<NAME>_FILE` to its path (e.g.
`POSTGRES_PASSWORD_FILE`). Setting both `<NAME>` and `<NAME>_FILE` is an error. Variables
referenced in the config (`${SHOP_APP_PASSWORD}`) work the same way.

Use `verify-full` in production. `disable` is the default only so a local Compose setup works
without certificates.

## CLI flags

| Flag | Default | Description |
|---|---|---|
| `--dry-run` | `DRY_RUN` | Log every mutation instead of executing it ([details](concepts.md#dry-run)) |
| `--migrations` | `MIGRATIONS_ENABLED` | `--migrations=false` skips migrations |
| `--version` | | Print the version and exit |

Flags take precedence over their environment variables.

## Connection retry

At startup, the provisioner retries the connection with exponential backoff (1s, 2s, 4s, …
capped at 30s, up to 15 retries, at most 5 minutes in total). It can therefore start
alongside the database without wait scripts. Each retry is logged as a warning, without
credentials. Connections to the individual databases later in the run are retried the same
way.

## Privileges the admin user needs

The simplest setup is a superuser, which `POSTGRES_USER` of the official image is. Managed
services and shared servers often don't give you one. A non-superuser works too, with these
privileges for the features you use:

| Feature | Needs |
|---|---|
| Creating and altering roles | `CREATEROLE`. Granting `SUPERUSER`, `REPLICATION` or `BYPASSRLS` requires holding that attribute. On PostgreSQL 16+, roles must have been created by the admin, or the admin needs `ADMIN OPTION` on them. |
| `member_of` | `ADMIN OPTION` on each group role, e.g. `GRANT pg_monitor TO admin WITH ADMIN OPTION` (PostgreSQL 16+) |
| Creating databases | `CREATEDB`. Setting `owner` requires being able to `SET ROLE` to the owner. |
| Schemas, grants, revokes, migrations | Ownership of (or membership in the owner of) the objects involved. Revokes only remove grants made by the admin or the object owner. |
| Extensions | Whatever the extension requires: many need a superuser, some are "trusted" and only need `CREATE` on the database. |
| `shared_preload_libraries` check | `pg_read_all_settings`, otherwise the check is skipped |
| Publications | `CREATE` on the database and ownership of the tables. `all_tables: true` requires a superuser. |

When something is missing, the run fails with PostgreSQL's error (e.g.
`permission denied to create role`) and the resource it was working on.

## Next steps

- [Manage secrets](howto/secrets.md) before going to production.
- [FAQ](faq.md) for operations questions.
