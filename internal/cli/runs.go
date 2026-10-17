package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/admin"
	"github.com/staltz/fabrica/internal/credentials"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/paths"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/runview"
	"github.com/staltz/fabrica/internal/token"
)

// runsClock is the time, and the time zone to show times in. Tests replace
// it, so what they check doesn't depend on when and where they run.
var runsClock = func() (time.Time, *time.Location) { return time.Now(), time.Local }

// Defaults for how many runs the commands read, and the width of the table.
const (
	runsListLimit   = 50
	runsExportLimit = 200
	runsWidth       = 80
)

// runsFlags are the flags of one `fabrica runs` command.
type runsFlags struct {
	factory, routine, repo, runtime string
	outcomes                        []string
	since                           string
	limit                           int
	server                          string
	asJSON                          bool
	out                             string
}

// filterFlags adds the flags that say which runs to read, and --server.
func (o *runsFlags) filterFlags(cmd *cobra.Command, limit int, what string) {
	f := cmd.Flags()
	f.StringVar(&o.factory, "factory", "", "only runs of this factory")
	f.StringVar(&o.routine, "routine", "", "only runs of this routine; - means interactive runs, the ones with no routine")
	f.StringVar(&o.repo, "repo", "", "only runs on this repo, like github.com/acme/app")
	f.StringVar(&o.runtime, "runtime", "", "only runs on this runtime, like claude-code-cloud")
	f.StringSliceVar(&o.outcomes, "outcome", nil, "only runs with this outcome: succeeded, failed, in_progress or stale (repeat the flag for several)")
	f.StringVar(&o.since, "since", "7d", "only runs that started since then: 7d, 48h, or an RFC 3339 time")
	f.IntVar(&o.limit, "limit", limit, "the most runs "+what)
	o.serverFlag(cmd)
}

func (o *runsFlags) serverFlag(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.server, "server", "", "read a remote server's run history instead of this machine's: its URL, or the name of a factory in your home repo")
}

// filter turns the flags into a filter. The limit can be more than one page
// of the admin API, since runs.ListAll reads as many pages as it takes.
func (o *runsFlags) filter(now time.Time) (runs.Filter, error) {
	f := runs.Filter{Factory: o.factory, Routine: o.routine, Repo: o.repo, Runtime: o.runtime, Outcomes: o.outcomes}
	if err := f.Validate(); err != nil {
		return f, err
	}
	if o.limit < 1 {
		return f, fmt.Errorf("limit is %d, but it has to be at least 1", o.limit)
	}
	f.Limit = o.limit
	var err error
	f.Since, err = runs.ParseSince(o.since, now)
	return f, err
}

