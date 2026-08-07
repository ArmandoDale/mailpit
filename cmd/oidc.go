package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/axllent/mailpit/config"
	"github.com/axllent/mailpit/internal/apitoken"
	"github.com/axllent/mailpit/internal/auth"
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

	f.StringVar(&apiTokens, "api-token", apiTokens,
		"Service tokens for non-interactive API access: name:secret:tag1,tag2 (or name:secret:* for the whole mailbox)")
	f.StringVar(&apiTokenFile, "api-token-file", apiTokenFile,
		"File holding service token definitions, one per line")
}

// apiTokens and apiTokenFile hold the service token definitions, given inline
// or in a file the way the other credential sets are.
var (
	apiTokens    string
	apiTokenFile string
)

// loadAPITokens parses the configured service tokens.
//
// Both sources are concatenated rather than one overriding the other: a
// deployment may keep the project tokens in a file and add one inline, and
// silently dropping either half would lock out a pipeline.
func loadAPITokens() error {
	if v := os.Getenv("MP_API_TOKENS"); v != "" {
		apiTokens = v
	}

	if v := os.Getenv("MP_API_TOKENS_FILE"); v != "" {
		apiTokenFile = v
	}

	definitions := apiTokens

	if apiTokenFile != "" {
		b, err := os.ReadFile(filepath.Clean(apiTokenFile))
		if err != nil {
			return fmt.Errorf("[apitoken] cannot read %s: %w", apiTokenFile, err)
		}

		definitions = definitions + "\n" + string(b)
	}

	store, err := apitoken.Parse(definitions)
	if err != nil {
		return err
	}

	server.APITokens = store

	return nil
}

// initAPITokens loads the service tokens and reports what they can reach.
//
// A malformed definition is fatal for the same reason a failed discovery is:
// the operator asked for an access path, and starting without it produces a
// pipeline that fails with 401 for no visible reason.
func initAPITokens() {
	if err := loadAPITokens(); err != nil {
		logger.Log().Fatal(err.Error())
	}

	if server.APITokens.Len() == 0 {
		return
	}

	if !oidcConfig.Enabled() {
		logger.Log().Warn("[apitoken] tokens are configured but authentication is off, so they grant nothing extra: every request is already unrestricted")
		return
	}

	logger.Log().Infof("[apitoken] %d service token(s) configured", server.APITokens.Len())

	// Said out loud because it is the one configuration that opts out of the
	// isolation everything else enforces.
	if names := server.APITokens.Unrestricted(); len(names) > 0 {
		logger.Log().Warnf("[apitoken] these tokens can read every project's mail: %s", strings.Join(names, ", "))
	}
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

// checkPOP3Conflict reports why POP3 cannot be served alongside OIDC.
//
// The POP3 server reads the mailbox unscoped (internal/pop3 lists messages with
// scope.Unrestricted()) and authenticates against its own password file, which
// knows nothing about identities, roles or tags. Any POP3 account therefore
// downloads every project's mail, which defeats the isolation the HTTP side
// enforces. That was harmless upstream, where projects did not exist.
//
// Refusing to start is deliberate: silently ignoring one of the two settings
// would leave the operator believing something that is not true, in either
// direction. The condition mirrors pop3.Run() so the guard triggers exactly
// when a listener would have been opened.
func checkPOP3Conflict() error {
	if !oidcConfig.Enabled() {
		return nil
	}

	if auth.POP3Credentials == nil || config.POP3Listen == "" {
		return nil
	}

	return errors.New("[oidc] POP3 cannot be enabled together with OIDC: the POP3 server has no notion of projects " +
		"and would serve every project's mail to any POP3 account. Remove --pop3-auth-file/MP_POP3_AUTH " +
		"(or set --pop3 to an empty value) and read the mailbox over the authenticated HTTP API instead")
}

// initOIDC brings up authentication when an issuer is configured.
//
// Discovery failing is fatal on purpose: starting without authentication when
// it was asked for would serve every project's mail to anyone.
func initOIDC() {
	loadOIDCFromEnv()

	// Loaded even when OIDC is off, so a misconfigured token is reported at
	// startup rather than the first time a pipeline tries to use it.
	initAPITokens()

	if !oidcConfig.Enabled() {
		return
	}

	// Before the listener, not after: an unscoped POP3 server that runs even
	// for a moment has already handed out the mailbox.
	if err := checkPOP3Conflict(); err != nil {
		logger.Log().Fatal(err.Error())
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
