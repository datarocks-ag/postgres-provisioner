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
// where every element is quoted separately), or an explicit null, which resets the setting.
type SettingValue struct {
	// Values holds the value elements. It is nil for a reset (YAML null).
	Values []string

	// invalid describes why the YAML value was rejected. It is reported by validation,
	// which knows the config path, instead of by decoding, which only knows the line.
	invalid string
}

// IsReset reports whether the setting should be reset (YAML null).
func (v SettingValue) IsReset() bool {
	return v.Values == nil
}

// UnmarshalYAML accepts a scalar or a sequence of non-null scalars. Null values never
// reach it (yaml.v3 skips UnmarshalYAML for them); Settings.UnmarshalYAML handles those.
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
			if isNullNode(item) {
				v.invalid = "list elements must not be null or empty"
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

// UnmarshalYAML decodes a settings mapping. It inspects null values itself because only
// an explicit `null` means reset: an empty value (`work_mem:`) or `~` is more likely a
// forgotten value, and silently resetting the setting would be surprising.
func (s *Settings) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: settings must be a mapping", node.Line)
	}
	settings := make(Settings, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode, valueNode := node.Content[i], node.Content[i+1]
		var name string
		if err := keyNode.Decode(&name); err != nil {
			return err
		}
		if _, dup := settings[name]; dup {
			return fmt.Errorf("line %d: setting %q is defined more than once", keyNode.Line, name)
		}

		var value SettingValue
		switch {
		case isNullNode(valueNode) && isExplicitNull(valueNode):
			// Zero value: a reset.
		case isNullNode(valueNode):
			value.invalid = "value is empty (use null to reset the setting)"
		default:
			if err := valueNode.Decode(&value); err != nil {
				return err
			}
		}
		settings[name] = value
	}
	*s = settings
	return nil
}

// isNullNode reports whether node is a YAML null: empty, `~`, or `null`.
func isNullNode(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.ShortTag() == "!!null"
}

// isExplicitNull reports whether a null node is spelled out as the word null.
func isExplicitNull(node *yaml.Node) bool {
	switch node.Value {
	case "null", "Null", "NULL":
		return true
	}
	return false
}

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
		if value.invalid != "" {
			return fmt.Errorf("%s.%s: %s", path, name, value.invalid)
		}
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
