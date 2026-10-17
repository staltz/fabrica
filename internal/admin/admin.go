// Package admin is the admin API of a Fabrica server: a read-only JSON view
// of run history, for people. Agents write run history and never read it,
// so this is a package of its own, away from the MCP server that agents
// talk to, and only the admin token opens it.
//
//	GET …/_admin/runs            a page of runs, newest first
//	GET …/_admin/runs/<run_id>   one run in full
//
// The remote server hands it every path with a /_admin/ segment, wherever
// that sits, so it works under any path prefix (for example when `server:`
// is https://acme.dev/fabrica). See specs/v2-run-history.md.
package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/token"
)

// segment is what marks an admin path. Factory names can't contain an
// underscore, so no factory endpoint has it.
const segment = "/_admin"

// Matches reports whether a URL path belongs to the admin API: it has a
// /_admin/ segment. When it has several, the last one counts.
func Matches(urlPath string) bool {
	_, ok := route(urlPath)
	return ok
}

// route cuts a URL path at its last /_admin/ segment, and returns what
// comes after it: "runs", or "runs/<run_id>".
func route(urlPath string) (rest string, ok bool) {
	p := path.Clean("/" + urlPath)
	if i := strings.LastIndex(p, segment+"/"); i >= 0 {
		return p[i+len(segment)+1:], true
	}
	if strings.HasSuffix(p, segment) {
		return "", true
	}
	return "", false
}

type handler struct {
	db     *runs.DB
	secret string
	now    func() time.Time
}

// New makes the admin API of the server that has this secret, reading run
// history from db.
func New(db *runs.DB, secret string) http.Handler {
	return &handler{db: db, secret: secret, now: time.Now}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Run history is private, so nothing should keep a copy.
	w.Header().Set("Cache-Control", "no-store")
	rest, ok := route(r.URL.Path)
	if !ok {
		fail(w, http.StatusNotFound, "there's no admin API at "+r.URL.Path)
		return
	}
	// The token only travels in a header: in a URL it would end up in
	// proxy logs. A factory's token is no use here, and gets the same answer
	// as no token at all.
	tok, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !bearer || !token.ValidAdmin(h.secret, strings.TrimSpace(tok)) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="fabrica-admin"`)
		fail(w, http.StatusUnauthorized, "missing or wrong admin token; send it in an Authorization: Bearer header (`fabrica auth admin-token` prints it)")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		fail(w, http.StatusMethodNotAllowed, "the admin API is read-only; use GET")
		return
	}
	id, isRun := strings.CutPrefix(rest, "runs/")
	switch {
	case rest == "runs":
		h.list(w, r)
	case isRun && id != "" && !strings.Contains(id, "/"):
		h.detail(w, r, id)
	default:
		fail(w, http.StatusNotFound, fmt.Sprintf("there's no admin route %q; try runs and runs/<run_id>", rest))
	}
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	f, err := h.filter(r.URL.Query())
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	list, next, err := h.db.List(r.Context(), f)
	if err != nil {
		fail(w, http.StatusInternalServerError, "couldn't read run history: "+err.Error())
		return
	}
	send(w, runs.NewPage(list, next))
}

func (h *handler) detail(w http.ResponseWriter, r *http.Request, id string) {
	d, err := h.db.Detail(r.Context(), id)
	switch {
	case errors.Is(err, runs.ErrNoRun):
		fail(w, http.StatusNotFound, "there's no run "+id)
	case err != nil:
		fail(w, http.StatusInternalServerError, "couldn't read run history: "+err.Error())
	default:
		send(w, d)
	}
}

// filter reads the query parameters of GET …/_admin/runs.
func (h *handler) filter(q url.Values) (runs.Filter, error) {
	f := runs.Filter{
		Factory: q.Get("factory"), Routine: q.Get("routine"), Repo: q.Get("repo"), Runtime: q.Get("runtime"),
		Before: q.Get("before"),
	}
	for _, list := range q["outcome"] {
		for _, o := range strings.Split(list, ",") {
			if o = strings.TrimSpace(o); o != "" {
				f.Outcomes = append(f.Outcomes, o)
			}
		}
	}
	f.Since = h.now().Add(-runs.DefaultSince)
	if since := q.Get("since"); since != "" {
		var err error
		if f.Since, err = runs.ParseSince(since, h.now()); err != nil {
			return f, err
		}
	}
	if limit := q.Get("limit"); limit != "" {
		n, err := strconv.Atoi(limit)
		if err != nil {
			return f, fmt.Errorf("limit %q isn't a number; it has to be from 1 to %d", limit, runs.MaxLimit)
		}
		if err := runs.CheckLimit(n); err != nil {
			return f, err
		}
		f.Limit = n
	}
	return f, f.Validate()
}

func send(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
