package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/auth"
	"github.com/staltz/fabrica/internal/credentials"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/token"
)

func newAuth(g *globals) *cobra.Command {
	var noCheck bool
	status := func(cmd *cobra.Command, _ []string) error { return authStatus(cmd, g, noCheck) }
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Set up and check the credentials apply needs",
		Long: `Auth shows every credential your home repo's factories need for
` + "`fabrica apply`" + `, and checks each one with the service it's for:

  - the secret of each Fabrica server your cloud factories use (the
    server checks the tokens apply makes with it),
  - each runtime API's credentials, like a Cursor API key,
  - git access to the home repo, the blueprints and the product repos.

` + "`fabrica auth login`" + ` sets up whatever is missing: it makes server secrets,
opens the page where you make an API key and checks the key before
saving it, and signs git in to GitHub through the GitHub CLI. Credentials
are saved in your system's keychain (the macOS Keychain, or the Secret
Service on Linux), or else in a file only you can read. An environment
variable, like FABRICA_SERVER_SECRET or CURSOR_API_KEY, always wins over
anything saved, so CI keeps working the way it did.

Exits 1 when something apply needs is missing or refused.`,
		Args: cobra.NoArgs,
		RunE: status,
	}
	cmd.Flags().BoolVar(&noCheck, "no-check", false, "don't ask servers, runtime APIs or git whether the credentials work")
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show the credentials apply needs, and whether each works (the default)",
		Args:  cobra.NoArgs,
		RunE:  status,
	}
	statusCmd.Flags().BoolVar(&noCheck, "no-check", false, "don't ask servers, runtime APIs or git whether the credentials work")
	cmd.AddCommand(statusCmd, newAuthLogin(g), newAuthLogout(g), newAuthEnv(g), newAuthAdminToken(g))
	return cmd
}

// authRequirements works out what the home repo needs.
func authRequirements(ctx context.Context, g *globals) (*auth.Requirements, error) {
	dir, err := g.homeDir(ctx)
	if err != nil {
		return nil, err
	}
	h, err := home.Open(dir)
	if err != nil {
		return nil, err
	}
	t, err := newTools()
	if err != nil {
		return nil, err
	}
	return auth.Find(ctx, &auth.Env{Git: t.git, Loader: t.loader}, h), nil
}

func homeKind(h *home.Home) string {
	if h.Personal {
		return "personal home repo"
	}
	return "team home repo"
}

func authStatus(cmd *cobra.Command, g *globals, noCheck bool) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	reqs, err := authRequirements(ctx, g)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Home repo %s (%s)\n", reqs.Home.Dir, homeKind(reqs.Home))
	if len(reqs.Needs) == 0 && len(reqs.Repos) == 0 && len(reqs.Problems) == 0 {
		fmt.Fprintln(out, "\nIt has no factories yet, so apply needs nothing.")
		return nil
	}

	problems, optional := 0, 0
	section := ""
	for _, n := range reqs.Needs {
		title := "Server secrets"
		if n.Kind == auth.RuntimeCredential {
			title = "Runtime credentials"
		}
		if title != section {
			fmt.Fprintf(out, "\n%s\n", title)
			section = title
		}
		s := auth.Check(ctx, n, !noCheck)
		mark, detail := describeStatus(s)
		switch {
		case s.Problem():
			problems++
		case !s.Set():
			optional++
		}
		fmt.Fprintf(out, "  %s %s: %s\n    %s\n", mark, n.Name, n.Title(), detail)
	}

	switch {
	case len(reqs.Repos) > 0 && noCheck:
		fmt.Fprintln(out, "\nGit access (not checked)")
		for _, r := range reqs.Repos {
			fmt.Fprintf(out, "  - %s: %s\n", r.Name, strings.Join(r.Why, ", "))
		}
	case len(reqs.Repos) > 0:
		fmt.Fprintln(out, "\nGit access")
		for _, a := range auth.CheckRepos(ctx, reqs.Repos) {
			mark := "✓"
			if a.Err != nil {
				mark = "✗"
				problems++
			}
			fmt.Fprintf(out, "  %s %s: %s\n", mark, a.Repo.Name, strings.Join(a.Repo.Why, ", "))
			if a.Err != nil {
				fmt.Fprintf(out, "    git can't read it: %s\n", auth.GitProblem(a.Err))
			}
		}
	}
	if len(reqs.Problems) > 0 {
		fmt.Fprintln(out, "\nCouldn't work out everything")
		for _, p := range reqs.Problems {
			fmt.Fprintf(out, "  ✗ %s\n", p)
			problems++
		}
	}

	fmt.Fprintln(out)
	switch {
	case problems > 0:
		fmt.Fprintf(out, "✗ %s. Run `fabrica auth login` to fix what Fabrica can.\n", plural(problems, "problem"))
		return &ExitError{Code: 1}
	case optional > 0:
		fmt.Fprintf(out, "✓ Apply has everything it needs. `fabrica auth login` can also set up the optional %s above.\n", map[bool]string{true: "credential", false: "credentials"}[optional == 1])
	default:
		fmt.Fprintln(out, "✓ Apply has everything it needs.")
	}
	return nil
}

