package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/axllent/mailpit/internal/scope"
	"github.com/axllent/mailpit/internal/storage"
	"github.com/jhillyerd/enmime/v2"
)

// enableTestSessions turns on session auth for the duration of a test and
// restores the unauthenticated default afterwards.
func enableTestSessions(t *testing.T) {
	t.Helper()

	EnableSessions(time.Hour)

	t.Cleanup(func() {
		Sessions = nil
		scope.Enforce = false
	})
}

// storeTestMessage saves a message with the given tags and returns its ID.
func storeTestMessage(t *testing.T, subject string, tags []string) string {
	t.Helper()

	env, err := enmime.Builder().
		From("Sender", "sender@example.com").
		To("Recipient", "recipient@example.com").
		Subject(subject).
		Text([]byte("body")).
		Build()
	if err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	if err := env.Encode(buf); err != nil {
		t.Fatal(err)
	}

	b := buf.Bytes()

	id, err := storage.Store(&b, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(tags) > 0 {
		if _, err := storage.SetMessageTags(id, tags); err != nil {
			t.Fatal(err)
		}
	}

	return id
}

// loginAs creates a session and returns the cookie a browser would send.
func loginAs(t *testing.T, tags []string) *http.Cookie {
	t.Helper()

	sc := scope.Unrestricted()
	if tags != nil {
		sc = scope.ForTags(tags)
	}

	s, err := Sessions.Create("sub-1", "tester", "", nil, sc)
	if err != nil {
		t.Fatal(err)
	}

	return &http.Cookie{Name: "mp_session", Value: s.ID}
}

// get issues a request through the full route stack, optionally with a cookie.
func get(t *testing.T, ts *httptest.Server, path string, c *http.Cookie) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}

	if c != nil {
		req.AddCookie(c)
	}

	// Do not follow the login redirect: the status is what we assert on.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	return res
}

func TestSessionRequired(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	t.Run("api without session is 401", func(t *testing.T) {
		res := get(t, ts, "/api/v1/messages", nil)
		defer res.Body.Close()

		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("got %d, want 401", res.StatusCode)
		}
	})

	t.Run("api with session is 200", func(t *testing.T) {
		res := get(t, ts, "/api/v1/messages", loginAs(t, nil))
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Errorf("got %d, want 200", res.StatusCode)
		}
	})

	t.Run("forged cookie is rejected", func(t *testing.T) {
		res := get(t, ts, "/api/v1/messages", &http.Cookie{Name: "mp_session", Value: "not-a-real-session"})
		defer res.Body.Close()

		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("got %d, want 401 for an invented session ID", res.StatusCode)
		}
	})

	t.Run("logout route stays reachable without a session", func(t *testing.T) {
		res := get(t, ts, "/auth/logout", nil)
		defer res.Body.Close()

		if res.StatusCode == http.StatusUnauthorized {
			t.Error("auth routes must not require a session")
		}
	})
}

// TestSessionScopesByIDRoutes is the bypass test: a message outside the
// caller's scope must not be reachable on any by-ID route, and must report 404
// rather than 403 so the existence of the ID is not disclosed.
func TestSessionScopesByIDRoutes(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	mine := storeTestMessage(t, "mine", []string{"progetto-alfa"})
	theirs := storeTestMessage(t, "theirs", []string{"progetto-beta"})

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	cookie := loginAs(t, []string{"progetto-alfa"})

	routes := []string{
		"/api/v1/message/%s",
		"/api/v1/message/%s/raw",
		"/api/v1/message/%s/headers",
		"/api/v1/message/%s/html-check",
		"/api/v1/message/%s/link-check",
		"/api/v1/message/%s/part/1",
		"/api/v1/message/%s/part/1/thumb",
		"/view/%s",
		"/view/%s.html",
		"/view/%s.txt",
	}

	for _, route := range routes {
		t.Run("blocked "+route, func(t *testing.T) {
			res := get(t, ts, fmtRoute(route, theirs), cookie)
			defer res.Body.Close()

			if res.StatusCode != http.StatusNotFound {
				t.Errorf("LEAK: %s returned %d for another project's message, want 404",
					fmtRoute(route, theirs), res.StatusCode)
			}
		})
	}

	t.Run("own message still reachable", func(t *testing.T) {
		res := get(t, ts, fmtRoute("/api/v1/message/%s", mine), cookie)
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Errorf("got %d for own message, want 200", res.StatusCode)
		}
	})
}

