// Package runs records run history: every routine firing in the cloud and
// every interactive session in your editor.
//
// It also keeps routine memories: the notes a routine with `memory: true`
// leaves for its own next runs, on runtimes without a memory feature of
// their own.
//
// History can't be filled in after the fact, so Fabrica records from day
// one. v0.1 has no built-in way to read it: you open the SQLite file with
// any SQLite tool. That makes the schema below a public interface, so later
// versions must migrate it carefully (PRAGMA user_version tracks the
// version) or keep a compatible view.
//
// The remote server keeps its database on a persistent volume. The local
// server uses ~/.fabrica/runs.db, which never leaves your machine.
package runs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite" // pure-Go SQLite, so fabrica stays a static binary
)

// SchemaVersion is the version of the schema this Fabrica writes.
const SchemaVersion = 3

// Schema is the whole run-history schema, at SchemaVersion. It's public:
// see manual/run-history.md.
const Schema = schemaV1 + schemaV2 + schemaV3

// migrations[i] takes a database from schema version i to i+1.
var migrations = []string{schemaV1, schemaV2, schemaV3}

// schemaV1 is the first schema: runs, their checkpoints and skill loads.
const schemaV1 = `
CREATE TABLE runs (
  run_id        TEXT PRIMARY KEY,
  started_at    TEXT NOT NULL,           -- RFC 3339, UTC
  intent        TEXT NOT NULL,           -- the agent's summary of the request, at most 20 words
  routine       TEXT,                    -- NULL for interactive sessions
  runtime       TEXT,
  repo          TEXT,                    -- like github.com/acme/app
  factory       TEXT,
  blueprint_sha TEXT,                    -- the exact blueprint commit served
  stub          TEXT,                    -- the stub fingerprint the run claimed, if any
  served_by     TEXT NOT NULL CHECK (served_by IN ('local', 'remote'))
);

CREATE TABLE checkpoints (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id   TEXT NOT NULL REFERENCES runs(run_id),
  at       TEXT NOT NULL,                -- RFC 3339, UTC
  author   TEXT NOT NULL CHECK (author IN ('agent', 'fabrica')),
  status   TEXT NOT NULL CHECK (status IN ('progress', 'succeeded', 'failed')),
  summary  TEXT NOT NULL,                -- at most 20 words
  links    TEXT NOT NULL DEFAULT '[]'    -- a JSON array of URLs
);
CREATE INDEX checkpoints_by_run ON checkpoints(run_id, at);

CREATE TABLE skill_loads (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id  TEXT NOT NULL REFERENCES runs(run_id),
  at      TEXT NOT NULL,
  skill   TEXT NOT NULL
);
CREATE INDEX skill_loads_by_run ON skill_loads(run_id);

-- One row per run with its latest checkpoint, for quick looks.
CREATE VIEW run_overview AS
SELECT
  r.run_id, r.started_at, r.intent, r.routine, r.runtime, r.repo,
  r.factory, r.blueprint_sha, r.served_by,
  (SELECT c.status  FROM checkpoints c WHERE c.run_id = r.run_id ORDER BY c.at DESC, c.id DESC LIMIT 1) AS last_status,
  (SELECT c.summary FROM checkpoints c WHERE c.run_id = r.run_id ORDER BY c.at DESC, c.id DESC LIMIT 1) AS last_summary,
  (SELECT COUNT(*)  FROM checkpoints c WHERE c.run_id = r.run_id) AS checkpoint_count,
  (SELECT group_concat(skill, ',') FROM (SELECT DISTINCT s.skill FROM skill_loads s WHERE s.run_id = r.run_id ORDER BY s.skill)) AS skills_loaded
FROM runs r;
`

