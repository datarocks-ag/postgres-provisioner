package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSecretFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExpandResolvesReferences(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		files map[string]string // variable name -> file content, exposed via NAME_FILE
		input string
		want  string
	}{
		{name: "no references", input: "plain text", want: "plain text"},
		{name: "set variable", env: map[string]string{"EXP_A": "hello"}, input: "${EXP_A}", want: "hello"},
		{name: "multiple references", env: map[string]string{"EXP_A": "hello", "EXP_B": "world"}, input: "${EXP_A}_${EXP_B}", want: "hello_world"},
		{name: "set but empty", env: map[string]string{"EXP_A": ""}, input: "x${EXP_A}x", want: "xx"},
		{name: "default when unset", input: "${EXP_UNSET:-fallback}", want: "fallback"},
		{name: "empty default when unset", input: "${EXP_UNSET:-}", want: ""},
		{name: "variable wins over default", env: map[string]string{"EXP_A": "set"}, input: "${EXP_A:-fallback}", want: "set"},
		{name: "default when empty", env: map[string]string{"EXP_A": ""}, input: "${EXP_A:-fallback}", want: "fallback"},
		{name: "empty default when empty", env: map[string]string{"EXP_A": ""}, input: "${EXP_A:-}", want: ""},
		{name: "default when file is empty", files: map[string]string{"EXP_F": "\n"}, input: "${EXP_F:-fallback}", want: "fallback"},
		{name: "file with trailing LF", files: map[string]string{"EXP_F": "from-file\n"}, input: "${EXP_F}", want: "from-file"},
		{name: "file with trailing CRLF", files: map[string]string{"EXP_F": "from-file\r\n"}, input: "${EXP_F}", want: "from-file"},
		{name: "file without newline", files: map[string]string{"EXP_F": "from-file"}, input: "${EXP_F}", want: "from-file"},
		{name: "file wins over default", files: map[string]string{"EXP_F": "from-file"}, input: "${EXP_F:-fallback}", want: "from-file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			for k, content := range tt.files {
				t.Setenv(k+"_FILE", writeSecretFile(t, content))
			}
			e := &expander{}
			got := e.expand("field", tt.input)
			if err := e.err(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("expand(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExpandReportsErrors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T)
		input   string
		wantErr []string
	}{
		{
			name:    "unset variable",
			input:   "${EXP_MISSING}",
			wantErr: []string{"field", "EXP_MISSING is not set", "EXP_MISSING_FILE"},
		},
		{
			name: "variable and file both set",
			setup: func(t *testing.T) {
				t.Setenv("EXP_BOTH", "a")
				t.Setenv("EXP_BOTH_FILE", writeSecretFile(t, "b"))
			},
			input:   "${EXP_BOTH}",
			wantErr: []string{"field", "both EXP_BOTH and EXP_BOTH_FILE are set"},
		},
		{
			name: "unreadable file",
			setup: func(t *testing.T) {
				t.Setenv("EXP_NOFILE_FILE", filepath.Join(t.TempDir(), "does-not-exist"))
			},
			input:   "${EXP_NOFILE}",
			wantErr: []string{"field", "reading EXP_NOFILE_FILE", "does-not-exist"},
		},
		{
			name: "file too large",
			setup: func(t *testing.T) {
				t.Setenv("EXP_BIG_FILE", writeSecretFile(t, strings.Repeat("x", maxSecretFileSize+1)))
			},
			input:   "${EXP_BIG}",
			wantErr: []string{"field", "exceeds"},
		},
		{name: "invalid name", input: "${foo bar}", wantErr: []string{"field", "invalid environment variable reference"}},
		{name: "empty name", input: "${}", wantErr: []string{"invalid environment variable reference"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(t)
			}
			e := &expander{}
			e.expand("field", tt.input)
			err := e.err()
			if err == nil {
				t.Fatal("expected error")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("expected error to contain %q, got: %v", want, err)
				}
			}
		})
	}
}

