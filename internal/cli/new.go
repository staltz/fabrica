package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/paths"
	"github.com/staltz/fabrica/internal/product"
	"github.com/staltz/fabrica/internal/scaffold"
)

const gettingStartedURL = "https://github.com/staltz/fabrica/blob/main/docs/getting-started.md"

func newNew() *cobra.Command {
	var empty, noGit bool
	cmd := &cobra.Command{
		Use:   "new <folder>",
		Short: "Start a new blueprint repo in a folder",
		Long: `New writes a new blueprint into a folder (creating it if needed) and runs
git init there, so you get a blueprint repo that's ready to edit and commit.

The blueprint is a copy of Fabrica's starter blueprint, built into this
binary: skills, routines and rules for writing the test first, keeping
changes small and reviewing every pull request, plus GitHub and Slack
connectors. It works as it is, and it's yours to change. With --empty,
you get a blueprint with nothing in it yet: a fabrica.yaml with every key
commented out, and a README.

New never overwrites a file: if the folder already has any file it would
write, it writes nothing. When the folder is inside a git repo already, it
doesn't run git init, since a blueprint can live in a subfolder of a repo.
It never commits for you either. Look the files over, then commit them.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			dir, err := filepath.Abs(target)
			if err != nil {
				return err
			}
			tmpl := scaffold.Starter
			if empty {
				tmpl = scaffold.Empty
			}
			written, err := scaffold.Blueprint(dir, tmpl)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			// A new blueprint has no imports, so loading it never needs git.
			f, diags := blueprint.NewLoader(nil).LoadDir(ctx, dir, blueprint.LoadOptions{})
			if empty {
				fmt.Fprintf(out, "Created an empty blueprint in %s.\n", dir)
			} else {
				fmt.Fprintf(out, "Created a blueprint in %s, from Fabrica's starter blueprint.\n", dir)
				if f != nil {
					fmt.Fprintf(out, "  %s\n", itemCounts(f))
				}
			}
			if !slices.Contains(written, "README.md") {
				fmt.Fprintln(out, "  kept the README.md that was already there")
			}
			if errs, _ := printDiags(out, diags, false); errs > 0 {
				return &ExitError{Code: 1}
			}

			// Work out the repo the blueprint lives in, for git init and for
			// the factory to suggest.
			repo, top, branch, subpath := "", "", "", ""
			if !noGit {
				repo, top, _ = product.RepoName(ctx, dir)
				var wt gitx.Worktree
				switch {
				case top == "":
					if wt, err = gitx.Init(ctx, dir); err != nil {
						return fmt.Errorf("the blueprint is written, but git init failed: %w", err)
					}
					top = wt.Dir
					fmt.Fprintln(out, "Initialized a new git repo there.")
				case paths.Same(top, dir):
					wt = gitx.Worktree{Dir: top}
					fmt.Fprintln(out, "It's at the root of a git repo that was already there.")
				default:
					wt = gitx.Worktree{Dir: top}
					rel, _ := filepath.Rel(top, paths.Canonical(dir))
					subpath = filepath.ToSlash(rel)
					fmt.Fprintf(out, "It's inside the git repo at %s, so there's no new repo.\n", top)
				}
				branch, _ = wt.Branch(ctx)
			}
			if repo == "" {
				name := dir
				if top != "" {
					name = top
				}
				repo = "github.com/you/" + filepath.Base(name)
			}
			if branch == "" {
				branch = "main"
			}

			fmt.Fprintln(out, "\nNext:")
			if cwd, err := os.Getwd(); err != nil || !paths.Same(cwd, dir) {
				fmt.Fprintf(out, "  cd %s\n", target)
			}
			if empty {
				fmt.Fprintln(out, "  # add skills, routines and rules; the README says where each one goes")
			}
			fmt.Fprintln(out, "  fabrica check")
			if !noGit {
				fmt.Fprintln(out, `  git add . && git commit -m "Start a blueprint"`)
			}
			factory := fmt.Sprintf("git: %s, ref: %s", repo, branch)
			if subpath != "" {
				factory += ", path: " + subpath
			}
			fmt.Fprintf(out, "\nThen push it somewhere git can reach, and deploy it from a home repo with:\n\n  blueprint: { %s }\n\n", factory)
			fmt.Fprintf(out, "Getting started walks you through it: %s\n", gettingStartedURL)
			return nil
		},
	}
	cmd.Flags().BoolVar(&empty, "empty", false, "start from an empty blueprint instead of the starter blueprint")
	cmd.Flags().BoolVar(&noGit, "no-git", false, "don't run git init")
	return cmd
}

// itemCounts sums up what's in a blueprint, like "5 skills, 3 routines, 4
// rules, 2 connectors, 0 providers".
func itemCounts(f *blueprint.Blueprint) string {
	return strings.Join([]string{
		plural(len(f.Skills), "skill"), plural(len(f.Routines), "routine"), plural(len(f.Rules), "rule"),
		plural(len(f.Connectors), "connector"), plural(len(f.Providers), "provider"),
	}, ", ")
}
