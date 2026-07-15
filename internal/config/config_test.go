package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestValidationMigrationsValid(t *testing.T) {
	yaml := `
roles:
  - name: "app_user"
    password: "pass"
databases:
  - name: "db1"
    owner: "app_user"
    migrations:
      directory: "./migrations/db1"
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Databases[0].Migrations == nil {
		t.Fatal("expected migrations to be non-nil")
	}
	if cfg.Databases[0].Migrations.Directory != "./migrations/db1" {
		t.Errorf("expected directory './migrations/db1', got %q", cfg.Databases[0].Migrations.Directory)
	}
}

func TestValidationMigrationsEmptyDirectory(t *testing.T) {
	yaml := `
roles:
  - name: "app_user"
    password: "pass"
databases:
  - name: "db1"
    owner: "app_user"
    migrations:
      directory: ""
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for empty migrations directory")
	}
	if !strings.Contains(err.Error(), "databases[0].migrations") {
		t.Errorf("expected error to mention 'databases[0].migrations', got: %v", err)
	}
}

func TestValidationMigrationsNullByteInDirectory(t *testing.T) {
	yaml := "roles:\n  - name: \"app_user\"\n    password: \"pass\"\ndatabases:\n  - name: \"db1\"\n    owner: \"app_user\"\n    migrations:\n      directory: \"./mig\\x00rations\"\n"
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for null byte in migrations directory")
	}
	if !strings.Contains(err.Error(), "databases[0].migrations.directory") {
		t.Errorf("expected error to mention 'databases[0].migrations.directory', got: %v", err)
	}
}

func TestMigrationsEnvVarExpansion(t *testing.T) {
	t.Setenv("TEST_MIG_DIR", "/opt/migrations")

	yaml := `
roles:
  - name: "app_user"
    password: "pass"
databases:
  - name: "db1"
    owner: "app_user"
    migrations:
      directory: "${TEST_MIG_DIR}/db1"
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Databases[0].Migrations.Directory != "/opt/migrations/db1" {
		t.Errorf("expected '/opt/migrations/db1', got %q", cfg.Databases[0].Migrations.Directory)
	}
}

func TestMigrationsNilWhenAbsent(t *testing.T) {
	yaml := `
databases:
  - name: "db1"
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Databases[0].Migrations != nil {
		t.Error("expected migrations to be nil when not specified")
	}
}

func TestLoadNonexistentFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
	if !strings.Contains(err.Error(), "reading config file") {
		t.Errorf("expected 'reading config file' in error, got: %v", err)
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	yaml := `
roles:
  - name: [invalid yaml
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !strings.Contains(err.Error(), "parsing config YAML") {
		t.Errorf("expected 'parsing config YAML' in error, got: %v", err)
	}
}

func TestValidationMissingDatabaseName(t *testing.T) {
	yaml := `
databases:
  - owner: "someowner"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for missing database name")
	}
}

func TestValidationGrantMissingRole(t *testing.T) {
	yaml := `
databases:
  - name: "db1"
    grants:
      - privileges: ["SELECT"]
        on_database: true
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for missing grant role")
	}
}

func TestValidationGrantMissingPrivileges(t *testing.T) {
	yaml := `
roles:
  - name: "user1"
    password: "pass"
databases:
  - name: "db1"
    grants:
      - role: "user1"
        on_database: true
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for missing grant privileges")
	}
}

func TestValidationNullByteInPassword(t *testing.T) {
	yaml := "roles:\n  - name: \"user1\"\n    password: \"pass\\x00word\"\n"
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for null byte in password")
	}
}

func TestValidationNullByteInValidUntil(t *testing.T) {
	yaml := "roles:\n  - name: \"user1\"\n    options:\n      valid_until: \"2030\\x0001-01\"\n"
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for null byte in valid_until")
	}
}

func TestValidationNullByteInDatabaseName(t *testing.T) {
	yaml := "databases:\n  - name: \"db\\x00evil\"\n"
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for null byte in database name")
	}
}

func TestValidationNullByteInDatabaseOwner(t *testing.T) {
	yaml := "roles:\n  - name: \"user1\"\n    password: \"pass\"\ndatabases:\n  - name: \"db1\"\n    owner: \"user\\x001\"\n"
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for null byte in database owner")
	}
}

func TestValidationNullByteInExtension(t *testing.T) {
	yaml := "databases:\n  - name: \"db1\"\n    extensions:\n      - \"uuid\\x00ossp\"\n"
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for null byte in extension")
	}
}

func TestValidationNullByteInSchemaName(t *testing.T) {
	yaml := "databases:\n  - name: \"db1\"\n    schemas:\n      - name: \"app\\x00evil\"\n"
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for null byte in schema name")
	}
}

func TestValidationNullByteInGrantRole(t *testing.T) {
	yaml := "roles:\n  - name: \"user1\"\n    password: \"pass\"\ndatabases:\n  - name: \"db1\"\n    grants:\n      - role: \"user\\x001\"\n        privileges: [\"SELECT\"]\n        on_database: true\n"
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for null byte in grant role")
	}
}

func TestValidationNullByteInSchemaOwner(t *testing.T) {
	yaml := "databases:\n  - name: \"db1\"\n    schemas:\n      - name: \"app\"\n        owner: \"user\\x001\"\n"
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for null byte in schema owner")
	}
}

