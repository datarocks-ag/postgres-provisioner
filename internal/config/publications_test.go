package config

import (
	"strings"
	"testing"
)

func TestLoadPublications(t *testing.T) {
	t.Setenv("CDC_SCHEMA", "app")
	cfg, err := Load(writeTempConfig(t, `
databases:
  - name: "db1"
    publications:
      - name: "cdc"
        tables: ["${CDC_SCHEMA}.orders", "billing.invoices"]
        publish: ["Insert", "update"]
      - name: "everything"
        all_tables: true
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pubs := cfg.Databases[0].Publications
	if strings.Join(pubs[0].Tables, ",") != "app.orders,billing.invoices" {
		t.Errorf("unexpected tables: %v", pubs[0].Tables)
	}
	if got := strings.Join(pubs[0].EffectivePublish(), ","); got != "insert,update" {
		t.Errorf("EffectivePublish = %q, want insert,update", got)
	}
	if got := strings.Join(pubs[1].EffectivePublish(), ","); got != "insert,update,delete,truncate" {
		t.Errorf("default EffectivePublish = %q, want all operations", got)
	}
}

func TestValidationPublicationErrors(t *testing.T) {
	tests := []struct {
		name    string
		pubs    string
		wantErr string
	}{
		{"missing name", `[{tables: ["app.t"]}]`, "databases[0].publications[0]: name is required"},
		{"duplicate name", `[{name: cdc, tables: ["app.t"]}, {name: cdc, all_tables: true}]`, `databases[0].publications[1]: duplicate publication name "cdc"`},
		{"neither tables nor all_tables", `[{name: cdc}]`, "exactly one of tables or all_tables is required"},
		{"both tables and all_tables", `[{name: cdc, tables: ["app.t"], all_tables: true}]`, "exactly one of tables or all_tables is required"},
		{"unqualified table", `[{name: cdc, tables: ["orders"]}]`, `databases[0].publications[0].tables[0]: "orders" must be schema-qualified`},
		{"duplicate table", `[{name: cdc, tables: ["app.t", "app.t"]}]`, `duplicate table "app.t"`},
		{"invalid operation", `[{name: cdc, tables: ["app.t"], publish: ["upsert"]}]`, `databases[0].publications[0].publish[0]: invalid operation "upsert"`},
		{"duplicate operation", `[{name: cdc, tables: ["app.t"], publish: ["insert", "INSERT"]}]`, `duplicate operation "INSERT"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeTempConfig(t, `
databases:
  - name: "db1"
    publications: `+tt.pubs+`
`))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestLoadReportsUnresolvedPublicationReference(t *testing.T) {
	_, err := Load(writeTempConfig(t, `
databases:
  - name: "db1"
    publications:
      - name: "cdc"
        tables: ["app.${PUB_TABLE_UNSET}"]
`))
	if err == nil || !strings.Contains(err.Error(), "databases[0].publications[0].tables[0]: environment variable PUB_TABLE_UNSET is not set") {
		t.Fatalf("expected path-qualified unresolved reference error, got: %v", err)
	}
}
