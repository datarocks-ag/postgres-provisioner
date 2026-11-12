# postgres-provisioner

[![CI](https://github.com/datarocks-ag/postgres-provisioner/actions/workflows/ci.yaml/badge.svg)](https://github.com/datarocks-ag/postgres-provisioner/actions/workflows/ci.yaml)
![coverage](https://raw.githubusercontent.com/datarocks-ag/postgres-provisioner/badges/.badges/develop/coverage.svg)

Declarative, idempotent provisioning for PostgreSQL. Describe the roles, databases, schemas,
privileges, extensions, migrations and publications your services need in one YAML file;
the provisioner makes the server match it, every time it runs, and then exits.

It is built to run **before your application starts**: as a Docker Compose service your app
`depends_on`, or as a Kubernetes Job or init container.

## Why

Every service needs the same handful of things from Postgres: a login role, a database it
owns, a schema, the right grants, maybe `pgcrypto`, and a read-only account for reporting or
monitoring. Usually that ends up in an `init.sql` that only runs on an empty data directory,
in a pile of manual `psql` sessions, or in the application itself running with far more
privileges than it needs.

postgres-provisioner replaces that with a file you can review in a pull request:

- **Idempotent** — run it on every deploy. Existing resources are reconciled, not recreated.
- **Additive and safe** — it never drops roles, databases, schemas, tables or grants that
  aren't in the config. Removing privileges only happens through explicit `revokes`.
- **Secret-friendly** — `${VAR}` references, `*_FILE` secrets (Docker/Kubernetes), and
  passwords are redacted from every log line.
- **Previewable** — `--dry-run` logs every statement it would execute.
- **Small** — a single static binary in a `scratch` image.

## Quick start

Two files next to each other. A config:

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

And a Compose file that runs the provisioner once Postgres is healthy:

```yaml
# docker-compose.yaml
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
```

```console
$ docker compose up postgres-provisioner
...
{"level":"INFO","msg":"Creating role","role":"shop_app"}
{"level":"INFO","msg":"Creating database","database":"shop"}
{"level":"INFO","msg":"postgres-provisioner finished successfully"}
```

Run it again and nothing is recreated. That's the whole idea. The
[getting started guide](docs/getting-started.md) takes this example further: schemas,
migrations, grants, a read-only user, and wiring your application to it.

## Documentation

Read in this order if you're new; jump straight to a how-to if you know what you need.

| | |
|---|---|
| **[Getting started](docs/getting-started.md)** | A guided tour: from an empty Postgres to an application database with migrations and a read-only user. |
| **[How it works](docs/concepts.md)** | The model behind a run: what gets reconciled, what is never touched, the order of steps, `strategy`, dry-run. |
| **How-to guides** | Task-focused stories: |
| &nbsp;&nbsp;[Monitor Postgres with postgres_exporter](docs/howto/monitoring.md) | A least-privilege `pg_monitor` account instead of the superuser. |
| &nbsp;&nbsp;[Read-only and reporting users](docs/howto/read-only-users.md) | Schema-wide, table- and column-level read access. |
| &nbsp;&nbsp;[Harden a database](docs/howto/harden-database.md) | Revoke PostgreSQL's `PUBLIC` defaults and grant access explicitly. |
| &nbsp;&nbsp;[Manage secrets](docs/howto/secrets.md) | Docker secrets, Kubernetes secrets, password rotation. |
| &nbsp;&nbsp;[When the application runs its own migrations](docs/howto/app-migrations.md) | Grants for tables the provisioner doesn't create. |
| &nbsp;&nbsp;[Change data capture with publications](docs/howto/cdc-publications.md) | Logical replication for Debezium, Redpanda Connect and friends. |
| **[Deployment](docs/deployment.md)** | Docker Compose, Kubernetes, binaries, environment variables, CLI flags, required privileges. |
| **[Configuration reference](docs/configuration.md)** | Every field of the YAML file. |
| **[FAQ](docs/faq.md)** | Questions people ask after their first week. |
| **[Upgrading to 2.0](docs/upgrading-to-2.0.md)** | Breaking changes and how to migrate from 1.x. |
| **[Changelog](CHANGELOG.md)** | What changed in each release. |

A complete, commented config lives in [config.example.yaml](config.example.yaml), and
[docker-compose.yaml](docker-compose.yaml) runs it against a local Postgres.

## Development

```bash
make build            # Build binary
make test             # Run unit tests
make test-integration # Run integration tests (requires Docker)
make lint             # Run golangci-lint
make vet              # Run go vet
make fmt              # Format the code
make docker           # Build Docker image
```

The integration tests start PostgreSQL 16 with testcontainers.

**Branches and releases:** work happens on `feature/`, `bugfix/` and `hotfix/` branches merged
into `develop` through pull requests. A release gets a `release/x.y.z` branch with a
"Cut CHANGELOG x.y.z" commit, a `vx.y.z` tag (which builds binaries and images), and is merged
back into `develop`.
