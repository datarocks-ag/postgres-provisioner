//go:build integration

package provisioner_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"postgres-provisioner/internal/config"
	"postgres-provisioner/internal/db"
	"postgres-provisioner/internal/provisioner"
)

func setupPostgres(t *testing.T) (*sql.DB, db.ConnConfig, func()) {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("admin"),
		postgres.WithPassword("adminpass"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get connection string: %v", err)
	}

	adminDB, err := sql.Open("postgres", connStr)
	if err != nil {
		t.Fatalf("failed to open connection: %v", err)
	}

	if err := adminDB.Ping(); err != nil {
		t.Fatalf("failed to ping: %v", err)
	}

	host, err := pgContainer.Host(ctx)
	if err != nil {
		t.Fatalf("failed to get host: %v", err)
	}

	mappedPort, err := pgContainer.MappedPort(ctx, "5432")
	if err != nil {
		t.Fatalf("failed to get mapped port: %v", err)
	}

	connCfg := db.ConnConfig{
		User:     "admin",
		Password: "adminpass",
		Host:     host,
		Port:     mappedPort.Port(),
		DBName:   "postgres",
		SSLMode:  "disable",
	}

	cleanup := func() {
		adminDB.Close()
		if err := testcontainers.TerminateContainer(pgContainer); err != nil {
			log.Printf("failed to terminate container: %v", err)
		}
	}

	return adminDB, connCfg, cleanup
}

func writeTestConfig(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIntegrationFullProvisioning(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	configYAML := `
roles:
  - name: "app_user"
    password: "appsecret"
    options:
      login: true
      superuser: false
      createdb: false

  - name: "readonly_user"
    password: "readsecret"
    options:
      login: true

databases:
  - name: "myapp"
    owner: "app_user"
    extensions: ["pgcrypto"]
    schemas:
      - name: "app"
        owner: "app_user"
    grants:
      - role: "app_user"
        privileges: ["ALL"]
        on_schema: "app"
      - role: "readonly_user"
        privileges: ["CONNECT"]
        on_database: true
      - role: "readonly_user"
        privileges: ["SELECT"]
        on_tables_in_schema: "app"
`
	cfgPath := writeTestConfig(t, configYAML)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	ctx := context.Background()
	p := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())

	// First run
	if err := p.Run(ctx); err != nil {
		t.Fatalf("first provisioning run failed: %v", err)
	}

	// Verify roles exist
	assertRoleExists(t, adminDB, "app_user")
	assertRoleExists(t, adminDB, "readonly_user")

	// Verify database exists
	assertDatabaseExists(t, adminDB, "myapp")

	// Connect to the new database and verify schema + extension
	appDB, err := db.ConnectToDatabase(ctx, connCfg, "myapp")
	if err != nil {
		t.Fatalf("failed to connect to myapp: %v", err)
	}
	defer appDB.Close()

	assertSchemaExists(t, appDB, "app")
	assertExtensionExists(t, appDB, "pgcrypto")

	// Second run should be idempotent (no errors)
	p2 := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	if err := p2.Run(ctx); err != nil {
		t.Fatalf("second (idempotent) provisioning run failed: %v", err)
	}
}

func TestIntegrationIdempotentRoleUpdate(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	configYAML := `
roles:
  - name: "test_role"
    password: "pass1"
    options:
      login: true
`
	cfgPath := writeTestConfig(t, configYAML)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())

	if err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}
	assertRoleExists(t, adminDB, "test_role")

	// Update password
	configYAML2 := `
roles:
  - name: "test_role"
    password: "pass2"
    options:
      login: true
      createdb: true
`
	cfgPath2 := writeTestConfig(t, configYAML2)
	cfg2, err := config.Load(cfgPath2)
	if err != nil {
		t.Fatal(err)
	}

	p2 := provisioner.New(adminDB, connCfg, cfg2, provisioner.DefaultOptions())
	if err := p2.Run(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify the role still exists and has createdb
	var canCreateDB bool
	err = adminDB.QueryRowContext(ctx,
		"SELECT rolcreatedb FROM pg_roles WHERE rolname = $1", "test_role",
	).Scan(&canCreateDB)
	if err != nil {
		t.Fatal(err)
	}
	if !canCreateDB {
		t.Error("expected test_role to have CREATEDB after update")
	}
}

func TestIntegrationEmptyConfig(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	cfgPath := writeTestConfig(t, "{}")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	p := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("empty config should succeed: %v", err)
	}
}

