package provisioner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"postgres-provisioner/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMigrationFilePattern(t *testing.T) {
	tests := []struct {
		name    string
		matches bool
		prefix  string
		version string
		desc    string
	}{
		{"V0001__create_users.sql", true, "V", "0001", "create_users"},
		{"R0001__seed_data.sql", true, "R", "0001", "seed_data"},
		{"V123__multi_word_desc.sql", true, "V", "123", "multi_word_desc"},
		{"V0__init.sql", true, "V", "0", "init"},
		{"not_a_migration.sql", false, "", "", ""},
		{"V__no_version.sql", false, "", "", ""},
		{"V0001_single_underscore.sql", false, "", "", ""},
		{"V0001__.sql", false, "", "", ""},
		{"README.md", false, "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := migrationFilePattern.FindStringSubmatch(tt.name)
			if tt.matches && matches == nil {
				t.Errorf("expected %q to match, but it didn't", tt.name)
			}
			if !tt.matches && matches != nil {
				t.Errorf("expected %q not to match, but it did: %v", tt.name, matches)
			}
			if tt.matches && matches != nil {
				if matches[1] != tt.prefix {
					t.Errorf("prefix: got %q, want %q", matches[1], tt.prefix)
				}
				if matches[2] != tt.version {
					t.Errorf("version: got %q, want %q", matches[2], tt.version)
				}
				if matches[3] != tt.desc {
					t.Errorf("description: got %q, want %q", matches[3], tt.desc)
				}
			}
		})
	}
}

func TestDiscoverMigrations(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "V0002__add_index.sql", "CREATE INDEX idx_name ON users(name);")
	writeFile(t, dir, "V0001__create_users.sql", "CREATE TABLE users (id int);")
	writeFile(t, dir, "R0001__seed_data.sql", "INSERT INTO users VALUES (1);")
	writeFile(t, dir, "README.md", "ignore me")

	files, err := discoverMigrations(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}

	// Versioned first (sorted), then repeatable
	if files[0].Filename != "V0001__create_users.sql" {
		t.Errorf("first file: got %q, want V0001__create_users.sql", files[0].Filename)
	}
	if files[0].Type != MigrationVersioned {
		t.Errorf("first file type: got %q, want versioned", files[0].Type)
	}
	if files[1].Filename != "V0002__add_index.sql" {
		t.Errorf("second file: got %q, want V0002__add_index.sql", files[1].Filename)
	}
	if files[2].Filename != "R0001__seed_data.sql" {
		t.Errorf("third file: got %q, want R0001__seed_data.sql", files[2].Filename)
	}
	if files[2].Type != MigrationRepeatable {
		t.Errorf("third file type: got %q, want repeatable", files[2].Type)
	}
}

func TestDiscoverMigrationsEmpty(t *testing.T) {
	dir := t.TempDir()
	files, err := discoverMigrations(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestDiscoverMigrationsDirNotExist(t *testing.T) {
	_, err := discoverMigrations("/nonexistent/dir")
	if err == nil {
		t.Fatal("expected error for non-existent directory")
	}
}

func TestDiscoverMigrationsDuplicateVersioned(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "V0001__first.sql", "SELECT 1;")
	writeFile(t, dir, "V0001__second.sql", "SELECT 2;")

	_, err := discoverMigrations(dir)
	if err == nil {
		t.Fatal("expected error for duplicate versioned versions")
	}
}

func TestDiscoverMigrationsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "V0001__empty.sql", "")

	_, err := discoverMigrations(dir)
	if err == nil {
		t.Fatal("expected error for empty migration file")
	}
}

