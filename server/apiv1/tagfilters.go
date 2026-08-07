package apiv1

import (
	"encoding/json"
	"net/http"

	"github.com/axllent/mailpit/internal/storage"
)

// Tag filter rules are global configuration, not per-user data: they decide
// which tag every incoming message gets, and therefore which project can see
// it. A caller who can write them can grant themselves any scope — tagging
// every message with a tag they hold makes the whole mailbox visible — so all
// three routes are restricted to unrestricted callers.
//
// The read is restricted too: the rules spell out the tag layout of every
// project, which a project user has no business enumerating.

// TagFiltersResponse carries both sources of tag rules.
//
// Two lists rather than one merged list because they are not the same kind of
// thing: Filters can be written here, ConfigFilters comes from the deployment
// configuration and can only be read. Merging them would invite an edit that
// silently disappears on the next restart.
type TagFiltersResponse struct {
	// Filters are the runtime rules, editable through this API.
	Filters []storage.TagFilterRule

	// ConfigFilters come from --tags-config and are read-only here. They are
	// returned because tagging and "apply to existing" both use them, so an
	// administrator who cannot see them cannot explain the result.
	ConfigFilters []storage.TagFilterRule
}

// tagFiltersResponse builds the reply both the read and the write return.
func tagFiltersResponse(runtime []storage.TagFilterRule) TagFiltersResponse {
	return TagFiltersResponse{
		Filters:       runtime,
		ConfigFilters: storage.GetConfigTagFilters(),
	}
}

// GetTagFilters (method: GET) returns the auto-tag filter rules, runtime and
// configured.
func GetTagFilters(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}

	w.Header().Add("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(tagFiltersResponse(storage.GetRuntimeTagFilters())); err != nil {
		httpError(w, err.Error())
	}
}

// SetTagFilters (method: PUT) sets runtime auto-tag filter rules.
func SetTagFilters(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}

	decoder := json.NewDecoder(r.Body)

	var data struct {
		Filters []storage.TagFilterRule
	}

	if err := decoder.Decode(&data); err != nil {
		httpError(w, err.Error())
		return
	}

	rules, err := storage.SetRuntimeTagFilters(data.Filters)
	if err != nil {
		httpError(w, err.Error())
		return
	}

	w.Header().Add("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(tagFiltersResponse(rules)); err != nil {
		httpError(w, err.Error())
	}
}

// ApplyTagFilters (method: POST) applies current tag filter rules to all existing messages.
func ApplyTagFilters(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}

	count, err := storage.ApplyTagFiltersToAll()
	if err != nil {
		httpError(w, err.Error())
		return
	}

	w.Header().Add("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]int{"updated": count}); err != nil {
		httpError(w, err.Error())
	}
}