func newRuns(g *globals) *cobra.Command {
	list, show, export := &runsFlags{}, &runsFlags{}, &runsFlags{}
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Read run history: list runs, show one, or export a retro bundle",
		Long: `Runs reads run history: every routine firing, and every session in your
editor, that an agent recorded with run_begin. Agents write run history and
only people read it, so this is for you and never for them.

  fabrica runs                list the latest runs (same as runs list)
  fabrica runs show <run>     one run in full: what it loaded, reported and
                              found slow
  fabrica runs export         write a retro bundle: a folder of Markdown and
                              JSON to hand to your own retro skill

It reads runs.db in your home repo, the history of your local sessions. With
--server it reads a remote Fabrica server instead, through the server's admin
API: give it the server's URL, or the name of a factory in your home repo
(it uses that factory's server:). That takes the server's admin token, from
FABRICA_ADMIN_TOKEN, or else made from the server secret that
` + "`fabrica auth login`" + ` saved. ` + "`fabrica auth admin-token`" + ` prints it. Local
and remote history are never mixed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runsList(cmd, g, list) },
	}
	list.filterFlags(cmd, runsListLimit, "to list")
	cmd.Flags().BoolVar(&list.asJSON, "json", false, "print JSON, the same as the admin API's")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List the latest runs, newest first (the default)",
		Long: `List shows runs newest first: when each started, how it went, which routine
and repo it was for, what the agent said it was doing, and enough of its ID
to hand to ` + "`fabrica runs show`" + `. Times are in your time zone, and long text
is cut to fit an 80-column terminal.`,
		Example: `  fabrica runs --since 3d
  fabrica runs --routine pr-review --outcome failed --outcome stale
  fabrica runs --routine -                   # interactive sessions
  fabrica runs --server https://fabrica.acme.dev --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runsList(cmd, g, list) },
	}
	list.filterFlags(listCmd, runsListLimit, "to list")
	listCmd.Flags().BoolVar(&list.asJSON, "json", false, "print JSON, the same as the admin API's")

	showCmd := &cobra.Command{
		Use:   "show <run>",
		Short: "Show one run in full: its timeline of loads, checkpoints and friction",
		Long: `Show prints one run: what it was for, then everything that happened in it
in time order. Give the whole run ID, or the start of it, like the RUN
column of ` + "`fabrica runs`" + ` shows. A start that fits several runs lists them.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runsShow(cmd, g, show, args[0]) },
	}
	show.serverFlag(showCmd)
	showCmd.Flags().BoolVar(&show.asJSON, "json", false, "print JSON, the same as the admin API's")

	exportCmd := &cobra.Command{
		Use:   "export",
		Short: "Write a retro bundle: a folder of Markdown and JSON about a set of runs",
		Long: `Export writes a retro bundle for the runs that match the filters: a folder
you can hand to a person, or to your own retro skill (Fabrica doesn't ship
one). It has a README.md with totals and pointers, runs.json with every run in
full, and runs/<run_id>.md for each run, the same as ` + "`fabrica runs show`" + `.
Transcripts aren't copied into it; it only points at them. See
manual/run-history.md for the layout.

It won't write into a folder that has files in it.`,
		Example: `  fabrica runs export --routine pr-review --since 14d
  fabrica runs export --server acme --out ./retro`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runsExport(cmd, g, export) },
	}
	export.filterFlags(exportCmd, runsExportLimit, "to export")
	exportCmd.Flags().StringVar(&export.out, "out", "", "the folder to write (default ./fabrica-runs-<YYYYMMDD-HHMM>/)")

	cmd.AddCommand(listCmd, showCmd, exportCmd)
	return cmd
}

// runsSource is where `fabrica runs` reads from: this machine's run history,
// or a remote server's admin API.
type runsSource interface {
	runs.Lister
	Detail(ctx context.Context, runID string) (*runs.Detail, error)
	Details(ctx context.Context, runIDs []string) ([]*runs.Detail, error)
}

// localRuns is this machine's run history.
type localRuns struct{ *runs.DB }

