package oidc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Provider wraps the discovery, token exchange and ID token verification for
// one identity provider.
type Provider struct {
	cfg      Config
	oauth2   oauth2.Config
	verifier *gooidc.IDTokenVerifier

	// endSession is the provider's RP-initiated logout endpoint, empty when
	// discovery does not advertise one.
	endSession string

	// ctx carries the HTTP client used for every call to the provider, so a
	// custom CA bundle applies to discovery, JWKS and token exchange alike.
	ctx context.Context
}

// Identity is what a successful login tells us about the user.
type Identity struct {
	Subject  string
	Username string
	Email    string
	Roles    []string

	// IDToken is the raw, already verified token. It is kept only to be
	// handed back as id_token_hint on logout, which is how the provider
	// knows whose session to end.
	IDToken string
}

// New performs discovery and returns a ready provider.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if !cfg.Enabled() {
		return nil, errors.New("oidc: issuer and client ID are required")
	}

	httpClient, err := httpClientFor(cfg)
	if err != nil {
		return nil, err
	}

	ctx = gooidc.ClientContext(ctx, httpClient)

	// Accept a discovery document whose issuer differs from the URL it came
	// from. Required for WSO2 tenants; see Config.InsecureSkipIssuerCheck.
	if cfg.InsecureSkipIssuerCheck {
		ctx = gooidc.InsecureIssuerURLContext(ctx, cfg.Issuer)
	}

	provider, err := gooidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discovery failed: %w", err)
	}

	scopes := append([]string{gooidc.ScopeOpenID}, cfg.Scopes...)

	// end_session_endpoint is not part of the core discovery struct, so it is
	// read from the raw document. Providers that do not implement RP-initiated
	// logout simply omit it, and logout stays local.
	var extra struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&extra); err != nil {
		return nil, fmt.Errorf("oidc: reading discovery document: %w", err)
	}

	return &Provider{
		cfg:        cfg,
		ctx:        ctx,
		endSession: extra.EndSessionEndpoint,
		oauth2: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       scopes,
		},
		verifier: provider.Verifier(&gooidc.Config{
			ClientID: cfg.ClientID,

			// The issuer check is relaxed only when configured; the
			// signature, audience and expiry checks always apply.
			SkipIssuerCheck: cfg.InsecureSkipIssuerCheck,
		}),
	}, nil
}

// httpClientFor builds the HTTP client used to reach the provider, trusting an
// extra CA when one is configured. Internal identity servers commonly present
// a corporate CA that is not in the container's trust store — the fix is to
// add the CA, never to skip verification, since this is the channel identity
// travels on.
func httpClientFor(cfg Config) (*http.Client, error) {
	client := &http.Client{Timeout: 15 * time.Second}

	if cfg.CACertFile == "" {
		return client, nil
	}

	pem, err := os.ReadFile(cfg.CACertFile)
	if err != nil {
		return nil, fmt.Errorf("oidc: reading CA bundle: %w", err)
	}

	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}

	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("oidc: no certificates found in %s", cfg.CACertFile)
	}

	client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}

	return client, nil
}

// Config returns the provider's configuration.
func (p *Provider) Config() Config {
	return p.cfg
}

// AuthCodeURL returns where to send the browser to log in. The PKCE challenge
// binds the authorization code to this specific login attempt.
func (p *Provider) AuthCodeURL(state, verifier string) string {
	return p.oauth2.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
}

// Exchange trades the authorization code for tokens and verifies the ID token.
//
// Verification happens here, in the workload itself: signature against the
// provider's JWKS, audience, expiry and algorithm. Trusting a gateway to have
// done it is what makes a service return 200 to a request that bypassed the
// gateway.
func (p *Provider) Exchange(ctx context.Context, code, verifier string) (*Identity, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.ctx.Value(oauth2.HTTPClient))

	token, err := p.oauth2.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("oidc: token exchange failed: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("oidc: response contained no id_token")
	}

	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("oidc: id_token verification failed: %w", err)
	}

	id, err := p.identityFrom(idToken)
	if err != nil {
		return nil, err
	}

	id.IDToken = rawIDToken

	return id, nil
}

// EndSessionURL returns where to send the browser to end the session at the
// provider, or "" when the provider does not support RP-initiated logout.
//
// Without this step logout is only local: the provider's SSO session survives,
// so the next authorization request is answered without asking for credentials
// and the user is silently signed back in.
func (p *Provider) EndSessionURL(idTokenHint, postLogoutRedirectURI string) string {
	if p.endSession == "" {
		return ""
	}

	u, err := url.Parse(p.endSession)
	if err != nil {
		return ""
	}

	q := u.Query()

	// id_token_hint identifies the session to end. client_id is sent as well
	// because a provider that cannot read the hint still needs to know which
	// client's post-logout redirect URI to validate.
	if idTokenHint != "" {
		q.Set("id_token_hint", idTokenHint)
	}

	q.Set("client_id", p.cfg.ClientID)

	if postLogoutRedirectURI != "" {
		q.Set("post_logout_redirect_uri", postLogoutRedirectURI)
	}

	u.RawQuery = q.Encode()

	return u.String()
}

// identityFrom extracts the fields we care about from a verified token.
func (p *Provider) identityFrom(idToken *gooidc.IDToken) (*Identity, error) {
	// Decode into a map so the roles claim can be read under whatever name
	// it was configured with, and in whichever shape it arrives.
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc: decoding claims: %w", err)
	}

	id := &Identity{Subject: idToken.Subject}

	for _, k := range []string{"preferred_username", "username", "upn", "email", "name"} {
		if v, ok := claims[k].(string); ok && v != "" {
			id.Username = v
			break
		}
	}

	if v, ok := claims["email"].(string); ok {
		id.Email = v
	}

	id.Roles = rolesFromClaims(claims, p.cfg)

	return id, nil
}

// rolesFromClaims reads the configured roles claim, tolerating both the array
// and separated-string encodings.
func rolesFromClaims(claims map[string]any, cfg Config) []string {
	raw, ok := claims[cfg.RolesClaim]
	if !ok {
		// Absent, not empty: providers that populate claims on a best-effort
		// basis omit the key when the user has no value for it.
		return nil
	}

	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}

		return clean(out)

	case string:
		if cfg.RoleSeparator == "" {
			return clean([]string{v})
		}

		return clean(strings.Split(v, cfg.RoleSeparator))
	}

	return nil
}
