package provisioner

import (
	"fmt"

	"postgres-provisioner/internal/config"
)

// ValidateMigrations reads every configured migrations directory and checks its files the
// way a run would (naming, empty files, duplicate versions), without touching a database.
// It returns the first problem found.
func ValidateMigrations(cfg *config.Config) error {
	for _, database := range cfg.Databases {
		if database.Migrations == nil || database.Migrations.Directory == "" {
			continue
		}
		if _, err := discoverMigrations(database.Migrations.Directory); err != nil {
			return fmt.Errorf("migrations for database %q: %w", database.Name, err)
		}
	}
	return nil
}
