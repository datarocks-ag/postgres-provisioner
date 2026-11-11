package config

import (
	"strings"
	"testing"
)

func TestParseQualifiedName(t *testing.T) {
	valid := map[string]QualifiedName{
		"app.users": {Schema: "app", Name: "users"},
		"App.Users": {Schema: "App", Name: "Users"},
		"a b.c-d":   {Schema: "a b", Name: "c-d"},
	}
	for in, want := range valid {
		got, err := ParseQualifiedName(in)
		if err != nil || got != want {
			t.Errorf("ParseQualifiedName(%q) = (%+v, %v), want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"users", "a.b.c", ".users", "app.", ""} {
		if _, err := ParseQualifiedName(in); err == nil {
			t.Errorf("ParseQualifiedName(%q): expected error", in)
		}
	}
}

func TestLoadObjectGrant(t *testing.T) {
	t.Setenv("TABLE_SCHEMA", "app")
	path := writeTempConfig(t, `
roles:
  - name: "reporting"
databases:
  - name: "db1"
    grants:
      - role: "reporting"
        privileges: ["SELECT"]
        on_tables: ["${TABLE_SCHEMA}.users", "app.orders"]
        columns: ["id", "email"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	g := cfg.Databases[0].Grants[0]
	if strings.Join(g.OnTables, ",") != "app.users,app.orders" || strings.Join(g.Columns, ",") != "id,email" {
		t.Errorf("unexpected grant: %+v", g)
	}
	if !g.IsObjectGrant() {
		t.Error("expected IsObjectGrant")
	}
}

func TestValidationObjectGrantErrors(t *testing.T) {
	tests := []struct {
		name    string
		grant   string
		wantErr string
	}{
		{"unqualified table", `privileges: ["SELECT"]
        on_tables: ["users"]`, `databases[0].grants[0].on_tables[0]: "users" must be schema-qualified`},
		{"too many dots", `privileges: ["SELECT"]
        on_tables: ["db.app.users"]`, "must be schema-qualified"},
		{"duplicate table", `privileges: ["SELECT"]
        on_tables: ["app.users", "app.users"]`, `databases[0].grants[0].on_tables[1]: duplicate table "app.users"`},
		{"columns without on_tables", `privileges: ["SELECT"]
        on_tables_in_schema: "app"
        columns: ["id"]`, "databases[0].grants[0].columns: only allowed together with on_tables"},
		{"invalid column privilege", `privileges: ["DELETE"]
        on_tables: ["app.users"]
        columns: ["id"]`, `databases[0].grants[0].privileges[0]: privilege "DELETE" is not valid with columns`},
		{"duplicate column", `privileges: ["SELECT"]
        on_tables: ["app.users"]
        columns: ["id", "id"]`, `databases[0].grants[0].columns[1]: duplicate column "id"`},
		{"empty column", `privileges: ["SELECT"]
        on_tables: ["app.users"]
        columns: [""]`, "databases[0].grants[0].columns[0]: must not be empty"},
		{"invalid table privilege", `privileges: ["USAGE"]
        on_tables: ["app.users"]`, `privilege "USAGE" is not valid for on_tables`},
		{"combined with another target", `privileges: ["SELECT"]
        on_tables: ["app.users"]
        on_tables_in_schema: "app"`, "exactly one of"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeTempConfig(t, `
roles:
  - name: "reporting"
databases:
  - name: "db1"
    grants:
      - role: "reporting"
        `+tt.grant+`
`))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}
