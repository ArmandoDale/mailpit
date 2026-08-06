package cmd

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/axllent/mailpit/internal/logger"
	"github.com/axllent/mailpit/internal/oidc"
	"github.com/axllent/mailpit/server"
	"github.com/spf13/cobra"
)

// oidcConfig holds the OIDC settings gathered from flags and environment.
var oidcConfig = oidc.Defaults()

// registerOIDCFlags adds the authentication flags. Everything is optional:
// without an issuer Mailpit behaves exactly as upstream.
func registerOIDCFlags(cmd *cobra.Command) {
	f := cmd.Flags()

	f.StringVar(&oidcConfig.Issuer, "oidc-issuer", oidcConfig.Issuer,
		"OIDC issuer URL (enables authentication)")
	f.StringVar(&oidcConfig.ClientID, "oidc-client-id", oidcConfig.ClientID,
		"OIDC client ID")
	f.StringVar(&oidcConfig.ClientSecret, "oidc-client-secret", oidcConfig.ClientSecret,
		"OIDC client secret")
	f.StringVar(&oidcConfig.RedirectURL, "oidc-redirect-url", oidcConfig.RedirectURL,
		"OIDC callback URL registered with the provider")
	f.StringSliceVar(&oidcConfig.Scopes, "oidc-scopes", oidcConfig.Scopes,
		"Additional OIDC scopes to request (openid is always included)")
	f.StringVar(&oidcConfig.RolesClaim, "oidc-roles-claim", oidcConfig.RolesClaim,
		"Claim carrying the user's roles or groups")
	f.StringVar(&oidcConfig.RoleSeparator, "oidc-role-separator", oidcConfig.RoleSeparator,
		"Separator for a roles claim sent as a single string")
	f.BoolVar(&oidcConfig.StripUserStorePrefix, "oidc-strip-userstore-prefix", oidcConfig.StripUserStorePrefix,
		"Strip the DOMAIN/ qualifier federated directories add to role names")
	f.StringVar(&oidcConfig.RolePrefix, "oidc-role-prefix", oidcConfig.RolePrefix,
		"Prefix marking roles that map to Mailpit tags")
	f.StringVar(&oidcConfig.AdminRole, "oidc-admin-role", oidcConfig.AdminRole,
		"Role granting unrestricted access (after the role prefix)")
	f.DurationVar(&oidcConfig.SessionTTL, "oidc-session-ttl", oidcConfig.SessionTTL,
		"How long a session lasts before the user must sign in again")
	f.StringVar(&oidcConfig.CACertFile, "oidc-ca-cert", oidcConfig.CACertFile,
		"CA bundle to trust when calling the identity provider")
	f.BoolVar(&oidcConfig.InsecureSkipIssuerCheck, "oidc-skip-issuer-check", oidcConfig.InsecureSkipIssuerCheck,
		"Accept a discovery document whose issuer differs from its URL (required for WSO2 tenants)")
}

// loadOIDCFromEnv applies MP_OIDC_* environment variables, matching how the
// rest of Mailpit is configured in containers.
func loadOIDCFromEnv() {
	str := func(key string, target *string) {
		if v := os.Getenv(key); v != "" {
			*target = v
		}
	}

	str("MP_OIDC_ISSUER", &oidcConfig.Issuer)
	str("MP_OIDC_CLIENT_ID", &oidcConfig.ClientID)
	str("MP_OIDC_CLIENT_SECRET", &oidcConfig.ClientSecret)
	str("MP_OIDC_REDIRECT_URL", &oidcConfig.RedirectURL)
	str("MP_OIDC_ROLES_CLAIM", &oidcConfig.RolesClaim)
	str("MP_OIDC_ROLE_SEPARATOR", &oidcConfig.RoleSeparator)
	str("MP_OIDC_ROLE_PREFIX", &oidcConfig.RolePrefix)
	str("MP_OIDC_ADMIN_ROLE", &oidcConfig.AdminRole)
	str("MP_OIDC_CA_CERT", &oidcConfig.CACertFile)

	if v := os.Getenv("MP_OIDC_SCOPES"); v != "" {
		oidcConfig.Scopes = strings.Split(v, ",")
	}

	if v := os.Getenv("MP_OIDC_SESSION_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			oidcConfig.SessionTTL = d
		} else {
			logger.Log().Warnf("[oidc] ignoring invalid MP_OIDC_SESSION_TTL %q", v)
		}
	}

	if v := os.Getenv("MP_OIDC_STRIP_USERSTORE_PREFIX"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			oidcConfig.StripUserStorePrefix = b
		}
	}

	if v := os.Getenv("MP_OIDC_SKIP_ISSUER_CHECK"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			oidcConfig.InsecureSkipIssuerCheck = b
		}
	}
}

// initOIDC brings up authentication when an issuer is configured.
//
// Discovery failing is fatal on purpose: starting without authentication when
// it was asked for would serve every project's mail to anyone.
func initOIDC() {
	loadOIDCFromEnv()

	if !oidcConfig.Enabled() {
		return
	}

	if oidcConfig.RedirectURL == "" {
		logger.Log().Fatal("[oidc] --oidc-redirect-url is required when an issuer is set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.EnableOIDC(ctx, oidcConfig); err != nil {
		logger.Log().Fatalf("[oidc] %s", err.Error())
	}
}
