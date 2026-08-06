package session

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/axllent/mailpit/internal/scope"
)

func TestSessionLifecycle(t *testing.T) {
	st := NewStore(time.Hour, false)

	s, err := st.Create("sub-1", "tester", "", []string{"mailpit-alfa"}, scope.ForTags([]string{"alfa"}))
	if err != nil {
		t.Fatal(err)
	}

	if got := st.Get(s.ID); got == nil {
		t.Fatal("session not retrievable after creation")
	}

	st.Delete(s.ID)

	if got := st.Get(s.ID); got != nil {
		t.Error("session still retrievable after delete")
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	// A negative TTL creates a session that is already past its deadline.
	st := NewStore(-time.Minute, false)

	s, err := st.Create("sub-1", "tester", "", nil, scope.Unrestricted())
	if err != nil {
		t.Fatal(err)
	}

	if st.Get(s.ID) != nil {
		t.Error("an expired session must not be returned")
	}

	if st.Count() != 0 {
		t.Error("Get should drop the expired session it rejected")
	}
}

func TestIDsAreUnique(t *testing.T) {
	st := NewStore(time.Hour, false)

	seen := map[string]bool{}
	for range 500 {
		s, err := st.Create("sub", "tester", "", nil, scope.Unrestricted())
		if err != nil {
			t.Fatal(err)
		}
		if seen[s.ID] {
			t.Fatal("duplicate session ID generated")
		}
		seen[s.ID] = true
	}
}

func TestPrune(t *testing.T) {
	st := NewStore(time.Hour, false)

	live, err := st.Create("sub", "tester", "", nil, scope.Unrestricted())
	if err != nil {
		t.Fatal(err)
	}

	dead, err := st.Create("sub", "tester", "", nil, scope.Unrestricted())
	if err != nil {
		t.Fatal(err)
	}
	dead.Expires = time.Now().Add(-time.Minute)

	if removed := st.Prune(); removed != 1 {
		t.Errorf("pruned %d sessions, want 1", removed)
	}

	if st.Get(live.ID) == nil {
		t.Error("prune removed a live session")
	}
}

// TestCookieAttributes guards the CSRF mitigation. SameSite=Lax is what keeps
// the session cookie off cross-site state-changing requests now that we no
// longer use a bearer header.
func TestCookieAttributes(t *testing.T) {
	st := NewStore(time.Hour, true)

	s, err := st.Create("sub", "tester", "", nil, scope.Unrestricted())
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	st.SetCookie(w, s, "/")

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}

	c := cookies[0]

	if !c.HttpOnly {
		t.Error("cookie must be HttpOnly so JavaScript cannot read the session")
	}
	if !c.Secure {
		t.Error("cookie must be Secure when the UI is served over TLS")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Error("cookie must be SameSite=Lax: it is the CSRF mitigation")
	}
}

func TestFromRequest(t *testing.T) {
	st := NewStore(time.Hour, false)

	s, err := st.Create("sub", "tester", "", nil, scope.Unrestricted())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("valid cookie resolves", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: CookieName, Value: s.ID})

		if st.FromRequest(r) == nil {
			t.Error("expected the session to resolve")
		}
	})

	t.Run("unknown ID does not resolve", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: CookieName, Value: "invented"})

		if st.FromRequest(r) != nil {
			t.Error("an invented session ID must not resolve")
		}
	})

	t.Run("no cookie does not resolve", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)

		if st.FromRequest(r) != nil {
			t.Error("expected no session without a cookie")
		}
	})
}
