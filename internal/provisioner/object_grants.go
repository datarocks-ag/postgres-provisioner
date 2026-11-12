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

// grantOnTables grants privileges on specific tables, optionally restricted to columns.
// It runs after migrations; a table that still doesn't exist is an error, because
// silently skipping it would only surface later as "permission denied" in the app.
// In dry-run, migrations aren't applied, so missing tables are only logged.
func (p *Provisioner) grantOnTables(ctx context.Context, dbConn *sql.DB, grant config.Grant) error {
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
		if !p.opts.DryRun {
			return fmt.Errorf("tables not found: %s (on_tables grants run after migrations; the tables must exist by then, and names are case-sensitive)",
				described)
		}
		slog.Info("[DRY RUN] tables not present yet; they may be created by pending migrations",
			"tables", described)
	}

	slog.Info("Granting on tables", "tables", strings.Join(grant.OnTables, ", "),
		"privileges", strings.Join(grant.Privileges, ", "), "columns", strings.Join(grant.Columns, ", "))
	query := fmt.Sprintf("GRANT %s ON TABLE %s TO %s",
		privilegeList(grant.Privileges, grant.Columns), quoteQualifiedNames(tables), quoteIdentifier(grant.Role))
	_, err = p.execMutation(ctx, dbConn, query)
	return err
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
	isMissing := make(map[string]bool, len(missing))
	for _, m := range missing {
		isMissing[m] = true
	}
	var invalid []string
	for _, table := range tables {
		if isMissing[table.String()] {
			continue
		}
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
