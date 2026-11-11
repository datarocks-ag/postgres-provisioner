package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SettingValue is the desired value of a run-time parameter (ALTER ROLE/DATABASE ... SET).
//
// In YAML it is a scalar (one value), a sequence (a list setting such as search_path,
// where every element is quoted separately), or null, which resets the setting.
type SettingValue struct {
	// Values holds the value elements. It is nil for a reset (YAML null).
	Values []string
}

// IsReset reports whether the setting should be reset (YAML null).
func (v SettingValue) IsReset() bool {
	return v.Values == nil
}

// UnmarshalYAML accepts a scalar or a sequence of scalars. yaml.v3 does not call it for
// null values, which therefore decode to the zero SettingValue, i.e. a reset.
func (v *SettingValue) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		v.Values = []string{node.Value}
		return nil
	case yaml.SequenceNode:
		v.Values = make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: list setting values must be scalars", item.Line)
			}
			v.Values = append(v.Values, item.Value)
		}
		return nil
	default:
		return fmt.Errorf("line %d: setting value must be a scalar, a list or null", node.Line)
	}
}

// Settings maps run-time parameter names to their desired values.
type Settings map[string]SettingValue

// SortedNames returns the setting names sorted case-insensitively (PostgreSQL parameter
// names are case-insensitive), so generated SQL is deterministic.
func (s Settings) SortedNames() []string {
	names := make([]string, 0, len(s))
	for name := range s {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		li, lj := strings.ToLower(names[i]), strings.ToLower(names[j])
		if li != lj {
			return li < lj
		}
		return names[i] < names[j]
	})
	return names
}

// settingNamePattern accepts PostgreSQL parameter names, including custom
// "prefix.name" parameters. Names are emitted unquoted, so this is the injection guard.
var settingNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*(\.[A-Za-z_][A-Za-z0-9_$]*)?$`)

// expandSettings expands env var references in the setting values of a settings block at path.
func (e *expander) expandSettings(path string, settings Settings) {
	for name, value := range settings {
		for i, v := range value.Values {
			value.Values[i] = e.expand(fmt.Sprintf("%s.%s[%d]", path, name, i), v)
		}
		settings[name] = value
	}
}

// validateSettings checks names and values of a settings block at path. Names are
// case-insensitive in PostgreSQL, so names differing only in case are duplicates.
func validateSettings(path string, settings Settings) error {
	seen := make(map[string]string)
	for _, name := range settings.SortedNames() {
		value := settings[name]
		if !settingNamePattern.MatchString(name) {
			return fmt.Errorf("%s: invalid setting name %q", path, name)
		}
		lower := strings.ToLower(name)
		if other, dup := seen[lower]; dup {
			return fmt.Errorf("%s: settings %q and %q refer to the same parameter", path, other, name)
		}
		seen[lower] = name
		if value.Values != nil && len(value.Values) == 0 {
			return fmt.Errorf("%s.%s: list must not be empty (use null to reset)", path, name)
		}
		for _, v := range value.Values {
			if containsNullByte(v) {
				return fmt.Errorf("%s.%s: contains null byte", path, name)
			}
		}
	}
	return nil
}

// sortedKeys returns the keys of m in sorted order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SortedRoles returns the role names of a role_settings block in sorted order.
func SortedRoles(roleSettings map[string]Settings) []string {
	return sortedKeys(roleSettings)
}
