package provisioner

import (
	"context"
	"database/sql"
	"log/slog"
)

func (p *Provisioner) ensureExtension(ctx context.Context, dbConn *sql.DB, extName string) error {
	slog.Info("Ensuring extension", "extension", extName)
	query := "CREATE EXTENSION IF NOT EXISTS " + quoteIdentifier(extName)
	_, err := dbConn.ExecContext(ctx, query)
	return err
}
