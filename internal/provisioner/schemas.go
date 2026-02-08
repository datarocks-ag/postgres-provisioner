package provisioner

import (
	"context"
	"database/sql"
	"log/slog"
)

func (p *Provisioner) ensureSchema(ctx context.Context, dbConn *sql.DB, name, owner string) error {
	slog.Info("Ensuring schema", "schema", name, "owner", owner)

	query := "CREATE SCHEMA IF NOT EXISTS " + quoteIdentifier(name)
	if _, err := dbConn.ExecContext(ctx, query); err != nil {
		return err
	}

	if owner != "" {
		query = "ALTER SCHEMA " + quoteIdentifier(name) + " OWNER TO " + quoteIdentifier(owner)
		if _, err := dbConn.ExecContext(ctx, query); err != nil {
			return err
		}
	}

	return nil
}
