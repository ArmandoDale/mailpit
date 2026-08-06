package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/axllent/mailpit/internal/scope"
	"github.com/axllent/mailpit/internal/storage"
)

// do issues a request with a body and a session cookie.
func do(t *testing.T, ts *httptest.Server, method, path, body string, c *http.Cookie) *http.Response {
	t.Helper()

	req, err := http.NewRequest(method, ts.URL+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")

	if c != nil {
		req.AddCookie(c)
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

// messageExists asks whether the row is still there at all.
//
// Note it cannot use MessageInScope with an unrestricted scope: that answers
// "may this caller see it", and short-circuits to true without touching the
// database.
func messageExists(t *testing.T, id string) bool {
	t.Helper()

	_, err := storage.GetMessage(id)

	return err == nil
}

// TestDeleteAllIsScoped guards the "Delete all" button. Before this was
// scoped, a project user emptied the whole mailbox for everyone.
func TestDeleteAllIsScoped(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	mine := storeTestMessage(t, "mine", []string{"progetto-alfa"})
	theirs := storeTestMessage(t, "theirs", []string{"progetto-beta"})
	untagged := storeTestMessage(t, "untagged", nil)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := do(t, ts, http.MethodDelete, "/api/v1/messages", "{}", loginAs(t, []string{"progetto-alfa"}))
	defer res.Body.Close()

	if messageExists(t, mine) {
		t.Error("own message survived delete-all")
	}
	if !messageExists(t, theirs) {
		t.Error("DATA LOSS: delete-all removed another project's message")
	}
	if messageExists(t, untagged) {
		t.Error("untagged message should be deletable: it is visible to this user")
	}
}

// TestDeleteByIDIsScoped covers IDs supplied in the request body, which the
// path-based middleware never sees.
func TestDeleteByIDIsScoped(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	mine := storeTestMessage(t, "mine", []string{"progetto-alfa"})
	theirs := storeTestMessage(t, "theirs", []string{"progetto-beta"})

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{"IDs": []string{mine, theirs}})

	res := do(t, ts, http.MethodDelete, "/api/v1/messages", string(body), loginAs(t, []string{"progetto-alfa"}))
	defer res.Body.Close()

	if messageExists(t, mine) {
		t.Error("own message was not deleted")
	}
	if !messageExists(t, theirs) {
		t.Error("DATA LOSS: another project's message was deleted by naming its ID")
	}
}

// TestTagWriteByIDIsScoped: tagging is a write, and the IDs arrive in the body.
func TestTagWriteByIDIsScoped(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	theirs := storeTestMessage(t, "theirs", []string{"progetto-beta"})

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{"IDs": []string{theirs}, "Tags": []string{"dirottato"}})

	res := do(t, ts, http.MethodPut, "/api/v1/tags", string(body), loginAs(t, []string{"progetto-alfa"}))
	defer res.Body.Close()

	tags := storage.GetAllTags(scope.Unrestricted())
	for _, tag := range tags {
		if tag == "dirottato" {
			t.Error("a project user retagged another project's message")
		}
	}
}

// TestTagListIsScoped: the tag list is the list of project names.
func TestTagListIsScoped(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	storeTestMessage(t, "mine", []string{"progetto-alfa"})
	storeTestMessage(t, "theirs", []string{"progetto-beta"})

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := get(t, ts, "/api/v1/tags", loginAs(t, []string{"progetto-alfa"}))
	defer res.Body.Close()

	var tags []string
	if err := json.NewDecoder(res.Body).Decode(&tags); err != nil {
		t.Fatal(err)
	}

	for _, tag := range tags {
		if tag == "progetto-beta" {
			t.Error("LEAK: the tag list disclosed another project's name")
		}
	}

	if len(tags) != 1 || tags[0] != "progetto-alfa" {
		t.Errorf("got tags %v, want [progetto-alfa]", tags)
	}
}

// TestCountersMatchTheList: a total that disagrees with the visible list is
// both a metadata leak and a UI that contradicts itself.
func TestCountersMatchTheList(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	storeTestMessage(t, "mine", []string{"progetto-alfa"})
	storeTestMessage(t, "theirs 1", []string{"progetto-beta"})
	storeTestMessage(t, "theirs 2", []string{"progetto-beta"})
	storeTestMessage(t, "untagged", nil)

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := get(t, ts, "/api/v1/messages", loginAs(t, []string{"progetto-alfa"}))
	defer res.Body.Close()

	var out struct {
		Total          uint64 `json:"total"`
		Unread         uint64 `json:"unread"`
		Count          uint64 `json:"count"`
		MessagesCount  uint64 `json:"messages_count"`
		MessagesUnread uint64 `json:"messages_unread"`
		Tags           []string
		Messages       []struct{ ID string }
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}

	// One own message plus the untagged one.
	const visible = 2

	if len(out.Messages) != visible {
		t.Fatalf("listed %d messages, want %d", len(out.Messages), visible)
	}

	for name, got := range map[string]uint64{
		"total":           out.Total,
		"unread":          out.Unread,
		"count":           out.Count,
		"messages_count":  out.MessagesCount,
		"messages_unread": out.MessagesUnread,
	} {
		if got != visible {
			t.Errorf("%s = %d, want %d — the counter disagrees with the list", name, got, visible)
		}
	}

	for _, tag := range out.Tags {
		if tag == "progetto-beta" {
			t.Error("LEAK: the summary disclosed another project's tag")
		}
	}
}

// TestTagAdminOperationsRequireAdmin: renaming or deleting a tag affects every
// project that uses it.
func TestTagAdminOperationsRequireAdmin(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	storeTestMessage(t, "mine", []string{"progetto-alfa"})

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	project := loginAs(t, []string{"progetto-alfa"})

	t.Run("project user cannot rename", func(t *testing.T) {
		res := do(t, ts, http.MethodPut, "/api/v1/tags/progetto-alfa", `{"Name":"rinominato"}`, project)
		defer res.Body.Close()

		if res.StatusCode != http.StatusForbidden {
			t.Errorf("got %d, want 403", res.StatusCode)
		}
	})

	t.Run("project user cannot delete", func(t *testing.T) {
		res := do(t, ts, http.MethodDelete, "/api/v1/tags/progetto-alfa", "", project)
		defer res.Body.Close()

		if res.StatusCode != http.StatusForbidden {
			t.Errorf("got %d, want 403", res.StatusCode)
		}
	})

	t.Run("admin can rename", func(t *testing.T) {
		res := do(t, ts, http.MethodPut, "/api/v1/tags/progetto-alfa", `{"Name":"rinominato"}`, loginAs(t, nil))
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Errorf("got %d, want 200 for an admin", res.StatusCode)
		}
	})
}

// TestMarkAllReadIsScoped: "mark all read" must mean "all of mine".
func TestMarkAllReadIsScoped(t *testing.T) {
	setup()
	defer storage.Close()

	enableTestSessions(t)

	storeTestMessage(t, "mine", []string{"progetto-alfa"})
	storeTestMessage(t, "theirs", []string{"progetto-beta"})

	ts := httptest.NewServer(apiRoutes())
	defer ts.Close()

	res := do(t, ts, http.MethodPut, "/api/v1/messages", `{"Read":true}`, loginAs(t, []string{"progetto-alfa"}))
	defer res.Body.Close()

	if unread := storage.CountUnread(scope.ForTags([]string{"progetto-beta"})); unread == 0 {
		t.Error("mark-all-read touched another project's messages")
	}
}
