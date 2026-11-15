package provisioner

import (
	"strings"
	"testing"

	"postgres-provisioner/internal/config"
)

func TestValidateMigrationsAcceptsValidDirectories(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "V0001__create.sql", "CREATE TABLE t (id int);")
	cfg := &config.Config{Databases: []config.Database{
		{Name: "shop", Migrations: &config.Migrations{Directory: dir}},
		{Name: "no_migrations"},
	}}

	if err := ValidateMigrations(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMigrationsReportsMissingDirectory(t *testing.T) {
	cfg := &config.Config{Databases: []config.Database{
		{Name: "shop", Migrations: &config.Migrations{Directory: "/does/not/exist"}},
	}}

	err := ValidateMigrations(cfg)
	if err == nil || !strings.Contains(err.Error(), `database "shop"`) {
		t.Fatalf("expected an error naming the database, got %v", err)
	}
}

func TestValidateMigrationsReportsInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "V0001__create.sql", "CREATE TABLE t (id int);")
	writeFile(t, dir, "V1__create_again.sql", "CREATE TABLE u (id int);")
	cfg := &config.Config{Databases: []config.Database{
		{Name: "shop", Migrations: &config.Migrations{Directory: dir}},
	}}

	err := ValidateMigrations(cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate versioned migration") {
		t.Fatalf("expected a duplicate-version error, got %v", err)
	}
}
