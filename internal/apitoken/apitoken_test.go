package apitoken

import (
	"net/http"
	"strings"
	"testing"
)

const validSecret = "0123456789abcdef0123456789abcdef"

func TestParseRejectsBadDefinitions(t *testing.T) {
	tests := []struct {
		name  string
		entry string
	}{
		{"missing tag list", "ci:" + validSecret},
		{"missing secret and tags", "ci"},
		{"empty name", ":" + validSecret + ":progetto-alfa"},
		{"short secret", "ci:troppo-corto:progetto-alfa"},
		{"empty tag list", "ci:" + validSecret + ":"},
		{"tag list of separators only", "ci:" + validSecret + ":,,"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, err := Parse(tc.entry)
			if err == nil {
				t.Fatal("expected the definition to be rejected")
			}

			if store != nil {
				t.Error("a rejected definition must not yield a usable store")
			}

			// The secret must never reach the logs through the error text.
			if strings.Contains(err.Error(), validSecret) {
				t.Errorf("the error leaked the secret: %s", err)
			}
		})
	}
}

func TestParseRejectsDuplicateNames(t *testing.T) {
	_, err := Parse("ci:" + validSecret + ":alfa ci:" + validSecret + "x:beta")
	if err == nil {
		t.Fatal("expected duplicate token names to be rejected")
	}
}

func TestParseEmptyYieldsNoStore(t *testing.T) {
	store, err := Parse("   \n  ")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	if store.Len() != 0 {
		t.Error("expected no tokens")
	}

	// A nil store must be safe to use: it is the state whenever no tokens are
	// configured, which is the normal case.
	if _, ok := store.Lookup(validSecret); ok {
		t.Error("a nil store authenticated something")
	}
}

func TestParseScopes(t *testing.T) {
	store, err := Parse("alfa:" + validSecret + ":progetto-alfa,progetto-beta\nadmin:" + validSecret + "9:*")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	if store.Len() != 2 {
		t.Fatalf("expected 2 tokens, got %d", store.Len())
	}

	scoped, ok := store.Lookup(validSecret)
	if !ok {
		t.Fatal("the scoped token was not found")
	}

	if scoped.Scope.IsUnrestricted() {
		t.Error("a tag list must not produce an unrestricted scope")
	}

	if !scoped.Scope.AllowsTags([]string{"progetto-beta"}) {
		t.Error("the token does not see a tag it was granted")
	}

	if scoped.Scope.AllowsTags([]string{"progetto-gamma"}) {
		t.Error("the token sees a tag it was not granted")
	}

	if !scoped.Scope.AllowsTags(nil) {
		t.Error("untagged messages must stay visible, as they are for project users")
	}

	admin, ok := store.Lookup(validSecret + "9")
	if !ok {
		t.Fatal("the unrestricted token was not found")
	}

	if !admin.Scope.IsUnrestricted() {
		t.Error(`"*" must grant the whole mailbox`)
	}

	if names := store.Unrestricted(); len(names) != 1 || names[0] != "admin" {
		t.Errorf("expected startup to be able to name the unrestricted token, got %v", names)
	}
}

func TestLookupRejectsWrongSecrets(t *testing.T) {
	store, err := Parse("ci:" + validSecret + ":progetto-alfa")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	for _, secret := range []string{"", validSecret + "x", validSecret[:len(validSecret)-1], "ci"} {
		if _, ok := store.Lookup(secret); ok {
			t.Errorf("secret %q was accepted", secret)
		}
	}
}

func TestBearerHeaderParsing(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"Bearer " + validSecret, validSecret},
		{"bearer " + validSecret, validSecret},
		{"Bearer  " + validSecret + " ", validSecret},
		{"Basic dXNlcjpwYXNz", ""},
		{validSecret, ""},
		{"", ""},
	}

	for _, tc := range tests {
		r, err := http.NewRequest(http.MethodGet, "http://localhost/api/v1/messages", nil)
		if err != nil {
			t.Fatal(err)
		}

		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}

		if got := bearerSecret(r); got != tc.want {
			t.Errorf("header %q: got %q, want %q", tc.header, got, tc.want)
		}

		if Presented(r) != (tc.want != "") {
			t.Errorf("header %q: Presented disagrees with the parsed secret", tc.header)
		}
	}
}
