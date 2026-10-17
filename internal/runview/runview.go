// Package runview shows run history to people: the table of `fabrica runs`,
// one run's timeline for `fabrica runs show`, and the retro bundle that
// `fabrica runs export` writes. It formats what internal/runs reads and
// reads nothing itself, so it belongs to the operator plane like the code
// that calls it.
//
// Agents write most of the text it shows (intents, summaries, friction
// notes, session pointers), and some of them run on code nobody trusts. So
// every piece of text goes through clean, which turns control characters
// (a terminal escape sequence, say) and line breaks into plain spaces.
package runview

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/staltz/fabrica/internal/runs"
)

// View says how to show times, and where blueprints live.
type View struct {
	// Now is the current time. A date from this year is shown without its
	// year. The zero value means time.Now().
	Now time.Time
	// Loc is the time zone to show times in. Nil means time.Local.
	Loc *time.Location
	// BlueprintPath says where a factory's blueprint lives inside its git
	// repo, like "team/blueprint", or "" when it's at the root. known is
	// false when this machine doesn't know the factory. Nil means it never
	// does.
	BlueprintPath func(factory string) (path string, known bool)
}

func (v View) loc() *time.Location {
	if v.Loc == nil {
		return time.Local
	}
	return v.Loc
}

func (v View) now() time.Time {
	if v.Now.IsZero() {
		return time.Now()
	}
	return v.Now
}

// when formats a time and date for people: "Oct 17 09:41", with seconds
// when asked, and with the year when it isn't this year's. Markdown files
// outlive the day they were made on, so there it's always "2026-10-17
// 09:41".
func (v View) when(t time.Time, seconds, markdown bool) string {
	t = t.In(v.loc())
	clock := "15:04"
	if seconds {
		clock = "15:04:05"
	}
	switch {
	case markdown:
		return t.Format("2006-01-02 " + clock)
	case t.Year() != v.now().In(v.loc()).Year():
		return t.Format("Jan 2 2006 " + clock)
	}
	return t.Format("Jan 2 " + clock)
}

// short formats a time for a table, where every column counts: "Oct 17
// 09:41", without the year.
func (v View) short(t time.Time) string { return t.In(v.loc()).Format("Jan 2 15:04") }

// day formats a date the way when does.
func (v View) day(t time.Time, markdown bool) string {
	t = t.In(v.loc())
	switch {
	case markdown:
		return t.Format("2006-01-02")
	case t.Year() != v.now().In(v.loc()).Year():
		return t.Format("Jan 2 2006")
	}
	return t.Format("Jan 2")
}

// zone names the time zone times are shown in, like "UTC+02:00".
func (v View) zone() string {
	_, offset := v.now().In(v.loc()).Zone()
	if offset == 0 {
		return "UTC"
	}
	return "UTC" + v.now().In(v.loc()).Format("-07:00")
}

// clean makes text from an agent safe to print: control characters and line
// breaks become spaces, and runs of spaces become one.
func clean(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

// cut shortens s to at most n characters, ending it with … when it had to.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// pad fills s with spaces up to n characters.
func pad(s string, n int) string {
	if gap := n - utf8.RuneCountInString(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// shortRepo drops the host from a repo name: github.com/acme/app is
// acme/app.
func shortRepo(repo string) string {
	if _, rest, ok := strings.Cut(repo, "/"); ok {
		return rest
	}
	return repo
}

// elapsed says how long a gap in time is, in the two biggest units that
// matter: 40s, 17m, 2h 5m, 1d 3h.
func elapsed(d time.Duration) string {
	pair := func(big int, bigUnit string, small int, smallUnit string) string {
		if small == 0 {
			return fmt.Sprintf("%d%s", big, bigUnit)
		}
		return fmt.Sprintf("%d%s %d%s", big, bigUnit, small, smallUnit)
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return pair(int(d/time.Hour), "h", int(d%time.Hour/time.Minute), "m")
	}
	return pair(int(d/(24*time.Hour)), "d", int(d%(24*time.Hour)/time.Hour), "h")
}

// plural writes "1 run" or "3 runs".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// outcomeOrder is the order the bundle lists runs in: the ones to look at
// first come first.
var outcomeOrder = []string{runs.OutcomeFailed, runs.OutcomeStale, runs.OutcomeSucceeded, runs.OutcomeInProgress}

// Counts says how many runs had each outcome, like "17 succeeded, 4 failed,
// 2 stale". Outcomes nobody had are left out.
func Counts(list []*runs.Detail) string {
	n := map[string]int{}
	for _, d := range list {
		n[d.Outcome]++
	}
	var parts []string
	for _, o := range []string{runs.OutcomeSucceeded, runs.OutcomeFailed, runs.OutcomeStale, runs.OutcomeInProgress} {
		if n[o] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[o], strings.ReplaceAll(o, "_", " ")))
		}
	}
	return strings.Join(parts, ", ")
}
