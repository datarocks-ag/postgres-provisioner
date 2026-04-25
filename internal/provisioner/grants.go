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
		// Find the schema owner to set default privileges for tables they create
		schemaOwner := database.Owner
		for _, s := range database.Schemas {
			if s.Name == grant.OnTablesInSchema && s.Owner != "" {
				schemaOwner = s.Owner
				break
			}
		}
		return p.grantOnTablesInSchema(ctx, dbConn, grant.OnTablesInSchema, privs, role, schemaOwner)
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

// grantOnTablesInSchema applies three coupled statements (GRANT on existing tables and
// ALTER DEFAULT PRIVILEGES for both the current user and the schema owner) inside a
// single transaction so that a partial failure cannot leave the schema with grants on
// existing tables but no default privileges for newly created ones.
//
// In dry-run mode the transaction is skipped entirely; each statement is logged as a
// preview with a note that they would run in a single transaction.
func (p *Provisioner) grantOnTablesInSchema(ctx context.Context, dbConn *sql.DB, schema, privs, role, schemaOwner string) error {
	slog.Info("Granting on tables in schema", "schema", schema, "privileges", privs, "schema_owner", schemaOwner)

	if p.opts.DryRun {
		return p.dryRunGrantOnTablesInSchema(schema, privs, role, schemaOwner)
	}

	tx, err := dbConn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := fmt.Sprintf(
		"GRANT %s ON ALL TABLES IN SCHEMA %s TO %s",
		privs, quoteIdentifier(schema), role,
	)
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("grant on existing tables: %w", err)
	}

	query = fmt.Sprintf(
		"ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT %s ON TABLES TO %s",
		quoteIdentifier(schema), privs, role,
	)
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("alter default privileges: %w", err)
	}

	if schemaOwner != "" {
		query = fmt.Sprintf(
			"ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT %s ON TABLES TO %s",
			quoteIdentifier(schemaOwner), quoteIdentifier(schema), privs, role,
		)
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("alter default privileges for schema owner: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing grant transaction: %w", err)
	}
	return nil
}

func (p *Provisioner) dryRunGrantOnTablesInSchema(schema, privs, role, schemaOwner string) error {
	logTx := func(query string) {
		slog.Info("[DRY RUN] would execute in transaction", "sql", redactSecrets(query))
	}
	logTx(fmt.Sprintf("GRANT %s ON ALL TABLES IN SCHEMA %s TO %s",
		privs, quoteIdentifier(schema), role))
	logTx(fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT %s ON TABLES TO %s",
		quoteIdentifier(schema), privs, role))
	if schemaOwner != "" {
		logTx(fmt.Sprintf("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT %s ON TABLES TO %s",
			quoteIdentifier(schemaOwner), quoteIdentifier(schema), privs, role))
	}
	return nil
}