func TestExpandRejectsNestedReferences(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		input string
	}{
		{name: "unset outer, nested default", env: map[string]string{"NEST_B": "bval"}, input: "x${NEST_UNSET:-${NEST_B}}y"},
		{name: "set outer, nested default", env: map[string]string{"NEST_A": "aval", "NEST_B": "bval"}, input: "x${NEST_A:-${NEST_B}}y"},
		{name: "deeper nesting", env: map[string]string{"NEST_A": "aval"}, input: "x${NEST_A:-${NEST_B:-${NEST_C}}}y"},
		{name: "unbalanced nesting", env: map[string]string{"NEST_A": "aval"}, input: "x${NEST_A:-${NEST_B}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			e := &expander{}
			got := e.expand("roles[0].name", tt.input)
			err := e.err()
			if err == nil || !strings.Contains(err.Error(), "roles[0].name: nested references are not supported") {
				t.Fatalf("expected nested-reference error, got: %v", err)
			}
			if strings.ContainsAny(got, "{}") || strings.Contains(got, "val") {
				t.Errorf("expand(%q) = %q, want no remnants of the nested reference", tt.input, got)
			}
		})
	}
}

func TestLoadReportsAllUnresolvedReferences(t *testing.T) {
	path := writeTempConfig(t, `
roles:
  - name: "app_user"
    password: "${LOAD_MISSING_PASSWORD}"
databases:
  - name: "${LOAD_MISSING_DB}"
    owner: "app_user"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"roles[0].password", "LOAD_MISSING_PASSWORD", "databases[0].name", "LOAD_MISSING_DB"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to mention %q, got: %v", want, err)
		}
	}
}

func TestLoadRejectsPasswordResolvingToEmpty(t *testing.T) {
	tests := []struct {
		name     string
		password string
		setup    func(t *testing.T)
	}{
		{"empty variable", "${EMPTY_PW}", func(t *testing.T) { t.Setenv("EMPTY_PW", "") }},
		{"empty default", "${EMPTY_PW_UNSET:-}", func(*testing.T) {}},
		{"empty file", "${EMPTY_PW_F}", func(t *testing.T) { t.Setenv("EMPTY_PW_F_FILE", writeSecretFile(t, "\n")) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			path := writeTempConfig(t, `
roles:
  - name: "app_user"
    password: "`+tt.password+`"
`)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "roles[0].password: environment variable reference resolved to an empty value") {
				t.Fatalf("expected empty-password error, got: %v", err)
			}
		})
	}
}

func TestLoadAllowsLiteralEmptyPassword(t *testing.T) {
	path := writeTempConfig(t, `
roles:
  - name: "nologin_role"
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadPasswordFromFile(t *testing.T) {
	t.Setenv("APP_PW_FILE", writeSecretFile(t, "s3cret-from-file\n"))
	path := writeTempConfig(t, `
roles:
  - name: "app_user"
    password: "${APP_PW}"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Roles[0].Password != "s3cret-from-file" {
		t.Errorf("expected password from file, got %q", cfg.Roles[0].Password)
	}
}

func TestExpansionErrorsDoNotLeakSecrets(t *testing.T) {
	const sentinel = "SENTINEL-SECRET-VALUE-91c4"
	t.Setenv("LEAK_BOTH", sentinel)
	t.Setenv("LEAK_BOTH_FILE", writeSecretFile(t, sentinel))
	t.Setenv("LEAK_BIG_FILE", writeSecretFile(t, sentinel+strings.Repeat("x", maxSecretFileSize)))

	path := writeTempConfig(t, `
roles:
  - name: "a"
    password: "${LEAK_BOTH}"
  - name: "b"
    password: "${LEAK_BIG}"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Errorf("error leaked secret value: %v", err)
	}
}

func TestLookupEnvOrFileUnset(t *testing.T) {
	val, ok, err := LookupEnvOrFile("LOOKUP_DEFINITELY_UNSET")
	if err != nil || ok || val != "" {
		t.Errorf("expected (\"\", false, nil), got (%q, %v, %v)", val, ok, err)
	}
}