func TestIntegrationCreateTableAndVerifyGrants(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	configYAML := `
roles:
  - name: "writer"
    password: "writerpass"
    options:
      login: true
  - name: "reader"
    password: "readerpass"
    options:
      login: true

databases:
  - name: "granttest"
    owner: "writer"
    schemas:
      - name: "data"
        owner: "writer"
    grants:
      - role: "writer"
        privileges: ["ALL"]
        on_schema: "data"
      - role: "reader"
        privileges: ["USAGE"]
        on_schema: "data"
      - role: "reader"
        privileges: ["SELECT"]
        on_tables_in_schema: "data"
`
	cfgPath := writeTestConfig(t, configYAML)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	if err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}

	// Connect as writer and create a table
	writerCfg := connCfg
	writerCfg.User = "writer"
	writerCfg.Password = "writerpass"
	writerCfg.DBName = "granttest"
	writerDB, err := db.Connect(ctx, writerCfg)
	if err != nil {
		t.Fatalf("connect as writer: %v", err)
	}
	defer writerDB.Close()

	_, err = writerDB.ExecContext(ctx, `CREATE TABLE data.items (id serial PRIMARY KEY, name text)`)
	if err != nil {
		t.Fatalf("create table as writer: %v", err)
	}

	_, err = writerDB.ExecContext(ctx, `INSERT INTO data.items (name) VALUES ('test')`)
	if err != nil {
		t.Fatalf("insert as writer: %v", err)
	}

	// Connect as reader and verify SELECT works
	readerCfg := connCfg
	readerCfg.User = "reader"
	readerCfg.Password = "readerpass"
	readerCfg.DBName = "granttest"
	readerDB, err := db.Connect(ctx, readerCfg)
	if err != nil {
		t.Fatalf("connect as reader: %v", err)
	}
	defer readerDB.Close()

	var name string
	err = readerDB.QueryRowContext(ctx, `SELECT name FROM data.items LIMIT 1`).Scan(&name)
	if err != nil {
		t.Fatalf("reader SELECT failed: %v", err)
	}
	if name != "test" {
		t.Errorf("expected 'test', got %q", name)
	}

	// Reader should NOT be able to INSERT
	_, err = readerDB.ExecContext(ctx, `INSERT INTO data.items (name) VALUES ('hack')`)
	if err == nil {
		t.Error("expected reader INSERT to fail, but it succeeded")
	}
}

func TestIntegrationDatabaseWithLocaleOptions(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	// The Synapse (matrix) case: a database that hard-requires C collation and
	// TEMPLATE template0. Exercises CREATE DATABASE ... TEMPLATE/ENCODING/LC_*.
	configYAML := `
roles:
  - name: "synapse"
    password: "synapsepass"
    options:
      login: true

databases:
  - name: "synapse"
    owner: "synapse"
    options:
      encoding: "UTF8"
      lc_collate: "C"
      lc_ctype: "C"
      template: "template0"
`
	cfgPath := writeTestConfig(t, configYAML)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	p := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	if err := p.Run(ctx); err != nil {
		t.Fatalf("provisioning run failed: %v", err)
	}

	assertDatabaseExists(t, adminDB, "synapse")

	var collate, ctype, encoding string
	err = adminDB.QueryRowContext(ctx,
		"SELECT datcollate, datctype, pg_encoding_to_char(encoding) FROM pg_database WHERE datname = $1",
		"synapse",
	).Scan(&collate, &ctype, &encoding)
	if err != nil {
		t.Fatalf("query pg_database for synapse: %v", err)
	}
	if collate != "C" {
		t.Errorf("expected datcollate 'C', got %q", collate)
	}
	if ctype != "C" {
		t.Errorf("expected datctype 'C', got %q", ctype)
	}
	if encoding != "UTF8" {
		t.Errorf("expected encoding 'UTF8', got %q", encoding)
	}

	// Second run must be idempotent (database exists → warn and continue).
	p2 := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	if err := p2.Run(ctx); err != nil {
		t.Fatalf("second (idempotent) run failed: %v", err)
	}
}

// TestIntegrationConnectWithSpecialCharPassword exercises finding #1 end-to-end:
// a login role whose password contains URL-special characters ('@' and ':') must
// still be connectable through db.Connect, which builds the DSN via net/url.
func TestIntegrationConnectWithSpecialCharPassword(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	const specialPassword = "p@ss:w0rd@Ohshaa3Di:x"
	configYAML := fmt.Sprintf(`
roles:
  - name: "special_user"
    password: %q
    options:
      login: true

databases:
  - name: "specialdb"
    owner: "special_user"
`, specialPassword)

	cfgPath := writeTestConfig(t, configYAML)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	p := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	if err := p.Run(ctx); err != nil {
		t.Fatalf("provisioning run failed: %v", err)
	}

	// Connect as the special-character-password user — this fails on raw DSN
	// concatenation ("invalid port") but succeeds once credentials are encoded.
	userCfg := connCfg
	userCfg.User = "special_user"
	userCfg.Password = specialPassword
	userCfg.DBName = "specialdb"
	userDB, err := db.Connect(ctx, userCfg)
	if err != nil {
		t.Fatalf("connect with special-character password: %v", err)
	}
	defer userDB.Close()

	var one int
	if err := userDB.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("query as special_user: %v", err)
	}
	if one != 1 {
		t.Errorf("expected 1, got %d", one)
	}
}

