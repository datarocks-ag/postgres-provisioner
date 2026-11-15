# Connect to a managed or TLS-only server

After this you can run the provisioner against a server that requires TLS (Transport Layer
Security), with the server certificate verified against a certificate authority (CA) you trust.

## When you need this

Your database is a managed service such as Amazon RDS, Google Cloud SQL or Azure Database for
PostgreSQL, or a self-hosted server that only accepts encrypted connections. The provisioner
connects without TLS by default (`POSTGRES_SSLMODE=disable`), so against such a server it
either fails to connect or, with a weak mode, connects without checking who it talks to.

## What you need to know first

- **SSL mode.** `POSTGRES_SSLMODE` becomes the `sslmode` of every connection the provisioner
  opens. The PostgreSQL driver the provisioner uses ([lib/pq](https://pkg.go.dev/github.com/lib/pq@v1.12.3))
  accepts `disable`, `allow`, `prefer`, `require`, `verify-ca` and `verify-full`. Only
  `verify-full` checks both that a trusted CA signed the certificate and that the certificate
  names the host you connect to. See [SSL support in libpq](https://www.postgresql.org/docs/current/libpq-ssl.html#LIBPQ-SSL-PROTECTION).
- **Trusted CAs.** The driver reads the CA file from the standard `PGSSLROOTCERT` variable.
  Without it (and without a
  `~/.postgresql/root.crt`), Go's system trust store applies. The image is built `FROM scratch` but copies
  the Alpine CA bundle to `/etc/ssl/certs/ca-certificates.crt` (see `Dockerfile`), so
  certificates from public CAs verify without mounting anything.

## Steps

The examples use `db.example.com` as the server and `/certs/ca.crt` as the CA file; replace
both with your own.

### 1. Get the CA certificate, if the server's CA is not public

Managed services and internal servers usually sign with their own CA. Download the provider's
CA bundle as a PEM file (links under [Further reading](#further-reading)), or ask whoever runs
the server for it. A server certificate from a public CA needs no file: skip to step 3.

### 2. Mount the CA file and point `PGSSLROOTCERT` at it

In Docker Compose, add the file as a read-only volume:

```yaml
  postgres-provisioner:
    image: ghcr.io/datarocks-ag/postgres-provisioner:2.0
    environment:
      POSTGRES_HOST: db.example.com
      POSTGRES_SSLMODE: verify-full
      PGSSLROOTCERT: /certs/ca.crt
      # … POSTGRES_USER, POSTGRES_PASSWORD, PGHELPER_CONFIG_PATH as before
    volumes:
      - ./config.yaml:/config.yaml:ro
      - ./certs/ca.crt:/certs/ca.crt:ro
```

In Kubernetes, ship the CA as a ConfigMap and mount it into the
[Job](../deployment.md#kubernetes):

```console
$ kubectl create configmap postgres-ca --from-file=ca.crt=./certs/ca.crt
```

```yaml
          env:
            - { name: POSTGRES_HOST, value: db.example.com }
            - { name: POSTGRES_SSLMODE, value: verify-full }
            - { name: PGSSLROOTCERT, value: /certs/ca.crt }
            # …
          volumeMounts:
            - { name: postgres-ca, mountPath: /certs, readOnly: true }
      volumes:
        - name: postgres-ca
          configMap: { name: postgres-ca }
```

Setting `PGSSLROOTCERT` replaces the system trust store: only the CAs in that file are trusted.

### 3. Set the SSL mode to `verify-full`

Set `POSTGRES_SSLMODE=verify-full` as shown above, and set `POSTGRES_HOST` to the name that
the server certificate carries, usually the DNS name the provider gives you, not an IP address.
Use `POSTGRES_SSLMODE`, not `PGSSLMODE`: the provisioner always writes `sslmode` into its
connection string, which takes precedence over `PGSSLMODE`.

## Check that it works

Run a dry run, which connects and reads but changes nothing:

```console
$ docker compose run --rm postgres-provisioner --dry-run
```

A `Connected to PostgreSQL` line means the TLS handshake and the certificate check passed:

```json
{"time":"…","level":"INFO","msg":"Connected to PostgreSQL","host":"db.example.com","port":"5432","database":"postgres"}
```

To confirm from your workstation that the session is encrypted, connect with `psql` using
the same settings:

```console
$ psql "host=db.example.com sslmode=verify-full sslrootcert=./certs/ca.crt user=<admin-user> dbname=postgres" \
    -c "SELECT ssl, version FROM pg_stat_ssl WHERE pid = pg_backend_pid()"
 ssl | version
-----+---------
 t   | TLSv1.3
```

The `version` column shows whichever TLS version the server negotiates.

## Pitfalls

- **Errors are retried, not reported at once.** A TLS failure is logged as
  `PostgreSQL not ready, retrying` and the provisioner keeps trying for up to five minutes
  (see [Connection retry](../deployment.md#connection-retry)). Read the `error` field of the
  first warning instead of waiting for the run to end.
- **`require` does not verify the certificate.** It encrypts, but accepts any certificate, so
  it does not protect against a man-in-the-middle. With `PGSSLROOTCERT` set, `require` checks
  the CA but still not the hostname. Use `verify-full`.
- **Unknown CA:** `x509: certificate signed by unknown authority` means the CA file is missing,
  not mounted at the `PGSSLROOTCERT` path, or is the wrong bundle.
- **Hostname mismatch:** `POSTGRES_HOST` must match a name in the certificate. Connecting by
  IP address to a certificate with only DNS names fails with
  `x509: cannot validate certificate for 10.0.0.5 because it doesn't contain any IP SANs`.
  Some providers issue certificates that don't name the address you connect to; check their
  documentation before falling back to `verify-ca`.
- **Server without TLS:** `pq: SSL is not enabled on the server` means `verify-full` (or
  `require`) is set, but the server does not offer TLS.
- **Typo in the mode:** `verify_full` is rejected with
  `pq: wrong value for "sslmode": "verify_full" is not supported; …`.

## Further reading

- [Environment variables](../deployment.md#environment-variables) in the deployment guide.
- [SSL support in libpq](https://www.postgresql.org/docs/current/libpq-ssl.html) for what each
  mode protects against.
- Provider CA bundles and TLS settings:
  [Amazon RDS](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.SSL.html),
  [Google Cloud SQL](https://cloud.google.com/sql/docs/postgres/configure-ssl-instance),
  [Azure Database for PostgreSQL](https://learn.microsoft.com/en-us/azure/postgresql/flexible-server/how-to-connect-tls-ssl).
- [Manage secrets](secrets.md) for the admin credentials that go with the connection.
