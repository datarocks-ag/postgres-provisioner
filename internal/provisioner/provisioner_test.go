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

	expectExtensionMissing(mock, "uuid-ossp")
	mock.ExpectExec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{}
	if err := p.ensureExtension(context.Background(), mockDB, config.Extension{Name: "uuid-ossp"}, "update"); err != nil {
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
		Extensions: []config.Extension{{Name: "uuid-ossp"}},
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

	// Schema (before extensions, so extensions can be installed into it)
	mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "app"`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ALTER SCHEMA "app" OWNER TO "appuser"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	// Extension
	expectExtensionMissing(mock, "uuid-ossp")
	mock.ExpectExec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).
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
		Extensions: []config.Extension{{Name: "bad_ext"}},
	}

	expectExtensionMissing(mock, "bad_ext")
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

	// Only the read-only extension lookup runs; no Exec — both must be skipped in dry-run mode.
	expectExtensionMissing(mock, "uuid-ossp")
	p := &Provisioner{opts: dryRunOptions()}

	if err := p.ensureExtension(context.Background(), mockDB, config.Extension{Name: "uuid-ossp"}, "update"); err != nil {
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
	relationKindSQL         = `SELECT relkind::text FROM pg_class WHERE oid = to_regclass\(\$1\)`
)

// expectRelationKind expects the relkind lookup grantOnTables runs for each existing table.
func expectRelationKind(mock sqlmock.Sqlmock, quotedName, kind string) {
	mock.ExpectQuery(relationKindSQL).WithArgs(quotedName).
		WillReturnRows(sqlmock.NewRows([]string{"relkind"}).AddRow(kind))
}

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
	expectRelationKind(mock, `"app"."users"`, "r")
	expectRelationKind(mock, `"App"."Orders"`, "v")
	mock.ExpectExec(`^GRANT SELECT \("id", "email"\), UPDATE \("id", "email"\) ON TABLE "app"\."users", "App"\."Orders" TO "reporting"$`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{}
	grant := config.Grant{
		Role:       "reporting",
		Privileges: []string{"SELECT", "UPDATE"},
		OnTables:   []string{"app.users", "App.Orders"},
		Columns:    []string{"id", "email"},
	}
	if err := p.grantOnTables(context.Background(), mockDB, grant, migrationsRan); err != nil {
		t.Fatalf("grantOnTables: %v", err)
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
	expectRelationKind(mock, `"app"."users"`, "p")
	mock.ExpectExec(`^GRANT SELECT, INSERT ON TABLE "app"\."users" TO "app"$`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{}
	grant := config.Grant{Role: "app", Privileges: []string{"SELECT", "INSERT"}, OnTables: []string{"app.users"}}
	if err := p.grantOnTables(context.Background(), mockDB, grant, migrationsRan); err != nil {
		t.Fatalf("grantOnTables: %v", err)
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
	err = p.grantOnTables(context.Background(), mockDB, grant, migrationsRan)
	if err == nil || !strings.Contains(err.Error(), "tables not found: app.ghost (checked after this run's migrations") ||
		!strings.Contains(err.Error(), "names are case-sensitive") {
		t.Fatalf("expected missing table error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGrantOnTablesRejectsUnsupportedRelationKinds(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	for _, name := range []string{`"app"."users_pkey"`, `"app"."point"`, `"app"."users"`} {
		mock.ExpectQuery(toRegclassQuery).WithArgs(name).
			WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow(name))
	}
	expectRelationKind(mock, `"app"."users_pkey"`, "i")
	expectRelationKind(mock, `"app"."point"`, "c")
	expectRelationKind(mock, `"app"."users"`, "r")
	// No GRANT: an unsupported kind fails before any mutation.

	p := &Provisioner{}
	grant := config.Grant{Role: "app", Privileges: []string{"SELECT"}, OnTables: []string{"app.users_pkey", "app.point", "app.users"}}
	err = p.grantOnTables(context.Background(), mockDB, grant, migrationsRan)
	want := "app.users_pkey is an index; app.point is a composite type (on_tables supports tables, partitioned tables, views, materialized views and foreign tables)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %v", want, err)
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
	err = p.grantOnTables(context.Background(), mockDB, grant, migrationsRan)
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
	if err := p.grantOnTables(context.Background(), mockDB, grant, migrationsRan); err != nil {
		t.Fatalf("grantOnTables: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGrantOnTablesSkipsMissingTablesWhenMigrationsDidNotRun: without migrations in this
// run (disabled, or none configured) a missing table only warns; the grant covers the
// tables that exist.
func TestGrantOnTablesSkipsMissingTablesWhenMigrationsDidNotRun(t *testing.T) {
	tests := []struct {
		name       string
		migrations migrationStatus
		wantReason string
	}{
		{"migrations disabled", migrationsDisabled, "migrations are disabled (MIGRATIONS_ENABLED=false"},
		{"no migrations configured", migrationsNotConfigured, "no migrations are configured for this database"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer mockDB.Close()
			logs := captureLogs(t)

			mock.ExpectQuery(toRegclassQuery).WithArgs(`"app"."users"`).
				WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow("app.users"))
			mock.ExpectQuery(toRegclassQuery).WithArgs(`"app"."orders"`).
				WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow(nil))
			expectRelationKind(mock, `"app"."users"`, "r")
			mock.ExpectQuery(caseInsensitiveMatchSQL).WithArgs("app", "orders").
				WillReturnRows(sqlmock.NewRows([]string{"nspname", "relname"}))
			mock.ExpectExec(`^GRANT SELECT ON TABLE "app"\."users" TO "app"$`).WillReturnResult(sqlmock.NewResult(0, 0))

			p := &Provisioner{opts: DefaultOptions()}
			grant := config.Grant{Role: "app", Privileges: []string{"SELECT"}, OnTables: []string{"app.users", "app.orders"}}
			if err := p.grantOnTables(context.Background(), mockDB, grant, tt.migrations); err != nil {
				t.Fatalf("grantOnTables: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			out := logs.String()
			if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "tables=app.orders") ||
				!strings.Contains(out, tt.wantReason) {
				t.Errorf("expected warning naming app.orders with reason %q, got:\n%s", tt.wantReason, out)
			}
			if strings.Contains(out, "pending migrations") {
				t.Errorf("warning must not mention pending migrations:\n%s", out)
			}
		})
	}
}

func TestGrantOnTablesAllMissingWithoutMigrationsSkipsGrant(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(toRegclassQuery).WithArgs(`"app"."users"`).
		WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow(nil))
	mock.ExpectQuery(caseInsensitiveMatchSQL).WithArgs("app", "users").
		WillReturnRows(sqlmock.NewRows([]string{"nspname", "relname"}))
	// No GRANT: there is no existing table to grant on.

	p := &Provisioner{opts: DefaultOptions()}
	grant := config.Grant{Role: "app", Privileges: []string{"SELECT"}, OnTables: []string{"app.users"}}
	if err := p.grantOnTables(context.Background(), mockDB, grant, migrationsDisabled); err != nil {
		t.Fatalf("grantOnTables: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestProvisionDatabaseResourcesMigrationsDisabledToleratesMissingTables is the
// init-container case: the app runs its own migrations, so on a fresh database the
// on_tables grant must not fail provisioning.
func TestProvisionDatabaseResourcesMigrationsDisabledToleratesMissingTables(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	database := config.Database{
		Name:       "mydb",
		Grants:     []config.Grant{{Role: "app", Privileges: []string{"SELECT"}, OnTables: []string{"app.users"}}},
		Migrations: &config.Migrations{Directory: t.TempDir()},
	}

	// No migration queries: migrations are disabled.
	mock.ExpectQuery(toRegclassQuery).WithArgs(`"app"."users"`).
		WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow(nil))
	mock.ExpectQuery(caseInsensitiveMatchSQL).WithArgs("app", "users").
		WillReturnRows(sqlmock.NewRows([]string{"nspname", "relname"}))

	opts := DefaultOptions()
	opts.MigrationsEnabled = false
	p := &Provisioner{opts: opts}
	if err := p.provisionDatabaseResources(context.Background(), mockDB, database, "update"); err != nil {
		t.Fatalf("provisionDatabaseResources: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestDryRunProvisionDatabaseResourcesPreviewsObjectGrants runs the whole per-database
// phase in dry-run on a fresh database: migrations are only previewed, so the table they
// would create is missing; that must be logged, not fail, and the GRANT is previewed.
func TestDryRunProvisionDatabaseResourcesPreviewsObjectGrants(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	logs := captureLogs(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "V0001__init.sql"), []byte("CREATE TABLE app.users (id int);"), 0o600); err != nil {
		t.Fatal(err)
	}
	database := config.Database{
		Name: "mydb",
		Grants: []config.Grant{
			{Role: "app", Privileges: []string{"SELECT"}, OnTables: []string{"app.users"}, Columns: []string{"id"}},
			{Role: "app", Privileges: []string{"USAGE"}, OnSchema: "app"},
		},
		Migrations: &config.Migrations{Directory: dir},
	}

	// Only read-only queries: no Exec, Begin or Commit in dry-run.
	mock.ExpectQuery(`SELECT version, type, checksum FROM _schema_migrations`).
		WillReturnError(&pq.Error{Code: pgUndefinedTable, Message: `relation "_schema_migrations" does not exist`})
	mock.ExpectQuery(toRegclassQuery).WithArgs(`"app"."users"`).
		WillReturnRows(sqlmock.NewRows([]string{"to_regclass"}).AddRow(nil))
	mock.ExpectQuery(caseInsensitiveMatchSQL).WithArgs("app", "users").
		WillReturnRows(sqlmock.NewRows([]string{"nspname", "relname"}))

	p := &Provisioner{opts: dryRunOptions()}
	if err := p.provisionDatabaseResources(context.Background(), mockDB, database, "update"); err != nil {
		t.Fatalf("provisionDatabaseResources: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	out := logs.String()
	for _, want := range []string{
		`[DRY RUN] would execute" sql="GRANT USAGE ON SCHEMA \"app\" TO \"app\""`,
		`[DRY RUN] would apply migration" file=V0001__init.sql`,
		`[DRY RUN] tables not present yet; a real run fails unless pending migrations create them" tables=app.users`,
		`[DRY RUN] would execute" sql="GRANT SELECT (\"id\") ON TABLE \"app\".\"users\" TO \"app\""`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected log containing %s, got:\n%s", want, out)
		}
	}
	// Order: the table grant is previewed after the migration plan.
	if strings.Index(out, "would apply migration") > strings.Index(out, "ON TABLE") {
		t.Errorf("expected migration preview before table grant:\n%s", out)
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
	expectRelationKind(mock, `"app"."users"`, "r")
	mock.ExpectExec(`^GRANT SELECT ON TABLE "app"\."users" TO "app"$`).WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{opts: DefaultOptions()}
	if err := p.provisionDatabaseResources(context.Background(), mockDB, database, "update"); err != nil {
		t.Fatalf("provisionDatabaseResources: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

const roleSettingsQuery = `SELECT unnest\(s.setconfig\) FROM pg_db_role_setting s\s+JOIN pg_roles r ON r.oid = s.setrole\s+WHERE r.rolname = \$1 AND s.setdatabase = 0`

func settingRows(entries ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"unnest"})
	for _, e := range entries {
		rows.AddRow(e)
	}
	return rows
}

func TestApplySettingsOnlyChangesDifferingValues(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(roleSettingsQuery).WithArgs("app").
		WillReturnRows(settingRows(
			"statement_timeout=30s",
			`search_path=app, "$user", public`,
			"work_mem=4MB",
		))
	// search_path and statement_timeout match; only work_mem changes, lock_timeout resets
	// nothing (not set), and the new jit setting is applied. Names are applied sorted.
	mock.ExpectExec(`^ALTER ROLE "app" SET jit = 'off'$`).WillReturnResult(sqlmock.NewResult(0, 0))
	// The stored search_path contains quotes, so PostgreSQL computes the quoted form.
	mock.ExpectQuery(`SELECT string_agg\(quote_ident\(v\)`).
		WithArgs(pq.Array([]string{"app", "$user", "public"})).
		WillReturnRows(sqlmock.NewRows([]string{"string_agg"}).AddRow(`app, "$user", public`))
	mock.ExpectExec(`^ALTER ROLE "app" SET work_mem = '64MB'$`).WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{adminDB: mockDB}
	desired := config.Settings{
		"statement_timeout": {Values: []string{"30s"}},
		"search_path":       {Values: []string{"app", "$user", "public"}},
		"Work_Mem":          {Values: []string{"64MB"}},
		"lock_timeout":      {},
		"jit":               {Values: []string{"off"}},
	}
	if err := p.applySettings(context.Background(), roleScope("app"), desired); err != nil {
		t.Fatalf("applySettings: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySettingsListQuotesEachElement(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(roleSettingsQuery).WithArgs("app").WillReturnRows(settingRows())
	mock.ExpectExec(`^ALTER ROLE "app" SET search_path = 'app', '\$user', 'it''s'$`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{adminDB: mockDB}
	desired := config.Settings{"search_path": {Values: []string{"app", "$user", "it's"}}}
	if err := p.applySettings(context.Background(), roleScope("app"), desired); err != nil {
		t.Fatalf("applySettings: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySettingsResetsOnlyWhenSet(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(`SELECT unnest\(s.setconfig\)`).WithArgs("app", "mydb").
		WillReturnRows(settingRows("statement_timeout=5min"))
	mock.ExpectExec(`^ALTER ROLE "app" IN DATABASE "mydb" RESET statement_timeout$`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{adminDB: mockDB}
	desired := config.Settings{"statement_timeout": {}, "work_mem": {}}
	if err := p.applySettings(context.Background(), roleInDatabaseScope("app", "mydb"), desired); err != nil {
		t.Fatalf("applySettings: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunSettingsDoNotExecute(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(`SELECT unnest\(s.setconfig\)`).WithArgs("mydb").WillReturnRows(settingRows())
	// No ALTER DATABASE in dry-run.

	p := &Provisioner{adminDB: mockDB, opts: dryRunOptions()}
	desired := config.Settings{"timezone": {Values: []string{"UTC"}}}
	if err := p.applySettings(context.Background(), databaseScope("mydb"), desired); err != nil {
		t.Fatalf("applySettings: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRoleCreateStrategySkipsSettings(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_roles WHERE rolname = \$1\)`).
		WithArgs("app").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	// No settings query: strategy=create leaves existing roles untouched.

	p := &Provisioner{adminDB: mockDB}
	role := config.Role{Name: "app", Settings: config.Settings{"work_mem": {Values: []string{"64MB"}}}}
	created, err := p.ensureRole(context.Background(), role, "create")
	if err != nil {
		t.Fatalf("ensureRole: %v", err)
	}
	if created {
		t.Error("expected created=false for an existing role")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSettingMatches(t *testing.T) {
	// Values without quotes in the stored form are compared without a query.
	tests := []struct {
		stored string
		values []string
		want   bool
	}{
		{"30s", []string{"30s"}, true},
		{"31s", []string{"30s"}, false},
		{"app, public", []string{"app", "public"}, true},
		{"app, public", []string{"public", "app"}, false},
	}
	p := &Provisioner{}
	for _, tt := range tests {
		got, err := p.settingMatches(context.Background(), tt.stored, tt.values)
		if err != nil {
			t.Fatalf("settingMatches(%q, %v): %v", tt.stored, tt.values, err)
		}
		if got != tt.want {
			t.Errorf("settingMatches(%q, %v) = %v, want %v", tt.stored, tt.values, got, tt.want)
		}
	}
}

func TestSettingMatchesQuotedFormComputedByPostgres(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(`SELECT string_agg\(quote_ident\(v\), ', ' ORDER BY ord\)`).
		WithArgs(pq.Array([]string{"app", "user"})).
		WillReturnRows(sqlmock.NewRows([]string{"string_agg"}).AddRow(`app, "user"`))
	mock.ExpectQuery(`SELECT string_agg\(quote_ident\(v\), ', ' ORDER BY ord\)`).
		WithArgs(pq.Array([]string{"user", "app"})).
		WillReturnRows(sqlmock.NewRows([]string{"string_agg"}).AddRow(`"user", app`))
	mock.ExpectQuery(`SELECT string_agg`).WillReturnError(errors.New("boom"))

	p := &Provisioner{adminDB: mockDB}
	ctx := context.Background()
	if got, err := p.settingMatches(ctx, `app, "user"`, []string{"app", "user"}); err != nil || !got {
		t.Errorf("expected match for quoted reserved keyword, got %v, %v", got, err)
	}
	if got, err := p.settingMatches(ctx, `app, "user"`, []string{"user", "app"}); err != nil || got {
		t.Errorf("expected mismatch for different order, got %v, %v", got, err)
	}
	if _, err := p.settingMatches(ctx, `"x"`, []string{"y"}); err == nil {
		t.Error("expected query error to be returned")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// databaseWithSettings returns a database with settings and role_settings for Run tests.
func databaseWithSettings(strategy string) config.Database {
	return config.Database{
		Name:         "appdb",
		Strategy:     strategy,
		Settings:     config.Settings{"timezone": {Values: []string{"UTC"}}},
		RoleSettings: map[string]config.Settings{"app": {"work_mem": {Values: []string{"8MB"}}}},
	}
}

func TestRunCreateStrategySkipsSettingsForExistingDatabase(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("appdb").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	// No settings queries or ALTERs: database settings and role_settings follow the
	// database's strategy=create. sqlmock fails Run on any unexpected query.

	cfg := &config.Config{Databases: []config.Database{databaseWithSettings("create")}}
	if err := New(mockDB, db.ConnConfig{}, cfg, DefaultOptions()).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunPreviewsSettingsForDatabaseToBeCreated(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM pg_database WHERE datname = \$1\)`).
		WithArgs("appdb").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`SELECT unnest\(s.setconfig\)`).WithArgs("appdb").WillReturnRows(settingRows())
	mock.ExpectQuery(`SELECT unnest\(s.setconfig\)`).WithArgs("app", "appdb").WillReturnRows(settingRows())
	// No execs and no connection to the database that doesn't exist yet.

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

	cfg := &config.Config{Databases: []config.Database{databaseWithSettings("update")}}
	if err := New(mockDB, db.ConnConfig{}, cfg, dryRunOptions()).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	want := []string{
		`CREATE DATABASE "appdb"`,
		`ALTER DATABASE "appdb" SET timezone = 'UTC'`,
		`ALTER ROLE "app" IN DATABASE "appdb" SET work_mem = '8MB'`,
	}
	if len(captured) != len(want) {
		t.Fatalf("captured SQL = %q, want %q", captured, want)
	}
	for i := range want {
		if !strings.HasPrefix(captured[i], want[i]) {
			t.Errorf("captured[%d] = %q, want prefix %q", i, captured[i], want[i])
		}
	}
}

const extensionLookupQuery = `SELECT e.extversion, n.nspname, e.extrelocatable\s+FROM pg_extension e`

var extensionLookupColumns = []string{"extversion", "nspname", "extrelocatable"}

func expectExtensionMissing(mock sqlmock.Sqlmock, name string) {
	mock.ExpectQuery(extensionLookupQuery).WithArgs(name).
		WillReturnRows(sqlmock.NewRows(extensionLookupColumns))
}

// expectExtensionInstalled expects the lookup of a relocatable installed extension.
func expectExtensionInstalled(mock sqlmock.Sqlmock, name, version, schema string) {
	expectExtensionInstalledRelocatable(mock, name, version, schema, true)
}

func expectExtensionInstalledRelocatable(mock sqlmock.Sqlmock, name, version, schema string, relocatable bool) {
	mock.ExpectQuery(extensionLookupQuery).WithArgs(name).
		WillReturnRows(sqlmock.NewRows(extensionLookupColumns).AddRow(version, schema, relocatable))
}

func TestEnsureExtensionWithSchemaVersionCascade(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectExtensionMissing(mock, "pg_stat_statements")
	mock.ExpectExec(`^CREATE EXTENSION IF NOT EXISTS "pg_stat_statements" SCHEMA "monitoring" VERSION '1\.10' CASCADE$`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{}
	ext := config.Extension{Name: "pg_stat_statements", Schema: "monitoring", Version: "1.10", Cascade: true}
	if err := p.ensureExtension(context.Background(), mockDB, ext, "update"); err != nil {
		t.Fatalf("ensureExtension: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureExtensionInstalledIsNoOp(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectExtensionInstalled(mock, "pgcrypto", "1.3", "public")
	// No CREATE: installed and no version pinned.

	p := &Provisioner{}
	if err := p.ensureExtension(context.Background(), mockDB, config.Extension{Name: "pgcrypto"}, "update"); err != nil {
		t.Fatalf("ensureExtension: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureExtensionVersionDriftUpdates(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectExtensionInstalled(mock, "pg_stat_statements", "1.9", "public")
	expectUpdatePath(mock, "pg_stat_statements", "1.9", "1.10", true)
	mock.ExpectExec(`^ALTER EXTENSION "pg_stat_statements" UPDATE TO '1\.10'$`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{}
	ext := config.Extension{Name: "pg_stat_statements", Version: "1.10"}
	if err := p.ensureExtension(context.Background(), mockDB, ext, "update"); err != nil {
		t.Fatalf("ensureExtension: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A pinned version below the installed one (or one without an update script) can't be reached
// with ALTER EXTENSION ... UPDATE TO: warn and keep the installed version instead of failing.
func TestEnsureExtensionWithoutUpdatePathWarnsAndSkips(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	logs := captureLogs(t)

	expectExtensionInstalled(mock, "pg_stat_statements", "1.10", "public")
	expectUpdatePath(mock, "pg_stat_statements", "1.10", "1.9", false)
	// No ALTER EXTENSION.

	p := &Provisioner{}
	ext := config.Extension{Name: "pg_stat_statements", Version: "1.9"}
	if err := p.ensureExtension(context.Background(), mockDB, ext, "update"); err != nil {
		t.Fatalf("ensureExtension: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "No update path") {
		t.Errorf("expected no-update-path warning, got logs:\n%s", logs.String())
	}
}

func expectUpdatePath(mock sqlmock.Sqlmock, name, from, to string, exists bool) {
	mock.ExpectQuery(`FROM pg_extension_update_paths\(\$1\)`).WithArgs(name, from, to).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(exists))
}

func TestEnsureExtensionVersionDriftStrategyCreateSkips(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectExtensionInstalled(mock, "pg_stat_statements", "1.9", "public")
	// No ALTER with strategy=create.

	p := &Provisioner{}
	ext := config.Extension{Name: "pg_stat_statements", Version: "1.10"}
	if err := p.ensureExtension(context.Background(), mockDB, ext, "create"); err != nil {
		t.Fatalf("ensureExtension: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureExtensionSchemaDriftWarnsOnly(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectExtensionInstalled(mock, "pgcrypto", "1.3", "public")
	// No ALTER EXTENSION ... SET SCHEMA.

	var logs strings.Builder
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))

	p := &Provisioner{}
	ext := config.Extension{Name: "pgcrypto", Schema: "crypto"}
	if err := p.ensureExtension(context.Background(), mockDB, ext, "update"); err != nil {
		t.Fatalf("ensureExtension: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "installed_schema=public") {
		t.Errorf("expected schema drift warning, got logs:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), `SET SCHEMA \"crypto\"`) {
		t.Errorf("expected SET SCHEMA hint for a relocatable extension, got logs:\n%s", logs.String())
	}
}

// A non-relocatable extension (e.g. plpgsql, or one with a fixed schema) can't be moved
// with SET SCHEMA: the hint must say to drop and recreate it instead.
func TestEnsureExtensionSchemaDriftNonRelocatableHint(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	logs := captureLogs(t)

	expectExtensionInstalledRelocatable(mock, "plpgsql", "1.0", "pg_catalog", false)

	p := &Provisioner{}
	ext := config.Extension{Name: "plpgsql", Schema: "app"}
	if err := p.ensureExtension(context.Background(), mockDB, ext, "update"); err != nil {
		t.Fatalf("ensureExtension: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "SET SCHEMA") || !strings.Contains(logs.String(), "not relocatable") {
		t.Errorf("expected drop-and-recreate hint without SET SCHEMA, got logs:\n%s", logs.String())
	}
}

func TestDryRunExtensionVersionUpdateDoesNotExecute(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectExtensionInstalled(mock, "pg_stat_statements", "1.9", "public")
	expectUpdatePath(mock, "pg_stat_statements", "1.9", "1.10", true)

	p := &Provisioner{opts: dryRunOptions()}
	ext := config.Extension{Name: "pg_stat_statements", Version: "1.10"}
	if err := p.ensureExtension(context.Background(), mockDB, ext, "update"); err != nil {
		t.Fatalf("ensureExtension: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

const preloadWarning = "requires shared_preload_libraries"

func expectShowPreload(mock sqlmock.Sqlmock, loaded string) {
	mock.ExpectQuery(`SHOW shared_preload_libraries`).
		WillReturnRows(sqlmock.NewRows([]string{"shared_preload_libraries"}).AddRow(loaded))
}

func expectExtensionExists(mock sqlmock.Sqlmock, name string, exists bool) {
	mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM pg_extension WHERE extname = \$1\)`).WithArgs(name).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(exists))
}

func TestParsePreloadLibraries(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"pg_stat_statements", []string{"pg_stat_statements"}},
		{`auto_explain, "pg_cron" ,pg_stat_statements`, []string{"auto_explain", "pg_cron", "pg_stat_statements"}},
		{"$libdir/pg_stat_statements", []string{"pg_stat_statements"}},
		{"pg_stat_statements.so, timescaledb.dylib", []string{"pg_stat_statements", "timescaledb"}},
		{`"$libdir/pg_cron.so"`, []string{"pg_cron"}},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := parsePreloadLibraries(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parsePreloadLibraries(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			for _, lib := range tt.want {
				if !got[lib] {
					t.Errorf("parsePreloadLibraries(%q) = %v, missing %q", tt.raw, got, lib)
				}
			}
		})
	}
}

func TestCheckPreloadLibrariesSkipsQueryWhenNotNeeded(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	// No SHOW expected.

	p := &Provisioner{}
	database := config.Database{Name: "db", Extensions: []config.Extension{{Name: "pgcrypto"}}}
	if err := p.checkPreloadLibraries(context.Background(), mockDB, database); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// pg_cron cannot be created without its library preloaded: the run must fail before any
// schema or extension of the database is touched.
func TestProvisionDatabaseResourcesFailsEarlyWhenCreateNeedsPreload(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectShowPreload(mock, "pg_stat_statements")
	expectExtensionExists(mock, "pg_cron", false)
	// No CREATE SCHEMA / CREATE EXTENSION.

	database := config.Database{
		Name:       "db",
		Schemas:    []config.Schema{{Name: "app"}},
		Extensions: []config.Extension{{Name: "pgcrypto"}, {Name: "pg_cron"}},
	}
	p := &Provisioner{}
	err = p.provisionDatabaseResources(context.Background(), mockDB, database, "update")
	if err == nil || !strings.Contains(err.Error(), "-c shared_preload_libraries=pg_cron") {
		t.Fatalf("expected actionable preload error, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An already installed pg_cron whose library is no longer preloaded only warns: the run can
// proceed, the extension fails when used.
func TestCheckPreloadLibrariesInstalledCreateNeedsPreloadWarns(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	logs := captureLogs(t)

	expectShowPreload(mock, "")
	expectExtensionExists(mock, "pg_cron", true)

	p := &Provisioner{}
	database := config.Database{Name: "db", Extensions: []config.Extension{{Name: "pg_cron"}}}
	if err := p.checkPreloadLibraries(context.Background(), mockDB, database); err != nil {
		t.Fatalf("checkPreloadLibraries: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), preloadWarning) || !strings.Contains(logs.String(), "library=pg_cron") {
		t.Errorf("expected preload warning, got logs:\n%s", logs.String())
	}
}

// pg_stat_statements installs without preloading: it is checked before CREATE EXTENSION,
// warned about, and still created.
func TestProvisionDatabaseResourcesWarnsAndCreatesPgStatStatements(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	logs := captureLogs(t)

	mock.MatchExpectationsInOrder(true)
	expectShowPreload(mock, "auto_explain")
	expectExtensionMissing(mock, "pg_stat_statements")
	mock.ExpectExec(`CREATE EXTENSION IF NOT EXISTS "pg_stat_statements"`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	database := config.Database{Name: "db", Extensions: []config.Extension{{Name: "pg_stat_statements"}}}
	p := &Provisioner{}
	if err := p.provisionDatabaseResources(context.Background(), mockDB, database, "update"); err != nil {
		t.Fatalf("provisionDatabaseResources: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), preloadWarning) || !strings.Contains(logs.String(), "library=pg_stat_statements") {
		t.Errorf("expected preload warning, got logs:\n%s", logs.String())
	}
}

// shared_preload_libraries is server-wide: it is read once per run and each missing library
// is warned about once, however many databases configure it.
func TestCheckPreloadLibrariesOncePerRun(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	logs := captureLogs(t)

	expectShowPreload(mock, "")

	p := &Provisioner{}
	for _, name := range []string{"db1", "db2"} {
		database := config.Database{Name: name, Extensions: []config.Extension{{Name: "pg_stat_statements"}}}
		if err := p.checkPreloadLibraries(context.Background(), mockDB, database); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logs.String(), preloadWarning); n != 1 {
		t.Errorf("expected 1 preload warning, got %d:\n%s", n, logs.String())
	}
}

// Without permission to read the setting, the check is skipped (logged once at INFO) and
// even pg_cron is left to CREATE EXTENSION.
func TestCheckPreloadLibrariesSkippedWhenShowFails(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	logs := captureLogs(t)

	mock.ExpectQuery(`SHOW shared_preload_libraries`).WillReturnError(fmt.Errorf("permission denied"))

	p := &Provisioner{}
	for _, name := range []string{"db1", "db2"} {
		database := config.Database{Name: name, Extensions: []config.Extension{{Name: "pg_cron"}}}
		if err := p.checkPreloadLibraries(context.Background(), mockDB, database); err != nil {
			t.Fatalf("expected the check to be skipped, got: %v", err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logs.String(), "level=INFO msg=\"Could not read shared_preload_libraries"); n != 1 {
		t.Errorf("expected 1 info log about the skipped check, got %d:\n%s", n, logs.String())
	}
}

const (
	publicationQuery       = `SELECT puballtables, pubinsert, pubupdate, pubdelete, pubtruncate FROM pg_publication WHERE pubname = \$1`
	publicationTablesQuery = `SELECT n\.nspname, c\.relname\s+FROM pg_publication_rel pr`
)

func expectPublicationMissing(mock sqlmock.Sqlmock, name string) {
	mock.ExpectQuery(publicationQuery).WithArgs(name).
		WillReturnRows(sqlmock.NewRows([]string{"puballtables", "pubinsert", "pubupdate", "pubdelete", "pubtruncate"}))
}

func expectPublication(mock sqlmock.Sqlmock, name string, allTables bool, ops [4]bool, tables ...[2]string) {
	mock.ExpectQuery(publicationQuery).WithArgs(name).
		WillReturnRows(sqlmock.NewRows([]string{"puballtables", "pubinsert", "pubupdate", "pubdelete", "pubtruncate"}).
			AddRow(allTables, ops[0], ops[1], ops[2], ops[3]))
	if allTables {
		return
	}
	rows := sqlmock.NewRows([]string{"nspname", "relname"})
	for _, t := range tables {
		rows.AddRow(t[0], t[1])
	}
	mock.ExpectQuery(publicationTablesQuery).WithArgs(name).WillReturnRows(rows)
}

func expectTableExists(mock sqlmock.Sqlmock, quoted string, exists bool) {
	row := sqlmock.NewRows([]string{"to_regclass"})
	if exists {
		row.AddRow(quoted)
	} else {
		row.AddRow(nil)
	}
	mock.ExpectQuery(toRegclassQuery).WithArgs(quoted).WillReturnRows(row)
}

var allOps = [4]bool{true, true, true, true}

func TestEnsurePublicationCreate(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectPublicationMissing(mock, "cdc")
	expectTableExists(mock, `"app"."orders"`, true)
	expectTableExists(mock, `"billing"."invoices"`, true)
	mock.ExpectExec(`^CREATE PUBLICATION "cdc" FOR TABLE "app"\."orders", "billing"\."invoices" WITH \(publish = 'insert, update'\)$`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{}
	pub := config.Publication{Name: "cdc", Tables: []string{"app.orders", "billing.invoices"}, Publish: []string{"update", "insert"}}
	if err := p.ensurePublication(context.Background(), mockDB, pub, "update"); err != nil {
		t.Fatalf("ensurePublication: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsurePublicationCreateAllTables(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectPublicationMissing(mock, "all")
	mock.ExpectExec(`^CREATE PUBLICATION "all" FOR ALL TABLES$`).WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{}
	if err := p.ensurePublication(context.Background(), mockDB, config.Publication{Name: "all", AllTables: true}, "update"); err != nil {
		t.Fatalf("ensurePublication: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsurePublicationAddsMissingTableOnlyAndKeepsExtra(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	// Published: app.orders (configured) and app.legacy (not configured — kept).
	expectPublication(mock, "cdc", false, allOps, [2]string{"app", "orders"}, [2]string{"app", "legacy"})
	expectTableExists(mock, `"app"."customers"`, true)
	mock.ExpectExec(`^ALTER PUBLICATION "cdc" ADD TABLE "app"\."customers"$`).WillReturnResult(sqlmock.NewResult(0, 0))

	var logs strings.Builder
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))

	p := &Provisioner{}
	pub := config.Publication{Name: "cdc", Tables: []string{"app.orders", "app.customers"}}
	if err := p.ensurePublication(context.Background(), mockDB, pub, "update"); err != nil {
		t.Fatalf("ensurePublication: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "app.legacy") {
		t.Errorf("expected warning about extra table app.legacy, logs:\n%s", logs.String())
	}
}

func TestEnsurePublicationPublishDrift(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectPublication(mock, "cdc", false, allOps, [2]string{"app", "orders"})
	mock.ExpectExec(`^ALTER PUBLICATION "cdc" SET \(publish = 'insert, update, delete'\)$`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	p := &Provisioner{}
	pub := config.Publication{Name: "cdc", Tables: []string{"app.orders"}, Publish: []string{"insert", "update", "delete"}}
	if err := p.ensurePublication(context.Background(), mockDB, pub, "update"); err != nil {
		t.Fatalf("ensurePublication: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsurePublicationUpToDateIsNoOp(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectPublication(mock, "cdc", false, allOps, [2]string{"app", "orders"})

	p := &Provisioner{}
	if err := p.ensurePublication(context.Background(), mockDB, config.Publication{Name: "cdc", Tables: []string{"app.orders"}}, "update"); err != nil {
		t.Fatalf("ensurePublication: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsurePublicationMissingTableFails(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectPublicationMissing(mock, "cdc")
	expectTableExists(mock, `"app"."ghost"`, false)

	p := &Provisioner{}
	err = p.ensurePublication(context.Background(), mockDB, config.Publication{Name: "cdc", Tables: []string{"app.ghost"}}, "update")
	if err == nil || !strings.Contains(err.Error(), "tables not found: app.ghost") {
		t.Fatalf("expected missing table error, got %v", err)
	}
}

func TestDryRunPublicationMissingTableDoesNotFail(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectPublicationMissing(mock, "cdc")
	expectTableExists(mock, `"app"."future"`, false)
	// No CREATE PUBLICATION in dry-run.

	p := &Provisioner{opts: dryRunOptions()}
	if err := p.ensurePublication(context.Background(), mockDB, config.Publication{Name: "cdc", Tables: []string{"app.future"}}, "update"); err != nil {
		t.Fatalf("ensurePublication: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsurePublicationCreateStrategySkipsExisting(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	expectPublication(mock, "cdc", false, [4]bool{true, false, false, false}, [2]string{"app", "orders"})
	// No ALTER with strategy=create, even though publish and tables differ.

	p := &Provisioner{}
	pub := config.Publication{Name: "cdc", Tables: []string{"app.orders", "app.more"}}
	if err := p.ensurePublication(context.Background(), mockDB, pub, "create"); err != nil {
		t.Fatalf("ensurePublication: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
