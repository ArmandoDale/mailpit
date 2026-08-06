package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/axllent/mailpit/config"
	"github.com/axllent/mailpit/internal/oidc"
	"github.com/axllent/mailpit/internal/scope"
	"github.com/axllent/mailpit/internal/storage"
	jose "github.com/go-jose/go-jose/v4"
)

// fakeOP is a minimal but real OpenID Provider: it publishes discovery and a
// JWKS, and signs ID tokens with an RSA key. Mailpit verifies those signatures
// for real, so this exercises the whole callback path rather than stubbing it.
//
// It exists alongside the Keycloak compose file because it can reproduce two
// WSO2 behaviours Keycloak cannot:
//   - an issuer that differs from the discovery URL (tenant-qualified WSO2)
//   - a roles claim serialised as a separated string instead of an array
type fakeOP struct {
	server *httptest.Server
	key    *rsa.PrivateKey

	// issuerOverride, when set, is published as "issuer" instead of the
	// server's own URL — the WSO2 tenant mismatch.
	issuerOverride string

	// roles is what the next issued token will carry, in whichever shape
	// rolesAsString dictates.
	roles         []string
	rolesAsString bool

	// noEndSession omits end_session_endpoint from discovery, as a provider
	// without RP-initiated logout would.
	noEndSession bool
}

func newFakeOP(t *testing.T) *fakeOP {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	op := &fakeOP{key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", op.discovery)
	mux.HandleFunc("/jwks", op.jwks)
	mux.HandleFunc("/token", op.token)

	op.server = httptest.NewServer(mux)
	t.Cleanup(op.server.Close)

	return op
}

// issuer is what the provider claims to be, which is not necessarily where it
// is served from.
func (op *fakeOP) issuer() string {
	if op.issuerOverride != "" {
		return op.issuerOverride
	}

	return op.server.URL
}

func (op *fakeOP) discovery(w http.ResponseWriter, _ *http.Request) {
	doc := map[string]any{
		"issuer":                                op.issuer(),
		"authorization_endpoint":                op.server.URL + "/authorize",
		"token_endpoint":                        op.server.URL + "/token",
		"jwks_uri":                              op.server.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	}

	if !op.noEndSession {
		doc["end_session_endpoint"] = op.server.URL + "/logout"
	}

	writeTestJSON(w, doc)
}

func (op *fakeOP) jwks(w http.ResponseWriter, _ *http.Request) {
	writeTestJSON(w, jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{{
			Key:       op.key.Public(),
			KeyID:     "test-key",
			Algorithm: "RS256",
			Use:       "sig",
		}},
	})
}

// token issues a signed ID token for whatever code it is given. Validating the
// code itself is the provider's job, not something under test here.
func (op *fakeOP) token(w http.ResponseWriter, _ *http.Request) {
	claims := map[string]any{
		"iss":                op.issuer(),
		"aud":                "mailpit",
		"sub":                "user-subject-1",
		"preferred_username": "tester",
		"email":              "tester@example.it",
		"exp":                time.Now().Add(time.Hour).Unix(),
		"iat":                time.Now().Unix(),
	}

	if op.roles != nil {
		if op.rolesAsString {
			claims["groups"] = strings.Join(op.roles, ",")
		} else {
			claims["groups"] = op.roles
		}
	}

	writeTestJSON(w, map[string]any{
		"access_token": "opaque-access-token",
		"token_type":   "Bearer",
		"id_token":     op.signIDToken(claims),
	})
}

func (op *fakeOP) signIDToken(claims map[string]any) string {
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: op.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"),
	)
	if err != nil {
		panic(err)
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}

	obj, err := signer.Sign(payload)
	if err != nil {
		panic(err)
	}

	s, err := obj.CompactSerialize()
	if err != nil {
		panic(err)
	}

	return s
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// enableTestOIDC points Mailpit at the fake provider.
func enableTestOIDC(t *testing.T, op *fakeOP, mutate func(*oidc.Config)) {
	t.Helper()

	cfg := oidc.Defaults()
	cfg.Issuer = op.server.URL
	cfg.ClientID = "mailpit"
	cfg.ClientSecret = "secret"
	cfg.RedirectURL = "http://mailpit.test/auth/callback"

	if mutate != nil {
		mutate(&cfg)
	}

	if err := EnableOIDC(context.Background(), cfg); err != nil {
		t.Fatalf("enabling OIDC: %s", err)
	}

	t.Cleanup(func() {
		OIDCProvider = nil
		Sessions = nil
		scope.Enforce = false
	})
}

