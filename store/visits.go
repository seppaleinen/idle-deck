package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DashboardVisitStore persists the user's "last seen" marker for the
// review-inbox (issue #36). The marker is server-side: one row, survives
// browser clears, identical across devices.
type DashboardVisitStore interface {
	// GetLastSeen returns the last time the user marked the inbox as read.
	// Returns the zero time if never seen.
	GetLastSeen(ctx context.Context) (time.Time, error)
	// UpdateLastSeen upserts the single marker row to t.
	UpdateLastSeen(ctx context.Context, t time.Time) error
}

// SQLiteDashboardVisits implements DashboardVisitStore on the same SQLite
// database as the queue. The constructor takes *sql.DB (unexported now func()
// field) so tests can pin the clock — the same pattern as SQLiteWatermarks
// and SQLiteHeartbeat in store/queue.go.
type SQLiteDashboardVisits struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteDashboardVisits(db *sql.DB) *SQLiteDashboardVisits {
	return &SQLiteDashboardVisits{db: db, now: time.Now}
}

func (v *SQLiteDashboardVisits) GetLastSeen(ctx context.Context) (time.Time, error) {
	var n int64
	err := v.db.QueryRowContext(ctx, `SELECT last_seen FROM dashboard_visits WHERE id = 1`).Scan(&n)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("dashboard visits: get last seen: %w", err)
	}
	return time.Unix(0, n), nil
}

func (v *SQLiteDashboardVisits) UpdateLastSeen(ctx context.Context, t time.Time) error {
	_, err := v.db.ExecContext(ctx, `
INSERT INTO dashboard_visits (id, last_seen) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET last_seen = excluded.last_seen`, t.UnixNano())
	if err != nil {
		return fmt.Errorf("dashboard visits: update last seen: %w", err)
	}
	return nil
}