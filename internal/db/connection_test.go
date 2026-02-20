package db

import "testing"

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
	want := "postgresql://user:p@ss:word@db.example.com:5433/test-db?sslmode=require"
	if got != want {
		t.Errorf("DSN() = %q, want %q", got, want)
	}
}
