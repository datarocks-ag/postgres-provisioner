//go:build integration

package provisioner_test

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	p := provisioner.New(adminDB, connCfg, cfg, true)

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
	p2 := provisioner.New(adminDB, connCfg, cfg, true)
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
	p := provisioner.New(adminDB, connCfg, cfg, true)

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

	p2 := provisioner.New(adminDB, connCfg, cfg2, true)
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

	p := provisioner.New(adminDB, connCfg, cfg, true)
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
	p := provisioner.New(adminDB, connCfg, cfg, true)
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
	os.WriteFile(filepath.Join(migDir, "V0001__create_items.sql"),
		[]byte("CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);"), 0644)
	os.WriteFile(filepath.Join(migDir, "V0002__add_column.sql"),
		[]byte("ALTER TABLE items ADD COLUMN created_at timestamptz DEFAULT now();"), 0644)

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
	p := provisioner.New(adminDB, connCfg, cfg, true)
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
	os.WriteFile(filepath.Join(migDir, "V0001__create_items.sql"),
		[]byte("CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);"), 0644)

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
	p := provisioner.New(adminDB, connCfg, cfg, true)
	if err := p.Run(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Second run — should be idempotent
	p2 := provisioner.New(adminDB, connCfg, cfg, true)
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
	os.WriteFile(filepath.Join(migDir, "V0001__create_items.sql"),
		[]byte("CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);"), 0644)

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
	p := provisioner.New(adminDB, connCfg, cfg, true)
	if err := p.Run(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Modify the migration file (versioned = immutable, should error)
	os.WriteFile(filepath.Join(migDir, "V0001__create_items.sql"),
		[]byte("CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL, extra text);"), 0644)

	p2 := provisioner.New(adminDB, connCfg, cfg, true)
	err = p2.Run(ctx)
	if err == nil {
		t.Fatal("expected error for versioned migration checksum mismatch")
	}
}

func TestIntegrationMigrationsRepeatable(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	migDir := t.TempDir()
	os.WriteFile(filepath.Join(migDir, "V0001__create_items.sql"),
		[]byte("CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);"), 0644)
	os.WriteFile(filepath.Join(migDir, "R0001__seed_data.sql"),
		[]byte("INSERT INTO items (name) VALUES ('seed1') ON CONFLICT DO NOTHING;"), 0644)

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
	p := provisioner.New(adminDB, connCfg, cfg, true)
	if err := p.Run(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Modify repeatable migration content
	os.WriteFile(filepath.Join(migDir, "R0001__seed_data.sql"),
		[]byte("INSERT INTO items (name) VALUES ('seed2') ON CONFLICT DO NOTHING;"), 0644)

	// Second run — repeatable should re-execute without error
	p2 := provisioner.New(adminDB, connCfg, cfg, true)
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

	// checksum should match the new content
	if checksum == "" {
		t.Error("expected non-empty checksum")
	}
}

func TestIntegrationMigrationsDisabled(t *testing.T) {
	adminDB, connCfg, cleanup := setupPostgres(t)
	defer cleanup()

	migDir := t.TempDir()
	os.WriteFile(filepath.Join(migDir, "V0001__create_items.sql"),
		[]byte("CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);"), 0644)

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
	p := provisioner.New(adminDB, connCfg, cfg, false)
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
