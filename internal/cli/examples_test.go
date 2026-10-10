package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/staltz/fabrica/internal/diag"
	"github.com/staltz/fabrica/internal/factory"
	"github.com/staltz/fabrica/internal/gitx"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/product"
)

// These tests keep the examples in examples/ working, so the docs never
// point people at something broken.

func examplesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func loadExampleFactory(t *testing.T, name string) *factory.Factory {
	t.Helper()
	l := factory.NewLoader(gitx.NewCache(t.TempDir()))
	f, diags := l.LoadDir(context.Background(), filepath.Join(examplesDir(t), name), factory.LoadOptions{})
	if f != nil {
		diags = append(diags, f.Lint()...)
	}
	if problems := append(diags.Only(diag.Error), diags.Only(diag.Warning)...); len(problems) > 0 {
		t.Fatalf("examples/%s should have no errors or warnings:\n%s", name, problems)
	}
	return f
}

func TestStarterFactoryIsClean(t *testing.T) {
	f := loadExampleFactory(t, "starter-factory")
	if len(f.Skills) != 5 || len(f.Routines) != 3 || len(f.Rules) != 4 || len(f.Connectors) != 2 {
		t.Errorf("the docs describe 5 skills, 3 routines, 4 rules and 2 connectors; got %d, %d, %d, %d",
			len(f.Skills), len(f.Routines), len(f.Rules), len(f.Connectors))
	}
	for name, s := range f.Skills {
		if !s.TextOnly {
			t.Errorf("starter skill %s should be text-only, so it works in the cloud: %s", name, s.NotTextOnlyReason)
		}
	}
}

func TestCustomProviderFactoryIsClean(t *testing.T) {
	f := loadExampleFactory(t, filepath.Join("custom-provider", "factory"))
	if p := f.Providers["acme-runner"]; p == nil || p.Provider == nil || p.Provider.APIPath == "" {
		t.Errorf("acme-runner should load with its api program: %+v", p)
	}
}

func TestExampleHomeReposParse(t *testing.T) {
	t.Setenv("FABRICA_HOME", t.TempDir())
	team, err := home.Open(filepath.Join(examplesDir(t), "home"))
	if err != nil {
		t.Fatal(err)
	}
	if team.Diags.HasErrors() || len(team.Deployments) != 2 {
		t.Errorf("examples/home: %d deployments\n%s", len(team.Deployments), team.Diags)
	}
	personalDir := filepath.Join(examplesDir(t), "personal-home")
	t.Setenv("FABRICA_HOME", personalDir)
	personal, err := home.Open(personalDir)
	if err != nil {
		t.Fatal(err)
	}
	if personal.Diags.HasErrors() || len(personal.Deployments) != 2 {
		t.Errorf("examples/personal-home: %d deployments\n%s", len(personal.Deployments), personal.Diags)
	}
	if d, _ := personal.MatchLocal("github.com/oss/project", "claude-code"); d == nil || d.Name != "oss-work" {
		t.Errorf("oss-work should win for its repo, got %v", d)
	}
}

func TestExampleProductRepoPasses(t *testing.T) {
	f := loadExampleFactory(t, "starter-factory")
	ws, err := product.InspectDir(context.Background(), filepath.Join(examplesDir(t), "product-repo"))
	if err != nil {
		t.Fatal(err)
	}
	diags := product.Check(ws, f)
	if problems := append(diags.Only(diag.Error), diags.Only(diag.Warning)...); len(problems) > 0 {
		t.Errorf("examples/product-repo should pass against the starter factory:\n%s", problems)
	}
}

// TestCustomProviderEndToEnd deploys a routine to Acme Runner through the
// example's Python api program.
func TestCustomProviderEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 isn't installed")
	}
	w := setupCLI(t)
	repo := w.Host.Repo("github.com/acme/acme-factory")
	copyTree(t, filepath.Join(examplesDir(t), "custom-provider", "factory"), repo.Dir)
	repo.Commit("acme factory")
	db := filepath.Join(t.TempDir(), "acme-runner.json")
	t.Setenv("ACME_RUNNER_DB", db)
	w.Home.Write(map[string]string{"deployments/triage.yaml": "factory: { git: github.com/acme/acme-factory, ref: main }\nserver: https://fabrica.example.com\nruntimes: [acme-runner]\nroutines: [triage]\nrepos: [github.com/acme/app]\n"}).CommitPush("triage")

	out := mustFab(t, 2, w.Home.Dir, "plan")
	if !strings.Contains(out, "+ create triage @ github.com/acme/app on acme-runner (api)") {
		t.Fatalf("plan:\n%s", out)
	}
	mustFab(t, 0, w.Home.Dir, "apply")
	data, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	var jobs map[string]struct {
		Identity string `json:"identity"`
	}
	if err := json.Unmarshal(data, &jobs); err != nil || len(jobs) != 1 {
		t.Fatalf("the runtime should have one job: %s (%v)", data, err)
	}
	for _, j := range jobs {
		if j.Identity != "fabrica.example.com/triage#triage@github.com/acme/app" {
			t.Errorf("identity = %q", j.Identity)
		}
	}
	if out := mustFab(t, 0, w.Home.Dir, "plan"); !strings.Contains(out, "Everything is in sync") {
		t.Errorf("after apply, `api list` should find the job:\n%s", out)
	}
}

// copyTree copies a folder, keeping executable bits.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}
