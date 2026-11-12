package config

import (
	"fmt"
	"os"
	"slices"
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
	// MemberOf lists roles this role is granted membership in (GRANT <group> TO <role>).
	// Entries may be predefined roles such as pg_monitor, so they need not be declared in roles.
	MemberOf []string `yaml:"member_of"`
	Strategy string   `yaml:"strategy"`
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

// expandConfig walks the config and expands env var references in string fields.
// It returns every unresolved reference as a joined error.
func expandConfig(cfg *Config) error {
	e := &expander{}
	cfg.Strategy = e.expand("strategy", cfg.Strategy)
	for i := range cfg.Roles {
		r := &cfg.Roles[i]
		path := fmt.Sprintf("roles[%d]", i)
		r.Name = e.expand(path+".name", r.Name)
		r.Password = e.expandSecret(path+".password", r.Password)
		r.Strategy = e.expand(path+".strategy", r.Strategy)
		for j := range r.MemberOf {
			r.MemberOf[j] = e.expand(fmt.Sprintf("%s.member_of[%d]", path, j), r.MemberOf[j])
		}
		if r.Options.ValidUntil != nil {
			validUntil := e.expand(path+".options.valid_until", *r.Options.ValidUntil)
			r.Options.ValidUntil = &validUntil
		}
	}
	for i := range cfg.Databases {
		d := &cfg.Databases[i]
		path := fmt.Sprintf("databases[%d]", i)
		d.Name = e.expand(path+".name", d.Name)
		d.Strategy = e.expand(path+".strategy", d.Strategy)
		d.Owner = e.expand(path+".owner", d.Owner)
		d.Options.Encoding = e.expand(path+".options.encoding", d.Options.Encoding)
		d.Options.Locale = e.expand(path+".options.locale", d.Options.Locale)
		d.Options.LcCollate = e.expand(path+".options.lc_collate", d.Options.LcCollate)
		d.Options.LcCtype = e.expand(path+".options.lc_ctype", d.Options.LcCtype)
		d.Options.Template = e.expand(path+".options.template", d.Options.Template)
		for j := range d.Extensions {
			d.Extensions[j] = e.expand(fmt.Sprintf("%s.extensions[%d]", path, j), d.Extensions[j])
		}
		for j := range d.Schemas {
			sPath := fmt.Sprintf("%s.schemas[%d]", path, j)
			d.Schemas[j].Name = e.expand(sPath+".name", d.Schemas[j].Name)
			d.Schemas[j].Owner = e.expand(sPath+".owner", d.Schemas[j].Owner)
		}
		if d.Migrations != nil {
			d.Migrations.Directory = e.expand(path+".migrations.directory", d.Migrations.Directory)
		}
		for j := range d.Grants {
			g := &d.Grants[j]
			gPath := fmt.Sprintf("%s.grants[%d]", path, j)
			g.Role = e.expand(gPath+".role", g.Role)
			g.OnSchema = e.expand(gPath+".on_schema", g.OnSchema)
			g.OnTablesInSchema = e.expand(gPath+".on_tables_in_schema", g.OnTablesInSchema)
			for k := range g.Privileges {
				g.Privileges[k] = e.expand(fmt.Sprintf("%s.privileges[%d]", gPath, k), g.Privileges[k])
			}
		}
	}
	return e.err()
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

	if err := expandConfig(&cfg); err != nil {
		return nil, fmt.Errorf("expanding environment variables: %w", err)
	}

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

// validateMemberOf checks a role's member_of entries. Group roles are not required to be
// declared in roles because predefined roles (pg_monitor, pg_read_all_stats, ...) never are;
// a missing group role surfaces as an error when the GRANT is executed.
func validateMemberOf(i int, r Role) error {
	seen := make(map[string]bool)
	for j, group := range r.MemberOf {
		path := fmt.Sprintf("roles[%d].member_of[%d]", i, j)
		if group == "" {
			return fmt.Errorf("%s: role name must not be empty", path)
		}
		if containsNullByte(group) {
			return fmt.Errorf("%s: contains null byte", path)
		}
		if group == r.Name {
			return fmt.Errorf("%s: role %q cannot be a member of itself", path, group)
		}
		if seen[group] {
			return fmt.Errorf("%s: duplicate role %q", path, group)
		}
		seen[group] = true
	}
	return nil
}

// validateMemberOfCycles rejects circular memberships among roles declared in the config
// (e.g. a.member_of [b] and b.member_of [a]), which PostgreSQL refuses mid-run. Undeclared
// group roles (such as predefined roles) have no member_of in this config and cannot close
// a cycle, so they are ignored. Roles are traversed in config order for deterministic errors.
func validateMemberOfCycles(roles []Role) error {
	index := make(map[string]int, len(roles))
	for i, r := range roles {
		index[r.Name] = i
	}

	const (
		unvisited = iota
		inProgress
		done
	)
	state := make([]int, len(roles))
	var stack []string

	var visit func(i int) error
	visit = func(i int) error {
		state[i] = inProgress
		stack = append(stack, roles[i].Name)
		for j, group := range roles[i].MemberOf {
			k, declared := index[group]
			if !declared {
				continue
			}
			switch state[k] {
			case inProgress:
				cycle := append([]string{}, stack[slices.Index(stack, group):]...)
				cycle = append(cycle, group)
				return fmt.Errorf("roles[%d].member_of[%d]: circular role membership %s",
					i, j, strings.Join(cycle, " -> "))
			case unvisited:
				if err := visit(k); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[i] = done
		return nil
	}

	for i := range roles {
		if state[i] == unvisited {
			if err := visit(i); err != nil {
				return err
			}
		}
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
		nullByteFields := []struct{ path, value string }{
			{fmt.Sprintf("roles[%d].name", i), r.Name},
			{fmt.Sprintf("roles[%d].password", i), r.Password},
		}
		if r.Options.ValidUntil != nil {
			nullByteFields = append(nullByteFields,
				struct{ path, value string }{fmt.Sprintf("roles[%d].options.valid_until", i), *r.Options.ValidUntil})
		}
		if err := checkNullBytes(nullByteFields...); err != nil {
			return err
		}
		if r.Options.ConnectionLimit != nil && *r.Options.ConnectionLimit < -1 {
			return fmt.Errorf("roles[%d].options.connection_limit: must be >= -1, got %d", i, *r.Options.ConnectionLimit)
		}
		if roleNames[r.Name] {
			return fmt.Errorf("roles[%d]: duplicate role name %q", i, r.Name)
		}
		roleNames[r.Name] = true
		if err := validateMemberOf(i, r); err != nil {
			return err
		}
	}
	if err := validateMemberOfCycles(cfg.Roles); err != nil {
		return err
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