// describeStatus is the mark and the line under a credential's name.
func describeStatus(s *auth.Status) (mark, detail string) {
	n := s.Need
	switch {
	case s.Err != nil:
		return "✗", "couldn't read it: " + s.Err.Error()
	case !s.Set() && s.Found.Env:
		mark = "·"
		if n.Required() {
			mark = "✗"
		}
		return mark, fmt.Sprintf("%s is set to nothing in your environment, so %s", n.EnvVar(), n.Without())
	case !s.Set():
		mark = "·"
		if n.Required() {
			mark = "✗"
		}
		return mark, "not set, so " + n.Without()
	}
	from := "saved in " + s.Found.From
	if s.Found.Env {
		from = "from " + s.Found.From
	}
	if s.Detail != "" {
		from += "; " + s.Detail
	}
	switch s.Verdict {
	case auth.Rejected:
		return "✗", from
	case auth.Unreachable:
		return "!", from
	}
	return "✓", from
}

// resolveNeeds turns names from the command line into credentials: a
// server's URL, an environment variable, or a runtime. "git" is left out,
// for the caller.
func resolveNeeds(reqs *auth.Requirements, names []string) ([]*auth.Need, bool, error) {
	var out []*auth.Need
	git := false
	seen := map[*auth.Need]bool{}
	for _, name := range names {
		if name == "git" {
			git = true
			continue
		}
		found := reqs.Lookup(name)
		if len(found) == 0 {
			found = auth.Standalone(name)
		}
		if len(found) == 0 {
			return nil, false, fmt.Errorf("there's no credential called %q; name a server's URL (like https://fabrica.acme.dev), an environment variable (like CURSOR_API_KEY), a runtime (like cursor-cloud) or git; `fabrica auth` lists what this home repo needs", name)
		}
		for _, n := range found {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out, git, nil
}

func newAuthLogin(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "login [credential...]",
		Short: "Set up the credentials apply needs, and save them in your keychain",
		Long: `Login sets up the credentials apply needs: every one that's missing or
refused, or the ones you name. Name a server's URL, an environment
variable (like CURSOR_API_KEY), a runtime (like cursor-cloud), or git.

  - A server secret: paste the one your Fabrica server uses (Fabrica asks
    the server whether it's right), or press Enter to make a new one, for
    a new server. ` + "`fabrica auth env`" + ` then hands it to the server and CI.
  - A runtime credential: press Enter to open the page where you make
    one, then paste it. Fabrica checks it with the runtime where it can.
  - git: when git can't read a repo on GitHub, login signs you in to
    GitHub in your browser with the GitHub CLI (gh), and lets git use it.

Credentials are saved in your system's keychain where Fabrica can use
one (the macOS Keychain, or the Secret Service on Linux), or else in a
file only you can read. On a terminal, what you paste isn't shown.
Without one, answers are read from stdin, one per line, so
` + "`echo \"$KEY\" | fabrica auth login CURSOR_API_KEY`" + ` works in scripts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			reqs, err := authRequirements(ctx, g)
			if err != nil {
				return err
			}
			l := &login{ctx: ctx, ui: newPrompter(cmd.InOrStdin(), cmd.OutOrStdout()), in: cmd.InOrStdin(), out: cmd.OutOrStdout(), errOut: cmd.ErrOrStderr(), reqs: reqs, named: len(args) > 0}
			needs, git := reqs.Needs, true
			if len(args) > 0 {
				if needs, git, err = resolveNeeds(reqs, args); err != nil {
					return err
				}
			}
			return l.run(needs, git)
		},
	}
}

// login walks you through setting up credentials.
type login struct {
	ctx         context.Context
	ui          *prompter
	in          io.Reader
	out, errOut io.Writer
	reqs        *auth.Requirements
	// named is true when you named what to set up, so it's set up again
	// even when it works already.
	named bool
	// savedForHome and newSecrets are what this login saved, for the
	// summary.
	savedForHome bool
	newSecrets   []string
}

// errStop means there's no more input, so login stops where it is.
var errStop = errors.New("stopped: there's no more input")

func (l *login) run(needs []*auth.Need, git bool) error {
	fmt.Fprintf(l.out, "Setting up credentials for home repo %s.\n", l.reqs.Home.Dir)
	switch {
	case credentials.InKeychain():
		fmt.Fprintf(l.out, "Fabrica saves them in %s.\n", credentials.Where())
	case strings.EqualFold(os.Getenv(credentials.KeychainEnv), "off"):
		fmt.Fprintf(l.out, "Fabrica saves them in %s, since %s=off.\n", credentials.Where(), credentials.KeychainEnv)
	default:
		fmt.Fprintf(l.out, "Fabrica saves them in %s, since it found no keychain it can use here (on Linux, that's the Secret Service, through secret-tool).\n", credentials.Where())
	}
	if len(needs) == 0 && (!git || len(l.reqs.Repos) == 0) {
		fmt.Fprintln(l.out, "\nThis home repo's factories need no credentials, and no git access.")
		return nil
	}
	for _, n := range needs {
		var err error
		if n.Kind == auth.ServerSecret {
			err = l.server(n)
		} else {
			err = l.runtime(n)
		}
		if errors.Is(err, errStop) {
			fmt.Fprintln(l.out, "\nStopped: there's no more input.")
			return &ExitError{Code: 1}
		}
		if err != nil {
			return err
		}
	}
	if git && len(l.reqs.Repos) > 0 {
		if err := l.git(); errors.Is(err, errStop) {
			fmt.Fprintln(l.out, "\nStopped: there's no more input.")
			return &ExitError{Code: 1}
		} else if err != nil {
			return err
		}
	}
	l.summary()
	return nil
}

// current reports whether a credential is set up and works, printing so,
// so login can skip it.
func (l *login) current(n *auth.Need) bool {
	if l.named {
		return false
	}
	s := auth.Check(l.ctx, n, true)
	if !s.Set() || s.Verdict == auth.Rejected {
		return false
	}
	_, detail := describeStatus(s)
	fmt.Fprintf(l.out, "\n✓ %s: %s\n  %s\n", n.Name, n.Title(), detail)
	return true
}

// envNote says when an environment variable will win over what's saved.
func (l *login) envNote(n *auth.Need) {
	names := []string{n.EnvVar()}
	if n.Kind == auth.RuntimeCredential {
		names = n.Credential.Names()
	}
	for _, name := range names {
		if _, ok := os.LookupEnv(name); ok {
			fmt.Fprintf(l.out, "  Note: %s is set in your environment, and it wins over anything saved. Unset it to use what you save now.\n", name)
			return
		}
	}
}

func (l *login) server(n *auth.Need) error {
	if l.current(n) {
		return nil
	}
	fmt.Fprintf(l.out, "\n%s: %s\n", n.Name, n.Title())
	fmt.Fprintln(l.out, "  Your Fabrica server and `fabrica apply` share this secret: apply makes each factory's token with it, and the server checks every token with it.")
	l.envNote(n)
	probe := auth.ProbeServer(l.ctx, n)
	switch {
	case probe.Running():
		fmt.Fprintf(l.out, "  The server is running factory %s, so it has a secret already.\n", probe.Factory)
	case probe.Err != nil:
		fmt.Fprintf(l.out, "  Fabrica can't check a secret with the server right now: %v.\n", probe.Err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		value, err := l.ui.secret("  Paste the secret your server uses, or press Enter to make a new one: ")
		if err != nil {
			return errStop
		}
		if value == "" {
			if probe.Running() {
				ok, err := l.ui.yes("  A new secret only works once the server has it too, and then every stub of its factories needs `fabrica apply` again, with new tokens. Make a new one anyway? [y/N] ", false)
				if err != nil {
					return errStop
				}
				if !ok {
					continue
				}
			}
			if value, err = token.NewSecret(); err != nil {
				return err
			}
			if err := l.save(n, value, "Made a new secret"); err != nil {
				return err
			}
			l.newSecrets = append(l.newSecrets, n.Server)
			return nil
		}
		if err := checkValue(value); err != nil {
			fmt.Fprintf(l.out, "  ✗ That won't do: %v.\n", err)
			continue
		}
		if probe.Running() {
			verdict, detail := auth.VerifyServerSecret(l.ctx, n, value)
			switch verdict {
			case auth.Rejected:
				fmt.Fprintf(l.out, "  ✗ No: %s.\n", detail)
				continue
			case auth.Accepted:
				fmt.Fprintf(l.out, "  ✓ Yes: %s.\n", detail)
			default:
				fmt.Fprintf(l.out, "  Couldn't check it: %s.\n", detail)
			}
		}
		return l.save(n, value, "")
	}
	fmt.Fprintf(l.out, "  Skipped %s after three tries.\n", n.Name)
	return nil
}

// checkValue says whether a pasted server secret can be one.
func checkValue(value string) error {
	if err := credentials.Check(value); err != nil {
		return err
	}
	if err := token.Check(value); err != nil {
		return fmt.Errorf("it %v", err)
	}
	return nil
}

func (l *login) runtime(n *auth.Need) error {
	if l.current(n) {
		return nil
	}
	c := n.Credential
	fmt.Fprintf(l.out, "\n%s: %s\n", n.Name, n.Title())
	if len(n.Runtimes) > 0 {
		fmt.Fprintf(l.out, "  Without it, %s.\n", n.Without())
	}
	l.envNote(n)
	if !n.Required() && !l.named {
		ok, err := l.ui.yes("  Set it up now? [Y/n] ", true)
		if err != nil {
			return errStop
		}
		if !ok {
			fmt.Fprintln(l.out, "  Skipped.")
			return nil
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		question := "  Paste it: "
		if c.URL != "" {
			question = fmt.Sprintf("  Paste it, or press Enter to open %s and make one there: ", c.URL)
		}
		value, err := l.ui.secret(question)
		if err != nil {
			return errStop
		}
		if value == "" && c.URL != "" {
			if auth.OpenBrowser(c.URL) {
				fmt.Fprintln(l.out, "  Opened it in your browser.")
			} else {
				fmt.Fprintf(l.out, "  Open this page in your browser: %s\n", c.URL)
			}
			if c.Instructions != "" {
				fmt.Fprintf(l.out, "  %s\n", c.Instructions)
			}
			if value, err = l.ui.secret("  Then paste it here (or press Enter to skip): "); err != nil {
				return errStop
			}
		}
		if value == "" {
			fmt.Fprintln(l.out, "  Skipped.")
			return nil
		}
		if err := credentials.Check(value); err != nil {
			fmt.Fprintf(l.out, "  ✗ That won't do: %v.\n", err)
			continue
		}
		verdict, detail := auth.Verify(l.ctx, n, value)
		switch verdict {
		case auth.Rejected:
			fmt.Fprintf(l.out, "  ✗ That didn't work: %s\n", detail)
			continue
		case auth.Accepted:
			fmt.Fprintf(l.out, "  ✓ %s.\n", capitalize(detail))
		case auth.Unreachable:
			fmt.Fprintf(l.out, "  Couldn't check it: %s\n", detail)
		}
		return l.save(n, value, "")
	}
	fmt.Fprintf(l.out, "  Skipped %s after three tries.\n", n.Name)
	return nil
}

// save saves a credential and says where.
func (l *login) save(n *auth.Need, value, what string) error {
	where, err := credentials.Set(n.Key(), value)
	if err != nil {
		return err
	}
	for _, need := range l.reqs.Needs {
		l.savedForHome = l.savedForHome || need == n
	}
	if what == "" {
		fmt.Fprintf(l.out, "  ✓ Saved in %s.\n", where)
	} else {
		fmt.Fprintf(l.out, "  ✓ %s, and saved it in %s.\n", what, where)
	}
	return nil
}

func (l *login) git() error {
	fmt.Fprintln(l.out, "\nGit access")
	var failing []auth.Repo
	for _, a := range auth.CheckRepos(l.ctx, l.reqs.Repos) {
		if a.Err != nil {
			failing = append(failing, a.Repo)
			fmt.Fprintf(l.out, "  ✗ git can't read %s (%s): %s\n", a.Repo.Name, strings.Join(a.Repo.Why, ", "), auth.GitProblem(a.Err))
		}
	}
	if len(failing) == 0 {
		fmt.Fprintf(l.out, "  ✓ git can read all %s apply uses.\n", plural(len(l.reqs.Repos), "repo"))
		return nil
	}
	var github, others []auth.Repo
	for _, r := range failing {
		if r.OnGitHub() {
			github = append(github, r)
		} else {
			others = append(others, r)
		}
	}
	if len(github) > 0 {
		if err := l.github(github); err != nil {
			return err
		}
	}
	if len(others) > 0 {
		fmt.Fprintln(l.out, "  Fabrica runs git, so give git access to the rest: set up a credential helper or an SSH key that can read them, then run `fabrica auth` to check.")
	}
	return nil
}

// github fixes git's access to repos on github.com with the GitHub CLI.
func (l *login) github(repos []auth.Repo) error {
	gh := auth.FindGitHubCLI()
	switch {
	case gh == nil:
		fmt.Fprintln(l.out, "  The easiest fix for GitHub is its CLI: install it from https://cli.github.com, then run `fabrica auth login git` again. It signs you in in your browser, and lets git use that sign-in. An SSH key or a credential helper works too.")
		return nil
	case !l.ui.terminal():
		fmt.Fprintln(l.out, "  To fix it, run `gh auth login --web` and then `gh auth setup-git` in a terminal (or `fabrica auth login git`, which runs them for you).")
		return nil
	}
	signedIn := gh.SignedIn(l.ctx)
	question := "  Sign in to GitHub with the GitHub CLI? It opens your browser, then lets git use that sign-in. [Y/n] "
	if signedIn {
		question = "  You're signed in to the GitHub CLI, but git isn't using that sign-in. Let it? This runs `gh auth setup-git`, which adds gh as git's credential helper for github.com. [Y/n] "
	}
	ok, err := l.ui.yes(question, true)
	if err != nil {
		return errStop
	}
	if !ok {
		fmt.Fprintln(l.out, "  Skipped.")
		return nil
	}
	if !signedIn {
		if err := gh.Login(l.ctx, l.in, l.out, l.errOut); err != nil {
			fmt.Fprintf(l.out, "  ✗ gh auth login didn't work: %v\n", err)
			return nil
		}
	}
	if err := gh.SetupGit(l.ctx, l.in, l.out, l.errOut); err != nil {
		fmt.Fprintf(l.out, "  ✗ gh auth setup-git didn't work: %v\n", err)
		return nil
	}
	for _, a := range auth.CheckRepos(l.ctx, repos) {
		if a.Err != nil {
			fmt.Fprintf(l.out, "  ✗ git still can't read %s: %s. Check that your GitHub account can see it.\n", a.Repo.Name, auth.GitProblem(a.Err))
		} else {
			fmt.Fprintf(l.out, "  ✓ git can read %s now.\n", a.Repo.Name)
		}
	}
	return nil
}

// summary says what's next: where new secrets need to go, and how to
// give CI the same credentials.
func (l *login) summary() {
	fmt.Fprintln(l.out)
	for _, server := range l.newSecrets {
		fmt.Fprintf(l.out, "Your server at %s needs the same secret, in its FABRICA_SERVER_SECRET. `fabrica auth env` prints it, ready to use:\n", server)
		fmt.Fprintf(l.out, "  fabrica auth env %s > fabrica.env    # then: docker run --env-file fabrica.env …\n", server)
	}
	if l.savedForHome {
		if repo := l.reqs.GitHubRepo(); repo != "" {
			fmt.Fprintf(l.out, "To give the home repo's GitHub Actions the same credentials:\n  fabrica auth env | gh secret set --env-file - --repo %s\n", repo)
		}
	}
	fmt.Fprintln(l.out, "Run `fabrica auth` to check everything, and `fabrica plan` to see what apply would do.")
}

func newAuthLogout(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "logout <credential>...",
		Short: "Forget saved credentials",
		Long: `Logout forgets credentials that ` + "`fabrica auth login`" + ` saved: name a
server's URL, an environment variable (like CURSOR_API_KEY), or a
runtime (like cursor-cloud). It doesn't touch your environment
variables, and Fabrica keeps no git credentials (git and gh do).`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			reqs, err := authRequirements(cmd.Context(), g)
			if err != nil {
				return err
			}
			needs, git, err := resolveNeeds(reqs, args)
			if err != nil {
				return err
			}
			if git {
				return errors.New("Fabrica keeps no git credentials: git and the GitHub CLI do. To sign git out of GitHub, run `gh auth logout`")
			}
			for _, n := range needs {
				where, err := credentials.Delete(n.Key())
				if err != nil {
					return err
				}
				if len(where) == 0 {
					fmt.Fprintf(out, "%s wasn't saved.\n", n.Name)
				} else {
					fmt.Fprintf(out, "Forgot %s (it was in %s).\n", n.Name, strings.Join(where, " and "))
				}
				if _, ok := os.LookupEnv(n.EnvVar()); ok {
					fmt.Fprintf(out, "%s is still set in your environment, so Fabrica still uses that.\n", n.EnvVar())
				}
			}
			return nil
		},
	}
}

func newAuthEnv(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "env [credential...]",
		Short: "Print credentials as NAME=value lines, for your server or CI",
		Long: `Env prints the credentials this home repo needs (or the ones you name)
as environment variable lines, NAME=value, the way a .env file has them.
That's the format docker's --env-file, ` + "`gh secret set --env-file -`" + ` and
most hosting platforms read, so you can pipe them where they go:

  fabrica auth env | gh secret set --env-file - --repo acme/home
  fabrica auth env https://fabrica.acme.dev > fabrica.env

A server secret comes out as FABRICA_SERVER_SECRET, which holds one
secret, so when the factories use several servers with different
secrets, name the one you want. Credentials that aren't set up are left
out, and the ones you named are an error.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			reqs, err := authRequirements(cmd.Context(), g)
			if err != nil {
				return err
			}
			needs := reqs.Needs
			if len(args) > 0 {
				var git bool
				if needs, git, err = resolveNeeds(reqs, args); err != nil {
					return err
				}
				if git {
					return errors.New("Fabrica keeps no git credentials, so there are none to print")
				}
			}
			values := map[string]string{}
			from := map[string]string{}
			for _, n := range needs {
				found, err := n.Lookup()
				if err != nil {
					return err
				}
				if found.Value == "" {
					if len(args) > 0 {
						return fmt.Errorf("%s isn't set up; run `fabrica auth login %s`", n.Name, n.Name)
					}
					fmt.Fprintf(errOut, "Leaving out %s: it isn't set up.\n", n.Name)
					continue
				}
				name := n.EnvVar()
				if v, ok := values[name]; ok && v != found.Value {
					return fmt.Errorf("%s and %s have different secrets, but %s holds one; name the server you want, like `fabrica auth env %s`", from[name], n.Name, name, n.Name)
				}
				values[name], from[name] = found.Value, n.Name
			}
			if len(values) == 0 {
				return errors.New("there are no credentials to print; `fabrica auth` shows what this home repo needs")
			}
			if isTerminalWriter(out) {
				fmt.Fprintln(errOut, "These are secrets: pipe them where they go, rather than copying them around.")
			}
			names := make([]string, 0, len(values))
			for name := range values {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				fmt.Fprintf(out, "%s=%s\n", name, values[name])
			}
			return nil
		},
	}
}

