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

// extensionFields are the keys allowed in the mapping form of an extension.
var extensionFields = map[string]bool{"name": true, "schema": true, "version": true, "cascade": true}

// UnmarshalYAML accepts the extension name as a plain string or a mapping with options.
// Unknown keys in the mapping are rejected, so a typo like "shema" doesn't silently drop
// the option (node.Decode has no KnownFields equivalent).
func (e *Extension) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		e.Name = node.Value
		return nil
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			if !extensionFields[key.Value] {
				return fmt.Errorf("line %d: unknown extension field %q (allowed: name, schema, version, cascade)", key.Line, key.Value)
			}
		}
		type plain Extension // no UnmarshalYAML method, avoids recursion
		return node.Decode((*plain)(e))
	default:
		return fmt.Errorf("line %d: extension must be a name or a mapping", node.Line)
	}
}
