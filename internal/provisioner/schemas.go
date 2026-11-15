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

	// Applied under both strategies: with strategy=create, schemas are only provisioned in a
	// database created in this run, so there is no existing owner to protect.
	if owner != "" {
		query = "ALTER SCHEMA " + quoteIdentifier(name) + " OWNER TO " + quoteIdentifier(owner)
		if _, err := p.execMutation(ctx, dbConn, query); err != nil {
			return err
		}
	}

	return nil
}
