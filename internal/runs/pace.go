package runs

import (
	"context"
	"time"
)

// Pace fire statuses.
const (
	// Firing means the server claimed the beat and is calling the webhook.
	// A fire that stays firing was cut short (the server stopped); it isn't
	// tried again, so a stub never runs twice in one beat.
	Firing = "firing"
	Fired  = "fired"
	// FireFailed means the webhook call failed; Error says why.
	FireFailed = "failed"
)

// Fire is one call to the webhook of a stub, on one beat of its factory's
// heartbeat.
type Fire struct {
	Factory string
	Runtime string
	// Stub is the stub's identity.
	Stub    string
	Routine string
	Repo    string
	// BeatAt is when the beat started.
	BeatAt  time.Time
	FiredAt time.Time
	Status  string
	Error   string
}

// ClaimFire records that the server is about to fire a stub on a beat, and
// reports whether it should: false when the stub was already fired (or
// tried) on that beat, by this server before it restarted, say.
func (d *DB) ClaimFire(ctx context.Context, f Fire) (bool, error) {
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO pace_fires (factory, runtime, stub, beat_at, routine, repo, fired_at, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		f.Factory, f.Runtime, f.Stub, stamp(f.BeatAt), f.Routine, f.Repo, stamp(f.FiredAt), Firing)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// FinishFire records how a claimed fire went: fired, or failed and why.
func (d *DB) FinishFire(ctx context.Context, f Fire, fireErr error) error {
	status, msg := Fired, ""
	if fireErr != nil {
		status, msg = FireFailed, fireErr.Error()
	}
	_, err := d.db.ExecContext(ctx,
		`UPDATE pace_fires SET status = ?, error = ? WHERE factory = ? AND runtime = ? AND stub = ? AND beat_at = ?`,
		status, null(msg), f.Factory, f.Runtime, f.Stub, stamp(f.BeatAt))
	return err
}

// PaceFires lists the fires since a time, oldest first. It's for the operator
// plane and tests, like the rest of the read side.
func (d *DB) PaceFires(ctx context.Context, since time.Time) ([]Fire, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT factory, runtime, stub, routine, repo, beat_at, fired_at, status, COALESCE(error, '')
		 FROM pace_fires WHERE fired_at >= ? ORDER BY fired_at, stub`, stamp(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fire
	for rows.Next() {
		var f Fire
		var beat, fired string
		if err := rows.Scan(&f.Factory, &f.Runtime, &f.Stub, &f.Routine, &f.Repo, &beat, &fired, &f.Status, &f.Error); err != nil {
			return nil, err
		}
		f.BeatAt, _ = time.Parse(time.RFC3339Nano, beat)
		f.FiredAt, _ = time.Parse(time.RFC3339Nano, fired)
		out = append(out, f)
	}
	return out, rows.Err()
}
