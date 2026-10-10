package cli

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/factory"
)

func newLock() *cobra.Command {
	return &cobra.Command{
		Use:   "lock [factory-folder]",
		Short: "Pin every import of a factory to an exact commit, in fabrica.lock",
		Long: `Lock resolves each import's ref to an exact commit and writes them to
fabrica.lock. Imports that are already pinned keep their commit: locking
never moves a pin on its own (that's what ` + "`fabrica upgrade`" + ` is for).
It only writes the lock when the factory has no errors with it, like two
imports bringing the same item.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := factoryDir(args)
			if err != nil {
				return err
			}
			t, err := newTools()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			lock, changes, _, diags := t.loader.Lock(cmd.Context(), dir)
			if len(changes) > 0 {
				fmt.Fprint(out, factory.FormatChanges(changes))
			}
			errs, warns := printDiags(out, diags, false)
			if errs > 0 {
				fmt.Fprintln(out, countSummary(errs, warns))
				fmt.Fprintf(out, "%s wasn't written.\n", factory.LockFile)
				return &ExitError{Code: 1}
			}
			return writeLock(out, dir, lock)
		},
	}
}

func newUpgrade() *cobra.Command {
	var dir string
	var yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "upgrade [import-alias...]",
		Short: "Move imports to the newest commit of their ref, showing what changed first",
		Long: `Upgrade moves imports (all of them, or the ones you name) to the newest
commit of their ref, and shows a diff of every item that changed, added or
went away. It calls out changes to items you override (so you can decide
whether your version needs the same change) and new provider code (which
can run with your runtime credentials during apply).

It asks before writing fabrica.lock, unless you pass --yes.

To update the fabrica binary itself, use ` + "`fabrica self-update`" + `.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			abs, err := factoryDir([]string{dir})
			if err != nil {
				return fmt.Errorf("%w (to update fabrica itself, run `fabrica self-update`)", err)
			}
			t, err := newTools()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			lock, changes, _, diags := t.loader.Upgrade(cmd.Context(), abs, args)
			if len(changes) == 0 && !diags.HasErrors() {
				printDiags(out, diags, false)
				fmt.Fprintln(out, "Every import is already at the newest commit of its ref.")
				return nil
			}
			fmt.Fprint(out, factory.FormatChanges(changes))
			errs, warns := printDiags(out, diags, false)
			if errs > 0 {
				fmt.Fprintln(out, countSummary(errs, warns))
				fmt.Fprintf(out, "%s wasn't written: the upgraded factory has errors. Fix them (often with only: or exclude:), then upgrade again.\n", factory.LockFile)
				return &ExitError{Code: 1}
			}
			if dryRun {
				fmt.Fprintln(out, "Dry run: nothing was written.")
				return nil
			}
			if !yes {
				if !isTerminal(cmd.InOrStdin()) {
					return &ExitError{Code: 1, Err: fmt.Errorf("not writing %s without confirmation; review the diff above and run again with --yes", factory.LockFile)}
				}
				if !ask(cmd.InOrStdin(), out, "Write the new "+factory.LockFile+"? [y/N] ") {
					fmt.Fprintln(out, "Nothing was written.")
					return nil
				}
			}
			return writeLock(out, abs, lock)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "the factory folder")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "write fabrica.lock without asking")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only show what would change")
	return cmd
}

func factoryDir(args []string) (string, error) {
	dir := "."
	if len(args) > 0 && args[0] != "" {
		dir = args[0]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if !exists(filepath.Join(abs, factory.ManifestFile)) {
		return "", fmt.Errorf("there's no %s in %s, so it isn't a factory", factory.ManifestFile, abs)
	}
	return abs, nil
}

// writeLock writes fabrica.lock when it differs from what's on disk.
func writeLock(out io.Writer, dir string, lock *factory.Lock) error {
	path := filepath.Join(dir, factory.LockFile)
	existing, _ := factory.ReadLock(dir)
	switch {
	case len(lock.Imports) == 0 && !exists(path):
		fmt.Fprintln(out, "This factory has no imports, so there's nothing to lock.")
		return nil
	case existing != nil && reflect.DeepEqual(existing.Imports, lock.Imports):
		fmt.Fprintf(out, "%s is up to date.\n", factory.LockFile)
		return nil
	}
	if err := factory.WriteLock(dir, lock); err != nil {
		return err
	}
	fmt.Fprintf(out, "Wrote %s. Commit it together with %s.\n", factory.LockFile, factory.ManifestFile)
	return nil
}

func ask(in io.Reader, out io.Writer, question string) bool {
	fmt.Fprint(out, question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