func assertRoleExists(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)", name).Scan(&exists)
	if err != nil {
		t.Fatalf("checking role %q: %v", name, err)
	}
	if !exists {
		t.Errorf("role %q does not exist", name)
	}
}

func assertDatabaseExists(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists)
	if err != nil {
		t.Fatalf("checking database %q: %v", name, err)
	}
	if !exists {
		t.Errorf("database %q does not exist", name)
	}
}

func assertSchemaExists(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)", name).Scan(&exists)
	if err != nil {
		t.Fatalf("checking schema %q: %v", name, err)
	}
	if !exists {
		t.Errorf("schema %q does not exist", name)
	}
}

func writeMigrationFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("writing migration file %q: %v", name, err)
	}
}

func assertExtensionExists(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = $1)", name).Scan(&exists)
	if err != nil {
		t.Fatalf("checking extension %q: %v", name, err)
	}
	if !exists {
		t.Errorf("extension %q does not exist", name)
	}
}

func TestIntegrationMigrationsVersioned(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	migDir := t.TempDir()
	writeMigrationFile(t, migDir, "V0001__create_items.sql",
		"CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);")
	writeMigrationFile(t, migDir, "V0002__add_column.sql",
		"ALTER TABLE items ADD COLUMN created_at timestamptz DEFAULT now();")

	configYAML := fmt.Sprintf(`
roles:
  - name: "mig_user"
    password: "migpass"
    options:
      login: true
databases:
  - name: "migdb"
    owner: "mig_user"
    migrations:
      directory: %q
`, migDir)

	cfgPath := writeTestConfig(t, configYAML)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	p := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	if err := p.Run(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Verify table exists with both columns
	appDB, err := db.ConnectToDatabase(ctx, connCfg, "migdb")
	if err != nil {
		t.Fatalf("connect to migdb: %v", err)
	}
	defer appDB.Close()

	_, err = appDB.ExecContext(ctx, "INSERT INTO items (name) VALUES ('test')")
	if err != nil {
		t.Fatalf("insert into items: %v", err)
	}

	var name string
	var createdAt sql.NullTime
	err = appDB.QueryRowContext(ctx, "SELECT name, created_at FROM items LIMIT 1").Scan(&name, &createdAt)
	if err != nil {
		t.Fatalf("select from items: %v", err)
	}
	if name != "test" {
		t.Errorf("expected 'test', got %q", name)
	}

	// Verify tracking table
	var count int
	err = appDB.QueryRowContext(ctx, "SELECT count(*) FROM _schema_migrations WHERE type = 'versioned'").Scan(&count)
	if err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 versioned migration records, got %d", count)
	}
}

func TestIntegrationMigrationsIdempotent(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	migDir := t.TempDir()
	writeMigrationFile(t, migDir, "V0001__create_items.sql",
		"CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);")

	configYAML := fmt.Sprintf(`
roles:
  - name: "mig_user"
    password: "migpass"
    options:
      login: true
databases:
  - name: "migdb2"
    owner: "mig_user"
    migrations:
      directory: %q
`, migDir)

	cfgPath := writeTestConfig(t, configYAML)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx := context.Background()

	// First run
	p := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	if err := p.Run(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Second run — should be idempotent
	p2 := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	if err := p2.Run(ctx); err != nil {
		t.Fatalf("second run (idempotent): %v", err)
	}

	// Verify still only 1 migration record
	appDB, err := db.ConnectToDatabase(ctx, connCfg, "migdb2")
	if err != nil {
		t.Fatalf("connect to migdb2: %v", err)
	}
	defer appDB.Close()

	var count int
	err = appDB.QueryRowContext(ctx, "SELECT count(*) FROM _schema_migrations").Scan(&count)
	if err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 migration record after idempotent run, got %d", count)
	}
}

