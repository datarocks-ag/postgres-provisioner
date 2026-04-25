package provisioner

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"postgres-provisioner/internal/config"
)

// MigrationType distinguishes versioned (run-once) from repeatable (re-run on change) migrations.
type MigrationType string

const (
	MigrationVersioned  MigrationType = "versioned"
	MigrationRepeatable MigrationType = "repeatable"
)

// MigrationFile represents a discovered migration file.
type MigrationFile struct {
	Type        MigrationType
	Version     string
	Description string
	Filename    string
	Content     string
	Checksum    string
	SortOrder   int // numeric version for sorting
}

// migrationRecord represents a row in the _schema_migrations tracking table.
type migrationRecord struct {
	Version  string
	Type     string
	Checksum string
}

var migrationFilePattern = regexp.MustCompile(`^(V|R)(\d+)__(.+)\.sql$`)

const createMigrationTableSQL = `CREATE TABLE IF NOT EXISTS _schema_migrations (
    version     TEXT        NOT NULL,
    type        TEXT        NOT NULL CHECK (type IN ('versioned', 'repeatable')),
    description TEXT        NOT NULL,
    filename    TEXT        NOT NULL,
    checksum    TEXT        NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (version, type)
)`

// discoverMigrations reads a directory and returns parsed, sorted migration files.
func discoverMigrations(directory string) ([]MigrationFile, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("reading migration directory %q: %w", directory, err)
	}

	var versioned []MigrationFile
	var repeatable []MigrationFile

	seenVersioned := make(map[int]string) // numeric version -> filename
	seenRepeatable := make(map[int]string)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		matches := migrationFilePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}

		prefix := matches[1]
		version := matches[2]
		description := matches[3]

		content, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading migration file %q: %w", entry.Name(), err)
		}

		if len(content) == 0 {
			return nil, fmt.Errorf("migration file %q is empty", entry.Name())
		}

		hash := sha256.Sum256(content)
		checksum := hex.EncodeToString(hash[:])

		mf := MigrationFile{
			Version:     version,
			Description: description,
			Filename:    entry.Name(),
			Content:     string(content),
			Checksum:    checksum,
		}

		sortOrder, err := strconv.Atoi(version)
		if err != nil {
			return nil, fmt.Errorf("migration file %q: version %q is not a valid integer: %w", entry.Name(), version, err)
		}
		mf.SortOrder = sortOrder

		switch prefix {
		case "V":
			mf.Type = MigrationVersioned
			if prev, ok := seenVersioned[sortOrder]; ok {
				return nil, fmt.Errorf("duplicate versioned migration version %d: %q and %q", sortOrder, prev, entry.Name())
			}
			seenVersioned[sortOrder] = entry.Name()
			versioned = append(versioned, mf)
		case "R":
			mf.Type = MigrationRepeatable
			if prev, ok := seenRepeatable[sortOrder]; ok {
				return nil, fmt.Errorf("duplicate repeatable migration version %d: %q and %q", sortOrder, prev, entry.Name())
			}
			seenRepeatable[sortOrder] = entry.Name()
			repeatable = append(repeatable, mf)
		}
	}

	sort.Slice(versioned, func(i, j int) bool {
		if versioned[i].SortOrder != versioned[j].SortOrder {
			return versioned[i].SortOrder < versioned[j].SortOrder
		}
		return versioned[i].Filename < versioned[j].Filename
	})
	sort.Slice(repeatable, func(i, j int) bool {
		if repeatable[i].SortOrder != repeatable[j].SortOrder {
			return repeatable[i].SortOrder < repeatable[j].SortOrder
		}
		return repeatable[i].Filename < repeatable[j].Filename
	})

	// All versioned first, then all repeatable
	return append(versioned, repeatable...), nil
}

// ensureMigrationTable creates the tracking table if it doesn't exist.
func ensureMigrationTable(ctx context.Context, dbConn *sql.DB) error {
	_, err := dbConn.ExecContext(ctx, createMigrationTableSQL)
	return err
}