// login drives the whole flow: it calls /auth/login, follows the state cookie
// to the callback as the provider's redirect would, and returns the response.
func login(t *testing.T, ts *httptest.Server) *http.Response {
	t.Helper()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	res, err := client.Get(ts.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	var stateCookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == loginStateCookie {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("/auth/login did not set the login state cookie")
	}

	// The state the provider will echo back is the first half of the cookie.
	state, _, ok := strings.Cut(stateCookie.Value, ":")
	if !ok {
		t.Fatal("malformed login state cookie")
	}

	req, err := http.NewRequest(http.MethodGet,
		ts.URL+"/auth/callback?code=any-code&state="+url.QueryEscape(state), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(stateCookie)

	cb, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	return cb
}

func sessionCookieFrom(res *http.Response) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == "mp_session" && c.Value != "" {
			return c
		}
	}

	return nil
}

func TestOIDCLoginCreatesScopedSession(t *testing.T) {
	setup()
	defer storage.Close()

	op := newFakeOP(t)
	op.roles = []string{"ICTIPZS/mailpit-progetto-alfa", "ICTIPZS/Domain Users"}

	enableTestOIDC(t, op, nil)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := login(t, ts)
	defer res.Body.Close()

	if res.StatusCode != http.StatusFound {
		t.Fatalf("callback returned %d, want 302", res.StatusCode)
	}

	c := sessionCookieFrom(res)
	if c == nil {
		t.Fatal("no session cookie issued")
	}

	if !c.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}

	s := Sessions.Get(c.Value)
	if s == nil {
		t.Fatal("session not stored")
	}

	if s.Username != "tester" {
		t.Errorf("username = %q, want tester", s.Username)
	}

	if s.Scope.IsUnrestricted() {
		t.Fatal("a project user must not get an unrestricted scope")
	}

	if !s.Scope.AllowsTags([]string{"progetto-alfa"}) {
		t.Error("own project not visible: the ICTIPZS/ prefix was probably not stripped")
	}

	if s.Scope.AllowsTags([]string{"progetto-beta"}) {
		t.Error("LEAK: another project is visible")
	}
}

// TestOIDCRolesAsSeparatedString covers the WSO2 encoding Keycloak never
// produces: the roles claim as one comma-separated string.
func TestOIDCRolesAsSeparatedString(t *testing.T) {
	setup()
	defer storage.Close()

	op := newFakeOP(t)
	op.roles = []string{"ICTIPZS/mailpit-progetto-alfa", "ICTIPZS/mailpit-progetto-beta"}
	op.rolesAsString = true

	enableTestOIDC(t, op, nil)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := login(t, ts)
	defer res.Body.Close()

	c := sessionCookieFrom(res)
	if c == nil {
		t.Fatal("no session cookie issued")
	}

	s := Sessions.Get(c.Value)
	if s == nil {
		t.Fatal("session not stored")
	}

	for _, tag := range []string{"progetto-alfa", "progetto-beta"} {
		if !s.Scope.AllowsTags([]string{tag}) {
			t.Errorf("tag %q missing: the separated-string claim was not parsed", tag)
		}
	}
}

// TestOIDCUserWithoutRoles is the failure that must not become admin.
func TestOIDCUserWithoutRoles(t *testing.T) {
	setup()
	defer storage.Close()

	op := newFakeOP(t)
	op.roles = []string{"ICTIPZS/Domain Users"}

	enableTestOIDC(t, op, nil)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := login(t, ts)
	defer res.Body.Close()

	c := sessionCookieFrom(res)
	if c == nil {
		t.Fatal("no session cookie issued")
	}

	s := Sessions.Get(c.Value)
	if s.Scope.IsUnrestricted() {
		t.Fatal("a user with no Mailpit role must not see everything")
	}
	if s.Scope.AllowsTags([]string{"progetto-alfa"}) {
		t.Error("LEAK: a user with no role can see a project")
	}
	if !s.Scope.AllowsTags(nil) {
		t.Error("untagged mail should stay visible")
	}
}

