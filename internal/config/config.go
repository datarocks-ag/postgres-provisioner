package config

import (
	"fmt"
	"os"
	"slices"
	"sort"
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

// privilegesByTarget lists the privileges PostgreSQL accepts for each grant target.
// Checking them at load time turns a mid-run "invalid privilege type" failure into a
// config error.
var privilegesByTarget = map[GrantTarget]map[string]bool{
	TargetDatabase:          privilegeSet("ALL", "CREATE", "CONNECT", "TEMPORARY", "TEMP"),
	TargetSchema:            privilegeSet("ALL", "CREATE", "USAGE"),
	TargetTablesInSchema:    privilegeSet("ALL", "SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"),
	TargetSequencesInSchema: privilegeSet("ALL", "USAGE", "SELECT", "UPDATE"),
	TargetFunctionsInSchema: privilegeSet("ALL", "EXECUTE"),
}

func privilegeSet(privileges ...string) map[string]bool {
	set := make(map[string]bool, len(privileges))
	for _, p := range privileges {
		set[p] = true
	}
	return set
}

func sortedPrivileges(set map[string]bool) []string {
	privileges := make([]string, 0, len(set))
	for p := range set {
		privileges = append(privileges, p)
	}
	sort.Strings(privileges)
	return privileges
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

// Grant defines a privilege grant. Exactly one target field must be set.
type Grant struct {
	Role                string   `yaml:"role"`
	Privileges          []string `yaml:"privileges"`
	OnSchema            string   `yaml:"on_schema"`
	OnDatabase          bool     `yaml:"on_database"`
	OnTablesInSchema    string   `yaml:"on_tables_in_schema"`
	OnSequencesInSchema string   `yaml:"on_sequences_in_schema"`
	OnFunctionsInSchema string   `yaml:"on_functions_in_schema"`
}

// GrantTarget identifies the kind of object a grant applies to. Its value is the YAML key.
type GrantTarget string

// Grant targets, one per YAML key.
const (
	TargetDatabase          GrantTarget = "on_database"
	TargetSchema            GrantTarget = "on_schema"
	TargetTablesInSchema    GrantTarget = "on_tables_in_schema"
	TargetSequencesInSchema GrantTarget = "on_sequences_in_schema"
	TargetFunctionsInSchema GrantTarget = "on_functions_in_schema"
)

// Targets returns every target set on the grant. A valid grant has exactly one.
func (g Grant) Targets() []GrantTarget {
	var targets []GrantTarget
	if g.OnDatabase {
		targets = append(targets, TargetDatabase)
	}
	if g.OnSchema != "" {
		targets = append(targets, TargetSchema)
	}
	if g.OnTablesInSchema != "" {
		targets = append(targets, TargetTablesInSchema)
	}
	if g.OnSequencesInSchema != "" {
		targets = append(targets, TargetSequencesInSchema)
	}
	if g.OnFunctionsInSchema != "" {
		targets = append(targets, TargetFunctionsInSchema)
	}
	return targets
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
	// Revokes lists privileges to remove, applied before Grants. Only on_database and
	// on_schema targets are supported; role may be PUBLIC.
	Revokes    []Grant     `yaml:"revokes"`
	Migrations *Migrations `yaml:"migrations"`
	Strategy   string      `yaml:"strategy"`
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
			e.expandGrant(fmt.Sprintf("%s.grants[%d]", path, j), &d.Grants[j])
		}
		for j := range d.Revokes {
			e.expandGrant(fmt.Sprintf("%s.revokes[%d]", path, j), &d.Revokes[j])
		}
	}
	return e.err()
}

