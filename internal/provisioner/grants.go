package provisioner

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"postgres-provisioner/internal/config"
)

func (p *Provisioner) applyGrant(ctx context.Context, dbConn *sql.DB, database config.Database, grant config.Grant) error {
	privs := strings.Join(grant.Privileges, ", ")
	role := quoteIdentifier(grant.Role)

	switch {
	case grant.OnDatabase:
		return p.grantOnDatabase(ctx, dbConn, database.Name, privs, role)
	case grant.OnSchema != "":
		return p.grantOnSchema(ctx, dbConn, grant.OnSchema, privs, role)
	case grant.OnTablesInSchema != "":
		schema := grant.OnTablesInSchema
		return p.grantOnObjectsInSchema(ctx, dbConn, tablesKind, schema, privs, role, schemaOwnerFor(database, schema))
	case grant.OnSequencesInSchema != "":
		schema := grant.OnSequencesInSchema
		return p.grantOnObjectsInSchema(ctx, dbConn, sequencesKind, schema, privs, role, schemaOwnerFor(database, schema))
	case grant.OnFunctionsInSchema != "":
		schema := grant.OnFunctionsInSchema
		return p.grantOnObjectsInSchema(ctx, dbConn, functionsKind, schema, privs, role, schemaOwnerFor(database, schema))
	default:
		return fmt.Errorf("no grant target specified")
	}
}

func (p *Provisioner) grantOnDatabase(ctx context.Context, dbConn *sql.DB, dbName, privs, role string) error {
	slog.Info("Granting on database", "database", dbName, "privileges", privs)
	query := fmt.Sprintf("GRANT %s ON DATABASE %s TO %s", privs, quoteIdentifier(dbName), role)
	_, err := p.execMutation(ctx, dbConn, query)
	return err
}

func (p *Provisioner) grantOnSchema(ctx context.Context, dbConn *sql.DB, schema, privs, role string) error {
	slog.Info("Granting on schema", "schema", schema, "privileges", privs)
	query := fmt.Sprintf("GRANT %s ON SCHEMA %s TO %s", privs, quoteIdentifier(schema), role)
	_, err := p.execMutation(ctx, dbConn, query)
	return err
}

// schemaObjectKind describes a class of schema objects that supports both
// "GRANT ... ON ALL <kind> IN SCHEMA" and "ALTER DEFAULT PRIVILEGES ... ON <kind>".
type schemaObjectKind struct {
	label          string // human-readable, used in logs and errors
	allKeyword     string // keyword in GRANT ... ON ALL <allKeyword> IN SCHEMA
	defaultKeyword string // keyword in ALTER DEFAULT PRIVILEGES ... ON <defaultKeyword>
}

var (
	tablesKind    = schemaObjectKind{label: "tables", allKeyword: "TABLES", defaultKeyword: "TABLES"}
	sequencesKind = schemaObjectKind{label: "sequences", allKeyword: "SEQUENCES", defaultKeyword: "SEQUENCES"}
	// ROUTINES covers both functions and procedures; ALTER DEFAULT PRIVILEGES ... ON FUNCTIONS
	// likewise applies to procedures.
	functionsKind = schemaObjectKind{label: "functions", allKeyword: "ROUTINES", defaultKeyword: "FUNCTIONS"}
)

// schemaOwnerFor returns the configured owner of schema, falling back to the database owner.
func schemaOwnerFor(database config.Database, schema string) string {
	for _, s := range database.Schemas {
		if s.Name == schema && s.Owner != "" {
			return s.Owner
		}
	}
	return database.Owner
}

// grantOnObjectsInSchema applies three coupled statements (GRANT on existing objects and
// ALTER DEFAULT PRIVILEGES for both the current user and the schema owner) inside a
// single transaction so that a partial failure cannot leave the schema with grants on
// existing objects but no default privileges for newly created ones.
//
// In dry-run mode the transaction is skipped entirely; each statement is logged as a
// preview with a note that they would run in a single transaction.
func (p *Provisioner) grantOnObjectsInSchema(ctx context.Context, dbConn *sql.DB, kind schemaObjectKind, schema, privs, role, schemaOwner string) error {
	slog.Info("Granting on "+kind.label+" in schema", "schema", schema, "privileges", privs, "schema_owner", schemaOwner)

	statements := objectsInSchemaStatements(kind, schema, privs, role, schemaOwner)

	if p.opts.DryRun {
		for _, stmt := range statements {
			slog.Info("[DRY RUN] would execute in transaction", "sql", redactSecrets(stmt.sql))
		}
		return nil
	}

	tx, err := dbConn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt.sql); err != nil {
			return fmt.Errorf("%s: %w", stmt.description, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing grant transaction: %w", err)
	}
	return nil
}

type describedStatement struct {
	sql         string
	description string
}

func objectsInSchemaStatements(kind schemaObjectKind, schema, privs, role, schemaOwner string) []describedStatement {
	statements := []describedStatement{
		{
			sql: fmt.Sprintf("GRANT %s ON ALL %s IN SCHEMA %s TO %s",
				privs, kind.allKeyword, quoteIdentifier(schema), role),
			description: "grant on existing " + kind.label,
		},
		{
			sql: fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT %s ON %s TO %s",
				quoteIdentifier(schema), privs, kind.defaultKeyword, role),
			description: "alter default privileges",
		},
	}
	if schemaOwner != "" {
		statements = append(statements, describedStatement{
			sql: fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT %s ON %s TO %s",
				quoteIdentifier(schemaOwner), quoteIdentifier(schema), privs, kind.defaultKeyword, role),
			description: "alter default privileges for schema owner",
		})
	}
	return statements
}
