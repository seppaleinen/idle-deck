package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type nowFunc func() time.Time

func defaultNow() time.Time { return time.Now() }

const DriverName = "sqlite"

func dsn(path string) string {
	return path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode=WAL"
}

const SchemaVersion = 11

const v1Schema = `
CREATE TABLE IF NOT EXISTS repositories (
	id               TEXT PRIMARY KEY,
	url              TEXT NOT NULL,
	name             TEXT NOT NULL,
	tracker          TEXT NOT NULL,
	allowlisted      INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS tasks (
	id               TEXT PRIMARY KEY,
	repository_id    TEXT NOT NULL,
	ticket_tracker   TEXT NOT NULL,
	ticket_repo_id   TEXT NOT NULL,
	ticket_external_id  TEXT NOT NULL,
	ticket_url       TEXT NOT NULL,
	tier             TEXT NOT NULL,
	prompt           TEXT NOT NULL,
	payload          TEXT NOT NULL DEFAULT '',
	timeout_seconds  INTEGER NOT NULL DEFAULT 0,
	budget           INTEGER NOT NULL DEFAULT 0,
	state            TEXT NOT NULL DEFAULT 'queued',
	event_type       TEXT NOT NULL,
	dedupe_key       TEXT NOT NULL,
	received_at      INTEGER NOT NULL DEFAULT 0,
	created_at       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS attempts (
	id               TEXT PRIMARY KEY,
	task_id          TEXT NOT NULL REFERENCES tasks(id),
	ordinal          INTEGER NOT NULL,
	outcome          TEXT NOT NULL DEFAULT '',
	started_at       INTEGER,
	finished_at      INTEGER,
	timeout_seconds  INTEGER NOT NULL DEFAULT 0,
	remote_session_id TEXT,
	CONSTRAINT unique_task_ordinal UNIQUE (task_id, ordinal)
);

CREATE TABLE IF NOT EXISTS artifacts (
	id               TEXT PRIMARY KEY,
	attempt_id       TEXT NOT NULL REFERENCES attempts(id),
	kind             TEXT NOT NULL,
	branch_purpose   TEXT,
	uri              TEXT NOT NULL,
	produced_at      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_tasks_dedupe ON tasks (dedupe_key);
CREATE INDEX IF NOT EXISTS idx_tasks_state_tier ON tasks (state, tier);
CREATE INDEX IF NOT EXISTS idx_attempts_task ON attempts (task_id);
CREATE INDEX IF NOT EXISTS idx_artifacts_attempt ON artifacts (attempt_id);
`

const v2AddTaskTriggerBy = `
ALTER TABLE tasks ADD COLUMN triggered_by_event_type TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN triggered_by_dedupe_key TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN triggered_by_received_at INTEGER NOT NULL DEFAULT 0;

UPDATE tasks SET
	triggered_by_event_type = event_type,
	triggered_by_dedupe_key = dedupe_key,
	triggered_by_received_at = received_at;
`

const v3AddTaskLineageAndAttemptArtifacts = `
ALTER TABLE tasks ADD COLUMN derived_from TEXT;
ALTER TABLE attempts ADD COLUMN artifacts TEXT NOT NULL DEFAULT '';
`

const v4AddTrackerWatermark = `
CREATE TABLE IF NOT EXISTS tracker_watermarks (
    repo       TEXT PRIMARY KEY,
    watermark  TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
`

const v5AddTrackerPolls = `
CREATE TABLE IF NOT EXISTS tracker_polls (
    repo       TEXT PRIMARY KEY,
    last_poll  INTEGER NOT NULL
);
`

const v6AddHeartbeat = `
CREATE TABLE IF NOT EXISTS heartbeat (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    beat_at    INTEGER NOT NULL,
    run_lock   INTEGER NOT NULL DEFAULT 0
);
`

const v7AddDashboardActions = `
CREATE TABLE IF NOT EXISTS dashboard_actions (
	id            TEXT PRIMARY KEY,
	kind          TEXT NOT NULL,
	payload       TEXT NOT NULL DEFAULT '{}',
	status        TEXT NOT NULL DEFAULT 'pending',
	created_at    INTEGER NOT NULL DEFAULT 0,
	started_at    INTEGER,
	finished_at   INTEGER,
	error         TEXT
);

CREATE INDEX IF NOT EXISTS idx_dashboard_actions_status ON dashboard_actions (status);
`

const v8AddWatermarkETag = `
ALTER TABLE tracker_watermarks ADD COLUMN etag TEXT;
`

// v10AddDashboardVisits persists the user's "last seen" marker for the
// review-inbox (issue #36). The marker is server-side: one row, survives
// browser clears, identical across devices. last_seen is Unix nanoseconds;
// row id=1 is the single marker; 0 = never seen.
const v10AddDashboardVisits = `
CREATE TABLE IF NOT EXISTS dashboard_visits (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    last_seen INTEGER NOT NULL DEFAULT 0
);
`

