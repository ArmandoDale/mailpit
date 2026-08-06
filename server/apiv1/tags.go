package apiv1

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/axllent/mailpit/internal/scope"
	"github.com/axllent/mailpit/internal/storage"
	"github.com/axllent/mailpit/server/websockets"
)

// GetAllTags (method: GET) will get all tags currently in use
func GetAllTags(w http.ResponseWriter, r *http.Request) {
	// swagger:route GET /api/v1/tags tags GetAllTags
	//
	// # Get all current tags
	//
	// Returns a JSON array of all unique message tags.
	//
	//	Produces:
	//	  - application/json
	//
	//	Schemes: http, https
	//
	//	Responses:
	//	  200: ArrayResponse
	//    400: ErrorResponse

	w.Header().Add("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(storage.GetAllTags(scope.FromRequest(r))); err != nil {
		httpError(w, err.Error())
	}
}

// SetMessageTags (method: PUT) will set the tags for all provided IDs
func SetMessageTags(w http.ResponseWriter, r *http.Request) {
	// swagger:route PUT /api/v1/tags tags SetTagsParams
	//
	// # Set message tags
	//
	// This will overwrite any existing tags for selected message database IDs. To remove all tags from a message, pass an empty tags array.
	//
	//	Consumes:
	//	  - application/json
	//
	//	Produces:
	//	  - text/plain
	//
	//	Schemes: http, https
	//
	//	Responses:
	//	  200: OKResponse
	//    400: ErrorResponse

	decoder := json.NewDecoder(r.Body)

	var data struct {
		Tags []string
		IDs  []string
	}

	err := decoder.Decode(&data)
	if err != nil {
		httpError(w, err.Error())
		return
	}

	ids := data.IDs

	if len(ids) > 0 {
		// The IDs come from the request body, so filter them: tagging is a
		// write, and a project user must not touch another project's mail.
		ids, err = storage.FilterIDsInScope(ids, scope.FromRequest(r))
		if err != nil {
			httpError(w, err.Error())
			return
		}

		sc := scope.FromRequest(r)

		for _, id := range ids {
			tags := data.Tags

			if !sc.IsUnrestricted() {
				tags, err = tagsWithinScope(id, data.Tags, sc)
				if err != nil {
					http.Error(w, err.Error(), http.StatusForbidden)
					return
				}
			}

			if _, err := storage.SetMessageTags(id, tags); err != nil {
				httpError(w, err.Error())
				return
			}
		}
	}

	w.Header().Add("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

// RenameTag (method: PUT) used to rename a tag
func RenameTag(w http.ResponseWriter, r *http.Request) {
	// swagger:route PUT /api/v1/tags/{Tag} tags RenameTagParams
	//
	// # Rename a tag
	//
	// Renames an existing tag.
	//
	//	Produces:
	//	  - text/plain
	//
	//	Schemes: http, https
	//
	//	Responses:
	//	  200: OKResponse
	//    400: ErrorResponse

	tag := r.PathValue("tag")

	decoder := json.NewDecoder(r.Body)

	var data struct {
		Name string
	}

	err := decoder.Decode(&data)
	if err != nil {
		httpError(w, err.Error())
		return
	}

	// Renaming a tag is a global operation: it affects every project that
	// uses it. Restricted to administrators, which also makes application
	// tags safe from the users who merely consume them.
	if !requireAdmin(w, r) {
		return
	}

	if err := storage.RenameTag(tag, data.Name); err != nil {
		httpError(w, err.Error())
		return
	}

	websockets.Broadcast("prune", nil)

	w.Header().Add("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

// DeleteTag (method: DELETE) used to delete a tag
func DeleteTag(w http.ResponseWriter, r *http.Request) {
	// swagger:route DELETE /api/v1/tags/{Tag} tags DeleteTagParams
	//
	// # Delete a tag
	//
	// Deletes a tag. This will not delete any messages with the tag, but will remove the tag from any messages containing the tag.
	//
	//	Produces:
	//	  - text/plain
	//
	//	Schemes: http, https
	//
	//	Responses:
	//	  200: OKResponse
	//    400: ErrorResponse

	tag := r.PathValue("tag")

	// Deleting a tag is global, like renaming it.
	if !requireAdmin(w, r) {
		return
	}

	if err := storage.DeleteTag(tag); err != nil {
		httpError(w, err.Error())
		return
	}

	websockets.Broadcast("prune", nil)

	w.Header().Add("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

// tagsWithinScope works out what a restricted caller is actually allowed to
// write, given that SetMessageTags overwrites the whole tag set.
//
// Two things have to hold, and neither is about the caller's own convenience:
//
//   - every tag they ask for must be one they hold, or tagging becomes a way
//     to push a message into a project they cannot see;
//   - tags they do not hold must survive, or overwriting a message shared with
//     another project would quietly remove it from that project's view.
//
// The result must also not be empty: an untagged message is visible to
// everyone, so clearing the tags would be a way to publish it.
func tagsWithinScope(id string, requested []string, sc scope.Scope) ([]string, error) {
	for _, t := range requested {
		if !sc.AllowsTags([]string{t}) {
			return nil, fmt.Errorf("tag %q is outside your projects", t)
		}
	}

	final := []string{}
	seen := map[string]bool{}

	// Keep what the caller cannot see, so they cannot take it away either.
	for _, t := range storage.MessageTags(id) {
		if !sc.AllowsTags([]string{t}) && !seen[t] {
			final = append(final, t)
			seen[t] = true
		}
	}

	for _, t := range requested {
		if !seen[t] {
			final = append(final, t)
			seen[t] = true
		}
	}

	if len(final) == 0 {
		return nil, errors.New("removing every tag would make the message visible to everyone")
	}

	return final, nil
}

// requireAdmin blocks a request that is not made by an unrestricted caller.
// It reports whether the request may proceed, and has written the response
// when it may not.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if scope.FromRequest(r).IsUnrestricted() {
		return true
	}

	http.Error(w, "Forbidden", http.StatusForbidden)

	return false
}
