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

func TestUnsetEnvVarFails(t *testing.T) {
	path := writeTempConfig(t, `
roles:
  - name: "app_user"
    password: "${TOTALLY_UNSET_VAR}"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unset environment variable")
	}
	for _, want := range []string{"roles[0].password", "TOTALLY_UNSET_VAR", "TOTALLY_UNSET_VAR_FILE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to mention %q, got: %v", want, err)
		}
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
        on_tables_in_schema: "public"
      - role: "user1"
        privileges: ["CONNECT", "temp"]
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
	if cfg.Databases[0].Extensions[0].Name != "pgcrypto" {
		t.Errorf("expected extension 'pgcrypto', got %q", cfg.Databases[0].Extensions[0].Name)
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
        on_tables_in_schema: "public"
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

func TestLoadRoleMemberOf(t *testing.T) {
	t.Setenv("MONITOR_ROLE", "pg_monitor")
	path := writeTempConfig(t, `
roles:
  - name: "postgres_exporter"
    member_of: ["${MONITOR_ROLE}", "pg_read_all_settings"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"pg_monitor", "pg_read_all_settings"}
	got := cfg.Roles[0].MemberOf
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("expected member_of %v, got %v", want, got)
	}
}

func TestValidationMemberOfInvalid(t *testing.T) {
	cases := []struct {
		name     string
		memberOf string
		wantErr  string
	}{
		{"empty entry", `[""]`, "must not be empty"},
		{"self membership", `["exporter"]`, "cannot be a member of itself"},
		{"duplicate entry", `["pg_monitor", "pg_monitor"]`, "duplicate role"},
		{"null byte", `["pg_\u0000monitor"]`, "null byte"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempConfig(t, fmt.Sprintf(`
roles:
  - name: "exporter"
    member_of: %s
`, tc.memberOf))
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidationMemberOfCycles(t *testing.T) {
	cases := []struct {
		name    string
		roles   string
		wantErr string
	}{
		{
			name: "two-role cycle",
			roles: `
  - name: "a"
    member_of: ["b"]
  - name: "b"
    member_of: ["a"]`,
			wantErr: "roles[1].member_of[0]: circular role membership a -> b -> a",
		},
		{
			name: "three-role cycle",
			roles: `
  - name: "a"
    member_of: ["pg_monitor", "b"]
  - name: "b"
    member_of: ["c"]
  - name: "c"
    member_of: ["a"]`,
			wantErr: "roles[2].member_of[0]: circular role membership a -> b -> c -> a",
		},
		{
			name: "cycle not reachable from first role",
			roles: `
  - name: "a"
    member_of: ["pg_monitor"]
  - name: "b"
    member_of: ["c"]
  - name: "c"
    member_of: ["b"]`,
			wantErr: "roles[2].member_of[0]: circular role membership b -> c -> b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempConfig(t, "roles:"+tc.roles+"\n")
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidationMemberOfAcyclic(t *testing.T) {
	cases := []struct {
		name  string
		roles string
	}{
		{
			name: "chain",
			roles: `
  - name: "a"
    member_of: ["b"]
  - name: "b"
    member_of: ["c"]
  - name: "c"
    member_of: ["pg_monitor"]`,
		},
		{
			name: "diamond",
			roles: `
  - name: "a"
    member_of: ["b", "c"]
  - name: "b"
    member_of: ["d"]
  - name: "c"
    member_of: ["d"]
  - name: "d"`,
		},
		{
			name: "shared undeclared group",
			roles: `
  - name: "a"
    member_of: ["pg_monitor"]
  - name: "b"
    member_of: ["pg_monitor", "a"]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempConfig(t, "roles:"+tc.roles+"\n")
			if _, err := Load(path); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidationPrivilegeInvalidForTarget(t *testing.T) {
	tests := []struct {
		name      string
		grant     string
		wantError string
	}{
		{"SELECT on database", `privileges: ["SELECT"]
        on_database: true`, `privilege "SELECT" is not valid for on_database`},
		{"CONNECT on schema", `privileges: ["CONNECT"]
        on_schema: "app"`, `privilege "CONNECT" is not valid for on_schema`},
		{"USAGE on tables", `privileges: ["usage"]
        on_tables_in_schema: "app"`, `privilege "usage" is not valid for on_tables_in_schema`},
		{"EXECUTE on sequences", `privileges: ["EXECUTE"]
        on_sequences_in_schema: "app"`, `privilege "EXECUTE" is not valid for on_sequences_in_schema`},
		{"SELECT on functions", `privileges: ["SELECT"]
        on_functions_in_schema: "app"`, `privilege "SELECT" is not valid for on_functions_in_schema`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, `
roles:
  - name: "user1"
databases:
  - name: "db1"
    grants:
      - role: "user1"
        `+tt.grant+`
`)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "databases[0].grants[0].privileges[0]: "+tt.wantError) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantError, err)
			}
		})
	}
}

func TestValidationAllCombinedWithOtherPrivileges(t *testing.T) {
	tests := []struct {
		name      string
		grant     string
		wantError string
	}{
		{"database", `privileges: ["CONNECT", "ALL"]
        on_database: true`, "privileges[1]: ALL"},
		{"schema", `privileges: ["all", "USAGE"]
        on_schema: "app"`, "privileges[0]: ALL"},
		{"tables in schema", `privileges: ["ALL", "SELECT"]
        on_tables_in_schema: "app"`, "privileges[0]: ALL"},
		{"sequences in schema", `privileges: ["USAGE", "ALL"]
        on_sequences_in_schema: "app"`, "privileges[1]: ALL"},
		{"functions in schema", `privileges: ["EXECUTE", "ALL"]
        on_functions_in_schema: "app"`, "privileges[1]: ALL"},
		{"tables", `privileges: ["ALL", "SELECT"]
        on_tables: ["app.users"]`, "privileges[0]: ALL"},
		{"columns", `privileges: ["SELECT", "ALL"]
        on_tables: ["app.users"]
        columns: ["id"]`, "privileges[1]: ALL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeTempConfig(t, `
roles:
  - name: "user1"
databases:
  - name: "db1"
    grants:
      - role: "user1"
        `+tt.grant+`
`))
			want := "databases[0].grants[0]." + tt.wantError + " cannot be combined with other privileges"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected error containing %q, got: %v", want, err)
			}
		})
	}
}

func TestValidationAllAlone(t *testing.T) {
	_, err := Load(writeTempConfig(t, `
roles:
  - name: "user1"
databases:
  - name: "db1"
    grants:
      - { role: "user1", privileges: ["ALL"], on_tables: ["app.users"], columns: ["id"] }
      - { role: "user1", privileges: ["ALL"], on_schema: "app" }
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidationNullByteInGrantTargets(t *testing.T) {
	for _, field := range []string{"on_schema", "on_tables_in_schema", "on_sequences_in_schema", "on_functions_in_schema"} {
		t.Run(field, func(t *testing.T) {
			cfg := &Config{
				Roles: []Role{{Name: "user1"}},
				Databases: []Database{{
					Name:   "db1",
					Grants: []Grant{{Role: "user1", Privileges: []string{"ALL"}}},
				}},
			}
			g := &cfg.Databases[0].Grants[0]
			switch field {
			case "on_schema":
				g.OnSchema = "app\x00"
			case "on_tables_in_schema":
				g.OnTablesInSchema = "app\x00"
			case "on_sequences_in_schema":
				g.OnSequencesInSchema = "app\x00"
			case "on_functions_in_schema":
				g.OnFunctionsInSchema = "app\x00"
			}
			err := validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "databases[0].grants[0]."+field+": contains null byte") {
				t.Fatalf("expected null byte error for %s, got: %v", field, err)
			}
		})
	}
}

func TestLoadSequenceAndFunctionGrants(t *testing.T) {
	t.Setenv("GRANT_SCHEMA", "app")
	path := writeTempConfig(t, `
roles:
  - name: "app_user"
databases:
  - name: "db1"
    grants:
      - role: "app_user"
        privileges: ["USAGE", "select", "UPDATE"]
        on_sequences_in_schema: "${GRANT_SCHEMA}"
      - role: "app_user"
        privileges: ["EXECUTE"]
        on_functions_in_schema: "${GRANT_SCHEMA}"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	grants := cfg.Databases[0].Grants
	if grants[0].OnSequencesInSchema != "app" || grants[1].OnFunctionsInSchema != "app" {
		t.Errorf("expected expanded schema targets, got %q / %q", grants[0].OnSequencesInSchema, grants[1].OnFunctionsInSchema)
	}
}

func TestValidationSequenceGrantCombinedWithOtherTarget(t *testing.T) {
	path := writeTempConfig(t, `
roles:
  - name: "app_user"
databases:
  - name: "db1"
    grants:
      - role: "app_user"
        privileges: ["USAGE"]
        on_schema: "app"
        on_sequences_in_schema: "app"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("expected exactly-one-target error, got: %v", err)
	}
}

func TestLoadRevokes(t *testing.T) {
	t.Setenv("REVOKE_SCHEMA", "public")
	path := writeTempConfig(t, `
roles:
  - name: "app"
databases:
  - name: "db1"
    revokes:
      - { role: "PUBLIC", privileges: ["CONNECT"], on_database: true }
      - { role: "public", privileges: ["CREATE"], on_schema: "${REVOKE_SCHEMA}" }
      - { role: "app", privileges: ["TEMP"], on_database: true }
    grants:
      - { role: "app", privileges: ["CONNECT"], on_database: true }
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.Databases[0].Revokes[1].OnSchema; got != "public" {
		t.Errorf("expected expanded schema 'public', got %q", got)
	}
}

func TestValidationRevokeErrors(t *testing.T) {
	tests := []struct {
		name    string
		entries string
		wantErr string
	}{
		{
			name: "undeclared role",
			entries: `revokes:
      - { role: "ghost", privileges: ["CONNECT"], on_database: true }`,
			wantErr: `databases[0].revokes[0].role: role "ghost" is not declared in roles`,
		},
		{
			name: "unsupported target",
			entries: `revokes:
      - { role: "PUBLIC", privileges: ["SELECT"], on_tables_in_schema: "app" }`,
			wantErr: "databases[0].revokes[0]: on_tables_in_schema is not supported for revokes",
		},
		{
			name: "privilege invalid for target",
			entries: `revokes:
      - { role: "PUBLIC", privileges: ["USAGE"], on_database: true }`,
			wantErr: `databases[0].revokes[0].privileges[0]: privilege "USAGE" is not valid for on_database`,
		},
		{
			name: "missing target",
			entries: `revokes:
      - { role: "PUBLIC", privileges: ["CONNECT"] }`,
			wantErr: "databases[0].revokes[0]: exactly one of on_database or on_schema is required",
		},
		{
			name: "conflicts with grant",
			entries: `revokes:
      - { role: "app", privileges: ["CONNECT"], on_database: true }
    grants:
      - { role: "app", privileges: ["CONNECT"], on_database: true }`,
			wantErr: `databases[0].revokes[0]: conflicts with grants[0] (role "app", CONNECT on database)`,
		},
		{
			name: "ALL conflicts with specific grant",
			entries: `revokes:
      - { role: "app", privileges: ["ALL"], on_schema: "data" }
    grants:
      - { role: "app", privileges: ["USAGE"], on_schema: "data" }`,
			wantErr: `databases[0].revokes[0]: conflicts with grants[0] (role "app", USAGE on schema "data")`,
		},
		{
			name: "TEMP conflicts with TEMPORARY",
			entries: `revokes:
      - { role: "app", privileges: ["temp"], on_database: true }
    grants:
      - { role: "app", privileges: ["TEMPORARY"], on_database: true }`,
			wantErr: "conflicts with grants[0]",
		},
		{
			name: "duplicate revoke",
			entries: `revokes:
      - { role: "PUBLIC", privileges: ["CONNECT"], on_database: true }
      - { role: "public", privileges: ["ALL"], on_database: true }`,
			wantErr: `databases[0].revokes[1]: duplicates revokes[0] (role "PUBLIC", CONNECT on database)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, `
roles:
  - name: "app"
databases:
  - name: "db1"
    `+tt.entries+`
`)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidationGrantToPublicStillRejected(t *testing.T) {
	path := writeTempConfig(t, `
databases:
  - name: "db1"
    grants:
      - { role: "PUBLIC", privileges: ["CONNECT"], on_database: true }
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "is not declared in roles") {
		t.Fatalf("expected undeclared role error for PUBLIC grant, got: %v", err)
	}
}

// PostgreSQL accepts quoted role names like "PUBLIC" or "Public", but grants and revokes
// would send their privileges to the PUBLIC pseudo-role, so any case is rejected.
func TestValidationRejectsRoleNamedPublic(t *testing.T) {
	for _, name := range []string{"public", "PUBLIC", "Public"} {
		t.Run(name, func(t *testing.T) {
			path := writeTempConfig(t, `
roles:
  - name: "app"
  - name: "`+name+`"
`)
			_, err := Load(path)
			want := fmt.Sprintf(`roles[1].name: %q is reserved`, name)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected error containing %q, got: %v", want, err)
			}
		})
	}
}

func TestExpandPrivileges(t *testing.T) {
	tests := []struct {
		target GrantTarget
		in     []string
		want   string
	}{
		{TargetDatabase, []string{"ALL"}, "CONNECT,CREATE,TEMPORARY"},
		{TargetDatabase, []string{"temp", "TEMPORARY", " connect "}, "CONNECT,TEMPORARY"},
		{TargetSchema, []string{"all"}, "CREATE,USAGE"},
		{TargetSchema, []string{"CREATE"}, "CREATE"},
	}
	for _, tt := range tests {
		got := strings.Join(ExpandPrivileges(tt.target, tt.in), ",")
		if got != tt.want {
			t.Errorf("ExpandPrivileges(%s, %v) = %s, want %s", tt.target, tt.in, got, tt.want)
		}
	}
}

func TestLoadReportsUnresolvedRevokeReference(t *testing.T) {
	path := writeTempConfig(t, `
databases:
  - name: "db1"
    revokes:
      - { role: "PUBLIC", privileges: ["CONNECT"], on_schema: "${REVOKE_SCHEMA_UNSET}" }
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "databases[0].revokes[0].on_schema: environment variable REVOKE_SCHEMA_UNSET is not set") {
		t.Fatalf("expected path-qualified unresolved reference error, got: %v", err)
	}
}

func TestValidationRevokeAllCombinedWithOtherPrivileges(t *testing.T) {
	path := writeTempConfig(t, `
databases:
  - name: "db1"
    revokes:
      - { role: "PUBLIC", privileges: ["ALL", "CONNECT"], on_database: true }
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "databases[0].revokes[0].privileges[0]: ALL cannot be combined with other privileges") {
		t.Fatalf("expected ALL-combination error for revoke, got: %v", err)
	}
}

func TestLoadReportsUnresolvedObjectGrantReference(t *testing.T) {
	path := writeTempConfig(t, `
roles:
  - name: "reporting"
databases:
  - name: "db1"
    grants:
      - { role: "reporting", privileges: ["SELECT"], on_tables: ["app.users"], columns: ["${OBJ_COLUMN_UNSET}"] }
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "databases[0].grants[0].columns[0]: environment variable OBJ_COLUMN_UNSET is not set") {
		t.Fatalf("expected path-qualified unresolved reference error, got: %v", err)
	}
}

func TestExtensionRejectsNonScalar(t *testing.T) {
	path := writeTempConfig(t, `
databases:
  - name: "db1"
    extensions:
      - ["pgcrypto"]
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "extension must be a name or a mapping") {
		t.Fatalf("expected extension shape error, got: %v", err)
	}
}

func TestExtensionRejectsUnknownField(t *testing.T) {
	path := writeTempConfig(t, `
databases:
  - name: "db1"
    extensions:
      - name: pgcrypto
        shema: crypto
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `line 6: unknown extension field "shema"`) {
		t.Fatalf("expected unknown field error with line, got: %v", err)
	}
}

func TestExtensionScalarAndMappingForms(t *testing.T) {
	t.Setenv("EXT_SCHEMA", "monitoring")
	path := writeTempConfig(t, `
databases:
  - name: "db1"
    extensions:
      - pgcrypto
      - name: pg_stat_statements
        schema: "${EXT_SCHEMA}"
        version: "1.10"
        cascade: true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	exts := cfg.Databases[0].Extensions
	if exts[0] != (Extension{Name: "pgcrypto"}) {
		t.Errorf("unexpected scalar extension: %+v", exts[0])
	}
	want := Extension{Name: "pg_stat_statements", Schema: "monitoring", Version: "1.10", Cascade: true}
	if exts[1] != want {
		t.Errorf("got %+v, want %+v", exts[1], want)
	}
}

func TestValidationExtensionErrors(t *testing.T) {
	tests := []struct {
		name    string
		exts    string
		wantErr string
	}{
		{"duplicate", `["pgcrypto", {name: pgcrypto, version: "1.3"}]`, `databases[0].extensions[1]: duplicate extension "pgcrypto"`},
		{"missing name", `[{schema: app}]`, "databases[0].extensions[0]: name must not be empty"},
		{"null byte in version", `[{name: pgcrypto, version: "1\u0000"}]`, "databases[0].extensions[0].version: contains null byte"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeTempConfig(t, `
databases:
  - name: "db1"
    extensions: `+tt.exts+`
`))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestLoadReportsUnresolvedExtensionReference(t *testing.T) {
	path := writeTempConfig(t, `
databases:
  - name: "db1"
    extensions:
      - { name: pgcrypto, schema: "${EXT_SCHEMA_UNSET}" }
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "databases[0].extensions[0].schema: environment variable EXT_SCHEMA_UNSET is not set") {
		t.Fatalf("expected path-qualified unresolved reference error, got: %v", err)
	}
}
