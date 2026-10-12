package cli

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/hook"
	"github.com/staltz/fabrica/internal/localtools"
	"github.com/staltz/fabrica/internal/mcpserver"
	"github.com/staltz/fabrica/internal/paths"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/token"
)

func newMCP(g *globals) *cobra.Command {
	var (
		httpMode  bool
		runtime   string
		workspace string
		listen    string
		dbPath    string
		ref       string
		publicURL string
		refresh   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve a blueprint over MCP, locally (stdio) or remotely (--http)",
		Long: `Without --http, mcp serves one session over stdio. Local tools run it as
their user-level fabrica MCP server (` + "`fabrica apply`" + ` sets that up). Each
time a session starts, it reads your personal home repo's local
factories, matches the workspace's git remote, checks the workspace,
and serves that blueprint. Runs are recorded in runs.db in your home repo.

With --http, it's the remote server for cloud runtimes. Point --home at
a home repo (a git URL or a path to a git repo) and it serves one
endpoint per cloud factory, at <server>/<factory>. It needs
FABRICA_SERVER_SECRET (each factory's token is HMAC(secret,
factory name)), read access to the home repo and the blueprints, and a
persistent folder for its run-history database (--db). It re-fetches
the home repo regularly, so merges show up shortly after they land.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			t, err := newTools()
			if err != nil {
				return err
			}
			logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))

			if httpMode {
				if g.home == "" {
					return fmt.Errorf("--http needs --home: the git URL (or path) of the home repo to serve")
				}
				secret, err := token.SecretFromEnv()
				if err != nil {
					return err
				}
				db, err := runs.Open(dbPath)
				if err != nil {
					return err
				}
				defer db.Close()
				srv, err := mcpserver.NewRemote(mcpserver.RemoteOptions{
					Home: g.home, HomeRef: ref, PublicURL: publicURL, Secret: secret,
					RefreshEvery: refresh, Git: t.git, Loader: t.loader, Runs: db, Logger: logger,
				})
				if err != nil {
					return err
				}
				if err := srv.Refresh(ctx); err != nil {
					return fmt.Errorf("couldn't read the home repo: %w", err)
				}
				go srv.RunRefresher(ctx)
				return srv.Serve(ctx, listen)
			}

			if runtime == "" {
				return fmt.Errorf("--runtime is required for a local server (one of %v); `fabrica apply` sets it for you", localtools.Runtimes())
			}
			homeDir := g.home
			if homeDir == "" {
				if homeDir, err = paths.PersonalHome(); err != nil {
					return err
				}
			}
			db, err := runs.Open(filepath.Join(homeDir, "runs.db"))
			if err != nil {
				return err
			}
			defer db.Close()
			return mcpserver.RunLocal(ctx, mcpserver.LocalOptions{
				Runtime: runtime, HomeDir: homeDir, Workspace: workspace,
				Git: t.git, Loader: t.loader, Runs: db, SkillCache: t.cacheDir,
			})
		},
	}
	f := cmd.Flags()
	f.BoolVar(&httpMode, "http", false, "serve cloud runtimes over HTTP instead of one local session over stdio")
	f.StringVar(&runtime, "runtime", "", "local mode: the tool this server runs in (claude-code-local, cursor-local, codex-cli or copilot-local)")
	f.StringVar(&workspace, "workspace", "", "local mode: serve this folder instead of asking the tool for its workspace")
	f.StringVar(&listen, "listen", ":8080", "--http: the address to listen on")
	f.StringVar(&dbPath, "db", "fabrica-runs.db", "--http: the run-history database (keep it on a persistent volume)")
	f.StringVar(&ref, "ref", "", "--http: the home repo branch to serve (default: its default branch)")
	f.StringVar(&publicURL, "url", "", "--http: only serve factories whose server: is this URL")
	f.DurationVar(&refresh, "refresh", time.Minute, "--http: how often to re-fetch the home repo and blueprints")
	return cmd
}

func newHook(g *globals) *cobra.Command {
	var o hook.Options
	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Run a local routine's hook (local tools call this; you don't need to)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			if o.HomeDir = g.home; o.HomeDir == "" {
				if o.HomeDir, err = paths.PersonalHome(); err != nil {
					return err
				}
			}
			cache, err := paths.Cache()
			if err != nil {
				return err
			}
			o.MarkerDir = filepath.Join(cache, "hooks")
			o.Stdin, o.Stdout = cmd.InOrStdin(), cmd.OutOrStdout()
			return hook.Run(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Runtime, "runtime", "", "the local runtime")
	f.StringVar(&o.Factory, "factory", "", "the local factory")
	f.StringVar(&o.Routine, "routine", "", "the routine")
	f.StringVar(&o.Fingerprint, "stub", "", "the stub fingerprint")
	f.StringVar(&o.Event, "event", "", "the trigger")
	for _, name := range []string{"runtime", "factory", "routine", "stub", "event"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
