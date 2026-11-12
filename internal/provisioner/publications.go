package provisioner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"postgres-provisioner/internal/config"
)

// publicationState is the current definition of a publication.
type publicationState struct {
	allTables bool
	publish   []string
	tables    map[string]bool // schema.table
}

// ensurePublication creates a publication or reconciles an existing one additively:
// a changed publish list is set, missing tables are added, and tables published but
// not configured are only warned about — dropping them could silently cut off a CDC
// consumer. FOR ALL TABLES can't be toggled with ALTER PUBLICATION, so a mismatch warns.
func (p *Provisioner) ensurePublication(ctx context.Context, dbConn *sql.DB, pub config.Publication, strategy string) error {
	state, err := currentPublication(ctx, dbConn, pub.Name)
	if err != nil {
		return err
	}

	tables, err := parseTables(pub.Tables)
	if err != nil {
		return err
	}

	if state == nil {
		if err := p.requireTables(ctx, dbConn, tables); err != nil {
			return err
		}
		slog.Info("Creating publication", "publication", pub.Name)
		_, err := p.execMutation(ctx, dbConn, createPublicationSQL(pub, tables))
		return err
	}

	if strategy == "create" {
		slog.Info("Skipping existing publication (strategy=create)", "publication", pub.Name)
		return nil
	}

	if state.allTables != pub.AllTables {
		slog.Warn("Publication all_tables differs from config and can't be altered; recreate it manually to change it",
			"publication", pub.Name, "installed_all_tables", state.allTables, "configured_all_tables", pub.AllTables)
	}

	desiredPublish := pub.EffectivePublish()
	if strings.Join(state.publish, ", ") != strings.Join(desiredPublish, ", ") {
		slog.Info("Updating publication operations", "publication", pub.Name,
			"from", strings.Join(state.publish, ", "), "to", strings.Join(desiredPublish, ", "))
		query := fmt.Sprintf("ALTER PUBLICATION %s SET (publish = %s)",
			quoteIdentifier(pub.Name), quoteLiteral(strings.Join(desiredPublish, ", ")))
		if _, err := p.execMutation(ctx, dbConn, query); err != nil {
			return err
		}
	}

	if state.allTables || pub.AllTables {
		return nil
	}
	return p.reconcilePublicationTables(ctx, dbConn, pub.Name, tables, state.tables)
}

func (p *Provisioner) reconcilePublicationTables(ctx context.Context, dbConn *sql.DB, name string, desired []config.QualifiedName, current map[string]bool) error {
	desiredSet := make(map[string]bool, len(desired))
	var toAdd []config.QualifiedName
	for _, table := range desired {
		desiredSet[table.String()] = true
		if !current[table.String()] {
			toAdd = append(toAdd, table)
		}
	}

	var extra []string
	for table := range current {
		if !desiredSet[table] {
			extra = append(extra, table)
		}
	}
	if len(extra) > 0 {
		slog.Warn("Publication contains tables not in config; leaving them (additive only)",
			"publication", name, "tables", strings.Join(extra, ", "))
	}

	if len(toAdd) == 0 {
		slog.Info("Publication tables up to date", "publication", name)
		return nil
	}
	if err := p.requireTables(ctx, dbConn, toAdd); err != nil {
		return err
	}
	slog.Info("Adding tables to publication", "publication", name, "tables", quoteQualifiedNames(toAdd))
	query := fmt.Sprintf("ALTER PUBLICATION %s ADD TABLE %s", quoteIdentifier(name), quoteQualifiedNames(toAdd))
	_, err := p.execMutation(ctx, dbConn, query)
	return err
}

// requireTables fails if any table doesn't exist. Publications run after migrations, so
// missing tables are a config error; in dry-run, where migrations aren't applied, they
// are only logged.
func (p *Provisioner) requireTables(ctx context.Context, dbConn *sql.DB, tables []config.QualifiedName) error {
	missing, err := missingTables(ctx, dbConn, tables)
	if err != nil {
		return fmt.Errorf("checking tables: %w", err)
	}
	if len(missing) == 0 {
		return nil
	}
	if p.opts.DryRun {
		slog.Info("[DRY RUN] tables not present yet; they may be created by pending migrations",
			"tables", strings.Join(missing, ", "))
		return nil
	}
	return fmt.Errorf("tables not found: %s", strings.Join(missing, ", "))
}

func createPublicationSQL(pub config.Publication, tables []config.QualifiedName) string {
	query := "CREATE PUBLICATION " + quoteIdentifier(pub.Name)
	if pub.AllTables {
		query += " FOR ALL TABLES"
	} else {
		query += " FOR TABLE " + quoteQualifiedNames(tables)
	}
	if len(pub.Publish) > 0 {
		query += " WITH (publish = " + quoteLiteral(strings.Join(pub.EffectivePublish(), ", ")) + ")"
	}
	return query
}

func parseTables(raw []string) ([]config.QualifiedName, error) {
	tables := make([]config.QualifiedName, 0, len(raw))
	for _, r := range raw {
		table, err := config.ParseQualifiedName(r)
		if err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	return tables, nil
}

// currentPublication returns the publication's definition, or nil if it doesn't exist.
func currentPublication(ctx context.Context, dbConn *sql.DB, name string) (*publicationState, error) {
	var allTables, insert, update, del, truncate bool
	err := dbConn.QueryRowContext(ctx,
		`SELECT puballtables, pubinsert, pubupdate, pubdelete, pubtruncate FROM pg_publication WHERE pubname = $1`,
		name,
	).Scan(&allTables, &insert, &update, &del, &truncate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading publication %q: %w", name, err)
	}

	state := &publicationState{allTables: allTables, tables: make(map[string]bool)}
	for i, enabled := range []bool{insert, update, del, truncate} {
		if enabled {
			state.publish = append(state.publish, config.PublishOperations[i])
		}
	}
	if allTables {
		return state, nil
	}

	rows, err := dbConn.QueryContext(ctx,
		`SELECT schemaname, tablename FROM pg_publication_tables WHERE pubname = $1`, name)
	if err != nil {
		return nil, fmt.Errorf("reading tables of publication %q: %w", name, err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table string
		if err := rows.Scan(&schema, &table); err != nil {
			return nil, err
		}
		state.tables[config.QualifiedName{Schema: schema, Name: table}.String()] = true
	}
	return state, rows.Err()
}

// warnIfWalLevelNotLogical warns when publications are configured but the server's
// wal_level isn't logical: the publication is created, but nothing can subscribe to it
// until wal_level=logical is set (requires a restart).
func (p *Provisioner) warnIfWalLevelNotLogical(ctx context.Context, dbConn *sql.DB, database config.Database) {
	var walLevel string
	if err := dbConn.QueryRowContext(ctx, "SHOW wal_level").Scan(&walLevel); err != nil {
		slog.Debug("Could not check wal_level", "database", database.Name, "error", err)
		return
	}
	if walLevel != "logical" {
		slog.Warn("Publications require wal_level=logical for logical replication; consumers can't subscribe until the server is restarted with it",
			"database", database.Name, "wal_level", walLevel, "hint", "start postgres with -c wal_level=logical")
	}
}
