package provisioner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"postgres-provisioner/internal/config"
)

// migrationStatus records what migrateDatabase did for a database in this run. Object
// grants use it to decide whether a missing table is an error or expected.
type migrationStatus int

const (
	// migrationsNotConfigured: the database has no migrations directory.
	migrationsNotConfigured migrationStatus = iota
	// migrationsDisabled: migrations are configured but disabled (MIGRATIONS_ENABLED=false),
	// typically because the application runs them itself.
	migrationsDisabled
	// migrationsRan: this run applied the migrations (in dry-run: previewed them).
	migrationsRan
)

// grantOnTables grants privileges on specific tables, optionally restricted to columns.
// It runs after migrations. What happens to tables that don't exist depends on migrations:
//   - migrationsRan: an error, because the migrations this run applied should have created
//     them, and silently skipping would only surface later as "permission denied" in the
//     app. In dry-run the migrations were only previewed, so missing tables are logged and
//     the full GRANT is previewed.
//   - migrationsDisabled / migrationsNotConfigured: a warning. Something else (e.g. the
//     application) creates the tables, possibly after this provisioner ran as an init
//     container; failing would deadlock that startup. The grant covers the existing tables
//     and the missing ones are picked up by the next run.
func (p *Provisioner) grantOnTables(ctx context.Context, dbConn *sql.DB, grant config.Grant, migrations migrationStatus) error {
	tables := make([]config.QualifiedName, 0, len(grant.OnTables))
	for _, raw := range grant.OnTables {
		table, err := config.ParseQualifiedName(raw)
		if err != nil {
			return err
		}
		tables = append(tables, table)
	}

	missing, err := missingTables(ctx, dbConn, tables)
	if err != nil {
		return fmt.Errorf("checking tables: %w", err)
	}
	if err := checkGrantableRelationKinds(ctx, dbConn, tables, missing); err != nil {
		return err
	}
	if len(missing) > 0 {
		described, err := describeMissingTables(ctx, dbConn, missing)
		if err != nil {
			return fmt.Errorf("checking tables: %w", err)
		}
		switch {
		case migrations == migrationsRan && !p.opts.DryRun:
			return fmt.Errorf("tables not found: %s (checked after this run's migrations; names are case-sensitive)",
				described)
		case migrations == migrationsRan:
			slog.Info("[DRY RUN] tables not present yet; a real run fails unless pending migrations create them",
				"tables", described, "role", grant.Role)
		default:
			slog.Warn("Skipping on_tables grant for tables that don't exist yet; re-run provisioning once they exist",
				"tables", described, "role", grant.Role, "reason", missingTablesReason(migrations))
			tables = existingTables(tables, missing)
			if len(tables) == 0 {
				return nil
			}
		}
	}

	quoted := quoteQualifiedNames(tables)
	slog.Info("Granting on tables", "tables", quoted,
		"privileges", strings.Join(grant.Privileges, ", "), "columns", strings.Join(grant.Columns, ", "))
	query := fmt.Sprintf("GRANT %s ON TABLE %s TO %s",
		privilegeList(grant.Privileges, grant.Columns), quoted, quoteIdentifier(grant.Role))
	_, err = p.execMutation(ctx, dbConn, query)
	return err
}

// missingTablesReason explains, for the warning, why missing tables are tolerated.
func missingTablesReason(migrations migrationStatus) string {
	if migrations == migrationsDisabled {
		return "migrations are disabled (MIGRATIONS_ENABLED=false / --migrations=false), so this run did not create any tables"
	}
	return "no migrations are configured for this database, so this run did not create any tables"
}

// existingTables returns tables without the ones named in missing (as returned by missingTables).
func existingTables(tables []config.QualifiedName, missing []string) []config.QualifiedName {
	isMissing := make(map[string]bool, len(missing))
	for _, m := range missing {
		isMissing[m] = true
	}
	existing := make([]config.QualifiedName, 0, len(tables))
	for _, t := range tables {
		if !isMissing[t.String()] {
			existing = append(existing, t)
		}
	}
	return existing
}

// privilegeList renders privileges for GRANT. With columns, the column list must follow
// every privilege: SELECT ("a", "b"), UPDATE ("a", "b").
func privilegeList(privileges, columns []string) string {
	if len(columns) == 0 {
		return strings.Join(privileges, ", ")
	}
	quotedColumns := make([]string, len(columns))
	for i, c := range columns {
		quotedColumns[i] = quoteIdentifier(c)
	}
	columnList := " (" + strings.Join(quotedColumns, ", ") + ")"
	parts := make([]string, len(privileges))
	for i, priv := range privileges {
		parts[i] = priv + columnList
	}
	return strings.Join(parts, ", ")
}