func TestIntegrationMigrationsChecksumMismatch(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	migDir := t.TempDir()
	writeMigrationFile(t, migDir, "V0001__create_items.sql",
		"CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);")

	configYAML := fmt.Sprintf(`
roles:
  - name: "mig_user"
    password: "migpass"
    options:
      login: true
databases:
  - name: "migdb3"
    owner: "mig_user"
    migrations:
      directory: %q
`, migDir)

	cfgPath := writeTestConfig(t, configYAML)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx := context.Background()

	// First run
	p := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	if err := p.Run(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Modify the migration file (versioned = immutable, should error)
	writeMigrationFile(t, migDir, "V0001__create_items.sql",
		"CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL, extra text);")

	p2 := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	err = p2.Run(ctx)
	if err == nil {
		t.Fatal("expected error for versioned migration checksum mismatch")
	}
}

func TestIntegrationMigrationsRepeatable(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	migDir := t.TempDir()
	writeMigrationFile(t, migDir, "V0001__create_items.sql",
		"CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);")
	writeMigrationFile(t, migDir, "R0001__seed_data.sql",
		"INSERT INTO items (name) VALUES ('seed1') ON CONFLICT DO NOTHING;")

	configYAML := fmt.Sprintf(`
roles:
  - name: "mig_user"
    password: "migpass"
    options:
      login: true
databases:
  - name: "migdb4"
    owner: "mig_user"
    migrations:
      directory: %q
`, migDir)

	cfgPath := writeTestConfig(t, configYAML)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx := context.Background()

	// First run
	p := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	if err := p.Run(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Modify repeatable migration content
	updatedContent := "INSERT INTO items (name) VALUES ('seed2') ON CONFLICT DO NOTHING;"
	writeMigrationFile(t, migDir, "R0001__seed_data.sql", updatedContent)

	// Second run — repeatable should re-execute without error
	p2 := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
	if err := p2.Run(ctx); err != nil {
		t.Fatalf("second run with changed repeatable: %v", err)
	}

	// Verify updated checksum in tracking table
	appDB, err := db.ConnectToDatabase(ctx, connCfg, "migdb4")
	if err != nil {
		t.Fatalf("connect to migdb4: %v", err)
	}
	defer appDB.Close()

	var checksum string
	err = appDB.QueryRowContext(ctx,
		"SELECT checksum FROM _schema_migrations WHERE version = '0001' AND type = 'repeatable'").Scan(&checksum)
	if err != nil {
		t.Fatalf("query checksum: %v", err)
	}

	// checksum must match the SHA-256 of the updated content
	expectedHash := sha256.Sum256([]byte(updatedContent))
	expectedChecksum := hex.EncodeToString(expectedHash[:])
	if checksum != expectedChecksum {
		t.Errorf("expected checksum %q for updated content, got %q", expectedChecksum, checksum)
	}
}

func TestIntegrationMigrationsDisabled(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	migDir := t.TempDir()
	writeMigrationFile(t, migDir, "V0001__create_items.sql",
		"CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);")

	configYAML := fmt.Sprintf(`
roles:
  - name: "mig_user"
    password: "migpass"
    options:
      login: true
databases:
  - name: "migdb_disabled"
    owner: "mig_user"
    migrations:
      directory: %q
`, migDir)

	cfgPath := writeTestConfig(t, configYAML)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx := context.Background()

	// Run with migrations disabled
	p := provisioner.New(adminDB, connCfg, cfg, provisioner.Options{MigrationsEnabled: false})
	if err := p.Run(ctx); err != nil {
		t.Fatalf("run with migrations disabled: %v", err)
	}

	// Verify database was created but _schema_migrations table does not exist
	appDB, err := db.ConnectToDatabase(ctx, connCfg, "migdb_disabled")
	if err != nil {
		t.Fatalf("connect to migdb_disabled: %v", err)
	}
	defer appDB.Close()

	var exists bool
	err = appDB.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = '_schema_migrations')").Scan(&exists)
	if err != nil {
		t.Fatalf("check _schema_migrations: %v", err)
	}
	if exists {
		t.Error("expected _schema_migrations table NOT to exist when migrations are disabled")
	}
}

// TestIntegrationMonitoringRoleMembership provisions a non-superuser account for
// postgres_exporter via pg_monitor membership and verifies it can read statistics
// that are otherwise restricted to superusers, while not being able to write.
func TestIntegrationMonitoringRoleMembership(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	configYAML := `
roles:
  - name: "postgres_exporter"
    password: "exporterpass"
    member_of: ["pg_monitor"]
    options:
      login: true
      superuser: false
`
	cfgPath := writeTestConfig(t, configYAML)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	for run := 1; run <= 2; run++ {
		p := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions())
		if err := p.Run(ctx); err != nil {
			t.Fatalf("provisioning run %d failed: %v", run, err)
		}
	}

	var isMember bool
	if err := adminDB.QueryRowContext(ctx,
		"SELECT pg_has_role('postgres_exporter', 'pg_monitor', 'MEMBER')",
	).Scan(&isMember); err != nil {
		t.Fatal(err)
	}
	if !isMember {
		t.Fatal("expected postgres_exporter to be a member of pg_monitor")
	}

	exporterCfg := connCfg
	exporterCfg.User = "postgres_exporter"
	exporterCfg.Password = "exporterpass"
	exporterDB, err := db.Connect(ctx, exporterCfg)
	if err != nil {
		t.Fatalf("connect as postgres_exporter: %v", err)
	}
	defer exporterDB.Close()

	// Other sessions' query text is only visible with pg_read_all_stats (part of pg_monitor).
	var hiddenQueries int
	if err := exporterDB.QueryRowContext(ctx,
		"SELECT count(*) FROM pg_stat_activity WHERE usename = 'admin' AND query = '<insufficient privilege>'",
	).Scan(&hiddenQueries); err != nil {
		t.Fatalf("query pg_stat_activity: %v", err)
	}
	if hiddenQueries != 0 {
		t.Errorf("expected admin session queries to be visible, %d were hidden", hiddenQueries)
	}

	// pg_ls_waldir() is restricted to superusers and pg_monitor members.
	var walFiles int
	if err := exporterDB.QueryRowContext(ctx, "SELECT count(*) FROM pg_ls_waldir()").Scan(&walFiles); err != nil {
		t.Fatalf("pg_ls_waldir as postgres_exporter: %v", err)
	}

	if _, err := exporterDB.ExecContext(ctx, "CREATE TABLE should_fail (id int)"); err == nil {
		t.Error("expected postgres_exporter to be unable to create tables in the postgres database")
	}
}

