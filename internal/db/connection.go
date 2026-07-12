package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"regexp"
	"strings"
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

// DSN returns the PostgreSQL connection string. User, password, and database
// name are percent-encoded via net/url so credentials containing URL-special
// characters (e.g. '@' or ':') don't corrupt the parsed host/port.
func (c ConnConfig) DSN() string {
	u := url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(c.User, c.Password),
		Host:   net.JoinHostPort(c.Host, c.Port),
		Path:   "/" + c.DBName,
	}
	q := url.Values{}
	q.Set("sslmode", c.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

// connURLUserinfoPattern matches the userinfo section of an embedded
// postgres/postgresql connection URL so it can be masked before logging.
var connURLUserinfoPattern = regexp.MustCompile(`(postgres(?:ql)?://)[^@\s"]*@`)

// redactConnError returns err's message with any embedded credentials masked,
// making it safe to log. Connection errors from the driver can echo the DSN
// (including the plaintext password), which otherwise leaks into journals and
// log shippers. It masks both the userinfo portion of any embedded connection
// URL and the raw/escaped password value.
func redactConnError(err error, password string) string {
	msg := connURLUserinfoPattern.ReplaceAllString(err.Error(), "${1}***REDACTED***@")
	if password != "" {
		msg = strings.ReplaceAll(msg, password, "***REDACTED***")
		if esc := url.QueryEscape(password); esc != password {
			msg = strings.ReplaceAll(msg, esc, "***REDACTED***")
		}
	}
	return msg
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
				"error", redactConnError(err, cfg.Password),
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
