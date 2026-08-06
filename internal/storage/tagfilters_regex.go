package storage

import (
	"context"
	"encoding/json"
	"net/mail"
	"regexp"
	"strings"

	"github.com/axllent/mailpit/config"
	"github.com/axllent/mailpit/internal/logger"
	"github.com/axllent/mailpit/internal/tools"
	"github.com/leporo/sqlf"
)

// Tag rule types.
const (
	// TagRuleSearch matches using Mailpit's search syntax, compiled to SQL.
	// This is the original behaviour and stays the default.
	TagRuleSearch = "search"

	// TagRuleRegex matches a regular expression against a message field.
	// Search syntax cannot express anchors, alternation or capture groups,
	// which is what an administrator needs to turn a recipient domain into a
	// tag.
	TagRuleRegex = "regex"
)

// Fields a regex rule can be applied to.
const (
	FieldTo      = "to"
	FieldFrom    = "from"
	FieldCc      = "cc"
	FieldBcc     = "bcc"
	FieldSubject = "subject"

	// FieldAny matches against every address and the subject.
	FieldAny = "any"
)

// validFields is the set accepted from configuration.
var validFields = map[string]bool{
	FieldTo: true, FieldFrom: true, FieldCc: true,
	FieldBcc: true, FieldSubject: true, FieldAny: true,
}

// NormaliseRuleType returns a supported rule type, defaulting to search so
// that rules written before regex support keep working unchanged.
func NormaliseRuleType(t string) string {
	if strings.EqualFold(strings.TrimSpace(t), TagRuleRegex) {
		return TagRuleRegex
	}

	return TagRuleSearch
}

// NormaliseRuleField returns a supported field, defaulting to the recipient —
// the common case being "tag by the domain the mail was sent to".
func NormaliseRuleField(f string) string {
	f = strings.ToLower(strings.TrimSpace(f))
	if validFields[f] {
		return f
	}

	return FieldTo
}

// messageFields holds the values a regex rule can be evaluated against.
type messageFields struct {
	Subject string
	From    []string
	To      []string
	Cc      []string
	Bcc     []string
}

// values returns the strings to test for a given field.
func (m messageFields) values(field string) []string {
	switch field {
	case FieldFrom:
		return m.From
	case FieldCc:
		return m.Cc
	case FieldBcc:
		return m.Bcc
	case FieldSubject:
		return []string{m.Subject}
	case FieldAny:
		out := make([]string, 0, len(m.From)+len(m.To)+len(m.Cc)+len(m.Bcc)+1)
		out = append(out, m.From...)
		out = append(out, m.To...)
		out = append(out, m.Cc...)
		out = append(out, m.Bcc...)

		return append(out, m.Subject)
	default:
		return m.To
	}
}

// loadMessageFields reads the values regex rules need. It is called once per
// message rather than once per rule.
func loadMessageFields(id string) (messageFields, error) {
	var subject, metadata string

	f := messageFields{}

	err := sqlf.From(tenant("mailbox")).
		Select("Subject").To(&subject).
		Select("Metadata").To(&metadata).
		Where("ID = ?", id).
		QueryRowAndClose(context.TODO(), db)
	if err != nil {
		return f, err
	}

	f.Subject = subject

	var meta Metadata
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		return f, err
	}

	addresses := func(list []*mail.Address) []string {
		out := make([]string, 0, len(list))
		for _, a := range list {
			if a != nil && a.Address != "" {
				out = append(out, a.Address)
			}
		}

		return out
	}

	if meta.From != nil && meta.From.Address != "" {
		f.From = []string{meta.From.Address}
	}

	f.To = addresses(meta.To)
	f.Cc = addresses(meta.Cc)
	f.Bcc = addresses(meta.Bcc)

	return f, nil
}

// regexTagsFor evaluates one compiled regex rule and returns the tags it
// produces.
//
// Tag templates may reference capture groups, so a single rule can derive the
// tag from the address itself: matching `@([a-z0-9-]+)\.dev\.it$` on the
// recipient with the tag template `$1` tags each message with its own
// subdomain, instead of needing one rule per project.
func regexTagsFor(f TagFilter, fields messageFields) []string {
	tags := []string{}
	seen := map[string]bool{}

	for _, value := range fields.values(f.Field) {
		match := f.Regexp.FindStringSubmatchIndex(value)
		if match == nil {
			continue
		}

		for _, template := range f.Tags {
			expanded := string(f.Regexp.ExpandString(nil, template, value, match))

			expanded = cleanExpandedTag(expanded)
			if expanded == "" || seen[strings.ToLower(expanded)] {
				continue
			}

			seen[strings.ToLower(expanded)] = true
			tags = append(tags, expanded)
		}
	}

	return tags
}

// cleanExpandedTag applies the same validation as any other tag, since the
// value may come from a capture group and therefore from the message itself.
func cleanExpandedTag(s string) string {
	t := tools.CleanTag(s)
	if t == "" || !config.ValidTagRegexp.MatchString(t) {
		return ""
	}

	return t
}

// compileRegexRule builds the compiled form of a regex rule, or reports why it
// cannot be used. An invalid expression is skipped with a warning: one bad
// rule entered in the UI must not stop the others from working.
func compileRegexRule(match string) (*regexp.Regexp, bool) {
	re, err := regexp.Compile(match)
	if err != nil {
		logger.Log().Warnf("[tags] ignoring rule with invalid regex %q: %s", match, err.Error())
		return nil, false
	}

	return re, true
}