func TestDiscoverMigrationsChecksum(t *testing.T) {
	dir := t.TempDir()
	content := "CREATE TABLE users (id int);"
	writeFile(t, dir, "V0001__create.sql", content)

	files, err := discoverMigrations(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hash := sha256.Sum256([]byte(content))
	expected := hex.EncodeToString(hash[:])
	if files[0].Checksum != expected {
		t.Errorf("checksum: got %q, want %q", files[0].Checksum, expected)
	}
}

func TestExecuteMigrationInsert(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mf := MigrationFile{
		Type:        MigrationVersioned,
		Version:     "0001",
		Description: "create_users",
		Filename:    "V0001__create_users.sql",
		Content:     "CREATE TABLE users (id int);",
		Checksum:    "abc123",
	}

	mock.ExpectBegin()
	mock.ExpectExec("CREATE TABLE users").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO _schema_migrations").
		WithArgs("0001", "versioned", "create_users", "V0001__create_users.sql", "abc123").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := executeMigration(context.Background(), mockDB, mf, false); err != nil {
		t.Fatalf("executeMigration: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteMigrationUpdate(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mf := MigrationFile{
		Type:        MigrationRepeatable,
		Version:     "0001",
		Description: "seed_data",
		Filename:    "R0001__seed_data.sql",
		Content:     "INSERT INTO users VALUES (1);",
		Checksum:    "def456",
	}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO users").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE _schema_migrations").
		WithArgs("def456", "R0001__seed_data.sql", "seed_data", "0001", "repeatable").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := executeMigration(context.Background(), mockDB, mf, true); err != nil {
		t.Fatalf("executeMigration: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureMigrationTable(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectExec("CREATE TABLE IF NOT EXISTS _schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := ensureMigrationTable(context.Background(), mockDB); err != nil {
		t.Fatalf("ensureMigrationTable: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAppliedMigrations(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	rows := sqlmock.NewRows([]string{"version", "type", "checksum"}).
		AddRow("0001", "versioned", "abc123").
		AddRow("0001", "repeatable", "def456")

	mock.ExpectQuery("SELECT version, type, checksum FROM _schema_migrations").
		WillReturnRows(rows)

	applied, err := loadAppliedMigrations(context.Background(), mockDB)
	if err != nil {
		t.Fatalf("loadAppliedMigrations: %v", err)
	}

	if len(applied) != 2 {
		t.Fatalf("expected 2 applied, got %d", len(applied))
	}

	rec, ok := applied["0001:versioned"]
	if !ok {
		t.Fatal("missing key 0001:versioned")
	}
	if rec.Checksum != "abc123" {
		t.Errorf("checksum: got %q, want abc123", rec.Checksum)
	}

	rec, ok = applied["0001:repeatable"]
	if !ok {
		t.Fatal("missing key 0001:repeatable")
	}
	if rec.Checksum != "def456" {
		t.Errorf("checksum: got %q, want def456", rec.Checksum)
	}
}

func TestDiscoverMigrationsDuplicateRepeatable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "R0001__first.sql", "SELECT 1;")
	writeFile(t, dir, "R0001__second.sql", "SELECT 2;")

	_, err := discoverMigrations(dir)
	if err == nil {
		t.Fatal("expected error for duplicate repeatable versions")
	}
}

func TestDiscoverMigrationsSkipsDirectories(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "V0001__create.sql", "CREATE TABLE t (id int);")
	if err := os.Mkdir(filepath.Join(dir, "V0002__subdir.sql"), 0755); err != nil {
		t.Fatal(err)
	}

	files, err := discoverMigrations(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
}

func TestRunMigrationsNoFiles(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()

	p := &Provisioner{opts: DefaultOptions()}
	err = p.runMigrations(context.Background(), mockDB, "testdb", config.Migrations{Directory: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunMigrationsDiscoverError(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	p := &Provisioner{opts: DefaultOptions()}
	err = p.runMigrations(context.Background(), mockDB, "testdb", config.Migrations{Directory: "/nonexistent"})
	if err == nil {
		t.Fatal("expected error for non-existent directory")
	}
}

func TestRunMigrationsAllApplied(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()
	content := "CREATE TABLE users (id int);"
	writeFile(t, dir, "V0001__create_users.sql", content)

	hash := sha256.Sum256([]byte(content))
	checksum := hex.EncodeToString(hash[:])

	// ensureMigrationTable
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS _schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))

	// loadAppliedMigrations returns the same migration with matching checksum
	rows := sqlmock.NewRows([]string{"version", "type", "checksum"}).
		AddRow("0001", "versioned", checksum)
	mock.ExpectQuery("SELECT version, type, checksum FROM _schema_migrations").
		WillReturnRows(rows)

	// No execution expected — already applied with same checksum

	p := &Provisioner{opts: DefaultOptions()}
	if err := p.runMigrations(context.Background(), mockDB, "testdb", config.Migrations{Directory: dir}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunMigrationsVersionedChecksumMismatch(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()
	writeFile(t, dir, "V0001__create_users.sql", "CREATE TABLE users (id int);")

	// ensureMigrationTable
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS _schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))

	// loadAppliedMigrations returns the migration with a DIFFERENT checksum
	rows := sqlmock.NewRows([]string{"version", "type", "checksum"}).
		AddRow("0001", "versioned", "old_checksum_that_doesnt_match")
	mock.ExpectQuery("SELECT version, type, checksum FROM _schema_migrations").
		WillReturnRows(rows)

	p := &Provisioner{opts: DefaultOptions()}
	err = p.runMigrations(context.Background(), mockDB, "testdb", config.Migrations{Directory: dir})
	if err == nil {
		t.Fatal("expected error for versioned checksum mismatch")
	}
	if !contains(err.Error(), "checksum mismatch") {
		t.Errorf("expected 'checksum mismatch' in error, got: %v", err)
	}
}

func TestRunMigrationsRepeatableChecksumChange(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()
	content := "INSERT INTO users VALUES (1), (2);"
	writeFile(t, dir, "R0001__seed.sql", content)

	hash := sha256.Sum256([]byte(content))
	newChecksum := hex.EncodeToString(hash[:])

	// ensureMigrationTable
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS _schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))

	// loadAppliedMigrations returns old checksum
	rows := sqlmock.NewRows([]string{"version", "type", "checksum"}).
		AddRow("0001", "repeatable", "old_checksum")
	mock.ExpectQuery("SELECT version, type, checksum FROM _schema_migrations").
		WillReturnRows(rows)

	// executeMigration with isUpdate=true
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO users").WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("UPDATE _schema_migrations").
		WithArgs(newChecksum, "R0001__seed.sql", "seed", "0001", "repeatable").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	p := &Provisioner{opts: DefaultOptions()}
	if err := p.runMigrations(context.Background(), mockDB, "testdb", config.Migrations{Directory: dir}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunMigrationsNewMigration(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()
	content := "CREATE TABLE orders (id int);"
	writeFile(t, dir, "V0001__create_orders.sql", content)

	hash := sha256.Sum256([]byte(content))
	checksum := hex.EncodeToString(hash[:])

	// ensureMigrationTable
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS _schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))

	// loadAppliedMigrations returns empty
	mock.ExpectQuery("SELECT version, type, checksum FROM _schema_migrations").
		WillReturnRows(sqlmock.NewRows([]string{"version", "type", "checksum"}))

	// executeMigration with isUpdate=false
	mock.ExpectBegin()
	mock.ExpectExec("CREATE TABLE orders").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO _schema_migrations").
		WithArgs("0001", "versioned", "create_orders", "V0001__create_orders.sql", checksum).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	p := &Provisioner{opts: DefaultOptions()}
	if err := p.runMigrations(context.Background(), mockDB, "testdb", config.Migrations{Directory: dir}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunMigrationsMultiple(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()
	content1 := "CREATE TABLE users (id int);"
	content2 := "CREATE TABLE orders (id int);"
	writeFile(t, dir, "V0001__create_users.sql", content1)
	writeFile(t, dir, "V0002__create_orders.sql", content2)

	hash1 := sha256.Sum256([]byte(content1))
	checksum1 := hex.EncodeToString(hash1[:])
	hash2 := sha256.Sum256([]byte(content2))
	checksum2 := hex.EncodeToString(hash2[:])

	// ensureMigrationTable
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS _schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))

	// V0001 already applied
	rows := sqlmock.NewRows([]string{"version", "type", "checksum"}).
		AddRow("0001", "versioned", checksum1)
	mock.ExpectQuery("SELECT version, type, checksum FROM _schema_migrations").
		WillReturnRows(rows)

	// Only V0002 should be executed
	mock.ExpectBegin()
	mock.ExpectExec("CREATE TABLE orders").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO _schema_migrations").
		WithArgs("0002", "versioned", "create_orders", "V0002__create_orders.sql", checksum2).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	p := &Provisioner{opts: DefaultOptions()}
	if err := p.runMigrations(context.Background(), mockDB, "testdb", config.Migrations{Directory: dir}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunMigrationsEnsureTableError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()
	writeFile(t, dir, "V0001__create.sql", "CREATE TABLE t (id int);")

	mock.ExpectExec("CREATE TABLE IF NOT EXISTS _schema_migrations").
		WillReturnError(fmt.Errorf("permission denied"))

	p := &Provisioner{opts: DefaultOptions()}
	err = p.runMigrations(context.Background(), mockDB, "testdb", config.Migrations{Directory: dir})
	if err == nil {
		t.Fatal("expected error for failed migration table creation")
	}
}

func TestRunMigrationsLoadAppliedError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()
	writeFile(t, dir, "V0001__create.sql", "CREATE TABLE t (id int);")

	mock.ExpectExec("CREATE TABLE IF NOT EXISTS _schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectQuery("SELECT version, type, checksum FROM _schema_migrations").
		WillReturnError(fmt.Errorf("query failed"))

	p := &Provisioner{opts: DefaultOptions()}
	err = p.runMigrations(context.Background(), mockDB, "testdb", config.Migrations{Directory: dir})
	if err == nil {
		t.Fatal("expected error for failed applied migrations query")
	}
}

func TestRunMigrationsExecutionError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()
	writeFile(t, dir, "V0001__bad.sql", "INVALID SQL;")

	mock.ExpectExec("CREATE TABLE IF NOT EXISTS _schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectQuery("SELECT version, type, checksum FROM _schema_migrations").
		WillReturnRows(sqlmock.NewRows([]string{"version", "type", "checksum"}))

	mock.ExpectBegin()
	mock.ExpectExec("INVALID SQL").WillReturnError(fmt.Errorf("syntax error"))
	mock.ExpectRollback()

	p := &Provisioner{opts: DefaultOptions()}
	err = p.runMigrations(context.Background(), mockDB, "testdb", config.Migrations{Directory: dir})
	if err == nil {
		t.Fatal("expected error for failed migration execution")
	}
}

func TestExecuteMigrationBeginError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectBegin().WillReturnError(fmt.Errorf("cannot begin"))

	mf := MigrationFile{
		Type:     MigrationVersioned,
		Version:  "0001",
		Filename: "V0001__test.sql",
		Content:  "SELECT 1;",
		Checksum: "abc",
	}

	err = executeMigration(context.Background(), mockDB, mf, false)
	if err == nil {
		t.Fatal("expected error for failed begin")
	}
}

func TestExecuteMigrationTrackingError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mf := MigrationFile{
		Type:        MigrationVersioned,
		Version:     "0001",
		Description: "test",
		Filename:    "V0001__test.sql",
		Content:     "SELECT 1;",
		Checksum:    "abc",
	}

	mock.ExpectBegin()
	mock.ExpectExec("SELECT 1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO _schema_migrations").
		WithArgs("0001", "versioned", "test", "V0001__test.sql", "abc").
		WillReturnError(fmt.Errorf("unique constraint violation"))
	mock.ExpectRollback()

	err = executeMigration(context.Background(), mockDB, mf, false)
	if err == nil {
		t.Fatal("expected error for failed tracking insert")
	}
}

func TestRunMigrationsRepeatableReExecuteError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()
	writeFile(t, dir, "R0001__seed.sql", "INSERT INTO users VALUES (1);")

	mock.ExpectExec("CREATE TABLE IF NOT EXISTS _schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))

	rows := sqlmock.NewRows([]string{"version", "type", "checksum"}).
		AddRow("0001", "repeatable", "old_checksum")
	mock.ExpectQuery("SELECT version, type, checksum FROM _schema_migrations").
		WillReturnRows(rows)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO users").WillReturnError(fmt.Errorf("table does not exist"))
	mock.ExpectRollback()

	p := &Provisioner{opts: DefaultOptions()}
	err = p.runMigrations(context.Background(), mockDB, "testdb", config.Migrations{Directory: dir})
	if err == nil {
		t.Fatal("expected error for failed repeatable re-execution")
	}
}

func TestDiscoverMigrationsUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "V0001__unreadable.sql")
	if err := os.WriteFile(filePath, []byte("SELECT 1;"), 0644); err != nil {
		t.Fatal(err)
	}
	// Remove read permissions
	if err := os.Chmod(filePath, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filePath, 0644) })

	_, err := discoverMigrations(dir)
	if err == nil {
		t.Fatal("expected error for unreadable migration file")
	}
}

func TestLoadAppliedMigrationsScanError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	// Return rows with wrong number of columns to trigger scan error
	rows := sqlmock.NewRows([]string{"version", "type", "checksum"}).
		AddRow("0001", "versioned", nil) // nil checksum may cause scan issue depending on driver
	// Actually, a better way is to use RowError
	rows.RowError(0, fmt.Errorf("row error"))

	mock.ExpectQuery("SELECT version, type, checksum FROM _schema_migrations").
		WillReturnRows(rows)

	_, err = loadAppliedMigrations(context.Background(), mockDB)
	if err == nil {
		t.Fatal("expected error for rows error")
	}
}

func TestLoadAppliedMigrationsQueryError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery("SELECT version, type, checksum FROM _schema_migrations").
		WillReturnError(fmt.Errorf("query failed"))

	_, err = loadAppliedMigrations(context.Background(), mockDB)
	if err == nil {
		t.Fatal("expected error for failed query")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("writing file %q: %v", name, err)
	}
}