func (l localRuns) Details(ctx context.Context, runIDs []string) ([]*runs.Detail, error) {
	out := make([]*runs.Detail, 0, len(runIDs))
	for _, id := range runIDs {
		d, err := l.Detail(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// runsOrigin says where a source is, for messages and for the bundle.
type runsOrigin struct {
	server string // the remote server's address; empty for this machine
	path   string // this machine's run history, a file
	token  string // where the admin token came from, like "from the environment variable …"
}

func (o runsOrigin) String() string {
	if o.server != "" {
		return o.server
	}
	return "the run history on this machine"
}

// wrap adds where a failure happened to an error from the source, and says
// what to do about the ones a person can fix.
func (o runsOrigin) wrap(err error) error {
	switch {
	case err == nil || o.server == "":
		return err
	case admin.IsStatus(err, 401):
		return fmt.Errorf("%s refused the admin token (%s); it has to be the one made from that server's secret, which `fabrica auth admin-token %s` prints", o.server, o.token, o.server)
	case admin.IsStatus(err, 404):
		return fmt.Errorf("%s has no admin API there; check that the address is the server's own, and that it runs a Fabrica that has one", o.server)
	}
	return fmt.Errorf("%s: %w", o.server, err)
}

// openRuns opens the run history the --server flag points at, which is this
// machine's when it's empty. Close what it returns when done.
func openRuns(ctx context.Context, g *globals, server string) (runsSource, runsOrigin, func(), error) {
	if server == "" {
		path, err := g.runsDB()
		if err != nil {
			return nil, runsOrigin{}, nil, err
		}
		// Reading history shouldn't make an empty database, least of all
		// in a folder that was a typo.
		if _, err := os.Stat(path); err != nil {
			return nil, runsOrigin{}, nil, fmt.Errorf("there's no run history at %s yet; Fabrica records a run when an agent calls run_begin, so it fills up as you use agents", path)
		}
		db, err := runs.Open(path)
		if err != nil {
			return nil, runsOrigin{}, nil, err
		}
		return localRuns{db}, runsOrigin{path: path}, func() { db.Close() }, nil
	}
	address, err := runsServer(ctx, g, server)
	if err != nil {
		return nil, runsOrigin{}, nil, err
	}
	tok, from, err := runsAdminToken(address)
	if err != nil {
		return nil, runsOrigin{}, nil, err
	}
	return admin.NewClient(address, tok), runsOrigin{server: address, token: from}, func() {}, nil
}

// runsDB is the local run-history database: runs.db in --home, or else in
// your personal home repo, which is where `fabrica mcp` writes it.
func (g *globals) runsDB() (string, error) {
	if g.home == "" {
		return paths.LocalRunsDB()
	}
	dir, err := filepath.Abs(g.home)
	return filepath.Join(dir, "runs.db"), err
}

// runsServer works out the address of the server --server means: a URL, or
// the name of a factory in the home repo, which has the server: it uses.
func runsServer(ctx context.Context, g *globals, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if strings.Contains(arg, "://") {
		u, err := url.Parse(arg)
		if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" ||
			(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
			return "", fmt.Errorf("--server %q must be an https URL (plain http is only allowed for localhost), like https://fabrica.acme.dev", arg)
		}
		return credentials.NormalizeServer(arg), nil
	}
	dir, err := g.homeDir(ctx)
	if err != nil {
		return "", err
	}
	h, err := home.Open(dir)
	if err != nil {
		return "", err
	}
	var cloud []string
	for _, name := range h.Names() {
		if h.Factories[name].Scope == provider.Cloud {
			cloud = append(cloud, name)
		}
	}
	d := h.Factories[arg]
	switch {
	case d == nil && len(cloud) == 0:
		return "", fmt.Errorf("--server %q isn't a server URL, and the home repo at %s has no cloud factory with that name; write the server's URL, like https://fabrica.acme.dev", arg, dir)
	case d == nil:
		return "", fmt.Errorf("--server %q isn't a server URL or a factory of the home repo at %s; write the server's URL, like https://fabrica.acme.dev, or name a factory: %s", arg, dir, strings.Join(cloud, ", "))
	case d.Server == "":
		return "", fmt.Errorf("factory %s is a local factory, so its run history is on this machine: leave out --server", arg)
	}
	return credentials.NormalizeServer(d.Server), nil
}

// runsAdminToken finds the admin token for a server, and says where it came
// from. FABRICA_ADMIN_TOKEN comes first; if it's set, even to nothing,
// nothing else is asked. Otherwise the token is made from the server
// secret, the way `fabrica auth admin-token` does.
func runsAdminToken(server string) (tok, from string, err error) {
	if v, ok := os.LookupEnv(token.AdminEnvVar); ok {
		if tok = strings.TrimSpace(v); tok == "" {
			return "", "", fmt.Errorf("%s is set to nothing; set it to the admin token of %s, or unset it", token.AdminEnvVar, server)
		}
		return tok, "from the environment variable " + token.AdminEnvVar, nil
	}
	tok, found, err := adminTokenOf(server)
	if err != nil {
		return "", "", err
	}
	if tok == "" {
		return "", "", fmt.Errorf("there's no admin token for %s: run `fabrica auth login %s` to save the server's secret, or set %s", server, server, token.AdminEnvVar)
	}
	return tok, "made from the server secret in " + found.From, nil
}

// runsView makes the view the commands show runs with.
func runsView(ctx context.Context, g *globals) runview.View {
	now, loc := runsClock()
	var once sync.Once
	var h *home.Home
	return runview.View{Now: now, Loc: loc, BlueprintPath: func(factory string) (string, bool) {
		// The blueprint's path: of a factory comes from the home repo, when
		// this machine has it.
		once.Do(func() {
			if dir, err := g.homeDir(ctx); err == nil {
				h, _ = home.Open(dir)
			}
		})
		if h == nil || h.Factories[factory] == nil {
			return "", false
		}
		return h.Factories[factory].Blueprint.Path, true
	}}
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runsList(cmd *cobra.Command, g *globals, o *runsFlags) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	now, _ := runsClock()
	f, err := o.filter(now)
	if err != nil {
		return err
	}
	src, origin, closeSrc, err := openRuns(ctx, g, o.server)
	if err != nil {
		return err
	}
	defer closeSrc()
	list, next, err := runs.ListAll(ctx, src, f)
	if err != nil {
		return origin.wrap(err)
	}
	if o.asJSON {
		return printJSON(out, runs.NewPage(list, next))
	}
	if view := runsView(ctx, g); len(list) == 0 {
		view.Empty(out, f.Since)
	} else {
		view.List(out, list, next != "", runsWidth)
	}
	return nil
}

func runsShow(cmd *cobra.Command, g *globals, o *runsFlags, arg string) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	src, origin, closeSrc, err := openRuns(ctx, g, o.server)
	if err != nil {
		return err
	}
	defer closeSrc()
	view := runsView(ctx, g)
	id, err := resolveRun(ctx, src, origin, view, arg)
	if err != nil {
		return err
	}
	d, err := src.Detail(ctx, id)
	if err != nil {
		return origin.wrap(err)
	}
	if o.asJSON {
		return printJSON(out, d)
	}
	_, err = io.WriteString(out, view.Show(d))
	return err
}

// maxCandidates is how many runs an ambiguous ID lists.
const maxCandidates = 5

// resolveRun finds the run an ID, or the start of one, means. IDs sort by
// time, so the runs that start with the prefix are the ones right below
// "prefix~" (~ comes after every character an ID has), and one list call
// finds them, wherever the runs are.
func resolveRun(ctx context.Context, src runsSource, origin runsOrigin, view runview.View, arg string) (string, error) {
	prefix := strings.ToLower(strings.TrimSpace(arg))
	if prefix == "" {
		return "", errors.New("name a run: its ID, or the start of it, like the RUN column of `fabrica runs` shows")
	}
	list, _, err := src.List(ctx, runs.Filter{Before: prefix + "~", Limit: maxCandidates + 1})
	if err != nil {
		return "", origin.wrap(err)
	}
	var matches []runs.Summary
	for _, s := range list {
		if !strings.HasPrefix(s.RunID, prefix) {
			break
		}
		matches = append(matches, s)
	}
	switch {
	case len(matches) == 0:
		return "", fmt.Errorf("there's no run starting with %q in %s; `fabrica runs` lists the latest ones", arg, origin)
	case len(matches) == 1 || matches[0].RunID == prefix:
		return matches[0].RunID, nil
	}
	more := len(matches) > maxCandidates
	fits := fmt.Sprintf("%d runs", len(matches))
	if more {
		fits = fmt.Sprintf("more than %d runs", maxCandidates)
	}
	return "", fmt.Errorf("%q fits %s; use more of the ID to pick one:\n%s", arg, fits,
		strings.TrimRight(view.Candidates(matches[:min(len(matches), maxCandidates)], more, runsWidth), "\n"))
}

func runsExport(cmd *cobra.Command, g *globals, o *runsFlags) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	now, loc := runsClock()
	f, err := o.filter(now)
	if err != nil {
		return err
	}
	dir := o.out
	if dir == "" {
		dir = "./fabrica-runs-" + now.In(loc).Format("20060102-1504") + "/"
	}
	// Before reading anything: a folder that has files in it is no place
	// for a bundle.
	if err := runview.CheckDir(dir); err != nil {
		return err
	}
	src, origin, closeSrc, err := openRuns(ctx, g, o.server)
	if err != nil {
		return err
	}
	defer closeSrc()
	list, next, err := runs.ListAll(ctx, src, f)
	if err != nil {
		return origin.wrap(err)
	}
	view := runsView(ctx, g)
	if len(list) == 0 {
		view.Empty(out, f.Since)
		fmt.Fprintln(out, "Nothing was exported.")
		return nil
	}
	ids := make([]string, len(list))
	for i, s := range list {
		ids[i] = s.RunID
	}
	details, err := src.Details(ctx, ids)
	if err != nil {
		return origin.wrap(err)
	}
	err = view.WriteBundle(dir, runview.Bundle{Server: origin.server, LocalPath: origin.path, Filter: f, More: next != "", Runs: details})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Exported %s (%s) to %s\n", plural(len(details), "run"), runview.Counts(details), strings.TrimSuffix(dir, "/")+"/")
	if next != "" {
		fmt.Fprintln(out, "More runs match than that. Raise --limit to export them.")
	}
	return nil
}
