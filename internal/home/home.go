package home

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/paths"
	"github.com/staltz/fabrica/internal/provider"
)

// Folder names inside a home repo.
const (
	DeploymentsDir = "deployments"
	StateDir       = "state"
)

// Home is a home repo, read from disk.
type Home struct {
	Dir string
	// Personal is true for your personal home repo ($FABRICA_HOME or
	// ~/.fabrica). Only the personal home repo may have local deployments.
	Personal    bool
	Deployments map[string]*Deployment
	// StateNames lists every deployment that has a state file, including
	// deployments whose file was deleted (their resources need cleaning
	// up).
	StateNames []string
	// Diags are problems found while reading, including problems that
	// involve several deployments.
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
	h := &Home{Dir: abs, Personal: paths.Same(abs, personal), Deployments: map[string]*Deployment{}}

	entries, err := os.ReadDir(filepath.Join(abs, DeploymentsDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if errors.Is(err, fs.ErrNotExist) {
		if _, statErr := os.Stat(abs); statErr != nil {
			return nil, fmt.Errorf("there's no home repo at %s; create it (git init) and add a deployments/ folder", abs)
		}
	}
	for _, e := range entries {
		file := DeploymentsDir + "/" + e.Name()
		switch {
		case e.IsDir():
			h.Diags.Warnf("home.stray-file", file, "is a folder; deployments are deployments/<name>.yaml files, so Fabrica ignores it")
		case strings.HasSuffix(e.Name(), ".yml"):
			h.Diags.Errorf("deployment.extension", file, "deployment files use the .yaml extension").
				Hintf("rename it to deployments/%s.yaml", strings.TrimSuffix(e.Name(), ".yml"))
		case !strings.HasSuffix(e.Name(), ".yaml"):
			h.Diags.Warnf("home.stray-file", file, "isn't a deployment, so Fabrica ignores it")
		default:
			data, err := os.ReadFile(filepath.Join(abs, DeploymentsDir, e.Name()))
			if err != nil {
				h.Diags.Errorf("deployment.unreadable", file, "%v", err)
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".yaml")
			d, diags := ParseDeployment(name, file, data, h.Personal)
			h.Diags = append(h.Diags, diags...)
			if d != nil {
				h.Deployments[name] = d
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
	return h, nil
}

// Names lists the deployments, sorted.
func (h *Home) Names() []string { return sortedNames(h.Deployments) }

// checkLocalOverlap enforces that every session gets exactly one factory:
// at most one wildcard deployment per local runtime, and never two
// specific local deployments for the same repo and runtime.
func (h *Home) checkLocalOverlap() diag.List {
	var diags diag.List
	wildcards := map[string][]string{} // runtime → deployments
	specific := map[[2]string][]string{}
	for _, name := range h.Names() {
		d := h.Deployments[name]
		if d.Scope != provider.Local {
			continue
		}
		for _, rt := range d.Runtimes {
			if d.IsWildcard() {
				wildcards[rt] = append(wildcards[rt], name)
				continue
			}
			for _, repo := range d.SpecificRepos() {
				specific[[2]string{repo, rt}] = append(specific[[2]string{repo, rt}], name)
			}
		}
	}
	for rt, ds := range wildcards {
		if len(ds) > 1 {
			diags.Errorf("home.wildcard-overlap", DeploymentsDir, "deployments %s all use the wildcard repos: [\"*\"] for runtime %s; there can be at most one", strings.Join(ds, " and "), rt).
				Hintf("keep the wildcard in one of them and list specific repos in the others")
		}
	}
	for key, ds := range specific {
		if len(ds) > 1 {
			diags.Errorf("home.local-overlap", DeploymentsDir, "deployments %s both cover %s on %s; a session must get exactly one factory", strings.Join(ds, " and "), key[0], key[1]).
				Hintf("remove the repo from all but one of them")
		}
	}
	return diags.Sorted()
}

// MatchLocal finds the local deployment that serves repo on runtime. A
// deployment that names the repo beats the wildcard. When there's no
// match, or the match is ambiguous, or a deployment file is broken (it
// might have been the one covering this repo), it returns nil and the
// reason. It never guesses.
func (h *Home) MatchLocal(repo, runtime string) (*Deployment, string) {
	if broken := h.BrokenFiles(); len(broken) > 0 {
		return nil, fmt.Sprintf("your home repo (%s) has deployment files with errors (%s), so Fabrica can't tell which factory should serve this repo; run `fabrica check %s` to see what's wrong", h.Dir, strings.Join(broken, ", "), h.Dir)
	}
	var specific, wildcards []*Deployment
	for _, name := range h.Names() {
		d := h.Deployments[name]
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
		return nil, fmt.Sprintf("deployments %s all cover %s on %s, and a session must get exactly one factory; remove the repo from all but one of them", deploymentNames(specific), repo, runtime)
	case len(wildcards) == 1:
		return wildcards[0], ""
	case len(wildcards) > 1:
		return nil, fmt.Sprintf("deployments %s all use the wildcard for %s; there can be at most one", deploymentNames(wildcards), runtime)
	}
	return nil, fmt.Sprintf("no local deployment in your home repo (%s) covers %s on %s; add one to %s/, for example with `repos: [\"*\"]` and `runtimes: [%s]`", h.Dir, repo, runtime, DeploymentsDir, runtime)
}

// BrokenFiles lists deployment files that couldn't be read.
func (h *Home) BrokenFiles() []string {
	var out []string
	for _, d := range h.Diags {
		if d.Severity == diag.Error && strings.HasPrefix(d.Code, "deployment.") {
			out = append(out, d.Where)
		}
	}
	sort.Strings(out)
	return out
}

func deploymentNames(ds []*Deployment) string {
	var names []string
	for _, d := range ds {
		names = append(names, d.Name)
	}
	return strings.Join(names, " and ")
}
