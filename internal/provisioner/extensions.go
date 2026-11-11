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

// ensureExtension installs the extension if missing. For an installed extension it
// updates a pinned version that differs (strategy "update" only) and warns about a
// differing schema: ALTER EXTENSION ... SET SCHEMA breaks references to the extension's
// objects (views, function bodies) and some extensions can't be relocated, so the
// provisioner leaves moving it to an operator.
func (p *Provisioner) ensureExtension(ctx context.Context, dbConn *sql.DB, ext config.Extension, strategy string) error {
	var installedVersion, installedSchema string
	err := dbConn.QueryRowContext(ctx,
		`SELECT e.extversion, n.nspname
		FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = $1`, ext.Name,
	).Scan(&installedVersion, &installedSchema)
	if errors.Is(err, sql.ErrNoRows) {
		slog.Info("Creating extension", "extension", ext.Name, "schema", ext.Schema, "version", ext.Version)
		_, err := p.execMutation(ctx, dbConn, createExtensionSQL(ext))
		return err
	}
	if err != nil {
		return fmt.Errorf("checking installed extension: %w", err)
	}

	if ext.Schema != "" && ext.Schema != installedSchema {
		slog.Warn("Extension is installed in a different schema; not moving it automatically",
			"extension", ext.Name, "installed_schema", installedSchema, "configured_schema", ext.Schema,
			"hint", fmt.Sprintf("ALTER EXTENSION %s SET SCHEMA %s", quoteIdentifier(ext.Name), quoteIdentifier(ext.Schema)))
	}

	if ext.Version == "" || ext.Version == installedVersion {
		slog.Info("Extension already installed", "extension", ext.Name, "version", installedVersion)
		return nil
	}
	if strategy == "create" {
		slog.Info("Skipping extension version update (strategy=create)",
			"extension", ext.Name, "installed_version", installedVersion, "configured_version", ext.Version)
		return nil
	}

	slog.Info("Updating extension", "extension", ext.Name, "from", installedVersion, "to", ext.Version)
	query := fmt.Sprintf("ALTER EXTENSION %s UPDATE TO %s", quoteIdentifier(ext.Name), quoteLiteral(ext.Version))
	_, err = p.execMutation(ctx, dbConn, query)
	return err
}

func createExtensionSQL(ext config.Extension) string {
	parts := []string{"CREATE EXTENSION IF NOT EXISTS " + quoteIdentifier(ext.Name)}
	if ext.Schema != "" {
		parts = append(parts, "SCHEMA "+quoteIdentifier(ext.Schema))
	}
	if ext.Version != "" {
		parts = append(parts, "VERSION "+quoteLiteral(ext.Version))
	}
	if ext.Cascade {
		parts = append(parts, "CASCADE")
	}
	return strings.Join(parts, " ")
}

// preloadLibraries maps extensions to the library they require in the server's
// shared_preload_libraries. CREATE EXTENSION succeeds without it, but the extension's
// views/functions fail at use time — e.g. pg_stat_statements, which postgres_exporter reads.
var preloadLibraries = map[string]string{
	"pg_stat_statements": "pg_stat_statements",
	"pg_cron":            "pg_cron",
	"timescaledb":        "timescaledb",
}

// warnMissingPreloadLibraries logs a warning for each configured extension whose library
// is not in shared_preload_libraries. The provisioner can't fix this itself: the setting
// requires a server restart. Failure to read the setting is logged at debug level only,
// since a non-superuser admin may not be allowed to see it.
func (p *Provisioner) warnMissingPreloadLibraries(ctx context.Context, dbConn *sql.DB, database config.Database) {
	missing, err := missingPreloadLibraries(ctx, dbConn, database.Extensions)
	if err != nil {
		slog.Debug("Could not check shared_preload_libraries", "database", database.Name, "error", err)
		return
	}
	for _, lib := range missing {
		slog.Warn("Extension requires shared_preload_libraries; it is installed but will fail when used until the server is restarted with the library preloaded",
			"database", database.Name, "library", lib,
			"hint", "start postgres with -c shared_preload_libraries="+lib)
	}
}

// missingPreloadLibraries returns the preload libraries required by exts that the server
// has not loaded. It only queries the server when at least one extension needs preloading.
func missingPreloadLibraries(ctx context.Context, dbConn *sql.DB, exts []config.Extension) ([]string, error) {
	var needed []string
	for _, ext := range exts {
		if lib, ok := preloadLibraries[ext.Name]; ok {
			needed = append(needed, lib)
		}
	}
	if len(needed) == 0 {
		return nil, nil
	}

	var raw string
	if err := dbConn.QueryRowContext(ctx, "SHOW shared_preload_libraries").Scan(&raw); err != nil {
		return nil, err
	}
	loaded := make(map[string]bool)
	for _, lib := range strings.Split(raw, ",") {
		loaded[strings.Trim(strings.TrimSpace(lib), `"`)] = true
	}

	var missing []string
	for _, lib := range needed {
		if !loaded[lib] {
			missing = append(missing, lib)
		}
	}
	return missing, nil
}
