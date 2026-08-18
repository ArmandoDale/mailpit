package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/axllent/mailpit/config"
	"github.com/axllent/mailpit/internal/logger"
	"github.com/goccy/go-yaml"
)

// tagFilterSeedSettingKey records that the tags-config file has already seeded
// the runtime rules. Without it, deleting every rule from the web UI would be
// undone by the next restart re-importing the file.
const tagFilterSeedSettingKey = "TagFiltersSeeded"

// yamlTagFilters mirrors the structure of the --tags-config file, so that a file
// written here stays readable by the stock Mailpit parser.
type yamlTagFilters struct {
	Filters []yamlTagFilter `yaml:"filters"`
}

type yamlTagFilter struct {
	Match string `yaml:"match"`
	Tags  string `yaml:"tags"`
}

// seedTagFiltersFromFile imports the rules of the --tags-config file into the
// database, once, on the first start of an instance that has one configured.
//
// The import is what makes the file a seed rather than a second, invisible
// source of rules: once imported the rules behave exactly like any rule created
// from the web UI, and appear alongside them.
func seedTagFiltersFromFile() {
	if config.TagsConfig == "" {
		return
	}

	if SettingGet(tagFilterSeedSettingKey) != "" {
		return // already seeded, the database is now authoritative
	}

	rules := make([]TagFilterRule, 0, len(config.TagsConfigFilters))
	for _, t := range config.TagsConfigFilters {
		rules = append(rules, TagFilterRule{Match: t.Match, Tags: t.Tags})
	}

	existing := GetRuntimeTagFilters()
	if len(existing) > 0 {
		// An instance upgraded from a version without seeding: keep what is in
		// the database, merge in anything from the file that is not there yet.
		known := map[string]struct{}{}
		for _, r := range existing {
			known[strings.ToLower(strings.TrimSpace(r.Match))] = struct{}{}
		}

		merged := existing
		for _, r := range rules {
			if _, ok := known[strings.ToLower(strings.TrimSpace(r.Match))]; !ok {
				merged = append(merged, r)
			}
		}
		rules = merged
	}

	if _, err := SetRuntimeTagFilters(rules); err != nil {
		logger.Log().Errorf("[tags] could not seed rules from %s: %s", config.TagsConfig, err.Error())
		return
	}

	if err := SettingPut(tagFilterSeedSettingKey, "1"); err != nil {
		logger.Log().Errorf("[tags] could not record the seeding of %s: %s", config.TagsConfig, err.Error())
		return
	}

	logger.Log().Infof("[tags] seeded %d rule(s) from %s", len(rules), config.TagsConfig)
}

// writeTagFiltersToFile writes the current rules back to the --tags-config file.
//
// The file is the versionable representation of rules that otherwise exist only
// in the database, which is not part of any backup: keeping it in step with every
// change is what allows an instance to be rebuilt with its tagging intact.
//
// A failure here is logged but never propagated: the rule has already been saved,
// and an unwritable file must not make the feature unusable.
func writeTagFiltersToFile(rules []TagFilterRule) {
	if config.TagsConfig == "" {
		return
	}

	if err := writeTagFilterFile(config.TagsConfig, rules); err != nil {
		logger.Log().Errorf("[tags] rules saved, but %s could not be updated: %s", config.TagsConfig, err.Error())
		logger.Log().Warnf("[tags] the current rules exist only in the database until this is resolved")
		return
	}

	logger.Log().Debugf("[tags] wrote %d rule(s) to %s", len(rules), config.TagsConfig)
}

// writeTagFilterFile serialises the rules and replaces the file atomically, so
// that an interrupted write cannot leave a half-written file behind.
func writeTagFilterFile(path string, rules []TagFilterRule) error {
	path = filepath.Clean(path)

	out := yamlTagFilters{Filters: []yamlTagFilter{}}
	for _, r := range rules {
		out.Filters = append(out.Filters, yamlTagFilter{
			Match: r.Match,
			Tags:  strings.Join(r.Tags, ","),
		})
	}

	data, err := yaml.Marshal(out)
	if err != nil {
		return err
	}

	header := "# Managed by Mailpit: rewritten on every change to the tag filter rules.\n" +
		"# Edits made here are read only when a new database is seeded from this file.\n"

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tagfilters-*.yml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	defer func() {
		// no-op once the rename succeeded
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(header + string(data)); err != nil {
		_ = tmp.Close()
		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Chmod(tmpName, 0600); err != nil {
		return fmt.Errorf("could not set permissions: %s", err.Error())
	}

	return os.Rename(tmpName, path)
}
