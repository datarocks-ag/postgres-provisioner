package provisioner

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// runLockKey identifies the advisory lock that serializes provisioner runs ("pgprov").
// Advisory locks are scoped to the database the session is connected to, so runs only
// exclude each other when they connect to the same POSTGRES_DB.
const runLockKey int64 = 0x706770726f76

// AcquireRunLock takes a session-level advisory lock on a connection reserved from adminDB,
// so that two provisioner runs against the same server don't interleave their
// check-then-create steps. If another run holds the lock, it logs that it is waiting and
// blocks until the lock is free or ctx is canceled.
//
// The returned release function unlocks and returns the connection; call it when the run
// ends. If the process dies instead, the server releases the lock with the session.
func AcquireRunLock(ctx context.Context, adminDB *sql.DB) (release func(), err error) {
	conn, err := adminDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("reserving a connection for the run lock: %w", err)
	}

	var acquired bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", runLockKey).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("acquiring the run lock: %w", err)
	}
	if !acquired {
		slog.Info("Waiting for another provisioner run to finish")
		if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", runLockKey); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("waiting for the run lock: %w", err)
		}
	}
	slog.Info("Acquired the provisioner run lock")

	return func() {
		// Not ctx: the lock must be released even after the run was canceled.
		if _, err := conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", runLockKey); err != nil {
			slog.Warn("Releasing the run lock failed; it is released when the connection closes", "error", err)
		}
		_ = conn.Close()
	}, nil
}
