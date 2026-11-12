package provisioner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lib/pq"

	"postgres-provisioner/internal/config"
)

// pgDependentObjectsStillExist is the SQLSTATE of "dependent privileges exist", returned
// when REVOKE without CASCADE would orphan grants the grantee made WITH GRANT OPTION.
const pgDependentObjectsStillExist = "2BP01"

// aclQueries reads an object's existence and the privileges a grantee holds on it.
// COALESCE with acldefault() makes the implicit defaults visible (e.g. a new database
// grants CONNECT and TEMPORARY to PUBLIC while datacl is still NULL).
var aclQueries = map[config.GrantTarget]struct{ exists, privileges string }{
	config.TargetDatabase: {
		exists: `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`,
		privileges: `SELECT a.privilege_type
			FROM pg_database o
			CROSS JOIN LATERAL aclexplode(COALESCE(o.datacl, acldefault('d', o.datdba))) a
			WHERE o.datname = $1 AND a.grantee = $2::oid`,
	},
	config.TargetSchema: {
		exists: `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname = $1)`,
		privileges: `SELECT a.privilege_type
			FROM pg_namespace o
			CROSS JOIN LATERAL aclexplode(COALESCE(o.nspacl, acldefault('n', o.nspowner))) a
			WHERE o.nspname = $1 AND a.grantee = $2::oid`,
	},
}

// applyRevoke revokes the listed privileges that the grantee currently holds. Privileges
// that aren't held are skipped, so repeated runs are no-ops.
func (p *Provisioner) applyRevoke(ctx context.Context, dbConn *sql.DB, database config.Database, revoke config.Grant) error {
	targets := revoke.Targets()
	if len(targets) != 1 {
		return fmt.Errorf("revoke must have exactly one target")
	}
	target := targets[0]

	var objectSQL, objectName string
	switch target {
	case config.TargetDatabase:
		objectName = database.Name
		objectSQL = "DATABASE " + quoteIdentifier(objectName)
	case config.TargetSchema:
		objectName = revoke.OnSchema
		objectSQL = "SCHEMA " + quoteIdentifier(objectName)
	default:
		return fmt.Errorf("%s is not supported for revokes", target)
	}

	requested := config.ExpandPrivileges(target, revoke.Privileges)
	held, exists, err := heldPrivileges(ctx, dbConn, target, objectName, revoke.Role)
	if err != nil {
		return err
	}

	if !exists {
		// In dry-run, an object declared in the config would have been created earlier in
		// this run, so preview against the privileges a freshly created object has.
		var created bool
		held, created = p.newObjectPrivileges(database, target, objectName, revoke.Role)
		if !p.opts.DryRun || !created {
			return fmt.Errorf("%s does not exist", strings.ToLower(objectSQL))
		}
	}

	toRevoke := intersect(requested, held)
	if len(toRevoke) == 0 {
		msg := "Privileges already revoked, skipping"
		if !exists {
			msg = "[DRY RUN] privileges would not be held on the newly created object, skipping"
		}
		slog.Info(msg, "role", revoke.Role, "on", strings.ToLower(objectSQL), "privileges", strings.Join(requested, ", "))
		return nil
	}

	privs := strings.Join(toRevoke, ", ")
	slog.Info("Revoking privileges", "role", revoke.Role, "on", strings.ToLower(objectSQL), "privileges", privs)
	query := fmt.Sprintf("REVOKE %s ON %s FROM %s", privs, objectSQL, granteeSQL(revoke.Role))
	if _, err := p.execMutation(ctx, dbConn, query); err != nil {
		return explainRevokeError(err)
	}
	if p.opts.DryRun {
		return nil
	}

	// REVOKE only removes privileges granted by the current role (or the owner, for a
	// superuser), so a grant made by another grantor survives silently. Surface it.
	stillHeld, _, err := heldPrivileges(ctx, dbConn, target, objectName, revoke.Role)
	if err != nil {
		return fmt.Errorf("verifying revoke: %w", err)
	}
	if remaining := intersect(toRevoke, stillHeld); len(remaining) > 0 {
		slog.Warn("Privileges still held after REVOKE, likely granted by a different grantor",
			"role", revoke.Role, "on", strings.ToLower(objectSQL), "privileges", strings.Join(remaining, ", "))
	}
	return nil
}