// TestIntegrationRolePasswordFromSecretFile provisions a role whose password comes from
// a Docker/Kubernetes-style secret file (APP_PW_FILE) and logs in with it.
func TestIntegrationRolePasswordFromSecretFile(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	const password = "pw-from-secret-file"
	secretPath := filepath.Join(t.TempDir(), "app_pw")
	if err := os.WriteFile(secretPath, []byte(password+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_PW_FILE", secretPath)

	cfgPath := writeTestConfig(t, `
roles:
  - name: "file_secret_user"
    password: "${APP_PW}"
    options:
      login: true
`)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	if err := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions()).Run(ctx); err != nil {
		t.Fatalf("provisioning run failed: %v", err)
	}

	userCfg := connCfg
	userCfg.User = "file_secret_user"
	userCfg.Password = password
	userDB, err := db.Connect(ctx, userCfg)
	if err != nil {
		t.Fatalf("connect with password from secret file: %v", err)
	}
	defer userDB.Close()

	if err := userDB.PingContext(ctx); err != nil {
		t.Fatalf("ping as file_secret_user: %v", err)
	}
}

// TestIntegrationRolePasswordWithEscapedReference provisions a role whose password
// contains a literal "${" written as the "$${" escape, and logs in with it.
func TestIntegrationRolePasswordWithEscapedReference(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	cfgPath := writeTestConfig(t, `
roles:
  - name: "escaped_pw_user"
    password: "pa$${not_a_var}ss"
    options:
      login: true
`)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	if err := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions()).Run(ctx); err != nil {
		t.Fatalf("provisioning run failed: %v", err)
	}

	userCfg := connCfg
	userCfg.User = "escaped_pw_user"
	userCfg.Password = "pa${not_a_var}ss"
	userDB, err := db.Connect(ctx, userCfg)
	if err != nil {
		t.Fatalf("connect with unescaped password: %v", err)
	}
	defer userDB.Close()

	if err := userDB.PingContext(ctx); err != nil {
		t.Fatalf("ping as escaped_pw_user: %v", err)
	}
}

