package storage

import (
	"bytes"
	"testing"

	"github.com/axllent/mailpit/config"
	"github.com/axllent/mailpit/internal/scope"
	"github.com/jhillyerd/enmime/v2"
)

// storeTagged saves a message and applies the given tags, returning its ID.
func storeTagged(t *testing.T, subject string, tags []string) string {
	t.Helper()

	msg := enmime.Builder().
		From("Sender", "sender@example.com").
		To("Recipient", "recipient@example.com").
		Subject(subject).
		Text([]byte("body of " + subject))

	env, err := msg.Build()
	if err != nil {
		t.Fatalf("building message: %s", err)
	}

	buf := new(bytes.Buffer)
	if err := env.Encode(buf); err != nil {
		t.Fatalf("encoding message: %s", err)
	}

	bufBytes := buf.Bytes()

	id, err := Store(&bufBytes, nil)
	if err != nil {
		t.Fatalf("storing message: %s", err)
	}

	if len(tags) > 0 {
		if _, err := SetMessageTags(id, tags); err != nil {
			t.Fatalf("tagging message: %s", err)
		}
	}

	return id
}

// scopeFixture stores one message per project plus an untagged one.
func scopeFixture(t *testing.T) (alfa, beta, untagged string) {
	t.Helper()

	alfa = storeTagged(t, "alfa message", []string{"progetto-alfa"})
	beta = storeTagged(t, "beta message", []string{"progetto-beta"})
	untagged = storeTagged(t, "untagged message", nil)

	return alfa, beta, untagged
}

func TestScopeIsolatesList(t *testing.T) {
	setup(config.DBTenantID(""))

	alfa, beta, untagged := scopeFixture(t)

	t.Run("unrestricted sees everything", func(t *testing.T) {
		got, err := List(0, 0, 100, scope.Unrestricted())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Errorf("expected 3 messages, got %d", len(got))
		}
	})

	t.Run("project scope sees own plus untagged", func(t *testing.T) {
		got, err := List(0, 0, 100, scope.ForTags([]string{"progetto-alfa"}))
		if err != nil {
			t.Fatal(err)
		}

		ids := map[string]bool{}
		for _, m := range got {
			ids[m.ID] = true
		}

		if !ids[alfa] {
			t.Error("own message not visible")
		}
		if !ids[untagged] {
			t.Error("untagged message should stay visible")
		}
		if ids[beta] {
			t.Error("LEAK: another project's message is visible")
		}
	})

	t.Run("denied scope sees nothing", func(t *testing.T) {
		got, err := List(0, 0, 100, scope.Denied())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("LEAK: denied scope returned %d messages", len(got))
		}
	})
}

func TestScopeIsolatesSearch(t *testing.T) {
	setup(config.DBTenantID(""))

	alfa, beta, _ := scopeFixture(t)

	sc := scope.ForTags([]string{"progetto-alfa"})

	t.Run("search cannot reach another project", func(t *testing.T) {
		got, total, err := Search("beta", "", 0, 0, 100, sc)
		if err != nil {
			t.Fatal(err)
		}
		if total != 0 || len(got) != 0 {
			t.Errorf("LEAK: search returned %d results for another project", total)
		}
	})

	t.Run("explicit tag search cannot widen the scope", func(t *testing.T) {
		// A user appending tag:progetto-beta to their own query must not
		// escape the scope: the scope constraint is ANDed, never replaced.
		got, _, err := Search("tag:progetto-beta", "", 0, 0, 100, sc)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("LEAK: tag: search escaped the scope, got %d results", len(got))
		}
	})

	t.Run("negated search cannot widen the scope", func(t *testing.T) {
		// Exclusions must not turn into a way of selecting hidden rows.
		got, _, err := Search("-tag:progetto-alfa", "", 0, 0, 100, sc)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range got {
			if m.ID == beta {
				t.Error("LEAK: negated search exposed another project's message")
			}
		}
	})

	t.Run("own project remains searchable", func(t *testing.T) {
		got, _, err := Search("alfa", "", 0, 0, 100, sc)
		if err != nil {
			t.Fatal(err)
		}

		found := false
		for _, m := range got {
			if m.ID == alfa {
				found = true
			}
		}
		if !found {
			t.Error("own message not searchable within scope")
		}
	})
}

func TestScopeIsolatesDelete(t *testing.T) {
	setup(config.DBTenantID(""))

	alfa, beta, untagged := scopeFixture(t)

	// A project user deleting "everything" must not touch other projects.
	if err := DeleteSearch("", "", scope.ForTags([]string{"progetto-alfa"})); err != nil {
		t.Fatal(err)
	}

	remaining, err := List(0, 0, 100, scope.Unrestricted())
	if err != nil {
		t.Fatal(err)
	}

	left := map[string]bool{}
	for _, m := range remaining {
		left[m.ID] = true
	}

	if !left[beta] {
		t.Error("DATA LOSS: a project user deleted another project's message")
	}

	// Assert the delete actually happened, otherwise this test would pass
	// just as happily against a delete that silently did nothing.
	if left[alfa] {
		t.Error("own message was not deleted")
	}
	if left[untagged] {
		t.Error("untagged message was not deleted: it is visible to this user")
	}
}

func TestMessageInScope(t *testing.T) {
	setup(config.DBTenantID(""))

	alfa, beta, untagged := scopeFixture(t)

	sc := scope.ForTags([]string{"progetto-alfa"})

	cases := []struct {
		name string
		id   string
		sc   scope.Scope
		want bool
	}{
		{"own message", alfa, sc, true},
		{"untagged message", untagged, sc, true},
		{"other project", beta, sc, false},
		{"unrestricted reaches other project", beta, scope.Unrestricted(), true},
		{"denied reaches nothing", alfa, scope.Denied(), false},
		{"unknown id", "does-not-exist", sc, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MessageInScope(tc.id, tc.sc)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("MessageInScope = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestScopeMultipleTags covers a user belonging to more than one project.
func TestScopeMultipleTags(t *testing.T) {
	setup(config.DBTenantID(""))

	alfa, beta, _ := scopeFixture(t)
	gamma := storeTagged(t, "gamma message", []string{"progetto-gamma"})

	sc := scope.ForTags([]string{"progetto-alfa", "progetto-beta"})

	for _, tc := range []struct {
		id   string
		want bool
	}{
		{alfa, true},
		{beta, true},
		{gamma, false},
	} {
		got, err := MessageInScope(tc.id, sc)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("message %s: in scope = %v, want %v", tc.id, got, tc.want)
		}
	}
}
