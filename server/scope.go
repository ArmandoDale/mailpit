package server

import (
	"net/http"
	"strings"

	"github.com/axllent/mailpit/config"
	"github.com/axllent/mailpit/internal/logger"
	"github.com/axllent/mailpit/internal/scope"
	"github.com/axllent/mailpit/internal/storage"
)

// requestMessageID returns the message ID a request addresses, or "" when the
// request is not about a single message.
//
// Two shapes exist: the API routes declare an {id} wildcard, and the web UI
// serves /view/<id>, /view/<id>.html and /view/<id>.txt.
func requestMessageID(r *http.Request) string {
	if id := r.PathValue("id"); id != "" {
		return id
	}

	prefix := config.Webroot + "view/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return ""
	}

	id := strings.TrimPrefix(r.URL.Path, prefix)
	id = strings.TrimSuffix(id, ".html")
	id = strings.TrimSuffix(id, ".txt")

	// "latest" is resolved by its own handler, which applies the scope when
	// it queries for the most recent message.
	if id == "latest" {
		return ""
	}

	return id
}

// enforceMessageScope blocks requests for a message outside the caller's scope.
// It reports whether the request may proceed, and has already written the
// response when it may not.
//
// This lives in the middleware rather than in each handler on purpose. There
// are nine by-ID routes (raw, headers, part, thumb, release, html-check,
// link-check, sa-check, and the message itself) plus the web UI views, none of
// which pass through searchQueryBuilder. Message IDs are guessable, so a single
// route missing the check would defeat the isolation on all of them — and a
// route added later would silently inherit the gap.
//
// A message outside the scope is reported as 404, not 403: whether an ID exists
// is itself information the caller is not entitled to.
func enforceMessageScope(w http.ResponseWriter, r *http.Request) bool {
	sc := scope.FromRequest(r)
	if sc.IsUnrestricted() {
		return true
	}

	id := requestMessageID(r)
	if id == "" {
		return true
	}

	ok, err := storage.MessageInScope(id, sc)
	if err != nil {
		logger.Log().Errorf("[scope] %s", err.Error())
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return false
	}

	if !ok {
		http.Error(w, "Message not found", http.StatusNotFound)
		return false
	}

	return true
}