func TestOIDCAdminRole(t *testing.T) {
	setup()
	defer storage.Close()

	op := newFakeOP(t)
	op.roles = []string{"ICTIPZS/mailpit-admin"}

	enableTestOIDC(t, op, nil)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := login(t, ts)
	defer res.Body.Close()

	s := Sessions.Get(sessionCookieFrom(res).Value)
	if !s.Scope.IsUnrestricted() {
		t.Error("the admin role must grant an unrestricted scope")
	}
}

// TestOIDCIssuerMismatch reproduces the WSO2 tenant behaviour: discovery is
// served under /t/<tenant>/... but reports the root issuer. Without the
// override go-oidc refuses the provider outright.
func TestOIDCIssuerMismatch(t *testing.T) {
	setup()
	defer storage.Close()

	op := newFakeOP(t)
	op.issuerOverride = "https://idserver.example.it/oauth2/token"
	op.roles = []string{"ICTIPZS/mailpit-progetto-alfa"}

	t.Run("fails without the override", func(t *testing.T) {
		cfg := oidc.Defaults()
		cfg.Issuer = op.server.URL
		cfg.ClientID = "mailpit"
		cfg.RedirectURL = "http://mailpit.test/auth/callback"

		if _, err := oidc.New(context.Background(), cfg); err == nil {
			t.Fatal("expected discovery to reject the mismatched issuer")
		}
	})

	t.Run("succeeds with the override, still verifying the signature", func(t *testing.T) {
		enableTestOIDC(t, op, func(c *oidc.Config) {
			c.InsecureSkipIssuerCheck = true
		})

		ts := httptest.NewServer(apiRoutes())
		defer ts.Close()

		res := login(t, ts)
		defer res.Body.Close()

		c := sessionCookieFrom(res)
		if c == nil {
			t.Fatal("login failed despite the issuer override")
		}

		if !Sessions.Get(c.Value).Scope.AllowsTags([]string{"progetto-alfa"}) {
			t.Error("scope not derived from the token")
		}
	})
}

// TestOIDCStateMismatch: a callback carrying somebody else's state must not
// open a session.
func TestOIDCStateMismatch(t *testing.T) {
	setup()
	defer storage.Close()

	op := newFakeOP(t)
	op.roles = []string{"ICTIPZS/mailpit-admin"}

	enableTestOIDC(t, op, nil)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	res, err := client.Get(ts.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	var stateCookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == loginStateCookie {
			stateCookie = c
		}
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/auth/callback?code=x&state=not-the-issued-state", nil)
	req.AddCookie(stateCookie)

	cb, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Body.Close()

	if cb.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400 for a mismatched state", cb.StatusCode)
	}

	if sessionCookieFrom(cb) != nil {
		t.Error("a session was opened despite the state mismatch")
	}
}

// TestOIDCCallbackWithoutState rejects a bare callback, which is what a
// cross-site attempt to inject a code looks like.
func TestOIDCCallbackWithoutState(t *testing.T) {
	setup()
	defer storage.Close()

	op := newFakeOP(t)
	enableTestOIDC(t, op, nil)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/auth/callback?code=x&state=y")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400 without a login state cookie", res.StatusCode)
	}
}

