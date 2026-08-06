// Package oidc authenticates users against an OpenID Connect provider and
// turns the roles it returns into a visibility scope.
//
// The flow runs entirely server-side: the browser never sees a token, only an
// opaque session cookie. That keeps the token out of JavaScript and lets the
// WebSocket authenticate on its handshake like any other request.
package oidc

import (
	"strings"
	"time"
)

// Config describes how to talk to the identity provider and how to read the
// roles it returns.
//
// Everything provider-specific lives here rather than in code, so moving from
// a local Keycloak to WSO2 is a configuration change. The defaults match a
// plain Keycloak; the WSO2-specific values are documented on each field.
type Config struct {
	// Issuer is the OIDC issuer URL, used for discovery.
	Issuer string

	// ClientID and ClientSecret identify Mailpit as a confidential client.
	ClientID     string
	ClientSecret string

	// RedirectURL is the callback registered with the provider, e.g.
	// https://mailpit.example.it/auth/callback
	RedirectURL string

	// Scopes requested at login. "openid" is added automatically.
	//
	// On the IPZS tenants the roles travel in a "groups" scope that already
	// exists — see the discovery document of an existing tenant.
	Scopes []string

	// RolesClaim is the claim carrying the user's roles or groups.
	//
	// The name is ours to choose when the external claim is created, but on
	// tenants that already publish one it is typically "groups" or "roles".
	RolesClaim string

	// RoleSeparator splits a roles claim that arrives as a single string
	// rather than a JSON array. WSO2 serialises multi-valued claims with a
	// configurable separator, so both shapes must be accepted.
	RoleSeparator string

	// StripUserStorePrefix removes a leading "DOMAIN/" from each role.
	//
	// Identities federated from the corporate directory come back qualified
	// with their user store domain (on the IPZS dev tenants: "ICTIPZS/").
	// Without stripping it, no role ever matches and every user logs in with
	// an empty scope.
	StripUserStorePrefix bool

	// RolePrefix marks the roles that concern Mailpit and is removed to
	// obtain the tag name: "mailpit-progetto-x" -> tag "progetto-x".
	// Roles without this prefix are ignored.
	RolePrefix string

	// AdminRole grants an unrestricted scope. Compared after prefix
	// stripping and case-folding.
	AdminRole string

	// SessionTTL is how long a session lasts before the user logs in again.
	SessionTTL time.Duration

	// InsecureSkipIssuerCheck accepts a discovery document whose "issuer"
	// differs from the URL it was fetched from.
	//
	// This is not paranoia: WSO2 tenants serve discovery at
	// /t/<tenant>/oauth2/token/.well-known/openid-configuration but report
	// the root issuer, which makes the default check fail. Signature
	// validation against the tenant's JWKS is unaffected, and the audience
	// check still ties the token to our client.
	InsecureSkipIssuerCheck bool

	// CACertFile is an optional CA bundle to trust when calling the
	// provider, for internal PKIs whose root is not in the system store.
	CACertFile string
}

// Defaults returns a Config with the values that suit a standard provider.
func Defaults() Config {
	return Config{
		Scopes:               []string{"groups"},
		RolesClaim:           "groups",
		RoleSeparator:        ",",
		StripUserStorePrefix: true,
		RolePrefix:           "mailpit-",
		AdminRole:            "admin",
		SessionTTL:           8 * time.Hour,
	}
}

// Enabled reports whether enough is configured to attempt OIDC.
func (c Config) Enabled() bool {
	return c.Issuer != "" && c.ClientID != ""
}

// PostLogoutRedirectURL is where the provider sends the browser back after
// ending the session. It is derived from RedirectURL rather than configured
// separately: both must be registered with the provider, and deriving one from
// the other removes a way to get them inconsistent.
func (c Config) PostLogoutRedirectURL() string {
	const callback = "auth/callback"

	if c.RedirectURL == "" {
		return ""
	}

	if i := strings.LastIndex(c.RedirectURL, callback); i >= 0 {
		return c.RedirectURL[:i]
	}

	return c.RedirectURL
}
