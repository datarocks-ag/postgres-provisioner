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
