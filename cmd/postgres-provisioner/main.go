package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"postgres-provisioner/internal/config"
	"postgres-provisioner/internal/db"
	"postgres-provisioner/internal/provisioner"
)

func main() {
	setupLogging()

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

	p := provisioner.New(adminDB, connCfg, cfg)
	if err := p.Run(ctx); err != nil {
		slog.Error("Provisioning failed", "error", err)
		os.Exit(1)
	}

	slog.Info("postgres-provisioner finished successfully")
}

func setupLogging() {
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

func requireEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		slog.Error("Required environment variable not set", "variable", key)
		os.Exit(1)
	}
	return val
}

func envOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
