package provisioner

import (
	"context"
	"testing"

	"postgres-provisioner/internal/config"
	"postgres-provisioner/internal/db"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEnsureRoleCreate(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	login := true
	role := config.Role{
		Name:     "testuser",
		Password: "secret",
		Options:  config.RoleOptions{Login: &login},
	}

	// Role does not exist
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("testuser").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	mock.ExpectExec(`CREATE ROLE "testuser" WITH PASSWORD 'secret' LOGIN`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{adminDB: mockDB}
	if err := p.ensureRole(context.Background(), role, "update"); err != nil {
		t.Fatalf("ensureRole: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRoleAlter(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	role := config.Role{
		Name:     "existing",
		Password: "newpass",
	}

	// Role exists
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("existing").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	mock.ExpectExec(`ALTER ROLE "existing" WITH PASSWORD 'newpass'`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{adminDB: mockDB}
	if err := p.ensureRole(context.Background(), role, "update"); err != nil {
		t.Fatalf("ensureRole: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRoleCreateStrategySkipsExisting(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	role := config.Role{
		Name:     "existing",
		Password: "newpass",
	}

	// Role exists
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("existing").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	// No ALTER expected — strategy=create skips update

	p := &Provisioner{adminDB: mockDB}
	if err := p.ensureRole(context.Background(), role, "create"); err != nil {
		t.Fatalf("ensureRole: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDatabaseCreate(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name:  "testdb",
		Owner: "testuser",
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("testdb").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	mock.ExpectExec(`CREATE DATABASE "testdb" OWNER "testuser"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{adminDB: mockDB}
	created, err := p.ensureDatabase(context.Background(), database, "update")
	if err != nil {
		t.Fatalf("ensureDatabase: %v", err)
	}
	if !created {
		t.Fatal("expected created=true for new database")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDatabaseExists(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name:  "existingdb",
		Owner: "owner",
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("existingdb").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	mock.ExpectExec(`ALTER DATABASE "existingdb" OWNER TO "owner"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{adminDB: mockDB}
	created, err := p.ensureDatabase(context.Background(), database, "update")
	if err != nil {
		t.Fatalf("ensureDatabase: %v", err)
	}
	if created {
		t.Fatal("expected created=false for existing database")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDatabaseCreateStrategySkipsExisting(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name:  "existingdb",
		Owner: "owner",
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("existingdb").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	// No ALTER expected — strategy=create skips update

	p := &Provisioner{adminDB: mockDB}
	created, err := p.ensureDatabase(context.Background(), database, "create")
	if err != nil {
		t.Fatalf("ensureDatabase: %v", err)
	}
	if created {
		t.Fatal("expected created=false for existing database with strategy=create")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureExtension(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectExec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{}
	if err := p.ensureExtension(context.Background(), mockDB, "uuid-ossp"); err != nil {
		t.Fatalf("ensureExtension: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureSchema(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec(`ALTER SCHEMA "app" OWNER TO "appuser"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{}
	if err := p.ensureSchema(context.Background(), mockDB, "app", "appuser", "update"); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureSchemaCreateStrategySkipsOwner(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	// CREATE SCHEMA always runs
	mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// No ALTER SCHEMA OWNER expected — strategy=create skips owner update

	p := &Provisioner{}
	if err := p.ensureSchema(context.Background(), mockDB, "app", "appuser", "create"); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnDatabase(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	grant := config.Grant{
		Role:       "reader",
		Privileges: []string{"CONNECT"},
		OnDatabase: true,
	}

	mock.ExpectExec(`GRANT CONNECT ON DATABASE "mydb" TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	database := config.Database{Name: "mydb"}
	p := &Provisioner{}
	if err := p.applyGrant(context.Background(), mockDB, database, grant); err != nil {
		t.Fatalf("applyGrant: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnSchema(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	grant := config.Grant{
		Role:       "appuser",
		Privileges: []string{"ALL"},
		OnSchema:   "app",
	}

	mock.ExpectExec(`GRANT ALL ON SCHEMA "app" TO "appuser"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	database := config.Database{Name: "mydb"}
	p := &Provisioner{}
	if err := p.applyGrant(context.Background(), mockDB, database, grant); err != nil {
		t.Fatalf("applyGrant: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnTablesInSchema(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	grant := config.Grant{
		Role:             "reader",
		Privileges:       []string{"SELECT"},
		OnTablesInSchema: "app",
	}

	mock.ExpectExec(`GRANT SELECT ON ALL TABLES IN SCHEMA "app" TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES IN SCHEMA "app" GRANT SELECT ON TABLES TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES FOR ROLE "appowner" IN SCHEMA "app" GRANT SELECT ON TABLES TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	database := config.Database{
		Name:  "mydb",
		Owner: "appowner",
	}
	p := &Provisioner{}
	if err := p.applyGrant(context.Background(), mockDB, database, grant); err != nil {
		t.Fatalf("applyGrant: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"simple", `"simple"`},
		{`has"quote`, `"has""quote"`},
		{"with space", `"with space"`},
		{"", `""`},
		{`back\slash`, `"back\slash"`},
	}
	for _, tt := range tests {
		got := quoteIdentifier(tt.input)
		if got != tt.want {
			t.Errorf("quoteIdentifier(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"simple", `'simple'`},
		{"it's", `'it''s'`},
		{"has'two'quotes", `'has''two''quotes'`},
		{"", `''`},
		{`back\slash`, `'back\slash'`},
	}
	for _, tt := range tests {
		got := quoteLiteral(tt.input)
		if got != tt.want {
			t.Errorf("quoteLiteral(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRunFullProvisioning(t *testing.T) {
	// Test the orchestrator with a minimal config that only has roles and databases
	// (no per-db resources, since those need a separate connection).
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	login := true
	cfg := &config.Config{
		Roles: []config.Role{
			{Name: "app", Password: "pass", Options: config.RoleOptions{Login: &login}},
		},
		Databases: []config.Database{
			{Name: "appdb", Owner: "app"},
		},
	}

	// Role check + create
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("app").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`CREATE ROLE "app" WITH PASSWORD 'pass' LOGIN`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// Database check + create
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("appdb").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`CREATE DATABASE "appdb" OWNER "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// The Run method tries to connect to the target DB for per-db resources.
	// Since we can't mock db.ConnectToDatabase here, we test with no extensions/schemas/grants.
	// The full integration test covers the complete flow.

	// We'll test just the role+database portion by calling methods directly
	p := New(mockDB, db.ConnConfig{}, cfg)

	ctx := context.Background()
	for _, role := range cfg.Roles {
		strategy := config.EffectiveStrategy(role.Strategy, cfg.Strategy)
		if err := p.ensureRole(ctx, role, strategy); err != nil {
			t.Fatalf("ensureRole: %v", err)
		}
	}
	for _, database := range cfg.Databases {
		strategy := config.EffectiveStrategy(database.Strategy, cfg.Strategy)
		if _, err := p.ensureDatabase(ctx, database, strategy); err != nil {
			t.Fatalf("ensureDatabase: %v", err)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