// schemaV2 adds routine memories.
const schemaV2 = `
-- The notes a routine keeps between runs, one per factory, routine and
-- repo. Only routines with memory: true have one, and only on runtimes
-- whose own memory Fabrica stands in for.
CREATE TABLE memories (
  factory    TEXT NOT NULL,
  routine    TEXT NOT NULL,
  repo       TEXT NOT NULL,
  content    TEXT NOT NULL,              -- Markdown, written by the agent
  updated_at TEXT NOT NULL,              -- RFC 3339, UTC
  updated_by TEXT NOT NULL REFERENCES runs(run_id),
  PRIMARY KEY (factory, routine, repo)
);
`

// schemaV3 adds session pointers, loads of rules and routines (not just
// skills), friction notes and a computed outcome for every run.
const schemaV3 = `
ALTER TABLE runs ADD COLUMN session TEXT;  -- where the transcript lives, if known

-- Every skill, rule and routine a run loaded.
CREATE TABLE loads (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id  TEXT NOT NULL REFERENCES runs(run_id),
  at      TEXT NOT NULL,                 -- RFC 3339, UTC
  kind    TEXT NOT NULL CHECK (kind IN ('skill', 'rule', 'routine')),
  name    TEXT NOT NULL
);
CREATE INDEX loads_by_run ON loads(run_id, at);

INSERT INTO loads (run_id, at, kind, name)
  SELECT run_id, at, 'skill', skill FROM skill_loads ORDER BY id;

DROP VIEW run_overview;                  -- it reads skill_loads, so it goes first
DROP TABLE skill_loads;

-- Old queries keep working.
CREATE VIEW skill_loads AS
  SELECT id, run_id, at, name AS skill FROM loads WHERE kind = 'skill';

-- Short notes from agents about what slowed them down.
CREATE TABLE friction (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id  TEXT NOT NULL REFERENCES runs(run_id),
  at      TEXT NOT NULL,                 -- RFC 3339, UTC
  kind    TEXT NOT NULL CHECK (kind IN ('navigation', 'checks', 'standards', 'tools', 'info', 'other')),
  note    TEXT NOT NULL                  -- at most 20 words
);
CREATE INDEX friction_by_run ON friction(run_id);

-- One row per run with its latest checkpoint and what we think happened to
-- it, for quick looks. A run whose latest checkpoint is still progress (or
-- that has none) and that has been quiet for 6 hours is stale.
CREATE VIEW run_overview AS
SELECT
  -- v1 columns, same order as before
  r.run_id, r.started_at, r.intent, r.routine, r.runtime, r.repo,
  r.factory, r.blueprint_sha, r.served_by,
  (SELECT c.status  FROM checkpoints c WHERE c.run_id = r.run_id ORDER BY c.at DESC, c.id DESC LIMIT 1) AS last_status,
  (SELECT c.summary FROM checkpoints c WHERE c.run_id = r.run_id ORDER BY c.at DESC, c.id DESC LIMIT 1) AS last_summary,
  (SELECT COUNT(*)  FROM checkpoints c WHERE c.run_id = r.run_id) AS checkpoint_count,
  (SELECT group_concat(name, ',') FROM (SELECT DISTINCT l.name FROM loads l WHERE l.run_id = r.run_id AND l.kind = 'skill' ORDER BY l.name)) AS skills_loaded,
  -- new in v3
  r.session,
  COALESCE((SELECT MAX(c.at) FROM checkpoints c WHERE c.run_id = r.run_id), r.started_at) AS last_at,
  CASE (SELECT c.status FROM checkpoints c WHERE c.run_id = r.run_id ORDER BY c.at DESC, c.id DESC LIMIT 1)
    WHEN 'succeeded' THEN 'succeeded'
    WHEN 'failed'    THEN 'failed'
    ELSE CASE
      WHEN COALESCE((SELECT MAX(c.at) FROM checkpoints c WHERE c.run_id = r.run_id), r.started_at)
           < strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-6 hours')
      THEN 'stale'
      ELSE 'in_progress'
    END
  END AS outcome,
  (SELECT COUNT(*) FROM friction f WHERE f.run_id = r.run_id) AS friction_count,
  (SELECT json_group_array(DISTINCT l.value)
     FROM checkpoints c, json_each(c.links) l
     WHERE c.run_id = r.run_id
       AND (l.value LIKE '%/pull/%' OR l.value LIKE '%/merge_requests/%')) AS pr_links
FROM runs r;
`

