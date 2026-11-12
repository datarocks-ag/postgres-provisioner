package db

import (
	"errors"
	"strings"
	"testing"
)

func TestDSN(t *testing.T) {
	cfg := ConnConfig{
		User:     "admin",
		Password: "secret",
		Host:     "localhost",
		Port:     "5432",
		DBName:   "mydb",
		SSLMode:  "disable",
	}

	got := cfg.DSN()
	want := "postgresql://admin:secret@localhost:5432/mydb?sslmode=disable"
	if got != want {
		t.Errorf("DSN() = %q, want %q", got, want)
	}
}

func TestDSNSpecialCharacters(t *testing.T) {
	cfg := ConnConfig{
		User:     "user",
		Password: "p@ss:word",
		Host:     "db.example.com",
		Port:     "5433",
		DBName:   "test-db",
		SSLMode:  "require",
	}

	got := cfg.DSN()
	want := "postgresql://user:p%40ss%3Aword@db.example.com:5433/test-db?sslmode=require"
	if got != want {
		t.Errorf("DSN() = %q, want %q", got, want)
	}
}

func TestDSNDatabaseNameWithSlash(t *testing.T) {
	cfg := ConnConfig{
		User:     "user",
		Password: "pass",
		Host:     "localhost",
		Port:     "5432",
		DBName:   "tenant/db",
		SSLMode:  "disable",
	}

	got := cfg.DSN()
	want := "postgresql://user:pass@localhost:5432/tenant%2Fdb?sslmode=disable"
	if got != want {
		t.Errorf("DSN() = %q, want %q", got, want)
	}
}

func TestDSNSessionParams(t *testing.T) {
	cfg := ConnConfig{
		User:     "user",
		Password: "p@ss:word",
		Host:     "localhost",
		Port:     "5432",
		DBName:   "mydb",
		SSLMode:  "disable",
		SessionParams: map[string]string{
			"statement_timeout": "0",
			"lock_timeout":      "0",
		},
	}

	got := cfg.DSN()
	want := "postgresql://user:p%40ss%3Aword@localhost:5432/mydb?options=-c+lock_timeout%3D0+-c+statement_timeout%3D0&sslmode=disable"
	if got != want {
		t.Errorf("DSN() = %q, want %q", got, want)
	}
}

func TestSessionOptionsEscapesWhitespaceAndBackslash(t *testing.T) {
	got := sessionOptions(map[string]string{"application_name": `my app\x`})
	want := `-c application_name=my\ app\\x`
	if got != want {
		t.Errorf("sessionOptions() = %q, want %q", got, want)
	}
}

func TestSafeSessionParams(t *testing.T) {
	params := SafeSessionParams()
	want := map[string]string{
		"statement_timeout":                   "0",
		"lock_timeout":                        "0",
		"idle_in_transaction_session_timeout": "0",
		"default_transaction_read_only":       "off",
	}
	if len(params) != len(want) {
		t.Fatalf("SafeSessionParams() = %v, want %v", params, want)
	}
	for name, value := range want {
		if params[name] != value {
			t.Errorf("SafeSessionParams()[%q] = %q, want %q", name, params[name], value)
		}
	}
	if _, ok := params["search_path"]; ok {
		t.Error("search_path must not be overridden")
	}
}

func TestRedactConnError(t *testing.T) {
	const password = "p@ss:word"

	tests := []struct {
		name        string
		err         error
		password    string
		wantMissing []string // substrings that must NOT appear
		wantContain string   // substring that must appear
	}{
		{
			name:        "url userinfo is masked",
			err:         errors.New(`parse "postgresql://admin:p@ss:word@host:5432/db": invalid port`),
			password:    password,
			wantMissing: []string{"p@ss:word", "admin:"},
			wantContain: "***REDACTED***",
		},
		{
			name:        "raw password value is masked",
			err:         errors.New("dial failed for user with password p@ss:word"),
			password:    password,
			wantMissing: []string{"p@ss:word"},
			wantContain: "***REDACTED***",
		},
		{
			name:        "percent-escaped password is masked",
			err:         errors.New("parse error near p%40ss%3Aword segment"),
			password:    password,
			wantMissing: []string{"p%40ss%3Aword"},
			wantContain: "***REDACTED***",
		},
		{
			// A space escapes to %20 under userinfo rules (as DSN() encodes it),
			// not '+' as url.QueryEscape would produce. A truncated parse error
			// that lost the trailing '@' won't match the URL regex, so the
			// escaped-value fallback must use the same encoding as the DSN.
			name:        "userinfo-escaped password with space is masked",
			err:         errors.New(`parse "postgresql://admin:p%40ss%20w%3Ard`),
			password:    "p@ss w:rd",
			wantMissing: []string{"p%40ss%20w%3Ard"},
			wantContain: "***REDACTED***",
		},
		{
			name:        "benign error is left intact",
			err:         errors.New("connection refused"),
			password:    password,
			wantContain: "connection refused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactConnError(tt.err, tt.password)
			for _, s := range tt.wantMissing {
				if strings.Contains(got, s) {
					t.Errorf("redactConnError() = %q, must not contain %q", got, s)
				}
			}
			if tt.wantContain != "" && !strings.Contains(got, tt.wantContain) {
				t.Errorf("redactConnError() = %q, want to contain %q", got, tt.wantContain)
			}
		})
	}
}
