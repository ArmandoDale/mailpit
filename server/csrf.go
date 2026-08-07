package server

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/axllent/mailpit/internal/apitoken"
	"github.com/axllent/mailpit/internal/logger"
)

// Cross-site request forgery is the cost of authenticating with a cookie: the
// browser attaches it to any request another site provokes, so a page the user
// visits elsewhere could delete their mail or retag it.
//
// The session cookie is SameSite=Lax, which already stops the common shapes,
// but that is one mitigation living in the browser and it is invisible from
// here. This adds a second, server-side check that can be pointed at and
// tested, which is what §27 asks for.
//
// Deliberately not a token in a hidden field: that needs the Vue UI to fetch
// and echo a value on every write, and the property that actually protects us
// — the request came from our own origin — is already stated by the browser in
// headers it will not let a page forge.

// safeMethods do not change state, so a forged one achieves nothing.
var safeMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodOptions: true,
}

// checkCSRF reports whether a state-changing request may proceed. It has
// already written the response when it may not.
//
// The check applies only to cookie-authenticated requests. A bearer token is
// never attached by the browser on its own, so a token request cannot be
// forged by another site and needs no check — which is also what keeps CI
// pipelines working from any origin.
func checkCSRF(w http.ResponseWriter, r *http.Request) bool {
	if Sessions == nil {
		// Authentication is off: upstream behaviour, nothing to protect.
		return true
	}

	if safeMethods[r.Method] {
		return true
	}

	if apitoken.Presented(r) {
		return true
	}

	if crossSite, reason := isCrossSite(r); crossSite {
		logger.Log().Warnf("[csrf] blocked a %s to %s: %s", r.Method, r.URL.Path, reason)
		http.Error(w, "Blocked: cross-site request", http.StatusForbidden)

		return false
	}

	return true
}

// isCrossSite reports whether the request came from somewhere other than
// Mailpit's own pages, and why.
func isCrossSite(r *http.Request) (bool, string) {
	// Sec-Fetch-Site is the browser's own account of where the request came
	// from, and cannot be set by page script. "none" means the user typed the
	// address or used a bookmark.
	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "same-origin", "none":
		return false, ""
	case "":
		// Older browser, or not a browser at all. Fall through to Origin.
	default:
		// Covers "cross-site" and "same-site": a sibling host under the same
		// registrable domain is still not us, and on a shared corporate domain
		// that distinction is exactly what matters.
		return true, "Sec-Fetch-Site: " + site
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin on a write means no browser: browsers have attached it to
		// state-changing requests for years. Rejecting here would break every
		// existing script and curl call without closing anything, since the
		// attack needs a browser to supply the cookie in the first place.
		return false, ""
	}

	u, err := url.Parse(origin)
	if err != nil {
		return true, "unparseable Origin"
	}

	// Compared against Host rather than a configured URL so that reaching
	// Mailpit by any name it is served under keeps working. Both sides are the
	// authority the browser used, so they match whenever the request is ours.
	if !strings.EqualFold(u.Host, r.Host) {
		return true, "Origin " + u.Host + " does not match " + r.Host
	}

	return false, ""
}
