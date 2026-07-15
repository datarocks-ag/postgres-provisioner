package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// validPrivileges is the allowlist of PostgreSQL privilege keywords that may
// appear in grant configurations. Anything else is rejected at validation time
// to prevent SQL injection (privileges are interpolated unquoted into GRANT).
var validPrivileges = map[string]bool{
	"ALL":        true,
	"SELECT":     true,
	"INSERT":     true,
	"UPDATE":     true,
	"DELETE":     true,
	"TRUNCATE":   true,
	"REFERENCES": true,
	"TRIGGER":    true,
	"USAGE":      true,
	"CREATE":     true,
	"CONNECT":    true,
	"TEMPORARY":  true,
	"TEMP":       true,
	"EXECUTE":    true,
}

// validStrategies is the allowlist of update strategy values.
var validStrategies = map[string]bool{
	"":       true, // inherits from parent/default
	"create": true, // only create if missing, skip if exists
	"update": true, // create or update (default behavior)
}

// EffectiveStrategy returns the first non-empty strategy from the given list,
// defaulting to "update" if all are empty.
func EffectiveStrategy(strategies ...string) string {
	for _, s := range strategies {
		if s != "" {
			return s
		}
	}
	return "update"
}

// reservedDatabases are system databases that must not be provisioned.
var reservedDatabases = map[string]bool{
	"template0": true,
	"template1": true,
	"postgres":  true,
}

// containsNullByte returns true if s contains a null byte (\x00).
func containsNullByte(s string) bool {
	return strings.ContainsRune(s, '\x00')
}

// RoleOptions controls PostgreSQL role attributes.
type RoleOptions struct {
	Login           *bool   `yaml:"login"`
	Superuser       *bool   `yaml:"superuser"`
	CreateDB        *bool   `yaml:"createdb"`
	CreateRole      *bool   `yaml:"createrole"`
	Inherit         *bool   `yaml:"inherit"`
	Replication     *bool   `yaml:"replication"`
	BypassRLS       *bool   `yaml:"bypassrls"`
	ConnectionLimit *int    `yaml:"connection_limit"`
	ValidUntil      *string `yaml:"valid_until"`
}

// Role defines a PostgreSQL role to provision.
type Role struct {
	Name     string      `yaml:"name"`
	Password string      `yaml:"password"`
	Options  RoleOptions `yaml:"options"`
	Strategy string      `yaml:"strategy"`
}

// Grant defines a privilege grant.
type Grant struct {
	Role               string   `yaml:"role"`
	Privileges         []string `yaml:"privileges"`
	OnSchema           string   `yaml:"on_schema"`
	OnDatabase         bool     `yaml:"on_database"`
	OnTablesInSchema   string   `yaml:"on_tables_in_schema"`
}

// Schema defines a database schema.
type Schema struct {
	Name  string `yaml:"name"`
	Owner string `yaml:"owner"`
}

// Migrations configures SQL migration file discovery for a database.
type Migrations struct {
	Directory string `yaml:"directory"`
}

// DatabaseOptions controls CREATE DATABASE attributes that are fixed at
// creation time and cannot be changed with ALTER DATABASE afterward (encoding,
// locale, template). They are applied only when the database is first created.
type DatabaseOptions struct {
	Encoding  string `yaml:"encoding"`
	Locale    string `yaml:"locale"`
	LcCollate string `yaml:"lc_collate"`
	LcCtype   string `yaml:"lc_ctype"`
	Template  string `yaml:"template"`
}

// IsZero reports whether no database option is set.
func (o DatabaseOptions) IsZero() bool {
	return o == DatabaseOptions{}
}

// Database defines a PostgreSQL database to provision.
type Database struct {
	Name       string          `yaml:"name"`
	Owner      string          `yaml:"owner"`
	Options    DatabaseOptions `yaml:"options"`
	Extensions []string        `yaml:"extensions"`
	Schemas    []Schema        `yaml:"schemas"`
	Grants     []Grant         `yaml:"grants"`
	Migrations *Migrations     `yaml:"migrations"`
	Strategy   string          `yaml:"strategy"`
}

// Config is the top-level YAML configuration.
type Config struct {
	Strategy  string     `yaml:"strategy"`
	Roles     []Role     `yaml:"roles"`
	Databases []Database `yaml:"databases"`
}

var envVarPattern = regexp.MustCompile(`\$\{([^}]+)}`)

// expandEnvVars replaces ${VAR} references with their environment variable values.
func expandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		varName := envVarPattern.FindStringSubmatch(match)[1]
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return match // leave unresolved vars as-is
	})
}

