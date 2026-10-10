package localtools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/promptblock"
)

// Claude Code desktop scheduled tasks live in
// ~/.claude/scheduled-tasks/<id>/SKILL.md: a name and description in the
// frontmatter, and the prompt as the body. The schedule and the folder the
// task runs in aren't in the file. The app keeps those itself, and it only
// runs tasks it created, so after Fabrica writes a new task you register it
// in the app once (see TaskSetup). Later edits to the file apply on the
// task's next run.

func (c *claudeCode) tasksDir() string { return filepath.Join(c.home, ".claude", "scheduled-tasks") }

// TaskID is the scheduled task's ID, which is also its folder name. The
// hash keeps it unique when names would otherwise collide.
func TaskID(t Task) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{t.Factory, t.Routine, t.Repo}, "\x00")))
	return "fabrica-" + slug(t.Routine) + "-" + hex.EncodeToString(sum[:4])
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func taskDescription(t Task) string {
	return fmt.Sprintf("Fabrica routine `%s` from factory `%s` on `%s` (stub `%s`), cron `%s` in UTC",
		t.Routine, t.Factory, t.Repo, t.Fingerprint, t.Cron)
}

var taskDescriptionRe = regexp.MustCompile("^Fabrica routine `([^`]+)` from factory `([^`]+)` on `([^`]+)` \\(stub `([^`]*)`\\), cron `([^`]+)` in UTC$")

func renderTask(t Task) []byte {
	name, _ := json.Marshal(TaskID(t))
	desc, _ := json.Marshal(taskDescription(t))
	return []byte(fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", name, desc, strings.TrimSpace(t.Prompt)))
}

// parseTask reads back a SKILL.md that Fabrica wrote. It reports false for
// anything else, which Fabrica then leaves alone.
func parseTask(data []byte) (Task, bool) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return Task{}, false
	}
	front, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return Task{}, false
	}
	var desc string
	for _, line := range strings.Split(front, "\n") {
		if v, ok := strings.CutPrefix(line, "description:"); ok {
			desc = unquoteYAML(strings.TrimSpace(v))
		}
	}
	m := taskDescriptionRe.FindStringSubmatch(desc)
	if m == nil {
		return Task{}, false
	}
	return Task{Routine: m[1], Factory: m[2], Repo: m[3], Fingerprint: m[4], Cron: m[5], Prompt: strings.TrimSpace(body)}, true
}

// unquoteYAML undoes the quoting Fabrica (or the app, if it rewrites the
// file) puts around a frontmatter value.
func unquoteYAML(v string) string {
	switch {
	case strings.HasPrefix(v, `"`):
		var s string
		if json.Unmarshal([]byte(v), &s) == nil {
			return s
		}
	case strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") && len(v) >= 2:
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return v
}

// readClaudeTasks finds the scheduled tasks Fabrica installed.
func readClaudeTasks(dir string) ([]Task, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Task
	for _, e := range entries {
		if !e.IsDir() || !IsFabricaEntry(e.Name()) {
			continue
		}
		data, err := readOptional(filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			return nil, err
		}
		if t, ok := parseTask(data); ok && TaskID(t) == e.Name() {
			out = append(out, t)
		}
	}
	return out, nil
}

// writeClaudeTasks makes the scheduled tasks Fabrica installed match
// desired: it writes new and changed ones and removes the ones that
// aren't wanted anymore.
func writeClaudeTasks(dir string, desired []Task) error {
	current, err := readClaudeTasks(dir)
	if err != nil {
		return err
	}
	want := map[string]Task{}
	for _, t := range desired {
		want[TaskID(t)] = t
	}
	for _, t := range current {
		if _, ok := want[TaskID(t)]; !ok {
			if err := os.RemoveAll(filepath.Join(dir, TaskID(t))); err != nil {
				return err
			}
		}
	}
	for id, t := range want {
		file := filepath.Join(dir, id, "SKILL.md")
		before, err := readOptional(file)
		if err != nil {
			return err
		}
		if err := writeIfChanged(file, before, renderTask(t)); err != nil {
			return err
		}
		// Fabrica owns the whole file, so a backup inside the task's
		// folder would only get in the app's way.
		_ = os.Remove(file + ".fabrica.bak")
	}
	return nil
}

// TaskSetup is what you need to do in the Claude desktop app after
// `fabrica apply` changed scheduled tasks from cur to want, one step each.
// A step that has you prompt Claude ends with the prompt, wrapped by
// promptblock.
// now gives the local time zone, because the app reads cron in local time.
func TaskSetup(cur, want []Task, now time.Time) []string {
	byID := map[string]Task{}
	for _, t := range cur {
		byID[TaskID(t)] = t
	}
	wantIDs := map[string]bool{}
	var out []string
	for _, t := range want {
		id := TaskID(t)
		wantIDs[id] = true
		old, had := byID[id]
		if had && old.Cron == t.Cron {
			continue // the app picks up prompt changes on the next run
		}
		schedule := fmt.Sprintf("cron `%s` (that's UTC; the app reads cron in local time)", t.Cron)
		if local, ok := LocalCron(t.Cron, now); ok {
			_, offset := now.Zone()
			schedule = fmt.Sprintf("cron `%s` (`%s` in UTC, at today's offset of UTC%s)", local, t.Cron, formatOffset(offset))
		}
		if had {
			out = append(out, fmt.Sprintf("Change the schedule of %s to %s.", id, schedule))
		} else {
			// The app only runs tasks it created itself, so a new one has
			// to be registered there once. After that, Fabrica's edits to
			// its SKILL.md apply on the next run.
			prompt := fmt.Sprintf("Create a scheduled task from ~/.claude/scheduled-tasks/%s/SKILL.md. Keep its task ID (`%s`), description and prompt exactly as they are in that file, and schedule it on %s.", id, id, schedule)
			out = append(out, fmt.Sprintf("Register %s: in a desktop session on your checkout of %s, send Claude this prompt:\n%s", id, t.Repo, promptblock.Wrap(prompt)))
		}
	}
	for _, t := range cur {
		if !wantIDs[TaskID(t)] {
			out = append(out, fmt.Sprintf("Delete %s from the app's list of routines; Fabrica removed its file.", TaskID(t)))
		}
	}
	return out
}

func formatOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}
	return fmt.Sprintf("%s%02d:%02d", sign, seconds/3600, seconds%3600/60)
}

