package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/auth"
	"github.com/staltz/fabrica/internal/dashboard"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/paths"
	"github.com/staltz/fabrica/internal/repourl"
	"github.com/staltz/fabrica/internal/runs"
)

// openBrowser is what --open uses. Tests swap it.
var openBrowser = auth.OpenBrowser

func newDashboard(g *globals) *cobra.Command {
	var (
		port   int
		openIt bool
	)
	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Look at your run history in a web page on this machine",
		Long: `Serves a dashboard of the run history in runs.db, in your home repo, on
127.0.0.1 only, so nobody else on your network can reach it. It shows how
your routines are doing: runs per day, success rates, what refused to start,
what slowed agents down, and the PRs they touched. Click through to one
routine, or to one run.

It prints a link with a code in it. Open the link in your browser: the code
works once, and trades itself for a cookie that keeps that browser in until
you stop the dashboard. Press Ctrl-C to stop it.

The remote server has the same dashboard at <server>/_dashboard. You log in
there with the admin token that ` + "`fabrica auth admin-token`" + ` prints.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if port < 0 || port > 65535 {
				return fmt.Errorf("--port %d isn't a port number; use one from 1 to 65535, or leave it out to get a free one", port)
			}
			homeDir := g.home
			if homeDir == "" {
				var err error
				if homeDir, err = paths.PersonalHome(); err != nil {
					return err
				}
			}
			dbPath := filepath.Join(homeDir, "runs.db")
			if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("there's no run history at %s yet; Fabrica starts it the first time an agent calls run_begin in a local tool (`fabrica apply` sets those up)", dbPath)
			}
			db, err := runs.Open(dbPath)
			if err != nil {
				return err
			}
			defer db.Close()
			t, err := newTools()
			if err != nil {
				return err
			}

			code, err := newCode()
			if err != nil {
				return err
			}
			// Only this machine can connect.
			ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				if port != 0 {
					return fmt.Errorf("couldn't listen on port %d (%v); pick another with --port, or leave it out to get a free one", port, err)
				}
				return err
			}
			link := fmt.Sprintf("http://%s/_dashboard?code=%s", ln.Addr().String(), code)

			dash := dashboard.New(db, dashboard.Options{Code: code, Subject: localSubject(homeDir, t.git)})
			srv := &http.Server{Handler: rootRedirect(dash), ReadHeaderTimeout: 10 * time.Second}
			serveErr := make(chan error, 1)
			go func() { serveErr <- srv.Serve(ln) }()

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Run history from %s\n\nOpen this link in your browser (it works once):\n\n  %s\n\nPress Ctrl-C to stop.\n", dbPath, link)
			if openIt && !openBrowser(link) {
				fmt.Fprintln(cmd.ErrOrStderr(), "Couldn't open a browser here; open the link yourself.")
			}

			select {
			case err := <-serveErr:
				return err
			case <-ctx.Done():
			}
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdown); err != nil {
				_ = srv.Close()
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&port, "port", 0, "the port to listen on (default: any free one)")
	f.BoolVar(&openIt, "open", false, "open the link in your browser")
	return cmd
}

// newCode makes the one-time code that opens a local dashboard: 128 bits of
// randomness.
func newCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// rootRedirect sends someone who opened the bare address to the dashboard,
// and leaves everything else to it.
func rootRedirect(dash http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, "/_dashboard", http.StatusFound)
			return
		}
		dash.ServeHTTP(w, r)
	})
}

// localSubject finds the subject line of a commit of a local factory's
// blueprint in the git cache, for the dashboard's charts. It reads the home
// repo each time, so it follows changes to it, and says nothing when
// anything is missing.
func localSubject(homeDir string, git *gitx.Cache) func(ctx context.Context, factory, sha string) string {
	return func(ctx context.Context, factory, sha string) string {
		h, err := home.Open(homeDir)
		if err != nil || h.Factories[factory] == nil {
			return ""
		}
		subject, err := git.Remote(repourl.GitURL(h.Factories[factory].Blueprint.Git), gitx.Full).Subject(ctx, sha)
		if err != nil {
			return ""
		}
		return subject
	}
}
