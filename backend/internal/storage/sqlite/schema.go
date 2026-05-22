package sqlite

// migrations is the ordered list of schema migrations. Each entry runs once,
// tracked in the schema_migrations table. Never edit or delete a past entry —
// only append.
var migrations = []string{
	// v1: initial schema for v2 redesign
	`
	CREATE TABLE IF NOT EXISTS servers (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		token_hash  TEXT,
		os          TEXT,
		version     TEXT,
		tags        TEXT,
		last_seen   INTEGER,
		created_at  INTEGER NOT NULL
	);

	-- No PK on (server_id, type, ts): bursts within the same millisecond
	-- would otherwise collide and silently drop samples. SQLite gives us
	-- an implicit rowid for free; query paths use the composite index.
	CREATE TABLE IF NOT EXISTS metrics (
		server_id   TEXT NOT NULL,
		type        TEXT NOT NULL,
		ts          INTEGER NOT NULL,
		value       REAL NOT NULL
	);
	CREATE INDEX IF NOT EXISTS metrics_server_type_ts ON metrics(server_id, type, ts);
	CREATE INDEX IF NOT EXISTS metrics_server_ts ON metrics(server_id, ts);

	CREATE TABLE IF NOT EXISTS alerts_history (
		id         TEXT PRIMARY KEY,
		server_id  TEXT,
		rule_id    TEXT,
		rule_name  TEXT,
		metric     TEXT,
		value      REAL,
		threshold  REAL,
		severity   TEXT,
		ts         INTEGER NOT NULL,
		status     TEXT NOT NULL,
		diagnosis  TEXT
	);
	CREATE INDEX IF NOT EXISTS alerts_history_ts ON alerts_history(ts);

	CREATE TABLE IF NOT EXISTS terminal_sessions (
		id            TEXT PRIMARY KEY,
		server_id     TEXT NOT NULL,
		user          TEXT NOT NULL,
		started_at    INTEGER NOT NULL,
		ended_at      INTEGER,
		input_bytes   INTEGER DEFAULT 0,
		output_bytes  INTEGER DEFAULT 0,
		transcript    TEXT
	);

	CREATE TABLE IF NOT EXISTS ai_conversations (
		id          TEXT PRIMARY KEY,
		user        TEXT NOT NULL,
		started_at  INTEGER NOT NULL,
		updated_at  INTEGER NOT NULL,
		model       TEXT,
		title       TEXT,
		messages    TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS ai_conversations_user_updated ON ai_conversations(user, updated_at);
	`,
}
