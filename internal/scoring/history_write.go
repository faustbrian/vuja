package scoring

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
)

const historyBusyTimeout = 5 * time.Second

func shortHistoryWriteBudget(ctx context.Context) bool {
	deadline, ok := ctx.Deadline()
	return ok && time.Until(deadline) < historyBusyTimeout
}

// A Go context does not promptly interrupt SQLite's busy-handler sleep. Pin
// the physical connection while capping that handler to the remaining budget;
// restore its ordinary policy before readers or other writers can borrow it.
func (f *FrecencyStore) budgetedWriteConnection(ctx context.Context) (*sql.Conn, func(), error) {
	connection, err := f.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	changed := false
	release := func() {
		if changed {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			_, err := connection.ExecContext(cleanup, "PRAGMA busy_timeout=5000")
			cancel()
			if err != nil {
				// Never return a connection with an unknown policy to the pool.
				_ = connection.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
		_ = connection.Close()
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			release()
			return nil, nil, context.DeadlineExceeded
		}
		if remaining < historyBusyTimeout {
			// Floor: zero means no busy sleeping for a sub-millisecond budget.
			changed = true
			if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", remaining.Milliseconds())); err != nil {
				release()
				return nil, nil, err
			}
		}
	}
	return connection, release, nil
}

// Callers retain their local gate and transaction ordering. In particular,
// completion can resolve a forgotten/already-completed execution read-only;
// changing the lock-wait budget must not force that no-op to acquire a writer.
// Operations needing writer ownership before reads keep their explicit guard.
func (f *FrecencyStore) beginWriteTx(ctx context.Context) (*sql.Tx, func(), error) {
	if !shortHistoryWriteBudget(ctx) {
		tx, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, nil, err
		}
		return tx, func() { _ = tx.Rollback() }, nil
	}
	connection, release, err := f.budgetedWriteConnection(ctx)
	if err != nil {
		return nil, nil, err
	}
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		release()
		return nil, nil, err
	}
	finish := func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			_ = connection.Raw(func(any) error { return driver.ErrBadConn })
		}
		release()
	}
	return tx, finish, nil
}

func (f *FrecencyStore) execWrite(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if !shortHistoryWriteBudget(ctx) {
		return f.db.ExecContext(ctx, query, args...)
	}
	connection, release, err := f.budgetedWriteConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return connection.ExecContext(ctx, query, args...)
}
