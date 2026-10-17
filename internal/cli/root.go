// Package cli is the `fabrica` command line.
//
// Fabrica sits beside git and never wraps it. Blueprint and home repos are
// plain git repos that you edit and review with your usual git and PR
// workflow. The CLI only ever writes to git in one place: committing and
// pushing state in a home repo.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/mcpserver"
	"github.com/staltz/fabrica/internal/paths"
)

// ExitError carries a specific exit code out of a command. `fabrica plan`
// uses it to exit with 2 when there are changes.
type ExitError struct {
	Code int
	// Err is printed when set. Plan's exit code 2 has nothing to print.
	Err error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit code %d", e.Code)
	}
	return e.Err.Error()
}

// Main runs the CLI and returns the process exit code.
func Main(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ensurePersonalHome(ctx, stderr)
	mcpserver.Version, _ = readVersion()
	root := newRoot()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		if exit.Err != nil {
			fmt.Fprintf(stderr, "fabrica: %v\n", exit.Err)
		}
		return exit.Code
	}
	fmt.Fprintf(stderr, "fabrica: %v\n", err)
	return 1
}

// ensurePersonalHome creates your personal home repo before any command
// runs, unless it's there already. Failing to is only a warning: most
// commands don't need it, and the ones that do say so themselves. That's
// also why it says nothing when there's no home folder to put it in.
func ensurePersonalHome(ctx context.Context, stderr io.Writer) {
	dir, created, err := home.EnsurePersonal(ctx)
	switch {
	case created:
		fmt.Fprintf(stderr, "Created your personal home repo at %s.\n", dir)
	case err != nil && dir != "":
		fmt.Fprintf(stderr, "fabrica: couldn't create your personal home repo at %s: %v\n", dir, err)
	}
}

// globals are the flags every command shares.
type globals struct {
	home string
}

func newRoot() *cobra.Command {
	g := &globals{}
	var update bool
	root := &cobra.Command{
		Use:   "fabrica",
		Short: "Write down how your AI agents build software, once, and run it on many AI tools",
		Long: `Fabrica lets you write down how your AI agents build software (skills,
routines, rules) once, in a git repo called a blueprint, and run that same
setup on many AI coding tools: Claude, Cursor, Codex, GitHub Copilot and
more.

Deploying a blueprint makes a factory: a file in a home repo that says
which product repos (its targets) and tools the blueprint runs on.
Agents load the blueprint live from the Fabrica MCP server, so improving
your process is just a git merge. Every agent run is recorded.

Typical workflow:
  fabrica new        start a new blueprint repo
  fabrica check      lint a blueprint, a home repo or a product repo
  fabrica lock       pin a blueprint's imports in fabrica.lock
  fabrica auth       check the credentials apply needs (auth login sets them up)
  fabrica plan       see what apply would do (exit 0 in sync, 2 changes, 1 errors)
  fabrica apply      install stubs in runtimes, and Fabrica in your local tools
  fabrica destroy    remove everything apply installed

Run fabrica -u to update Fabrica itself to the latest release.`,
		Version:       versionLine(),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if update {
				return selfUpdate(cmd)
			}
			return cmd.Help()
		},
	}
	// Cobra's own -v says "version for fabrica" and prints "fabrica version
	// …", unlike `fabrica version`. Make both match.
	root.SetVersionTemplate("{{.Version}}\n")
	root.Flags().BoolP("version", "v", false, "print Fabrica's version")
	root.Flags().BoolVarP(&update, "update", "u", false, "update fabrica to the latest release")
	root.PersistentFlags().StringVar(&g.home, "home", "", "the home repo to use (default: the current folder if it's a home repo, otherwise $FABRICA_HOME or ~/.fabrica)")
	root.AddCommand(
		newNew(),
		newCheck(g),
		newLock(),
		newUpgrade(),
		newAuth(g),
		newPlan(g),
		newApply(g),
		newDestroy(g),
		newConfirm(g),
		newUnlock(g),
		newMCP(g),
		newDashboard(g),
		newHook(g),
		newVersion(),
	)
	return root
}

// homeDir works out which home repo a command is about: --home, else the
// current folder (or its git repo) when it looks like a home repo, else
// your personal home repo.
func (g *globals) homeDir(ctx context.Context) (string, error) {
	if g.home != "" {
		return filepath.Abs(g.home)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	candidates := []string{cwd}
	if wt, err := gitx.OpenWorktree(ctx, cwd); err == nil {
		candidates = append(candidates, wt.Dir)
	}
	for _, dir := range candidates {
		if isHomeRepo(dir) {
			return dir, nil
		}
	}
	return paths.PersonalHome()
}

func isHomeRepo(dir string) bool {
	for _, sub := range []string{home.FactoriesDir, home.StateDir} {
		if info, err := os.Stat(filepath.Join(dir, sub)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// tools are the shared helpers commands need.
type tools struct {
	cacheDir string
	git      *gitx.Cache
	loader   *blueprint.Loader
}

func newTools() (*tools, error) {
	dir, err := paths.Cache()
	if err != nil {
		return nil, err
	}
	cache := gitx.NewCache(dir)
	return &tools{cacheDir: dir, git: cache, loader: blueprint.NewLoader(cache)}, nil
}

// fabricaBinary is the path local tools should run. We prefer the path on
// your PATH (often a symlink that stays put across upgrades) when it's the
// same program that's running now.
func fabricaBinary() string {
	if b := os.Getenv("FABRICA_BIN"); b != "" {
		return b
	}
	exe, err := os.Executable()
	if err != nil {
		return "fabrica"
	}
	if p, err := exec.LookPath("fabrica"); err == nil {
		if abs, err := filepath.Abs(p); err == nil && sameFile(abs, exe) {
			return abs
		}
	}
	return exe
}

func sameFile(a, b string) bool {
	ia, err1 := os.Stat(a)
	ib, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ia, ib)
}

// printDiags prints diagnostics, errors first, and returns the number of
// errors and warnings.
func printDiags(w io.Writer, diags diag.List, withNotes bool) (errs, warns int) {
	for _, d := range diags.Dedup().Sorted() {
		if d.Severity == diag.Note && !withNotes {
			continue
		}
		fmt.Fprintln(w, indentContinuation(d.String()))
		switch d.Severity {
		case diag.Error:
			errs++
		case diag.Warning:
			warns++
		}
	}
	return errs, warns
}

func indentContinuation(s string) string {
	return strings.ReplaceAll(s, "\n", "\n  ")
}

func countSummary(errs, warns int) string {
	switch {
	case errs == 0 && warns == 0:
		return "✓ No problems found."
	case errs == 0:
		return fmt.Sprintf("✓ No errors (%s).", plural(warns, "warning"))
	}
	return fmt.Sprintf("✗ %s, %s.", plural(errs, "error"), plural(warns, "warning"))
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// isTerminal reports whether r is an interactive terminal, so commands can
// ask before doing something.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
