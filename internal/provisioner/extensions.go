package provisioner

import (
	"context"
	"database/sql"
	"log/slog"

	"postgres-provisioner/internal/config"
)

func (p *Provisioner) ensureExtension(ctx context.Context, dbConn *sql.DB, ext config.Extension) error {
	slog.Info("Ensuring extension", "extension", ext.Name)
	query := "CREATE EXTENSION IF NOT EXISTS " + quoteIdentifier(ext.Name)
	_, err := p.execMutation(ctx, dbConn, query)
	return err
}
