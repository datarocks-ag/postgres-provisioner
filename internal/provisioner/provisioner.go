package provisioner

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
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
// Secrets in the SQL (e.g. PASSWORD literals) are redacted before logging.
func (p *Provisioner) execMutation(ctx context.Context, exec dbExecer, query string, args ...any) (sql.Result, error) {
	if p.opts.DryRun {
		slog.Info("[DRY RUN] would execute", "sql", redactSecrets(query))
		return dryRunResult{}, nil
	}
	return exec.ExecContext(ctx, query, args...)
}

// dryRunResult is a no-op sql.Result returned by execMutation when DryRun is enabled.
type dryRunResult struct{}

func (dryRunResult) LastInsertId() (int64, error) { return 0, nil }
func (dryRunResult) RowsAffected() (int64, error) { return 0, nil }

// passwordLiteralPattern matches a PASSWORD '...' clause, accounting for Postgres's
// doubled single-quote escaping inside string literals (e.g. 'it''s').
var passwordLiteralPattern = regexp.MustCompile(`(?i)(PASSWORD\s+)'(?:[^']|'')*'`)

// redactSecrets masks secret literals in SQL so the redacted form is safe to log.
// Currently it covers PASSWORD '...' clauses, which are the only secret-bearing
// statements emitted by this package (CREATE/ALTER ROLE).
func redactSecrets(sql string) string {
	return passwordLiteralPattern.ReplaceAllString(sql, `${1}'***REDACTED***'`)
}

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
// Roles -> Role memberships -> Databases -> (per-db) Extensions -> Schemas -> Revokes -> Grants -> Migrations -> Object grants
func (p *Provisioner) Run(ctx context.Context) error {
	if p.opts.DryRun {
		slog.Info("Starting provisioning in DRY RUN mode — no changes will be applied")
	} else {
		slog.Info("Starting provisioning")
	}

	// 1. Roles
	skipMemberships := make(map[string]bool)
	for _, role := range p.cfg.Roles {
		strategy := config.EffectiveStrategy(role.Strategy, p.cfg.Strategy)
		created, err := p.ensureRole(ctx, role, strategy)
		if err != nil {
			return fmt.Errorf("provisioning role %q: %w", role.Name, err)
		}
		if strategy == "create" && !created {
			skipMemberships[role.Name] = true
		}
	}

	// 2. Role memberships — a separate pass so a role can be a member of another
	// declared role regardless of the order in which they appear in the config.
	for _, role := range p.cfg.Roles {
		if len(role.MemberOf) == 0 {
			continue
		}
		if skipMemberships[role.Name] {
			slog.Info("Skipping memberships for existing role (strategy=create)", "role", role.Name)
			continue
		}
		if err := p.ensureMemberships(ctx, role); err != nil {
			return fmt.Errorf("provisioning memberships for role %q: %w", role.Name, err)
		}
	}

	// 3. Databases, then per-database resources
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
				"revokes", len(database.Revokes),
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
	// 4. Extensions (always applied — CREATE EXTENSION IF NOT EXISTS is idempotent)
	for _, ext := range database.Extensions {
		if err := p.ensureExtension(ctx, dbConn, ext); err != nil {
			return fmt.Errorf("provisioning extension %q in database %q: %w", ext, database.Name, err)
		}
	}

	// 5. Schemas (strategy inherited from database)
	for _, schema := range database.Schemas {
		owner := schema.Owner
		if owner == "" {
			owner = database.Owner
		}
		if err := p.ensureSchema(ctx, dbConn, schema.Name, owner, dbStrategy); err != nil {
			return fmt.Errorf("provisioning schema %q in database %q: %w", schema.Name, database.Name, err)
		}
	}

	// 6. Revokes (before grants, so "revoke from PUBLIC, grant to app" converges)
	for _, revoke := range database.Revokes {
		if err := p.applyRevoke(ctx, dbConn, database, revoke); err != nil {
			return fmt.Errorf("applying revoke for role %q in database %q: %w", revoke.Role, database.Name, err)
		}
	}

	// 7. Schema-level grants (always applied — GRANT is idempotent)
	for _, grant := range database.Grants {
		if grant.IsObjectGrant() {
			continue
		}
		if err := p.applyGrant(ctx, dbConn, database, grant); err != nil {
			return fmt.Errorf("applying grant for role %q in database %q: %w", grant.Role, database.Name, err)
		}
	}

	// 8. Migrations
	migrations, err := p.migrateDatabase(ctx, dbConn, database)
	if err != nil {
		return err
	}

	// 9. Object grants (on_tables) — after migrations, which usually create the tables
	for _, grant := range database.Grants {
		if !grant.IsObjectGrant() {
			continue
		}
		if err := p.grantOnTables(ctx, dbConn, grant, migrations); err != nil {
			return fmt.Errorf("applying grant for role %q in database %q: %w", grant.Role, database.Name, err)
		}
	}

	return nil
}

// migrateDatabase runs the database's migrations if configured and enabled, and reports
// which of those applied.
func (p *Provisioner) migrateDatabase(ctx context.Context, dbConn *sql.DB, database config.Database) (migrationStatus, error) {
	if database.Migrations == nil || database.Migrations.Directory == "" {
		return migrationsNotConfigured, nil
	}
	if !p.opts.MigrationsEnabled {
		slog.Info("Migrations disabled, skipping", "database", database.Name)
		return migrationsDisabled, nil
	}
	if err := p.runMigrations(ctx, dbConn, database.Name, *database.Migrations); err != nil {
		return migrationsRan, fmt.Errorf("running migrations for database %q: %w", database.Name, err)
	}
	return migrationsRan, nil
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
