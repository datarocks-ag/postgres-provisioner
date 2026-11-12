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
	var relocatable bool
	err := dbConn.QueryRowContext(ctx,
		`SELECT e.extversion, n.nspname, e.extrelocatable
		FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = $1`, ext.Name,
	).Scan(&installedVersion, &installedSchema, &relocatable)
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
			"hint", schemaDriftHint(ext, relocatable))
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

	// A downgrade or a version the server doesn't ship has no update path; ALTER EXTENSION
	// would fail and abort the run, while the installed version keeps working.
	var hasUpdatePath bool
	err = dbConn.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_extension_update_paths($1)
		WHERE source = $2 AND target = $3 AND path IS NOT NULL)`,
		ext.Name, installedVersion, ext.Version,
	).Scan(&hasUpdatePath)
	if err != nil {
		return fmt.Errorf("checking extension update path: %w", err)
	}
	if !hasUpdatePath {
		slog.Warn("No update path from the installed to the configured extension version; skipping update",
			"extension", ext.Name, "installed_version", installedVersion, "configured_version", ext.Version,
			"hint", "a downgrade needs DROP EXTENSION and CREATE EXTENSION ... VERSION; otherwise install a package providing the version")
		return nil
	}

	slog.Info("Updating extension", "extension", ext.Name, "from", installedVersion, "to", ext.Version)
	query := fmt.Sprintf("ALTER EXTENSION %s UPDATE TO %s", quoteIdentifier(ext.Name), quoteLiteral(ext.Version))
	_, err = p.execMutation(ctx, dbConn, query)
	return err
}

// schemaDriftHint tells the operator how to move an installed extension to the configured
// schema. ALTER EXTENSION ... SET SCHEMA only works for relocatable extensions; the others
// must be dropped and recreated, which also drops objects that depend on them.
func schemaDriftHint(ext config.Extension, relocatable bool) string {
	if relocatable {
		return fmt.Sprintf("ALTER EXTENSION %s SET SCHEMA %s", quoteIdentifier(ext.Name), quoteIdentifier(ext.Schema))
	}
	return fmt.Sprintf("extension is not relocatable: DROP EXTENSION %s and let the provisioner recreate it in %s "+
		"(DROP ... CASCADE also drops dependent objects)", quoteIdentifier(ext.Name), quoteIdentifier(ext.Schema))
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

// preloadRequirement describes an extension whose library must be listed in the server's
// shared_preload_libraries.
type preloadRequirement struct {
	library string
	// requiredForCreate is true when CREATE EXTENSION itself fails without the library
	// preloaded, because the library's _PG_init raises an ERROR when loaded any other way.
	// Otherwise CREATE EXTENSION succeeds and the extension only fails when used.
	requiredForCreate bool
}

// preloadRequirements maps extensions to the library they require in shared_preload_libraries.
// pg_stat_statements installs fine but its views fail at use time (e.g. for postgres_exporter);
// pg_cron and timescaledb refuse to be created at all.
var preloadRequirements = map[string]preloadRequirement{
	"pg_stat_statements": {library: "pg_stat_statements"},
	"pg_cron":            {library: "pg_cron", requiredForCreate: true},
	"timescaledb":        {library: "timescaledb", requiredForCreate: true},
}

// preloadState caches the server's shared_preload_libraries for one Run. The setting is
// server-wide, so it is read at most once, and each missing library is warned about once.
type preloadState struct {
	checked bool
	// loaded is nil when the setting could not be read and the check is skipped.
	loaded map[string]bool
	warned map[string]bool
}

// checkPreloadLibraries runs before any extension of database is created. For each configured
// extension whose library isn't preloaded, it fails if the extension can't be created without
// the library and isn't installed yet; otherwise it logs a warning. The provisioner can't fix
// this itself, since changing shared_preload_libraries requires a server restart.
// If the setting can't be read (a non-superuser admin without pg_read_all_settings), the check
// is skipped with an info log.
func (p *Provisioner) checkPreloadLibraries(ctx context.Context, dbConn *sql.DB, database config.Database) error {
	var exts []config.Extension
	for _, ext := range database.Extensions {
		if _, ok := preloadRequirements[ext.Name]; ok {
			exts = append(exts, ext)
		}
	}
	if len(exts) == 0 {
		return nil
	}

	loaded := p.loadedPreloadLibraries(ctx, dbConn)
	if loaded == nil {
		return nil
	}
	for _, ext := range exts {
		req := preloadRequirements[ext.Name]
		if loaded[req.library] {
			continue
		}
		if req.requiredForCreate {
			installed, err := extensionInstalled(ctx, dbConn, ext.Name)
			if err != nil {
				return fmt.Errorf("checking installed extension %q in database %q: %w", ext.Name, database.Name, err)
			}
			if !installed {
				return fmt.Errorf("extension %q in database %q can only be created when library %q is in shared_preload_libraries: "+
					"restart postgres with -c shared_preload_libraries=%s (or set it in postgresql.conf)",
					ext.Name, database.Name, req.library, req.library)
			}
		}
		p.warnMissingPreloadLibrary(req.library)
	}
	return nil
}

// loadedPreloadLibraries returns the server's preloaded libraries, reading the setting on
// first use. It returns nil when the setting can't be read.
func (p *Provisioner) loadedPreloadLibraries(ctx context.Context, dbConn *sql.DB) map[string]bool {
	if p.preload.checked {
		return p.preload.loaded
	}
	p.preload.checked = true

	var raw string
	if err := dbConn.QueryRowContext(ctx, "SHOW shared_preload_libraries").Scan(&raw); err != nil {
		slog.Info("Could not read shared_preload_libraries; skipping the preload check for extensions",
			"error", err, "hint", "grant pg_read_all_settings to the admin role to enable it")
		return nil
	}
	p.preload.loaded = parsePreloadLibraries(raw)
	return p.preload.loaded
}

// warnMissingPreloadLibrary logs that library isn't preloaded, once per Run.
func (p *Provisioner) warnMissingPreloadLibrary(library string) {
	if p.preload.warned[library] {
		return
	}
	if p.preload.warned == nil {
		p.preload.warned = make(map[string]bool)
	}
	p.preload.warned[library] = true
	slog.Warn("Extension requires shared_preload_libraries; it is installed but will fail when used until the server is restarted with the library preloaded",
		"library", library, "hint", "start postgres with -c shared_preload_libraries="+library)
}

// parsePreloadLibraries parses the comma-separated shared_preload_libraries setting into a set
// of library names. Entries are normalized the way the server resolves them, so
// "$libdir/pg_stat_statements" and "pg_stat_statements.so" both count as "pg_stat_statements".
func parsePreloadLibraries(raw string) map[string]bool {
	loaded := make(map[string]bool)
	for _, entry := range strings.Split(raw, ",") {
		lib := strings.Trim(strings.TrimSpace(entry), `"`)
		lib = strings.TrimPrefix(lib, "$libdir/")
		lib = strings.TrimSuffix(lib, ".so")
		lib = strings.TrimSuffix(lib, ".dylib")
		if lib != "" {
			loaded[lib] = true
		}
	}
	return loaded
}

func extensionInstalled(ctx context.Context, dbConn *sql.DB, name string) (bool, error) {
	var installed bool
	err := dbConn.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)", name).Scan(&installed)
	return installed, err
}