// expandConfig walks the config and expands env vars in string fields.
func expandConfig(cfg *Config) {
	cfg.Strategy = expandEnvVars(cfg.Strategy)
	for i := range cfg.Roles {
		cfg.Roles[i].Name = expandEnvVars(cfg.Roles[i].Name)
		cfg.Roles[i].Password = expandEnvVars(cfg.Roles[i].Password)
		cfg.Roles[i].Strategy = expandEnvVars(cfg.Roles[i].Strategy)
	}
	for i := range cfg.Databases {
		cfg.Databases[i].Name = expandEnvVars(cfg.Databases[i].Name)
		cfg.Databases[i].Strategy = expandEnvVars(cfg.Databases[i].Strategy)
		cfg.Databases[i].Owner = expandEnvVars(cfg.Databases[i].Owner)
		opts := &cfg.Databases[i].Options
		opts.Encoding = expandEnvVars(opts.Encoding)
		opts.Locale = expandEnvVars(opts.Locale)
		opts.LcCollate = expandEnvVars(opts.LcCollate)
		opts.LcCtype = expandEnvVars(opts.LcCtype)
		opts.Template = expandEnvVars(opts.Template)
		for j := range cfg.Databases[i].Extensions {
			cfg.Databases[i].Extensions[j] = expandEnvVars(cfg.Databases[i].Extensions[j])
		}
		for j := range cfg.Databases[i].Schemas {
			cfg.Databases[i].Schemas[j].Name = expandEnvVars(cfg.Databases[i].Schemas[j].Name)
			cfg.Databases[i].Schemas[j].Owner = expandEnvVars(cfg.Databases[i].Schemas[j].Owner)
		}
		if cfg.Databases[i].Migrations != nil {
			cfg.Databases[i].Migrations.Directory = expandEnvVars(cfg.Databases[i].Migrations.Directory)
		}
		for j := range cfg.Databases[i].Grants {
			cfg.Databases[i].Grants[j].Role = expandEnvVars(cfg.Databases[i].Grants[j].Role)
			cfg.Databases[i].Grants[j].OnSchema = expandEnvVars(cfg.Databases[i].Grants[j].OnSchema)
			cfg.Databases[i].Grants[j].OnTablesInSchema = expandEnvVars(cfg.Databases[i].Grants[j].OnTablesInSchema)
			for k := range cfg.Databases[i].Grants[j].Privileges {
				cfg.Databases[i].Grants[j].Privileges[k] = expandEnvVars(cfg.Databases[i].Grants[j].Privileges[k])
			}
		}
	}
}

