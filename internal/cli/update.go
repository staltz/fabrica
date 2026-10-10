package cli

import (
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/selfupdate"
)

// updater is what `fabrica -u` runs. Tests swap it for one that doesn't
// replace the test binary.
var updater = func(cmd *cobra.Command) (*selfupdate.Updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("couldn't find the fabrica binary: %w", err)
	}
	current, _ := readVersion()
	return &selfupdate.Updater{
		RepoURL:  selfupdate.RepoURL,
		Current:  current,
		Exe:      exe,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Progress: cmd.OutOrStdout(),
	}, nil
}

// selfUpdate replaces the running binary with the latest release.
func selfUpdate(cmd *cobra.Command) error {
	u, err := updater(cmd)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	res, err := u.Update(cmd.Context())
	if err != nil {
		return err
	}
	if !res.Updated {
		fmt.Fprintf(out, "fabrica %s is already the latest version.\n", res.To)
		return nil
	}
	fmt.Fprintf(out, "Updated fabrica from %s to %s, in %s\n", res.From, res.To, res.Path)
	return nil
}
