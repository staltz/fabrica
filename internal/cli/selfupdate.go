package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/selfupdate"
)

func newSelfUpdate() *cobra.Command {
	var want string
	var check bool
	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Update the fabrica binary to the latest release",
		Long: `Self-update replaces this fabrica binary with the latest release (or the
one you pick with --version), at the same path, so the local tools that
run it keep working. It downloads the same files as install.sh, checks
them against the release's checksums, and makes sure the new binary runs
before swapping it in. If anything goes wrong, your fabrica stays as it was.

Fabrica processes that are already running, like the MCP servers your AI
tools started, keep the old version until they restart.

Set FABRICA_DOWNLOAD_URL to update from a mirror instead: a folder with
one release's fabrica-<os>-<arch> binaries and checksums.txt, the same as
for install.sh.

With --check, it only says whether there's an update. Exit codes: 0 when
you're up to date, 2 when there's an update, 1 on errors.

To move a factory's imports to newer commits, use ` + "`fabrica upgrade`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			exe, err := selfupdate.Executable()
			if err != nil {
				return err
			}
			src := selfupdate.New(version, os.Getenv(selfupdate.MirrorEnvVar))
			u, err := src.Check(ctx, exe, version, want)
			if err != nil {
				return err
			}
			switch {
			case u.Status == selfupdate.UpToDate && want != "":
				fmt.Fprintf(out, "✓ fabrica %s is already installed.\n", version)
				return nil
			case u.Status == selfupdate.UpToDate:
				fmt.Fprintf(out, "✓ fabrica %s is up to date.\n", version)
				return nil
			case u.Status == selfupdate.Ahead:
				fmt.Fprintf(out, "✓ fabrica %s is newer than the latest release, %s. To switch to that release anyway, run `fabrica self-update --version %s`.\n", version, u.Version, u.Version)
				return nil
			}

			// A mirror doesn't say which version it has until its binary runs.
			if check {
				again := "fabrica self-update"
				if want != "" {
					again += " --version " + u.Version
				}
				if u.Version != "" {
					fmt.Fprintf(out, "fabrica %s is available (you have %s). Run `%s` to install it.\n", u.Version, version, again)
				} else {
					fmt.Fprintf(out, "The fabrica at %s is different from yours (%s). Run `%s` to install it.\n", u.From, version, again)
				}
				if u.Notes != "" {
					fmt.Fprintf(out, "Release notes: %s\n", u.Notes)
				}
				return &ExitError{Code: 2}
			}

			if u.Version != "" {
				fmt.Fprintf(out, "Updating fabrica %s → %s...\n", version, u.Version)
			} else {
				fmt.Fprintf(out, "Updating fabrica %s from %s...\n", version, u.From)
			}
			got, err := src.Install(ctx, u)
			if err != nil {
				return fmt.Errorf("%w; your fabrica wasn't changed", err)
			}
			fmt.Fprintf(out, "✓ Updated %s to %s.\n", u.Exe, got)
			if u.Notes != "" {
				fmt.Fprintf(out, "Release notes: %s\n", u.Notes)
			}
			fmt.Fprintln(out, "Fabrica processes that are already running, like the MCP servers in your AI tools, keep the old version until they restart.")
			return nil
		},
	}
	cmd.Flags().StringVar(&want, "version", "", "the release to install, like v0.2.0, even if it's older (default: the latest)")
	cmd.Flags().BoolVar(&check, "check", false, "only check whether there's an update")
	return cmd
}
