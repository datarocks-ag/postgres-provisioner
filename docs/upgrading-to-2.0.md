# Upgrading to 2.0

2.0 makes config loading strict. In 1.x, several mistakes went unnoticed: a missing
environment variable, a privilege that doesn't apply to its target, a misspelled key. Some
of them ended in a wrong password, others failed halfway through a run. In 2.0 they fail
**at load time, before anything is changed**, with the exact config path.

For most setups, upgrading means: run 2.0 with `--dry-run`, fix what it reports, deploy.

## The upgrade in three steps

1. **Pin the new image** (`ghcr.io/datarocks-ag/postgres-provisioner:2.0`) in a staging or
   local environment, with the same config and environment as production.
2. **Run it with `--dry-run`.** Config errors appear before it connects, all of them in one
   message. Fix them using the list below.
3. **Read the dry-run output** for unexpected statements, then deploy.

## Breaking changes

### Unresolved `${VAR}` references fail

**1.x:** an unset variable stayed in the value as literal text. A missing `APP_DB_PASSWORD`
set the role's password to the string `${APP_DB_PASSWORD}`, and nobody noticed until the
application couldn't log in.

**2.0:** loading fails and lists every unresolved reference:

```
roles[0].password: environment variable APP_DB_PASSWORD is not set (set APP_DB_PASSWORD or APP_DB_PASSWORD_FILE)
```

**What to do:** set the missing variables, or point `APP_DB_PASSWORD_FILE` at a secret
file. For values that are optional on purpose, say so: `${VAR:-}` (empty) or
`${VAR:-default}`.

### Passwords that resolve to an empty string are rejected

**1.x:** `password: ${APP_DB_PASSWORD}` with `APP_DB_PASSWORD=""` created the role without a
password.

**2.0:** `roles[0].password: environment variable reference resolved to an empty value`.

**What to do:** provide a value, or remove the `password` field if the role really has no
password.

### `<NAME>` and `<NAME>_FILE` together are an error

**1.x:** with both `POSTGRES_PASSWORD` and `POSTGRES_PASSWORD_FILE` set, the `_FILE` variable
was ignored.

**2.0:** the provisioner refuses to start. This applies to its own variables
(`POSTGRES_*`, `PGHELPER_CONFIG_PATH`, `LOG_LEVEL`, …) and to variables referenced in the
config.

**What to do:** remove one of the two. Leftovers from a migration to Docker secrets are the
usual cause.

### `$${` is an escape

**1.x:** `$${VAR}` produced `$` followed by the value of `VAR`.

**2.0:** `$${` produces a literal `${`, so `$${VAR}` stays `${VAR}`.

**What to do:** search your config for `$${`. If you meant "a dollar sign, then the
variable's value", move the `$` into the variable's value.

### Stricter syntax and validation

Each of these used to fail at runtime (or silently do the wrong thing) and now fails at load
time:

| Config | 2.0 |
|---|---|
| `${foo bar}`, `${}` | invalid reference |
| `${A:-${B}}` (nested default) | rejected; it never worked (it produced a literal `${B}` or a stray `}`) |
| A privilege that doesn't fit its target, e.g. `SELECT` with `on_database` | rejected with the allowed privileges |
| `ALL` together with other privileges | rejected (PostgreSQL rejects it too) |
| The same extension twice in a database | rejected |
| Unknown keys in an extension mapping, e.g. `shema:` | rejected |
| A role named `public` in any case | rejected (grants and revokes treat it as `PUBLIC`) |

The privilege rows could never apply: PostgreSQL rejected them mid-run. The others did run
in 1.x, but with a wrong value (invalid and nested references were left as literal text) or a
redundant statement (duplicate extensions). If 2.0 reports one of them, your 1.x setup
probably wasn't doing what you thought.

## Behavior changes

These don't make a config fail, but they can change the result:

- **Schemas are created before extensions.** An extension without a `schema` lands in the
  first existing schema of the admin's `search_path`. With the default `"$user", public`, a
  provisioned schema with the same name as the admin user (e.g. `admin`) now exists at that
  point and wins over `public`. Already installed extensions aren't moved. Set
  `schema: public` on the extension to keep the old placement for new databases.
- **The provisioner's sessions ignore timeouts and read-only defaults.** Every connection
  sets `statement_timeout=0`, `lock_timeout=0`, `idle_in_transaction_session_timeout=0` and
  `default_transaction_read_only=off`. If you relied on a role or database `statement_timeout`
  to bound migrations, it no longer applies to the provisioner.
- **`valid_until` supports `${VAR}`** like every other string.

## New in 2.0

Once you're upgraded, these are available:

- [`*_FILE` secrets and defaults](howto/secrets.md) for every variable
- [Revokes](howto/harden-database.md) to harden databases
- [Role and database settings](configuration.md#settings)
- [Extension options](configuration.md#extensions): schema, version, cascade, and the
  `shared_preload_libraries` check
- [Sequence and function grants](configuration.md#grants)
- [Table- and column-level grants](howto/read-only-users.md#level-2-specific-tables-and-columns)
- [Publications for CDC](howto/cdc-publications.md)

The [changelog](../CHANGELOG.md) has the complete list.
