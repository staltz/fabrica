package blueprint

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/staltz/fabrica/internal/names"
)

// AgentSDKPackage is the npm package every Agent SDK project depends on.
const AgentSDKPackage = "@grok/agent-sdk"

// formerAgentSDKPackages are the names the Agent SDK had before, which
// Cursor's deployments no longer take.
var formerAgentSDKPackages = []string{"@cursor/bdk", "@cursor/july"}

// agentLockfiles are the lockfiles an Agent SDK build can install from.
var agentLockfiles = []string{"package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock"}

// Agent is a Cursor Agent SDK project (https://cursor.com/docs/agent-sdk),
// completely unchanged: a package.json and a bot/ folder of Markdown and
// TypeScript. Fabrica doesn't read its TypeScript. It deploys the project
// from where it lives in git, so the agent's own channels, schedules and
// agent.ts decide what it does.
type Agent struct {
	ItemInfo
	// Dir is the project's folder on disk.
	Dir string
	// InstallDir is the folder with the project's lockfile: Dir, or a
	// parent of it in an npm workspace.
	InstallDir string
	// Source is where the project lives in git, which is where Cursor
	// builds it from. Its Git is empty for a blueprint read straight from a
	// folder, which can be checked but not deployed.
	Source AgentSource
}

func (a *Agent) withOrigin(o Origin) item { c := *a; c.Origin = o; return &c }

// AgentSource is where an Agent SDK project lives: a repo, an exact
// commit, and the project's folder inside the repo.
type AgentSource struct {
	// Git is the repo, as written in the factory or import, like
	// github.com/acme/blueprint.
	Git string
	// Commit is the exact commit.
	Commit string
	// Path is the project's folder inside the repo, like agents/pr-reviewer,
	// or empty for a project at the repo's root.
	Path string
}

