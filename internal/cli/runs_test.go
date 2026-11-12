package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/testkit"
	"github.com/staltz/fabrica/internal/token"
)

var update = flag.Bool("update", false, "rewrite the golden files of the runs tests")

// The runs tests pretend it's 10:04 on Oct 17 2026 in a time zone two hours
// ahead of UTC.
var (
	runsTestNow  = time.Date(2026, 10, 17, 8, 4, 0, 0, time.UTC)
	runsTestZone = time.FixedZone("TEST", 2*60*60)
)

// runsWorld is a home repo with a run-history database in it.
type runsWorld struct {
	home    string
	db      *runs.DB
	ids     map[string]string // name → run ID
	session string            // the transcript file one run points at
}

// runsTestID makes a run ID that sorts by time, as real ones do, with a
// tail that doesn't change between test runs.
func runsTestID(at time.Time, tail string) string {
	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	ms := uint64(at.UnixMilli())
	var stamp [10]byte
	for i := len(stamp) - 1; i >= 0; i-- {
		stamp[i], ms = alphabet[ms&31], ms>>5
	}
	return "run_" + string(stamp[:]) + tail
}

// newRunsWorld makes a home repo with run history and a clock to match.
//
// Runs that are still going have to start far in the future: the
// run_overview view counts a run as stale when it's been quiet for 6 hours
// by the real clock, which a test can't change. The stale one is long
// ago for the same reason.
func newRunsWorld(t *testing.T) *runsWorld {
	t.Helper()
	w := &runsWorld{home: t.TempDir(), ids: map[string]string{}}
	t.Setenv("FABRICA_HOME", w.home)
	t.Setenv("FABRICA_CACHE", t.TempDir())
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("CI", "")
	testkit.FreshCredentials(t)
	unsetenv(t, token.EnvVar)
	unsetenv(t, token.AdminEnvVar)
	if _, _, err := home.EnsurePersonal(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	old := runsClock
	runsClock = func() (time.Time, *time.Location) { return runsTestNow, runsTestZone }
	t.Cleanup(func() { runsClock = old })

	// A transcript on this machine, for the one run that points at a file.
	// Only a .jsonl file in the home folder counts as one.
	w.session = filepath.Join(userHome, "session.jsonl")
	if err := os.WriteFile(w.session, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := runs.Open(filepath.Join(w.home, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	w.db = db
	t.Cleanup(func() { db.Close() })
	w.seed(t)
	return w
}

func (w *runsWorld) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	at := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	begin := func(name, tail string, started time.Time, r runs.Run) {
		t.Helper()
		r.ID, r.StartedAt = runsTestID(started, tail), started
		w.ids[name] = r.ID
		must(w.db.Begin(ctx, r))
	}
	check := func(name, when, author, status, summary string, friction []runs.Friction, links ...string) {
		t.Helper()
		must(w.db.AddCheckpoint(ctx, w.ids[name], runs.Checkpoint{At: at(when), Author: author, Status: status, Summary: summary, Friction: friction, Links: links}))
	}
	load := func(name, when, kind, what string) {
		t.Helper()
		must(w.db.Loaded(ctx, w.ids[name], kind, what, at(when)))
	}
	const sha, oldSHA = "ee8f1420a5b6c7d8e9f00112233445566778899a", "b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7"

	// A routine that succeeded, as in the spec's example (the zone is two
	// hours ahead, so it started at 09:41 for whoever reads this).
	begin("review", "0000000000000001", at("2026-10-17T07:41:02Z"), runs.Run{Intent: "Review PR 412 for the billing retry change", Routine: "pr-review",
		Runtime: "claude-code-cloud", Repo: "github.com/acme/app", Factory: "acme", BlueprintSHA: sha, ServedBy: runs.ServedRemote, Session: "https://claude.ai/code/session_01Abc"})
	load("review", "2026-10-17T07:41:05Z", runs.LoadRoutine, "pr-review")
	load("review", "2026-10-17T07:41:06Z", runs.LoadRule, "product-knowledge-only")
	load("review", "2026-10-17T07:41:06Z", runs.LoadRule, "commit-messages")
	load("review", "2026-10-17T07:41:09Z", runs.LoadSkill, "pr-review")
	check("review", "2026-10-17T07:47:30Z", runs.ByAgent, runs.Progress, "Read the diff and the retry tests", nil)
	check("review", "2026-10-17T07:52:11Z", runs.ByAgent, runs.Progress, "Found the retry backoff settings", []runs.Friction{{Kind: "navigation", Note: "couldn't find where retry backoff is configured"}})
	check("review", "2026-10-17T07:58:40Z", runs.ByAgent, runs.Succeeded, "Posted review with two blocking comments", nil, "https://github.com/acme/app/pull/412")

	// Another review that started in the same instant, so the first
	// characters of their IDs are the same.
	begin("parallel", "1000000000000000", at("2026-10-17T07:41:02Z"), runs.Run{Intent: "Review PR 413 for the search index change", Routine: "pr-review",
		Runtime: "claude-code-cloud", Repo: "github.com/acme/app", Factory: "acme", BlueprintSHA: sha, ServedBy: runs.ServedRemote})
	load("parallel", "2026-10-17T07:41:07Z", runs.LoadRoutine, "pr-review")
	load("parallel", "2026-10-17T07:41:08Z", runs.LoadSkill, "pr-review")
	check("parallel", "2026-10-17T07:50:00Z", runs.ByAgent, runs.Succeeded, "Approved with one nit", nil, "https://github.com/acme/app/pull/413")

	// An interactive session that went quiet, with a transcript on this
	// machine.
	begin("flaky", "0000000000000002", at("2026-10-12T06:02:11Z"), runs.Run{Intent: "Fix flaky checkout test", Runtime: "claude-code-local",
		Repo: "github.com/acme/app", Factory: "acme", BlueprintSHA: sha, ServedBy: runs.ServedLocal, Session: w.session})
	load("flaky", "2026-10-12T06:02:15Z", runs.LoadRule, "product-knowledge-only")
	load("flaky", "2026-10-12T06:02:15Z", runs.LoadRule, "product-knowledge-only")
	load("flaky", "2026-10-12T06:02:16Z", runs.LoadSkill, "debugging")
	check("flaky", "2026-10-12T06:20:00Z", runs.ByAgent, runs.Progress, "Reproduced the flake in checkout.spec", []runs.Friction{{Kind: "tools", Note: "test runner takes four minutes per run"}})

	// A routine that failed, in another repo and runtime.
	begin("autofix", "0000000000000003", at("2026-10-16T20:15:00Z"), runs.Run{Intent: "Fix lint failure on main", Routine: "autofix",
		Runtime: "cursor-cloud", Repo: "github.com/acme/api", Factory: "acme", BlueprintSHA: oldSHA, ServedBy: runs.ServedRemote})
	load("autofix", "2026-10-16T20:15:04Z", runs.LoadRoutine, "autofix")
	load("autofix", "2026-10-16T20:15:05Z", runs.LoadSkill, "lint-fix")
	check("autofix", "2026-10-16T20:16:00Z", runs.ByAgent, runs.Progress, "Found the failing eslint rule", nil)
	check("autofix", "2026-10-16T20:20:30Z", runs.ByAgent, runs.Failed, "Lint still fails after two attempts", []runs.Friction{
		{Kind: "checks", Note: "no way to run lint locally before pushing"}, {Kind: "standards", Note: "the lint config and the style rule disagree"}},
		"https://github.com/acme/api/pull/77")

	// A run Fabrica refused.
	begin("refused", "0000000000000004", at("2026-10-16T18:00:00Z"), runs.Run{Intent: "Review PR 99 for the billing change", Routine: "pr-review",
		Runtime: "claude-code-cloud", Repo: "github.com/acme/api", Factory: "acme", ServedBy: runs.ServedRemote})
	check("refused", "2026-10-16T18:00:00Z", runs.ByFabrica, runs.Failed, "Refused: stub is outdated; the factory needs fabrica apply", nil)

	// Older than a week.
	begin("old", "0000000000000005", at("2026-10-01T10:00:00Z"), runs.Run{Intent: "Review PR 380", Routine: "pr-review",
		Runtime: "claude-code-cloud", Repo: "github.com/acme/app", Factory: "acme", BlueprintSHA: oldSHA, ServedBy: runs.ServedRemote})
	load("old", "2026-10-01T10:00:03Z", runs.LoadRoutine, "pr-review")
	check("old", "2026-10-01T10:05:00Z", runs.ByAgent, runs.Succeeded, "Approved", nil, "https://github.com/acme/app/pull/380")

	// Still going: it starts in 2099, so by the real clock it's never been
	// quiet for 6 hours. It's from another year, too.
	begin("going", "0000000000000006", at("2099-01-01T07:00:00Z"), runs.Run{Intent: "Draft the release notes", Runtime: "claude-code-local",
		Repo: "github.com/acme/web", Factory: "acme", ServedBy: runs.ServedLocal})
	check("going", "2099-01-01T07:05:00Z", runs.ByAgent, runs.Progress, "Listed the merged PRs", nil)

	// A run of another factory, a week and a half ago.
	begin("personal", "0000000000000007", at("2026-10-03T07:00:00Z"), runs.Run{Intent: "Tidy up my notes", Runtime: "claude-code-local",
		Repo: "github.com/me/notes", Factory: "personal", ServedBy: runs.ServedLocal})
	check("personal", "2026-10-03T07:10:00Z", runs.ByAgent, runs.Succeeded, "Moved the old notes to the archive", nil)
}

// run runs the CLI with the world's clock, and returns what it printed to
// standard output and to standard error.
func (w *runsWorld) run(t *testing.T, wantCode int, args ...string) (stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Main(context.Background(), args, strings.NewReader(""), &out, &errOut)
	if code != wantCode {
		t.Fatalf("fabrica %s: exit %d, want %d\n%s%s", strings.Join(args, " "), code, wantCode, out.String(), errOut.String())
	}
	return w.normalize(out.String()), w.normalize(errOut.String())
}

// normalize replaces what changes between test runs.
func (w *runsWorld) normalize(s string) string {
	s = strings.ReplaceAll(s, filepath.Dir(w.session), "$TRANSCRIPTS")
	return strings.ReplaceAll(s, w.home, "$FABRICA_HOME")
}

// goldenDir is testdata, where the tests start, since they move to a folder
// of their own.
var goldenDir, _ = filepath.Abs("testdata")

// golden compares output to testdata/runs/<name>.golden. Run the tests
// with -update to write them again after a change, and look at the diff.
func golden(t *testing.T, name, got string) {
	t.Helper()
	file := filepath.Join(goldenDir, "runs", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v\nrun the test with -update to write it", err)
	}
	if got != string(want) {
		t.Errorf("%s doesn't match %s\n--- got ---\n%s--- want ---\n%s", name, file, got, want)
	}
}

func TestRunsListGolden(t *testing.T) {
	w := newRunsWorld(t)
	for _, c := range []struct {
		name string
		args []string
	}{
		{"list", []string{"runs"}},
		{"list-subcommand", []string{"runs", "list"}},
		{"list-one-factory", []string{"runs", "--factory", "acme"}},
		{"list-interactive", []string{"runs", "--routine", "-"}},
		{"list-routine", []string{"runs", "--routine", "pr-review"}},
		{"list-failed-or-stale", []string{"runs", "--outcome", "failed", "--outcome", "stale"}},
		{"list-failed-or-stale-commas", []string{"runs", "--outcome", "failed,stale"}},
		{"list-repo-runtime", []string{"runs", "--repo", "github.com/acme/api", "--runtime", "cursor-cloud"}},
		{"list-since-a-month", []string{"runs", "--since", "30d"}},
		{"list-since-a-time", []string{"runs", "--since", "2026-10-16T19:00:00Z"}},
		{"list-limit", []string{"runs", "--limit", "3"}},
		{"list-nothing", []string{"runs", "--factory", "nobody"}},
		{"list-nothing-lately", []string{"runs", "--since", "1h", "--routine", "autofix"}},
		{"list-since-an-hour", []string{"runs", "--since", "1h"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, errOut := w.run(t, 0, c.args...)
			if errOut != "" {
				t.Errorf("standard error should be empty: %q", errOut)
			}
			for _, line := range strings.Split(out, "\n") {
				if n := len([]rune(line)); n > 80 {
					t.Errorf("a line is %d characters wide, but a table fits 80:\n%s", n, line)
				}
			}
			golden(t, c.name, out)
		})
	}
}

func TestRunsShowGolden(t *testing.T) {
	w := newRunsWorld(t)
	for _, c := range []struct {
		name, run string
	}{
		{"show-succeeded", "review"},        // merged loads, a link, friction
		{"show-failed", "autofix"},          // two friction notes at the end
		{"show-stale", "flaky"},             // a session file, a repeated load, an interactive run
		{"show-refused", "refused"},         // Fabrica's own checkpoint, no blueprint commit
		{"show-another-year", "going"},      // no routine, repo or session
		{"show-parallel", "parallel"},       // the same moment as another
		{"show-from-a-week-ago", "old"},     // an older blueprint commit
		{"show-by-whole-id", "autofix"},     // the full ID
		{"show-by-a-short-prefix", "flaky"}, // the 12 characters the table shows
	} {
		t.Run(c.name, func(t *testing.T) {
			arg := w.ids[c.run]
			if c.name == "show-by-a-short-prefix" || c.name == "show-failed" {
				arg = arg[:12]
			}
			out, errOut := w.run(t, 0, "runs", "show", arg)
			if errOut != "" {
				t.Errorf("standard error should be empty: %q", errOut)
			}
			golden(t, c.name, out)
		})
	}
}

func TestRunsShowFindsRunsByPrefix(t *testing.T) {
	w := newRunsWorld(t)
	review, parallel := w.ids["review"], w.ids["parallel"]
	if review[:14] != parallel[:14] || review[:15] == parallel[:15] {
		t.Fatalf("the two reviews should share 14 characters, and no more: %s %s", review, parallel)
	}

	// Anything that tells the runs apart works: 15 characters, more, all.
	for _, arg := range []string{review[:15], review[:20], review, strings.ToUpper(review), " " + review + " "} {
		out, _ := w.run(t, 0, "runs", "show", arg)
		if !strings.HasPrefix(out, "Run "+review+": succeeded\n") {
			t.Errorf("show %q:\n%s", arg, out)
		}
	}

	// The 12 characters of the table fit both, so it says so and lists them.
	out, errOut := w.run(t, 1, "runs", "show", review[:12])
	if out != "" {
		t.Errorf("an ambiguous prefix prints nothing to stdout: %q", out)
	}
	golden(t, "show-ambiguous", errOut)
	for _, id := range []string{review, parallel} {
		if !strings.Contains(errOut, id) {
			t.Errorf("the candidates should include %s:\n%s", id, errOut)
		}
	}
	// Even a single character is fine to ask with.
	_, errOut = w.run(t, 1, "runs", "show", "run_")
	if !strings.Contains(errOut, "fits more than 5 runs") || strings.Count(errOut, "\n  run_") != 5 || !strings.Contains(errOut, "…and more") {
		t.Errorf("a prefix of every run:\n%s", errOut)
	}

	// Unknown, empty and odd ones.
	_, errOut = w.run(t, 1, "runs", "show", "run_zzzz")
	golden(t, "show-unknown", errOut)
	for _, arg := range []string{"nope", "run_%", "' OR '1'='1", review + "x", "~"} {
		if _, errOut := w.run(t, 1, "runs", "show", arg); !strings.Contains(errOut, "there's no run starting with") {
			t.Errorf("show %q: %s", arg, errOut)
		}
	}
	if _, errOut := w.run(t, 1, "runs", "show", " "); !strings.Contains(errOut, "name a run") {
		t.Errorf("show with a blank: %s", errOut)
	}
	if _, errOut := w.run(t, 1, "runs", "show"); !strings.Contains(errOut, "accepts 1 arg") {
		t.Errorf("show with nothing: %s", errOut)
	}
}

// The RUN column is enough to tell the runs apart, and to ask `show` for
// one of them.
func TestRunsListPrefixesWork(t *testing.T) {
	w := newRunsWorld(t)
	out, _ := w.run(t, 0, "runs", "--since", "100d")
	seen := 0
	for _, line := range strings.Split(out, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[len(fields)-1], "run_") {
			continue
		}
		prefix := fields[len(fields)-1]
		seen++
		if len(prefix) < 12 {
			t.Errorf("%q is shorter than 12 characters", prefix)
		}
		shown, _ := w.run(t, 0, "runs", "show", prefix)
		if !strings.HasPrefix(shown, "Run "+prefix) {
			t.Errorf("show %s:\n%s", prefix, shown)
		}
	}
	if seen != 8 {
		t.Errorf("saw %d runs in\n%s", seen, out)
	}
}

func TestRunsExportGolden(t *testing.T) {
	w := newRunsWorld(t)
	out, _ := w.run(t, 0, "runs", "export", "--since", "30d")
	golden(t, "export-output", out)
	if !strings.Contains(out, "./fabrica-runs-20261017-1004/") {
		t.Errorf("the default folder is named after the local time:\n%s", out)
	}
	goldenTree(t, w, "fabrica-runs-20261017-1004", "export")

	// With filters and a folder of its own.
	dir := filepath.Join(t.TempDir(), "retro")
	out, _ = w.run(t, 0, "runs", "export", "--routine", "pr-review", "--outcome", "succeeded", "--since", "30d", "--out", dir)
	golden(t, "export-filtered-output", strings.ReplaceAll(out, dir, "$OUT"))
	goldenTree(t, w, dir, "export-filtered")
}

// goldenTree compares every file of a bundle to testdata/runs/<name>/, and
// checks that every file there is in the bundle. After -update, delete the
// files of a name that are gone by hand.
func goldenTree(t *testing.T, w *runsWorld, dir, name string) {
	t.Helper()
	files := func(root string) (rels []string) {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(root, p)
				rels = append(rels, strings.TrimSuffix(filepath.ToSlash(rel), ".golden"))
			}
			return err
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return rels
	}
	found := files(dir)
	for _, rel := range found {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		golden(t, name+"/"+rel, w.normalize(string(data)))
	}
	if *update {
		return
	}
	for _, rel := range files(filepath.Join(goldenDir, "runs", name)) {
		if !slices.Contains(found, rel) {
			t.Errorf("%s has a golden file for %s, but the bundle has no such file", name, rel)
		}
	}
}

func TestRunsExportRefusesAFullFolder(t *testing.T) {
	w := newRunsWorld(t)
	dir := t.TempDir()
	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut := w.run(t, 1, "runs", "export", "--out", dir)
	if out != "" || !strings.Contains(errOut, dir+" already has files in it") {
		t.Errorf("stdout %q, stderr %q", out, errOut)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("nothing should be written next to someone's files: %v", entries)
	}
	if data, _ := os.ReadFile(keep); string(data) != "mine" {
		t.Error("their file changed")
	}

	// An empty folder is fine, and so is one that isn't there.
	empty := t.TempDir()
	w.run(t, 0, "runs", "export", "--out", empty, "--since", "30d")
	if _, err := os.Stat(filepath.Join(empty, "README.md")); err != nil {
		t.Error(err)
	}
	// Again into the same folder: it has the bundle in it now.
	if _, errOut := w.run(t, 1, "runs", "export", "--out", empty); !strings.Contains(errOut, "already has files in it") {
		t.Errorf("exporting twice to one folder: %s", errOut)
	}
	// A file isn't a folder.
	if _, errOut := w.run(t, 1, "runs", "export", "--out", keep); !strings.Contains(errOut, "can't export to") {
		t.Errorf("exporting to a file: %s", errOut)
	}
	// Refusing comes before reading anything, so a server isn't asked.
	if _, errOut := w.run(t, 1, "runs", "export", "--out", dir, "--server", "https://never.invalid"); !strings.Contains(errOut, "already has files in it") {
		t.Errorf("a full folder with a server: %s", errOut)
	}
}

func TestRunsExportOfNothing(t *testing.T) {
	w := newRunsWorld(t)
	dir := filepath.Join(t.TempDir(), "retro")
	out, _ := w.run(t, 0, "runs", "export", "--factory", "nobody", "--out", dir)
	if !strings.Contains(out, "No runs found since") || !strings.Contains(out, "Nothing was exported.") {
		t.Errorf("export of nothing:\n%s", out)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("an empty bundle shouldn't be written")
	}
}

func TestRunsExportLimit(t *testing.T) {
	w := newRunsWorld(t)
	dir := filepath.Join(t.TempDir(), "retro")
	out, _ := w.run(t, 0, "runs", "export", "--limit", "2", "--out", dir)
	if !strings.Contains(out, "Exported 2 runs") || !strings.Contains(out, "More runs match than that. Raise --limit") {
		t.Errorf("export with a limit:\n%s", out)
	}
	readme, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if !strings.Contains(string(readme), "More runs match than that: this is the newest 2 of them.") {
		t.Errorf("the README should say it's not everything:\n%s", readme)
	}
	var bundle struct {
		SchemaVersion int `json:"schema_version"`
		Filters       map[string]any
		Runs          []map[string]any
	}
	data, _ := os.ReadFile(filepath.Join(dir, "runs.json"))
	if err := json.Unmarshal(data, &bundle); err != nil || len(bundle.Runs) != 2 || bundle.SchemaVersion != 4 || bundle.Filters["limit"] != 2.0 {
		t.Errorf("runs.json = %+v, %v", bundle, err)
	}
}

func TestRunsListLimitCanPassOnePage(t *testing.T) {
	w := newRunsWorld(t)
	// The admin API gives 500 runs at most in a page; --limit isn't held to that.
	big, _ := w.run(t, 0, "runs", "--limit", "5000", "--json", "--since", "100d")
	small, _ := w.run(t, 0, "runs", "--limit", "50", "--json", "--since", "100d")
	if big != small || strings.Count(big, `"run_id"`) != 8 {
		t.Errorf("--limit 5000 should list every run:\n%s", big)
	}
}

func TestRunsFlagsAreChecked(t *testing.T) {
	w := newRunsWorld(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"runs", "--outcome", "done"}, `outcome "done" isn't one of succeeded, failed, in_progress, stale`},
		{[]string{"runs", "--since", "yesterday"}, `since "yesterday" isn't a number of days or hours back`},
		{[]string{"runs", "--limit", "0"}, "limit is 0, but it has to be at least 1"},
		{[]string{"runs", "list", "--limit", "-4"}, "limit is -4"},
		{[]string{"runs", "export", "--limit", "0"}, "limit is 0"},
		{[]string{"runs", "extra"}, `unknown command "extra"`},
		{[]string{"runs", "show", "a", "b"}, "accepts 1 arg"},
		{[]string{"runs", "show", "--factory", "acme", "x"}, "unknown flag: --factory"},
		{[]string{"runs", "export", "--json"}, "unknown flag: --json"},
		{[]string{"runs", "--server", "acme"}, `--server "acme" isn't a server URL`},
		{[]string{"runs", "--server", "http://fabrica.acme.dev"}, "must be an https URL"},
		{[]string{"runs", "--server", "https://fabrica.acme.dev?x=1"}, "must be an https URL"},
	} {
		if _, errOut := w.run(t, 1, c.args...); !strings.Contains(errOut, c.want) {
			t.Errorf("fabrica %s:\n%s\nwant %q", strings.Join(c.args, " "), errOut, c.want)
		}
	}
}

