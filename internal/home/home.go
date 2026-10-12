package home

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/paths"
	"github.com/staltz/fabrica/internal/provider"
)

// Folder names inside a home repo.
const (
	FactoriesDir = "factories"
	StateDir     = "state"
)

// Home is a home repo, read from disk.
type Home struct {
	Dir string
	// Personal is true for your personal home repo ($FABRICA_HOME or
	// ~/.fabrica). Only the personal home repo may have local factories.
	Personal  bool
	Factories map[string]*Factory
	// StateNames lists every factory that has a state file, including
	// factories whose file was deleted (their resources need cleaning
	// up).
	StateNames []string
	// Diags are problems found while reading, including problems that
	// involve several factories.
	Diags diag.List
}

// Open reads the home repo in dir. Problems with individual files are
// collected in Diags rather than returned as an error, so one broken file
// doesn't hide the others.
func Open(dir string) (*Home, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	personal, err := paths.PersonalHome()
	if err != nil {
		return nil, err
	}
	h := &Home{Dir: abs, Personal: paths.Same(abs, personal), Factories: map[string]*Factory{}}

	entries, err := os.ReadDir(filepath.Join(abs, FactoriesDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if errors.Is(err, fs.ErrNotExist) {
		if _, statErr := os.Stat(abs); statErr != nil {
			return nil, fmt.Errorf("there's no home repo at %s; create it (git init) and add a factories/ folder", abs)
		}
	}
	for _, e := range entries {
		file := FactoriesDir + "/" + e.Name()
		switch {
		case e.IsDir():
			h.Diags.Warnf("home.stray-file", file, "is a folder; factories are factories/<name>.yaml files, so Fabrica ignores it")
		case strings.HasSuffix(e.Name(), ".yml"):
			h.Diags.Errorf("factory.extension", file, "factory files use the .yaml extension").
				Hintf("rename it to factories/%s.yaml", strings.TrimSuffix(e.Name(), ".yml"))
		case !strings.HasSuffix(e.Name(), ".yaml"):
			h.Diags.Warnf("home.stray-file", file, "isn't a factory, so Fabrica ignores it")
		default:
			data, err := os.ReadFile(filepath.Join(abs, FactoriesDir, e.Name()))
			if err != nil {
				h.Diags.Errorf("factory.unreadable", file, "%v", err)
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".yaml")
			d, diags := ParseFactory(name, file, data, h.Personal)
			h.Diags = append(h.Diags, diags...)
			if d != nil {
				h.Factories[name] = d
			}
		}
	}

	stateEntries, err := os.ReadDir(filepath.Join(abs, StateDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range stateEntries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			h.StateNames = append(h.StateNames, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	sort.Strings(h.StateNames)
	h.Diags = append(h.Diags, h.checkLocalOverlap()...)
	h.Diags = append(h.Diags, h.checkCopilotOverlap()...)
	return h, nil
}

// Names lists the factories, sorted.
func (h *Home) Names() []string { return sortedNames(h.Factories) }

// checkLocalOverlap enforces that every session gets exactly one blueprint:
// at most one wildcard factory per local runtime, and never two
// specific local factories for the same repo and runtime.
func (h *Home) checkLocalOverlap() diag.List {
	var diags diag.List
	wildcards := map[string][]string{} // runtime → factories
	specific := map[[2]string][]string{}
	for _, name := range h.Names() {
		d := h.Factories[name]
		if d.Scope != provider.Local {
			continue
		}
		for _, rt := range d.Runtimes {
			if d.IsWildcard() {
				wildcards[rt] = append(wildcards[rt], name)
				continue
			}
			for _, repo := range d.SpecificTargets() {
				specific[[2]string{repo, rt}] = append(specific[[2]string{repo, rt}], name)
			}
		}
	}
	for rt, ds := range wildcards {
		if len(ds) > 1 {
			diags.Errorf("home.wildcard-overlap", FactoriesDir, "factories %s all use the wildcard targets: [\"*\"] for runtime %s; there can be at most one", strings.Join(ds, " and "), rt).
				Hintf("keep the wildcard in one of them and list specific repos in the others")
		}
	}
	for key, ds := range specific {
		if len(ds) > 1 {
			diags.Errorf("home.local-overlap", FactoriesDir, "factories %s both cover %s on %s; a session must get exactly one blueprint", strings.Join(ds, " and "), key[0], key[1]).
				Hintf("remove the repo from all but one of them")
		}
	}
	return diags.Sorted()
}

// checkCopilotOverlap enforces that at most one factory deploys to each
// repo on copilot-cloud. Copilot reads MCP servers from the repo's
// settings, not from each automation, so a repo has room for only one
// factory's fabrica server.
func (h *Home) checkCopilotOverlap() diag.List {
	var diags diag.List
	byRepo := map[string][]string{}
	for _, name := range h.Names() {
		d := h.Factories[name]
		if d.Scope != provider.Cloud || !slices.Contains(d.Runtimes, "copilot-cloud") {
			continue
		}
		for _, repo := range d.SpecificTargets() {
			byRepo[repo] = append(byRepo[repo], name)
		}
	}
	for repo, ds := range byRepo {
		if len(ds) > 1 {
			diags.Errorf("home.copilot-overlap", FactoriesDir, "factories %s both deploy to %s on copilot-cloud, but Copilot keeps one set of MCP servers per repo, so only one of them can reach its Fabrica server", strings.Join(ds, " and "), repo).
				Hintf("remove copilot-cloud, or the repo, from all but one of them")
		}
	}
	return diags.Sorted()
}

// MatchLocal finds the local factory that serves repo on runtime. A
// factory that names the repo beats the wildcard. When there's no
// match, or the match is ambiguous, or a factory file is broken (it
// might have been the one covering this repo), it returns nil and the
// reason. It never guesses.
func (h *Home) MatchLocal(repo, runtime string) (*Factory, string) {
	if broken := h.BrokenFiles(); len(broken) > 0 {
		return nil, fmt.Sprintf("your home repo (%s) has factory files with errors (%s), so Fabrica can't tell which blueprint should serve this repo; run `fabrica check %s` to see what's wrong", h.Dir, strings.Join(broken, ", "), h.Dir)
	}
	var specific, wildcards []*Factory
	for _, name := range h.Names() {
		d := h.Factories[name]
		if d.Scope != provider.Local || !d.HasRuntime(runtime) {
			continue
		}
		switch {
		case d.Covers(repo):
			specific = append(specific, d)
		case d.IsWildcard():
			wildcards = append(wildcards, d)
		}
	}
	switch {
	case len(specific) == 1:
		return specific[0], ""
	case len(specific) > 1:
		return nil, fmt.Sprintf("factories %s all cover %s on %s, and a session must get exactly one blueprint; remove the repo from all but one of them", factoryNames(specific), repo, runtime)
	case len(wildcards) == 1:
		return wildcards[0], ""
	case len(wildcards) > 1:
		return nil, fmt.Sprintf("factories %s all use the wildcard for %s; there can be at most one", factoryNames(wildcards), runtime)
	}
	return nil, fmt.Sprintf("no local factory in your home repo (%s) covers %s on %s; add one to %s/, for example with `targets: [\"*\"]` and `runtimes: [%s]`", h.Dir, repo, runtime, FactoriesDir, runtime)
}

// BrokenFiles lists factory files that couldn't be read.
func (h *Home) BrokenFiles() []string {
	var out []string
	for _, d := range h.Diags {
		if d.Severity == diag.Error && strings.HasPrefix(d.Code, "factory.") {
			out = append(out, d.Where)
		}
	}
	sort.Strings(out)
	return out
}

func factoryNames(ds []*Factory) string {
	var names []string
	for _, d := range ds {
		names = append(names, d.Name)
	}
	return strings.Join(names, " and ")
}
