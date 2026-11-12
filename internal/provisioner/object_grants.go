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
