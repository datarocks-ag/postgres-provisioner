package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Extension defines a PostgreSQL extension to install in a database.
//
// In YAML it is either the extension name or a mapping with options:
//
//	extensions:
//	  - pgcrypto
//	  - { name: pg_stat_statements, schema: monitoring, version: "1.10", cascade: true }
type Extension struct {
	Name string `yaml:"name"`
	// Schema installs the extension's objects into this schema (CREATE EXTENSION ... SCHEMA).
	// It only applies on install; a differing schema on an installed extension is warned about.
	Schema string `yaml:"schema"`
	// Version pins the extension version. Installed extensions at a different version are
	// updated with ALTER EXTENSION ... UPDATE TO (strategy "update" only).
	Version string `yaml:"version"`
	// Cascade also installs extensions this one depends on.
	Cascade bool `yaml:"cascade"`
}

// UnmarshalYAML accepts the extension name as a plain string or a mapping with options.
func (e *Extension) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		e.Name = node.Value
		return nil
	case yaml.MappingNode:
		type plain Extension // no UnmarshalYAML method, avoids recursion
		return node.Decode((*plain)(e))
	default:
		return fmt.Errorf("line %d: extension must be a name or a mapping", node.Line)
	}
}