// loadAppliedMigrations queries the tracking table and returns a map keyed by "version:type".
func loadAppliedMigrations(ctx context.Context, dbConn *sql.DB) (map[string]migrationRecord, error) {
	rows, err := dbConn.QueryContext(ctx, "SELECT version, type, checksum FROM _schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := make(map[string]migrationRecord)
	for rows.Next() {
		var rec migrationRecord
		if err := rows.Scan(&rec.Version, &rec.Type, &rec.Checksum); err != nil {
			return nil, err
		}
		key := rec.Version + ":" + rec.Type
		applied[key] = rec
	}
	return applied, rows.Err()
}

// runMigrations orchestrates migration discovery and execution for a single database.
//
// In dry-run mode, the tracking table is not created and migrations are not executed;
// instead, each discovered migration is logged with its planned action (apply,
// re-execute, or skip). If the tracking table is missing, all migrations are reported
// as "would apply".
func (p *Provisioner) runMigrations(ctx context.Context, dbConn *sql.DB, dbName string, migrations config.Migrations) error {
	slog.Info("Running migrations", "database", dbName, "directory", migrations.Directory)

	files, err := discoverMigrations(migrations.Directory)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		slog.Info("No migration files found", "database", dbName, "directory", migrations.Directory)
		return nil
	}

	if p.opts.DryRun {
		return p.dryRunMigrations(ctx, dbConn, dbName, files)
	}

	if err := ensureMigrationTable(ctx, dbConn); err != nil {
		return fmt.Errorf("creating migration tracking table: %w", err)
	}

	applied, err := loadAppliedMigrations(ctx, dbConn)
	if err != nil {
		return fmt.Errorf("loading applied migrations: %w", err)
	}

	for _, mf := range files {
		key := mf.Version + ":" + string(mf.Type)
		rec, alreadyApplied := applied[key]

		if alreadyApplied {
			if rec.Checksum == mf.Checksum {
				slog.Debug("Skipping already-applied migration", "file", mf.Filename, "database", dbName)
				continue
			}

			// Checksum mismatch
			if mf.Type == MigrationVersioned {
				return fmt.Errorf("checksum mismatch for versioned migration %q in database %q: expected %s, got %s (versioned migrations are immutable)",
					mf.Filename, dbName, rec.Checksum, mf.Checksum)
			}

			// Repeatable: re-execute and update
			slog.Info("Re-executing repeatable migration (content changed)", "file", mf.Filename, "database", dbName)
			if err := executeMigration(ctx, dbConn, mf, true); err != nil {
				return fmt.Errorf("re-executing migration %q: %w", mf.Filename, err)
			}
			continue
		}

		// Not yet applied
		slog.Info("Applying migration", "file", mf.Filename, "type", mf.Type, "database", dbName)
		if err := executeMigration(ctx, dbConn, mf, false); err != nil {
			return fmt.Errorf("executing migration %q: %w", mf.Filename, err)
		}
	}

	slog.Info("Migrations complete", "database", dbName, "total_files", len(files))
	return nil
}

// dryRunMigrations reports the planned migration actions without executing or
// modifying the tracking table. A missing tracking table is treated as zero
// applied migrations rather than an error, so a dry-run against a fresh
// database surfaces the full plan.
func (p *Provisioner) dryRunMigrations(ctx context.Context, dbConn *sql.DB, dbName string, files []MigrationFile) error {
	slog.Info("[DRY RUN] would create migration tracking table if missing", "sql", createMigrationTableSQL)

	applied, err := loadAppliedMigrations(ctx, dbConn)
	if err != nil {
		// Most likely the table doesn't exist yet — treat as zero applied.
		slog.Info("[DRY RUN] tracking table not readable; assuming all migrations would be applied",
			"database", dbName, "reason", err.Error())
		applied = map[string]migrationRecord{}
	}

	for _, mf := range files {
		key := mf.Version + ":" + string(mf.Type)
		rec, alreadyApplied := applied[key]

		switch {
		case !alreadyApplied:
			slog.Info("[DRY RUN] would apply migration",
				"file", mf.Filename, "type", mf.Type, "database", dbName)
		case rec.Checksum == mf.Checksum:
			slog.Debug("[DRY RUN] migration already applied — would skip",
				"file", mf.Filename, "database", dbName)
		case mf.Type == MigrationVersioned:
			return fmt.Errorf("checksum mismatch for versioned migration %q in database %q: expected %s, got %s (versioned migrations are immutable)",
				mf.Filename, dbName, rec.Checksum, mf.Checksum)
		default:
			slog.Info("[DRY RUN] would re-execute repeatable migration (content changed)",
				"file", mf.Filename, "database", dbName)
		}
	}

	slog.Info("[DRY RUN] migration plan complete", "database", dbName, "total_files", len(files))
	return nil
}

// executeMigration runs a single migration file inside a transaction, including the tracking record.
func executeMigration(ctx context.Context, dbConn *sql.DB, mf MigrationFile, isUpdate bool) error {
	tx, err := dbConn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, mf.Content); err != nil {
		return fmt.Errorf("executing SQL: %w", err)
	}

	if isUpdate {
		_, err = tx.ExecContext(ctx,
			"UPDATE _schema_migrations SET checksum = $1, filename = $2, description = $3, applied_at = now() WHERE version = $4 AND type = $5",
			mf.Checksum, mf.Filename, mf.Description, mf.Version, string(mf.Type))
	} else {
		_, err = tx.ExecContext(ctx,
			"INSERT INTO _schema_migrations (version, type, description, filename, checksum) VALUES ($1, $2, $3, $4, $5)",
			mf.Version, string(mf.Type), mf.Description, mf.Filename, mf.Checksum)
	}
	if err != nil {
		return fmt.Errorf("updating tracking table: %w", err)
	}

	return tx.Commit()
}