// DB is a run-history database.
type DB struct {
	db *sql.DB
}

// Open opens (and if needed creates) the database at path.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// WAL mode and a busy timeout let several Fabrica processes (say, a
	// few editor sessions at once) share the file safely.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	d := &DB{db: db}
	if err := d.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("run history %s: %w", path, err)
	}
	return d, nil
}

func (d *DB) migrate() error {
	ctx := context.Background()
	conn, err := d.db.Conn(ctx) // BEGIN and COMMIT need to be on the same connection
	if err != nil {
		return err
	}
	defer conn.Close()
	// current is the database's schema version, or an error if it's one this
	// Fabrica can't use.
	current := func() (version int, err error) {
		if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return 0, err
		}
		if version > SchemaVersion {
			return 0, fmt.Errorf("this database uses schema version %d, made by a newer Fabrica; upgrade fabrica to use it", version)
		}
		return version, nil
	}

	version, err := current()
	if err != nil || version == SchemaVersion {
		return err
	}
	// Several Fabrica processes can start at once (a few editor sessions
	// right after an upgrade). Taking the write lock before migrating makes
	// the first one do it while the others wait, and then find nothing left
	// to do.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, "ROLLBACK") // fails harmlessly once committed
	if version, err = current(); err != nil || version == SchemaVersion {
		return err
	}
	for _, step := range migrations[version:] {
		if _, err := conn.ExecContext(ctx, step); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// Served-by values.
const (
	ServedLocal  = "local"
	ServedRemote = "remote"
)

// Run is one run record.
type Run struct {
	ID           string
	StartedAt    time.Time
	Intent       string
	Routine      string
	Runtime      string
	Repo         string
	Factory      string
	BlueprintSHA string
	Stub         string
	ServedBy     string
	// Session says where the run's transcript lives, if the agent said: a
	// file path, a session ID or a session URL.
	Session string
}

// Checkpoint statuses.
const (
	Progress  = "progress"
	Succeeded = "succeeded"
	Failed    = "failed"
)

// Checkpoint authors.
const (
	ByAgent   = "agent"
	ByFabrica = "fabrica"
)

// frictionKinds are the kinds of friction an agent can report.
var frictionKinds = []string{"navigation", "checks", "standards", "tools", "info", "other"}

// Friction is a short note from an agent about something that slowed it
// down.
type Friction struct {
	Kind string // navigation, checks, standards, tools, info or other
	Note string // at most 20 words
}

// Checkpoint is one progress report on a run.
type Checkpoint struct {
	At      time.Time
	Author  string
	Status  string
	Summary string
	Links   []string
	// Friction is written in the same transaction as the checkpoint.
	Friction []Friction
}

// Begin records the start of a run.
func (d *DB) Begin(ctx context.Context, r Run) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO runs (run_id, started_at, intent, routine, runtime, repo, factory, blueprint_sha, stub, served_by, session)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, stamp(r.StartedAt), r.Intent, null(r.Routine), null(r.Runtime), null(r.Repo),
		null(r.Factory), null(r.BlueprintSHA), null(r.Stub), r.ServedBy, null(r.Session))
	return err
}

