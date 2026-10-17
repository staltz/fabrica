package runs

// This file is the read side of run history: List, Detail and the types
// they return. Agents write run history and only people read it, so only
// operator-plane code (the CLI, the admin API and the dashboard) may call
// these. internal/mcpserver has a test that keeps it that way.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Outcomes of a run, worked out by the run_overview view from its
// checkpoints.
const (
	OutcomeSucceeded  = "succeeded"
	OutcomeFailed     = "failed"
	OutcomeInProgress = "in_progress"
	OutcomeStale      = "stale"
)

// Outcomes lists every outcome.
var Outcomes = []string{OutcomeSucceeded, OutcomeFailed, OutcomeInProgress, OutcomeStale}

// InteractiveRoutine is what a Filter's Routine says to mean the runs that
// have no routine, which are people working in their editor.
const InteractiveRoutine = "-"

// Limits on how many runs one List returns.
const (
	DefaultLimit = 100
	MaxLimit     = 500
)

// DefaultSince is how far back to look when nobody says: callers use it to
// make Filter.Since.
const DefaultSince = 7 * 24 * time.Hour

// Filter says which runs List returns. The zero value means every run, up
// to DefaultLimit of them.
type Filter struct {
	// Factory, Routine, Repo and Runtime each limit the runs to those with
	// that value. Empty means any. Routine "-" means interactive runs.
	Factory, Routine, Repo, Runtime string
	// Outcomes limits the runs to those with one of these outcomes.
	Outcomes []string
	// Since limits the runs to those that started at or after it. The zero
	// time means no limit.
	Since time.Time
	// Before is a run_id, for paging: only runs older than it.
	Before string
	// Limit is the most runs to return. Zero means DefaultLimit.
	Limit int
}

// Validate checks a filter's outcomes and limit, with an error fit for
// whoever typed them.
func (f Filter) Validate() error {
	for _, o := range f.Outcomes {
		if !slices.Contains(Outcomes, o) {
			return fmt.Errorf("outcome %q isn't one of %s", o, strings.Join(Outcomes, ", "))
		}
	}
	if f.Limit != 0 {
		return CheckLimit(f.Limit)
	}
	return nil
}

// CheckLimit checks a limit someone asked for: from 1 to MaxLimit.
func CheckLimit(n int) error {
	if n < 1 || n > MaxLimit {
		return fmt.Errorf("limit is %d, but it has to be from 1 to %d", n, MaxLimit)
	}
	return nil
}

