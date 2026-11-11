package cli

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/heartbeat"
	"github.com/staltz/fabrica/internal/paths"
	"github.com/staltz/fabrica/internal/runs"
)

func newHeartbeat(g *globals) *cobra.Command {
	var watch bool
	cmd := &cobra.Command{
		Use:   "heartbeat",
		Short: "Start the local routines whose pace says they're due",
		Long: `Heartbeat runs the routines with a pace trigger of your local factories.
Each local factory with a pace beats once every pace, counted from midnight
UTC on 1 January 1970, and on each beat, heartbeat starts a session for
every routine that runs on it: a headless run of the tool, like ` + "`claude -p`" + `,
in a fresh clone of each repo, served by your local ` + "`fabrica mcp`" + `.

It starts each routine at most once per beat, however often it runs (it
records every start in runs.db), so it's made to run every minute.
` + "`fabrica apply`" + ` installs a job that does that: a launchd agent on macOS, a
systemd user timer on Linux. With --watch, it keeps running and does it
itself. Cloud factories don't need it: their Fabrica server keeps their
heartbeat.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			t, err := newTools()
			if err != nil {
				return err
			}
			homeDir := g.home
			if homeDir == "" {
				if homeDir, err = paths.PersonalHome(); err != nil {
					return err
				}
			}
			if homeDir, err = filepath.Abs(homeDir); err != nil {
				return err
			}
			db, err := runs.Open(filepath.Join(homeDir, "runs.db"))
			if err != nil {
				return err
			}
			defer db.Close()
			o := heartbeat.Options{
				HomeDir: homeDir, Binary: fabricaBinary(), Git: t.git, Loader: t.loader, Runs: db, CacheDir: t.cacheDir,
				Logger: slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil)),
			}
			if watch {
				return heartbeat.Watch(ctx, o, time.Now)
			}
			started, err := heartbeat.Tick(ctx, o, time.Now())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			failed := 0
			for _, s := range started {
				if s.Err != nil {
					failed++
					fmt.Fprintf(out, "Couldn't start %s @ %s on %s (factory %s): %v\n", s.Routine, s.Repo, s.Runtime, s.Factory, s.Err)
					continue
				}
				fmt.Fprintf(out, "Started %s @ %s on %s (factory %s, beat %s); its output goes to %s\n", s.Routine, s.Repo, s.Runtime, s.Factory, s.BeatAt.Format(time.RFC3339), s.Log)
			}
			if failed > 0 {
				return &ExitError{Code: 1, Err: fmt.Errorf("%d of %d sessions didn't start", failed, len(started))}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&watch, "watch", false, "keep running, and tick at the start of every minute")
	return cmd
}
