package testkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The fake Agent SDK stands in for Cursor's `agent-sdk` command line and
// for `npm`, in tests. Both are scripts that run the test binary, which a
// test package's TestMain turns into the fake with MaybeRunFakeAgentSDK.
// Its "Cursor" is a folder of JSON files, one per deployment.

const (
	fakeSDKModeEnv = "FABRICA_FAKE_AGENT_SDK"
	fakeSDKExeEnv  = "FABRICA_FAKE_AGENT_SDK_EXE"
	fakeSDKDBEnv   = "FABRICA_FAKE_AGENT_SDK_DB"
	// FakeAgentSDKFailEnv, when set, makes the fake's deploys end failed,
	// with its value as the deployment's last error.
	FakeAgentSDKFailEnv = "FABRICA_FAKE_AGENT_SDK_FAIL"
	// FakeAgentSDKPageEnv sets how many deployments a page of
	// `deployment list` holds at most.
	FakeAgentSDKPageEnv = "FABRICA_FAKE_AGENT_SDK_PAGE"
	// FakeAgentSDKKey is the Cursor key NewFakeAgentSDK sets.
	FakeAgentSDKKey = "key_fake-agent-sdk"
)

// FakeAgentSDK is a handle on the fake's deployments, for tests.
type FakeAgentSDK struct {
	t  *testing.T
	DB string
	// Bin is the fake agent-sdk script.
	Bin string
}

// FakeDeployment is one deployment in the fake.
type FakeDeployment struct {
	Slug       string            `json:"slug"`
	Repo       string            `json:"repo"`
	Ref        string            `json:"ref"`
	Path       string            `json:"path"`
	EventRepos []string          `json:"event_repos"`
	Secrets    map[string]string `json:"secrets"`
	Deploys    int               `json:"deploys"`
	Status     string            `json:"status"`
}