// connectAs opens a connection to dbName as the given role.
func connectAs(t *testing.T, connCfg db.ConnConfig, user, password, dbName string) *sql.DB {
	t.Helper()
	cfg := connCfg
	cfg.User = user
	cfg.Password = password
	cfg.DBName = dbName
	conn, err := db.Connect(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect as %s: %v", user, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// TestIntegrationSequenceGrantsAllowSerialInsert verifies that on_sequences_in_schema
// is what lets a role insert into tables with serial columns created after provisioning,
// and that identity columns need no sequence grant.
func TestIntegrationSequenceGrantsAllowSerialInsert(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	cfg, err := config.Load(writeTestConfig(t, `
roles:
  - name: "owner"
    password: "ownerpass"
    options: { login: true }
  - name: "app"
    password: "apppass"
    options: { login: true }
  - name: "tables_only"
    password: "tablespass"
    options: { login: true }
  - name: "migrator"
    password: "migratorpass"
    options: { login: true }

databases:
  - name: "seqtest"
    owner: "owner"
    schemas:
      - name: "data"
        owner: "owner"
    grants:
      - { role: "migrator", privileges: ["USAGE", "CREATE"], on_schema: "data" }
      - { role: "app", privileges: ["USAGE"], on_schema: "data" }
      - { role: "app", privileges: ["SELECT", "INSERT"], on_tables_in_schema: "data" }
      - { role: "app", privileges: ["USAGE", "SELECT"], on_sequences_in_schema: "data" }
      - { role: "tables_only", privileges: ["USAGE"], on_schema: "data" }
      - { role: "tables_only", privileges: ["SELECT", "INSERT"], on_tables_in_schema: "data" }
`))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions()).Run(ctx); err != nil {
		t.Fatalf("provisioning run failed: %v", err)
	}

	ownerDB := connectAs(t, connCfg, "owner", "ownerpass", "seqtest")
	if _, err := ownerDB.ExecContext(ctx, `
		CREATE TABLE data.serial_items (id serial PRIMARY KEY, name text);
		CREATE TABLE data.identity_items (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text);
	`); err != nil {
		t.Fatalf("create tables as owner: %v", err)
	}

	appDB := connectAs(t, connCfg, "app", "apppass", "seqtest")
	if _, err := appDB.ExecContext(ctx, `INSERT INTO data.serial_items (name) VALUES ('ok')`); err != nil {
		t.Errorf("app insert into serial table: %v", err)
	}

	tablesOnlyDB := connectAs(t, connCfg, "tables_only", "tablespass", "seqtest")
	_, err = tablesOnlyDB.ExecContext(ctx, `INSERT INTO data.serial_items (name) VALUES ('denied')`)
	if err == nil || !strings.Contains(err.Error(), "permission denied for sequence") {
		t.Errorf("expected sequence permission error without sequence grant, got: %v", err)
	}
	if _, err := tablesOnlyDB.ExecContext(ctx, `INSERT INTO data.identity_items (name) VALUES ('ok')`); err != nil {
		t.Errorf("identity column insert should not need a sequence grant: %v", err)
	}

	// A sequence created by a role that is neither the provisioning user nor the schema
	// owner is not covered by default privileges, so only the next run's
	// GRANT ... ON ALL SEQUENCES reaches it.
	migratorDB := connectAs(t, connCfg, "migrator", "migratorpass", "seqtest")
	if _, err := migratorDB.ExecContext(ctx,
		`CREATE TABLE data.migrator_items (id serial PRIMARY KEY, name text)`,
	); err != nil {
		t.Fatalf("create table as migrator: %v", err)
	}
	appHasSequenceUsage := func() bool {
		t.Helper()
		var has bool
		if err := ownerDB.QueryRowContext(ctx,
			`SELECT has_sequence_privilege('app', 'data.migrator_items_id_seq', 'USAGE')`,
		).Scan(&has); err != nil {
			t.Fatal(err)
		}
		return has
	}
	if appHasSequenceUsage() {
		t.Fatal("app unexpectedly has USAGE on a sequence created by migrator before the second run")
	}

	if err := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions()).Run(ctx); err != nil {
		t.Fatalf("second provisioning run failed: %v", err)
	}
	if !appHasSequenceUsage() {
		t.Error("expected app to have USAGE on data.migrator_items_id_seq after the second run")
	}
	if _, err := appDB.ExecContext(ctx, `INSERT INTO data.migrator_items (name) VALUES ('ok')`); err != nil {
		t.Errorf("app insert into migrator-created serial table after second run: %v", err)
	}
}

