package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/axllent/mailpit/config"
	"github.com/axllent/mailpit/internal/apitoken"
	"github.com/axllent/mailpit/internal/logger"
	"github.com/axllent/mailpit/internal/scope"
	"github.com/axllent/mailpit/internal/session"
)

// Sessions holds authenticated browser sessions. It is nil until
// EnableSessions is called, and while nil Mailpit behaves exactly as upstream:
// no session is required and every request is unrestricted.
var Sessions *session.Store

// APITokens holds the configured service tokens for non-interactive callers.
// Nil when none are configured, in which case only sessions authenticate.
var APITokens *apitoken.Store

// sessionPruneInterval is how often expired sessions are swept. Get already
// discards them lazily, so this only bounds memory.
const sessionPruneInterval = 10 * time.Minute

// EnableSessions turns on cookie-based authentication and starts the sweeper.
// Once called, every request must carry a valid session or be one of the
// public auth routes.
func EnableSessions(ttl time.Duration) {
	// Only mark cookies Secure when the UI is actually served over TLS,
	// otherwise the browser drops them during local development.
	secure := config.UITLSCert != "" && config.UITLSKey != ""

	Sessions = session.NewStore(ttl, secure)

	// A request that reaches a handler without a scope is now a bug rather
	// than the normal unauthenticated case, so deny it.
	scope.Enforce = true

	go func() {
		for {
			time.Sleep(sessionPruneInterval)
			if n := Sessions.Prune(); n > 0 {
				logger.Log().Debugf("[session] pruned %d expired sessions", n)
			}
		}
	}()
}

// authPathPrefix is the route group that must stay reachable without a
// session, otherwise logging in would require being logged in.
func authPathPrefix() string {
	return config.Webroot + "auth/"
}

// isPublicPath reports whether a request may proceed without a session.
func isPublicPath(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, authPathPrefix())
}

// wantsHTML reports whether the request looks like a browser navigation, as
// opposed to an API or asset call. Navigations are redirected to the login
// endpoint; everything else gets a 401 it can act on.
func wantsHTML(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}

	if strings.HasPrefix(r.URL.Path, config.Webroot+"api/") {
		return false
	}

	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// authenticateSession resolves the session cookie and attaches its scope to
// the request. It returns the request to continue with, and whether the
// request may proceed — when it may not, the response has been written.
func authenticateSession(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	if Sessions == nil {
		return r, true
	}

	if isPublicPath(r) {
		return r, true
	}

	// A bearer token is the non-interactive way in. It is checked before the
	// cookie because the two never travel together: browsers do not send
	// Authorization headers on their own, which is also why a token request
	// needs no CSRF check.
	if apitoken.Presented(r) {
		t, ok := APITokens.FromRequest(r)
		if !ok {
			logger.Log().Warnf("[apitoken] rejected an unrecognised token from %s", r.RemoteAddr)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)

			return r, false
		}

		return r.WithContext(scope.NewContext(r.Context(), t.Scope)), true
	}

	s := Sessions.FromRequest(r)
	if s == nil {
		if wantsHTML(r) {
			http.Redirect(w, r, config.Webroot+"auth/login", http.StatusFound)
			return r, false
		}

		http.Error(w, "Unauthorized", http.StatusUnauthorized)

		return r, false
	}

	return r.WithContext(scope.NewContext(r.Context(), s.Scope)), true
}

// logoutHandler ends the local session and then, when the provider supports
// it, the session at the provider too.
//
// Dropping only the local session is not a logout: the provider's SSO session
// outlives it, so the redirect to the login endpoint is answered without any
// prompt and the user lands back where they started, apparently still signed
// in. The browser must therefore visit the provider's end-session endpoint,
// which is why this ends in a redirect out of Mailpit rather than a 302 to the
// webroot.
func logoutHandler(w http.ResponseWriter, r *http.Request) {
	if Sessions == nil {
		http.Error(w, "Sessions are not enabled", http.StatusNotFound)
		return
	}

	idTokenHint := ""

	if s := Sessions.FromRequest(r); s != nil {
		idTokenHint = s.IDToken
		Sessions.Delete(s.ID)
	}

	// The cookie goes first: whatever the provider does next, this browser no
	// longer holds a usable session here.
	Sessions.ClearCookie(w, config.Webroot)

	if OIDCProvider != nil {
		cfg := OIDCProvider.Config()
		if u := OIDCProvider.EndSessionURL(idTokenHint, cfg.PostLogoutRedirectURL()); u != "" {
			http.Redirect(w, r, u, http.StatusFound)
			return
		}
	}

	http.Redirect(w, r, config.Webroot, http.StatusFound)
}