// TestOIDCEndToEndIsolation is the whole point: log in as a project user and
// confirm the API only returns that project's mail.
func TestOIDCEndToEndIsolation(t *testing.T) {
	setup()
	defer storage.Close()

	mine := storeTestMessage(t, "mine", []string{"progetto-alfa"})
	theirs := storeTestMessage(t, "theirs", []string{"progetto-beta"})

	op := newFakeOP(t)
	op.roles = []string{"ICTIPZS/mailpit-progetto-alfa"}

	enableTestOIDC(t, op, nil)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := login(t, ts)
	defer res.Body.Close()

	cookie := sessionCookieFrom(res)
	if cookie == nil {
		t.Fatal("login failed")
	}

	listRes := get(t, ts, "/api/v1/messages", cookie)
	defer listRes.Body.Close()

	var list struct {
		Messages []struct {
			ID string
		}
	}
	if err := json.NewDecoder(listRes.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}

	ids := map[string]bool{}
	for _, m := range list.Messages {
		ids[m.ID] = true
	}

	if !ids[mine] {
		t.Error("own message missing from the list")
	}
	if ids[theirs] {
		t.Error("LEAK: another project's message is listed after login")
	}

	byID := get(t, ts, "/api/v1/message/"+theirs, cookie)
	defer byID.Body.Close()

	if byID.StatusCode != http.StatusNotFound {
		t.Errorf("fetching another project's message returned %d, want 404", byID.StatusCode)
	}
}

// TestOIDCLogoutEndsProviderSession is the regression test for a logout that
// only looked like one: dropping the local session while the provider's SSO
// session survived meant the next navigation was answered without a prompt and
// the user was signed straight back in.
func TestOIDCLogoutEndsProviderSession(t *testing.T) {
	setup()
	defer storage.Close()

	op := newFakeOP(t)
	op.roles = []string{"ICTIPZS/mailpit-progetto-alfa"}
	enableTestOIDC(t, op, nil)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := login(t, ts)
	defer res.Body.Close()

	cookie := sessionCookieFrom(res)
	if cookie == nil {
		t.Fatal("login did not set a session cookie")
	}

	before := Sessions.Count()

	out := get(t, ts, "/auth/logout", cookie)
	defer out.Body.Close()

	if out.StatusCode != http.StatusFound {
		t.Fatalf("logout returned %d, want 302", out.StatusCode)
	}

	loc := out.Header.Get("Location")
	if !strings.HasPrefix(loc, op.server.URL+"/logout") {
		t.Fatalf("logout redirected to %q, want the provider's end-session endpoint", loc)
	}

	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}

	q := u.Query()

	// Without the hint the provider cannot tell which session to end.
	if q.Get("id_token_hint") == "" {
		t.Error("end-session URL carries no id_token_hint")
	}

	if got, want := q.Get("post_logout_redirect_uri"), "http://mailpit.test/"; got != want {
		t.Errorf("post_logout_redirect_uri = %q, want %q", got, want)
	}

	if q.Get("client_id") != "mailpit" {
		t.Errorf("client_id = %q, want mailpit", q.Get("client_id"))
	}

	// The local session must be gone regardless of what the provider does.
	if after := Sessions.Count(); after != before-1 {
		t.Errorf("session count %d after logout, want %d", after, before-1)
	}

	var cleared bool
	for _, c := range out.Cookies() {
		if c.Name == "mp_session" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout did not expire the session cookie")
	}

	api := get(t, ts, "/api/v1/messages", cookie)
	defer api.Body.Close()

	if api.StatusCode != http.StatusUnauthorized {
		t.Errorf("the old cookie still returns %d, want 401", api.StatusCode)
	}
}

// TestOIDCLogoutWithoutEndSessionEndpoint covers providers that do not
// implement RP-initiated logout: the local session still ends, and the user is
// returned to the webroot rather than sent nowhere.
func TestOIDCLogoutWithoutEndSessionEndpoint(t *testing.T) {
	setup()
	defer storage.Close()

	op := newFakeOP(t)
	op.noEndSession = true
	op.roles = []string{"ICTIPZS/mailpit-progetto-alfa"}
	enableTestOIDC(t, op, nil)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := login(t, ts)
	defer res.Body.Close()

	cookie := sessionCookieFrom(res)
	if cookie == nil {
		t.Fatal("login did not set a session cookie")
	}

	out := get(t, ts, "/auth/logout", cookie)
	defer out.Body.Close()

	if got := out.Header.Get("Location"); got != config.Webroot {
		t.Errorf("logout redirected to %q, want the webroot %q", got, config.Webroot)
	}

	api := get(t, ts, "/api/v1/messages", cookie)
	defer api.Body.Close()

	if api.StatusCode != http.StatusUnauthorized {
		t.Errorf("the old cookie still returns %d, want 401", api.StatusCode)
	}
}