// agentPackage is the part of package.json that Fabrica reads.
type agentPackage struct {
	Name            string            `json:"name"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func (p agentPackage) dependsOn(pkg string) bool {
	_, dep := p.Dependencies[pkg]
	_, dev := p.DevDependencies[pkg]
	return dep || dev
}

// readAgentPackage reads a folder's package.json. ok is false when there's
// none; err is set when there's one Fabrica can't read.
func readAgentPackage(dir string) (pkg agentPackage, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return pkg, false, nil
	}
	if err != nil {
		return pkg, true, err
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return pkg, true, fmt.Errorf("package.json isn't valid JSON: %v", err)
	}
	return pkg, true, nil
}

// isAgentProject reports whether a folder looks like an Agent SDK project:
// it has a package.json that depends on the Agent SDK (or one of its former
// names, so parseAgent can say what to change).
func isAgentProject(dir string) bool {
	pkg, ok, err := readAgentPackage(dir)
	if !ok || err != nil {
		return false
	}
	if pkg.dependsOn(AgentSDKPackage) {
		return true
	}
	for _, former := range formerAgentSDKPackages {
		if pkg.dependsOn(former) {
			return true
		}
	}
	return false
}

// PackageAgentName turns a package.json name into an agent name: the
// part after the scope, so @acme/pr-reviewer is pr-reviewer.
func PackageAgentName(pkgName string) string {
	if strings.HasPrefix(pkgName, "@") {
		if _, rest, ok := strings.Cut(pkgName, "/"); ok {
			return rest
		}
	}
	return pkgName
}

// parseAgent reads an Agent SDK project folder. name is the agent's name
// (its folder's name in a blueprint, or its package name in a plain repo),
// file is where it is for messages, and repoRoot is the root of the repo
// the folder is in (lockfiles can sit at the root of an npm workspace), or
// empty when that isn't known.
func parseAgent(dir, name, file, repoRoot string, src AgentSource) *Agent {
	a := &Agent{ItemInfo: ItemInfo{Ref: names.Ref{Kind: names.Agents, Name: name}, File: file}, Dir: dir, Source: src}
	fail := func(format string, args ...any) *Agent {
		a.Problems.Errorf("agent.invalid", file, format, args...)
		return a
	}
	// hint adds a hint to the problem just reported.
	hint := func(a *Agent, format string, args ...any) *Agent {
		a.Problems[len(a.Problems)-1].Hint = fmt.Sprintf(format, args...)
		return a
	}
	if err := names.Validate(name); err != nil {
		return fail("isn't a valid agent name: %v", err)
	}
	pkg, ok, err := readAgentPackage(dir)
	switch {
	case !ok:
		return fail("there's no package.json in this folder; an Agent SDK project needs one that depends on %s", AgentSDKPackage)
	case err != nil:
		return fail("%v", err)
	}
	if !pkg.dependsOn(AgentSDKPackage) {
		for _, former := range formerAgentSDKPackages {
			if pkg.dependsOn(former) {
				return hint(fail("package.json depends on %s, the Agent SDK's former name", former),
					"switch to %s: npm uninstall %s && npm install %s", AgentSDKPackage, former, AgentSDKPackage)
			}
		}
		return hint(fail("package.json doesn't depend on %s, so this isn't an Agent SDK project", AgentSDKPackage),
			"add it with `npm install %s`", AgentSDKPackage)
	}

	// The SDK looks for the agent in bot/, or in agent/ for older projects.
	root := ""
	for _, candidate := range []string{"bot", "agent"} {
		if info, err := os.Stat(filepath.Join(dir, candidate)); err == nil && info.IsDir() {
			root = candidate
			break
		}
	}
	if root == "" {
		return hint(fail("there's no bot/ folder; the Agent SDK keeps the agent's files in bot/"),
			"`agent-sdk init` scaffolds one")
	}
	if !hasInstructions(filepath.Join(dir, root)) {
		return fail("%s/ has no instructions; the root agent needs %s/instructions.md (or instructions.ts, or an instructions/ folder)", root, root)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err == nil && src.Commit != "" {
		return hint(fail("node_modules/ is committed; Cursor installs dependencies from the lockfile when it builds the agent"),
			"remove it from git and add node_modules/ to .gitignore")
	}
	if a.InstallDir = lockfileDir(dir, repoRoot); a.InstallDir == "" {
		return hint(fail("there's no lockfile (%s) here or in a parent folder of the repo; Cursor installs the agent's dependencies from it", strings.Join(agentLockfiles, ", ")),
			"run `npm install` and commit package-lock.json")
	}

	hash, _, err := hashTreeSkipping(dir, "node_modules")
	if err != nil {
		return fail("%v", err)
	}
	a.Hash = hash
	return a
}

func hasInstructions(botDir string) bool {
	for _, f := range []string{"instructions.md", "instructions.ts"} {
		if info, err := os.Stat(filepath.Join(botDir, f)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	info, err := os.Stat(filepath.Join(botDir, "instructions"))
	return err == nil && info.IsDir()
}

// lockfileDir looks for a lockfile in dir and each parent folder up to
// repoRoot, and returns the folder it's in, or "" when there's none.
// Without a repoRoot, it goes up until it finds a .git.
func lockfileDir(dir, repoRoot string) string {
	for d := filepath.Clean(dir); ; {
		for _, f := range agentLockfiles {
			if _, err := os.Stat(filepath.Join(d, f)); err == nil {
				return d
			}
		}
		if repoRoot != "" && d == filepath.Clean(repoRoot) {
			return ""
		}
		if repoRoot == "" {
			if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
				return ""
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// loadPlainAgents finds the Agent SDK projects in a plain repo: the folder
// at path (the repo's root when it's empty) if it's a project, named after
// its package.json, or else each of its subfolders that is one, named
// after the folder.
func loadPlainAgents(root, git, commit, subpath string) []*Agent {
	dir := filepath.Join(root, filepath.FromSlash(subpath))
	if isAgentProject(dir) {
		pkg, _, _ := readAgentPackage(dir)
		name := PackageAgentName(pkg.Name)
		file := subpath
		if file == "" {
			file = "package.json"
		}
		a := parseAgent(dir, name, file, root, AgentSource{Git: git, Commit: commit, Path: subpath})
		if name == "" {
			a.Problems = nil
			a.Ref.Name = "unnamed"
			a.Problems.Errorf("agent.invalid", file, "package.json has no name, and an agent imported from a plain repo is named after it")
		}
		return []*Agent{a}
	}
	var out []*Agent
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() || isSymlink(e) {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if !isAgentProject(sub) {
			continue
		}
		p := path.Join(subpath, e.Name())
		out = append(out, parseAgent(sub, e.Name(), p, root, AgentSource{Git: git, Commit: commit, Path: p}))
	}
	return out
}