// TestIntegrationFunctionGrants verifies on_functions_in_schema for functions whose
// default EXECUTE was revoked from PUBLIC: an existing function is reached by
// GRANT ... ON ALL ROUTINES, and a function the schema owner creates later is reached
// by ALTER DEFAULT PRIVILEGES FOR ROLE <owner>.
func TestIntegrationFunctionGrants(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	migrationsDir := t.TempDir()
	writeMigrationFile(t, migrationsDir, "V0001__create_function.sql", `
CREATE FUNCTION api.answer() RETURNS int LANGUAGE sql AS 'SELECT 42';
REVOKE EXECUTE ON FUNCTION api.answer() FROM PUBLIC;
`)

	loadConfig := func(extraGrants string) *config.Config {
		t.Helper()
		cfg, err := config.Load(writeTestConfig(t, fmt.Sprintf(`
roles:
  - name: "fnowner"
    password: "fnownerpass"
    options: { login: true }
  - name: "caller"
    password: "callerpass"
    options: { login: true }
  - name: "stranger"
    password: "strangerpass"
    options: { login: true }

databases:
  - name: "fntest"
    owner: "fnowner"
    schemas:
      - name: "api"
        owner: "fnowner"
    grants:
      - { role: "caller", privileges: ["USAGE"], on_schema: "api" }
      - { role: "stranger", privileges: ["USAGE"], on_schema: "api" }
%s
    migrations:
      directory: %q
`, extraGrants, migrationsDir)))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	// First run without the function grant: the migration creates api.answer() and no
	// default privileges for functions exist yet.
	ctx := context.Background()
	if err := provisioner.New(adminDB, connCfg, loadConfig(""), provisioner.DefaultOptions()).Run(ctx); err != nil {
		t.Fatalf("first provisioning run failed: %v", err)
	}

	callerDB := connectAs(t, connCfg, "caller", "callerpass", "fntest")
	var answer int
	if err := callerDB.QueryRowContext(ctx, `SELECT api.answer()`).Scan(&answer); err == nil {
		t.Fatal("expected caller to be denied EXECUTE on api.answer() before the function grant")
	}

	// Second run adds the grant; api.answer() already exists, so only
	// GRANT ... ON ALL ROUTINES can reach it.
	cfg := loadConfig(`      - { role: "caller", privileges: ["EXECUTE"], on_functions_in_schema: "api" }`)
	if err := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions()).Run(ctx); err != nil {
		t.Fatalf("second provisioning run failed: %v", err)
	}
	if err := callerDB.QueryRowContext(ctx, `SELECT api.answer()`).Scan(&answer); err != nil {
		t.Fatalf("caller should be able to execute existing api.answer(): %v", err)
	}
	if answer != 42 {
		t.Errorf("expected 42, got %d", answer)
	}

	// A function the schema owner creates after provisioning is covered by
	// ALTER DEFAULT PRIVILEGES FOR ROLE fnowner, even with PUBLIC's EXECUTE revoked.
	ownerDB := connectAs(t, connCfg, "fnowner", "fnownerpass", "fntest")
	if _, err := ownerDB.ExecContext(ctx, `
		CREATE FUNCTION api.owned() RETURNS int LANGUAGE sql AS 'SELECT 7';
		REVOKE EXECUTE ON FUNCTION api.owned() FROM PUBLIC;
	`); err != nil {
		t.Fatalf("create function as schema owner: %v", err)
	}
	if err := callerDB.QueryRowContext(ctx, `SELECT api.owned()`).Scan(&answer); err != nil {
		t.Errorf("caller should be able to execute owner-created api.owned(): %v", err)
	}

	strangerDB := connectAs(t, connCfg, "stranger", "strangerpass", "fntest")
	for _, fn := range []string{"api.answer()", "api.owned()"} {
		if err := strangerDB.QueryRowContext(ctx, "SELECT "+fn).Scan(&answer); err == nil {
			t.Errorf("expected stranger to be denied EXECUTE on %s", fn)
		}
	}
}

// TestIntegrationRevokePublicConnect hardens a database so only explicitly granted roles
// can connect, and verifies a second run is a no-op.
func TestIntegrationRevokePublicConnect(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	cfg, err := config.Load(writeTestConfig(t, `
roles:
  - name: "app"
    password: "apppass"
    options: { login: true }
  - name: "outsider"
    password: "outsiderpass"
    options: { login: true }

databases:
  - name: "hardened"
    revokes:
      - { role: "PUBLIC", privileges: ["ALL"], on_database: true }
    grants:
      - { role: "app", privileges: ["CONNECT"], on_database: true }
`))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for run := 1; run <= 2; run++ {
		if err := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions()).Run(ctx); err != nil {
			t.Fatalf("provisioning run %d failed: %v", run, err)
		}
	}

	privilege := func(role, priv string) bool {
		t.Helper()
		var has bool
		if err := adminDB.QueryRowContext(ctx,
			`SELECT has_database_privilege($1, 'hardened', $2)`, role, priv,
		).Scan(&has); err != nil {
			t.Fatal(err)
		}
		return has
	}
	if privilege("outsider", "CONNECT") {
		t.Error("expected outsider to lose CONNECT after revoking from PUBLIC")
	}
	if privilege("outsider", "TEMPORARY") {
		t.Error("expected outsider to lose TEMPORARY after revoking ALL from PUBLIC")
	}
	if !privilege("app", "CONNECT") {
		t.Error("expected app to keep CONNECT via its explicit grant")
	}

	appDB := connectAs(t, connCfg, "app", "apppass", "hardened")
	if err := appDB.PingContext(ctx); err != nil {
		t.Errorf("app should be able to connect: %v", err)
	}
}

// TestIntegrationRevokeCreateOnPublicSchema revokes CREATE on the public schema from
// PUBLIC. PostgreSQL 15+ already denies it by default, so the test grants it first to
// exercise an actual revoke (and the "already revoked" path on the first run).
func TestIntegrationRevokeCreateOnPublicSchema(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	cfg, err := config.Load(writeTestConfig(t, `
roles:
  - name: "someone"
    options: { login: true }

databases:
  - name: "schemahardening"
    revokes:
      - { role: "PUBLIC", privileges: ["CREATE"], on_schema: "public" }
`))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	run := func() {
		t.Helper()
		if err := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions()).Run(ctx); err != nil {
			t.Fatalf("provisioning run failed: %v", err)
		}
	}
	run()

	dbConn := connectAs(t, connCfg, connCfg.User, connCfg.Password, "schemahardening")
	if _, err := dbConn.ExecContext(ctx, `GRANT CREATE ON SCHEMA public TO PUBLIC`); err != nil {
		t.Fatal(err)
	}
	canCreate := func() bool {
		t.Helper()
		var has bool
		if err := dbConn.QueryRowContext(ctx,
			`SELECT has_schema_privilege('someone', 'public', 'CREATE')`,
		).Scan(&has); err != nil {
			t.Fatal(err)
		}
		return has
	}
	if !canCreate() {
		t.Fatal("precondition: expected someone to have CREATE via PUBLIC")
	}

	run()
	if canCreate() {
		t.Error("expected CREATE on schema public to be revoked from PUBLIC")
	}
}

