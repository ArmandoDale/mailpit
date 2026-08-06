package storage

import (
	"bytes"
	"testing"

	"github.com/axllent/mailpit/config"
	"github.com/jhillyerd/enmime/v2"
)

// storeAddressed saves a message with explicit sender and recipient, so regex
// rules have something realistic to match against.
func storeAddressed(t *testing.T, from, to, subject string) string {
	t.Helper()

	env, err := enmime.Builder().
		From("", from).
		To("", to).
		Subject(subject).
		Text([]byte("body")).
		Build()
	if err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	if err := env.Encode(buf); err != nil {
		t.Fatal(err)
	}

	b := buf.Bytes()

	id, err := Store(&b, nil)
	if err != nil {
		t.Fatal(err)
	}

	return id
}

func tagsOf(t *testing.T, id string) map[string]bool {
	t.Helper()

	out := map[string]bool{}
	for _, tag := range getMessageTags(id) {
		out[tag] = true
	}

	return out
}

// setRules installs runtime rules and restores none afterwards.
func setRules(t *testing.T, rules []TagFilterRule) {
	t.Helper()

	if _, err := SetRuntimeTagFilters(rules); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = SetRuntimeTagFilters(nil)
	})
}

// TestRegexRuleOnRecipientDomain is the case from the requirements: mail sent
// to a project domain is tagged with that project.
func TestRegexRuleOnRecipientDomain(t *testing.T) {
	setup(config.DBTenantID(""))

	setRules(t, []TagFilterRule{
		{Type: TagRuleRegex, Field: FieldTo, Match: `@dev\.it$`, Tags: []string{"DEV"}},
	})

	hit := storeAddressed(t, "ci@example.com", "team@dev.it", "deploy")
	miss := storeAddressed(t, "ci@example.com", "team@prod.it", "deploy")

	if !tagsOf(t, hit)["DEV"] {
		t.Error("recipient on @dev.it was not tagged DEV")
	}
	if tagsOf(t, miss)["DEV"] {
		t.Error("recipient on another domain was tagged DEV")
	}
}

// TestRegexCaptureGroupBecomesTag covers deriving the tag from the address
// itself, so one rule serves every project instead of one rule per domain.
func TestRegexCaptureGroupBecomesTag(t *testing.T) {
	setup(config.DBTenantID(""))

	setRules(t, []TagFilterRule{
		{Type: TagRuleRegex, Field: FieldTo, Match: `@([a-z0-9-]+)\.dev\.it$`, Tags: []string{"$1"}},
	})

	alfa := storeAddressed(t, "ci@example.com", "team@alfa.dev.it", "deploy")
	beta := storeAddressed(t, "ci@example.com", "team@beta.dev.it", "deploy")

	if !tagsOf(t, alfa)["alfa"] {
		t.Errorf("expected tag alfa, got %v", getMessageTags(alfa))
	}
	if !tagsOf(t, beta)["beta"] {
		t.Errorf("expected tag beta, got %v", getMessageTags(beta))
	}
	if tagsOf(t, alfa)["beta"] {
		t.Error("capture group leaked between messages")
	}
}

// TestRegexAnchorsWorkWhereSearchCannot is the reason regex was added: search
// syntax matches substrings, so it cannot express "ends with".
func TestRegexAnchorsWorkWhereSearchCannot(t *testing.T) {
	setup(config.DBTenantID(""))

	setRules(t, []TagFilterRule{
		{Type: TagRuleRegex, Field: FieldTo, Match: `@dev\.it$`, Tags: []string{"DEV"}},
	})

	// A lookalike domain that a substring match on "dev.it" would catch.
	spoof := storeAddressed(t, "ci@example.com", "team@dev.it.attacker.com", "deploy")

	if tagsOf(t, spoof)["DEV"] {
		t.Error("anchored regex matched a domain that only contains dev.it")
	}
}

func TestRegexRuleFields(t *testing.T) {
	setup(config.DBTenantID(""))

	setRules(t, []TagFilterRule{
		{Type: TagRuleRegex, Field: FieldFrom, Match: `^alert@`, Tags: []string{"ALERT"}},
		{Type: TagRuleRegex, Field: FieldSubject, Match: `(?i)urgente`, Tags: []string{"URGENTE"}},
	})

	id := storeAddressed(t, "alert@monitoring.it", "team@dev.it", "Intervento URGENTE richiesto")

	tags := tagsOf(t, id)
	if !tags["ALERT"] {
		t.Error("from-field rule did not match")
	}
	if !tags["URGENTE"] {
		t.Error("subject rule did not match (case-insensitive flag)")
	}
}

// TestInvalidRegexIsRejectedAtSave: a broken expression must not be stored,
// otherwise it fails silently on every incoming message.
func TestInvalidRegexIsRejectedAtSave(t *testing.T) {
	setup(config.DBTenantID(""))

	saved, err := SetRuntimeTagFilters([]TagFilterRule{
		{Type: TagRuleRegex, Field: FieldTo, Match: `@dev\.it(`, Tags: []string{"DEV"}},
		{Type: TagRuleRegex, Field: FieldTo, Match: `@ok\.it$`, Tags: []string{"OK"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _, _ = SetRuntimeTagFilters(nil) })

	if len(saved) != 1 {
		t.Fatalf("saved %d rules, want 1 — the invalid regex should be dropped", len(saved))
	}
	if saved[0].Tags[0] != "OK" {
		t.Errorf("the wrong rule survived: %+v", saved[0])
	}
}

// TestSearchRulesStillWork guards backward compatibility: rules saved before
// regex support have no Type and must keep behaving as search rules.
func TestSearchRulesStillWork(t *testing.T) {
	setup(config.DBTenantID(""))

	setRules(t, []TagFilterRule{
		{Match: "subject:fattura", Tags: []string{"Fattura"}},
	})

	id := storeAddressed(t, "billing@example.com", "team@dev.it", "La tua fattura")

	if !tagsOf(t, id)["Fattura"] {
		t.Errorf("legacy search rule stopped matching, got %v", getMessageTags(id))
	}
}

// TestRegexAppliesRetroactively covers the "Apply to existing messages" path.
func TestRegexAppliesRetroactively(t *testing.T) {
	setup(config.DBTenantID(""))

	id := storeAddressed(t, "ci@example.com", "team@dev.it", "deploy")

	if tagsOf(t, id)["DEV"] {
		t.Fatal("message was tagged before any rule existed")
	}

	setRules(t, []TagFilterRule{
		{Type: TagRuleRegex, Field: FieldTo, Match: `@dev\.it$`, Tags: []string{"DEV"}},
	})

	n, err := ApplyTagFiltersToAll()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("updated %d messages, want 1", n)
	}

	if !tagsOf(t, id)["DEV"] {
		t.Error("existing message was not tagged retroactively")
	}
}

// TestRegexTagsAreValidated: a capture group takes its value from the message,
// so the expanded tag must still pass tag validation.
func TestRegexTagsAreValidated(t *testing.T) {
	setup(config.DBTenantID(""))

	setRules(t, []TagFilterRule{
		{Type: TagRuleRegex, Field: FieldFrom, Match: `^(.+)@`, Tags: []string{"$1"}},
	})

	// The local part contains characters that are not valid in a tag.
	id := storeAddressed(t, "we!rd+stuff@example.com", "team@dev.it", "hello")

	for _, tag := range getMessageTags(id) {
		if !config.ValidTagRegexp.MatchString(tag) {
			t.Errorf("an invalid tag %q was stored from a capture group", tag)
		}
	}
}