func fmtRoute(route, id string) string {
	return strings.ReplaceAll(route, "%s", id)
}

// TestTagFilterRulesAreAdminOnly is the regression test for a privilege
// escalation: the tag filter routes carried no authorisation check, so any
// signed-in user could rewrite the rules that decide which project sees which
// message. A project user could add a rule tagging every message with a tag
// they hold, apply it to the existing mailbox, and read everything.
func TestTagFilterRulesAreAdminOnly(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	project := loginAs(t, []string{"progetto-alfa"})
	admin := loginAs(t, nil)

	// GET is listed too: the rules describe every project's tag layout.
	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/tag-filters", ""},
		{http.MethodPut, "/api/v1/tag-filters", `{"Filters":[{"match":"anything","tags":["progetto-alfa"]}]}`},
		{http.MethodPost, "/api/v1/tag-filters/apply", ""},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path+" as a project user", func(t *testing.T) {
			res := do(t, ts, rt.method, rt.path, rt.body, project)
			defer res.Body.Close()

			if res.StatusCode != http.StatusForbidden {
				t.Errorf("got %d, want 403: a project user must not reach the global tag rules", res.StatusCode)
			}
		})

		t.Run(rt.method+" "+rt.path+" as an admin", func(t *testing.T) {
			res := do(t, ts, rt.method, rt.path, rt.body, admin)
			defer res.Body.Close()

			if res.StatusCode == http.StatusForbidden {
				t.Error("an unrestricted caller must still be able to manage tag rules")
			}
		})
	}

	// The rules must be unchanged by the refused calls.
	if rules := storage.GetRuntimeTagFilters(); len(rules) != 1 {
		t.Errorf("stored rules = %d, want the single rule the admin wrote", len(rules))
	}
}


// TestMessageTagsStayWithinScope covers the three ways a project user could
// otherwise use tagging to move a message across a project boundary.
func TestMessageTagsStayWithinScope(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	alfa := loginAs(t, []string{"progetto-alfa"})

	tagsOf := func(t *testing.T, id string) []string {
		t.Helper()
		return storage.MessageTags(id)
	}

	t.Run("cannot clear every tag", func(t *testing.T) {
		id := storeTestMessage(t, "solo mio", []string{"progetto-alfa"})

		res := do(t, ts, http.MethodPut, "/api/v1/tags",
			`{"IDs":["`+id+`"],"Tags":[]}`, alfa)
		defer res.Body.Close()

		if res.StatusCode != http.StatusForbidden {
			t.Errorf("got %d, want 403: an untagged message is visible to everyone", res.StatusCode)
		}

		if got := tagsOf(t, id); len(got) != 1 {
			t.Errorf("tags = %v, want the original tag untouched", got)
		}
	})

	t.Run("cannot apply a tag outside its own projects", func(t *testing.T) {
		id := storeTestMessage(t, "mio", []string{"progetto-alfa"})

		res := do(t, ts, http.MethodPut, "/api/v1/tags",
			`{"IDs":["`+id+`"],"Tags":["progetto-beta"]}`, alfa)
		defer res.Body.Close()

		if res.StatusCode != http.StatusForbidden {
			t.Errorf("got %d, want 403: tagging must not push mail into another project", res.StatusCode)
		}
	})

	t.Run("cannot strip another project's tag from a shared message", func(t *testing.T) {
		id := storeTestMessage(t, "condiviso", []string{"progetto-alfa", "progetto-beta"})

		res := do(t, ts, http.MethodPut, "/api/v1/tags",
			`{"IDs":["`+id+`"],"Tags":["progetto-alfa"]}`, alfa)
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Fatalf("got %d, want 200: tagging within scope is allowed", res.StatusCode)
		}

		var beta bool
		for _, tag := range tagsOf(t, id) {
			if tag == "progetto-beta" {
				beta = true
			}
		}

		if !beta {
			t.Error("LEAK: the other project's tag was removed, hiding the message from them")
		}
	})

	t.Run("an admin is not restricted", func(t *testing.T) {
		id := storeTestMessage(t, "admin", []string{"progetto-alfa"})

		res := do(t, ts, http.MethodPut, "/api/v1/tags",
			`{"IDs":["`+id+`"],"Tags":[]}`, loginAs(t, nil))
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Errorf("got %d, want 200: an unrestricted caller may clear tags", res.StatusCode)
		}
	})
}
