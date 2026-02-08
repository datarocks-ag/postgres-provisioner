package provisioner

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"postgres-provisioner/internal/config"
)

func (p *Provisioner) ensureRole(ctx context.Context, role config.Role) error {
	exists, err := roleExists(ctx, p.adminDB, role.Name)
	if err != nil {
		return err
	}

	if exists {
		slog.Info("Role already exists, updating", "role", role.Name)
		return alterRole(ctx, p.adminDB, role)
	}

	slog.Info("Creating role", "role", role.Name)
	return createRole(ctx, p.adminDB, role)
}

func roleExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)", name,
	).Scan(&exists)
	return exists, err
}

func createRole(ctx context.Context, db *sql.DB, role config.Role) error {
	var parts []string
	parts = append(parts, "CREATE ROLE "+quoteIdentifier(role.Name))

	if role.Password != "" {
		parts = append(parts, "WITH PASSWORD "+quoteLiteral(role.Password))
	}

	parts = append(parts, roleOptionsClauses(role.Options)...)

	query := strings.Join(parts, " ")
	_, err := db.ExecContext(ctx, query)
	return err
}

func alterRole(ctx context.Context, db *sql.DB, role config.Role) error {
	var parts []string
	parts = append(parts, "ALTER ROLE "+quoteIdentifier(role.Name)+" WITH")

	if role.Password != "" {
		parts = append(parts, "PASSWORD "+quoteLiteral(role.Password))
	}

	parts = append(parts, roleOptionsClauses(role.Options)...)

	query := strings.Join(parts, " ")
	_, err := db.ExecContext(ctx, query)
	return err
}

func roleOptionsClauses(opts config.RoleOptions) []string {
	var clauses []string

	if opts.Login != nil {
		if *opts.Login {
			clauses = append(clauses, "LOGIN")
		} else {
			clauses = append(clauses, "NOLOGIN")
		}
	}

	if opts.Superuser != nil {
		if *opts.Superuser {
			clauses = append(clauses, "SUPERUSER")
		} else {
			clauses = append(clauses, "NOSUPERUSER")
		}
	}

	if opts.CreateDB != nil {
		if *opts.CreateDB {
			clauses = append(clauses, "CREATEDB")
		} else {
			clauses = append(clauses, "NOCREATEDB")
		}
	}

	if opts.CreateRole != nil {
		if *opts.CreateRole {
			clauses = append(clauses, "CREATEROLE")
		} else {
			clauses = append(clauses, "NOCREATEROLE")
		}
	}

	if opts.ConnectionLimit != nil {
		clauses = append(clauses, fmt.Sprintf("CONNECTION LIMIT %d", *opts.ConnectionLimit))
	}

	return clauses
}
