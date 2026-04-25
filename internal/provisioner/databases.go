package provisioner

import (
	"context"
	"database/sql"
	"log/slog"

	"postgres-provisioner/internal/config"
)

func (p *Provisioner) ensureDatabase(ctx context.Context, database config.Database, strategy string) (bool, error) {
	exists, err := databaseExists(ctx, p.adminDB, database.Name)
	if err != nil {
		return false, err
	}

	if exists {
		if strategy == "create" {
			slog.Info("Skipping existing database (strategy=create)", "database", database.Name)
			return false, nil
		}
		slog.Info("Database already exists", "database", database.Name)
		if database.Owner != "" {
			return false, p.alterDatabaseOwner(ctx, database.Name, database.Owner)
		}
		return false, nil
	}

	slog.Info("Creating database", "database", database.Name)
	return true, p.createDatabase(ctx, database)
}

func databaseExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", name,
	).Scan(&exists)
	return exists, err
}

func (p *Provisioner) createDatabase(ctx context.Context, database config.Database) error {
	query := "CREATE DATABASE " + quoteIdentifier(database.Name)
	if database.Owner != "" {
		query += " OWNER " + quoteIdentifier(database.Owner)
	}
	_, err := p.execMutation(ctx, p.adminDB, query)
	return err
}

func (p *Provisioner) alterDatabaseOwner(ctx context.Context, dbName, owner string) error {
	query := "ALTER DATABASE " + quoteIdentifier(dbName) + " OWNER TO " + quoteIdentifier(owner)
	_, err := p.execMutation(ctx, p.adminDB, query)
	return err
}