func TestContainsNullByte(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"hello", false},
		{"", false},
		{"hel\x00lo", true},
		{"\x00", true},
	}
	for _, tt := range tests {
		got := containsNullByte(tt.input)
		if got != tt.want {
			t.Errorf("containsNullByte(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestExpandEnvVarsMultiple(t *testing.T) {
	t.Setenv("VAR_A", "hello")
	t.Setenv("VAR_B", "world")

	result := expandEnvVars("${VAR_A}_${VAR_B}")
	if result != "hello_world" {
		t.Errorf("expected 'hello_world', got %q", result)
	}
}

func TestExpandEnvVarsNoVars(t *testing.T) {
	result := expandEnvVars("no vars here")
	if result != "no vars here" {
		t.Errorf("expected 'no vars here', got %q", result)
	}
}

func TestValidationGrantThreeTargets(t *testing.T) {
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
        on_tables_in_schema: "public"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for grant with three targets")
	}
}

func TestEnvVarExpansionInGrants(t *testing.T) {
	t.Setenv("TEST_ROLE", "myuser")
	t.Setenv("TEST_SCHEMA", "myschema")

	yaml := `
roles:
  - name: "myuser"
    password: "pass"
databases:
  - name: "db1"
    owner: "myuser"
    grants:
      - role: "${TEST_ROLE}"
        privileges: ["ALL"]
        on_schema: "${TEST_SCHEMA}"
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Databases[0].Grants[0].Role != "myuser" {
		t.Errorf("expected role 'myuser', got %q", cfg.Databases[0].Grants[0].Role)
	}
	if cfg.Databases[0].Grants[0].OnSchema != "myschema" {
		t.Errorf("expected on_schema 'myschema', got %q", cfg.Databases[0].Grants[0].OnSchema)
	}
}

func TestEnvVarExpansionInExtensions(t *testing.T) {
	t.Setenv("TEST_EXT", "pgcrypto")

	yaml := `
roles:
  - name: "user1"
    password: "pass"
databases:
  - name: "db1"
    owner: "user1"
    extensions:
      - "${TEST_EXT}"
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Databases[0].Extensions[0] != "pgcrypto" {
		t.Errorf("expected extension 'pgcrypto', got %q", cfg.Databases[0].Extensions[0])
	}
}

func TestEnvVarExpansionInSchemas(t *testing.T) {
	t.Setenv("TEST_SCHEMA_NAME", "myschema")
	t.Setenv("TEST_SCHEMA_OWNER", "myuser")

	yaml := `
roles:
  - name: "myuser"
    password: "pass"
databases:
  - name: "db1"
    owner: "myuser"
    schemas:
      - name: "${TEST_SCHEMA_NAME}"
        owner: "${TEST_SCHEMA_OWNER}"
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Databases[0].Schemas[0].Name != "myschema" {
		t.Errorf("expected schema name 'myschema', got %q", cfg.Databases[0].Schemas[0].Name)
	}
	if cfg.Databases[0].Schemas[0].Owner != "myuser" {
		t.Errorf("expected schema owner 'myuser', got %q", cfg.Databases[0].Schemas[0].Owner)
	}
}

func TestEnvVarExpansionInPrivileges(t *testing.T) {
	t.Setenv("TEST_PRIV", "SELECT")

	yaml := `
roles:
  - name: "user1"
    password: "pass"
databases:
  - name: "db1"
    owner: "user1"
    grants:
      - role: "user1"
        privileges: ["${TEST_PRIV}"]
        on_database: true
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Databases[0].Grants[0].Privileges[0] != "SELECT" {
		t.Errorf("expected privilege 'SELECT', got %q", cfg.Databases[0].Grants[0].Privileges[0])
	}
}

func TestDatabaseOptionsParsed(t *testing.T) {
	yaml := `
roles:
  - name: "synapse"
    password: "pass"
databases:
  - name: "synapse"
    owner: "synapse"
    options:
      encoding: "UTF8"
      lc_collate: "C"
      lc_ctype: "C"
      template: "template0"
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts := cfg.Databases[0].Options
	if opts.Encoding != "UTF8" || opts.LcCollate != "C" || opts.LcCtype != "C" || opts.Template != "template0" {
		t.Errorf("unexpected options: %+v", opts)
	}
	if opts.IsZero() {
		t.Error("expected options to be non-zero")
	}
}

func TestDatabaseOptionsEnvVarExpansion(t *testing.T) {
	t.Setenv("TEST_TEMPLATE", "template0")
	t.Setenv("TEST_COLLATE", "C")

	yaml := `
databases:
  - name: "db1"
    options:
      template: "${TEST_TEMPLATE}"
      lc_collate: "${TEST_COLLATE}"
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Databases[0].Options.Template != "template0" {
		t.Errorf("expected template 'template0', got %q", cfg.Databases[0].Options.Template)
	}
	if cfg.Databases[0].Options.LcCollate != "C" {
		t.Errorf("expected lc_collate 'C', got %q", cfg.Databases[0].Options.LcCollate)
	}
}

func TestDatabaseOptionsAbsentIsZero(t *testing.T) {
	yaml := `
databases:
  - name: "db1"
`
	path := writeTempConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Databases[0].Options.IsZero() {
		t.Error("expected options to be zero when not specified")
	}
}

func TestValidationLocaleCombinedWithLcCollate(t *testing.T) {
	yaml := `
databases:
  - name: "db1"
    options:
      locale: "C"
      lc_collate: "C"
`
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for locale combined with lc_collate")
	}
	if !strings.Contains(err.Error(), "databases[0].options") {
		t.Errorf("expected error to mention 'databases[0].options', got: %v", err)
	}
}

func TestValidationNullByteInDatabaseOption(t *testing.T) {
	yaml := "databases:\n  - name: \"db1\"\n    options:\n      lc_collate: \"C\\x00evil\"\n"
	path := writeTempConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for null byte in database option")
	}
	if !strings.Contains(err.Error(), "databases[0].options.lc_collate") {
		t.Errorf("expected error to mention 'databases[0].options.lc_collate', got: %v", err)
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
