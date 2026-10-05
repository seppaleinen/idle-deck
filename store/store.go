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

const SchemaVersion = 4

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

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{version: 1, sql: v1Schema},
	{version: 2, sql: v2AddTaskTriggerBy},
	{version: 3, sql: v3AddTaskLineageAndAttemptArtifacts},
	{version: 4, sql: v4AddTrackerWatermark},
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
