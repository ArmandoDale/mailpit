// Package scope carries the set of tags a request is allowed to see.
//
// A Scope is derived from the authenticated user's roles (see the OIDC session
// middleware) and is threaded explicitly through every storage call that reads
// or deletes messages. It is deliberately an explicit parameter rather than an
// implicit lookup: forgetting to pass it is a compile error, not a silent
// widening of what the caller can see.
package scope

import (
	"context"
	"net/http"
	"sort"
	"strings"
)

type contextKey struct{}

var scopeKey = contextKey{}

// Enforce reports whether a request without a scope must be denied. It is set
// once at startup when authentication is configured, mirroring how
// auth.UICredentials gates basic auth.
//
// While it is false Mailpit serves an unrestricted mailbox exactly as upstream
// does, so the scope plumbing is inert. Once it is true a request that reached
// a handler without passing through the session middleware is a bug, and is
// denied rather than silently granted everything.
var Enforce bool

// Scope restricts which messages a request may read or delete.
//
// The zero value denies everything, so a Scope that was never populated cannot
// accidentally grant access. Use Unrestricted for callers that legitimately
// operate over the whole mailbox (background jobs, POP3, dump).
type Scope struct {
	// unrestricted grants access to every message regardless of tags.
	unrestricted bool

	// tags is the set of tag names the request may see. Only meaningful when
	// unrestricted is false. An empty list denies everything except untagged
	// messages (see IncludeUntagged).
	tags []string

	// includeUntagged also grants access to messages carrying no tags at all.
	// Messages that match no tag filter rule belong to no project; hiding them
	// would make mail silently disappear, so they stay visible.
	includeUntagged bool
}

// Unrestricted returns a Scope with no restrictions. Callers that use it are
// asserting they are not acting on behalf of a project-scoped user.
func Unrestricted() Scope {
	return Scope{unrestricted: true}
}

// Denied returns a Scope that grants nothing. It is the zero value, named for
// readability at call sites.
func Denied() Scope {
	return Scope{}
}

// ForTags returns a Scope limited to the given tags. Untagged messages are
// always included: they belong to no project, and hiding them would make mail
// appear to vanish.
func ForTags(tags []string) Scope {
	return Scope{tags: tags, includeUntagged: true}
}

// IsUnrestricted reports whether the scope grants access to everything.
func (s Scope) IsUnrestricted() bool {
	return s.unrestricted
}

// Tags returns the tag names the scope allows. Nil when unrestricted.
func (s Scope) Tags() []string {
	if s.unrestricted {
		return nil
	}

	return s.tags
}

// IncludeUntagged reports whether messages with no tags are visible.
func (s Scope) IncludeUntagged() bool {
	return !s.unrestricted && s.includeUntagged
}

// AllowsTags reports whether a message carrying these tags is visible.
//
// Used for live notifications, where the decision is made per connected client
// rather than in SQL, but must match what applyScope would allow.
func (s Scope) AllowsTags(tags []string) bool {
	if s.unrestricted {
		return true
	}

	if len(tags) == 0 {
		return s.includeUntagged
	}

	for _, t := range tags {
		for _, allowed := range s.tags {
			if t == allowed {
				return true
			}
		}
	}

	return false
}

// Key returns a stable identifier for the scope, so callers can group
// clients that see the same thing and compute per-scope data once.
func (s Scope) Key() string {
	if s.unrestricted {
		return "*"
	}

	sorted := append([]string(nil), s.tags...)
	sort.Strings(sorted)

	key := strings.Join(sorted, "")
	if s.includeUntagged {
		key += "+untagged"
	}

	return key
}

// NewContext returns a copy of ctx carrying s.
func NewContext(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey, s)
}

// FromContext returns the Scope stored in ctx, and whether one was present.
// When absent the returned Scope denies everything, so a caller that ignores
// the boolean fails closed.
func FromContext(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(scopeKey).(Scope)
	if !ok {
		return Denied(), false
	}

	return s, true
}

// FromRequest returns the Scope the session middleware attached to r.
//
// With Enforce off this yields an unrestricted scope, keeping behaviour
// identical to upstream Mailpit. With Enforce on a missing scope is denied.
func FromRequest(r *http.Request) Scope {
	if s, ok := FromContext(r.Context()); ok {
		return s
	}

	if Enforce {
		return Denied()
	}

	return Unrestricted()
}