func quoteQualifiedName(name config.QualifiedName) string {
	return quoteIdentifier(name.Schema) + "." + quoteIdentifier(name.Name)
}

func quoteQualifiedNames(names []config.QualifiedName) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = quoteQualifiedName(n)
	}
	return strings.Join(quoted, ", ")
}

// missingTables returns the names (schema.name) of the given relations that don't exist.
// to_regclass returns NULL instead of failing for unknown relations.
func missingTables(ctx context.Context, dbConn *sql.DB, names []config.QualifiedName) ([]string, error) {
	var missing []string
	for _, name := range names {
		var oid sql.NullString
		if err := dbConn.QueryRowContext(ctx, "SELECT to_regclass($1)::text", quoteQualifiedName(name)).Scan(&oid); err != nil {
			return nil, err
		}
		if !oid.Valid {
			missing = append(missing, name.String())
		}
	}
	return missing, nil
}

// relationKindQuery returns pg_class.relkind of a relation, or no row if it doesn't exist.
const relationKindQuery = `SELECT relkind::text FROM pg_class WHERE oid = to_regclass($1)`

// relationKind returns the pg_class.relkind of name ("r" table, "v" view, "i" index, ...),
// or "" if the relation doesn't exist. to_regclass also resolves indexes, sequences and
// composite types, so callers that need a specific kind must check it.
func relationKind(ctx context.Context, dbConn *sql.DB, name config.QualifiedName) (string, error) {
	var kind string
	err := dbConn.QueryRowContext(ctx, relationKindQuery, quoteQualifiedName(name)).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return kind, err
}

// grantableRelationKinds are the relkinds GRANT ... ON TABLE applies to as intended:
// tables, partitioned tables, views, materialized views and foreign tables.
var grantableRelationKinds = map[string]bool{"r": true, "p": true, "v": true, "m": true, "f": true}

// relationKindNames names the other relkinds for error messages.
var relationKindNames = map[string]string{
	"i": "an index",
	"I": "a partitioned index",
	"S": "a sequence",
	"c": "a composite type",
	"t": "a TOAST table",
}

// checkGrantableRelationKinds fails if any existing table in tables (those not listed in
// missing) is a relation kind on_tables doesn't support, such as an index.
func checkGrantableRelationKinds(ctx context.Context, dbConn *sql.DB, tables []config.QualifiedName, missing []string) error {
	var invalid []string
	for _, table := range existingTables(tables, missing) {
		kind, err := relationKind(ctx, dbConn, table)
		if err != nil {
			return fmt.Errorf("checking tables: %w", err)
		}
		if kind == "" || grantableRelationKinds[kind] {
			continue
		}
		label, ok := relationKindNames[kind]
		if !ok {
			label = fmt.Sprintf("a relation of kind %q", kind)
		}
		invalid = append(invalid, fmt.Sprintf("%s is %s", table, label))
	}
	if len(invalid) > 0 {
		return fmt.Errorf("%s (on_tables supports tables, partitioned tables, views, materialized views and foreign tables)",
			strings.Join(invalid, "; "))
	}
	return nil
}

// caseInsensitiveMatchQuery finds a relation whose schema and name match ignoring case.
// Names are used verbatim as quoted identifiers, so App.Users won't find app.users; this
// lookup only feeds the "did you mean" hint.
const caseInsensitiveMatchQuery = `SELECT n.nspname, c.relname FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE lower(n.nspname) = lower($1) AND lower(c.relname) = lower($2)
ORDER BY n.nspname, c.relname LIMIT 1`

// describeMissingTables joins the missing table names (as returned by missingTables) for
// messages, adding a "did you mean" hint for each one that exists with different case.
func describeMissingTables(ctx context.Context, dbConn *sql.DB, missing []string) (string, error) {
	described := make([]string, len(missing))
	for i, raw := range missing {
		described[i] = raw
		name, err := config.ParseQualifiedName(raw)
		if err != nil {
			continue
		}
		var match config.QualifiedName
		err = dbConn.QueryRowContext(ctx, caseInsensitiveMatchQuery, name.Schema, name.Name).Scan(&match.Schema, &match.Name)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", err
		}
		described[i] = fmt.Sprintf("%s (did you mean %s?)", raw, match)
	}
	return strings.Join(described, ", "), nil
}
