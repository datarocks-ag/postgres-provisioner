package provisioner

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"postgres-provisioner/internal/config"
)

// settingsScope identifies where settings are stored: role-wide, database-wide, or for a
// role within one database. pg_db_role_setting uses oid 0 for "any" role/database.
type settingsScope struct {
	description string // for logs, e.g. `role "app"`
	alterSQL    string // statement prefix, e.g. `ALTER ROLE "app"`
	query       string // reads current setconfig entries for this scope
	args        []any
}

func roleScope(role string) settingsScope {
	return settingsScope{
		description: fmt.Sprintf("role %q", role),
		alterSQL:    "ALTER ROLE " + quoteIdentifier(role),
		query: `SELECT unnest(s.setconfig) FROM pg_db_role_setting s
			JOIN pg_roles r ON r.oid = s.setrole
			WHERE r.rolname = $1 AND s.setdatabase = 0`,
		args: []any{role},
	}
}

func databaseScope(database string) settingsScope {
	return settingsScope{
		description: fmt.Sprintf("database %q", database),
		alterSQL:    "ALTER DATABASE " + quoteIdentifier(database),
		query: `SELECT unnest(s.setconfig) FROM pg_db_role_setting s
			JOIN pg_database d ON d.oid = s.setdatabase
			WHERE d.datname = $1 AND s.setrole = 0`,
		args: []any{database},
	}
}

func roleInDatabaseScope(role, database string) settingsScope {
	return settingsScope{
		description: fmt.Sprintf("role %q in database %q", role, database),
		alterSQL:    "ALTER ROLE " + quoteIdentifier(role) + " IN DATABASE " + quoteIdentifier(database),
		query: `SELECT unnest(s.setconfig) FROM pg_db_role_setting s
			JOIN pg_roles r ON r.oid = s.setrole
			JOIN pg_database d ON d.oid = s.setdatabase
			WHERE r.rolname = $1 AND d.datname = $2`,
		args: []any{role, database},
	}
}

// applySettings sets or resets each desired setting whose stored value differs. Settings
// not listed are left untouched, so removing one from the config does not reset it.
func (p *Provisioner) applySettings(ctx context.Context, scope settingsScope, desired config.Settings) error {
	if len(desired) == 0 {
		return nil
	}

	current, err := p.currentSettings(ctx, scope)
	if err != nil {
		return fmt.Errorf("reading current settings for %s: %w", scope.description, err)
	}

	for _, configName := range desired.SortedNames() {
		name := strings.ToLower(configName)
		value := desired[configName]
		stored, isSet := current[name]

		if value.IsReset() {
			if !isSet {
				slog.Info("Setting not set, nothing to reset", "scope", scope.description, "setting", name)
				continue
			}
			slog.Info("Resetting setting", "scope", scope.description, "setting", name)
			if _, err := p.execMutation(ctx, p.adminDB, fmt.Sprintf("%s RESET %s", scope.alterSQL, name)); err != nil {
				return fmt.Errorf("resetting %s for %s: %w", name, scope.description, err)
			}
			continue
		}

		if isSet && settingMatches(stored, value.Values) {
			slog.Info("Setting unchanged, skipping", "scope", scope.description, "setting", name)
			continue
		}

		slog.Info("Applying setting", "scope", scope.description, "setting", name)
		query := fmt.Sprintf("%s SET %s = %s", scope.alterSQL, name, formatSettingValue(value.Values))
		if _, err := p.execMutation(ctx, p.adminDB, query); err != nil {
			return fmt.Errorf("setting %s for %s: %w", name, scope.description, err)
		}
	}
	return nil
}

// currentSettings returns the stored settings of a scope, keyed by lower-case name.
// A role or database that doesn't exist yet has none.
func (p *Provisioner) currentSettings(ctx context.Context, scope settingsScope) (map[string]string, error) {
	rows, err := p.adminDB.QueryContext(ctx, scope.query, scope.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	current := make(map[string]string)
	for rows.Next() {
		var entry string
		if err := rows.Scan(&entry); err != nil {
			return nil, err
		}
		name, value, _ := strings.Cut(entry, "=")
		current[strings.ToLower(name)] = value
	}
	return current, rows.Err()
}

// formatSettingValue renders values as SQL literals. Each list element is quoted
// separately: SET search_path = 'a, b' would name a single schema "a, b".
func formatSettingValue(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = quoteLiteral(v)
	}
	return strings.Join(quoted, ", ")
}

// plainIdentifier matches identifiers PostgreSQL leaves unquoted when it stores list
// settings such as search_path.
var plainIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// settingMatches compares a stored setconfig value with the desired values. PostgreSQL
// stores lists joined by ", ", and quotes elements of identifier lists (search_path) that
// aren't plain identifiers, e.g. "$user". Both forms are accepted; a false mismatch only
// causes a redundant, harmless ALTER.
func settingMatches(stored string, values []string) bool {
	if stored == strings.Join(values, ", ") {
		return true
	}
	quoted := make([]string, len(values))
	for i, v := range values {
		if plainIdentifier.MatchString(v) {
			quoted[i] = v
		} else {
			quoted[i] = quoteIdentifier(v)
		}
	}
	return stored == strings.Join(quoted, ", ")
}
