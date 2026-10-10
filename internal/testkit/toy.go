package testkit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The toy runtime is a pretend cloud runtime for tests. Its "API" is a
// folder of JSON files, one per resource, and its provider is the test
// binary itself: a test package's TestMain calls MaybeRunToyProvider, so
// when Fabrica runs the provider's `api` script, the script runs the test
// binary in provider mode instead of running tests.

const (
	toyModeEnv = "FABRICA_TOY_PROVIDER"
	toyBinEnv  = "FABRICA_TOY_BIN"
	toyDBEnv   = "FABRICA_TOY_DB"
)

// ToyProviderFiles are the files of the toy folder provider, ready to drop
// into a factory under providers/toy/.
var ToyProviderFiles = map[string]string{
	"providers/toy/provider.yaml": `kind: cloud
description: A pretend runtime used in Fabrica's tests
apply_modes: [api]
find_by_identity: true
skill_transport: text
mcp_auth: { header: native }
triggers:
  github.pull_request.opened: native
  github.pull_request.synchronize: native
  schedule: native
connectors:
  github: native
  mcp.http: native
side_effects:
  "*": emulated
`,
	"providers/toy/api!x": "#!/bin/sh\nexec \"$" + toyBinEnv + "\" \"$@\"\n",
}

// MaybeRunToyProvider turns the test binary into the toy provider when it
// was started as one. Call it first thing in TestMain.
func MaybeRunToyProvider() {
	if os.Getenv(toyModeEnv) != "1" {
		return
	}
	if err := runToy(os.Args[len(os.Args)-1], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// ToyRuntime is a handle on the toy runtime's resources, for tests.
type ToyRuntime struct {
	t  *testing.T
	DB string
}

// NewToyRuntime sets up the environment so Fabrica's calls to the toy
// provider land in a fresh database folder.
func NewToyRuntime(t *testing.T) *ToyRuntime {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	db := t.TempDir()
	t.Setenv(toyModeEnv, "1")
	t.Setenv(toyBinEnv, bin)
	t.Setenv(toyDBEnv, db)
	return &ToyRuntime{t: t, DB: db}
}

// ToyResource is one resource in the toy runtime.
type ToyResource struct {
	RemoteID    string `json:"remote_id"`
	Identity    string `json:"identity"`
	Fingerprint string `json:"fingerprint"`
	Prompt      string `json:"prompt"`
	FabricaURL  string `json:"fabrica_url"`
	Token       string `json:"token"`
	Trigger     string `json:"trigger"`
}

// Resources lists everything in the toy runtime, sorted by identity.
func (r *ToyRuntime) Resources() []ToyResource {
	r.t.Helper()
	out, err := toyList(r.DB)
	if err != nil {
		r.t.Fatal(err)
	}
	return out
}

// Put adds a resource directly, like someone creating it by hand (or an
// apply that crashed before recording it).
func (r *ToyRuntime) Put(res ToyResource) {
	r.t.Helper()
	data, _ := json.Marshal(res)
	if err := os.WriteFile(filepath.Join(r.DB, res.RemoteID+".json"), data, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func toyList(db string) ([]ToyResource, error) {
	entries, err := os.ReadDir(db)
	if err != nil {
		return nil, err
	}
	var out []ToyResource
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(db, e.Name()))
		if err != nil {
			return nil, err
		}
		var r ToyResource
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity+out[i].RemoteID < out[j].Identity+out[j].RemoteID })
	return out, nil
}

func runToy(op string, in io.Reader, out io.Writer) error {
	db := os.Getenv(toyDBEnv)
	var req struct {
		Version  int    `json:"version"`
		Endpoint string `json:"endpoint"`
		RemoteID string `json:"remote_id"`
		Resource *struct {
			Identity    string `json:"identity"`
			Fingerprint string `json:"fingerprint"`
			Prompt      string `json:"prompt"`
			Trigger     struct {
				On string `json:"on"`
			} `json:"trigger"`
			Fabrica struct {
				URL   string `json:"url"`
				Token string `json:"token"`
			} `json:"fabrica"`
		} `json:"resource"`
	}
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return err
	}
	if req.Version != 1 {
		return fmt.Errorf("unsupported protocol version %d", req.Version)
	}
	enc := json.NewEncoder(out)
	switch op {
	case "list":
		all, err := toyList(db)
		if err != nil {
			return err
		}
		prefix := strings.TrimPrefix(strings.TrimPrefix(req.Endpoint, "https://"), "http://") + "#"
		var found []map[string]string
		for _, r := range all {
			if strings.HasPrefix(r.Identity, prefix) {
				found = append(found, map[string]string{"identity": r.Identity, "remote_id": r.RemoteID, "fingerprint": r.Fingerprint})
			}
		}
		return enc.Encode(map[string]any{"resources": found})
	case "create", "update":
		id := req.RemoteID
		if op == "create" {
			all, _ := toyList(db)
			id = fmt.Sprintf("toy_%d", len(all)+1)
			for {
				if _, err := os.Stat(filepath.Join(db, id+".json")); err != nil {
					break
				}
				id += "x"
			}
		}
		r := req.Resource
		res := ToyResource{RemoteID: id, Identity: r.Identity, Fingerprint: r.Fingerprint, Prompt: r.Prompt,
			FabricaURL: r.Fabrica.URL, Token: r.Fabrica.Token, Trigger: r.Trigger.On}
		data, _ := json.Marshal(res)
		if err := os.WriteFile(filepath.Join(db, id+".json"), data, 0o644); err != nil {
			return err
		}
		return enc.Encode(map[string]string{"remote_id": id})
	case "delete":
		if err := os.Remove(filepath.Join(db, req.RemoteID+".json")); err != nil {
			return err
		}
		return enc.Encode(map[string]string{})
	}
	return fmt.Errorf("unknown op %q", op)
}
