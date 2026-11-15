# Manage secrets

After this, no secret is in any file you commit: the provisioner, Postgres and your
application read them from Docker or Kubernetes secrets.

## When you need this

The getting-started setup has passwords in plain sight in `docker-compose.yaml`:
`POSTGRES_PASSWORD: adminpass`, `SHOP_APP_PASSWORD: shop-app-secret`. They end up in Git, in
`docker inspect`, in CI logs. Before going to production they have to move somewhere safer.

## What you need to know first

The provisioner reads secrets in two places, with slightly different rules. Both accept the
Docker and Kubernetes convention of a `<NAME>_FILE` variable that names a file holding the
value, with trailing line breaks (`\n`, `\r\n`) trimmed.

**`${<NAME>}` references in the config file** are resolved when the config is loaded:

1. The environment variable `<NAME>`, if set, even to an empty string.
2. Otherwise the **contents of the file** named by `<NAME>_FILE`.
3. Otherwise the run **fails** before connecting, naming the variable and where it's used:
   `roles[0].password: environment variable SHOP_APP_PASSWORD is not set (set SHOP_APP_PASSWORD or SHOP_APP_PASSWORD_FILE)`.

`${<NAME>:-<default>}` uses `<default>` when the variable is unset or empty. A password that
resolves to an empty string is rejected, because it would create a role without one. The
full syntax is in
[Environment variable expansion](../configuration.md#environment-variable-expansion).

**The provisioner's own variables** (`POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_HOST`, …)
are read from `<NAME>` or `<NAME>_FILE` the same way, but an empty value counts as unset:

- `POSTGRES_USER` and `POSTGRES_PASSWORD` are required. If one is missing or empty, the run
  stops with the log line `Required environment variable not set`, naming the variable.
- All others fall back to a default, e.g. `POSTGRES_HOST` to `localhost` and `POSTGRES_PORT`
  to `5432`. The list is in [Deployment → Environment variables](../deployment.md#environment-variables).

In both places, setting `<NAME>` and `<NAME>_FILE` together is an error, so it's always clear
which one is used. The rest of this guide is wiring.

## Steps

### 1. Put each secret in its own file

Generate the passwords into files outside Git. `openssl rand -hex` produces only `0-9a-f`, so
the passwords also work unescaped inside a `postgres://` URL, unlike base64 with its `/` and
`+`:

```console
$ mkdir -p secrets && echo 'secrets/' >> .gitignore
$ openssl rand -hex 24 > secrets/pg_admin_password
$ openssl rand -hex 24 > secrets/shop_app_password
$ openssl rand -hex 24 > secrets/shop_reporting_password
```

### 2. Hand the files to the containers with Docker Compose

Declare the secrets and point `*_FILE` variables at them. Postgres' official image supports
`_FILE` variables too, so both sides read the admin password from the same file:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: admin
      POSTGRES_PASSWORD_FILE: /run/secrets/pg_admin_password
    secrets: [pg_admin_password]

  postgres-provisioner:
    image: ghcr.io/datarocks-ag/postgres-provisioner:2.1
    environment:
      POSTGRES_HOST: postgres
      POSTGRES_USER: admin
      POSTGRES_PASSWORD_FILE: /run/secrets/pg_admin_password
      SHOP_APP_PASSWORD_FILE: /run/secrets/shop_app_password
      SHOP_REPORTING_PASSWORD_FILE: /run/secrets/shop_reporting_password
      PGHELPER_CONFIG_PATH: /config.yaml
    secrets: [pg_admin_password, shop_app_password, shop_reporting_password]
    volumes:
      - ./config.yaml:/config.yaml:ro

secrets:
  pg_admin_password:
    file: ./secrets/pg_admin_password
  shop_app_password:
    file: ./secrets/shop_app_password
  shop_reporting_password:
    file: ./secrets/shop_reporting_password
```

The config file doesn't change. It still says `password: ${SHOP_APP_PASSWORD}`, and the
provisioner finds `SHOP_APP_PASSWORD_FILE` on its own.

### 3. Or mount them on Kubernetes

On Kubernetes the idea is the same: mount the Secret as files and point `*_FILE` variables at
them.

```yaml
      containers:
        - name: postgres-provisioner
          image: ghcr.io/datarocks-ag/postgres-provisioner:2.1
          env:
            - name: POSTGRES_HOST
              value: postgres
            - name: POSTGRES_USER_FILE
              value: /secrets/admin/username
            - name: POSTGRES_PASSWORD_FILE
              value: /secrets/admin/password
            - name: SHOP_APP_PASSWORD_FILE
              value: /secrets/shop/password
          volumeMounts:
            - { name: admin-credentials, mountPath: /secrets/admin, readOnly: true }
            - { name: shop-credentials, mountPath: /secrets/shop, readOnly: true }
      volumes:
        - name: admin-credentials
          secret: { secretName: postgres-admin }
        - name: shop-credentials
          secret: { secretName: shop-db }
```

Passing a secret through `env.valueFrom.secretKeyRef` works as well. Files are preferable
because they don't show up in the process environment. The complete Job manifest is in
[Deployment → Kubernetes](../deployment.md#kubernetes).

### 4. Let the application read the same secret

The provisioner sets `shop_app`'s password from `secrets/shop_app_password`, so the
application must log in with the contents of that same file. A password hard-coded in its
`DATABASE_URL`, as in [Getting started](../getting-started.md#step-6-start-the-application-after-the-provisioner),
stops working after the first run with the new secret. Mount the secret into the application
too, and have it read the password from the file. How it does that depends on the
application; here it reads a `DATABASE_PASSWORD_FILE` variable:

```yaml
  shop-api:
    image: your-registry/shop-api
    depends_on:
      postgres-provisioner:
        condition: service_completed_successfully
    environment:
      DATABASE_URL: postgres://shop_app@postgres:5432/shop
      DATABASE_PASSWORD_FILE: /run/secrets/shop_app_password   # read by the application
    secrets: [shop_app_password]
```

### 5. Rotate a password

With the default `strategy: update`, the provisioner sets each role's password on every run.
Rotating is therefore: change the secret, run the provisioner, restart the application so it
reads the new secret.

```console
$ openssl rand -hex 24 > secrets/shop_app_password
$ docker compose run --rm postgres-provisioner
$ docker compose up -d --force-recreate shop-api
```

Existing sessions stay open after `ALTER ROLE <role> WITH PASSWORD '<new-password>'`; only
new logins need the new password. Between the provisioner run and the application restart,
new connections with the old password fail, so do the two back to back (or use your
platform's rolling restart).

If another system owns a role's password after creation (e.g. HashiCorp Vault's database
secrets engine), give that role `strategy: create`. Then the provisioner sets the initial
password once and never touches it again.

## Check that it works

A dry-run resolves every secret and shows the password statements, redacted:

```console
$ docker compose run --rm postgres-provisioner --dry-run | grep -o "PASSWORD '[^']*'"
PASSWORD '***REDACTED***'
PASSWORD '***REDACTED***'
```

One line per role with a password. A missing secret makes the run fail here instead, with the
error from [What you need to know first](#what-you-need-to-know-first).

After a real run, log in as the role with the password from the file:

```console
$ docker compose exec -e PGPASSWORD="$(cat secrets/shop_app_password)" postgres \
    psql -h localhost -U shop_app -d shop -c 'SELECT current_user'
 current_user
--------------
 shop_app
(1 row)
```

`password authentication failed for user "shop_app"` means the provisioner hasn't run since
the file changed, or it read a different file.

## Pitfalls

- **`<NAME>` and `<NAME>_FILE` both set.** The run fails with
  `both <NAME> and <NAME>_FILE are set`. This happens when a default environment, e.g. a
  leftover `POSTGRES_PASSWORD` from the getting-started Compose file, stays next to the new
  `_FILE` variable. Remove one of them.
- **Line breaks in secret files.** Trailing `\n` and `\r\n` are trimmed, so a file written on
  Windows or with `echo` works. Other whitespace, such as a trailing space, is part of the
  password.
- **URL-unsafe characters.** A password with `/`, `+`, `@`, `:` or `%` breaks an unescaped
  `postgres://` URL in the application. Generate hex passwords, or URL-encode the password
  where the application builds the URL.
- **A literal `${` in the config file:** write `$${`. `pa$${ss` becomes `pa${ss`. This applies
  only to text written in the config file. Values read from environment variables or secret
  files are used as they are and never expanded again, so a secret containing `${` needs no
  escaping.
- **Files over 64 KiB are rejected.** That is plenty for passwords and certificates; the
  limit catches a `_FILE` variable pointing at the wrong file.
- **Nothing leaks:** error messages name variables and config paths, never values, and
  `PASSWORD '…'` is redacted in every log line, including dry-run output.

## Further reading

- [Environment variable expansion](../configuration.md#environment-variable-expansion) in the
  configuration reference.
- [Environment variables](../deployment.md#environment-variables) of the provisioner itself.
- [FAQ: passwords](../faq.md#passwords-and-secrets).
- [Connect to a managed or TLS-only server](connect-over-tls.md) for certificates and
  `sslmode`.
