package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/apply"
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
		Loader: t.loader, Git: t.git, Secret: secret,
		Binary: fabricaBinary(), UserHome: userHome(),
		SkipLocal: os.Getenv("CI") != "",
		Refresh:   true,
		Only:      only,
	}, nil
}

func newPlan(g *globals) *cobra.Command {
	var asJSON, noRefresh bool
	cmd := &cobra.Command{
		Use:   "plan [deployment...]",
		Short: "Show what apply would do, and what stops it",
		Long: `Plan compares what your home repo and its factories ask for with what's
installed, for every deployment (or the ones you name), and shows:

  - creates, updates and deletes, with the reason for each
  - how each runtime handles each trigger, connector and side effect
    (native, emulated or unsupported), and its apply mode
  - whether each deployment follows a branch or is frozen
  - pending handoffs, and resources Fabrica can't verify
  - errors that block apply

It reads the home repo as it is on disk, so you can plan your changes
before you push them. Exit codes: 0 when everything is in sync, 2 when
there are only changes, 1 when there are errors. Running it on a schedule
in the home repo's CI catches problems that appear after an apply.

Set FABRICA_SERVER_SECRET to also catch rotated tokens. In CI (when $CI is
set), local deployments are skipped.`,
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
	return cmd
}

func newApply(g *globals) *cobra.Command {
	var handoffDir string
	cmd := &cobra.Command{
		Use:   "apply [deployment...]",
		Short: "Make runtimes match your home repo",
		Long: `Apply reconciles your deployments with their runtimes.

For each cloud deployment with changes and no errors, it takes the
deployment's lock (a commit to its state file, pushed to the home repo),
looks up the deployment's resources in each runtime by identity, re-plans,
applies each change (through the runtime's API, or by writing handoff
instructions for a person or a computer-use agent to follow), and then
records the results and releases the lock in one commit. Cloud applies
need FABRICA_SERVER_SECRET, to compute each deployment's token.

For local deployments in your personal home repo, it installs the
user-level fabrica MCP server and local routine hooks in each local tool.
It never writes into a product repo.

Apply works from the latest home repo on its remote, so commit and push
your deployment changes first.`,
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
			res, err := apply.Run(ctx, apply.Options{Plan: env, Out: out, HandoffDir: handoffDir}, dir)
			if err != nil {
				return err
			}
			if len(res.Handoffs) > 0 {
				fmt.Fprintf(out, "\n%s to follow. Each one ends with the `fabrica confirm` command to run when it's done. They contain the deployment's token, so keep them private:\n", plural(len(res.Handoffs), "handoff"))
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
	return cmd
}

func newConfirm(g *globals) *cobra.Command {
	var remoteID string
	cmd := &cobra.Command{
		Use:   "confirm <handoff-id>",
		Short: "Mark a handoff as done",
		Long: `Confirm closes a pending handoff once someone has followed its
instructions. Pass --remote-id with the runtime's ID for the resource when
you have it. Without one, Fabrica tracks the resource as unverifiable: no
drift detection and no automatic deletion.

Like apply, confirm writes the deployment's state file, so it commits and
pushes to the home repo, and refuses while an apply holds the lock.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir, err := g.homeDir(ctx)
			if err != nil {
				return err
			}
			c, err := apply.Confirm(ctx, dir, args[0], remoteID, time.Now())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			p := c.Pending
			fmt.Fprintf(out, "Confirmed %s: %s %s @ %s on %s (deployment %s).\n", p.ID, p.Action, p.Routine, p.Repo, p.Runtime, c.Deployment)
			if c.Unverifiable {
				fmt.Fprintln(out, "There's no remote ID, so Fabrica tracks it as unverifiable: no drift detection and no automatic deletion.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&remoteID, "remote-id", "", "the runtime's own ID for the resource you created or updated")
	return cmd
}

func newUnlock(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "unlock <deployment>",
		Short: "Remove a lock left behind by a crashed apply",
		Long: `Fabrica can't tell a crashed apply from a slow one, so it never guesses:
a crashed apply leaves its lock in place, and plan and apply show who
holds it and since when. Once you're sure that apply is gone, unlock
removes the lock. The next apply adopts whatever the crashed one created.`,
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
				fmt.Fprintf(out, "Deployment %s wasn't locked.\n", args[0])
				return nil
			}
			fmt.Fprintf(out, "Removed the lock on %s, held by %q since %s.\n", args[0], lock.Holder, lock.Since.Format(time.RFC3339))
			return nil
		},
	}
}
