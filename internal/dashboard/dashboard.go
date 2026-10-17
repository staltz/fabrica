// Package dashboard is the run-history dashboard: server-rendered pages with
// charts and tables, for people to look at how their routines are doing.
//
//	GET …/_dashboard                          the overview
//	GET …/_dashboard/routines/<factory>/<routine>   one routine
//	GET …/_dashboard/runs/<run_id>            one run
//
// One handler serves two places: `fabrica dashboard` on your machine, and
// the remote server. They differ only in who gets in. See
// specs/v2-run-history.md.
//
// Agents write run history and only people read it, so this package is
// operator-plane code: the MCP server only routes to it (see
// internal/mcpserver/remote.go), and never reads run history itself.
//
// The pages need no JavaScript and load nothing from the internet. Charts
// are inline SVG made in Go (charts.go), each with its numbers in a table.
// Run data, which agents write, is escaped everywhere, and it only becomes a
// link when it's an http(s) URL.
package dashboard

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/staltz/fabrica/internal/runs"
)

// segment is what marks a dashboard path. Factory names can't contain an
// underscore, so no factory endpoint has it.
const segment = "/_dashboard"

// Matches reports whether a URL path belongs to the dashboard: it ends in
// /_dashboard or has a /_dashboard/ segment. When it has several, the last
// one counts.
func Matches(urlPath string) bool {
	_, _, ok := route(urlPath)
	return ok
}

// route cuts a URL path at its last /_dashboard segment. The prefix is what
// comes before it (empty at the root, /fabrica when the server lives under
// that path), and rest is what comes after: "", "routines/acme/pr-review",
// "runs/<run_id>", "login" and so on.
func route(urlPath string) (prefix, rest string, ok bool) {
	p := path.Clean("/" + urlPath)
	if i := strings.LastIndex(p, segment+"/"); i >= 0 {
		return p[:i], p[i+len(segment)+1:], true
	}
	if strings.HasSuffix(p, segment) {
		return strings.TrimSuffix(p, segment), "", true
	}
	return "", "", false
}

// Options say who may see the dashboard, and add the bits that only the
// place it runs knows.
type Options struct {
	// Secret is the server secret of a remote server. Visitors log in with
	// the admin token computed from it, and nothing else gets them in.
	// Exactly one of Secret and Codes should be set; if neither is, nobody
	// gets in.
	Secret string
	// Codes are the one-time codes of a local dashboard: visiting
	// …/_dashboard?code=<code> trades a code it issued for a cookie, once.
	// The owner can issue more while the dashboard runs.
	Codes *Codes
	// Subject finds the subject line of a blueprint commit, for the tooltips
	// on a routine's chart. It returns "" when it can't, and the page goes on
	// without. It's optional.
	Subject func(ctx context.Context, factory, sha string) string
	// Now is the clock. The default is time.Now.
	Now func() time.Time
	// Logger gets the errors that don't fit in a page. The default is to
	// drop them.
	Logger *slog.Logger
}

// csp is the Content-Security-Policy of every response: no scripts, no
// fonts, no connections, no images but data: ones, no frames, and forms
// that only go back here.
const csp = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'self'"

type handler struct {
	db   *runs.DB
	opts Options
	now  func() time.Time
	log  *slog.Logger

	pages map[string]*template.Template

	// Local dashboards only: the codes, and the cookie they're traded for.
	codes *Codes
}

//go:embed templates/*.html templates/style.css
var files embed.FS

// New makes the dashboard of the run history in db.
func New(db *runs.DB, opts Options) http.Handler {
	h := &handler{db: db, opts: opts, now: opts.Now, log: opts.Logger, pages: map[string]*template.Template{}}
	if h.now == nil {
		h.now = time.Now
	}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	if opts.Secret == "" {
		h.codes = opts.Codes
	}
	css, err := files.ReadFile("templates/style.css")
	if err != nil {
		panic(err) // embedded, so this can't happen
	}
	funcs := template.FuncMap{"css": func() template.CSS { return template.CSS(css) }, "outcomeLabel": outcomeLabel}
	for _, name := range []string{"overview", "routine", "run", "login", "locked", "message"} {
		h.pages[name] = template.Must(template.New(name).Funcs(funcs).ParseFS(files, "templates/layout.html", "templates/parts.html", "templates/"+name+".html"))
	}
	return h
}

func (h *handler) remote() bool { return h.opts.Secret != "" }

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Run history is private: nothing should keep a copy, nothing should run
	// from it, and nothing should learn the address of this page.
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("Content-Security-Policy", csp)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Referrer-Policy", "no-referrer")

	prefix, rest, ok := route(r.URL.Path)
	if !ok {
		h.message(w, http.StatusNotFound, segment, "Not found", "There's no dashboard at "+r.URL.Path+".", false)
		return
	}
	base := prefix + segment

	if h.remote() && (rest == "login" || rest == "logout") {
		h.loginOrLogout(w, r, base, rest)
		return
	}
	if !h.remote() && r.URL.Query().Get("code") != "" {
		h.exchangeCode(w, r, base)
		return
	}
	if !h.authed(r) {
		h.deny(w, r, base)
		return
	}
	// Everything else only looks.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		h.message(w, http.StatusMethodNotAllowed, base, "Read-only", "The dashboard only shows run history, so it only answers GET.", true)
		return
	}

	parts := strings.Split(rest, "/")
	switch {
	case rest == "":
		h.overview(w, r, base)
	case len(parts) == 3 && parts[0] == "routines" && parts[1] != "" && parts[2] != "":
		h.routine(w, r, base, parts[1], parts[2])
	case len(parts) == 2 && parts[0] == "runs" && parts[1] != "":
		h.run(w, r, base, parts[1])
	default:
		h.notFound(w, base, "There's no page here. Try the overview.")
	}
}

// pageBase is what every page has: its title, and where the dashboard lives.
type pageBase struct {
	Title string
	// Base is the path of the dashboard's root, like /_dashboard or
	// /fabrica/_dashboard. Links start with it.
	Base string
	// LogOut says to offer a way out. Only a remote dashboard has one.
	LogOut bool
}

func (h *handler) base(title, base string, loggedIn bool) pageBase {
	return pageBase{Title: title + " · Fabrica", Base: base, LogOut: loggedIn && h.remote()}
}

// render executes a page, all or nothing.
func (h *handler) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	var buf bytes.Buffer
	if err := h.pages[name].ExecuteTemplate(&buf, "layout", data); err != nil {
		h.log.Error("couldn't render a dashboard page", "page", name, "error", err)
		http.Error(w, "couldn't render this page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r == nil || r.Method != http.MethodHead {
		_, _ = buf.WriteTo(w)
	}
}

// message shows a plain page: not found, a failure, and the like.
func (h *handler) message(w http.ResponseWriter, status int, base, title, text string, loggedIn bool) {
	data := struct {
		pageBase
		Heading, Text string
	}{h.base(title, base, loggedIn), title, text}
	h.render(w, nil, status, "message", data)
}

// failed shows a page for an error reading run history.
func (h *handler) failed(w http.ResponseWriter, base string, err error) {
	h.log.Error("couldn't read run history for the dashboard", "error", err)
	h.message(w, http.StatusInternalServerError, base, "Couldn't read run history", err.Error(), true)
}

func (h *handler) notFound(w http.ResponseWriter, base, text string) {
	h.message(w, http.StatusNotFound, base, "Not found", text, true)
}
