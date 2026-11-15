package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvOrDefault(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		defaultVal string
		envVal     string
		setEnv     bool
		want       string
	}{
		{"uses default when unset", "UNSET_VAR_12345", "fallback", "", false, "fallback"},
		{"uses env when set", "TEST_ENV_OR_DEFAULT", "fallback", "custom", true, "custom"},
		{"uses default for empty string", "TEST_ENV_EMPTY", "fallback", "", true, "fallback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv(tt.key, tt.envVal)
			}
			got := envOrDefault(tt.key, tt.defaultVal)
			if got != tt.want {
				t.Errorf("envOrDefault(%q, %q) = %q, want %q", tt.key, tt.defaultVal, got, tt.want)
			}
		})
	}
}

func TestSetupLogging(t *testing.T) {
	levels := []string{"debug", "info", "warn", "error", ""}
	for _, level := range levels {
		t.Run("level_"+level, func(t *testing.T) {
			if level != "" {
				t.Setenv("LOG_LEVEL", level)
			}
			// Should not panic
			setupLogging()
		})
	}
}

func TestResolveEnv(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(secretPath, []byte("file-pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("plain variable", func(t *testing.T) {
		t.Setenv("RESOLVE_PLAIN", "value")
		got, err := resolveEnv("RESOLVE_PLAIN")
		if err != nil || got != "value" {
			t.Errorf("got (%q, %v), want (\"value\", nil)", got, err)
		}
	})
	t.Run("file variable", func(t *testing.T) {
		t.Setenv("RESOLVE_FILE_FILE", secretPath)
		got, err := resolveEnv("RESOLVE_FILE")
		if err != nil || got != "file-pass" {
			t.Errorf("got (%q, %v), want (\"file-pass\", nil)", got, err)
		}
	})
	t.Run("unset", func(t *testing.T) {
		got, err := resolveEnv("RESOLVE_UNSET_12345")
		if err != nil || got != "" {
			t.Errorf("got (%q, %v), want (\"\", nil)", got, err)
		}
	})
	t.Run("both set", func(t *testing.T) {
		t.Setenv("RESOLVE_BOTH", "value")
		t.Setenv("RESOLVE_BOTH_FILE", secretPath)
		if _, err := resolveEnv("RESOLVE_BOTH"); err == nil {
			t.Error("expected error when both variable and file are set")
		}
	})
	t.Run("empty file is treated as unset", func(t *testing.T) {
		emptyPath := filepath.Join(t.TempDir(), "empty")
		if err := os.WriteFile(emptyPath, []byte("\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("RESOLVE_EMPTY_FILE", emptyPath)
		got, err := resolveEnv("RESOLVE_EMPTY")
		if err != nil || got != "" {
			t.Errorf("got (%q, %v), want (\"\", nil) so requireEnv reports it as not set", got, err)
		}
	})
	t.Run("envOrDefault uses file", func(t *testing.T) {
		t.Setenv("RESOLVE_DEFAULT_FILE", secretPath)
		if got := envOrDefault("RESOLVE_DEFAULT", "fallback"); got != "file-pass" {
			t.Errorf("envOrDefault = %q, want \"file-pass\"", got)
		}
	})
}

func TestParseBoolEnv(t *testing.T) {
	writeFile := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "value")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	for _, key := range []string{"DRY_RUN", "MIGRATIONS_ENABLED"} {
		t.Run(key+" unset uses default", func(t *testing.T) {
			t.Setenv(key, "")
			got, err := parseBoolEnv(key, true)
			if err != nil || !got {
				t.Errorf("got (%v, %v), want (true, nil)", got, err)
			}
		})
		t.Run(key+" from variable", func(t *testing.T) {
			t.Setenv(key, "false")
			got, err := parseBoolEnv(key, true)
			if err != nil || got {
				t.Errorf("got (%v, %v), want (false, nil)", got, err)
			}
		})
		t.Run(key+" from file", func(t *testing.T) {
			t.Setenv(key+"_FILE", writeFile(t, "true\n"))
			got, err := parseBoolEnv(key, false)
			if err != nil || !got {
				t.Errorf("got (%v, %v), want (true, nil)", got, err)
			}
		})
		t.Run(key+" both set", func(t *testing.T) {
			t.Setenv(key, "true")
			t.Setenv(key+"_FILE", writeFile(t, "true"))
			if _, err := parseBoolEnv(key, false); err == nil {
				t.Error("expected error when both variable and file are set")
			}
		})
		t.Run(key+" invalid value", func(t *testing.T) {
			t.Setenv(key, "maybe")
			if _, err := parseBoolEnv(key, false); err == nil {
				t.Error("expected error for non-boolean value")
			}
		})
	}
}

func writeValidateConfig(t *testing.T, migrationsDir string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := "roles:\n  - name: shop_app\ndatabases:\n  - name: shop\n    owner: shop_app\n" +
		"    migrations:\n      directory: " + migrationsDir + "\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateConfigAcceptsValidConfigAndMigrations(t *testing.T) {
	migrations := t.TempDir()
	if err := os.WriteFile(filepath.Join(migrations, "V1__init.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateConfig(writeValidateConfig(t, migrations), true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConfigReportsMissingMigrationsDirectory(t *testing.T) {
	if err := validateConfig(writeValidateConfig(t, "/does/not/exist"), true); err == nil {
		t.Fatal("expected an error for a missing migrations directory")
	}
}

func TestValidateConfigSkipsMigrationsWhenDisabled(t *testing.T) {
	if err := validateConfig(writeValidateConfig(t, "/does/not/exist"), false); err != nil {
		t.Fatalf("migrations disabled: unexpected error: %v", err)
	}
}

func TestValidateConfigReportsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("databases:\n  - name: shop\n    owner: nobody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateConfig(path, true); err == nil {
		t.Fatal("expected an error for an undeclared owner")
	}
}
