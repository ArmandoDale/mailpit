package storage

import (
	"context"
	"database/sql"
	"regexp"
	"strings"

	"github.com/axllent/mailpit/config"
	"github.com/axllent/mailpit/internal/logger"
	"github.com/axllent/mailpit/internal/scope"
	"github.com/axllent/mailpit/internal/tools"
	"github.com/leporo/sqlf"
)

// TagFilter struct
type TagFilter struct {
	// Match is the user-defined match
	Match string
	// Type is either TagRuleSearch or TagRuleRegex
	Type string
	// Field is the message field a regex rule applies to
	Field string
	// SQL represents the SQL equivalent of Match, for search rules
	SQL *sqlf.Stmt
	// Regexp is the compiled expression, for regex rules
	Regexp *regexp.Regexp
	// Tags to add on match. For regex rules these are templates and may
	// reference capture groups.
	Tags []string
}

var tagFilters = []TagFilter{}

// LoadTagFilters loads tag filters from the config and pre-generates the SQL query
func LoadTagFilters() {
	tagFilters = []TagFilter{}

	allFilters := make([]TagFilterRule, 0, len(config.TagFilters)+8)

	for _, t := range config.TagFilters {
		allFilters = append(allFilters, TagFilterRule{Match: t.Match, Tags: t.Tags})
	}

	for _, t := range GetRuntimeTagFilters() {
		allFilters = append(allFilters, t)
	}

	for _, t := range allFilters {
		match := strings.TrimSpace(t.Match)
		if match == "" {
			logger.Log().Warnf("[tags] ignoring tag item with missing 'match'")
			continue
		}
		if len(t.Tags) == 0 {
			logger.Log().Warnf("[tags] ignoring tag items with missing 'tags' array")
			continue
		}

		validTags := []string{}
		for _, tag := range t.Tags {
			// A regex rule's tags may be templates such as "$1", which are
			// not valid tag names until expanded.
			if NormaliseRuleType(t.Type) == TagRuleRegex {
				validTags = append(validTags, tag)
				continue
			}

			tagName := tools.CleanTag(tag)
			if !config.ValidTagRegexp.MatchString(tagName) || len(tagName) == 0 {
				logger.Log().Warnf("[tags] invalid tag (%s) - can only contain spaces, letters, numbers, - & _", tagName)
				continue
			}
			validTags = append(validTags, tagName)
		}

		if len(validTags) == 0 {
			continue
		}

		if NormaliseRuleType(t.Type) == TagRuleRegex {
			re, ok := compileRegexRule(match)
			if !ok {
				continue
			}

			tagFilters = append(tagFilters, TagFilter{
				Match:  match,
				Type:   TagRuleRegex,
				Field:  NormaliseRuleField(t.Field),
				Regexp: re,
				// Regex tags are templates, so they keep the raw value:
				// validation happens after capture-group expansion.
				Tags: t.Tags,
			})

			continue
		}

		tagFilters = append(tagFilters, TagFilter{
			Match: match,
			Type:  TagRuleSearch,
			Tags:  validTags,
			SQL:   searchQueryBuilder(match, "", scope.Unrestricted()),
		})
	}
}

// TagFilterMatches returns a slice of matching tags from a message
func tagFilterMatches(id string) []string {
	tags := []string{}

	if len(tagFilters) == 0 {
		return tags
	}

	// Regex rules read the message itself, so load its fields once instead of
	// once per rule — and only when a regex rule actually exists.
	var (
		fields       messageFields
		fieldsLoaded bool
	)

	for _, f := range tagFilters {
		if f.Type == TagRuleRegex {
			if !fieldsLoaded {
				var err error
				if fields, err = loadMessageFields(id); err != nil {
					logger.Log().Errorf("[tags] %s", err.Error())
					return tags
				}
				fieldsLoaded = true
			}

			tags = append(tags, regexTagsFor(f, fields)...)

			continue
		}

		var matchID string
		q := f.SQL.Clone().Where("ID = ?", id)
		if err := q.QueryAndClose(context.Background(), db, func(row *sql.Rows) {
			var ignore sql.NullString

			if err := row.Scan(&ignore, &matchID, &ignore, &ignore, &ignore, &ignore, &ignore, &ignore, &ignore, &ignore, &ignore, &ignore, &ignore, &ignore); err != nil {
				logger.Log().Errorf("[db] %s", err.Error())
				return
			}
		}); err != nil {
			logger.Log().Errorf("[db] %s", err.Error())
			return tags
		}
		if matchID == id {
			tags = append(tags, f.Tags...)
		}
	}

	return tags
}