// newObjectPrivileges returns the privileges role would hold on an object this run
// creates, and whether the run creates it at all (the database itself, or a schema
// declared in schemas). A new object's ACL is acldefault(): a database grants everything
// to its owner plus CONNECT and TEMPORARY to PUBLIC, a schema grants everything to its
// owner and nothing to PUBLIC. Without a configured owner, the provisioning user owns it.
func (p *Provisioner) newObjectPrivileges(database config.Database, target config.GrantTarget, objectName, role string) (map[string]bool, bool) {
	var owner string
	switch target {
	case config.TargetDatabase:
		owner = database.Owner
	case config.TargetSchema:
		if !declaresSchema(database, objectName) {
			return nil, false
		}
		owner = schemaOwnerFor(database, objectName)
	default:
		return nil, false
	}
	if owner == "" {
		owner = p.connCfg.User
	}

	held := make(map[string]bool)
	if role == owner {
		for _, privilege := range config.ExpandPrivileges(target, []string{"ALL"}) {
			held[privilege] = true
		}
	}
	if target == config.TargetDatabase && config.IsPublic(role) {
		held["CONNECT"] = true
		held["TEMPORARY"] = true
	}
	return held, true
}

func declaresSchema(database config.Database, name string) bool {
	for _, s := range database.Schemas {
		if s.Name == name {
			return true
		}
	}
	return false
}

// explainRevokeError adds a hint to "dependent privileges exist". The provisioner never
// revokes with CASCADE, because that silently removes grants other roles depend on.
func explainRevokeError(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == pgDependentObjectsStillExist {
		return fmt.Errorf("%w (the grantee holds the privilege WITH GRANT OPTION and granted it to other roles; "+
			"revoke those grants first, or run REVOKE ... CASCADE manually: the provisioner never cascades)", err)
	}
	return err
}

// heldPrivileges returns the privileges role directly holds on the object (PUBLIC means
// the PUBLIC pseudo-role, grantee oid 0), and whether the object exists. A role that
// doesn't exist yet holds nothing.
func heldPrivileges(ctx context.Context, dbConn *sql.DB, target config.GrantTarget, objectName, role string) (map[string]bool, bool, error) {
	queries, ok := aclQueries[target]
	if !ok {
		return nil, false, fmt.Errorf("no privilege lookup for %s", target)
	}

	var exists bool
	if err := dbConn.QueryRowContext(ctx, queries.exists, objectName).Scan(&exists); err != nil {
		return nil, false, fmt.Errorf("checking existence: %w", err)
	}
	if !exists {
		return nil, false, nil
	}

	var granteeOID int64 // 0 is PUBLIC
	if !config.IsPublic(role) {
		err := dbConn.QueryRowContext(ctx, `SELECT oid FROM pg_roles WHERE rolname = $1`, role).Scan(&granteeOID)
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]bool{}, true, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("looking up role %q: %w", role, err)
		}
	}

	rows, err := dbConn.QueryContext(ctx, queries.privileges, objectName, granteeOID)
	if err != nil {
		return nil, false, fmt.Errorf("reading privileges: %w", err)
	}
	defer rows.Close()

	held := make(map[string]bool)
	for rows.Next() {
		var privilege string
		if err := rows.Scan(&privilege); err != nil {
			return nil, false, fmt.Errorf("reading privileges: %w", err)
		}
		held[privilege] = true
	}
	return held, true, rows.Err()
}

func intersect(privileges []string, set map[string]bool) []string {
	var result []string
	for _, p := range privileges {
		if set[p] {
			result = append(result, p)
		}
	}
	return result
}
