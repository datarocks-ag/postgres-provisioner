package config

import (
	"fmt"
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
roles:
  - name: "user1"
    password: "pass"
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
roles:
  - name: "user1"
    password: "pass"
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

func TestValidationInvalidPrivilege(t *testing.T) {
	yaml := `
roles:
  - name: "user1"
    password: "pass"
databases:
  - name: "db1"
    grants:
      - role: "user1"
        privileges: ["SELECT; DROP TABLE foo --"]
        on_database: true
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for invalid privilege")
	}
}

func TestValidationValidPrivileges(t *testing.T) {
	yaml := `
roles:
  - name: "user1"
    password: "pass"
databases:
  - name: "db1"
    grants:
      - role: "user1"
        privileges: ["SELECT", "INSERT", "UPDATE", "DELETE"]
        on_database: true
      - role: "user1"
        privileges: ["ALL"]
        on_schema: "public"
      - role: "user1"
        privileges: ["usage", "create"]
        on_schema: "public"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error for valid privileges: %v", err)
	}
}

func TestValidationNullByteInName(t *testing.T) {
	yaml := "roles:\n  - name: \"user\\x00evil\"\n    password: \"pass\"\n"
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for null byte in role name")
	}
}

func TestValidationGrantRoleNotDeclared(t *testing.T) {
	yaml := `
roles:
  - name: "declared"
    password: "pass"
databases:
  - name: "db1"
    grants:
      - role: "undeclared"
        privileges: ["SELECT"]
        on_database: true
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for undeclared grant role")
	}
}

func TestValidationDatabaseOwnerNotDeclared(t *testing.T) {
	yaml := `
roles:
  - name: "existing"
    password: "pass"
databases:
  - name: "db1"
    owner: "nonexistent"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for undeclared database owner")
	}
}

func TestValidationSchemaOwnerNotDeclared(t *testing.T) {
	yaml := `
roles:
  - name: "existing"
    password: "pass"
databases:
  - name: "db1"
    schemas:
      - name: "app"
        owner: "nonexistent"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for undeclared schema owner")
	}
}

func TestValidationEmptyExtensionName(t *testing.T) {
	yaml := `
databases:
  - name: "db1"
    extensions: [""]
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for empty extension name")
	}
}

func TestValidationDuplicateSchemaName(t *testing.T) {
	yaml := `
databases:
  - name: "db1"
    schemas:
      - name: "app"
      - name: "app"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for duplicate schema name")
	}
}

func TestValidationReservedDatabaseName(t *testing.T) {
	for _, name := range []string{"template0", "template1", "postgres"} {
		t.Run(name, func(t *testing.T) {
			yaml := fmt.Sprintf(`
databases:
  - name: %q
`, name)
			path := writeTempConfig(t, yaml)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected validation error for reserved database name %q", name)
			}
		})
	}
}

func TestValidationInvalidConnectionLimit(t *testing.T) {
	yaml := `
roles:
  - name: "user1"
    password: "pass"
    options:
      connection_limit: -2
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for connection_limit < -1")
	}
}

func TestValidationInvalidGlobalStrategy(t *testing.T) {
	yaml := `
strategy: "invalid"
roles:
  - name: "user1"
    password: "pass"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for invalid global strategy")
	}
}

func TestValidationInvalidRoleStrategy(t *testing.T) {
	yaml := `
roles:
  - name: "user1"
    password: "pass"
    strategy: "invalid"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for invalid role strategy")
	}
}

func TestValidationInvalidDatabaseStrategy(t *testing.T) {
	yaml := `
databases:
  - name: "db1"
    strategy: "invalid"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for invalid database strategy")
	}
}

func TestValidStrategies(t *testing.T) {
	for _, strategy := range []string{"create", "update"} {
		t.Run(strategy, func(t *testing.T) {
			yaml := fmt.Sprintf(`
strategy: %q
roles:
  - name: "user1"
    password: "pass"
    strategy: %q
databases:
  - name: "db1"
    owner: "user1"
    strategy: %q
`, strategy, strategy, strategy)
			path := writeTempConfig(t, yaml)
			_, err := Load(path)
			if err != nil {
				t.Fatalf("unexpected error for strategy %q: %v", strategy, err)
			}
		})
	}
}

func TestEnvVarExpansionInStrategy(t *testing.T) {
	t.Setenv("TEST_STRATEGY", "create")

	yaml := `
strategy: "${TEST_STRATEGY}"
roles:
  - name: "user1"
    password: "pass"
    strategy: "${TEST_STRATEGY}"
databases:
  - name: "db1"
    owner: "user1"
    strategy: "${TEST_STRATEGY}"
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Strategy != "create" {
		t.Errorf("expected global strategy 'create', got %q", cfg.Strategy)
	}
	if cfg.Roles[0].Strategy != "create" {
		t.Errorf("expected role strategy 'create', got %q", cfg.Roles[0].Strategy)
	}
	if cfg.Databases[0].Strategy != "create" {
		t.Errorf("expected database strategy 'create', got %q", cfg.Databases[0].Strategy)
	}
}

func TestEffectiveStrategy(t *testing.T) {
	tests := []struct {
		name       string
		strategies []string
		want       string
	}{
		{"all empty defaults to update", []string{"", ""}, "update"},
		{"first wins", []string{"create", "update"}, "create"},
		{"fallback to second", []string{"", "create"}, "create"},
		{"single empty", []string{""}, "update"},
		{"single set", []string{"create"}, "create"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveStrategy(tt.strategies...)
			if got != tt.want {
				t.Errorf("EffectiveStrategy(%v) = %q, want %q", tt.strategies, got, tt.want)
			}
		})
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
