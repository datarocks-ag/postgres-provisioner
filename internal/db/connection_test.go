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
