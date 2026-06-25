package apiv1

import (
	"encoding/json"
	"net/http"

	"github.com/axllent/mailpit/internal/storage"
)

// GetTagFilters (method: GET) returns runtime auto-tag filter rules.
func GetTagFilters(w http.ResponseWriter, _ *http.Request) {
	w.Header().Add("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(storage.GetRuntimeTagFilters()); err != nil {
		httpError(w, err.Error())
	}
}

// SetTagFilters (method: PUT) sets runtime auto-tag filter rules.
func SetTagFilters(w http.ResponseWriter, r *http.Request) {
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
	if err := json.NewEncoder(w).Encode(rules); err != nil {
		httpError(w, err.Error())
	}
}

// ApplyTagFilters (method: POST) applies current tag filter rules to all existing messages.
func ApplyTagFilters(w http.ResponseWriter, _ *http.Request) {
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