func newAuthAdminToken(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "admin-token [server-url]",
		Short: "Print a server's admin token, which lets people read its run history",
		Long: `Admin-token prints the admin token of a Fabrica server. It unlocks the
server's admin API and its dashboard, where people read run history, and
nothing else: it can't make factory tokens, and it doesn't work as one.

The admin token is computed from the server secret, so there's nothing new
to keep: rotating the server secret rotates it too. Fabrica finds the
secret the way ` + "`fabrica auth`" + ` does: FABRICA_SERVER_SECRET, or else the one
` + "`fabrica auth login <url>`" + ` saved for that server.

Name the server's URL, like https://fabrica.acme.dev. Without one, it's the
only server your home repo's cloud factories use (and with several, you
name one).

The token is the only thing printed, so it's easy to pipe where it goes.
Give it to whoever should read run history. They can set it as
FABRICA_ADMIN_TOKEN, without ever having the server secret.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			server, err := adminTokenServer(cmd.Context(), g, args)
			if err != nil {
				return err
			}
			tok, found, err := adminTokenOf(server)
			if err != nil {
				return err
			}
			switch {
			case tok == "" && found.Env:
				return fmt.Errorf("%s is set to nothing in your environment, so there's no server secret to make the admin token with", token.EnvVar)
			case tok == "":
				return fmt.Errorf("there's no server secret for %s; run `fabrica auth login %s` to save the one your server uses, or set %s", server, server, token.EnvVar)
			}
			if isTerminalWriter(out) {
				fmt.Fprintln(errOut, "This is a secret: whoever has it can read your run history. Pipe it where it goes, rather than copying it around.")
			}
			fmt.Fprintln(out, tok)
			return nil
		},
	}
}

// adminTokenOf makes the admin token of the server at server from the server
// secret Fabrica has for it (FABRICA_SERVER_SECRET, or the one `fabrica auth
// login` saved), and says where that secret came from. The token is empty
// when there's no secret.
func adminTokenOf(server string) (tok string, found credentials.Found, err error) {
	found, err = credentials.ServerSecret(server)
	if err != nil || found.Value == "" {
		return "", found, err
	}
	return token.Admin(found.Value), found, nil
}

// adminTokenServer works out which server `fabrica auth admin-token` is
// about: the one named, or the only one the home repo's cloud factories
// use. It's empty when FABRICA_SERVER_SECRET is set, because that one
// secret counts for every server.
func adminTokenServer(ctx context.Context, g *globals, args []string) (string, error) {
	if len(args) == 1 {
		if !strings.Contains(args[0], "://") {
			return "", fmt.Errorf("%q isn't a server URL; name one like https://fabrica.acme.dev", args[0])
		}
		return credentials.NormalizeServer(args[0]), nil
	}
	if _, ok := os.LookupEnv(token.EnvVar); ok {
		return "", nil
	}
	dir, err := g.homeDir(ctx)
	if err != nil {
		return "", err
	}
	h, err := home.Open(dir)
	if err != nil {
		return "", err
	}
	var servers []string
	for _, name := range h.Names() {
		d := h.Factories[name]
		if server := credentials.NormalizeServer(d.Server); d.Scope == provider.Cloud && !slices.Contains(servers, server) {
			servers = append(servers, server)
		}
	}
	slices.Sort(servers)
	switch len(servers) {
	case 0:
		return "", errors.New("this home repo's factories use no Fabrica server; name one, like `fabrica auth admin-token https://fabrica.acme.dev`")
	case 1:
		return servers[0], nil
	}
	return "", fmt.Errorf("this home repo's factories use %d servers (%s); name the one you want, like `fabrica auth admin-token %s`", len(servers), strings.Join(servers, ", "), servers[0])
}

// isTerminalWriter reports whether w is an interactive terminal.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
