package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"postgres-provisioner/internal/config"
	"postgres-provisioner/internal/db"
	"postgres-provisioner/internal/provisioner"
)

var version = "dev"

func main() {
	setupLogging()

	migrationsDefault := boolEnvOrDefault("MIGRATIONS_ENABLED", true)
	dryRunDefault := boolEnvOrDefault("DRY_RUN", false)
	showVersion := flag.Bool("version", false, "print version and exit")
	migrationsEnabled := flag.Bool("migrations", migrationsDefault, "enable/disable migrations (env: MIGRATIONS_ENABLED)")
	dryRun := flag.Bool("dry-run", dryRunDefault, "log mutations as a preview without executing them (env: DRY_RUN)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	slog.Info("Starting postgres-provisioner", "version", version)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	connCfg := db.ConnConfig{
		User:     requireEnv("POSTGRES_USER"),
		Password: requireEnv("POSTGRES_PASSWORD"),
		Host:     envOrDefault("POSTGRES_HOST", "localhost"),
		Port:     envOrDefault("POSTGRES_PORT", "5432"),
		DBName:   envOrDefault("POSTGRES_DB", "postgres"),
		SSLMode:  envOrDefault("POSTGRES_SSLMODE", "disable"),
	}

	configPath := envOrDefault("PGHELPER_CONFIG_PATH", "./config.yaml")

	slog.Info("Loading configuration", "path", configPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}
	slog.Info("Configuration loaded",
		"roles", len(cfg.Roles),
		"databases", len(cfg.Databases),
	)

	slog.Info("Connecting to PostgreSQL",
		"host", connCfg.Host,
		"port", connCfg.Port,
		"database", connCfg.DBName,
	)
	adminDB, err := db.Connect(ctx, connCfg)
	if err != nil {
		slog.Error("Failed to connect to PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer adminDB.Close()

	p := provisioner.New(adminDB, connCfg, cfg, provisioner.Options{
		MigrationsEnabled: *migrationsEnabled,
		DryRun:            *dryRun,
	})
	if err := p.Run(ctx); err != nil {
		slog.Error("Provisioning failed", "error", err)
		os.Exit(1)
	}

	slog.Info("postgres-provisioner finished successfully")
}

func setupLogging() {
	// Install the JSON handler before resolving LOG_LEVEL: resolving it can fail (e.g.
	// both LOG_LEVEL and LOG_LEVEL_FILE set) and that error must be logged as JSON too.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	level := slog.LevelInfo
	switch envOrDefault("LOG_LEVEL", "info") {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}

// resolveEnv returns the value of key, falling back to the file named by key_FILE.
// An empty value is treated as unset and returned as "".
func resolveEnv(key string) (string, error) {
	val, _, err := config.LookupEnvOrFile(key)
	return val, err
}

func requireEnv(key string) string {
	val, err := resolveEnv(key)
	if err != nil {
		slog.Error("Invalid environment variable", "variable", key, "error", err)
		os.Exit(1)
	}
	if val == "" {
		slog.Error("Required environment variable not set", "variable", key, "file_variable", key+"_FILE")
		os.Exit(1)
	}
	return val
}

func envOrDefault(key, defaultVal string) string {
	val, err := resolveEnv(key)
	if err != nil {
		slog.Error("Invalid environment variable", "variable", key, "error", err)
		os.Exit(1)
	}
	if val != "" {
		return val
	}
	return defaultVal
}

// boolEnvOrDefault parses key (or the file named by key_FILE) as a boolean, returning
// defaultVal when it is unset or empty. Invalid values are fatal.
func boolEnvOrDefault(key string, defaultVal bool) bool {
	val, err := parseBoolEnv(key, defaultVal)
	if err != nil {
		slog.Error("Invalid environment variable", "variable", key, "error", err)
		os.Exit(1)
	}
	return val
}

func parseBoolEnv(key string, defaultVal bool) (bool, error) {
	raw, err := resolveEnv(key)
	if err != nil {
		return false, err
	}
	if raw == "" {
		return defaultVal, nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s value %q (expected true/false/1/0)", key, raw)
	}
	return parsed, nil
}
