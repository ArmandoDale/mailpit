package cmd

import (
	"testing"

	"github.com/axllent/mailpit/config"
	"github.com/axllent/mailpit/internal/auth"
	"github.com/axllent/mailpit/internal/oidc"
)

// TestPOP3IsRefusedWithOIDC covers the one combination that would quietly
// undo the per-project isolation: an unscoped POP3 listener next to an
// authenticated HTTP side.
func TestPOP3IsRefusedWithOIDC(t *testing.T) {
	origConfig := oidcConfig
	origListen := config.POP3Listen
	origCredentials := auth.POP3Credentials

	t.Cleanup(func() {
		oidcConfig = origConfig
		config.POP3Listen = origListen
		auth.POP3Credentials = origCredentials
	})

	setPOP3Credentials := func(t *testing.T, on bool) {
		t.Helper()

		auth.POP3Credentials = nil
		if !on {
			return
		}

		// bcrypt hash of "password"
		if err := auth.SetPOP3Auth("user:$2y$10$IK5.RQjkAoRXQZ.LTvpQPuFuJZTPuFvOZ.6zFPBdxu8sD9EVeQMhq"); err != nil {
			t.Fatalf("could not set POP3 credentials: %s", err)
		}

		if auth.POP3Credentials == nil {
			t.Fatal("POP3 credentials were not set")
		}
	}

	tests := []struct {
		name      string
		issuer    string
		pop3Auth  bool
		pop3Liste string
		wantErr   bool
	}{
		{"oidc and pop3 together", "https://idp.example/realm", true, "[::]:1110", true},
		{"pop3 without oidc stays upstream", "", true, "[::]:1110", false},
		{"oidc without pop3 credentials", "https://idp.example/realm", false, "[::]:1110", false},
		{"oidc with pop3 listener disabled", "https://idp.example/realm", true, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oidcConfig = oidc.Defaults()
			oidcConfig.Issuer = tc.issuer
			if tc.issuer != "" {
				// Enabled() needs both halves of the client identity.
				oidcConfig.ClientID = "mailpit"
			}
			config.POP3Listen = tc.pop3Liste
			setPOP3Credentials(t, tc.pop3Auth)

			err := checkPOP3Conflict()

			if tc.wantErr && err == nil {
				t.Error("expected Mailpit to refuse to start, got no error")
			}

			if !tc.wantErr && err != nil {
				t.Errorf("expected no error, got: %s", err)
			}
		})
	}
}