// captureLogs redirects the default slog logger to a buffer until the test ends.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	return &buf
}

// TestIntegrationRevokeWarnsOnOtherGrantor revokes CONNECT from PUBLIC when a non-owner
// role also granted it to PUBLIC. REVOKE (run as superuser, i.e. as the owner) only
// removes the owner's grant, so the privilege survives and a warning must be logged.
func TestIntegrationRevokeWarnsOnOtherGrantor(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE ROLE delegate`,
		`CREATE ROLE outsider LOGIN`,
		`CREATE DATABASE othergrantor`,
		`GRANT CONNECT ON DATABASE othergrantor TO delegate WITH GRANT OPTION`,
		// One round trip, so SET ROLE applies to the GRANT on the same pooled connection.
		`SET ROLE delegate; GRANT CONNECT ON DATABASE othergrantor TO PUBLIC; RESET ROLE`,
	} {
		if _, err := adminDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	cfg, err := config.Load(writeTestConfig(t, `
databases:
  - name: "othergrantor"
    revokes:
      - { role: "PUBLIC", privileges: ["CONNECT"], on_database: true }
`))
	if err != nil {
		t.Fatal(err)
	}

	logs := captureLogs(t)
	if err := provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions()).Run(ctx); err != nil {
		t.Fatalf("provisioning failed: %v", err)
	}

	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "different grantor") {
		t.Errorf("expected different-grantor warning, got logs:\n%s", out)
	}

	var grantors []string
	rows, err := adminDB.QueryContext(ctx, `SELECT g.rolname
		FROM pg_database d
		CROSS JOIN LATERAL aclexplode(d.datacl) a
		JOIN pg_roles g ON g.oid = a.grantor
		WHERE d.datname = 'othergrantor' AND a.grantee = 0 AND a.privilege_type = 'CONNECT'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var grantor string
		if err := rows.Scan(&grantor); err != nil {
			t.Fatal(err)
		}
		grantors = append(grantors, grantor)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(grantors, ",") != "delegate" {
		t.Errorf("expected only delegate's CONNECT grant to PUBLIC to remain, got grantors %v", grantors)
	}

	var outsiderCanConnect bool
	if err := adminDB.QueryRowContext(ctx,
		`SELECT has_database_privilege('outsider', 'othergrantor', 'CONNECT')`,
	).Scan(&outsiderCanConnect); err != nil {
		t.Fatal(err)
	}
	if !outsiderCanConnect {
		t.Error("expected outsider to keep CONNECT through delegate's grant to PUBLIC")
	}
}

// TestIntegrationRevokeFailsOnDependentPrivileges revokes CONNECT from a role that
// re-granted it WITH GRANT OPTION. The provisioner doesn't cascade, so REVOKE fails; the
// error must explain why and the privileges must stay untouched.
func TestIntegrationRevokeFailsOnDependentPrivileges(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE ROLE delegate`,
		`CREATE ROLE downstream`,
		`CREATE DATABASE dependent`,
		`GRANT CONNECT ON DATABASE dependent TO delegate WITH GRANT OPTION`,
		// One round trip, so SET ROLE applies to the GRANT on the same pooled connection.
		`SET ROLE delegate; GRANT CONNECT ON DATABASE dependent TO downstream; RESET ROLE`,
	} {
		if _, err := adminDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	cfg, err := config.Load(writeTestConfig(t, `
roles:
  - name: "delegate"
databases:
  - name: "dependent"
    revokes:
      - { role: "delegate", privileges: ["CONNECT"], on_database: true }
`))
	if err != nil {
		t.Fatal(err)
	}

	err = provisioner.New(adminDB, connCfg, cfg, provisioner.DefaultOptions()).Run(ctx)
	if err == nil {
		t.Fatal("expected REVOKE to fail on dependent privileges")
	}
	for _, want := range []string{"dependent privileges exist", "WITH GRANT OPTION", "CASCADE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}

	var downstreamCanConnect bool
	if err := adminDB.QueryRowContext(ctx,
		`SELECT has_database_privilege('downstream', 'dependent', 'CONNECT')`,
	).Scan(&downstreamCanConnect); err != nil {
		t.Fatal(err)
	}
	if !downstreamCanConnect {
		t.Error("expected downstream to keep CONNECT: the provisioner must not cascade")
	}
}
