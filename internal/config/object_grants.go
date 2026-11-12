package config

import (
	"fmt"
	"strings"
)

// QualifiedName is a schema-qualified object name such as app.users.
type QualifiedName struct {
	Schema string
	Name   string
}

// String returns the name in schema.name form.
func (q QualifiedName) String() string {
	return q.Schema + "." + q.Name
}

// ParseQualifiedName parses "schema.name". Exactly one dot is required: unqualified names
// would resolve via search_path, which differs between sessions. Both parts are used
// verbatim as quoted identifiers, so they are case-sensitive; identifiers containing a
// dot or a double quote are not supported. Double quotes are rejected because they almost
// always mean a pre-quoted name ("App"."Users"), which would otherwise be quoted twice.
func ParseQualifiedName(s string) (QualifiedName, error) {
	if strings.Contains(s, `"`) {
		return QualifiedName{}, fmt.Errorf("%q must not contain double quotes: write schema.name unquoted, it is quoted automatically and matched case-sensitively", s)
	}
	schema, name, ok := strings.Cut(s, ".")
	if !ok || schema == "" || name == "" || strings.Contains(name, ".") {
		return QualifiedName{}, fmt.Errorf("%q must be schema-qualified as schema.name", s)
	}
	return QualifiedName{Schema: schema, Name: name}, nil
}

// columnPrivileges are the privileges PostgreSQL accepts in column-level grants.
var columnPrivileges = privilegeSet("ALL", "SELECT", "INSERT", "UPDATE", "REFERENCES")

// validateObjectGrant checks the on_tables and columns fields of a grant at path.
func validateObjectGrant(path string, g Grant) error {
	if len(g.Columns) > 0 && len(g.OnTables) == 0 {
		return fmt.Errorf("%s.columns: only allowed together with on_tables", path)
	}

	seenTables := make(map[string]bool)
	for k, table := range g.OnTables {
		tablePath := fmt.Sprintf("%s.on_tables[%d]", path, k)
		if containsNullByte(table) {
			return fmt.Errorf("%s: contains null byte", tablePath)
		}
		if _, err := ParseQualifiedName(table); err != nil {
			return fmt.Errorf("%s: %w", tablePath, err)
		}
		if seenTables[table] {
			return fmt.Errorf("%s: duplicate table %q", tablePath, table)
		}
		seenTables[table] = true
	}

	seenColumns := make(map[string]bool)
	for k, column := range g.Columns {
		columnPath := fmt.Sprintf("%s.columns[%d]", path, k)
		if column == "" {
			return fmt.Errorf("%s: must not be empty", columnPath)
		}
		if containsNullByte(column) {
			return fmt.Errorf("%s: contains null byte", columnPath)
		}
		if strings.Contains(column, `"`) {
			return fmt.Errorf("%s: %q must not contain double quotes: write the column unquoted, it is quoted automatically and matched case-sensitively", columnPath, column)
		}
		if seenColumns[column] {
			return fmt.Errorf("%s: duplicate column %q", columnPath, column)
		}
		seenColumns[column] = true
	}
	if len(g.Columns) > 0 {
		for k, priv := range g.Privileges {
			if !columnPrivileges[normalizePrivilege(priv)] {
				return fmt.Errorf("%s.privileges[%d]: privilege %q is not valid with columns (allowed: %s)",
					path, k, priv, strings.Join(sortedPrivileges(columnPrivileges), ", "))
			}
		}
	}
	return nil
}
