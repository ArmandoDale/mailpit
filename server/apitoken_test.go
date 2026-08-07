package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/axllent/mailpit/internal/apitoken"
	"github.com/axllent/mailpit/internal/storage"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// enableTestTokens configures a token scoped to progetto-alfa.
func enableTestTokens(t *testing.T, definitions string) {
	t.Helper()

	store, err := apitoken.Parse(definitions)
	if err != nil {
		t.Fatalf("could not parse the test tokens: %s", err)
	}

	APITokens = store

	t.Cleanup(func() { APITokens = nil })
}

// getWithToken issues a request carrying a bearer token instead of a cookie.
func getWithToken(t *testing.T, ts *httptest.Server, path, secret string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}

	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	res, err := (&http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}).Do(req)
	if err != nil {
		t.Fatal(err)
	}

	return res
}

// TestAPITokenIsScoped is the point of the whole feature: a pipeline reads its
// own project's mail and nothing else. Before service tokens existed, the only
// non-interactive option would have been a credential that saw everything.
func TestAPITokenIsScoped(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)
	enableTestTokens(t, "ci-alfa:"+testSecret+":progetto-alfa")

	mine := storeTestMessage(t, "mine", []string{"progetto-alfa"})
	theirs := storeTestMessage(t, "theirs", []string{"progetto-beta"})
	untagged := storeTestMessage(t, "untagged", nil)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := getWithToken(t, ts, "/api/v1/messages", testSecret)
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected the token to be accepted, got %d", res.StatusCode)
	}

	var summary struct {
		Messages []struct {
			ID string
		}
	}

	if err := json.NewDecoder(res.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}

	seen := make(map[string]bool)
	for _, m := range summary.Messages {
		seen[m.ID] = true
	}

	if !seen[mine] {
		t.Error("the token cannot see its own project's message")
	}
	if seen[theirs] {
		t.Error("ISOLATION BREACH: the token listed another project's message")
	}
	if !seen[untagged] {
		t.Error("untagged messages must stay visible, as they are for project users")
	}

	// The by-ID route bypasses the list query, so it is checked separately.
	byID := getWithToken(t, ts, "/api/v1/message/"+theirs, testSecret)
	defer byID.Body.Close()

	if byID.StatusCode != http.StatusNotFound {
		t.Errorf("ISOLATION BREACH: fetching another project's message by ID returned %d, want 404", byID.StatusCode)
	}
}

// TestUnrestrictedAPIToken covers the explicit opt-out, so that "*" keeps
// meaning what the configuration says it means.
func TestUnrestrictedAPIToken(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)
	enableTestTokens(t, "admin:"+testSecret+":*")

	theirs := storeTestMessage(t, "theirs", []string{"progetto-beta"})

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := getWithToken(t, ts, "/api/v1/message/"+theirs, testSecret)
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("an unrestricted token was denied a message: %d", res.StatusCode)
	}
}

// TestBadAPITokenIsRejected: a wrong token must not fall through to the
// session check, which would answer a script with a redirect to a login page
// and a 200 it cannot interpret.
func TestBadAPITokenIsRejected(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)
	enableTestTokens(t, "ci-alfa:"+testSecret+":progetto-alfa")

	storeTestMessage(t, "mine", []string{"progetto-alfa"})

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := getWithToken(t, ts, "/api/v1/messages", "sbagliato-sbagliato-sbagliato")
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for an unrecognised token, got %d", res.StatusCode)
	}
}

// TestNoTokenStillNeedsASession guards against the token path accidentally
// becoming a way in for requests that carry no credential at all.
func TestNoTokenStillNeedsASession(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)
	enableTestTokens(t, "ci-alfa:"+testSecret+":progetto-alfa")

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := getWithToken(t, ts, "/api/v1/messages", "")
	defer res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without any credential, got %d", res.StatusCode)
	}
}
