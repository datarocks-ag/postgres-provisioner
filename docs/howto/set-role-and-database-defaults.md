# Set role and database defaults

After this you can give a role, a database, or a role within one database its own session
defaults, such as a statement timeout or a `search_path`, from the provisioner config.

## When you need this

A reporting user should never run a query longer than five minutes, the application should
find its tables without schema-qualifying every name, and every session in a database should
use UTC. Setting these with `SET` in each client is easy to forget; setting them server-wide
in `postgresql.conf` affects everyone. Per-role and per-database defaults sit in between.

## What you need to know first

- **Run-time parameters.** Settings like `statement_timeout` that a session can change. A
  default stored for a role or database applies when a new session starts. See
  [Settings](../configuration.md#settings).
- **Three scopes.** `roles[].settings` applies to the role in every database
  (`ALTER ROLE ... SET`), `databases[].settings` to every role in that database
  (`ALTER DATABASE ... SET`), and `databases[].role_settings` to one role in one database
  (`ALTER ROLE ... IN DATABASE ... SET`). When several apply, the most specific wins: role in
  database, then role, then database. See [ALTER ROLE](https://www.postgresql.org/docs/current/sql-alterrole.html).

## Steps

The names `billing`, `billing_app` and `billing_reporting` are examples; replace them with
your own.

### 1. Set role-wide defaults

`billing_app` gets a 30-second timeout and finds tables in `billing` first. Write list
settings such as `search_path` as a YAML list: a single string `"billing, public"` would name
one schema called `billing, public`.

```yaml
roles:
  - name: billing_app
    password: ${BILLING_APP_PASSWORD}
    options:
      login: true
    settings:                          # every session of billing_app, in any database
      statement_timeout: "30s"
      search_path: ["billing", "public"]
  - name: billing_reporting
    password: ${BILLING_REPORTING_PASSWORD}
    options:
      login: true
```

### 2. Set database-wide and per-role-in-database defaults

`role_settings` keys must be roles declared in `roles`.

```yaml
databases:
  - name: billing
    schemas:
      - name: billing
    settings:                          # every session in the billing database
      TimeZone: "UTC"
    role_settings:                     # billing_reporting, only in the billing database
      billing_reporting:
        statement_timeout: "5min"
        default_transaction_read_only: "on"
```

### 3. Run the provisioner

It compares each setting with `pg_db_role_setting` and only changes those that differ. For
the config above, a first run executes:

```sql
ALTER ROLE "billing_app" SET search_path = 'billing', 'public'
ALTER ROLE "billing_app" SET statement_timeout = '30s'
ALTER DATABASE "billing" SET timezone = 'UTC'
ALTER ROLE "billing_reporting" IN DATABASE "billing" SET default_transaction_read_only = 'on'
ALTER ROLE "billing_reporting" IN DATABASE "billing" SET statement_timeout = '5min'
```

Each setting is logged as `Applying setting`, and as `Setting unchanged, skipping` on later runs.

## Check that it works

List the stored defaults with `\drds` as the admin user:

```console
$ psql -h localhost -U admin -d billing -c '\drds'
                        List of settings
       Role        | Database |             Settings
-------------------+----------+----------------------------------
 billing_app       |          | search_path=billing, public     +
                   |          | statement_timeout=30s
 billing_reporting | billing  | default_transaction_read_only=on+
                   |          | statement_timeout=5min
                   | billing  | TimeZone=UTC
```

Then open a new session as the role and read the effective value:

```console
$ psql -h localhost -U billing_reporting -d billing -c 'SHOW statement_timeout'
 statement_timeout
-------------------
 5min
```

## Pitfalls

- **Open sessions keep their old values.** Defaults apply when a session starts. Restart the
  application or recycle its connection pool after a change.
- **Removing a setting keeps it.** The provisioner leaves settings it isn't told about
  untouched. Set the value to `null` to reset it (`ALTER ROLE "billing_app" RESET statement_timeout`),
  or reset it by hand as the admin user. An empty value (`statement_timeout:`) is rejected
  with `value is empty (use null to reset the setting)`.
- **Unknown or server-wide parameters fail at run time.** Names are only checked for their
  form when the config loads. A typo fails the run with
  `pq: unrecognized configuration parameter "statment_timeout"`, and a parameter that can't
  be set per role, such as `shared_buffers`, with
  `cannot be changed without restarting the server`.
- **`strategy: create`:** role `settings` are only applied to newly created roles; database
  `settings` and `role_settings` follow the database's strategy.
- **The provisioner's own sessions ignore timeouts and read-only defaults.** It overrides
  them at connection start, but `search_path` is not overridden. See
  [Sessions the provisioner opens](../concepts.md#sessions-the-provisioner-opens).

## Further reading

- [Settings](../configuration.md#settings) in the configuration reference.
- [Additive by design](../concepts.md#additive-by-design) for why removing a setting doesn't
  reset it.
- [Create read-only users](read-only-users.md), which uses a `statement_timeout` for
  reporting users.
- [Client connection defaults](https://www.postgresql.org/docs/current/runtime-config-client.html)
  in the PostgreSQL documentation.
