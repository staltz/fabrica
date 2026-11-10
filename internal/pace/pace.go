// Package pace reads a factory's pace, like 30m, 4h or 2d, and counts the
// factory's beats.
//
// A routine with a pace trigger, like `trigger: { on: pace, every: 4 }`,
// runs every 4 paces of the factory that deploys it: every 2h in a
// factory with `pace: 30m`. Runtimes don't run it on a schedule of their
// own. Each stub gets a webhook instead, and the remote Fabrica server
// keeps the factory's heartbeat: it beats once every pace, and on every
// 4th beat it calls the routine's webhook. So any pace works, not just the
// periods cron can repeat.
package pace

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Pace is a factory's pace, in whole minutes. Zero means the factory has
// none.
type Pace int

const (
	hour = 60
	day  = 24 * hour
)

var paceRe = regexp.MustCompile(`^([1-9][0-9]*)([mhd])$`)

// Parse reads a pace like 5m, 4h or 2d: a whole number of minutes, hours
// or days.
func Parse(s string) (Pace, error) {
	m := paceRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q isn't a pace; write a whole number of minutes, hours or days, like 30m, 4h or 2d", s)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n > 365*day {
		return 0, fmt.Errorf("%q is too long; a pace is at most a year", s)
	}
	switch m[2] {
	case "h":
		n *= hour
	case "d":
		n *= day
	}
	if n > 365*day {
		return 0, fmt.Errorf("%q is too long; a pace is at most a year", s)
	}
	return Pace(n), nil
}

// String writes the pace in its largest whole unit, like 30m, 2h or 2d.
func (p Pace) String() string { return Minutes(int(p)) }

// Minutes writes a number of minutes in its largest whole unit, like 30m,
// 2h, 90m or 2d.
func Minutes(n int) string {
	switch {
	case n != 0 && n%day == 0:
		return strconv.Itoa(n/day) + "d"
	case n != 0 && n%hour == 0:
		return strconv.Itoa(n/hour) + "h"
	default:
		return strconv.Itoa(n) + "m"
	}
}

// Times is the pace times a routine's every:, in minutes.
func (p Pace) Times(every int) int { return int(p) * every }

// Duration is the pace as a time.Duration.
func (p Pace) Duration() time.Duration { return time.Duration(p) * time.Minute }

// Beat is one beat of a factory's heartbeat: the Nth pace since the Unix
// epoch (midnight UTC on 1 January 1970). Counting from a fixed instant
// makes every beat's number and start time the same wherever, and however
// often, they're worked out, so a server that restarts finds the beat it
// was on.
type Beat struct {
	// N is the beat's number: how many whole paces passed since the epoch
	// before it started.
	N int64
	// At is when it started.
	At time.Time
}

// BeatAt is the beat a time falls in. The pace must not be zero.
func (p Pace) BeatAt(t time.Time) Beat {
	n := t.Unix() / int64(p.Duration()/time.Second)
	return p.Beat(n)
}

// Beat is beat number n. The pace must not be zero.
func (p Pace) Beat(n int64) Beat {
	return Beat{N: n, At: time.Unix(n*int64(p.Duration()/time.Second), 0).UTC()}
}

// Fires reports whether a routine that runs every so many paces fires on
// this beat: on every beat whose number is a multiple of every. So `every:
// 4` at a pace of 30m fires every 2h, on the hour, from midnight UTC.
func (b Beat) Fires(every int) bool { return every > 0 && b.N%int64(every) == 0 }
