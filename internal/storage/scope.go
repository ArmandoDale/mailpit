package storage

import (
	"context"
	"database/sql"
	"strings"

	"github.com/axllent/mailpit/internal/scope"
	"github.com/leporo/sqlf"
)

// untaggedPredicate matches messages carrying no tags at all.
func untaggedPredicate() string {
	return `m.ID NOT IN (SELECT mt.ID FROM ` + tenant("message_tags") + ` mt)`
}

// taggedPredicate matches messages carrying at least one of n tags. The caller
// supplies the tag names as query arguments in the same order.
func taggedPredicate(n int) string {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", n), ",")

	return `m.ID IN (SELECT mt.ID FROM ` + tenant("message_tags") + ` mt
		JOIN ` + tenant("tags") + ` t ON mt.TagID = t.ID
		WHERE t.Name IN (` + placeholders + `))`
}

// applyScope restricts q to the messages the scope allows. The mailbox table
// must be aliased as "m", which every query built here already does.
//
// This is the single chokepoint for read/delete visibility: every search-based
// operation funnels through searchQueryBuilder, which calls this.
func applyScope(q *sqlf.Stmt, sc scope.Scope) {
	if sc.IsUnrestricted() {
		return
	}

	tags := sc.Tags()

	if len(tags) == 0 {
		if sc.IncludeUntagged() {
			q.Where(untaggedPredicate())
		} else {
			// A scope with no tags and no untagged access grants nothing.
			// Deny explicitly rather than leaving the query unconstrained.
			q.Where("1 = 0")
		}

		return
	}

	args := make([]any, len(tags))
	for i, t := range tags {
		args[i] = t
	}

	if sc.IncludeUntagged() {
		q.Where("("+taggedPredicate(len(tags))+" OR "+untaggedPredicate()+")", args...)
	} else {
		q.Where(taggedPredicate(len(tags)), args...)
	}
}

// MessageInScope reports whether a single message is visible to the scope.
//
// The by-ID routes (raw, headers, parts, thumbnails, checks, release) do not go
// through searchQueryBuilder, so each must consult this before returning
// anything. Message IDs are guessable, so skipping the check on any one route
// defeats the isolation on all of them.
func MessageInScope(id string, sc scope.Scope) (bool, error) {
	if sc.IsUnrestricted() {
		return true, nil
	}

	var total float64 // use float64 for rqlite compatibility

	q := sqlf.From(tenant("mailbox")+" m").
		Select("COUNT(*)").To(&total).
		Where("m.ID = ?", id)

	applyScope(q, sc)

	if err := q.QueryRowAndClose(context.TODO(), db); err != nil {
		return false, err
	}

	return total > 0, nil
}

// FilterIDsInScope returns the subset of ids the scope may act on.
//
// The by-ID middleware only covers identifiers in the URL path. Several
// endpoints instead take a list of IDs in the request body — delete, read
// status, tagging — and those must be filtered here, or a project user could
// name another project's messages and act on them.
func FilterIDsInScope(ids []string, sc scope.Scope) ([]string, error) {
	if sc.IsUnrestricted() || len(ids) == 0 {
		return ids, nil
	}

	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")

	var id string
	allowed := make([]string, 0, len(ids))

	q := sqlf.From(tenant("mailbox")+" m").
		Select("m.ID").To(&id).
		Where("m.ID IN ("+placeholders+")", args...)

	applyScope(q, sc)

	if err := q.QueryAndClose(context.TODO(), db, func(_ *sql.Rows) {
		allowed = append(allowed, id)
	}); err != nil {
		return nil, err
	}

	return allowed, nil
}

// DeleteAllInScope deletes everything the scope can see.
//
// An unrestricted caller gets the fast truncate path; a project user gets a
// scoped delete, so "Delete all" empties their own mail and nobody else's.
func DeleteAllInScope(sc scope.Scope) error {
	if sc.IsUnrestricted() {
		return DeleteAllMessages()
	}

	return DeleteSearch("", "", sc)
}

// MarkAllReadInScope marks everything the scope can see as read or unread.
func MarkAllReadInScope(read bool, sc scope.Scope) error {
	if sc.IsUnrestricted() {
		if read {
			return MarkAllRead()
		}

		return MarkAllUnread()
	}

	return SetSearchReadStatus("", "", read, sc)
}
