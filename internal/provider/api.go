package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ProtocolVersion is the version of the JSON protocol Fabrica speaks with a
// folder provider's `api` program. It goes into every request so programs
// can refuse versions they don't understand.
const ProtocolVersion = 1

// Resource is everything a runtime needs to set up one stub: one routine,
// on one repo, in one runtime.
type Resource struct {
	// Identity is the stable Fabrica identity, like
	// "fabrica.acme.dev/pr-review#pr-review@github.com/acme/app". The
	// runtime must store it somewhere it can read back (a name or metadata
	// field), so `list` can return it.
	Identity   string              `json:"identity"`
	Factory    string              `json:"factory"`
	Routine    string              `json:"routine"`
	Repo       string              `json:"repo"`
	Trigger    ResourceTrigger     `json:"trigger"`
	Connectors []ResourceConnector `json:"connectors"`
	Fabrica    ResourceFabrica     `json:"fabrica"`
	// Memory is "on" or "off" on runtimes with their own memory feature
	// (memory: native), mirroring the routine's memory: key, and empty
	// elsewhere.
	Memory      string `json:"memory,omitempty"`
	Prompt      string `json:"prompt"`
	Fingerprint string `json:"fingerprint"`
}

// ResourceTrigger is the trigger to install.
type ResourceTrigger struct {
	On   string `json:"on"`
	Cron string `json:"cron,omitempty"`
}

// ResourceConnector is one connector to install.
type ResourceConnector struct {
	Name string `json:"name"`
	// Kind is "native" (use the runtime's built-in integration called Name)
	// or "mcp" (register the MCP server described by the other fields).
	Kind      string   `json:"kind"`
	URL       string   `json:"url,omitempty"`
	Transport string   `json:"transport,omitempty"`
	Auth      string   `json:"auth,omitempty"`
	Secret    string   `json:"secret,omitempty"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	Env       []string `json:"env,omitempty"`
}

// ResourceFabrica is the connection to the Fabrica server. It carries the
// factory's secret token, so programs must never log it.
type ResourceFabrica struct {
	URL       string `json:"url"`
	Placement string `json:"placement"` // "header" or "url"
	Token     string `json:"token"`
}

// RemoteResource is a resource as the runtime reports it back.
type RemoteResource struct {
	Identity string `json:"identity"`
	RemoteID string `json:"remote_id"`
	// Fingerprint is the stub fingerprint the runtime has, if it can tell.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// request is what Fabrica sends on the program's stdin.
type request struct {
	Version  int       `json:"version"`
	Op       string    `json:"op"`
	Factory  string    `json:"factory,omitempty"`
	Endpoint string    `json:"endpoint,omitempty"`
	RemoteID string    `json:"remote_id,omitempty"`
	Resource *Resource `json:"resource,omitempty"`
}

// response is what the program prints on stdout.
type response struct {
	Error     string           `json:"error,omitempty"`
	RemoteID  string           `json:"remote_id,omitempty"`
	Resources []RemoteResource `json:"resources,omitempty"`
}

// RuntimeAPI is how Fabrica creates, updates and deletes a cloud runtime's
// resources itself. Every API can also find what it made, in one of two
// ways: a Lister (find_by_identity) or a Getter (find_by_remote_id).
type RuntimeAPI interface {
	// Create creates a resource and returns the runtime's ID for it.
	Create(ctx context.Context, res Resource) (string, error)
	// Update replaces an existing resource's trigger, connectors and prompt.
	Update(ctx context.Context, remoteID string, res Resource) error
	// Delete deletes a resource. Deleting one that's already gone is fine.
	Delete(ctx context.Context, factory, remoteID string) error
}

// Lister is an API that can list every resource belonging to a factory
// endpoint, with the identity each one carries.
type Lister interface {
	List(ctx context.Context, factory, endpoint string) ([]RemoteResource, error)
}

// Getter is an API that can't list resources, but can read one back by
// its remote ID. Get returns nil, and no error, when it's gone.
type Getter interface {
	Get(ctx context.Context, remoteID string) (*RemoteResource, error)
}

// ManualSetup is an API whose resources rely on something it can't set up
// itself, so a person has to.
type ManualSetup interface {
	// Setup describes, in Markdown, what one factory's resources need set
	// up by hand, or returns "" when there's nothing to do.
	Setup(factory string, resources []Resource) string
}

// APIClient runs a folder provider's `api` program.
//
// The protocol is deliberately boring, so you can write a provider in any
// language: Fabrica runs `api <op>`, writes one JSON request to its stdin,
// and reads one JSON response from its stdout. A non-zero exit, or an
// "error" field in the response, means the operation failed. Whatever the
// program prints on stderr is shown to you when that happens.
//
// The operations are:
//
//	list    → {"resources": [{"identity", "remote_id", "fingerprint"?}]}
//	create  → {"remote_id": "…"}
//	update  → {}
//	delete  → {}
type APIClient struct {
	Path    string
	Timeout time.Duration
}

// API returns a client for the runtime's API: a folder provider's api
// program, or a built-in runtime's API client.
func (p *Provider) API() (RuntimeAPI, error) {
	switch {
	case p.APIPath != "":
		return &APIClient{Path: p.APIPath, Timeout: 2 * time.Minute}, nil
	case p.builtinAPI != nil:
		return p.builtinAPI()
	}
	return nil, fmt.Errorf("runtime %s has no api program", p.Name())
}

// List asks the runtime for every resource belonging to one factory
// endpoint, with the identity each one carries.
func (c *APIClient) List(ctx context.Context, factory, endpoint string) ([]RemoteResource, error) {
	resp, err := c.call(ctx, request{Op: "list", Factory: factory, Endpoint: endpoint})
	if err != nil {
		return nil, err
	}
	for _, r := range resp.Resources {
		if r.Identity == "" || r.RemoteID == "" {
			return nil, fmt.Errorf("%s list returned a resource without an identity or remote_id", c.Path)
		}
	}
	return resp.Resources, nil
}

// Create creates a resource and returns the runtime's ID for it.
func (c *APIClient) Create(ctx context.Context, res Resource) (string, error) {
	resp, err := c.call(ctx, request{Op: "create", Factory: res.Factory, Resource: &res})
	if err != nil {
		return "", err
	}
	if resp.RemoteID == "" {
		return "", fmt.Errorf("%s create didn't return a remote_id", c.Path)
	}
	return resp.RemoteID, nil
}

// Update replaces an existing resource's trigger, connectors and prompt.
func (c *APIClient) Update(ctx context.Context, remoteID string, res Resource) error {
	_, err := c.call(ctx, request{Op: "update", Factory: res.Factory, RemoteID: remoteID, Resource: &res})
	return err
}

// Delete deletes a resource.
func (c *APIClient) Delete(ctx context.Context, factory, remoteID string) error {
	_, err := c.call(ctx, request{Op: "delete", Factory: factory, RemoteID: remoteID})
	return err
}

func (c *APIClient) call(ctx context.Context, req request) (*response, error) {
	req.Version = ProtocolVersion
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.Path, req.Op)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	var resp response
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil && runErr == nil {
			return nil, fmt.Errorf("%s %s printed something that isn't JSON: %v", c.Path, req.Op, err)
		}
	}
	if runErr != nil || resp.Error != "" {
		msg := resp.Error
		if msg == "" {
			msg = strings.TrimSpace(stderr.String())
		}
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			msg = fmt.Sprintf("timed out after %s", timeout)
		}
		return nil, fmt.Errorf("%s %s failed: %s", c.Path, req.Op, msg)
	}
	return &resp, nil
}
