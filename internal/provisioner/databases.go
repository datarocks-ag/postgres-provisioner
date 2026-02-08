package provisioner

import (
	"context"
	"database/sql"
	"log/slog"

	"postgres-provisioner/internal/config"
)

func (p *Provisioner) ensureDatabase(ctx context.Context, database config.Database) error {
	exists, err := databaseExists(ctx, p.adminDB, database.Name)
	if err != nil {
		return err
	}

	if exists {
		slog.Info("Database already exists", "database", database.Name)
		if database.Owner != "" {
			return alterDatabaseOwner(ctx, p.adminDB, database.Name, database.Owner)
		}
		return nil
	}

	slog.Info("Creating database", "database", database.Name)
	return createDatabase(ctx, p.adminDB, database)
}

func databaseExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", name,
	).Scan(&exists)
	return exists, err
}

func createDatabase(ctx context.Context, db *sql.DB, database config.Database) error {
	query := "CREATE DATABASE " + quoteIdentifier(database.Name)
	if database.Owner != "" {
		query += " OWNER " + quoteIdentifier(database.Owner)
	}
	_, err := db.ExecContext(ctx, query)
	return err
}

func alterDatabaseOwner(ctx context.Context, db *sql.DB, dbName, owner string) error {
	query := "ALTER DATABASE " + quoteIdentifier(dbName) + " OWNER TO " + quoteIdentifier(owner)
	_, err := db.ExecContext(ctx, query)
	return err
}
