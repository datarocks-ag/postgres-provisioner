package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"time"

	_ "github.com/lib/pq"
)

// ConnConfig holds PostgreSQL connection parameters.
type ConnConfig struct {
	User     string
	Password string
	Host     string
	Port     string
	DBName   string
	SSLMode  string
}

// DSN returns the PostgreSQL connection string.
func (c ConnConfig) DSN() string {
	return fmt.Sprintf(
		"postgresql://%s:%s@%s:%s/%s?sslmode=%s",
		c.User, c.Password, c.Host, c.Port, c.DBName, c.SSLMode,
	)
}

// Connect opens a connection to PostgreSQL with exponential backoff retry.
// It retries up to maxRetries times with an initial delay of 1s, capped at 30s,
// and an overall timeout of 5 minutes.
func Connect(ctx context.Context, cfg ConnConfig) (*sql.DB, error) {
	const (
		maxRetries   = 15
		initialDelay = 1 * time.Second
		maxDelay     = 30 * time.Second
		totalTimeout = 5 * time.Minute
	)

	ctx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()

	dsn := cfg.DSN()

	for attempt := 0; attempt <= maxRetries; attempt++ {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return nil, fmt.Errorf("sql.Open: %w", err)
		}

		if err := db.PingContext(ctx); err != nil {
			db.Close()

			if ctx.Err() != nil {
				return nil, fmt.Errorf("connection timeout after %s: %w", totalTimeout, ctx.Err())
			}

			delay := time.Duration(float64(initialDelay) * math.Pow(2, float64(attempt)))
			if delay > maxDelay {
				delay = maxDelay
			}

			slog.Warn("PostgreSQL not ready, retrying",
				"attempt", attempt+1,
				"max_retries", maxRetries,
				"delay", delay,
				"error", err,
			)

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, fmt.Errorf("connection timeout: %w", ctx.Err())
			}
			continue
		}

		slog.Info("Connected to PostgreSQL",
			"host", cfg.Host,
			"port", cfg.Port,
			"database", cfg.DBName,
		)
		return db, nil
	}

	return nil, fmt.Errorf("failed to connect after %d retries", maxRetries+1)
}

// ConnectToDatabase creates a new connection to a specific database,
// reusing the same credentials from the base config.
func ConnectToDatabase(ctx context.Context, baseCfg ConnConfig, dbName string) (*sql.DB, error) {
	cfg := baseCfg
	cfg.DBName = dbName
	return Connect(ctx, cfg)
}
