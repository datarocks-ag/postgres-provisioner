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

// Options configures optional Provisioner behavior.
type Options struct {
	MigrationsEnabled bool
	// DryRun, when true, logs every mutation as a preview instead of executing it.
	// Read-only queries (existence checks, applied-migration lookups) still hit
	// the database so the preview reflects the live state.
	DryRun bool
}

// DefaultOptions returns Options with sensible defaults.
func DefaultOptions() Options {
	return Options{
		MigrationsEnabled: true,
	}
}

// dbExecer is the subset of *sql.DB and *sql.Tx that supports ExecContext.
// It lets execMutation route the same query through either a connection or a transaction.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// execMutation runs a mutation against exec, or logs it as a preview when DryRun is enabled.
// It returns a no-op sql.Result in dry-run mode so callers can ignore RowsAffected/LastInsertId.
func (p *Provisioner) execMutation(ctx context.Context, exec dbExecer, query string, args ...any) (sql.Result, error) {
	if p.opts.DryRun {
		slog.Info("[DRY RUN] would execute", "sql", query)
		return dryRunResult{}, nil
	}
	return exec.ExecContext(ctx, query, args...)
}

// dryRunResult is a no-op sql.Result returned by execMutation when DryRun is enabled.
type dryRunResult struct{}

func (dryRunResult) LastInsertId() (int64, error) { return 0, nil }
func (dryRunResult) RowsAffected() (int64, error) { return 0, nil }

// Provisioner orchestrates idempotent PostgreSQL resource provisioning.
type Provisioner struct {
	adminDB *sql.DB
	connCfg db.ConnConfig
	cfg     *config.Config
	opts    Options
}

// New creates a new Provisioner.
func New(adminDB *sql.DB, connCfg db.ConnConfig, cfg *config.Config, opts Options) *Provisioner {
	return &Provisioner{
		adminDB: adminDB,
		connCfg: connCfg,
		cfg:     cfg,
		opts:    opts,
	}
}

// Run executes the full provisioning sequence:
// Roles -> Databases -> (per-db) Extensions -> Schemas -> Grants
func (p *Provisioner) Run(ctx context.Context) error {
	if p.opts.DryRun {
		slog.Info("Starting provisioning in DRY RUN mode — no changes will be applied")
	} else {
		slog.Info("Starting provisioning")
	}

	// 1. Roles
	for _, role := range p.cfg.Roles {
		strategy := config.EffectiveStrategy(role.Strategy, p.cfg.Strategy)
		if err := p.ensureRole(ctx, role, strategy); err != nil {
			return fmt.Errorf("provisioning role %q: %w", role.Name, err)
		}
	}

	// 2. Databases, then per-database resources
	for _, database := range p.cfg.Databases {
		dbStrategy := config.EffectiveStrategy(database.Strategy, p.cfg.Strategy)
		created, err := p.ensureDatabase(ctx, database, dbStrategy)
		if err != nil {
			return fmt.Errorf("provisioning database %q: %w", database.Name, err)
		}

		// strategy=create: only provision sub-resources for newly created databases
		if dbStrategy == "create" && !created {
			slog.Info("Skipping sub-resources for existing database (strategy=create)", "database", database.Name)
			continue
		}

		// In dry-run mode, a "created" database wasn't actually created — connecting
		// to it would block until the retry timeout expires. Log a summary of what
		// would happen and skip the per-database connect/provision step. When the
		// database already existed (created=false), we can safely connect and the
		// sub-resource mutations will continue to be previewed.
		if p.opts.DryRun && created {
			slog.Info("[DRY RUN] would connect to newly-created database and provision per-database resources",
				"database", database.Name,
				"extensions", len(database.Extensions),
				"schemas", len(database.Schemas),
				"grants", len(database.Grants),
				"migrations", database.Migrations != nil && database.Migrations.Directory != "",
			)
			continue
		}

		dbConn, err := db.ConnectToDatabase(ctx, p.connCfg, database.Name)
		if err != nil {
			return fmt.Errorf("connecting to database %q: %w", database.Name, err)
		}

		err = p.provisionDatabaseResources(ctx, dbConn, database, dbStrategy)
		dbConn.Close()
		if err != nil {
			return err
		}
	}

	slog.Info("Provisioning complete")
	return nil
}

func (p *Provisioner) provisionDatabaseResources(ctx context.Context, dbConn *sql.DB, database config.Database, dbStrategy string) error {
	// 3. Extensions (always applied — CREATE EXTENSION IF NOT EXISTS is idempotent)
	for _, ext := range database.Extensions {
		if err := p.ensureExtension(ctx, dbConn, ext); err != nil {
			return fmt.Errorf("provisioning extension %q in database %q: %w", ext, database.Name, err)
		}
	}

	// 4. Schemas (strategy inherited from database)
	for _, schema := range database.Schemas {
		owner := schema.Owner
		if owner == "" {
			owner = database.Owner
		}
		if err := p.ensureSchema(ctx, dbConn, schema.Name, owner, dbStrategy); err != nil {
			return fmt.Errorf("provisioning schema %q in database %q: %w", schema.Name, database.Name, err)
		}
	}

	// 5. Grants (always applied — GRANT is idempotent)
	for _, grant := range database.Grants {
		if err := p.applyGrant(ctx, dbConn, database, grant); err != nil {
			return fmt.Errorf("applying grant for role %q in database %q: %w", grant.Role, database.Name, err)
		}
	}

	// 6. Migrations
	if database.Migrations != nil && database.Migrations.Directory != "" {
		if !p.opts.MigrationsEnabled {
			slog.Info("Migrations disabled, skipping", "database", database.Name)
			return nil
		}
		if err := p.runMigrations(ctx, dbConn, database.Name, *database.Migrations); err != nil {
			return fmt.Errorf("running migrations for database %q: %w", database.Name, err)
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
