// Package product inspects product repos: the repos with your actual code
// that agents work on.
//
// A product repo stays about the product. Fabrica only asks two things of
// it, and checks both before serving it:
//
//   - its root AGENTS.md (and root CLAUDE.md, if there is one) starts with
//     the Fabrica line, and
//   - none of its own skills has the same name as a blueprint skill, unless
//     the blueprint expects the repo to provide that skill.
//
// It also reports skills the blueprint expects but the repo doesn't provide.
package product

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/fabline"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/promptblock"
	"github.com/staltz/fabrica/internal/repourl"
)

// SkillDirs are where product repos keep their own skills, for the tools
// that read them.
var SkillDirs = []string{".claude/skills", ".agents/skills", ".cursor/skills", ".codex/skills", ".github/skills"}

// files is a read-only view of a repo: either a folder on disk (your
// workspace) or a commit of a remote repo (its default branch).
type files interface {
	readFile(ctx context.Context, p string) ([]byte, error)
	listDirs(ctx context.Context, p string) ([]string, error)
}

// Workspace is what Fabrica found in a product repo.
type Workspace struct {
	// Repo is the repo's canonical name, like github.com/acme/app.
	Repo string
	// Where says what was inspected, like a folder or "main@3f2a1c9".
	Where string
	// LineProblems lists the root files that should start with the Fabrica
	// line but don't (or are missing, for AGENTS.md).
	LineProblems []string
	// Skills maps each repo-local skill name to the folders it was found in.
	Skills map[string][]string
}

// HasLine reports whether the Fabrica line is everywhere it needs to be.
func (w *Workspace) HasLine() bool { return len(w.LineProblems) == 0 }

func inspect(ctx context.Context, fsys files) (*Workspace, error) {
	ws := &Workspace{Skills: map[string][]string{}}
	agents, err := fsys.readFile(ctx, "AGENTS.md")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		ws.LineProblems = append(ws.LineProblems, "there's no root AGENTS.md")
	case err != nil:
		return nil, err
	case !fabline.StartsWithLine(agents):
		ws.LineProblems = append(ws.LineProblems, "the root AGENTS.md doesn't start with the Fabrica line")
	}
	claude, err := fsys.readFile(ctx, "CLAUDE.md")
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	case !fabline.StartsWithLine(claude):
		ws.LineProblems = append(ws.LineProblems, "the root CLAUDE.md doesn't start with the Fabrica line (Claude Code ignores AGENTS.md whenever a CLAUDE.md exists)")
	}
	for _, dir := range SkillDirs {
		names, err := fsys.listDirs(ctx, dir)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if _, err := fsys.readFile(ctx, path.Join(dir, name, "SKILL.md")); err == nil {
				ws.Skills[name] = append(ws.Skills[name], path.Join(dir, name))
			}
		}
	}
	return ws, nil
}

// InspectDir inspects a workspace on disk.
func InspectDir(ctx context.Context, dir string) (*Workspace, error) {
	ws, err := inspect(ctx, dirFiles(dir))
	if err != nil {
		return nil, err
	}
	ws.Where = dir
	return ws, nil
}

// InspectRemote inspects the default branch of a remote product repo.
func InspectRemote(ctx context.Context, cache *gitx.Cache, repo string) (*Workspace, error) {
	remote := cache.Remote(repourl.CloneURL(repo), gitx.Partial)
	branch, commit, err := remote.DefaultBranch(ctx)
	if err != nil {
		return nil, err
	}
	ws, err := inspect(ctx, remoteFiles{remote: remote, commit: commit})
	if err != nil {
		return nil, err
	}
	ws.Repo = repo
	ws.Where = branch + "@" + gitx.Short(commit)
	return ws, nil
}

