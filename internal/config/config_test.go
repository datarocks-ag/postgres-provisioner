package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadValidConfig(t *testing.T) {
	yaml := `
roles:
  - name: "app_user"
    password: "secret"
    options:
      login: true
      superuser: false

databases:
  - name: "myapp"
    owner: "app_user"
    extensions: ["uuid-ossp"]
    schemas:
      - name: "app"
        owner: "app_user"
    grants:
      - role: "app_user"
        privileges: ["ALL"]
        on_schema: "app"
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Roles) != 1 {
		t.Fatalf("expected 1 role, got %d", len(cfg.Roles))
	}
	if cfg.Roles[0].Name != "app_user" {
		t.Errorf("expected role name app_user, got %s", cfg.Roles[0].Name)
	}
	if cfg.Roles[0].Password != "secret" {
		t.Errorf("expected password secret, got %s", cfg.Roles[0].Password)
	}
	if cfg.Roles[0].Options.Login == nil || !*cfg.Roles[0].Options.Login {
		t.Error("expected login=true")
	}
	if len(cfg.Databases) != 1 {
		t.Fatalf("expected 1 database, got %d", len(cfg.Databases))
	}
	if cfg.Databases[0].Owner != "app_user" {
		t.Errorf("expected owner app_user, got %s", cfg.Databases[0].Owner)
	}
}

func TestEnvVarExpansion(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "env_secret")
	t.Setenv("TEST_DB_NAME", "envdb")

	yaml := `
roles:
  - name: "app_user"
    password: "${TEST_DB_PASSWORD}"

databases:
  - name: "${TEST_DB_NAME}"
    owner: "app_user"
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Roles[0].Password != "env_secret" {
		t.Errorf("expected env_secret, got %s", cfg.Roles[0].Password)
	}
	if cfg.Databases[0].Name != "envdb" {
		t.Errorf("expected envdb, got %s", cfg.Databases[0].Name)
	}
}

func TestUnsetEnvVarPreserved(t *testing.T) {
	os.Unsetenv("TOTALLY_UNSET_VAR")

	result := expandEnvVars("${TOTALLY_UNSET_VAR}")
	if result != "${TOTALLY_UNSET_VAR}" {
		t.Errorf("expected unresolved var to be preserved, got %s", result)
	}
}

func TestValidationMissingRoleName(t *testing.T) {
	yaml := `
roles:
  - password: "secret"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for missing role name")
	}
}

func TestValidationDuplicateRoleName(t *testing.T) {
	yaml := `
roles:
  - name: "dup"
    password: "a"
  - name: "dup"
    password: "b"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for duplicate role name")
	}
}

func TestValidationDuplicateDBName(t *testing.T) {
	yaml := `
databases:
  - name: "db1"
  - name: "db1"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for duplicate database name")
	}
}

func TestValidationGrantMissingTarget(t *testing.T) {
	yaml := `
databases:
  - name: "db1"
    grants:
      - role: "user1"
        privileges: ["ALL"]
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for grant missing target")
	}
}

func TestValidationGrantMultipleTargets(t *testing.T) {
	yaml := `
databases:
  - name: "db1"
    grants:
      - role: "user1"
        privileges: ["ALL"]
        on_schema: "public"
        on_database: true
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for grant with multiple targets")
	}
}

func TestValidationMissingSchemaName(t *testing.T) {
	yaml := `
databases:
  - name: "db1"
    schemas:
      - owner: "user1"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for missing schema name")
	}
}

func TestEmptyConfig(t *testing.T) {
	yaml := `{}`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error for empty config: %v", err)
	}
	if len(cfg.Roles) != 0 || len(cfg.Databases) != 0 {
		t.Error("expected empty roles and databases")
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return path
}