// v11AddRepositoryURL adds repository_url to tasks for the tracker to supply
// a clone URL to the worker (closes #27).
const v11AddRepositoryURL = `
ALTER TABLE tasks ADD COLUMN repository_url TEXT;
`

// v9AddUniqueDedupeKey enforces the idempotency invariant: Enqueue is
// idempotent per dedupe_key, so two tasks with the same key must not coexist.
// SQLite cannot ALTER TABLE ADD CONSTRAINT, so the tasks table is rebuilt:
// copy existing rows (INSERT OR IGNORE keeps the first occurrence of each
// key) into a new table carrying the UNIQUE constraint, then swap.
//
// Column list is explicit (not SELECT *) so the migration is robust against
// a truncated pre-v1 schema (e.g. test fixtures that omit unused columns).
const v9AddUniqueDedupeKey = `
CREATE TABLE tasks_new (
	id               TEXT PRIMARY KEY,
	repository_id    TEXT NOT NULL,
	ticket_tracker   TEXT NOT NULL,
	ticket_repo_id   TEXT NOT NULL,
	ticket_external_id  TEXT NOT NULL,
	ticket_url       TEXT NOT NULL,
	tier             TEXT NOT NULL,
	prompt           TEXT NOT NULL,
	payload          TEXT NOT NULL DEFAULT '',
	timeout_seconds  INTEGER NOT NULL DEFAULT 0,
	budget           INTEGER NOT NULL DEFAULT 0,
	state            TEXT NOT NULL DEFAULT 'queued',
	event_type       TEXT NOT NULL,
	dedupe_key       TEXT NOT NULL,
	received_at      INTEGER NOT NULL DEFAULT 0,
	created_at       INTEGER NOT NULL DEFAULT 0,
	triggered_by_event_type TEXT NOT NULL DEFAULT '',
	triggered_by_dedupe_key TEXT NOT NULL DEFAULT '',
	triggered_by_received_at INTEGER NOT NULL DEFAULT 0,
	derived_from     TEXT,
	CONSTRAINT unique_dedupe_key UNIQUE (dedupe_key)
);
INSERT OR IGNORE INTO tasks_new (id, repository_id, ticket_tracker, ticket_repo_id,
	ticket_external_id, ticket_url, tier, prompt, payload, timeout_seconds,
	budget, state, event_type, dedupe_key, received_at, created_at,
	triggered_by_event_type, triggered_by_dedupe_key, triggered_by_received_at,
	derived_from)
SELECT id, repository_id,
	COALESCE(ticket_tracker, ''), COALESCE(ticket_repo_id, ''),
	COALESCE(ticket_external_id, ''), COALESCE(ticket_url, ''),
	tier, prompt, COALESCE(payload, ''),
	COALESCE(timeout_seconds, 0), COALESCE(budget, 0),
	COALESCE(state, 'queued'), COALESCE(event_type, ''),
	dedupe_key, COALESCE(received_at, 0), COALESCE(created_at, 0),
	COALESCE(triggered_by_event_type, ''), COALESCE(triggered_by_dedupe_key, ''),
	COALESCE(triggered_by_received_at, 0),
	derived_from
FROM tasks;
DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;
CREATE INDEX IF NOT EXISTS idx_tasks_dedupe ON tasks (dedupe_key);
CREATE INDEX IF NOT EXISTS idx_tasks_state_tier ON tasks (state, tier);
`

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{version: 1, sql: v1Schema},
	{version: 2, sql: v2AddTaskTriggerBy},
	{version: 3, sql: v3AddTaskLineageAndAttemptArtifacts},
	{version: 4, sql: v4AddTrackerWatermark},
	{version: 5, sql: v5AddTrackerPolls},
	{version: 6, sql: v6AddHeartbeat},
	{version: 7, sql: v7AddDashboardActions},
	{version: 8, sql: v8AddWatermarkETag},
	{version: 9, sql: v9AddUniqueDedupeKey},
	{version: 10, sql: v10AddDashboardVisits},
	{version: 11, sql: v11AddRepositoryURL},
}

// CurrentVersion returns the schema version recorded in the database.
// Returns 0 if no migrations have been recorded yet.
func CurrentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: read schema_version: %w", err)
	}
	return v, nil
}

func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open(DriverName, dsn(path))
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: schema_version: %w", err)
	}
	var current int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("store: read schema_version: %w", err)
	}
	if current >= SchemaVersion {
		return nil
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("store: migration %d: %w", m.version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, m.version); err != nil {
			return fmt.Errorf("store: record migration %d: %w", m.version, err)
		}
	}
	return nil
}
