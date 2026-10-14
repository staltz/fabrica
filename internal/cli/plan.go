package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/apply"
	"github.com/staltz/fabrica/internal/credentials"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/plan"
	"github.com/staltz/fabrica/internal/token"
)

// planEnv builds the planning environment from the process environment.
func planEnv(t *tools, only []string) (*plan.Env, error) {
	secret := ""
	if os.Getenv(token.EnvVar) != "" {
		s, err := token.SecretFromEnv()
		if err != nil {
			return nil, err
		}
		secret = s
	}
	return &plan.Env{
		Loader: t.loader, Git: t.git, Secret: secret, ServerSecret: savedServerSecret,
		Binary: fabricaBinary(), UserHome: userHome(),
		SkipLocal: os.Getenv("CI") != "",
		Refresh:   true,
		Only:      only,
	}, nil
}

// savedServerSecret finds a server's secret where `fabrica auth login`
// saved it, unless FABRICA_SERVER_SECRET is set (even to nothing).
func savedServerSecret(server string) (string, error) {
	found, err := credentials.ServerSecret(server)
	return found.Value, err
}

func newPlan(g *globals) *cobra.Command {
	var asJSON, noRefresh, destroy bool
	cmd := &cobra.Command{
		Use:   "plan [factory...]",
		Short: "Show what apply would do, and what stops it",
		Long: `Plan compares what your home repo and its blueprints ask for with what's
installed, for every factory (or the ones you name), and shows:

  - creates, updates and deletes, with the reason for each
  - how each runtime handles each trigger, connector and side effect
    (native, emulated or unsupported), and its apply mode
  - whether each factory follows a branch or is frozen
  - pending handoffs, and resources Fabrica can't verify
  - errors that block apply

It reads the home repo as it is on disk, so you can plan your changes
before you push them. Exit codes: 0 when everything is in sync, 2 when
there are only changes, 1 when there are errors. Running it on a schedule
in the home repo's CI catches problems that appear after an apply.

With the server secret (saved by ` + "`fabrica auth login`" + `, or in
FABRICA_SERVER_SECRET), it also catches rotated tokens. In CI (when $CI
is set), local factories are skipped.

With --destroy, it shows what ` + "`fabrica destroy`" + ` would remove instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir, err := g.homeDir(ctx)
			if err != nil {
				return err
			}
			h, err := home.Open(dir)
			if err != nil {
				return err
			}
			t, err := newTools()
			if err != nil {
				return err
			}
			env, err := planEnv(t, args)
			if err != nil {
				return err
			}
			env.Refresh = !noRefresh
			env.Destroy = destroy
			p := plan.Build(ctx, env, h)
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(p.ToJSON()); err != nil {
					return err
				}
			} else {
				p.Render(out)
			}
			if code := p.ExitCode(); code != 0 {
				return &ExitError{Code: code}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the plan as JSON")
	cmd.Flags().BoolVar(&noRefresh, "no-refresh", false, "trust state instead of asking runtime APIs what exists")
	cmd.Flags().BoolVar(&destroy, "destroy", false, "show what destroy would remove instead")
	return cmd
}

func newApply(g *globals) *cobra.Command {
	var handoffDir string
	cmd := &cobra.Command{
		Use:   "apply [factory...]",
		Short: "Make runtimes match your home repo",
		Long: `Apply reconciles your factories with their runtimes.

For each cloud factory with changes and no errors, it takes the
factory's lock (a commit to its state file, pushed to the home repo),
looks up the factory's resources in each runtime by identity, re-plans,
applies each change (through the runtime's API, or by writing handoff
instructions for a person or a computer-use agent to follow), and then
records the results and releases the lock in one commit. Cloud applies
need the secret of each factory's server, to compute its token: run
` + "`fabrica auth login`" + ` once to set that up, or set FABRICA_SERVER_SECRET.

For local factories in your personal home repo, it installs the
user-level fabrica MCP server and local routine hooks in each local tool.
It never writes into a product repo.

Apply works from the latest home repo on its remote, so commit and push
your factory changes first.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir, err := g.homeDir(ctx)
			if err != nil {
				return err
			}
			t, err := newTools()
			if err != nil {
				return err
			}
			env, err := planEnv(t, args)
			if err != nil {
				return err
			}
			if handoffDir == "" {
				handoffDir = filepath.Join(t.cacheDir, "handoffs")
			}
			out := cmd.OutOrStdout()
			res, err := apply.Run(ctx, apply.Options{Plan: env, Out: out, HandoffDir: handoffDir, SessionDir: filepath.Join(t.cacheDir, "sessions")}, dir)
			if err != nil {
				return err
			}
			if len(res.Handoffs) > 0 {
				fmt.Fprintf(out, "\n%s to follow. Each one ends with the `fabrica confirm` command to run when it's done. They contain the factory's token, so keep them private:\n", plural(len(res.Handoffs), "handoff"))
				for _, f := range res.Handoffs {
					fmt.Fprintf(out, "  %s\n", f)
				}
			}
			if len(res.Setups) > 0 {
				fmt.Fprintf(out, "\nSome of what Fabrica made through runtime APIs relies on things those APIs can't set up, so check them by hand (once, or when the file changes). They contain the factory's token, so keep them private:\n")
				for _, f := range res.Setups {
					fmt.Fprintf(out, "  %s\n", f)
				}
			}
			if res.ExitCode() != 0 {
				return &ExitError{Code: res.ExitCode()}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&handoffDir, "handoff-dir", "", "where to write handoff instructions (default: the Fabrica cache folder)")
	return cmd
}

// errDeclined means you answered no when destroy asked.
var errDeclined = errors.New("declined")

func newDestroy(g *globals) *cobra.Command {
	var handoffDir string
	var yes bool
	cmd := &cobra.Command{
		Use:   "destroy [factory...]",
		Short: "Remove everything apply installed",
		Long: `Destroy is the opposite of apply: it removes everything your factories
(or the ones you name) installed in their runtimes. It leaves the
factory files alone, so the next apply puts it all back. To stop
deploying something for good, delete its factory file and apply.

For each cloud factory, it deletes every resource in its state, the
same way apply deletes a stub that's no longer deployed: through the
runtime's API, or by writing a delete handoff for a person or a
computer-use agent to follow and confirm. Like apply, it takes the
factory's lock and records the results in one commit, pushed to the
home repo. Deleting needs no token, so FABRICA_SERVER_SECRET is optional.

For your personal home repo, it removes what Fabrica installed in your
local tools: the fabrica MCP server, connector servers, hooks and
scheduled tasks. When you name local factories and others still use the
same tool, only the named ones' hooks and scheduled tasks go.

It shows what it will remove and asks before changing anything. Pass
--yes to skip the question; without a terminal to ask on, destroy refuses
unless you do. ` + "`fabrica plan --destroy`" + ` shows the same plan and changes
nothing.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir, err := g.homeDir(ctx)
			if err != nil {
				return err
			}
			t, err := newTools()
			if err != nil {
				return err
			}
			env, err := planEnv(t, args)
			if err != nil {
				return err
			}
			env.Destroy = true
			if handoffDir == "" {
				handoffDir = filepath.Join(t.cacheDir, "handoffs")
			}
			in, out := cmd.InOrStdin(), cmd.OutOrStdout()
			approve := func(p *plan.Plan) error {
				p.Render(out)
				if !yes {
					if !isTerminal(in) {
						return &ExitError{Code: 1, Err: errors.New("not destroying anything without confirmation; review the plan above and run again with --yes")}
					}
					if !ask(in, out, "Destroy everything listed above? [y/N] ") {
						return errDeclined
					}
				}
				fmt.Fprintln(out)
				return nil
			}
			res, err := apply.Run(ctx, apply.Options{Plan: env, Out: out, HandoffDir: handoffDir, Approve: approve}, dir)
			if errors.Is(err, errDeclined) {
				fmt.Fprintln(out, "Nothing was destroyed.")
				return nil
			}
			if err != nil {
				return err
			}
			if len(res.Handoffs) > 0 {
				// Unlike apply's, delete handoffs carry no token.
				fmt.Fprintf(out, "\n%s to follow. Each one ends with the `fabrica confirm` command to run when it's done. Until then, its stub can still run, since the Fabrica server serves a factory for as long as its file is in %s/:\n", plural(len(res.Handoffs), "delete handoff"), home.FactoriesDir)
				for _, f := range res.Handoffs {
					fmt.Fprintf(out, "  %s\n", f)
				}
			}
			if res.ExitCode() != 0 {
				return &ExitError{Code: res.ExitCode()}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&handoffDir, "handoff-dir", "", "where to write handoff instructions (default: the Fabrica cache folder)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "destroy without asking")
	return cmd
}

func newConfirm(g *globals) *cobra.Command {
	var remoteID string
	var absent bool
	cmd := &cobra.Command{
		Use:   "confirm <handoff-id>",
		Short: "Mark a handoff as done",
		Long: `Confirm closes a pending handoff once someone has followed its
instructions. Pass --remote-id with the runtime's ID for the resource when
you have it. Without one, Fabrica tracks the resource as unverifiable: no
drift detection and no automatic deletion.

Confirm also closes an API create whose result was never recorded (the
answer was lost, or apply stopped, on a runtime that can't list its
resources). Pass --remote-id with the resource's ID if it exists, or
--absent if you looked and it doesn't. Until then, apply will not create
it again.

Like apply, confirm writes the factory's state file, so it commits and
pushes to the home repo, and refuses while an apply holds the lock.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir, err := g.homeDir(ctx)
			if err != nil {
				return err
			}
			c, err := apply.Confirm(ctx, dir, args[0], remoteID, absent, time.Now())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if st := c.Init; st != nil {
				fmt.Fprintf(out, "Confirmed %s: init %s @ %s on %s started (factory %s). It won't run again.\n", st.ID, st.Routine, st.Repo, st.Runtime, c.Factory)
				return nil
			}
			p := c.Pending
			if c.Absent {
				fmt.Fprintf(out, "Cleared %s: %s @ %s on %s was not created (factory %s). The next apply can create it.\n", p.ID, p.Routine, p.Repo, p.Runtime, c.Factory)
				return nil
			}
			fmt.Fprintf(out, "Confirmed %s: %s %s @ %s on %s (factory %s).\n", p.ID, p.Action, p.Routine, p.Repo, p.Runtime, c.Factory)
			if c.Unverifiable {
				fmt.Fprintln(out, "There's no remote ID, so Fabrica tracks it as unverifiable: no drift detection and no automatic deletion.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&remoteID, "remote-id", "", "the runtime's own ID for the resource you created or updated")
	cmd.Flags().BoolVar(&absent, "absent", false, "the unfinished API create did not happen; the next apply may create it")
	return cmd
}

func newUnlock(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "unlock <factory>",
		Short: "Remove a lock left behind by a crashed apply",
		Long: `Fabrica can't tell a crashed apply from a slow one, so it never guesses:
a crashed apply leaves its lock in place, and plan and apply show who
holds it and since when. Once you're sure that apply is gone, unlock
removes the lock. The next apply adopts whatever the crashed one created.
A create on a runtime that can't list resources, whose ID was never
recorded, is not adopted: plan reports it, and apply won't retry it until
you confirm whether it exists.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir, err := g.homeDir(ctx)
			if err != nil {
				return err
			}
			lock, err := apply.Unlock(ctx, dir, args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if lock == nil {
				fmt.Fprintf(out, "Factory %s wasn't locked.\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "Removed the lock on %s, held by %q since %s.\n", args[0], lock.Holder, lock.Since.Format(time.RFC3339))
			return nil
		},
	}
}