// NewFakeAgentSDK puts the fake agent-sdk where Fabrica looks for it, and
// a fake npm on PATH, and sets a Cursor key.
func NewFakeAgentSDK(t *testing.T) *FakeAgentSDK {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	db := t.TempDir()
	for _, name := range []string{"agent-sdk", "npm"} {
		// The toy runtime's mode is on for the whole test, so turn it off
		// for the fake.
		script := "#!/bin/sh\nFABRICA_TOY_PROVIDER= " + fakeSDKModeEnv + "=1 exec \"$" + fakeSDKExeEnv + "\" " + name + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(fakeSDKExeEnv, exe)
	t.Setenv(fakeSDKDBEnv, db)
	t.Setenv("FABRICA_AGENT_SDK_BIN", filepath.Join(bin, "agent-sdk"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CURSOR_API_KEY", FakeAgentSDKKey)
	t.Setenv("CURSOR_SERVICE_ACCOUNT_KEY", "")
	return &FakeAgentSDK{t: t, DB: db, Bin: filepath.Join(bin, "agent-sdk")}
}

// Deployments lists the fake's deployments, sorted by slug.
func (f *FakeAgentSDK) Deployments() []FakeDeployment {
	f.t.Helper()
	list, err := fakeDeployments(f.DB)
	if err != nil {
		f.t.Fatal(err)
	}
	return list
}

// Put adds a deployment, like someone deploying it by hand.
func (f *FakeAgentSDK) Put(d FakeDeployment) {
	f.t.Helper()
	if err := fakeSave(f.DB, &d); err != nil {
		f.t.Fatal(err)
	}
}

// Remove deletes a deployment, like someone deleting it by hand.
func (f *FakeAgentSDK) Remove(slug string) {
	f.t.Helper()
	if err := os.Remove(filepath.Join(f.DB, slug+".json")); err != nil {
		f.t.Fatal(err)
	}
}

// Calls lists the commands the fake ran, one line each, like
// "agent-sdk deploy --dir …".
func (f *FakeAgentSDK) Calls() []string {
	data, _ := os.ReadFile(filepath.Join(f.DB, "calls.log"))
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// MaybeRunFakeAgentSDK turns the test binary into the fake agent-sdk or
// npm when it was started as one. Call it first thing in TestMain.
func MaybeRunFakeAgentSDK() {
	if os.Getenv(fakeSDKModeEnv) != "1" || len(os.Args) < 2 {
		return
	}
	db := os.Getenv(fakeSDKDBEnv)
	tool, args := os.Args[1], os.Args[2:]
	if f, err := os.OpenFile(filepath.Join(db, "calls.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, tool+" "+strings.Join(args, " "))
		f.Close()
	}
	var code int
	switch tool {
	case "npm":
		code = fakeNPM(args)
	default:
		code = fakeSDK(db, args, os.Stdin, os.Stdout)
	}
	os.Exit(code)
}

func fakeNPM(args []string) int {
	if len(args) == 0 || args[0] != "ci" {
		fmt.Fprintln(os.Stderr, "the fake npm only knows `npm ci`")
		return 1
	}
	if _, err := os.Stat("package-lock.json"); err != nil {
		fmt.Fprintln(os.Stderr, "npm ci needs a package-lock.json")
		return 1
	}
	if err := os.MkdirAll("node_modules", 0o755); err != nil {
		return 1
	}
	if err := os.WriteFile(filepath.Join("node_modules", ".fake-installed"), nil, 0o644); err != nil {
		return 1
	}
	return 0
}

// fakeFlags reads --name value flags (repeated ones in order) and the
// arguments that aren't flags.
func fakeFlags(args []string) (map[string][]string, []string) {
	flags := map[string][]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		name, ok := strings.CutPrefix(args[i], "--")
		if !ok {
			rest = append(rest, args[i])
			continue
		}
		switch name {
		case "json", "yes", "no-event-repos":
			flags[name] = append(flags[name], "true")
		default:
			if i+1 < len(args) {
				flags[name] = append(flags[name], args[i+1])
				i++
			}
		}
	}
	return flags, rest
}

var fakeSecretNamesRe = regexp.MustCompile(`secretNames:\s*\[([^\]]*)\]`)

func fakeSDK(db string, args []string, stdin io.Reader, stdout io.Writer) int {
	fail := func(code int, kind, format string, a ...any) int {
		doc := map[string]any{"error": map[string]string{"code": kind, "message": fmt.Sprintf(format, a...)}}
		json.NewEncoder(stdout).Encode(doc)
		return code
	}
	print := func(v any) int {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(v)
		return 0
	}
	if os.Getenv("CURSOR_API_KEY") == "" && os.Getenv("CURSOR_SERVICE_ACCOUNT_KEY") == "" {
		return fail(5, "unauthenticated", "Not signed in to Cursor.")
	}
	if len(args) > 0 && (args[0] == "deployment" || args[0] == "secret") && len(args) > 1 {
		args = append([]string{args[0] + " " + args[1]}, args[2:]...)
	}
	if len(args) == 0 {
		return fail(2, "usage", "no command")
	}
	flags, rest := fakeFlags(args[1:])
	one := func(name string) string {
		if v := flags[name]; len(v) > 0 {
			return v[len(v)-1]
		}
		return ""
	}
	switch args[0] {
	case "info":
		dir := one("dir")
		if !fakeInstalled(dir) {
			return fail(1, "failed", "error: Failed to load module: Cannot find package 'zod' imported from %s", dir)
		}
		src, _ := os.ReadFile(filepath.Join(dir, "bot", "agent.ts"))
		names := []string{}
		if m := fakeSecretNamesRe.FindSubmatch(src); m != nil {
			for _, n := range strings.Split(string(m[1]), ",") {
				if n = strings.Trim(strings.TrimSpace(n), `"'`); n != "" {
					names = append(names, n)
				}
			}
		}
		return print(map[string]any{"manifestVersion": 1, "info": map[string]any{"hosting": map[string]any{"secretNames": names}}})
	case "deploy":
		slug := one("slug")
		if !fakeInstalled(one("dir")) {
			return fail(1, "failed", "error: Failed to load module: Cannot find package 'zod'")
		}
		if !strings.HasPrefix(one("repo"), "https://") {
			return fail(2, "usage", "Invalid --repo: %s (expected an https:// git repository URL)", one("repo"))
		}
		d, _ := fakeLoad(db, slug)
		first := d == nil
		if first {
			d = &FakeDeployment{Slug: slug, Secrets: map[string]string{}}
		}
		d.Repo, d.Ref, d.Path, d.EventRepos = one("repo"), one("ref"), one("path"), flags["cursor-events-repo"]
		d.Deploys++
		d.Status = "running"
		var lastError any
		if why := os.Getenv(FakeAgentSDKFailEnv); why != "" {
			d.Status, lastError = "failed", why
		}
		if err := fakeSave(db, d); err != nil {
			return fail(1, "failed", "%v", err)
		}
		out := map[string]any{"slug": slug, "status": d.Status, "lastError": lastError, "secretNames": fakeSecretNames(d), "repositories": d.EventRepos}
		if first {
			out["aliasToken"] = "alias_" + slug
		}
		print(out)
		if d.Status != "running" {
			return 1
		}
		return 0
	case "deployment list":
		list, err := fakeDeployments(db)
		if err != nil {
			return fail(1, "failed", "%v", err)
		}
		size, _ := strconv.Atoi(os.Getenv(FakeAgentSDKPageEnv))
		if limit, _ := strconv.Atoi(one("limit")); size == 0 || limit < size {
			size = limit
		}
		start, _ := strconv.Atoi(strings.TrimPrefix(one("page-token"), "offset:"))
		items := []map[string]any{}
		for i := start; i < len(list) && i < start+size; i++ {
			items = append(items, map[string]any{"architecture": "v2", "slug": list[i].Slug, "status": list[i].Status})
		}
		var next any
		if start+size < len(list) {
			next = fmt.Sprintf("offset:%d", start+size)
		}
		return print(map[string]any{"items": items, "nextPageToken": next})
	case "deployment update", "deployment delete", "secret set":
		if len(rest) == 0 {
			return fail(2, "usage", "missing slug")
		}
		d, err := fakeLoad(db, rest[0])
		switch {
		case err != nil:
			return fail(1, "failed", "%v", err)
		case d == nil:
			return fail(4, "not_found", "There's no deployment %s.", rest[0])
		}
		switch args[0] {
		case "deployment delete":
			if err := os.Remove(filepath.Join(db, d.Slug+".json")); err != nil {
				return fail(1, "failed", "%v", err)
			}
			return print(map[string]any{"slug": d.Slug, "deleted": true})
		case "deployment update":
			d.EventRepos = flags["event-repo"]
		case "secret set":
			if len(rest) != 2 {
				return fail(2, "usage", "the fake sets one secret at a time")
			}
			value, _ := io.ReadAll(stdin)
			d.Secrets[rest[1]] = strings.TrimSuffix(string(value), "\n")
		}
		if err := fakeSave(db, d); err != nil {
			return fail(1, "failed", "%v", err)
		}
		return print(map[string]any{"slug": d.Slug, "secretNames": fakeSecretNames(d)})
	}
	return fail(2, "usage", "the fake agent-sdk doesn't know %q", args[0])
}

// fakeInstalled reports whether npm installed dependencies where Node would
// find them from dir: in its node_modules, or a parent folder's.
func fakeInstalled(dir string) bool {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "node_modules", ".fake-installed")); err == nil {
			return true
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

func fakeSecretNames(d *FakeDeployment) []string {
	names := []string{}
	for n := range d.Secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func fakeLoad(db, slug string) (*FakeDeployment, error) {
	data, err := os.ReadFile(filepath.Join(db, slug+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d FakeDeployment
	return &d, json.Unmarshal(data, &d)
}

func fakeSave(db string, d *FakeDeployment) error {
	if d.Secrets == nil {
		d.Secrets = map[string]string{}
	}
	if d.Status == "" {
		d.Status = "running"
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(db, d.Slug+".json"), data, 0o644)
}

func fakeDeployments(db string) ([]FakeDeployment, error) {
	entries, err := os.ReadDir(db)
	if err != nil {
		return nil, err
	}
	var out []FakeDeployment
	for _, e := range entries {
		slug, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		d, err := fakeLoad(db, slug)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}