// LocalCron converts a UTC cron expression to the time zone of now, at
// now's offset from UTC. It handles fixed minutes and hours (or every
// hour), with any day of the week, and reports false for anything else.
func LocalCron(cron string, now time.Time) (string, bool) {
	f := strings.Fields(cron)
	if len(f) != 5 || f[2] != "*" || f[3] != "*" {
		return "", false
	}
	_, offset := now.Zone()
	shift := offset / 60
	if shift == 0 {
		return cron, true
	}
	minutes, ok := cronValues(f[0], 0, 59)
	if !ok {
		return "", false
	}
	if f[1] == "*" {
		// Every hour: only the minutes move, but a day of the week no
		// longer lines up with whole local days.
		if f[4] != "*" {
			return "", false
		}
		var out []int
		for _, m := range minutes {
			out = append(out, ((m+shift)%60+60)%60)
		}
		return joinValues(out) + " * * * *", true
	}
	hours, ok := cronValues(f[1], 0, 23)
	if !ok {
		return "", false
	}
	type hm struct{ h, m int }
	pairs := map[hm]bool{}
	newMinutes, newHours, days := map[int]bool{}, map[int]bool{}, map[int]bool{}
	for _, h := range hours {
		for _, m := range minutes {
			total := h*60 + m + shift
			day := floorDiv(total, 24*60)
			total -= day * 24 * 60
			p := hm{total / 60, total % 60}
			pairs[p] = true
			newHours[p.h], newMinutes[p.m], days[day] = true, true, true
		}
	}
	// Cron fires on every minute × hour combination, so the shifted times
	// must still form one.
	if len(pairs) != len(newHours)*len(newMinutes) {
		return "", false
	}
	dow := "*"
	if f[4] != "*" {
		if len(days) != 1 {
			return "", false
		}
		weekdays, ok := cronValues(f[4], 0, 7)
		if !ok {
			return "", false
		}
		var day int
		for d := range days {
			day = d
		}
		var out []int
		for _, d := range weekdays {
			out = append(out, ((d%7+day)%7+7)%7)
		}
		dow = joinValues(out)
	}
	return fmt.Sprintf("%s %s * * %s", joinValues(keys(newMinutes)), joinValues(keys(newHours)), dow), true
}

// cronValues expands a cron field made of numbers, ranges and lists.
func cronValues(field string, lo, hi int) ([]int, bool) {
	var out []int
	for _, part := range strings.Split(field, ",") {
		a, b, isRange := strings.Cut(part, "-")
		from, err := strconv.Atoi(a)
		if err != nil {
			return nil, false
		}
		to := from
		if isRange {
			if to, err = strconv.Atoi(b); err != nil {
				return nil, false
			}
		}
		if from < lo || to > hi || from > to {
			return nil, false
		}
		for v := from; v <= to; v++ {
			out = append(out, v)
		}
	}
	return out, true
}

func joinValues(vs []int) string {
	seen := map[int]bool{}
	var uniq []int
	for _, v := range vs {
		if !seen[v] {
			seen[v] = true
			uniq = append(uniq, v)
		}
	}
	sort.Ints(uniq)
	parts := make([]string, len(uniq))
	for i, v := range uniq {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

func keys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}