// ParseSince turns what someone typed for "since" into a time: a number
// of days or hours back from now (7d, 48h), or an RFC 3339 time.
func ParseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if n := len(s); n > 1 && (s[n-1] == 'd' || s[n-1] == 'h') {
		// 16 bits keep the multiplication from overflowing.
		if count, err := strconv.ParseUint(s[:n-1], 10, 16); err == nil && count > 0 {
			unit := time.Hour
			if s[n-1] == 'd' {
				unit = 24 * time.Hour
			}
			return now.Add(-time.Duration(count) * unit), nil
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("since %q isn't a number of days or hours back, like 7d or 48h, or an RFC 3339 time, like 2026-10-10T00:00:00Z", s)
}

// Summary is one run, as the admin API, `fabrica runs --json` and the retro
// bundle show it. Times have whole-second precision. The fields agents
// write (intent, session, last_summary) are best effort.
type Summary struct {
	RunID           string    `json:"run_id"`
	StartedAt       time.Time `json:"started_at"`
	LastAt          time.Time `json:"last_at"` // the latest checkpoint, or started_at
	Outcome         string    `json:"outcome"` // see Outcomes
	Intent          string    `json:"intent"`
	Routine         string    `json:"routine,omitempty"` // empty for interactive runs
	Runtime         string    `json:"runtime,omitempty"`
	Repo            string    `json:"repo,omitempty"`
	Factory         string    `json:"factory,omitempty"`
	BlueprintSHA    string    `json:"blueprint_sha,omitempty"`
	ServedBy        string    `json:"served_by"` // ServedLocal or ServedRemote
	Session         string    `json:"session,omitempty"`
	LastSummary     string    `json:"last_summary,omitempty"`
	CheckpointCount int       `json:"checkpoint_count"`
	FrictionCount   int       `json:"friction_count"`
	PRLinks         []string  `json:"pr_links"` // never null
}

// Detail is one run in full: its summary, the stub it claimed and
// everything that happened in it.
type Detail struct {
	Summary
	Stub     string  `json:"stub,omitempty"` // the stub fingerprint the run claimed, if any
	Timeline []Event `json:"timeline"`       // never null
}

// Event types in a timeline.
const (
	EventLoad       = "load"
	EventCheckpoint = "checkpoint"
	EventFriction   = "friction"
)

// Event is one entry of a run's timeline: a load, a checkpoint or a
// friction note. Which fields are set depends on Type, and JSON only has
// the ones that apply.
type Event struct {
	At   time.Time `json:"at"`
	Type string    `json:"type"` // EventLoad, EventCheckpoint or EventFriction
	// Kind is the kind of load (skill, rule or routine), or of friction.
	Kind string `json:"kind,omitempty"`
	// Name is what a load loaded.
	Name string `json:"name,omitempty"`
	// Author, Status, Summary and Links are a checkpoint's.
	Author  string   `json:"author,omitempty"`
	Status  string   `json:"status,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Links   []string `json:"links,omitempty"`
	// Note is a friction note's text.
	Note string `json:"note,omitempty"`
}

// MarshalJSON writes the fields of the event's type, so a checkpoint
// without links still says "links": [] and a load has none.
func (e Event) MarshalJSON() ([]byte, error) {
	switch e.Type {
	case EventLoad:
		return json.Marshal(struct {
			At   time.Time `json:"at"`
			Type string    `json:"type"`
			Kind string    `json:"kind"`
			Name string    `json:"name"`
		}{e.At, e.Type, e.Kind, e.Name})
	case EventCheckpoint:
		links := e.Links
		if links == nil {
			links = []string{}
		}
		return json.Marshal(struct {
			At      time.Time `json:"at"`
			Type    string    `json:"type"`
			Author  string    `json:"author"`
			Status  string    `json:"status"`
			Summary string    `json:"summary"`
			Links   []string  `json:"links"`
		}{e.At, e.Type, e.Author, e.Status, e.Summary, links})
	case EventFriction:
		return json.Marshal(struct {
			At   time.Time `json:"at"`
			Type string    `json:"type"`
			Kind string    `json:"kind"`
			Note string    `json:"note"`
		}{e.At, e.Type, e.Kind, e.Note})
	}
	return nil, fmt.Errorf("unknown timeline event type %q", e.Type)
}

// Page is one page of runs, as the admin API and `fabrica runs --json`
// show it.
type Page struct {
	SchemaVersion int       `json:"schema_version"`
	Runs          []Summary `json:"runs"`
	// Next is the Before value for the next page. Missing on the last page.
	Next string `json:"next,omitempty"`
}

// NewPage makes a page of runs, at the current schema version.
func NewPage(list []Summary, next string) Page {
	if list == nil {
		list = []Summary{}
	}
	return Page{SchemaVersion: SchemaVersion, Runs: list, Next: next}
}

// summaryColumns are the columns of run_overview that make a Summary, in
// the order scanSummary reads them.
const summaryColumns = `run_id, started_at, last_at, outcome, intent, routine, runtime, repo, factory, blueprint_sha,
	served_by, session, last_summary, checkpoint_count, friction_count, pr_links`

type scanner interface{ Scan(dest ...any) error }

// scanSummary reads a row of summaryColumns, then any extra columns into
// the destinations given.
func scanSummary(row scanner, extra ...any) (Summary, error) {
	var s Summary
	var started, last, prLinks string
	var routine, runtime, repo, factory, sha, session, lastSummary sql.NullString
	dest := append([]any{&s.RunID, &started, &last, &s.Outcome, &s.Intent, &routine, &runtime, &repo, &factory, &sha,
		&s.ServedBy, &session, &lastSummary, &s.CheckpointCount, &s.FrictionCount, &prLinks}, extra...)
	if err := row.Scan(dest...); err != nil {
		return Summary{}, err
	}
	s.StartedAt, s.LastAt = parseTime(started).Truncate(time.Second), parseTime(last).Truncate(time.Second)
	s.Routine, s.Runtime, s.Repo, s.Factory, s.BlueprintSHA = routine.String, runtime.String, repo.String, factory.String, sha.String
	s.Session, s.LastSummary = session.String, lastSummary.String
	if err := json.Unmarshal([]byte(prLinks), &s.PRLinks); err != nil || s.PRLinks == nil {
		s.PRLinks = []string{}
	}
	return s, nil
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// where is the SQL condition for the filter, and its arguments. Only
// fixed text goes into the SQL: every value is an argument.
func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, values ...any) {
		conds = append(conds, cond)
		args = append(args, values...)
	}
	if f.Factory != "" {
		add("factory = ?", f.Factory)
	}
	switch f.Routine {
	case "":
	case InteractiveRoutine:
		add("routine IS NULL")
	default:
		add("routine = ?", f.Routine)
	}
	if f.Repo != "" {
		add("repo = ?", f.Repo)
	}
	if f.Runtime != "" {
		add("runtime = ?", f.Runtime)
	}
	if len(f.Outcomes) > 0 {
		values := make([]any, len(f.Outcomes))
		for i, o := range f.Outcomes {
			values[i] = o
		}
		add("outcome IN (?"+strings.Repeat(", ?", len(values)-1)+")", values...)
	}
	if !f.Since.IsZero() {
		// Stored times are text like 2026-10-17T09:41:02.123Z. Leaving the
		// Z off makes a time with fractional seconds sort after the same
		// second without them, which is where it belongs.
		add("started_at >= ?", f.Since.UTC().Format("2006-01-02T15:04:05"))
	}
	if f.Before != "" {
		add("run_id < ?", f.Before)
	}
	return strings.Join(conds, " AND "), args
}

// List reads a page of runs, newest first. When there are more, next is
// the Before value that gets the next page.
func (d *DB) List(ctx context.Context, f Filter) (runs []Summary, next string, err error) {
	if err := f.Validate(); err != nil {
		return nil, "", err
	}
	limit := f.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	query := "SELECT " + summaryColumns + " FROM run_overview"
	cond, args := f.where()
	if cond != "" {
		query += " WHERE " + cond
	}
	query += " ORDER BY run_id DESC LIMIT ?"
	// One more than asked for tells whether there's a next page.
	rows, err := d.db.QueryContext(ctx, query, append(args, limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	runs = []Summary{}
	for rows.Next() {
		s, err := scanSummary(rows)
		if err != nil {
			return nil, "", err
		}
		runs = append(runs, s)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(runs) > limit {
		runs = runs[:limit]
		next = runs[limit-1].RunID
	}
	return runs, next, nil
}

// Detail reads one run in full. It returns ErrNoRun when there's no such
// run.
func (d *DB) Detail(ctx context.Context, runID string) (*Detail, error) {
	// One transaction, so the timeline matches the summary's counts.
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var stub sql.NullString
	s, err := scanSummary(tx.QueryRowContext(ctx,
		`SELECT `+summaryColumns+`, (SELECT r.stub FROM runs r WHERE r.run_id = o.run_id) FROM run_overview o WHERE o.run_id = ?`, runID), &stub)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoRun
	}
	if err != nil {
		return nil, err
	}
	events, err := timeline(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	return &Detail{Summary: s, Stub: stub.String, Timeline: events}, nil
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// timelineEntry is an event with what it's sorted by.
type timelineEntry struct {
	Event
	at   time.Time // before it's cut to whole seconds
	rank int       // among events at the same time: loads, checkpoints, then friction
	id   int64
}

// timeline reads every load, checkpoint and friction note of a run, sorted
// by time. A friction note has the time of the checkpoint it came with,
// and comes right after it.
func timeline(ctx context.Context, q queryer, runID string) ([]Event, error) {
	var entries []timelineEntry
	read := func(query string, rank int, scan func(rows *sql.Rows, e *timelineEntry, at *string) error) error {
		rows, err := q.QueryContext(ctx, query, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e := timelineEntry{rank: rank}
			var at string
			if err := scan(rows, &e, &at); err != nil {
				return err
			}
			e.at = parseTime(at)
			e.Event.At = e.at.Truncate(time.Second)
			entries = append(entries, e)
		}
		return rows.Err()
	}

	err := read(`SELECT id, at, kind, name FROM loads WHERE run_id = ?`, 0, func(rows *sql.Rows, e *timelineEntry, at *string) error {
		e.Type = EventLoad
		return rows.Scan(&e.id, at, &e.Kind, &e.Name)
	})
	if err != nil {
		return nil, err
	}
	err = read(`SELECT id, at, author, status, summary, links FROM checkpoints WHERE run_id = ?`, 1, func(rows *sql.Rows, e *timelineEntry, at *string) error {
		e.Type = EventCheckpoint
		var links string
		if err := rows.Scan(&e.id, at, &e.Author, &e.Status, &e.Summary, &links); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(links), &e.Links)
		if e.Links == nil {
			e.Links = []string{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = read(`SELECT id, at, kind, note FROM friction WHERE run_id = ?`, 2, func(rows *sql.Rows, e *timelineEntry, at *string) error {
		e.Type = EventFriction
		return rows.Scan(&e.id, at, &e.Kind, &e.Note)
	})
	if err != nil {
		return nil, err
	}

	slices.SortStableFunc(entries, func(a, b timelineEntry) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		return int(a.id - b.id)
	})
	out := make([]Event, len(entries))
	for i, e := range entries {
		out[i] = e.Event
	}
	return out, nil
}
