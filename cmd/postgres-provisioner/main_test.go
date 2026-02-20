package main

import (
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
