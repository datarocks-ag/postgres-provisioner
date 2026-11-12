package config

import (
	"strings"
	"testing"
)

func TestLoadSettings(t *testing.T) {
	t.Setenv("TIMEOUT", "30s")
	path := writeTempConfig(t, `
roles:
  - name: "app"
    settings:
      statement_timeout: "${TIMEOUT}"
      work_mem: 65536
      jit: false
      search_path: ["app", "$user", "public"]
      lock_timeout: null
      idle_in_transaction_session_timeout: NULL
      app.empty: ""
databases:
  - name: "db1"
    settings:
      TimeZone: "UTC"
    role_settings:
      app:
        statement_timeout: "5min"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := cfg.Roles[0].Settings
	checks := map[string]string{
		"statement_timeout": "30s",
		"work_mem":          "65536",
		"jit":               "false",
		"search_path":       "app|$user|public",
	}
	for name, want := range checks {
		if got := strings.Join(s[name].Values, "|"); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if !s["lock_timeout"].IsReset() || !s["idle_in_transaction_session_timeout"].IsReset() {
		t.Error("expected explicit null to be a reset")
	}
	if got := s["app.empty"].Values; len(got) != 1 || got[0] != "" {
		t.Errorf("expected quoted empty string to be a value, got %v", got)
	}
	if s["statement_timeout"].IsReset() {
		t.Error("expected statement_timeout not to be a reset")
	}
	if got := cfg.Databases[0].RoleSettings["app"]["statement_timeout"].Values; len(got) != 1 || got[0] != "5min" {
		t.Errorf("unexpected role_settings value: %v", got)
	}
}

func TestValidationSettingsErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "injection in name",
			yaml: `
roles:
  - name: "app"
    settings:
      "work_mem = 1; DROP ROLE x; --": "1"`,
			wantErr: "roles[0].settings: invalid setting name",
		},
		{
			name: "space in name",
			yaml: `
roles:
  - name: "app"
    settings:
      "work mem": "1"`,
			wantErr: "invalid setting name",
		},
		{
			name: "case-insensitive duplicate",
			yaml: `
databases:
  - name: "db1"
    settings:
      TimeZone: "UTC"
      timezone: "UTC"`,
			wantErr: "databases[0].settings: settings",
		},
		{
			name: "empty list",
			yaml: `
roles:
  - name: "app"
    settings:
      search_path: []`,
			wantErr: "roles[0].settings.search_path: list must not be empty",
		},
		{
			name: "mapping value",
			yaml: `
roles:
  - name: "app"
    settings:
      work_mem: { a: 1 }`,
			wantErr: "setting value must be a scalar, a list or null",
		},
		{
			name: "empty value is not a reset",
			yaml: `
roles:
  - name: "app"
    settings:
      work_mem:
      statement_timeout: "30s"`,
			wantErr: "roles[0].settings.work_mem: value is empty (use null to reset the setting)",
		},
		{
			name: "tilde is not a reset",
			yaml: `
databases:
  - name: "db1"
    settings:
      work_mem: ~`,
			wantErr: "databases[0].settings.work_mem: value is empty",
		},
		{
			name: "empty value in role_settings",
			yaml: `
roles:
  - name: "app"
databases:
  - name: "db1"
    role_settings:
      app:
        work_mem:`,
			wantErr: "databases[0].role_settings.app.work_mem: value is empty",
		},
		{
			name: "null list element",
			yaml: `
roles:
  - name: "app"
    settings:
      search_path: [app, ~]`,
			wantErr: "roles[0].settings.search_path: list elements must not be null or empty",
		},
		{
			name: "explicit null list element",
			yaml: `
roles:
  - name: "app"
    settings:
      search_path: [app, null]`,
			wantErr: "roles[0].settings.search_path: list elements must not be null or empty",
		},
		{
			name: "duplicate setting",
			yaml: `
roles:
  - name: "app"
    settings:
      work_mem: "1MB"
      work_mem: "2MB"`,
			wantErr: `setting "work_mem" is defined more than once`,
		},
		{
			name: "settings not a mapping",
			yaml: `
roles:
  - name: "app"
    settings: [work_mem]`,
			wantErr: "settings must be a mapping",
		},
		{
			name: "undeclared role in role_settings",
			yaml: `
databases:
  - name: "db1"
    role_settings:
      ghost:
        work_mem: "1MB"`,
			wantErr: `databases[0].role_settings: role "ghost" is not declared in roles`,
		},
		{
			name: "custom parameter with valid prefix is accepted",
			yaml: `
roles:
  - name: "app"
    settings:
      "app.tenant": "acme"
      "bad..name": "x"`,
			wantErr: `invalid setting name "bad..name"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeTempConfig(t, tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestLoadReportsUnresolvedSettingReference(t *testing.T) {
	path := writeTempConfig(t, `
roles:
  - name: "app"
    settings:
      statement_timeout: "${SETTING_TIMEOUT_UNSET}"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "roles[0].settings.statement_timeout[0]: environment variable SETTING_TIMEOUT_UNSET is not set") {
		t.Fatalf("expected path-qualified unresolved reference error, got: %v", err)
	}
}
