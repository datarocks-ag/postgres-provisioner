package provisioner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"postgres-provisioner/internal/config"
	"postgres-provisioner/internal/db"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
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
	if _, err := p.ensureRole(context.Background(), role, "update"); err != nil {
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
	if _, err := p.ensureRole(context.Background(), role, "update"); err != nil {
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
	if _, err := p.ensureRole(context.Background(), role, "create"); err != nil {
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

	mock.ExpectBegin()
	mock.ExpectExec(`GRANT SELECT ON ALL TABLES IN SCHEMA "app" TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES IN SCHEMA "app" GRANT SELECT ON TABLES TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES FOR ROLE "appowner" IN SCHEMA "app" GRANT SELECT ON TABLES TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

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

func TestRoleOptionsClauses(t *testing.T) {
	boolTrue := true
	boolFalse := false
	connLimit := 10
	connLimitUnlimited := -1
	validUntil := "2030-01-01"

	tests := []struct {
		name string
		opts config.RoleOptions
		want []string
	}{
		{"empty options", config.RoleOptions{}, nil},
		{"login true", config.RoleOptions{Login: &boolTrue}, []string{"LOGIN"}},
		{"login false", config.RoleOptions{Login: &boolFalse}, []string{"NOLOGIN"}},
		{"superuser true", config.RoleOptions{Superuser: &boolTrue}, []string{"SUPERUSER"}},
		{"superuser false", config.RoleOptions{Superuser: &boolFalse}, []string{"NOSUPERUSER"}},
		{"createdb true", config.RoleOptions{CreateDB: &boolTrue}, []string{"CREATEDB"}},
		{"createdb false", config.RoleOptions{CreateDB: &boolFalse}, []string{"NOCREATEDB"}},
		{"createrole true", config.RoleOptions{CreateRole: &boolTrue}, []string{"CREATEROLE"}},
		{"createrole false", config.RoleOptions{CreateRole: &boolFalse}, []string{"NOCREATEROLE"}},
		{"inherit true", config.RoleOptions{Inherit: &boolTrue}, []string{"INHERIT"}},
		{"inherit false", config.RoleOptions{Inherit: &boolFalse}, []string{"NOINHERIT"}},
		{"replication true", config.RoleOptions{Replication: &boolTrue}, []string{"REPLICATION"}},
		{"replication false", config.RoleOptions{Replication: &boolFalse}, []string{"NOREPLICATION"}},
		{"bypassrls true", config.RoleOptions{BypassRLS: &boolTrue}, []string{"BYPASSRLS"}},
		{"bypassrls false", config.RoleOptions{BypassRLS: &boolFalse}, []string{"NOBYPASSRLS"}},
		{"connection limit", config.RoleOptions{ConnectionLimit: &connLimit}, []string{"CONNECTION LIMIT 10"}},
		{"connection limit unlimited", config.RoleOptions{ConnectionLimit: &connLimitUnlimited}, []string{"CONNECTION LIMIT -1"}},
		{"valid until", config.RoleOptions{ValidUntil: &validUntil}, []string{"VALID UNTIL '2030-01-01'"}},
		{"all options", config.RoleOptions{
			Login:           &boolTrue,
			Superuser:       &boolFalse,
			CreateDB:        &boolTrue,
			CreateRole:      &boolFalse,
			Inherit:         &boolTrue,
			Replication:     &boolFalse,
			BypassRLS:       &boolTrue,
			ConnectionLimit: &connLimit,
			ValidUntil:      &validUntil,
		}, []string{"LOGIN", "NOSUPERUSER", "CREATEDB", "NOCREATEROLE", "INHERIT", "NOREPLICATION", "BYPASSRLS", "CONNECTION LIMIT 10", "VALID UNTIL '2030-01-01'"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := roleOptionsClauses(tt.opts)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("clause[%d]: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestAlterRoleNoChanges(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	role := config.Role{
		Name: "existing",
		// No password, no options → nothing to alter
	}

	// roleExists query returns true
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("existing").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	// No ALTER expected — alterRole returns early with "no changes"

	p := &Provisioner{adminDB: mockDB}
	if _, err := p.ensureRole(context.Background(), role, "update"); err != nil {
		t.Fatalf("ensureRole: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDatabaseExistsNoOwner(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name: "existingdb",
		// No owner
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("existingdb").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	// No ALTER expected — empty owner

	p := &Provisioner{adminDB: mockDB}
	created, err := p.ensureDatabase(context.Background(), database, "update")
	if err != nil {
		t.Fatalf("ensureDatabase: %v", err)
	}
	if created {
		t.Fatal("expected created=false for existing database without owner")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateDatabaseNoOwner(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name: "newdb",
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("newdb").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	mock.ExpectExec(`CREATE DATABASE "newdb"`).
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

func TestCreateDatabaseWithOptions(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name:  "synapse",
		Owner: "synapse",
		Options: config.DatabaseOptions{
			Encoding:  "UTF8",
			LcCollate: "C",
			LcCtype:   "C",
			Template:  "template0",
		},
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("synapse").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	mock.ExpectExec(`CREATE DATABASE "synapse" OWNER "synapse" TEMPLATE "template0" ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C'`).
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

func TestCreateDatabaseWithLocale(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name:    "locdb",
		Options: config.DatabaseOptions{Locale: "C", Template: "template0"},
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("locdb").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	mock.ExpectExec(`CREATE DATABASE "locdb" TEMPLATE "template0" LOCALE 'C'`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{adminDB: mockDB}
	if _, err := p.ensureDatabase(context.Background(), database, "update"); err != nil {
		t.Fatalf("ensureDatabase: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestEnsureDatabaseExistingWithOptionsWarns verifies that options on an
// already-existing database don't trigger any DDL beyond the normal owner
// reconcile — create-time options can't be ALTERed, so we warn and continue.
func TestEnsureDatabaseExistingWithOptionsWarns(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name:    "synapse",
		Owner:   "synapse",
		Options: config.DatabaseOptions{LcCollate: "C", Template: "template0"},
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("synapse").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	// Only the owner reconcile runs; no CREATE/ALTER for locale/template.
	mock.ExpectExec(`ALTER DATABASE "synapse" OWNER TO "synapse"`).
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

func TestApplyGrantNoTarget(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	grant := config.Grant{
		Role:       "reader",
		Privileges: []string{"SELECT"},
		// No target specified
	}

	database := config.Database{Name: "mydb"}
	p := &Provisioner{}
	err = p.applyGrant(context.Background(), mockDB, database, grant)
	if err == nil {
		t.Fatal("expected error for grant with no target")
	}
	if err.Error() != "no grant target specified" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGrantOnTablesInSchemaNoOwner(t *testing.T) {
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

	mock.ExpectBegin()
	// Grant on existing tables
	mock.ExpectExec(`GRANT SELECT ON ALL TABLES IN SCHEMA "app" TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// Default privileges for admin user
	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES IN SCHEMA "app" GRANT SELECT ON TABLES TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// No third exec — empty schemaOwner skips the FOR ROLE clause
	mock.ExpectCommit()

	database := config.Database{
		Name: "mydb",
		// No owner
	}
	p := &Provisioner{}
	if err := p.applyGrant(context.Background(), mockDB, database, grant); err != nil {
		t.Fatalf("applyGrant: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnTablesInSchemaWithSchemaOwnerOverride(t *testing.T) {
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

	mock.ExpectBegin()
	mock.ExpectExec(`GRANT SELECT ON ALL TABLES IN SCHEMA "app" TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES IN SCHEMA "app" GRANT SELECT ON TABLES TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES FOR ROLE "schema_owner" IN SCHEMA "app" GRANT SELECT ON TABLES TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	database := config.Database{
		Name:  "mydb",
		Owner: "db_owner",
		Schemas: []config.Schema{
			{Name: "app", Owner: "schema_owner"},
		},
	}
	p := &Provisioner{}
	if err := p.applyGrant(context.Background(), mockDB, database, grant); err != nil {
		t.Fatalf("applyGrant: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureSchemaNoOwner(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// No ALTER SCHEMA expected — empty owner

	p := &Provisioner{}
	if err := p.ensureSchema(context.Background(), mockDB, "app", "", "update"); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionDatabaseResources(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name:       "testdb",
		Owner:      "appuser",
		Extensions: []string{"uuid-ossp"},
		Schemas: []config.Schema{
			{Name: "app", Owner: "appuser"},
		},
		Grants: []config.Grant{
			{
				Role:       "appuser",
				Privileges: []string{"ALL"},
				OnSchema:   "app",
			},
		},
	}

	// Extension
	mock.ExpectExec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// Schema
	mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER SCHEMA "app" OWNER TO "appuser"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// Grant
	mock.ExpectExec(`GRANT ALL ON SCHEMA "app" TO "appuser"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{opts: DefaultOptions()}
	if err := p.provisionDatabaseResources(context.Background(), mockDB, database, "update"); err != nil {
		t.Fatalf("provisionDatabaseResources: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionDatabaseResourcesMigrationsDisabled(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name: "testdb",
		Migrations: &config.Migrations{
			Directory: "/some/path",
		},
	}

	p := &Provisioner{opts: Options{MigrationsEnabled: false}}
	if err := p.provisionDatabaseResources(context.Background(), mockDB, database, "update"); err != nil {
		t.Fatalf("provisionDatabaseResources: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionDatabaseResourcesExtensionError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name:       "testdb",
		Extensions: []string{"bad_ext"},
	}

	mock.ExpectExec(`CREATE EXTENSION IF NOT EXISTS "bad_ext"`).
		WillReturnError(fmt.Errorf("extension not available"))

	p := &Provisioner{opts: DefaultOptions()}
	err = p.provisionDatabaseResources(context.Background(), mockDB, database, "update")
	if err == nil {
		t.Fatal("expected error for failed extension")
	}
}

func TestProvisionDatabaseResourcesSchemaError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name:    "testdb",
		Schemas: []config.Schema{{Name: "bad_schema"}},
	}

	mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "bad_schema"`).
		WillReturnError(fmt.Errorf("permission denied"))

	p := &Provisioner{opts: DefaultOptions()}
	err = p.provisionDatabaseResources(context.Background(), mockDB, database, "update")
	if err == nil {
		t.Fatal("expected error for failed schema creation")
	}
}

func TestProvisionDatabaseResourcesGrantError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name: "testdb",
		Grants: []config.Grant{
			{
				Role:       "baduser",
				Privileges: []string{"ALL"},
				OnSchema:   "public",
			},
		},
	}

	mock.ExpectExec(`GRANT ALL ON SCHEMA "public" TO "baduser"`).
		WillReturnError(fmt.Errorf("role does not exist"))

	p := &Provisioner{opts: DefaultOptions()}
	err = p.provisionDatabaseResources(context.Background(), mockDB, database, "update")
	if err == nil {
		t.Fatal("expected error for failed grant")
	}
}

func TestProvisionDatabaseResourcesSchemaOwnerFallback(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	// Schema with no owner should fall back to database owner
	database := config.Database{
		Name:  "testdb",
		Owner: "db_owner",
		Schemas: []config.Schema{
			{Name: "app"}, // no owner specified
		},
	}

	mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER SCHEMA "app" OWNER TO "db_owner"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{opts: DefaultOptions()}
	if err := p.provisionDatabaseResources(context.Background(), mockDB, database, "update"); err != nil {
		t.Fatalf("provisionDatabaseResources: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnTablesInSchemaGrantError(t *testing.T) {
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

	mock.ExpectBegin()
	mock.ExpectExec(`GRANT SELECT ON ALL TABLES IN SCHEMA "app" TO "reader"`).
		WillReturnError(fmt.Errorf("schema does not exist"))
	mock.ExpectRollback()

	database := config.Database{Name: "mydb", Owner: "owner"}
	p := &Provisioner{}
	err = p.applyGrant(context.Background(), mockDB, database, grant)
	if err == nil {
		t.Fatal("expected error for failed grant on tables")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnTablesInSchemaDefaultPrivError(t *testing.T) {
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

	mock.ExpectBegin()
	mock.ExpectExec(`GRANT SELECT ON ALL TABLES IN SCHEMA "app" TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES IN SCHEMA "app" GRANT SELECT ON TABLES TO "reader"`).
		WillReturnError(fmt.Errorf("permission denied"))
	mock.ExpectRollback()

	database := config.Database{Name: "mydb", Owner: "owner"}
	p := &Provisioner{}
	err = p.applyGrant(context.Background(), mockDB, database, grant)
	if err == nil {
		t.Fatal("expected error for failed default privileges")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnTablesInSchemaOwnerDefaultPrivError(t *testing.T) {
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

	mock.ExpectBegin()
	mock.ExpectExec(`GRANT SELECT ON ALL TABLES IN SCHEMA "app" TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES IN SCHEMA "app" GRANT SELECT ON TABLES TO "reader"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES FOR ROLE "owner" IN SCHEMA "app" GRANT SELECT ON TABLES TO "reader"`).
		WillReturnError(fmt.Errorf("role does not exist"))
	mock.ExpectRollback()

	database := config.Database{Name: "mydb", Owner: "owner"}
	p := &Provisioner{}
	err = p.applyGrant(context.Background(), mockDB, database, grant)
	if err == nil {
		t.Fatal("expected error for failed owner default privileges")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureSchemaCreateError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "bad"`).
		WillReturnError(fmt.Errorf("permission denied"))

	p := &Provisioner{}
	err = p.ensureSchema(context.Background(), mockDB, "bad", "owner", "update")
	if err == nil {
		t.Fatal("expected error for failed schema creation")
	}
}

func TestEnsureSchemaAlterOwnerError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER SCHEMA "app" OWNER TO "baduser"`).
		WillReturnError(fmt.Errorf("role does not exist"))

	p := &Provisioner{}
	err = p.ensureSchema(context.Background(), mockDB, "app", "baduser", "update")
	if err == nil {
		t.Fatal("expected error for failed ALTER SCHEMA OWNER")
	}
}

func TestProvisionDatabaseResourcesWithMigrations(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()
	content := "CREATE TABLE t (id int);"
	if writeErr := os.WriteFile(dir+"/V0001__init.sql", []byte(content), 0644); writeErr != nil {
		t.Fatal(writeErr)
	}

	database := config.Database{
		Name: "testdb",
		Migrations: &config.Migrations{
			Directory: dir,
		},
	}

	// ensureMigrationTable
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS _schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))

	// loadAppliedMigrations - empty
	mock.ExpectQuery("SELECT version, type, checksum FROM _schema_migrations").
		WillReturnRows(sqlmock.NewRows([]string{"version", "type", "checksum"}))

	// executeMigration
	hash := sha256.Sum256([]byte(content))
	checksum := hex.EncodeToString(hash[:])

	mock.ExpectBegin()
	mock.ExpectExec("CREATE TABLE t").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO _schema_migrations").
		WithArgs("0001", "versioned", "init", "V0001__init.sql", checksum).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	p := &Provisioner{opts: Options{MigrationsEnabled: true}}
	if err := p.provisionDatabaseResources(context.Background(), mockDB, database, "update"); err != nil {
		t.Fatalf("provisionDatabaseResources: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionDatabaseResourcesMigrationsError(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name: "testdb",
		Migrations: &config.Migrations{
			Directory: "/nonexistent/path",
		},
	}

	p := &Provisioner{opts: Options{MigrationsEnabled: true}}
	err = p.provisionDatabaseResources(context.Background(), mockDB, database, "update")
	if err == nil {
		t.Fatal("expected error for failed migrations")
	}
}

func TestEnsureRoleExistsQueryError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("testuser").
		WillReturnError(fmt.Errorf("connection lost"))

	p := &Provisioner{adminDB: mockDB}
	_, err = p.ensureRole(context.Background(), config.Role{Name: "testuser"}, "update")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEnsureDatabaseExistsQueryError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("testdb").
		WillReturnError(fmt.Errorf("connection lost"))

	p := &Provisioner{adminDB: mockDB}
	_, err = p.ensureDatabase(context.Background(), config.Database{Name: "testdb"}, "update")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEnsureRoleCreateWithOptions(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	boolTrue := true
	boolFalse := false
	connLimit := 5

	role := config.Role{
		Name:     "fulluser",
		Password: "pass",
		Options: config.RoleOptions{
			Login:           &boolTrue,
			Superuser:       &boolFalse,
			CreateDB:        &boolTrue,
			CreateRole:      &boolFalse,
			ConnectionLimit: &connLimit,
		},
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("fulluser").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	mock.ExpectExec(`CREATE ROLE "fulluser" WITH PASSWORD 'pass' LOGIN NOSUPERUSER CREATEDB NOCREATEROLE CONNECTION LIMIT 5`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{adminDB: mockDB}
	if _, err := p.ensureRole(context.Background(), role, "update"); err != nil {
		t.Fatalf("ensureRole: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRoleAlterWithOptions(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	boolTrue := true
	role := config.Role{
		Name:    "existing",
		Options: config.RoleOptions{Superuser: &boolTrue},
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("existing").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	mock.ExpectExec(`ALTER ROLE "existing" WITH SUPERUSER`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{adminDB: mockDB}
	if _, err := p.ensureRole(context.Background(), role, "update"); err != nil {
		t.Fatalf("ensureRole: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRoleNoPassword(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	login := true
	role := config.Role{
		Name:    "nopwuser",
		Options: config.RoleOptions{Login: &login},
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("nopwuser").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	mock.ExpectExec(`CREATE ROLE "nopwuser" LOGIN`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{adminDB: mockDB}
	if _, err := p.ensureRole(context.Background(), role, "update"); err != nil {
		t.Fatalf("ensureRole: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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
	p := New(mockDB, db.ConnConfig{}, cfg, DefaultOptions())

	ctx := context.Background()
	for _, role := range cfg.Roles {
		strategy := config.EffectiveStrategy(role.Strategy, cfg.Strategy)
		if _, err := p.ensureRole(ctx, role, strategy); err != nil {
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

// dryRunOptions returns Options with dry-run enabled and migrations enabled by default.
func dryRunOptions() Options {
	opts := DefaultOptions()
	opts.DryRun = true
	return opts
}

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "create role with password",
			in:   `CREATE ROLE "alice" WITH PASSWORD 's3cret'`,
			want: `CREATE ROLE "alice" WITH PASSWORD '***REDACTED***'`,
		},
		{
			name: "alter role with password and option",
			in:   `ALTER ROLE "alice" WITH PASSWORD 'rotated' LOGIN`,
			want: `ALTER ROLE "alice" WITH PASSWORD '***REDACTED***' LOGIN`,
		},
		{
			name: "password literal containing escaped single-quote",
			in:   `ALTER ROLE "x" WITH PASSWORD 'it''s'`,
			want: `ALTER ROLE "x" WITH PASSWORD '***REDACTED***'`,
		},
		{
			name: "case-insensitive PASSWORD keyword",
			in:   `password 'lower'`,
			want: `password '***REDACTED***'`,
		},
		{
			name: "no password clause is unchanged",
			in:   `GRANT SELECT ON TABLE "x" TO "y"`,
			want: `GRANT SELECT ON TABLE "x" TO "y"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.in)
			if got != tc.want {
				t.Errorf("redactSecrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestDryRunRedactsPasswordInLog verifies that the SQL captured by execMutation
// in dry-run mode does not leak the role password into the slog payload.
func TestDryRunRedactsPasswordInLog(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	// No ExpectExec — dry-run must not execute, but we capture the redacted SQL via slog handler.

	var captured []string
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == "sql" {
				captured = append(captured, a.Value.String())
			}
			return a
		},
	})))

	role := config.Role{Name: "alice", Password: "super-secret-pw"}
	p := &Provisioner{adminDB: mockDB, opts: dryRunOptions()}
	if _, err := p.ensureRole(context.Background(), role, "update"); err != nil {
		t.Fatalf("ensureRole: %v", err)
	}

	if len(captured) != 1 {
		t.Fatalf("expected 1 captured sql attr, got %d: %v", len(captured), captured)
	}
	if strings.Contains(captured[0], "super-secret-pw") {
		t.Errorf("dry-run log leaked password literal: %q", captured[0])
	}
	if !strings.Contains(captured[0], "***REDACTED***") {
		t.Errorf("expected redaction marker in dry-run log: %q", captured[0])
	}
}

func TestDryRunRoleCreate(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	// Existence check still runs
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	// CREATE ROLE must NOT be executed in dry-run mode

	role := config.Role{Name: "alice", Password: "s3cret"}
	p := &Provisioner{adminDB: mockDB, opts: dryRunOptions()}
	if _, err := p.ensureRole(context.Background(), role, "update"); err != nil {
		t.Fatalf("ensureRole: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunRoleAlter(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	// ALTER ROLE must NOT be executed in dry-run mode

	role := config.Role{Name: "alice", Password: "rotated"}
	p := &Provisioner{adminDB: mockDB, opts: dryRunOptions()}
	if _, err := p.ensureRole(context.Background(), role, "update"); err != nil {
		t.Fatalf("ensureRole: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunDatabaseCreate(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("appdb").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	// CREATE DATABASE must NOT be executed in dry-run mode

	database := config.Database{Name: "appdb", Owner: "appuser"}
	p := &Provisioner{adminDB: mockDB, opts: dryRunOptions()}
	created, err := p.ensureDatabase(context.Background(), database, "update")
	if err != nil {
		t.Fatalf("ensureDatabase: %v", err)
	}
	if !created {
		t.Errorf("expected created=true (the create was previewed), got false")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunSchemaAndExtension(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	// No Exec expectations — both must be skipped in dry-run mode.
	p := &Provisioner{opts: dryRunOptions()}

	if err := p.ensureExtension(context.Background(), mockDB, "uuid-ossp"); err != nil {
		t.Fatalf("ensureExtension: %v", err)
	}
	if err := p.ensureSchema(context.Background(), mockDB, "app", "appuser", "update"); err != nil {
		t.Fatalf("ensureSchema: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunGrantOnTablesInSchemaSkipsTransaction(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	// No ExpectBegin / ExpectExec / ExpectCommit — dry-run must not open a transaction.
	grant := config.Grant{
		Role:             "reader",
		Privileges:       []string{"SELECT"},
		OnTablesInSchema: "app",
	}
	database := config.Database{Name: "mydb", Owner: "appowner"}
	p := &Provisioner{opts: dryRunOptions()}
	if err := p.applyGrant(context.Background(), mockDB, database, grant); err != nil {
		t.Fatalf("applyGrant: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunMigrationsLogsPlanWithoutExecuting(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	// Two migration files in a temp dir — discoverMigrations reads them.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "V0001__init.sql"), []byte("SELECT 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "R0001__view.sql"), []byte("SELECT 2;\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// loadAppliedMigrations is read-only and runs even in dry-run; simulate the precise
	// "undefined table" Postgres error (SQLSTATE 42P01) so the dry-run plan reports both
	// migrations as "would apply". Any other error must propagate (see the test below).
	mock.ExpectQuery(`SELECT version, type, checksum FROM _schema_migrations`).
		WillReturnError(&pq.Error{Code: pgUndefinedTable, Message: `relation "_schema_migrations" does not exist`})

	// No CREATE TABLE, no BeginTx, no Exec — dry-run must not mutate.
	p := &Provisioner{opts: dryRunOptions()}
	if err := p.runMigrations(context.Background(), mockDB, "appdb", config.Migrations{Directory: dir}); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunMigrationsPropagatesNonMissingTableErrors(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "V0001__init.sql"), []byte("SELECT 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A permission-denied error (or any error other than 42P01) must be surfaced —
	// silently treating it as "no applied migrations" would mask real problems and
	// produce a misleading dry-run plan.
	wantErr := &pq.Error{Code: "42501", Message: "permission denied for table _schema_migrations"}
	mock.ExpectQuery(`SELECT version, type, checksum FROM _schema_migrations`).
		WillReturnError(wantErr)

	p := &Provisioner{opts: dryRunOptions()}
	err = p.runMigrations(context.Background(), mockDB, "appdb", config.Migrations{Directory: dir})
	if err == nil {
		t.Fatal("expected error to propagate, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped pq.Error to propagate, got: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

const membershipQuery = `SELECT EXISTS\(\s*SELECT 1 FROM pg_auth_members m`

func TestEnsureMembershipsGrantsMissingMembership(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(membershipQuery).
		WithArgs("exporter", "pg_monitor").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`GRANT "pg_monitor" TO "exporter"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{adminDB: mockDB}
	role := config.Role{Name: "exporter", MemberOf: []string{"pg_monitor"}}
	if err := p.ensureMemberships(context.Background(), role); err != nil {
		t.Fatalf("ensureMemberships: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureMembershipsSkipsExistingMembership(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(membershipQuery).
		WithArgs("exporter", "pg_monitor").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	// No GRANT expected

	p := &Provisioner{adminDB: mockDB}
	role := config.Role{Name: "exporter", MemberOf: []string{"pg_monitor"}}
	if err := p.ensureMemberships(context.Background(), role); err != nil {
		t.Fatalf("ensureMemberships: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureMembershipsPropagatesGrantError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(membershipQuery).
		WithArgs("exporter", "missing_role").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`GRANT "missing_role" TO "exporter"`).
		WillReturnError(fmt.Errorf(`role "missing_role" does not exist`))

	p := &Provisioner{adminDB: mockDB}
	role := config.Role{Name: "exporter", MemberOf: []string{"missing_role"}}
	err = p.ensureMemberships(context.Background(), role)
	if err == nil || !strings.Contains(err.Error(), "missing_role") {
		t.Fatalf("expected grant error mentioning missing_role, got %v", err)
	}
}

func TestDryRunMembershipDoesNotExecute(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(membershipQuery).
		WithArgs("exporter", "pg_monitor").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	// GRANT must NOT be executed in dry-run mode

	p := &Provisioner{adminDB: mockDB, opts: dryRunOptions()}
	role := config.Role{Name: "exporter", MemberOf: []string{"pg_monitor"}}
	if err := p.ensureMemberships(context.Background(), role); err != nil {
		t.Fatalf("ensureMemberships: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRunAppliesMembershipsAfterAllRoles verifies that a role can be a member of a
// declared role listed later in the config, because memberships run in a second pass.
func TestRunAppliesMembershipsAfterAllRoles(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	cfg := &config.Config{
		Roles: []config.Role{
			{Name: "alice", MemberOf: []string{"readers"}},
			{Name: "readers"},
		},
	}

	for _, name := range []string{"alice", "readers"} {
		mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
			WithArgs(name).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectExec(`CREATE ROLE "` + name + `"`).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectQuery(membershipQuery).
		WithArgs("alice", "readers").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`GRANT "readers" TO "alice"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := New(mockDB, db.ConnConfig{}, cfg, DefaultOptions())
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunCreateStrategySkipsMembershipsForExistingRole(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	cfg := &config.Config{
		Strategy: "create",
		Roles: []config.Role{
			{Name: "exporter", MemberOf: []string{"pg_monitor"}},
		},
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("exporter").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	// No membership check or GRANT expected

	p := New(mockDB, db.ConnConfig{}, cfg, DefaultOptions())
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunCreateStrategyGrantsMembershipsForNewRole(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	cfg := &config.Config{
		Strategy: "create",
		Roles: []config.Role{
			{Name: "exporter", MemberOf: []string{"pg_monitor"}},
		},
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("exporter").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`CREATE ROLE "exporter"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(membershipQuery).
		WithArgs("exporter", "pg_monitor").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(`GRANT "pg_monitor" TO "exporter"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := New(mockDB, db.ConnConfig{}, cfg, DefaultOptions())
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnSequencesInSchema(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	grant := config.Grant{Role: "app", Privileges: []string{"USAGE", "SELECT"}, OnSequencesInSchema: "app"}

	mock.ExpectBegin()
	mock.ExpectExec(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA "app" TO "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES IN SCHEMA "app" GRANT USAGE, SELECT ON SEQUENCES TO "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES FOR ROLE "appowner" IN SCHEMA "app" GRANT USAGE, SELECT ON SEQUENCES TO "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	p := &Provisioner{}
	database := config.Database{Name: "mydb", Owner: "appowner"}
	if err := p.applyGrant(context.Background(), mockDB, database, grant); err != nil {
		t.Fatalf("applyGrant: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnFunctionsInSchema(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	grant := config.Grant{Role: "app", Privileges: []string{"EXECUTE"}, OnFunctionsInSchema: "app"}

	mock.ExpectBegin()
	mock.ExpectExec(`GRANT EXECUTE ON ALL ROUTINES IN SCHEMA "app" TO "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES IN SCHEMA "app" GRANT EXECUTE ON FUNCTIONS TO "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES FOR ROLE "fnowner" IN SCHEMA "app" GRANT EXECUTE ON FUNCTIONS TO "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	p := &Provisioner{}
	database := config.Database{
		Name:    "mydb",
		Owner:   "dbowner",
		Schemas: []config.Schema{{Name: "app", Owner: "fnowner"}},
	}
	if err := p.applyGrant(context.Background(), mockDB, database, grant); err != nil {
		t.Fatalf("applyGrant: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnSequencesInSchemaDefaultPrivErrorRollsBack(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	grant := config.Grant{Role: "app", Privileges: []string{"USAGE"}, OnSequencesInSchema: "app"}

	mock.ExpectBegin()
	mock.ExpectExec(`GRANT USAGE ON ALL SEQUENCES IN SCHEMA "app" TO "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER DEFAULT PRIVILEGES IN SCHEMA "app" GRANT USAGE ON SEQUENCES TO "app"`).
		WillReturnError(fmt.Errorf("boom"))
	mock.ExpectRollback()

	p := &Provisioner{}
	err = p.applyGrant(context.Background(), mockDB, config.Database{Name: "mydb"}, grant)
	if err == nil || !strings.Contains(err.Error(), "alter default privileges") {
		t.Fatalf("expected default privileges error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunGrantOnSequencesSkipsTransaction(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	// No expectations: dry-run must neither begin a transaction nor execute.

	grant := config.Grant{Role: "app", Privileges: []string{"USAGE"}, OnSequencesInSchema: "app"}
	p := &Provisioner{opts: dryRunOptions()}
	if err := p.applyGrant(context.Background(), mockDB, config.Database{Name: "mydb", Owner: "o"}, grant); err != nil {
		t.Fatalf("applyGrant: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

const (
	databaseExistsQuery = `SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`
	databaseACLQuery    = `SELECT a.privilege_type\s+FROM pg_database o`
	schemaExistsQuery   = `SELECT EXISTS\(SELECT 1 FROM pg_namespace WHERE nspname = \$1\)`
	schemaACLQuery      = `SELECT a.privilege_type\s+FROM pg_namespace o`
	roleOIDQuery        = `SELECT oid FROM pg_roles WHERE rolname = \$1`
)

func privilegeRows(privileges ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"privilege_type"})
	for _, p := range privileges {
		rows.AddRow(p)
	}
	return rows
}

func TestRevokeOnDatabasePublicUsesBareKeyword(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	// Pre-check: PUBLIC (oid 0) holds the default CONNECT + TEMPORARY.
	mock.ExpectQuery(databaseExistsQuery).WithArgs("myapp").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(databaseACLQuery).WithArgs("myapp", int64(0)).
		WillReturnRows(privilegeRows("CONNECT", "TEMPORARY"))
	mock.ExpectExec(`^REVOKE CONNECT ON DATABASE "myapp" FROM PUBLIC$`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// Post-check: nothing left.
	mock.ExpectQuery(databaseExistsQuery).WithArgs("myapp").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(databaseACLQuery).WithArgs("myapp", int64(0)).
		WillReturnRows(privilegeRows("TEMPORARY"))

	p := &Provisioner{}
	revoke := config.Grant{Role: "public", Privileges: []string{"CONNECT"}, OnDatabase: true}
	if err := p.applyRevoke(context.Background(), mockDB, config.Database{Name: "myapp"}, revoke); err != nil {
		t.Fatalf("applyRevoke: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeOnlyRevokesHeldPrivileges(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(schemaExistsQuery).WithArgs("data").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(roleOIDQuery).WithArgs("app").
		WillReturnRows(sqlmock.NewRows([]string{"oid"}).AddRow(int64(16384)))
	mock.ExpectQuery(schemaACLQuery).WithArgs("data", int64(16384)).
		WillReturnRows(privilegeRows("USAGE"))
	mock.ExpectExec(`^REVOKE USAGE ON SCHEMA "data" FROM "app"$`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(schemaExistsQuery).WithArgs("data").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(roleOIDQuery).WithArgs("app").
		WillReturnRows(sqlmock.NewRows([]string{"oid"}).AddRow(int64(16384)))
	mock.ExpectQuery(schemaACLQuery).WithArgs("data", int64(16384)).
		WillReturnRows(privilegeRows())

	p := &Provisioner{}
	revoke := config.Grant{Role: "app", Privileges: []string{"ALL"}, OnSchema: "data"}
	if err := p.applyRevoke(context.Background(), mockDB, config.Database{Name: "myapp"}, revoke); err != nil {
		t.Fatalf("applyRevoke: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeSkipsWhenNotHeld(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(schemaExistsQuery).WithArgs("public").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(schemaACLQuery).WithArgs("public", int64(0)).
		WillReturnRows(privilegeRows("USAGE"))
	// CREATE isn't held: no REVOKE expected.

	p := &Provisioner{}
	revoke := config.Grant{Role: "PUBLIC", Privileges: []string{"CREATE"}, OnSchema: "public"}
	if err := p.applyRevoke(context.Background(), mockDB, config.Database{Name: "myapp"}, revoke); err != nil {
		t.Fatalf("applyRevoke: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeMissingSchemaFails(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(schemaExistsQuery).WithArgs("nope").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	p := &Provisioner{}
	revoke := config.Grant{Role: "PUBLIC", Privileges: []string{"CREATE"}, OnSchema: "nope"}
	err = p.applyRevoke(context.Background(), mockDB, config.Database{Name: "myapp"}, revoke)
	if err == nil || !strings.Contains(err.Error(), `schema "nope" does not exist`) {
		t.Fatalf("expected missing schema error, got %v", err)
	}
}

func TestDryRunRevokeDoesNotExecute(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(databaseExistsQuery).WithArgs("myapp").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(databaseACLQuery).WithArgs("myapp", int64(0)).
		WillReturnRows(privilegeRows("CONNECT"))
	// No REVOKE and no post-check in dry-run.

	p := &Provisioner{opts: dryRunOptions()}
	revoke := config.Grant{Role: "PUBLIC", Privileges: []string{"CONNECT"}, OnDatabase: true}
	if err := p.applyRevoke(context.Background(), mockDB, config.Database{Name: "myapp"}, revoke); err != nil {
		t.Fatalf("applyRevoke: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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

// TestRevokeWarnsWhenPrivilegeSurvives covers a privilege that another grantor gave to the
// grantee: REVOKE succeeds but leaves it in place, which must be surfaced as a warning.
func TestRevokeWarnsWhenPrivilegeSurvives(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(databaseExistsQuery).WithArgs("myapp").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(databaseACLQuery).WithArgs("myapp", int64(0)).
		WillReturnRows(privilegeRows("CONNECT", "TEMPORARY"))
	mock.ExpectExec(`^REVOKE CONNECT, TEMPORARY ON DATABASE "myapp" FROM PUBLIC$`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// Post-check: CONNECT is still held through another grantor.
	mock.ExpectQuery(databaseExistsQuery).WithArgs("myapp").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(databaseACLQuery).WithArgs("myapp", int64(0)).
		WillReturnRows(privilegeRows("CONNECT"))

	logs := captureLogs(t)
	p := &Provisioner{}
	revoke := config.Grant{Role: "PUBLIC", Privileges: []string{"CONNECT", "TEMPORARY"}, OnDatabase: true}
	if err := p.applyRevoke(context.Background(), mockDB, config.Database{Name: "myapp"}, revoke); err != nil {
		t.Fatalf("applyRevoke: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "different grantor") {
		t.Fatalf("expected different-grantor warning, got logs:\n%s", out)
	}
	if !strings.Contains(out, "privileges=CONNECT\n") {
		t.Errorf("expected warning to list only CONNECT, got logs:\n%s", out)
	}
}

func TestRevokeDependentPrivilegesErrorHasHint(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(databaseExistsQuery).WithArgs("myapp").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(roleOIDQuery).WithArgs("app").
		WillReturnRows(sqlmock.NewRows([]string{"oid"}).AddRow(int64(16384)))
	mock.ExpectQuery(databaseACLQuery).WithArgs("myapp", int64(16384)).
		WillReturnRows(privilegeRows("CONNECT"))
	pqErr := &pq.Error{Code: "2BP01", Message: "dependent privileges exist"}
	mock.ExpectExec(`^REVOKE CONNECT ON DATABASE "myapp" FROM "app"$`).WillReturnError(pqErr)

	p := &Provisioner{}
	revoke := config.Grant{Role: "app", Privileges: []string{"CONNECT"}, OnDatabase: true}
	err = p.applyRevoke(context.Background(), mockDB, config.Database{Name: "myapp"}, revoke)
	if !errors.Is(err, pqErr) {
		t.Fatalf("expected wrapped pq.Error, got %v", err)
	}
	if !strings.Contains(err.Error(), "WITH GRANT OPTION") || !strings.Contains(err.Error(), "CASCADE") {
		t.Errorf("expected hint about grant option and CASCADE, got %v", err)
	}
}

// captureSQL records the SQL that dry-run execMutation logs until the test ends.
func captureSQL(t *testing.T) *[]string {
	t.Helper()
	var captured []string
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == "sql" {
				captured = append(captured, a.Value.String())
			}
			return a
		},
	})))
	return &captured
}

// TestDryRunRevokeOnNewSchema previews revokes on a schema that this run would create.
// A new schema only grants to its owner, so only revokes from the owner would run.
func TestDryRunRevokeOnNewSchema(t *testing.T) {
	database := config.Database{
		Name:    "myapp",
		Owner:   "db_owner",
		Schemas: []config.Schema{{Name: "data", Owner: "schema_owner"}, {Name: "inherits"}},
	}
	tests := []struct {
		name    string
		revoke  config.Grant
		wantSQL string // empty: nothing to revoke
	}{
		{
			name:   "PUBLIC holds nothing",
			revoke: config.Grant{Role: "PUBLIC", Privileges: []string{"ALL"}, OnSchema: "data"},
		},
		{
			name:   "non-owner holds nothing",
			revoke: config.Grant{Role: "db_owner", Privileges: []string{"ALL"}, OnSchema: "data"},
		},
		{
			name:    "schema owner holds everything",
			revoke:  config.Grant{Role: "schema_owner", Privileges: []string{"ALL"}, OnSchema: "data"},
			wantSQL: `REVOKE CREATE, USAGE ON SCHEMA "data" FROM "schema_owner"`,
		},
		{
			name:    "owner falls back to database owner",
			revoke:  config.Grant{Role: "db_owner", Privileges: []string{"CREATE"}, OnSchema: "inherits"},
			wantSQL: `REVOKE CREATE ON SCHEMA "inherits" FROM "db_owner"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer mockDB.Close()
			mock.ExpectQuery(schemaExistsQuery).WithArgs(tt.revoke.OnSchema).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

			captured := captureSQL(t)
			p := &Provisioner{opts: dryRunOptions()}
			if err := p.applyRevoke(context.Background(), mockDB, database, tt.revoke); err != nil {
				t.Fatalf("applyRevoke: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}

			var want []string
			if tt.wantSQL != "" {
				want = []string{tt.wantSQL}
			}
			if strings.Join(*captured, "\n") != strings.Join(want, "\n") {
				t.Errorf("expected previewed SQL %q, got %q", want, *captured)
			}
		})
	}
}

// TestDryRunRevokeOnNewSchemaWithoutOwner covers a schema owned by the provisioning user.
func TestDryRunRevokeOnNewSchemaWithoutOwner(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	mock.ExpectQuery(schemaExistsQuery).WithArgs("data").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	captured := captureSQL(t)
	p := &Provisioner{opts: dryRunOptions(), connCfg: db.ConnConfig{User: "admin"}}
	database := config.Database{Name: "myapp", Schemas: []config.Schema{{Name: "data"}}}
	revoke := config.Grant{Role: "admin", Privileges: []string{"USAGE"}, OnSchema: "data"}
	if err := p.applyRevoke(context.Background(), mockDB, database, revoke); err != nil {
		t.Fatalf("applyRevoke: %v", err)
	}
	want := `REVOKE USAGE ON SCHEMA "data" FROM "admin"`
	if len(*captured) != 1 || (*captured)[0] != want {
		t.Errorf("expected previewed SQL %q, got %q", want, *captured)
	}
}

// TestDryRunRevokeOnUndeclaredMissingSchemaFails: a schema the run doesn't create would
// make the real run fail, so the dry run reports the same error.
func TestDryRunRevokeOnUndeclaredMissingSchemaFails(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	mock.ExpectQuery(schemaExistsQuery).WithArgs("nope").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	p := &Provisioner{opts: dryRunOptions()}
	revoke := config.Grant{Role: "PUBLIC", Privileges: []string{"CREATE"}, OnSchema: "nope"}
	err = p.applyRevoke(context.Background(), mockDB, config.Database{Name: "myapp"}, revoke)
	if err == nil || !strings.Contains(err.Error(), `schema "nope" does not exist`) {
		t.Fatalf("expected missing schema error, got %v", err)
	}
}

// TestDryRunRevokeOnNewDatabase previews against a new database's defaults: PUBLIC holds
// CONNECT and TEMPORARY, but not CREATE.
func TestDryRunRevokeOnNewDatabase(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	mock.ExpectQuery(databaseExistsQuery).WithArgs("myapp").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	captured := captureSQL(t)
	p := &Provisioner{opts: dryRunOptions()}
	revoke := config.Grant{Role: "PUBLIC", Privileges: []string{"ALL"}, OnDatabase: true}
	if err := p.applyRevoke(context.Background(), mockDB, config.Database{Name: "myapp"}, revoke); err != nil {
		t.Fatalf("applyRevoke: %v", err)
	}
	want := `REVOKE CONNECT, TEMPORARY ON DATABASE "myapp" FROM PUBLIC`
	if len(*captured) != 1 || (*captured)[0] != want {
		t.Errorf("expected previewed SQL %q, got %q", want, *captured)
	}
}

const (
	toRegclassQuery         = `SELECT to_regclass\(\$1\)::text`
	caseInsensitiveMatchSQL = `SELECT n\.nspname, c\.relname FROM pg_class c`
)

func TestGrantOnTablesWithColumns(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(toRegclassQuery).WithArgs(`"app"."users"`).
		WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow("app.users"))
	mock.ExpectQuery(toRegclassQuery).WithArgs(`"App"."Orders"`).
		WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow(`"App"."Orders"`))
	mock.ExpectExec(`^GRANT SELECT \("id", "email"\), UPDATE \("id", "email"\) ON TABLE "app"\."users", "App"\."Orders" TO "reporting"$`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{}
	grant := config.Grant{
		Role:       "reporting",
		Privileges: []string{"SELECT", "UPDATE"},
		OnTables:   []string{"app.users", "App.Orders"},
		Columns:    []string{"id", "email"},
	}
	if err := p.applyGrant(context.Background(), mockDB, config.Database{Name: "mydb"}, grant); err != nil {
		t.Fatalf("applyGrant: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnTablesWithoutColumns(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(toRegclassQuery).WithArgs(`"app"."users"`).
		WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow("app.users"))
	mock.ExpectExec(`^GRANT SELECT, INSERT ON TABLE "app"\."users" TO "app"$`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{}
	grant := config.Grant{Role: "app", Privileges: []string{"SELECT", "INSERT"}, OnTables: []string{"app.users"}}
	if err := p.applyGrant(context.Background(), mockDB, config.Database{Name: "mydb"}, grant); err != nil {
		t.Fatalf("applyGrant: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnTablesMissingTableFails(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(toRegclassQuery).WithArgs(`"app"."ghost"`).
		WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow(nil))
	mock.ExpectQuery(caseInsensitiveMatchSQL).WithArgs("app", "ghost").
		WillReturnRows(sqlmock.NewRows([]string{"nspname", "relname"}))

	p := &Provisioner{}
	grant := config.Grant{Role: "app", Privileges: []string{"SELECT"}, OnTables: []string{"app.ghost"}}
	err = p.applyGrant(context.Background(), mockDB, config.Database{Name: "mydb"}, grant)
	if err == nil || !strings.Contains(err.Error(), "tables not found: app.ghost (on_tables") ||
		!strings.Contains(err.Error(), "names are case-sensitive") {
		t.Fatalf("expected missing table error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnTablesMissingTableHintsCaseInsensitiveMatch(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(toRegclassQuery).WithArgs(`"app"."Users"`).
		WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow(nil))
	mock.ExpectQuery(caseInsensitiveMatchSQL).WithArgs("app", "Users").
		WillReturnRows(sqlmock.NewRows([]string{"nspname", "relname"}).AddRow("app", "users"))

	p := &Provisioner{}
	grant := config.Grant{Role: "app", Privileges: []string{"SELECT"}, OnTables: []string{"app.Users"}}
	err = p.applyGrant(context.Background(), mockDB, config.Database{Name: "mydb"}, grant)
	if err == nil || !strings.Contains(err.Error(), "tables not found: app.Users (did you mean app.users?)") {
		t.Fatalf("expected missing table error with hint, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunObjectGrantMissingTableDoesNotFail(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(toRegclassQuery).WithArgs(`"app"."future"`).
		WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow(nil))
	mock.ExpectQuery(caseInsensitiveMatchSQL).WithArgs("app", "future").
		WillReturnRows(sqlmock.NewRows([]string{"nspname", "relname"}))
	// No GRANT executed in dry-run.

	p := &Provisioner{opts: dryRunOptions()}
	grant := config.Grant{Role: "app", Privileges: []string{"SELECT"}, OnTables: []string{"app.future"}}
	if err := p.applyGrant(context.Background(), mockDB, config.Database{Name: "mydb"}, grant); err != nil {
		t.Fatalf("applyGrant: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestObjectGrantsRunAfterMigrations checks the per-database order: schema-level grants,
// then migrations, then on_tables grants — even when on_tables is listed first.
func TestObjectGrantsRunAfterMigrations(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "V0001__init.sql"), []byte("CREATE TABLE app.users (id int);"), 0o600); err != nil {
		t.Fatal(err)
	}

	database := config.Database{
		Name: "mydb",
		Grants: []config.Grant{
			{Role: "app", Privileges: []string{"SELECT"}, OnTables: []string{"app.users"}},
			{Role: "app", Privileges: []string{"USAGE"}, OnSchema: "app"},
		},
		Migrations: &config.Migrations{Directory: dir},
	}

	mock.ExpectExec(`^GRANT USAGE ON SCHEMA "app" TO "app"$`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS _schema_migrations`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT .+ FROM _schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version", "type", "checksum"}))
	mock.ExpectBegin()
	mock.ExpectExec(`CREATE TABLE app.users`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO _schema_migrations`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(toRegclassQuery).WithArgs(`"app"."users"`).
		WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow("app.users"))
	mock.ExpectExec(`^GRANT SELECT ON TABLE "app"\."users" TO "app"$`).WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{opts: DefaultOptions()}
	if err := p.provisionDatabaseResources(context.Background(), mockDB, database, "update"); err != nil {
		t.Fatalf("provisionDatabaseResources: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
