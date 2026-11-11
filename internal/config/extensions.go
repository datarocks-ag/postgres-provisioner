package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Extension defines a PostgreSQL extension to install in a database.
//
// In YAML it is written as the extension name.
type Extension struct {
	Name string `yaml:"name"`
}

// UnmarshalYAML accepts the extension name as a plain string.
func (e *Extension) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: extension must be a name", node.Line)
	}
	e.Name = node.Value
	return nil
}
