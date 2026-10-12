package testkit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// FakeCursor stands in for Cursor's Automations API: its four Connect
// calls in the JSON encoding, and the exchange of an API key for a session
// token. It keeps each automation's workflow exactly as Fabrica sent it,
// so tests can check what Cursor would get.
type FakeCursor struct {
	t   *testing.T
	URL string

	mu          sync.Mutex
	n           int
	automations map[string]*FakeAutomation
	failNext    map[string]string // method → Connect error code
}

// FakeAutomation is one automation in the fake.
type FakeAutomation struct {
	ID          string
	Name        string
	Description string
	// Workflow is the workflow as Fabrica sent it, decoded from JSON.
	Workflow map[string]any
}

// FakeCursorKey is the API key the fake accepts.
const FakeCursorKey = "key_fabrica_test"

const fakeCursorSession = "session-for-tests"

// NewFakeCursor starts the fake, and points Fabrica at it with the
// environment variables the cursor-cloud runtime reads.
func NewFakeCursor(t *testing.T) *FakeCursor {
	t.Helper()
	f := &FakeCursor{t: t, automations: map[string]*FakeAutomation{}, failNext: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	t.Setenv("CURSOR_API_KEY", FakeCursorKey)
	t.Setenv("CURSOR_TOKEN", "")
	t.Setenv("CURSOR_ENDPOINT", srv.URL)
	return f
}

// Automations lists the automations, sorted by name.
func (f *FakeCursor) Automations() []FakeAutomation {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []FakeAutomation
	for _, a := range f.automations {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name+out[i].ID < out[j].Name+out[j].ID })
	return out
}

// Put adds an automation with a prompt, like someone creating it in
// Cursor by hand, and returns its ID.
func (f *FakeCursor) Put(name, prompt string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	a := &FakeAutomation{ID: fmt.Sprintf("auto_%d", f.n), Name: name, Workflow: map[string]any{"prompts": []any{map[string]any{"prompt": prompt}}}}
	f.automations[a.ID] = a
	return a.ID
}

// Rename renames an automation, like someone editing it in Cursor.
func (f *FakeCursor) Rename(id, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.automations[id].Name = name
}

// Remove deletes an automation, like someone deleting it in Cursor.
func (f *FakeCursor) Remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.automations, id)
}

// FailNext makes the next call to method fail with a Connect error code.
func (f *FakeCursor) FailNext(method, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext[method] = code
}

func (f *FakeCursor) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/auth/exchange_user_api_key" {
		if r.Header.Get("Authorization") != "Bearer "+FakeCursorKey {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"accessToken": fakeCursorSession, "refreshToken": "unused"})
		return
	}
	method, ok := strings.CutPrefix(r.URL.Path, "/aiserver.v1.AutomationsService/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Connect-Protocol-Version") != "1" {
		connectError(w, "invalid_argument", "want a Connect unary call with the JSON encoding")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeCursorSession {
		connectError(w, "unauthenticated", "bad session token")
		return
	}
	var req struct {
		AutomationID string         `json:"automationId"`
		Name         *string        `json:"name"`
		Description  *string        `json:"description"`
		Workflow     map[string]any `json:"workflow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		connectError(w, "invalid_argument", err.Error())
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if code := f.failNext[method]; code != "" {
		delete(f.failNext, method)
		connectError(w, code, "Error")
		return
	}
	a := f.automations[req.AutomationID]
	switch method {
	case "CreateAutomation":
		f.n++
		a = &FakeAutomation{ID: fmt.Sprintf("auto_%d", f.n), Workflow: req.Workflow}
		if req.Name != nil {
			a.Name = *req.Name
		}
		if req.Description != nil {
			a.Description = *req.Description
		}
		f.automations[a.ID] = a
	case "GetAutomation", "UpdateAutomation", "DeleteAutomation":
		if a == nil {
			connectError(w, "not_found", "Error")
			return
		}
		if method == "DeleteAutomation" {
			delete(f.automations, a.ID)
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		if method == "UpdateAutomation" {
			if req.Name != nil {
				a.Name = *req.Name
			}
			if req.Description != nil {
				a.Description = *req.Description
			}
			if req.Workflow != nil {
				a.Workflow = req.Workflow
			}
		}
	default:
		connectError(w, "unimplemented", method)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow": map[string]any{
		"workflow":  map[string]any{"automationId": a.ID, "name": a.Name, "description": a.Description, "enabled": true, "workflow": a.Workflow, "createdAt": "1760000000"},
		"ownerName": "Test",
	}})
}

func connectError(w http.ResponseWriter, code, message string) {
	status := map[string]int{"not_found": http.StatusNotFound, "unauthenticated": http.StatusUnauthorized, "invalid_argument": http.StatusBadRequest}[code]
	if status == 0 {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