func TestRunsNeedsRunHistory(t *testing.T) {
	w := newRunsWorld(t)
	empty := t.TempDir()
	for _, args := range [][]string{{"runs"}, {"runs", "show", "run_x"}, {"runs", "export"}} {
		_, errOut := w.run(t, 1, append(args, "--home", empty)...)
		if want := "there's no run history at " + filepath.Join(empty, "runs.db") + " yet"; !strings.Contains(errOut, want) {
			t.Errorf("fabrica %s:\n%s\nwant %s", strings.Join(args, " "), errOut, want)
		}
	}
	if _, err := os.Stat(filepath.Join(empty, "runs.db")); err == nil {
		t.Error("reading history shouldn't make a database")
	}
	// --home picks the database too.
	other := t.TempDir()
	db, err := runs.Open(filepath.Join(other, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	out, _ := w.run(t, 0, "runs", "--home", other)
	if !strings.Contains(out, "No runs found") {
		t.Errorf("an empty database:\n%s", out)
	}
}

// What `runs show` hints at depends on where the factory's blueprint lives,
// which the home repo knows.
func TestRunsShowHintsWhereTheBlueprintIs(t *testing.T) {
	w := newRunsWorld(t)
	review, flaky := w.ids["review"], w.ids["flaky"]

	// This machine doesn't know the factory: the path may have a folder in
	// front of it.
	out, _ := w.run(t, 0, "runs", "show", review)
	hint := "See the instructions this run was served:\n  git -C <your blueprint clone> show ee8f142:routines/pr-review.md\n  (If the blueprint lives in a subfolder of its repo, put that folder in front of the path.)\n"
	if !strings.HasSuffix(out, hint) {
		t.Errorf("an unknown factory:\n%s", out)
	}

	// The home repo has it, with a blueprint in a subfolder.
	h := t.TempDir()
	if err := os.MkdirAll(filepath.Join(h, "factories"), 0o755); err != nil {
		t.Fatal(err)
	}
	factory := "blueprint: { git: github.com/acme/mono, ref: main, path: teams/platform }\nserver: https://fabrica.acme.dev\nruntimes: [claude-code-cloud]\nroutines: [pr-review]\ntargets: [github.com/acme/app]\n"
	if err := os.WriteFile(filepath.Join(h, "factories", "acme.yaml"), []byte(factory), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := runs.Open(filepath.Join(h, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"review", "flaky"} {
		d, _ := w.db.Detail(context.Background(), w.ids[name])
		r := runs.Run{ID: d.RunID, StartedAt: d.StartedAt, Intent: d.Intent, Routine: d.Routine, Runtime: d.Runtime, Repo: d.Repo, Factory: d.Factory, BlueprintSHA: d.BlueprintSHA, ServedBy: d.ServedBy}
		if err := db.Begin(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		for _, e := range d.Timeline {
			if e.Type == runs.EventLoad {
				if err := db.Loaded(context.Background(), d.RunID, e.Kind, e.Name, e.At); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	db.Close()
	out, _ = w.run(t, 0, "runs", "show", review, "--home", h)
	if want := "See the instructions this run was served:\n  git -C <your blueprint clone> show ee8f142:teams/platform/routines/pr-review.md\n"; !strings.HasSuffix(out, want) {
		t.Errorf("a factory with a blueprint path:\n%s", out)
	}
	// An interactive run points at the skills and rules it loaded.
	out, _ = w.run(t, 0, "runs", "show", flaky, "--home", h)
	if want := "  git -C <your blueprint clone> show ee8f142:teams/platform/rules/product-knowledge-only.mdc\n  git -C <your blueprint clone> show ee8f142:teams/platform/skills/debugging/SKILL.md\n"; !strings.HasSuffix(out, want) {
		t.Errorf("an interactive run:\n%s", out)
	}
}

// Text from agents can't do anything to a terminal or to the layout.
func TestRunsShowCleansAgentText(t *testing.T) {
	w := newRunsWorld(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC) // the newest
	id := runsTestID(at, "0000000000000009")
	err := w.db.Begin(ctx, runs.Run{ID: id, StartedAt: at, Intent: "Hello\x1b[2J\x1b]0;pwned\x07 there\n# not a heading", Routine: "pr-review", Factory: "acme",
		ServedBy: runs.ServedRemote, Session: "/tmp/x\r\n- [x](y)"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.AddCheckpoint(ctx, id, runs.Checkpoint{At: at.Add(time.Second), Author: runs.ByAgent, Status: runs.Progress, Summary: "line one\nline two\x00",
		Links: []string{"https://example.com/a\x1b[31m"}, Friction: []runs.Friction{{Kind: "other", Note: "\x1b[1mbold\x1b[0m"}}}); err != nil {
		t.Fatal(err)
	}
	out, _ := w.run(t, 0, "runs", "show", id)
	if strings.ContainsAny(out, "\x1b\x07\r\x00") {
		t.Errorf("control characters got through:\n%q", out)
	}
	if !strings.Contains(out, "Intent     Hello [2J ]0;pwned there # not a heading\n") || !strings.Contains(out, "progress   line one line two\n") {
		t.Errorf("show:\n%s", out)
	}
	list, _ := w.run(t, 0, "runs", "--routine", "pr-review", "--limit", "1")
	if strings.ContainsAny(list, "\x1b\x07\r\x00") || strings.Count(list, "\n") != 5 {
		t.Errorf("list:\n%q", list)
	}
	dir := filepath.Join(t.TempDir(), "b")
	w.run(t, 0, "runs", "export", "--limit", "1", "--out", dir)
	md, _ := os.ReadFile(filepath.Join(dir, "runs", id+".md"))
	if strings.ContainsAny(string(md), "\x1b\x07\r\x00") || strings.Contains(string(md), "\n# not a heading") || strings.Contains(string(md), "\n- [x](y)") {
		t.Errorf("the Markdown file:\n%q", md)
	}
	// JSON keeps the text as it was written, escaped.
	jsonOut, _ := w.run(t, 0, "runs", "show", id, "--json")
	var back runs.Detail
	if err := json.Unmarshal([]byte(jsonOut), &back); err != nil || !strings.Contains(back.Intent, "\x1b[2J") {
		t.Errorf("--json should keep the text: %v %q", err, back.Intent)
	}
}

func TestRunsOrderOfArguments(t *testing.T) {
	// The flags work before and after the subcommand's own words.
	w := newRunsWorld(t)
	a, _ := w.run(t, 0, "runs", "--factory", "acme", "list")
	b, _ := w.run(t, 0, "runs", "list", "--factory", "acme")
	if a != b || !strings.Contains(a, "run_") {
		t.Errorf("flags before and after list:\n%s\n%s", a, b)
	}
}

func TestRunsPrintsNothingToStderrUnlessItFails(t *testing.T) {
	w := newRunsWorld(t)
	for _, args := range [][]string{{"runs"}, {"runs", "show", w.ids["review"]}, {"runs", "--json"}} {
		if _, errOut := w.run(t, 0, args...); errOut != "" {
			t.Errorf("fabrica %s wrote to stderr: %q", strings.Join(args, " "), errOut)
		}
	}
}

func TestRunsHelp(t *testing.T) {
	w := newRunsWorld(t)
	for _, args := range [][]string{{"runs", "--help"}, {"runs", "list", "--help"}, {"runs", "show", "--help"}, {"runs", "export", "--help"}} {
		out, _ := w.run(t, 0, args...)
		for _, flag := range []string{"--server"} {
			if !strings.Contains(out, flag) {
				t.Errorf("fabrica %s should mention %s:\n%s", strings.Join(args, " "), flag, out)
			}
		}
	}
	out, _ := w.run(t, 0, "--help")
	if !strings.Contains(out, "runs ") {
		t.Errorf("fabrica --help should list runs:\n%s", out)
	}
}
