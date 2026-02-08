package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// RoleOptions controls PostgreSQL role attributes.
type RoleOptions struct {
	Login           *bool `yaml:"login"`
	Superuser       *bool `yaml:"superuser"`
	CreateDB        *bool `yaml:"createdb"`
	CreateRole      *bool `yaml:"createrole"`
	ConnectionLimit *int  `yaml:"connection_limit"`
}

// Role defines a PostgreSQL role to provision.
type Role struct {
	Name     string      `yaml:"name"`
	Password string      `yaml:"password"`
	Options  RoleOptions `yaml:"options"`
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

// Database defines a PostgreSQL database to provision.
type Database struct {
	Name       string   `yaml:"name"`
	Owner      string   `yaml:"owner"`
	Extensions []string `yaml:"extensions"`
	Schemas    []Schema `yaml:"schemas"`
	Grants     []Grant  `yaml:"grants"`
}

// Config is the top-level YAML configuration.
type Config struct {
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
	for i := range cfg.Roles {
		cfg.Roles[i].Name = expandEnvVars(cfg.Roles[i].Name)
		cfg.Roles[i].Password = expandEnvVars(cfg.Roles[i].Password)
	}
	for i := range cfg.Databases {
		cfg.Databases[i].Name = expandEnvVars(cfg.Databases[i].Name)
		cfg.Databases[i].Owner = expandEnvVars(cfg.Databases[i].Owner)
		for j := range cfg.Databases[i].Extensions {
			cfg.Databases[i].Extensions[j] = expandEnvVars(cfg.Databases[i].Extensions[j])
		}
		for j := range cfg.Databases[i].Schemas {
			cfg.Databases[i].Schemas[j].Name = expandEnvVars(cfg.Databases[i].Schemas[j].Name)
			cfg.Databases[i].Schemas[j].Owner = expandEnvVars(cfg.Databases[i].Schemas[j].Owner)
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

// validate checks the config for required fields and consistency.
func validate(cfg *Config) error {
	roleNames := make(map[string]bool)
	for i, r := range cfg.Roles {
		if r.Name == "" {
			return fmt.Errorf("roles[%d]: name is required", i)
		}
		if roleNames[r.Name] {
			return fmt.Errorf("roles[%d]: duplicate role name %q", i, r.Name)
		}
		roleNames[r.Name] = true
	}

	dbNames := make(map[string]bool)
	for i, d := range cfg.Databases {
		if d.Name == "" {
			return fmt.Errorf("databases[%d]: name is required", i)
		}
		if dbNames[d.Name] {
			return fmt.Errorf("databases[%d]: duplicate database name %q", i, d.Name)
		}
		dbNames[d.Name] = true

		for j, s := range d.Schemas {
			if s.Name == "" {
				return fmt.Errorf("databases[%d].schemas[%d]: name is required", i, j)
			}
		}

		for j, g := range d.Grants {
			if g.Role == "" {
				return fmt.Errorf("databases[%d].grants[%d]: role is required", i, j)
			}
			if len(g.Privileges) == 0 {
				return fmt.Errorf("databases[%d].grants[%d]: privileges is required", i, j)
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
	}

	return nil
}