// Load reads and parses a YAML config file, expanding env vars and validating.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config YAML: %w", err)
	}

	expandConfig(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

// checkNullBytes returns an error if any of the given field/value pairs contain a null byte.
func checkNullBytes(fields ...struct{ path, value string }) error {
	for _, f := range fields {
		if containsNullByte(f.value) {
			return fmt.Errorf("%s: contains null byte", f.path)
		}
	}
	return nil
}

// validateStrategy returns an error if the strategy value is invalid.
func validateStrategy(path, value string) error {
	if !validStrategies[value] {
		return fmt.Errorf("%s: invalid strategy %q (must be \"create\" or \"update\")", path, value)
	}
	return nil
}

// validate checks the config for required fields and consistency.
func validate(cfg *Config) error {
	if err := validateStrategy("strategy", cfg.Strategy); err != nil {
		return err
	}

	roleNames := make(map[string]bool)
	for i, r := range cfg.Roles {
		if err := validateStrategy(fmt.Sprintf("roles[%d].strategy", i), r.Strategy); err != nil {
			return err
		}
		if r.Name == "" {
			return fmt.Errorf("roles[%d]: name is required", i)
		}
		if err := checkNullBytes(
			struct{ path, value string }{fmt.Sprintf("roles[%d].name", i), r.Name},
			struct{ path, value string }{fmt.Sprintf("roles[%d].password", i), r.Password},
		); err != nil {
			return err
		}
		if r.Options.ConnectionLimit != nil && *r.Options.ConnectionLimit < -1 {
			return fmt.Errorf("roles[%d].options.connection_limit: must be >= -1, got %d", i, *r.Options.ConnectionLimit)
		}
		if roleNames[r.Name] {
			return fmt.Errorf("roles[%d]: duplicate role name %q", i, r.Name)
		}
		roleNames[r.Name] = true
	}

	dbNames := make(map[string]bool)
	for i, d := range cfg.Databases {
		if err := validateStrategy(fmt.Sprintf("databases[%d].strategy", i), d.Strategy); err != nil {
			return err
		}
		if d.Name == "" {
			return fmt.Errorf("databases[%d]: name is required", i)
		}
		if err := checkNullBytes(
			struct{ path, value string }{fmt.Sprintf("databases[%d].name", i), d.Name},
			struct{ path, value string }{fmt.Sprintf("databases[%d].owner", i), d.Owner},
		); err != nil {
			return err
		}
		if reservedDatabases[d.Name] {
			return fmt.Errorf("databases[%d]: %q is a reserved database name", i, d.Name)
		}
		if dbNames[d.Name] {
			return fmt.Errorf("databases[%d]: duplicate database name %q", i, d.Name)
		}
		dbNames[d.Name] = true

		if d.Owner != "" && !roleNames[d.Owner] {
			return fmt.Errorf("databases[%d].owner: role %q is not declared in roles", i, d.Owner)
		}

		if err := checkNullBytes(
			struct{ path, value string }{fmt.Sprintf("databases[%d].options.encoding", i), d.Options.Encoding},
			struct{ path, value string }{fmt.Sprintf("databases[%d].options.locale", i), d.Options.Locale},
			struct{ path, value string }{fmt.Sprintf("databases[%d].options.lc_collate", i), d.Options.LcCollate},
			struct{ path, value string }{fmt.Sprintf("databases[%d].options.lc_ctype", i), d.Options.LcCtype},
			struct{ path, value string }{fmt.Sprintf("databases[%d].options.template", i), d.Options.Template},
		); err != nil {
			return err
		}
		// Postgres rejects LOCALE combined with LC_COLLATE/LC_CTYPE in CREATE DATABASE.
		if d.Options.Locale != "" && (d.Options.LcCollate != "" || d.Options.LcCtype != "") {
			return fmt.Errorf("databases[%d].options: locale cannot be combined with lc_collate or lc_ctype", i)
		}

		for j, ext := range d.Extensions {
			if ext == "" {
				return fmt.Errorf("databases[%d].extensions[%d]: name must not be empty", i, j)
			}
			if containsNullByte(ext) {
				return fmt.Errorf("databases[%d].extensions[%d]: contains null byte", i, j)
			}
		}

		schemaNames := make(map[string]bool)
		for j, s := range d.Schemas {
			if s.Name == "" {
				return fmt.Errorf("databases[%d].schemas[%d]: name is required", i, j)
			}
			if err := checkNullBytes(
				struct{ path, value string }{fmt.Sprintf("databases[%d].schemas[%d].name", i, j), s.Name},
				struct{ path, value string }{fmt.Sprintf("databases[%d].schemas[%d].owner", i, j), s.Owner},
			); err != nil {
				return err
			}
			if schemaNames[s.Name] {
				return fmt.Errorf("databases[%d].schemas[%d]: duplicate schema name %q", i, j, s.Name)
			}
			schemaNames[s.Name] = true
			if s.Owner != "" && !roleNames[s.Owner] {
				return fmt.Errorf("databases[%d].schemas[%d].owner: role %q is not declared in roles", i, j, s.Owner)
			}
		}

		for j, g := range d.Grants {
			if g.Role == "" {
				return fmt.Errorf("databases[%d].grants[%d]: role is required", i, j)
			}
			if containsNullByte(g.Role) {
				return fmt.Errorf("databases[%d].grants[%d].role: contains null byte", i, j)
			}
			if !roleNames[g.Role] {
				return fmt.Errorf("databases[%d].grants[%d].role: role %q is not declared in roles", i, j, g.Role)
			}
			if len(g.Privileges) == 0 {
				return fmt.Errorf("databases[%d].grants[%d]: privileges is required", i, j)
			}
			for k, priv := range g.Privileges {
				normalized := strings.ToUpper(strings.TrimSpace(priv))
				if !validPrivileges[normalized] {
					return fmt.Errorf("databases[%d].grants[%d].privileges[%d]: invalid privilege %q", i, j, k, priv)
				}
			}
			targets := 0
			if g.OnSchema != "" {
				targets++
			}
			if g.OnDatabase {
				targets++
			}
			if g.OnTablesInSchema != "" {
				targets++
			}
			if targets != 1 {
				return fmt.Errorf("databases[%d].grants[%d]: exactly one of on_schema, on_database, or on_tables_in_schema is required", i, j)
			}
		}

		if d.Migrations != nil {
			if d.Migrations.Directory == "" {
				return fmt.Errorf("databases[%d].migrations: directory is required", i)
			}
			if containsNullByte(d.Migrations.Directory) {
				return fmt.Errorf("databases[%d].migrations.directory: contains null byte", i)
			}
		}
	}

	return nil
}