// Check checks a workspace against the blueprint that would serve it.
func Check(ws *Workspace, f *blueprint.Blueprint) diag.List {
	var diags diag.List
	where := ws.Repo
	if where == "" {
		where = ws.Where
	}
	for _, p := range ws.LineProblems {
		diags.Errorf("product.fabrica-line", where, "%s", p).
			Hintf("make the line between these delimiters the very first line of the file:\n%s", promptblock.Wrap(fabline.Sentence))
	}
	for _, name := range sortedSkillNames(ws.Skills) {
		if _, ok := f.Skills[name]; ok && !f.Expects(name) {
			diags.Errorf("product.skill-clash", where, "the repo has its own skill %q (%s), and so does the blueprint; Fabrica won't pick one", name, strings.Join(ws.Skills[name], ", ")).
				Hintf("rename or remove the repo's skill, or, if repos are meant to provide their own %q, add it to the blueprint's expects:", name)
		}
	}
	for _, e := range f.Manifest.Expects {
		if _, ok := ws.Skills[e.Name]; ok {
			continue
		}
		_, fallback := f.Skills[e.Name]
		switch {
		case fallback:
			diags.Notef("product.expects-default", where, "the repo doesn't provide skill %q, so the blueprint's default is used", e.Name)
		case e.Optional:
			diags.Notef("product.expects-optional", where, "the repo doesn't provide optional skill %q", e.Name)
		default:
			diags.Warnf("product.unmet-expects", where, "the blueprint expects the repo to provide skill %q, but it doesn't", e.Name).
				Hintf("add it as a repo skill, for example in .agents/skills/%s/SKILL.md", e.Name)
		}
	}
	return diags
}

// Clashes returns the repo skills that clash with blueprint skills.
func Clashes(ws *Workspace, f *blueprint.Blueprint) []string {
	var out []string
	for _, name := range sortedSkillNames(ws.Skills) {
		if _, ok := f.Skills[name]; ok && !f.Expects(name) {
			out = append(out, name)
		}
	}
	return out
}

func sortedSkillNames(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RepoName works out a workspace's canonical repo name from its git
// remote: `origin` if there is one, otherwise the only remote. Anything
// else is ambiguous, and Fabrica won't guess.
func RepoName(ctx context.Context, dir string) (name, toplevel string, err error) {
	wt, err := gitx.OpenWorktree(ctx, dir)
	if err != nil {
		return "", "", fmt.Errorf("%s isn't inside a git repo, so Fabrica can't tell which repo this is", dir)
	}
	remote := "origin"
	url := wt.RemoteURL(ctx, remote)
	if url == "" {
		remotes := wt.Remotes(ctx)
		switch len(remotes) {
		case 0:
			return "", wt.Dir, fmt.Errorf("the repo at %s has no git remote, so Fabrica can't tell which repo it is; add one with `git remote add origin <url>`", wt.Dir)
		case 1:
			url = wt.RemoteURL(ctx, remotes[0])
		default:
			return "", wt.Dir, fmt.Errorf("the repo at %s has several remotes (%s) and none is called origin, so Fabrica can't tell which repo it is", wt.Dir, strings.Join(remotes, ", "))
		}
	}
	name, err = repourl.Normalize(url)
	if err != nil {
		return "", wt.Dir, fmt.Errorf("the git remote of %s: %w", wt.Dir, err)
	}
	return name, wt.Dir, nil
}

type dirFiles string

func (d dirFiles) readFile(_ context.Context, p string) ([]byte, error) {
	return os.ReadFile(filepath.Join(string(d), filepath.FromSlash(p)))
}

func (d dirFiles) listDirs(_ context.Context, p string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(string(d), filepath.FromSlash(p)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

type remoteFiles struct {
	remote *gitx.Remote
	commit string
}

func (r remoteFiles) readFile(ctx context.Context, p string) ([]byte, error) {
	return r.remote.ReadFile(ctx, r.commit, p)
}

func (r remoteFiles) listDirs(ctx context.Context, p string) ([]string, error) {
	entries, err := r.remote.ListDir(ctx, r.commit, p)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type == "tree" {
			out = append(out, e.Name)
		}
	}
	return out, nil
}
