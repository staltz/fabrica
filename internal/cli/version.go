package cli

import (
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// version overrides the version Go stamps from git tags. Set it at build
// time with -ldflags "-X …/cli.version=v0.1.0", for builds made outside a
// git checkout.
var version string

// pseudoVersion matches the versions Go makes up for untagged commits, like
// v0.0.0-20261010123419-b79bb17c9bce or v0.1.1-0.20261010123419-b79bb17c9bce.
var pseudoVersion = regexp.MustCompile(`[-.]\d{14}-[0-9a-f]{12}$`)

// buildVersion works out Fabrica's version, like "0.1.0", or "dev" between
// releases, and the commit it was built from, like "b79bb17 2026-10-10",
// when Go recorded one.
func buildVersion(override string, info *debug.BuildInfo) (ver, commit string) {
	ver = override
	if info == nil {
		info = &debug.BuildInfo{}
	}
	if ver == "" {
		ver = info.Main.Version
	}
	// Uncommitted changes show up on the commit, not the version.
	ver = strings.TrimSuffix(ver, "+dirty")
	if ver == "" || ver == "(devel)" || pseudoVersion.MatchString(ver) {
		ver = "dev"
	}
	ver = strings.TrimPrefix(ver, "v")

	var rev, at string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			at = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return ver, ""
	}
	commit = rev[:min(len(rev), 7)]
	if dirty {
		commit += "-dirty"
	}
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		commit += " " + t.UTC().Format(time.DateOnly)
	}
	return ver, commit
}

func readVersion() (ver, commit string) {
	info, _ := debug.ReadBuildInfo()
	return buildVersion(version, info)
}

// versionLine is what `fabrica version` and `fabrica --version` print, like
// "fabrica 0.1.0 (b79bb17 2026-10-10)".
func versionLine() string {
	ver, commit := readVersion()
	if commit == "" {
		return "fabrica " + ver
	}
	return fmt.Sprintf("fabrica %s (%s)", ver, commit)
}

func newVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print Fabrica's version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), versionLine())
		},
	}
}