// AddCheckpoint records a checkpoint and its friction notes, all or none.
func (d *DB) AddCheckpoint(ctx context.Context, runID string, c Checkpoint) error {
	links := c.Links
	if links == nil {
		links = []string{}
	}
	data, _ := json.Marshal(links)
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := stamp(c.At)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO checkpoints (run_id, at, author, status, summary, links) VALUES (?, ?, ?, ?, ?, ?)`,
		runID, at, c.Author, c.Status, c.Summary, string(data)); err != nil {
		return err
	}
	for _, f := range c.Friction {
		if _, err := tx.ExecContext(ctx, `INSERT INTO friction (run_id, at, kind, note) VALUES (?, ?, ?, ?)`, runID, at, f.Kind, f.Note); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Load kinds.
const (
	LoadSkill   = "skill"
	LoadRule    = "rule"
	LoadRoutine = "routine"
)

// Loaded records that a run loaded a skill, rule or routine.
func (d *DB) Loaded(ctx context.Context, runID, kind, name string, at time.Time) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO loads (run_id, at, kind, name) VALUES (?, ?, ?, ?)`, runID, stamp(at), kind, name)
	return err
}

// ErrNoRun means there's no run with that ID.
var ErrNoRun = errors.New("no such run")

// Get reads one run.
func (d *DB) Get(ctx context.Context, runID string) (*Run, error) {
	var r Run
	var started string
	var routine, runtime, repo, factory, sha, stub, session sql.NullString
	err := d.db.QueryRowContext(ctx,
		`SELECT run_id, started_at, intent, routine, runtime, repo, factory, blueprint_sha, stub, served_by, session FROM runs WHERE run_id = ?`, runID).
		Scan(&r.ID, &started, &r.Intent, &routine, &runtime, &repo, &factory, &sha, &stub, &r.ServedBy, &session)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoRun
	}
	if err != nil {
		return nil, err
	}
	r.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	r.Routine, r.Runtime, r.Repo, r.Factory, r.BlueprintSHA, r.Stub, r.Session = routine.String, runtime.String, repo.String, factory.String, sha.String, stub.String, session.String
	return &r, nil
}

// Checkpoints reads a run's checkpoints, oldest first.
func (d *DB) Checkpoints(ctx context.Context, runID string) ([]Checkpoint, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT at, author, status, summary, links FROM checkpoints WHERE run_id = ? ORDER BY at, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		var at, links string
		if err := rows.Scan(&at, &c.Author, &c.Status, &c.Summary, &links); err != nil {
			return nil, err
		}
		c.At, _ = time.Parse(time.RFC3339Nano, at)
		_ = json.Unmarshal([]byte(links), &c.Links)
		out = append(out, c)
	}
	return out, rows.Err()
}

// SkillsLoaded lists the skills a run loaded, in load order.
func (d *DB) SkillsLoaded(ctx context.Context, runID string) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT skill FROM skill_loads WHERE run_id = ? ORDER BY at, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Memory is a routine's notes for its own next runs.
type Memory struct {
	Content   string
	UpdatedAt time.Time
	// UpdatedBy is the run that saved it.
	UpdatedBy string
}

// MaxMemoryBytes is the largest memory Fabrica keeps for one routine. It
// keeps memories to notes, which every run reads in full, rather than logs.
const MaxMemoryBytes = 16 * 1024

// Memory reads a routine's memory for one factory and repo. A routine
// that never saved anything has an empty one.
func (d *DB) Memory(ctx context.Context, factory, routine, repo string) (Memory, error) {
	var m Memory
	var at string
	err := d.db.QueryRowContext(ctx,
		`SELECT content, updated_at, updated_by FROM memories WHERE factory = ? AND routine = ? AND repo = ?`,
		factory, routine, repo).Scan(&m.Content, &at, &m.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, nil
	}
	if err != nil {
		return Memory{}, err
	}
	m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, at)
	return m, nil
}

