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
		return grantOnDatabase(ctx, dbConn, database.Name, privs, role)
	case grant.OnSchema != "":
		return grantOnSchema(ctx, dbConn, grant.OnSchema, privs, role)
	case grant.OnTablesInSchema != "":
		// Find the schema owner to set default privileges for tables they create
		schemaOwner := database.Owner
		for _, s := range database.Schemas {
			if s.Name == grant.OnTablesInSchema && s.Owner != "" {
				schemaOwner = s.Owner
				break
			}
		}
		return grantOnTablesInSchema(ctx, dbConn, grant.OnTablesInSchema, privs, role, schemaOwner)
	default:
		return fmt.Errorf("no grant target specified")
	}
}

func grantOnDatabase(ctx context.Context, dbConn *sql.DB, dbName, privs, role string) error {
	slog.Info("Granting on database", "database", dbName, "privileges", privs)
	query := fmt.Sprintf("GRANT %s ON DATABASE %s TO %s", privs, quoteIdentifier(dbName), role)
	_, err := dbConn.ExecContext(ctx, query)
	return err
}

func grantOnSchema(ctx context.Context, dbConn *sql.DB, schema, privs, role string) error {
	slog.Info("Granting on schema", "schema", schema, "privileges", privs)
	query := fmt.Sprintf("GRANT %s ON SCHEMA %s TO %s", privs, quoteIdentifier(schema), role)
	_, err := dbConn.ExecContext(ctx, query)
	return err
}

func grantOnTablesInSchema(ctx context.Context, dbConn *sql.DB, schema, privs, role, schemaOwner string) error {
	slog.Info("Granting on tables in schema", "schema", schema, "privileges", privs, "schema_owner", schemaOwner)

	// Grant on existing tables
	query := fmt.Sprintf(
		"GRANT %s ON ALL TABLES IN SCHEMA %s TO %s",
		privs, quoteIdentifier(schema), role,
	)
	if _, err := dbConn.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("grant on existing tables: %w", err)
	}

	// Set default privileges for tables created by the current (admin) user
	query = fmt.Sprintf(
		"ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT %s ON TABLES TO %s",
		quoteIdentifier(schema), privs, role,
	)
	if _, err := dbConn.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("alter default privileges: %w", err)
	}

	// Also set default privileges for tables created by the schema owner,
	// so that future tables they create are also accessible
	if schemaOwner != "" {
		query = fmt.Sprintf(
			"ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT %s ON TABLES TO %s",
			quoteIdentifier(schemaOwner), quoteIdentifier(schema), privs, role,
		)
		if _, err := dbConn.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("alter default privileges for schema owner: %w", err)
		}
	}

	return nil
}
