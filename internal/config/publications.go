package config

import (
	"fmt"
	"strings"
)

// Publication defines a logical replication publication (CREATE PUBLICATION), e.g. as the
// source for CDC tools such as Debezium or Redpanda Connect.
type Publication struct {
	Name string `yaml:"name"`
	// Tables lists schema-qualified tables (schema.table). Mutually exclusive with AllTables.
	Tables []string `yaml:"tables"`
	// AllTables publishes every table in the database (FOR ALL TABLES, requires superuser).
	AllTables bool `yaml:"all_tables"`
	// Publish limits the published operations (insert, update, delete, truncate).
	// Empty means PostgreSQL's default: all four.
	Publish []string `yaml:"publish"`
}

// PublishOperations are the operations a publication can publish, in PostgreSQL's order.
var PublishOperations = []string{"insert", "update", "delete", "truncate"}

// EffectivePublish returns the normalized operations the publication publishes.
func (p Publication) EffectivePublish() []string {
	if len(p.Publish) == 0 {
		return PublishOperations
	}
	selected := make(map[string]bool, len(p.Publish))
	for _, op := range p.Publish {
		selected[strings.ToLower(strings.TrimSpace(op))] = true
	}
	var ops []string
	for _, op := range PublishOperations {
		if selected[op] {
			ops = append(ops, op)
		}
	}
	return ops
}

// expandPublication expands env var references in a publication at path.
func (e *expander) expandPublication(path string, p *Publication) {
	p.Name = e.expand(path+".name", p.Name)
	for i := range p.Tables {
		p.Tables[i] = e.expand(fmt.Sprintf("%s.tables[%d]", path, i), p.Tables[i])
	}
	for i := range p.Publish {
		p.Publish[i] = e.expand(fmt.Sprintf("%s.publish[%d]", path, i), p.Publish[i])
	}
}

// validatePublications checks a database's publications at path (e.g. "databases[0]").
func validatePublications(path string, publications []Publication) error {
	validOps := make(map[string]bool, len(PublishOperations))
	for _, op := range PublishOperations {
		validOps[op] = true
	}

	names := make(map[string]bool)
	for i, pub := range publications {
		pubPath := fmt.Sprintf("%s.publications[%d]", path, i)
		if pub.Name == "" {
			return fmt.Errorf("%s: name is required", pubPath)
		}
		if containsNullByte(pub.Name) {
			return fmt.Errorf("%s.name: contains null byte", pubPath)
		}
		if names[pub.Name] {
			return fmt.Errorf("%s: duplicate publication name %q", pubPath, pub.Name)
		}
		names[pub.Name] = true

		if pub.AllTables == (len(pub.Tables) > 0) {
			return fmt.Errorf("%s: exactly one of tables or all_tables is required", pubPath)
		}

		tables := make(map[string]bool)
		for j, table := range pub.Tables {
			tablePath := fmt.Sprintf("%s.tables[%d]", pubPath, j)
			if containsNullByte(table) {
				return fmt.Errorf("%s: contains null byte", tablePath)
			}
			if _, err := ParseQualifiedName(table); err != nil {
				return fmt.Errorf("%s: %w", tablePath, err)
			}
			if tables[table] {
				return fmt.Errorf("%s: duplicate table %q", tablePath, table)
			}
			tables[table] = true
		}

		ops := make(map[string]bool)
		for j, op := range pub.Publish {
			normalized := strings.ToLower(strings.TrimSpace(op))
			if !validOps[normalized] {
				return fmt.Errorf("%s.publish[%d]: invalid operation %q (allowed: %s)",
					pubPath, j, op, strings.Join(PublishOperations, ", "))
			}
			if ops[normalized] {
				return fmt.Errorf("%s.publish[%d]: duplicate operation %q", pubPath, j, op)
			}
			ops[normalized] = true
		}
	}
	return nil
}
