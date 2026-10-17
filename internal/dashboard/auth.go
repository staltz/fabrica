package dashboard

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/staltz/fabrica/internal/token"
)

// remoteCookie is the cookie a remote dashboard keeps the admin token in.
const remoteCookie = "fabrica_admin"

// localCookie makes the cookie a local dashboard trades its code for: a
// name that's specific to this dashboard (cookies ignore ports, so two
// dashboards on 127.0.0.1 would otherwise log each other out) and a random
// value that isn't the code.
func localCookie(code string) (name, value string) {
	sum := sha256.Sum256([]byte(code))
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand doesn't fail on supported systems
	}
	return "fabrica_dashboard_" + hex.EncodeToString(sum[:4]), hex.EncodeToString(b)
}

// authed says whether the request carries the cookie of someone who's in.
func (h *handler) authed(r *http.Request) bool {
	switch {
	case h.remote():
		c, err := r.Cookie(remoteCookie)
		return err == nil && token.ValidAdmin(h.opts.Secret, c.Value)
	case h.session != "":
		c, err := r.Cookie(h.cookieName)
		return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(h.session)) == 1
	}
	return false // no way in was set up
}

// cookie is the dashboard's cookie, for setting or clearing. It can only
// be seen by pages of the dashboard itself, and never by scripts or other
// sites.
func (h *handler) cookie(r *http.Request, base, value string) *http.Cookie {
	name := remoteCookie
	if !h.remote() {
		name = h.cookieName
	}
	return &http.Cookie{
		Name: name, Value: value, Path: base,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: overHTTPS(r),
	}
}

// overHTTPS says whether the request came in over HTTPS, either straight to
// us or through a proxy that says so.
func overHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(first), "https")
}

// loginPage is the page with the admin token field.
type loginPage struct {
	pageBase
	Next  string // where to go after logging in
	Error string
}

// deny answers someone who isn't in: a login page on a remote dashboard, and
// on a local one the way to get a new link.
func (h *handler) deny(w http.ResponseWriter, r *http.Request, base string) {
	if h.remote() {
		h.render(w, r, http.StatusOK, "login", loginPage{pageBase: h.base("Log in", base, false), Next: nextPath(r.URL.RequestURI(), base)})
		return
	}
	h.render(w, r, http.StatusForbidden, "locked", h.base("Locked", base, false))
}

// nextPath checks a path to go to after logging in: it has to be a page of
// this dashboard, so a login link can't send anyone anywhere else. It
// returns "" for anything else.
func nextPath(next, base string) string {
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || strings.ContainsAny(next, "\\\r\n") {
		return ""
	}
	// Look at the path as a browser or proxy will once the dots are gone.
	p := path.Clean(u.Path)
	if p != base && !strings.HasPrefix(p, base+"/") {
		return ""
	}
	return (&url.URL{Path: p, RawQuery: u.RawQuery}).RequestURI()
}

// loginOrLogout handles POST …/_dashboard/login and …/logout.
func (h *handler) loginOrLogout(w http.ResponseWriter, r *http.Request, base, rest string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		if r.Method == http.MethodGet && rest == "login" { // someone opened the login address by hand
			http.Redirect(w, r, base, http.StatusSeeOther)
			return
		}
		h.message(w, http.StatusMethodNotAllowed, base, "Use the button", "Logging in and out are buttons on the dashboard, so they only answer POST.", false)
		return
	}
	if rest == "logout" {
		c := h.cookie(r, base, "")
		c.MaxAge = -1
		http.SetCookie(w, c)
		http.Redirect(w, r, base, http.StatusSeeOther)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		h.message(w, http.StatusBadRequest, base, "That didn't work", "The login form was too big or garbled. Go back and try again.", false)
		return
	}
	tok := strings.TrimSpace(r.PostForm.Get("token"))
	next := nextPath(r.PostForm.Get("next"), base)
	// A factory's token, a wrong one and none at all get the same answer.
	if !token.ValidAdmin(h.opts.Secret, tok) {
		h.render(w, r, http.StatusUnauthorized, "login", loginPage{
			pageBase: h.base("Log in", base, false), Next: next,
			Error: "That isn't the admin token for this server. `fabrica auth admin-token` prints it.",
		})
		return
	}
	http.SetCookie(w, h.cookie(r, base, tok))
	if next == "" {
		next = base
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// exchangeCode trades a local dashboard's one-time code for its cookie, and
// sends the visitor to the same page without the code in the address. The
// code works once. Someone who's already in just gets the clean address.
func (h *handler) exchangeCode(w http.ResponseWriter, r *http.Request, base string) {
	q := r.URL.Query()
	code := q.Get("code")
	q.Del("code")
	clean := *r.URL
	clean.RawQuery = q.Encode()
	clean.Fragment = ""
	if h.authed(r) {
		http.Redirect(w, r, clean.RequestURI(), http.StatusSeeOther)
		return
	}
	h.mu.Lock()
	ok := h.opts.Code != "" && !h.codeUsed && subtle.ConstantTimeCompare([]byte(code), []byte(h.opts.Code)) == 1
	if ok {
		h.codeUsed = true
	}
	h.mu.Unlock()
	if !ok {
		h.deny(w, r, base)
		return
	}
	http.SetCookie(w, h.cookie(r, base, h.session))
	http.Redirect(w, r, clean.RequestURI(), http.StatusSeeOther)
}
