package scope

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestAllowsTags guards the live-notification path. It must agree with what
// applyScope permits in SQL, otherwise a message invisible in the list would
// still be announced over the WebSocket.
func TestAllowsTags(t *testing.T) {
	project := ForTags([]string{"progetto-alfa", "progetto-beta"})

	cases := []struct {
		name string
		sc   Scope
		tags []string
		want bool
	}{
		{"unrestricted sees tagged", Unrestricted(), []string{"qualsiasi"}, true},
		{"unrestricted sees untagged", Unrestricted(), nil, true},
		{"own tag", project, []string{"progetto-alfa"}, true},
		{"second tag", project, []string{"progetto-beta"}, true},
		{"one of several tags matches", project, []string{"altro", "progetto-beta"}, true},
		{"foreign tag", project, []string{"progetto-gamma"}, false},
		{"untagged stays visible", project, nil, true},
		{"denied sees nothing", Denied(), []string{"progetto-alfa"}, false},
		{"denied does not see untagged", Denied(), nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sc.AllowsTags(tc.tags); got != tc.want {
				t.Errorf("AllowsTags(%v) = %v, want %v", tc.tags, got, tc.want)
			}
		})
	}
}

func TestZeroValueDeniesEverything(t *testing.T) {
	var s Scope

	if s.IsUnrestricted() {
		t.Error("zero value must not be unrestricted")
	}
	if s.AllowsTags([]string{"anything"}) {
		t.Error("zero value must not allow tagged messages")
	}
	if s.AllowsTags(nil) {
		t.Error("zero value must not allow untagged messages")
	}
}

func TestFromRequest(t *testing.T) {
	t.Run("scope from context wins", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r = r.WithContext(NewContext(r.Context(), ForTags([]string{"x"})))

		if got := FromRequest(r).Tags(); len(got) != 1 || got[0] != "x" {
			t.Errorf("got tags %v, want [x]", got)
		}
	})

	t.Run("no scope without enforcement is unrestricted", func(t *testing.T) {
		Enforce = false
		r := httptest.NewRequest("GET", "/", nil)

		if !FromRequest(r).IsUnrestricted() {
			t.Error("expected unrestricted when enforcement is off")
		}
	})

	t.Run("no scope with enforcement is denied", func(t *testing.T) {
		Enforce = true
		defer func() { Enforce = false }()

		r := httptest.NewRequest("GET", "/", nil)
		sc := FromRequest(r)

		if sc.IsUnrestricted() || sc.AllowsTags(nil) || sc.AllowsTags([]string{"x"}) {
			t.Error("a request that skipped the session middleware must be denied")
		}
	})
}

func TestFromContextMissingFailsClosed(t *testing.T) {
	sc, ok := FromContext(context.Background())

	if ok {
		t.Error("expected no scope in a bare context")
	}
	if sc.IsUnrestricted() || sc.AllowsTags(nil) {
		t.Error("the returned scope must deny everything")
	}
}
