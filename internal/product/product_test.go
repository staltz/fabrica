package product_test

import (
	"context"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/fabline"
	"github.com/staltz/fabrica/internal/factory"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/product"
	"github.com/staltz/fabrica/internal/testkit"
)

func loadFactory(t *testing.T) *factory.Factory {
	t.Helper()
	dir := t.TempDir()
	testkit.WriteFiles(t, dir, map[string]string{
		"fabrica.yaml":              "expects: [test, deploy?, lint]\n",
		"skills/pr-review/SKILL.md": "---\nname: pr-review\ndescription: Reviews.\n---\nx\n",
		"skills/lint/SKILL.md":      "---\nname: lint\ndescription: Default lint.\n---\nx\n",
	})
	f, diags := factory.NewLoader(gitx.NewCache(t.TempDir())).LoadDir(context.Background(), dir, factory.LoadOptions{})
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	return f
}

func TestCheckWorkspace(t *testing.T) {
	f := loadFactory(t)
	dir := t.TempDir()
	testkit.WriteFiles(t, dir, map[string]string{
		"AGENTS.md":                         fabline.Sentence + "\n\n# App\n",
		"CLAUDE.md":                         "# App\n",
		".claude/skills/pr-review/SKILL.md": "---\nname: pr-review\n---\n",
		".agents/skills/test/SKILL.md":      "---\nname: test\n---\n",
	})
	ws, err := product.InspectDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	diags := product.Check(ws, f)
	for _, code := range []string{"product.fabrica-line", "product.skill-clash", "product.expects-default", "product.expects-optional"} {
		if !diags.HasCode(code) {
			t.Errorf("want %s, got:\n%s", code, diags)
		}
	}
	if diags.HasCode("product.unmet-expects") {
		t.Errorf("test is provided by the repo, so nothing required is unmet:\n%s", diags)
	}
	if want := "\n-----BEGIN PROMPT-----\n" + fabline.Sentence + "\n-----END PROMPT-----"; !strings.Contains(diags.String(), want) {
		t.Errorf("the hint should give the Fabrica line between delimiters, got:\n%s", diags)
	}
	if got := product.Clashes(ws, f); len(got) != 1 || got[0] != "pr-review" {
		t.Errorf("Clashes = %v", got)
	}
}

func TestInspectRemoteDefaultBranch(t *testing.T) {
	host := testkit.NewHost(t)
	repo := host.Repo("github.com/acme/app")
	repo.Write(map[string]string{"AGENTS.md": "no line here\n"}).Commit("init")

	ws, err := product.InspectRemote(context.Background(), gitx.NewCache(t.TempDir()), "github.com/acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if ws.HasLine() || ws.Repo != "github.com/acme/app" {
		t.Errorf("workspace = %+v", ws)
	}
	diags := product.Check(ws, loadFactory(t))
	if !diags.HasCode("product.unmet-expects") || !diags.HasCode("product.fabrica-line") {
		t.Errorf("got:\n%s", diags)
	}
}

func TestRepoName(t *testing.T) {
	testkit.NewHost(t)
	dir := t.TempDir()
	testkit.Git(t, dir, "init", "--quiet")
	if _, _, err := product.RepoName(context.Background(), dir); err == nil {
		t.Error("a repo without remotes should be an error")
	}
	testkit.Git(t, dir, "remote", "add", "upstream", "git@github.com:Acme/App.git")
	name, _, err := product.RepoName(context.Background(), dir)
	if err != nil || name != "github.com/acme/app" {
		t.Errorf("RepoName = %q, %v", name, err)
	}
	testkit.Git(t, dir, "remote", "add", "fork", "git@github.com:me/app.git")
	if _, _, err := product.RepoName(context.Background(), dir); err == nil {
		t.Error("two remotes and no origin should be ambiguous")
	}
	testkit.Git(t, dir, "remote", "add", "origin", "https://github.com/acme/app")
	if name, _, err := product.RepoName(context.Background(), dir); err != nil || name != "github.com/acme/app" {
		t.Errorf("origin should win: %q, %v", name, err)
	}
}
