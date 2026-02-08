package provisioner

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"postgres-provisioner/internal/config"
	"postgres-provisioner/internal/db"
)

// Provisioner orchestrates idempotent PostgreSQL resource provisioning.
type Provisioner struct {
	adminDB *sql.DB
	connCfg db.ConnConfig
	cfg     *config.Config
}

// New creates a new Provisioner.
func New(adminDB *sql.DB, connCfg db.ConnConfig, cfg *config.Config) *Provisioner {
	return &Provisioner{
		adminDB: adminDB,
		connCfg: connCfg,
		cfg:     cfg,
	}
}

// Run executes the full provisioning sequence:
// Roles -> Databases -> (per-db) Extensions -> Schemas -> Grants
func (p *Provisioner) Run(ctx context.Context) error {
	slog.Info("Starting provisioning")

	// 1. Roles
	for _, role := range p.cfg.Roles {
		if err := p.ensureRole(ctx, role); err != nil {
			return fmt.Errorf("provisioning role %q: %w", role.Name, err)
		}
	}

	// 2. Databases, then per-database resources
	for _, database := range p.cfg.Databases {
		if err := p.ensureDatabase(ctx, database); err != nil {
			return fmt.Errorf("provisioning database %q: %w", database.Name, err)
		}

		// Connect to the target database for extensions/schemas/grants
		dbConn, err := db.ConnectToDatabase(ctx, p.connCfg, database.Name)
		if err != nil {
			return fmt.Errorf("connecting to database %q: %w", database.Name, err)
		}

		err = p.provisionDatabaseResources(ctx, dbConn, database)
		dbConn.Close()
		if err != nil {
			return err
		}
	}

	slog.Info("Provisioning complete")
	return nil
}

func (p *Provisioner) provisionDatabaseResources(ctx context.Context, dbConn *sql.DB, database config.Database) error {
	// 3. Extensions
	for _, ext := range database.Extensions {
		if err := p.ensureExtension(ctx, dbConn, ext); err != nil {
			return fmt.Errorf("provisioning extension %q in database %q: %w", ext, database.Name, err)
		}
	}

	// 4. Schemas
	for _, schema := range database.Schemas {
		owner := schema.Owner
		if owner == "" {
			owner = database.Owner
		}
		if err := p.ensureSchema(ctx, dbConn, schema.Name, owner); err != nil {
			return fmt.Errorf("provisioning schema %q in database %q: %w", schema.Name, database.Name, err)
		}
	}

	// 5. Grants
	for _, grant := range database.Grants {
		if err := p.applyGrant(ctx, dbConn, database, grant); err != nil {
			return fmt.Errorf("applying grant for role %q in database %q: %w", grant.Role, database.Name, err)
		}
	}

	return nil
}

// quoteIdentifier quotes a PostgreSQL identifier to prevent SQL injection.
// It doubles any embedded double-quotes and wraps in double-quotes.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteLiteral quotes a PostgreSQL string literal to prevent SQL injection.
// It doubles any embedded single-quotes and wraps in single-quotes.
func quoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}