// SaveMemory replaces a routine's memory for one factory and repo, on
// behalf of a run. Empty content forgets everything.
func (d *DB) SaveMemory(ctx context.Context, factory, routine, repo, content, runID string, at time.Time) error {
	if len(content) > MaxMemoryBytes {
		return fmt.Errorf("the memory is %d bytes, but the limit is %d; keep only what future runs need, and save it again", len(content), MaxMemoryBytes)
	}
	if strings.TrimSpace(content) == "" {
		_, err := d.db.ExecContext(ctx, `DELETE FROM memories WHERE factory = ? AND routine = ? AND repo = ?`, factory, routine, repo)
		return err
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO memories (factory, routine, repo, content, updated_at, updated_by) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (factory, routine, repo) DO UPDATE SET content = excluded.content, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		factory, routine, repo, content, stamp(at), runID)
	return err
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// MaxWords is the longest intent or summary Fabrica accepts.
const MaxWords = 20

// CheckWords checks an intent or summary: present, and at most 20 words.
func CheckWords(field, text string) error {
	n := len(strings.Fields(text))
	switch {
	case n == 0:
		return fmt.Errorf("%s is empty; sum it up in at most %d words of casual plain English", field, MaxWords)
	case n > MaxWords:
		return fmt.Errorf("%s has %d words, but the limit is %d; please send a shorter one in casual plain English", field, n, MaxWords)
	}
	return nil
}

// CheckStatus checks a checkpoint status, defaulting to progress.
func CheckStatus(status string) (string, error) {
	switch status {
	case "":
		return Progress, nil
	case Progress, Succeeded, Failed:
		return status, nil
	}
	return "", fmt.Errorf("status %q isn't one of progress, succeeded or failed", status)
}

// MaxLinks is the most links one checkpoint can carry.
const MaxLinks = 20

// CheckLinks checks that links are http(s) URLs.
func CheckLinks(links []string) error {
	if len(links) > MaxLinks {
		return fmt.Errorf("a checkpoint can carry at most %d links", MaxLinks)
	}
	for _, l := range links {
		u, err := url.Parse(l)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("link %q isn't an http(s) URL; links are for PRs, issues and commits", l)
		}
	}
	return nil
}

// MaxSessionBytes is the longest session pointer Fabrica accepts.
const MaxSessionBytes = 512

// CheckSession checks a session pointer: at most 512 bytes on one line,
// with no control characters. Empty is fine, because the agent may not know.
func CheckSession(s string) error {
	if len(s) > MaxSessionBytes {
		return fmt.Errorf("session is %d bytes, but the limit is %d; send just a transcript file path, a session ID or a session URL", len(s), MaxSessionBytes)
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return errors.New("session must be on one line, without control characters; send just a transcript file path, a session ID or a session URL")
	}
	return nil
}

// MaxFriction is the most friction notes one checkpoint can carry.
const MaxFriction = 5

// CheckFriction checks friction notes: a few per report, each with a known
// kind and a note of at most 20 words.
func CheckFriction(notes []Friction) error {
	if len(notes) > MaxFriction {
		return fmt.Errorf("a report can carry at most %d friction notes, but this one has %d; keep the ones that matter most", MaxFriction, len(notes))
	}
	for i, f := range notes {
		if !slices.Contains(frictionKinds, f.Kind) {
			return fmt.Errorf("friction kind %q isn't one of navigation, checks, standards, tools, info or other", f.Kind)
		}
		if err := CheckWords(fmt.Sprintf("friction note %d", i+1), f.Note); err != nil {
			return err
		}
	}
	return nil
}

// NewID makes a new run ID. IDs sort by creation time, which keeps
// `ORDER BY run_id` meaningful.
func NewID(now time.Time) string {
	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz" // Crockford base32, lowercase
	var b [16]byte
	ms := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err) // crypto/rand doesn't fail on supported systems
	}
	// 128 bits → 26 base32 characters.
	var out [26]byte
	var acc uint64
	bits, j := 0, 0
	for _, c := range b {
		acc = acc<<8 | uint64(c)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[j] = alphabet[(acc>>uint(bits))&31]
			j++
		}
	}
	if bits > 0 {
		out[j] = alphabet[(acc<<uint(5-bits))&31]
	}
	return "run_" + string(out[:])
}
