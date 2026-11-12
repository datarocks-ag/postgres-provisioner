# Manage secrets

**The situation:** the getting-started setup has passwords in plain sight in
`docker-compose.yaml`: `POSTGRES_PASSWORD: adminpass`, `SHOP_APP_PASSWORD: shop-app-secret`.
They end up in Git, in `docker inspect`, in CI logs. Before going to production they have to
move somewhere safer.

**The goal:** no secret in any file you commit, with the provisioner reading them from Docker
or Kubernetes secrets.

## How the provisioner finds a secret

Every `${VAR}` in the config, and each of the provisioner's own variables
(`POSTGRES_PASSWORD`, `POSTGRES_USER`, …), is resolved in this order:

1. The environment variable `VAR`, if set.
2. Otherwise the **contents of the file** named by `VAR_FILE`, with trailing newlines trimmed.
3. Otherwise the run **fails** before connecting, naming the variable and where it's used:
   `roles[0].password: environment variable SHOP_APP_PASSWORD is not set (set SHOP_APP_PASSWORD or SHOP_APP_PASSWORD_FILE)`.

Setting both `VAR` and `VAR_FILE` is an error, so it's always clear which one is used. That's
the whole mechanism. The rest of this guide is wiring.

## Docker Compose secrets

Put each secret in its own file, outside Git:

```console
$ mkdir -p secrets && echo 'secrets/' >> .gitignore
$ openssl rand -base64 24 > secrets/pg_admin_password
$ openssl rand -base64 24 > secrets/shop_app_password
```

Declare them and hand them to the containers. Postgres' official image supports `_FILE`
variables too, so both sides read the admin password from the same file:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: admin
      POSTGRES_PASSWORD_FILE: /run/secrets/pg_admin_password
    secrets: [pg_admin_password]

  postgres-provisioner:
    image: ghcr.io/datarocks-ag/postgres-provisioner:2.0
    environment:
      POSTGRES_HOST: postgres
      POSTGRES_USER: admin
      POSTGRES_PASSWORD_FILE: /run/secrets/pg_admin_password
      SHOP_APP_PASSWORD_FILE: /run/secrets/shop_app_password
      PGHELPER_CONFIG_PATH: /config.yaml
    secrets: [pg_admin_password, shop_app_password]
    volumes:
      - ./config.yaml:/config.yaml:ro

secrets:
  pg_admin_password:
    file: ./secrets/pg_admin_password
  shop_app_password:
    file: ./secrets/shop_app_password
```

The config file doesn't change. It still says `password: ${SHOP_APP_PASSWORD}`, and the
provisioner finds `SHOP_APP_PASSWORD_FILE` on its own.

## Kubernetes secrets

Same idea: mount the Secret as files and point `*_FILE` variables at them.

```yaml
      containers:
        - name: postgres-provisioner
          image: ghcr.io/datarocks-ag/postgres-provisioner:2.0
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

## Rotating a password

With the default `strategy: update`, the provisioner sets each role's password on every run.
Rotating is therefore: change the secret, run the provisioner, restart the application with
the new secret.

```console
$ openssl rand -base64 24 > secrets/shop_app_password
$ docker compose run --rm postgres-provisioner
$ docker compose up -d --force-recreate shop-api
```

Existing sessions stay open after `ALTER ROLE ... PASSWORD`; only new logins need the new
password. Between the provisioner run and the application restart, new connections with the
old password fail, so do the two back to back (or use your platform's rolling restart).

If another system owns a role's password after creation (e.g. HashiCorp Vault's database
secrets engine), give that role `strategy: create`. Then the provisioner sets the initial
password once and never touches it again.

## Good to know

- **Optional values:** `${VAR:-default}` falls back to `default` when `VAR` is unset or
  empty, and `${VAR:-}` makes a value optional. Passwords are the exception: a password
  that resolves to an empty string is rejected, because it would create a role without one.
- **A literal `${` in a value:** write `$${`. `pa$${ss` becomes `pa${ss`.
- **Nothing leaks:** error messages name variables and config paths, never values, and
  `PASSWORD '…'` is redacted in every log line, including dry-run output.
- **Files up to 64 KiB** are accepted, which is plenty for passwords and certificates.

## See also

- [Environment variable expansion](../configuration.md#environment-variable-expansion) in the
  configuration reference.
- [FAQ: passwords](../faq.md#passwords-and-secrets).
