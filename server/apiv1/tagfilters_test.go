package apiv1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/axllent/mailpit/config"
	"github.com/axllent/mailpit/internal/logger"
	"github.com/axllent/mailpit/internal/scope"
	"github.com/axllent/mailpit/internal/storage"
)

// TestMain brings up an in-memory database: reading the tag rules also reads
// the runtime ones, which live in the settings table.
func TestMain(m *testing.M) {
	logger.NoLogging = true
	config.MaxMessages = 0
	config.Database = ""

	if err := storage.InitDB(); err != nil {
		panic(err)
	}

	code := m.Run()

	storage.Close()

	os.Exit(code)
}

// withConfigTagFilter installs a rule as if it came from --tags-config.
func withConfigTagFilter(t *testing.T, match string, tags []string) {
	t.Helper()

	orig := config.TagFilters
	t.Cleanup(func() { config.TagFilters = orig })

	config.TagFilters = []config.AutoTag{{Match: match, Tags: tags}}
}

// getTagFilters calls the handler as an administrator.
func getTagFilters(t *testing.T) TagFiltersResponse {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, "/api/v1/tag-filters", nil)
	r = r.WithContext(scope.NewContext(r.Context(), scope.Unrestricted()))
	w := httptest.NewRecorder()

	GetTagFilters(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var res TagFiltersResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}

	return res
}

// TestTagFiltersExposeConfigRules: rules in the configuration file tag
// incoming mail and are used by "apply to existing", so an administrator has
// to be able to see them. Before this the panel was empty while messages
// arrived tagged, and the apply button ran rules never shown.
func TestTagFiltersExposeConfigRules(t *testing.T) {
	withConfigTagFilter(t, `subject:"Benvenuto"`, []string{"da-file-di-configurazione"})

	res := getTagFilters(t)

	if len(res.ConfigFilters) != 1 {
		t.Fatalf("expected 1 rule from the configuration file, got %d", len(res.ConfigFilters))
	}

	rule := res.ConfigFilters[0]

	if rule.Match != `subject:"Benvenuto"` {
		t.Errorf("unexpected match: %q", rule.Match)
	}

	if len(rule.Tags) != 1 || rule.Tags[0] != "da-file-di-configurazione" {
		t.Errorf("unexpected tags from the configuration file: %v", rule.Tags)
	}

	// The YAML format has no type or field, so these are search rules.
	if rule.Type != storage.TagRuleSearch {
		t.Errorf("configuration rules are search rules, got %q", rule.Type)
	}
}

// TestTagFiltersKeepTheTwoSourcesApart: the file belongs to whoever runs the
// deployment, so its rules must never appear among the editable ones, where
// saving would silently drop them.
func TestTagFiltersKeepTheTwoSourcesApart(t *testing.T) {
	withConfigTagFilter(t, `subject:"Benvenuto"`, []string{"da-file-di-configurazione"})

	res := getTagFilters(t)

	for _, rule := range res.Filters {
		if rule.Match == `subject:"Benvenuto"` {
			t.Error("a rule from the configuration file was returned as editable")
		}
	}
}

// TestTagFiltersWithoutConfigFile: the common case must stay an empty list
// rather than null, which the UI would have to special-case.
func TestTagFiltersWithoutConfigFile(t *testing.T) {
	orig := config.TagFilters
	t.Cleanup(func() { config.TagFilters = orig })

	config.TagFilters = nil

	if res := getTagFilters(t); res.ConfigFilters == nil {
		t.Error("expected an empty list, got null")
	}
}
