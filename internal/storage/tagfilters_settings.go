package storage

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/axllent/mailpit/internal/logger"
	"github.com/axllent/mailpit/internal/tools"
	"github.com/axllent/mailpit/server/websockets"
)

const runtimeTagFilterSettingKey = "TagFilters"

// TagFilterRule stores a tag rule used for automatic tagging.
type TagFilterRule struct {
	Match string   `json:"match"`
	Tags  []string `json:"tags"`
}

// GetRuntimeTagFilters returns runtime tag filter rules configured via the UI/API.
func GetRuntimeTagFilters() []TagFilterRule {
	raw := strings.TrimSpace(SettingGet(runtimeTagFilterSettingKey))
	if raw == "" {
		return []TagFilterRule{}
	}

	rules := []TagFilterRule{}
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		logger.Log().Warnf("[tags] invalid runtime tag filters in settings: %s", err.Error())
		return []TagFilterRule{}
	}

	return normalizeTagFilterRules(rules)
}

// SetRuntimeTagFilters validates and stores runtime tag filter rules.
func SetRuntimeTagFilters(rules []TagFilterRule) ([]TagFilterRule, error) {
	normalized := normalizeTagFilterRules(rules)
	b, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}

	if err := SettingPut(runtimeTagFilterSettingKey, string(b)); err != nil {
		return nil, err
	}

	LoadTagFilters()

	// keep the versionable representation of the rules in step with the database
	writeTagFiltersToFile(normalized)

	return normalized, nil
}

func normalizeTagFilterRules(rules []TagFilterRule) []TagFilterRule {
	normalized := []TagFilterRule{}

	for _, r := range rules {
		match := strings.TrimSpace(r.Match)
		if match == "" {
			continue
		}

		tags := []string{}
		seen := map[string]struct{}{}
		for _, t := range r.Tags {
			cleaned := tools.CleanTag(t)
			if cleaned == "" {
				continue
			}

			key := strings.ToLower(cleaned)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			tags = append(tags, cleaned)
		}

		if len(tags) == 0 {
			continue
		}

		normalized = append(normalized, TagFilterRule{Match: match, Tags: tags})
	}

	return normalized
}

// ApplyTagFiltersToAll applies current tag filter rules to all existing messages,
// adding matching tags (never removing existing ones).
// Returns the number of messages updated.
func ApplyTagFiltersToAll() (int, error) {
	if len(tagFilters) == 0 {
		return 0, nil
	}

	// Collect all message IDs
	rows, err := db.QueryContext(context.Background(),
		"SELECT ID FROM "+tenant("mailbox"))
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}

	updated := 0
	for _, id := range ids {
		matchedTags := tagFilterMatches(id)
		if len(matchedTags) == 0 {
			continue
		}

		// Fetch existing tags for the message
		existing := getMessageTags(id)
		existingSet := make(map[string]struct{}, len(existing))
		for _, t := range existing {
			existingSet[strings.ToLower(t)] = struct{}{}
		}

		// Add only tags not already present
		added := false
		for _, tag := range matchedTags {
			if _, ok := existingSet[strings.ToLower(tag)]; ok {
				continue
			}
			if _, err := addMessageTag(id, tag); err != nil {
				logger.Log().Errorf("[tags] error adding tag %q to %s: %s", tag, id, err.Error())
				continue
			}
			added = true
		}

		if added {
			updated++
			// Broadcast individual message update so the UI refreshes without a manual reload
			d := struct {
				ID   string
				Tags []string
			}{ID: id, Tags: getMessageTags(id)}
			websockets.Broadcast("update", d)
		}
	}

	if updated > 0 {
		BroadcastMailboxStats()
	}

	return updated, nil
}
