package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/axllent/mailpit/internal/storage"
)

// writeWithHeaders issues a state-changing request with the browser headers a
// given situation would produce.
func writeWithHeaders(t *testing.T, ts *httptest.Server, headers map[string]string, c *http.Cookie, bearer string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/messages", bytes.NewBufferString("{}"))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	if c != nil {
		req.AddCookie(c)
	}

	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
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

// TestCSRFBlocksForgedWrites: another site provoking a delete must not have it
// carried out just because the browser attached the session cookie.
func TestCSRFBlocksForgedWrites(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	cookie := loginAs(t, []string{"progetto-alfa"})

	tests := []struct {
		name    string
		headers map[string]string
		blocked bool
	}{
		{
			name:    "cross-site fetch",
			headers: map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://attaccante.example"},
			blocked: true,
		},
		{
			name:    "sibling host on the same domain",
			headers: map[string]string{"Sec-Fetch-Site": "same-site"},
			blocked: true,
		},
		{
			name:    "foreign Origin without Sec-Fetch-Site",
			headers: map[string]string{"Origin": "https://attaccante.example"},
			blocked: true,
		},
		{
			name:    "our own page",
			headers: map[string]string{"Sec-Fetch-Site": "same-origin"},
			blocked: false,
		},
		{
			name:    "typed address or bookmark",
			headers: map[string]string{"Sec-Fetch-Site": "none"},
			blocked: false,
		},
		{
			name:    "no browser headers at all",
			headers: map[string]string{},
			blocked: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := writeWithHeaders(t, ts, tc.headers, cookie, "")
			defer res.Body.Close()

			forbidden := res.StatusCode == http.StatusForbidden

			if tc.blocked && !forbidden {
				t.Errorf("CSRF: the request went through with status %d", res.StatusCode)
			}

			if !tc.blocked && forbidden {
				t.Errorf("a legitimate request was blocked as cross-site")
			}
		})
	}
}

// TestCSRFMatchesOwnOrigin covers the fallback path, where the browser sends
// Origin but not Sec-Fetch-Site.
func TestCSRFMatchesOwnOrigin(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := writeWithHeaders(t, ts, map[string]string{"Origin": ts.URL}, loginAs(t, []string{"progetto-alfa"}), "")
	defer res.Body.Close()

	if res.StatusCode == http.StatusForbidden {
		t.Error("a request from Mailpit's own origin was blocked")
	}
}

// TestCSRFDoesNotBlockTokens: a bearer token is never attached by a browser on
// its own, so a token request must not be treated as forged.
//
// The headers here are the ones that would block a cookie request. Origin is
// left out on purpose: a foreign Origin is already rejected upstream by the
// CORS check, before this one runs, and a pipeline does not send one anyway.
func TestCSRFDoesNotBlockTokens(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)
	enableTestTokens(t, "ci-alfa:"+testSecret+":progetto-alfa")

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := writeWithHeaders(t, ts, map[string]string{"Sec-Fetch-Site": "cross-site"}, nil, testSecret)
	defer res.Body.Close()

	if res.StatusCode == http.StatusForbidden {
		t.Error("a token request was blocked as cross-site")
	}

	// The same request without the token must be blocked, otherwise the case
	// above proves nothing.
	blocked := writeWithHeaders(t, ts, map[string]string{"Sec-Fetch-Site": "cross-site"}, loginAs(t, []string{"progetto-alfa"}), "")
	defer blocked.Body.Close()

	if blocked.StatusCode != http.StatusForbidden {
		t.Errorf("the cookie equivalent was not blocked: %d", blocked.StatusCode)
	}
}

// TestCSRFInertWithoutSessions: with authentication off nothing changes for
// existing deployments.
func TestCSRFInertWithoutSessions(t *testing.T) {
	setup()
	defer storage.Close()

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := writeWithHeaders(t, ts, map[string]string{"Sec-Fetch-Site": "cross-site"}, nil, "")
	defer res.Body.Close()

	if res.StatusCode == http.StatusForbidden {
		t.Error("the CSRF check fired with authentication disabled")
	}
}
