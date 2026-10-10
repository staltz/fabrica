package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/factory"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/paths"
	"github.com/staltz/fabrica/internal/plan"
	"github.com/staltz/fabrica/internal/product"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/repourl"
	"github.com/staltz/fabrica/internal/token"
)

func newCheck(g *globals) *cobra.Command {
	var factorySpec string
	cmd := &cobra.Command{
		Use:   "check [folder]",
		Short: "Check a factory, a home repo or a product repo for problems",
		Long: `Check looks at a folder and works out what it is:

  - A factory (it has a fabrica.yaml): lints every item and import, and
    warns about likely mistakes, like a skill that mentions a skill you
    didn't import.
  - A home repo (it has deployments/ or state/): validates every
    deployment against its factory and the runtimes' capability matrices,
    and checks each deployed product repo's default branch.
  - Anything else is treated as a product repo: checks the Fabrica line,
    skill name clashes and unmet expects: against the factory that serves
    it (from your home repo's deployments, or --factory).

It exits with 1 when there are errors, so it works well as a pre-commit
hook or a CI step.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "."
			if len(args) == 1 {
				target = args[0]
			}
			abs, err := filepath.Abs(target)
			if err != nil {
				return err
			}
			if _, err := os.Stat(abs); err != nil {
				return fmt.Errorf("%s doesn't exist", target)
			}
			t, err := newTools()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			switch {
			case exists(filepath.Join(abs, factory.ManifestFile)):
				return checkFactory(ctx, out, t, abs)
			case isHomeRepo(abs):
				return checkHome(ctx, out, t, abs)
			default:
				return checkProduct(ctx, out, t, g, abs, len(args) == 1, factorySpec)
			}
		},
	}
	cmd.Flags().StringVar(&factorySpec, "factory", "", "for a product repo, the factory to check it against: a folder, or <git>[@<ref>]")
	return cmd
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func failIfErrors(errs int) error {
	if errs > 0 {
		return &ExitError{Code: 1}
	}
	return nil
}

func checkFactory(ctx context.Context, out io.Writer, t *tools, dir string) error {
	f, diags := t.loader.LoadDir(ctx, dir, factory.LoadOptions{})
	fmt.Fprintf(out, "Factory %s\n", dir)
	if f != nil {
		diags = append(diags, f.Lint()...)
		fmt.Fprintf(out, "  %s\n", itemCounts(f))
		for _, imp := range f.Imports {
			kind := "factory"
			if !imp.IsFactory {
				kind = "plain repo"
			}
			fmt.Fprintf(out, "  import %s: %s@%s → %s (%s)\n", imp.Spec.Alias, imp.Spec.Git, imp.Spec.Ref, gitx.Short(imp.Commit), kind)
		}
		for _, o := range f.Overrides {
			fmt.Fprintf(out, "  overrides %s from import %s\n", o.Ref, o.Alias)
		}
		if len(f.Manifest.Expects) > 0 {
			var names []string
			for _, e := range f.Manifest.Expects {
				names = append(names, e.String())
			}
			fmt.Fprintf(out, "  expects product repos to provide: %s\n", strings.Join(names, ", "))
		}
	}
	errs, warns := printDiags(out, diags, false)
	fmt.Fprintln(out, countSummary(errs, warns))
	return failIfErrors(errs)
}

func checkHome(ctx context.Context, out io.Writer, t *tools, dir string) error {
	h, err := home.Open(dir)
	if err != nil {
		return err
	}
	secret, _ := token.SecretFromEnv()
	env := &plan.Env{Loader: t.loader, Git: t.git, Secret: secret, Binary: fabricaBinary(), UserHome: userHome(), SkipLocal: false}
	p := plan.Build(ctx, env, h)
	kind := "team home repo"
	if h.Personal {
		kind = "personal home repo"
	}
	fmt.Fprintf(out, "Home repo %s (%s)\n", dir, kind)
	errs, warns := printDiags(out, p.Diags, false)
	for _, d := range p.Deployments {
		scope := "cloud"
		switch {
		case d.Spec == nil:
			scope = "deleted"
		case d.Local():
			scope = "local"
		}
		fmt.Fprintf(out, "Deployment %s (%s)\n", d.Name, scope)
		for _, n := range d.Notes {
			fmt.Fprintf(out, "  · %s\n", n)
		}
		var buf strings.Builder
		e, w := printDiags(&buf, d.Diags, false)
		errs, warns = errs+e, warns+w
		for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			if line != "" {
				fmt.Fprintf(out, "  %s\n", line)
			}
		}
		if e == 0 {
			fmt.Fprintln(out, "  ✓ ok")
		}
	}
	if p.Local != nil {
		e, w := printDiags(out, p.Local.Diags, false)
		errs, warns = errs+e, warns+w
	}
	fmt.Fprintln(out, countSummary(errs, warns))
	return failIfErrors(errs)
}

// checkProduct checks a product repo. A folder you name explicitly is
// treated as the repo's root; otherwise it's the git repo you're in.
func checkProduct(ctx context.Context, out io.Writer, t *tools, g *globals, dir string, explicit bool, factorySpec string) error {
	repo, top, repoErr := product.RepoName(ctx, dir)
	root := dir
	if !explicit && top != "" {
		root = top
	}
	if top != "" && !paths.Same(top, root) {
		repo, repoErr = "", fmt.Errorf("%s isn't the root of a git repo, so Fabrica can't tell which repo it is", root)
	}
	top = root
	ws, err := product.InspectDir(ctx, top)
	if err != nil {
		return err
	}
	ws.Repo = repo
	if repo != "" {
		fmt.Fprintf(out, "Product repo %s (%s)\n", repo, top)
	} else {
		fmt.Fprintf(out, "Product repo %s\n", top)
	}

	type target struct {
		label string
		f     *factory.Factory
		diags diag.List
	}
	var targets []target
	if factorySpec != "" {
		f, diags := loadFactorySpec(ctx, t, factorySpec)
		targets = append(targets, target{label: "factory " + factorySpec, f: f, diags: diags})
	} else {
		if repoErr != nil {
			return fmt.Errorf("%v; pass --factory to say which factory to check against", repoErr)
		}
		homeDir, err := g.homeDir(ctx)
		if err != nil {
			return err
		}
		h, err := home.Open(homeDir)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, d := range deploymentsCovering(h, repo) {
			key := d.Factory.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			f, diags := loadFactoryRef(ctx, t, d.Factory)
			targets = append(targets, target{label: fmt.Sprintf("factory %s (deployment %s)", d.Factory, d.Name), f: f, diags: diags})
		}
		if len(targets) == 0 {
			return fmt.Errorf("no deployment in %s covers %s; pass --factory to check against a factory directly", homeDir, repo)
		}
	}

	errs, warns := 0, 0
	for _, tg := range targets {
		fmt.Fprintf(out, "Against %s\n", tg.label)
		diags := tg.diags.Only(diag.Error)
		if tg.f != nil {
			diags = append(diags, product.Check(ws, tg.f)...)
		}
		var buf strings.Builder
		e, w := printDiags(&buf, diags, true)
		errs, warns = errs+e, warns+w
		for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
			if line != "" {
				fmt.Fprintf(out, "  %s\n", line)
			}
		}
	}
	fmt.Fprintln(out, countSummary(errs, warns))
	return failIfErrors(errs)
}

// deploymentsCovering finds every deployment that serves repo: cloud ones
// that list it, and the local one that matches it on each local runtime.
func deploymentsCovering(h *home.Home, repo string) []*home.Deployment {
	var out []*home.Deployment
	seen := map[string]bool{}
	for _, name := range h.Names() {
		d := h.Deployments[name]
		if d.Scope == provider.Cloud && d.Covers(repo) {
			out = append(out, d)
			seen[name] = true
		}
	}
	for _, rt := range provider.BuiltinNames() {
		if p, _ := provider.Builtin(rt); p.IsLocal() {
			if d, _ := h.MatchLocal(repo, rt); d != nil && !seen[d.Name] {
				out = append(out, d)
				seen[d.Name] = true
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// loadFactorySpec loads a factory from a folder, or from "<git>[@<ref>]".
func loadFactorySpec(ctx context.Context, t *tools, spec string) (*factory.Factory, diag.List) {
	if info, err := os.Stat(spec); err == nil && info.IsDir() {
		return t.loader.LoadDir(ctx, spec, factory.LoadOptions{})
	}
	gitSpec, ref := spec, ""
	if i := strings.LastIndex(spec, "@"); i > 0 && !strings.ContainsAny(spec[i+1:], ":/") {
		gitSpec, ref = spec[:i], spec[i+1:]
	}
	return loadFactoryRef(ctx, t, home.FactoryRef{Git: gitSpec, Ref: ref})
}

func loadFactoryRef(ctx context.Context, t *tools, ref home.FactoryRef) (*factory.Factory, diag.List) {
	remote := t.git.Remote(repourl.GitURL(ref.Git), gitx.Full)
	var commit string
	if ref.Ref == "" {
		_, c, err := remote.DefaultBranch(ctx)
		if err != nil {
			return nil, diag.AsList(err, "factory.resolve", ref.Git)
		}
		commit = c
	} else {
		resolved, err := remote.Resolve(ctx, ref.Ref)
		if err != nil {
			return nil, diag.AsList(err, "factory.resolve", ref.Git)
		}
		commit = resolved.Commit
	}
	return t.loader.LoadGit(ctx, ref.Git, commit, ref.Path)
}

func userHome() string {
	h, _ := os.UserHomeDir()
	return h
}
