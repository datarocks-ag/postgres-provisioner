package provisioner

import (
	"context"
	"database/sql"
	"log/slog"
)

func (p *Provisioner) ensureSchema(ctx context.Context, dbConn *sql.DB, name, owner, strategy string) error {
	slog.Info("Ensuring schema", "schema", name, "owner", owner, "strategy", strategy)

	query := "CREATE SCHEMA IF NOT EXISTS " + quoteIdentifier(name)
	if _, err := p.execMutation(ctx, dbConn, query); err != nil {
		return err
	}

	// Only update owner if strategy is "update"
	if strategy != "create" && owner != "" {
		query = "ALTER SCHEMA " + quoteIdentifier(name) + " OWNER TO " + quoteIdentifier(owner)
		if _, err := p.execMutation(ctx, dbConn, query); err != nil {
			return err
		}
	}

	return nil
}
