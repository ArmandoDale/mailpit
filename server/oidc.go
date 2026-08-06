package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/axllent/mailpit/config"
	"github.com/axllent/mailpit/internal/logger"
	"github.com/axllent/mailpit/internal/oidc"
	"golang.org/x/oauth2"
)

// OIDCProvider is the configured identity provider, nil when OIDC is off.
var OIDCProvider *oidc.Provider

// loginStateCookie holds the CSRF state and the PKCE verifier between the
// redirect to the provider and the callback.
//
// It is SameSite=Lax on purpose: the callback arrives as a top-level GET
// navigation from the provider's domain, which Lax allows and Strict would
// block, silently breaking every login.
const loginStateCookie = "mp_oidc_login"

// EnableOIDC performs discovery and turns on session authentication.
func EnableOIDC(ctx context.Context, cfg oidc.Config) error {
	p, err := oidc.New(ctx, cfg)
	if err != nil {
		return err
	}

	OIDCProvider = p
	EnableSessions(cfg.SessionTTL)

	logger.Log().Infof("[oidc] authentication enabled against %s", cfg.Issuer)

	return nil
}

// loginHandler starts the authorization code flow.
func loginHandler(w http.ResponseWriter, r *http.Request) {
	if OIDCProvider == nil {
		http.Error(w, "Authentication is not configured", http.StatusNotFound)
		return
	}

	// Already signed in: nothing to do.
	if Sessions != nil && Sessions.FromRequest(r) != nil {
		http.Redirect(w, r, config.Webroot, http.StatusFound)
		return
	}

	state := oauth2.GenerateVerifier()
	verifier := oauth2.GenerateVerifier()

	http.SetCookie(w, &http.Cookie{
		Name:     loginStateCookie,
		Value:    state + ":" + verifier,
		Path:     config.Webroot,
		MaxAge:   600, // a login attempt that takes longer has been abandoned
		HttpOnly: true,
		Secure:   config.UITLSCert != "" && config.UITLSKey != "",
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, OIDCProvider.AuthCodeURL(state, verifier), http.StatusFound)
}

// callbackHandler completes the flow: it verifies the state, exchanges the
// code, verifies the ID token and opens a session.
func callbackHandler(w http.ResponseWriter, r *http.Request) {
	if OIDCProvider == nil || Sessions == nil {
		http.Error(w, "Authentication is not configured", http.StatusNotFound)
		return
	}

	// Surface an error the provider reported rather than failing obscurely
	// further down.
	if e := r.URL.Query().Get("error"); e != "" {
		logger.Log().Warnf("[oidc] provider returned error: %s (%s)", e, r.URL.Query().Get("error_description"))
		http.Error(w, "Authentication failed", http.StatusUnauthorized)

		return
	}

	c, err := r.Cookie(loginStateCookie)
	if err != nil || c.Value == "" {
		http.Error(w, "Login session expired, please try again", http.StatusBadRequest)
		return
	}

	// The cookie is consumed whatever happens next: a login attempt is
	// single-use.
	clearLoginState(w)

	state, verifier, ok := strings.Cut(c.Value, ":")
	if !ok {
		http.Error(w, "Malformed login state", http.StatusBadRequest)
		return
	}

	// Comparing the state we issued with the one returned is what stops a
	// third party from feeding us an authorization code of their own.
	if r.URL.Query().Get("state") != state {
		logger.Log().Warn("[oidc] state mismatch on callback")
		http.Error(w, "Invalid login state", http.StatusBadRequest)

		return
	}

	identity, err := OIDCProvider.Exchange(r.Context(), r.URL.Query().Get("code"), verifier)
	if err != nil {
		logger.Log().Errorf("[oidc] %s", err.Error())
		http.Error(w, "Authentication failed", http.StatusUnauthorized)

		return
	}

	sc := oidc.ScopeForRoles(identity.Roles, OIDCProvider.Config())

	s, err := Sessions.Create(identity.Subject, identity.Username, identity.IDToken, identity.Roles, sc)
	if err != nil {
		logger.Log().Errorf("[oidc] creating session: %s", err.Error())
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)

		return
	}

	Sessions.SetCookie(w, s, config.Webroot)

	if sc.IsUnrestricted() {
		logger.Log().Debugf("[oidc] %s signed in as admin", identity.Username)
	} else {
		logger.Log().Debugf("[oidc] %s signed in with tags %v", identity.Username, sc.Tags())
	}

	http.Redirect(w, r, config.Webroot, http.StatusFound)
}

func clearLoginState(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     loginStateCookie,
		Value:    "",
		Path:     config.Webroot,
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// sessionInfoHandler tells the UI who is signed in and what they can see, so
// it can preset the tag filter and hide admin-only controls.
func sessionInfoHandler(w http.ResponseWriter, r *http.Request) {
	type response struct {
		// Enabled tells the UI whether authentication is configured at all.
		// Without it the UI cannot distinguish "signed in as an admin" from
		// "no authentication", and would show a sign-out button that does
		// nothing.
		Enabled       bool     `json:"enabled"`
		Authenticated bool     `json:"authenticated"`
		Username      string   `json:"username,omitempty"`
		Roles         []string `json:"roles,omitempty"`
		Tags          []string `json:"tags,omitempty"`
		Admin         bool     `json:"admin"`
	}

	res := response{Enabled: Sessions != nil}

	if Sessions != nil {
		if s := Sessions.FromRequest(r); s != nil {
			res.Authenticated = true
			res.Username = s.Username
			res.Roles = s.Roles
			res.Admin = s.Scope.IsUnrestricted()
			res.Tags = s.Scope.Tags()
		}
	} else {
		// Authentication is off: the UI behaves as upstream.
		res.Authenticated = true
		res.Admin = true
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}