// expandGrant expands env var references in a grant or revoke entry at path.
func (e *expander) expandGrant(path string, g *Grant) {
	g.Role = e.expand(path+".role", g.Role)
	g.OnSchema = e.expand(path+".on_schema", g.OnSchema)
	g.OnTablesInSchema = e.expand(path+".on_tables_in_schema", g.OnTablesInSchema)
	g.OnSequencesInSchema = e.expand(path+".on_sequences_in_schema", g.OnSequencesInSchema)
	g.OnFunctionsInSchema = e.expand(path+".on_functions_in_schema", g.OnFunctionsInSchema)
	for k := range g.Privileges {
		g.Privileges[k] = e.expand(fmt.Sprintf("%s.privileges[%d]", path, k), g.Privileges[k])
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

// IsPublic reports whether role is the PUBLIC pseudo-role (case-insensitive).
// PostgreSQL reserves the name, so no real role can be called "public".
func IsPublic(role string) bool {
	return strings.EqualFold(role, "PUBLIC")
}

// normalizePrivilege returns the canonical upper-case form of a privilege keyword.
func normalizePrivilege(privilege string) string {
	return strings.ToUpper(strings.TrimSpace(privilege))
}

// validateGrant checks a single grant entry. path is the entry's config path, e.g.
// "databases[0].grants[1]".
func validateGrant(path string, g Grant, roleNames map[string]bool) error {
	if g.Role == "" {
		return fmt.Errorf("%s: role is required", path)
	}
	if err := checkNullBytes(
		struct{ path, value string }{path + ".role", g.Role},
		struct{ path, value string }{path + ".on_schema", g.OnSchema},
		struct{ path, value string }{path + ".on_tables_in_schema", g.OnTablesInSchema},
		struct{ path, value string }{path + ".on_sequences_in_schema", g.OnSequencesInSchema},
		struct{ path, value string }{path + ".on_functions_in_schema", g.OnFunctionsInSchema},
	); err != nil {
		return err
	}
	if !roleNames[g.Role] {
		return fmt.Errorf("%s.role: role %q is not declared in roles", path, g.Role)
	}
	if len(g.Privileges) == 0 {
		return fmt.Errorf("%s: privileges is required", path)
	}
	targets := g.Targets()
	if len(targets) != 1 {
		return fmt.Errorf("%s: exactly one of on_database, on_schema, on_tables_in_schema, on_sequences_in_schema, or on_functions_in_schema is required", path)
	}
	return validatePrivileges(path, g.Privileges, targets[0])
}

// validatePrivileges checks privileges against the injection allowlist and the set
// PostgreSQL accepts for target.
func validatePrivileges(path string, privileges []string, target GrantTarget) error {
	allowed := privilegesByTarget[target]
	for k, priv := range privileges {
		normalized := normalizePrivilege(priv)
		if !validPrivileges[normalized] {
			return fmt.Errorf("%s.privileges[%d]: invalid privilege %q", path, k, priv)
		}
		if !allowed[normalized] {
			return fmt.Errorf("%s.privileges[%d]: privilege %q is not valid for %s (allowed: %s)",
				path, k, priv, target, strings.Join(sortedPrivileges(allowed), ", "))
		}
	}
	return nil
}

// revocableTargets are the targets supported in revokes. Revoking on *_in_schema targets
// would also need ALTER DEFAULT PRIVILEGES ... REVOKE, which is not implemented.
var revocableTargets = map[GrantTarget]bool{
	TargetDatabase: true,
	TargetSchema:   true,
}

// validateRevoke checks a single revoke entry. Unlike grants, role may be PUBLIC.
func validateRevoke(path string, r Grant, roleNames map[string]bool) error {
	if r.Role == "" {
		return fmt.Errorf("%s: role is required", path)
	}
	if err := checkNullBytes(
		struct{ path, value string }{path + ".role", r.Role},
		struct{ path, value string }{path + ".on_schema", r.OnSchema},
	); err != nil {
		return err
	}
	if !IsPublic(r.Role) && !roleNames[r.Role] {
		return fmt.Errorf("%s.role: role %q is not declared in roles (use PUBLIC for all roles)", path, r.Role)
	}
	if len(r.Privileges) == 0 {
		return fmt.Errorf("%s: privileges is required", path)
	}
	targets := r.Targets()
	if len(targets) != 1 {
		return fmt.Errorf("%s: exactly one of on_database or on_schema is required", path)
	}
	if !revocableTargets[targets[0]] {
		return fmt.Errorf("%s: %s is not supported for revokes (supported: on_database, on_schema)", path, targets[0])
	}
	return validatePrivileges(path, r.Privileges, targets[0])
}

// ExpandPrivileges returns the concrete privileges a grant or revoke covers on target:
// normalized, deduplicated, sorted, with ALL expanded and TEMP mapped to TEMPORARY.
// The names match the privilege_type values reported by aclexplode().
func ExpandPrivileges(target GrantTarget, privileges []string) []string {
	set := make(map[string]bool)
	for _, priv := range privileges {
		switch normalized := normalizePrivilege(priv); normalized {
		case "ALL":
			for p := range privilegesByTarget[target] {
				if p != "ALL" && p != "TEMP" {
					set[p] = true
				}
			}
		case "TEMP":
			set["TEMPORARY"] = true
		default:
			set[normalized] = true
		}
	}
	return sortedPrivileges(set)
}

// privilegeKey identifies one privilege of one grantee on one object, for comparing
// grants with revokes.
type privilegeKey struct {
	role      string
	target    GrantTarget
	object    string
	privilege string
}

func privilegeKeys(g Grant) []privilegeKey {
	targets := g.Targets()
	if len(targets) != 1 || !revocableTargets[targets[0]] {
		return nil
	}
	role := g.Role
	if IsPublic(role) {
		role = "PUBLIC"
	}
	keys := make([]privilegeKey, 0, len(g.Privileges))
	for _, priv := range ExpandPrivileges(targets[0], g.Privileges) {
		keys = append(keys, privilegeKey{role: role, target: targets[0], object: g.OnSchema, privilege: priv})
	}
	return keys
}

// checkRevokeConflicts rejects revokes that contradict a grant (the result would depend
// on execution order) or repeat another revoke.
func checkRevokeConflicts(path string, d Database) error {
	granted := make(map[privilegeKey]int)
	for k, g := range d.Grants {
		for _, key := range privilegeKeys(g) {
			granted[key] = k
		}
	}
	revoked := make(map[privilegeKey]int)
	for j, r := range d.Revokes {
		for _, key := range privilegeKeys(r) {
			if k, ok := granted[key]; ok {
				return fmt.Errorf("%s.revokes[%d]: conflicts with grants[%d] (role %q, %s on %s)",
					path, j, k, key.role, key.privilege, describeTarget(key))
			}
			if prev, ok := revoked[key]; ok {
				return fmt.Errorf("%s.revokes[%d]: duplicates revokes[%d] (role %q, %s on %s)",
					path, j, prev, key.role, key.privilege, describeTarget(key))
			}
			revoked[key] = j
		}
	}
	return nil
}

func describeTarget(key privilegeKey) string {
	if key.target == TargetSchema {
		return fmt.Sprintf("schema %q", key.object)
	}
	return "database"
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
			if err := validateGrant(fmt.Sprintf("databases[%d].grants[%d]", i, j), g, roleNames); err != nil {
				return err
			}
		}
		for j, r := range d.Revokes {
			if err := validateRevoke(fmt.Sprintf("databases[%d].revokes[%d]", i, j), r, roleNames); err != nil {
				return err
			}
		}
		if err := checkRevokeConflicts(fmt.Sprintf("databases[%d]", i), d); err != nil {
			return err
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
