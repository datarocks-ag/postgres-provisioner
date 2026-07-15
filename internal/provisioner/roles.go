package provisioner

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"postgres-provisioner/internal/config"
)

func (p *Provisioner) ensureRole(ctx context.Context, role config.Role, strategy string) error {
	exists, err := roleExists(ctx, p.adminDB, role.Name)
	if err != nil {
		return err
	}

	if exists {
		if strategy == "create" {
			slog.Info("Skipping existing role (strategy=create)", "role", role.Name)
			return nil
		}
		slog.Info("Role already exists, updating", "role", role.Name)
		return p.alterRole(ctx, role)
	}

	slog.Info("Creating role", "role", role.Name)
	return p.createRole(ctx, role)
}

func roleExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)", name,
	).Scan(&exists)
	return exists, err
}

func (p *Provisioner) createRole(ctx context.Context, role config.Role) error {
	var parts []string
	parts = append(parts, "CREATE ROLE "+quoteIdentifier(role.Name))

	if role.Password != "" {
		parts = append(parts, "WITH PASSWORD "+quoteLiteral(role.Password))
	}

	parts = append(parts, roleOptionsClauses(role.Options)...)

	query := strings.Join(parts, " ")
	_, err := p.execMutation(ctx, p.adminDB, query)
	return err
}

func (p *Provisioner) alterRole(ctx context.Context, role config.Role) error {
	var clauses []string

	if role.Password != "" {
		clauses = append(clauses, "PASSWORD "+quoteLiteral(role.Password))
	}

	clauses = append(clauses, roleOptionsClauses(role.Options)...)

	if len(clauses) == 0 {
		slog.Info("No changes to apply for role", "role", role.Name)
		return nil
	}

	query := "ALTER ROLE " + quoteIdentifier(role.Name) + " WITH " + strings.Join(clauses, " ")
	_, err := p.execMutation(ctx, p.adminDB, query)
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

	if opts.Inherit != nil {
		if *opts.Inherit {
			clauses = append(clauses, "INHERIT")
		} else {
			clauses = append(clauses, "NOINHERIT")
		}
	}

	if opts.Replication != nil {
		if *opts.Replication {
			clauses = append(clauses, "REPLICATION")
		} else {
			clauses = append(clauses, "NOREPLICATION")
		}
	}

	if opts.BypassRLS != nil {
		if *opts.BypassRLS {
			clauses = append(clauses, "BYPASSRLS")
		} else {
			clauses = append(clauses, "NOBYPASSRLS")
		}
	}

	if opts.ConnectionLimit != nil {
		clauses = append(clauses, fmt.Sprintf("CONNECTION LIMIT %d", *opts.ConnectionLimit))
	}

	if opts.ValidUntil != nil {
		clauses = append(clauses, "VALID UNTIL "+quoteLiteral(*opts.ValidUntil))
	}

	return clauses
}
