package oidc

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestParseRoles covers the two encodings a provider may use for a
// multi-valued claim. WSO2 can send either depending on how the claim was
// mapped, so neither may be assumed.
func TestParseRoles(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		separator string
		want      []string
	}{
		{"json array", `["alfa","beta"]`, ",", []string{"alfa", "beta"}},
		{"single element array", `["alfa"]`, ",", []string{"alfa"}},
		{"empty array", `[]`, ",", nil},
		{"comma separated string", `"alfa,beta"`, ",", []string{"alfa", "beta"}},
		{"separated string with spaces", `"alfa, beta"`, ",", []string{"alfa", "beta"}},
		{"single string", `"alfa"`, ",", []string{"alfa"}},
		{"empty string", `""`, ",", nil},
		{"no separator configured", `"alfa,beta"`, "", []string{"alfa,beta"}},
		{"absent claim", ``, ",", nil},
		{"unexpected type", `{"a":1}`, ",", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseRoles(json.RawMessage(tc.raw), tc.separator)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseRoles(%s) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestNormaliseRole covers the user store qualifier that federated directories
// prepend. Observed on the IPZS dev tenants as "ICTIPZS/" on usernames.
func TestNormaliseRole(t *testing.T) {
	cases := []struct {
		in    string
		strip bool
		want  string
	}{
		{"ICTIPZS/mailpit-alfa", true, "mailpit-alfa"},
		{"PRIMARY/mailpit-alfa", true, "mailpit-alfa"},
		{"mailpit-alfa", true, "mailpit-alfa"},
		{"  mailpit-alfa  ", true, "mailpit-alfa"},
		{"ICTIPZS/mailpit-alfa", false, "ICTIPZS/mailpit-alfa"},
	}

	for _, tc := range cases {
		if got := NormaliseRole(tc.in, tc.strip); got != tc.want {
			t.Errorf("NormaliseRole(%q, %v) = %q, want %q", tc.in, tc.strip, got, tc.want)
		}
	}
}

func TestRolesToTags(t *testing.T) {
	cfg := Defaults()

	cases := []struct {
		name  string
		roles []string
		want  []string
	}{
		{
			"prefixed roles become tags",
			[]string{"mailpit-alfa", "mailpit-beta"},
			[]string{"alfa", "beta"},
		},
		{
			"user store qualifier is stripped",
			[]string{"ICTIPZS/mailpit-alfa"},
			[]string{"alfa"},
		},
		{
			"unrelated corporate groups are ignored",
			[]string{"ICTIPZS/Domain Users", "ICTIPZS/VPN-Access", "ICTIPZS/mailpit-alfa"},
			[]string{"alfa"},
		},
		{
			"duplicates collapse",
			[]string{"mailpit-alfa", "ICTIPZS/mailpit-alfa", "MAILPIT-ALFA"},
			[]string{"alfa"},
		},
		{
			"no matching role yields no tags",
			[]string{"ICTIPZS/Domain Users"},
			nil,
		},
		{
			"no roles at all",
			nil,
			nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RolesToTags(tc.roles, cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("RolesToTags(%v) = %v, want %v", tc.roles, got, tc.want)
			}
		})
	}
}

func TestIsAdmin(t *testing.T) {
	cfg := Defaults()

	cases := []struct {
		roles []string
		want  bool
	}{
		{[]string{"mailpit-admin"}, true},
		{[]string{"ICTIPZS/mailpit-admin"}, true},
		{[]string{"MAILPIT-ADMIN"}, true},
		{[]string{"mailpit-alfa"}, false},
		{[]string{"admin"}, false}, // a bare "admin" from the directory is not ours
		{nil, false},
	}

	for _, tc := range cases {
		if got := IsAdmin(tc.roles, cfg); got != tc.want {
			t.Errorf("IsAdmin(%v) = %v, want %v", tc.roles, got, tc.want)
		}
	}
}

func TestScopeForRoles(t *testing.T) {
	cfg := Defaults()

	t.Run("admin is unrestricted", func(t *testing.T) {
		if !ScopeForRoles([]string{"ICTIPZS/mailpit-admin"}, cfg).IsUnrestricted() {
			t.Error("the admin role must grant an unrestricted scope")
		}
	})

	t.Run("project user is limited to their tags", func(t *testing.T) {
		sc := ScopeForRoles([]string{"ICTIPZS/mailpit-alfa"}, cfg)

		if sc.IsUnrestricted() {
			t.Fatal("a project role must not grant everything")
		}
		if !sc.AllowsTags([]string{"alfa"}) {
			t.Error("own project not visible")
		}
		if sc.AllowsTags([]string{"beta"}) {
			t.Error("LEAK: another project is visible")
		}
		if !sc.AllowsTags(nil) {
			t.Error("untagged mail must stay visible")
		}
	})

	// A user whose roles map to nothing must not silently become an admin.
	// This is the failure mode to guard: a misconfigured claim yields no
	// roles, and "no restrictions" would be the worst possible default.
	t.Run("no usable roles is not admin", func(t *testing.T) {
		sc := ScopeForRoles([]string{"ICTIPZS/Domain Users"}, cfg)

		if sc.IsUnrestricted() {
			t.Fatal("a user with no Mailpit role must not see everything")
		}
		if sc.AllowsTags([]string{"alfa"}) {
			t.Error("LEAK: a user with no role can see a project")
		}
		if !sc.AllowsTags(nil) {
			t.Error("untagged mail should remain visible as evidence of the misconfiguration")
		}
	})

	t.Run("missing claim entirely is not admin", func(t *testing.T) {
		if ScopeForRoles(nil, cfg).IsUnrestricted() {
			t.Fatal("an absent roles claim must not grant everything")
		}
	})
}

// TestRolesFromClaims exercises the path taken with a real token, where claims
// arrive already decoded into a map.
func TestRolesFromClaims(t *testing.T) {
	cfg := Defaults()

	cases := []struct {
		name   string
		claims map[string]any
		want   []string
	}{
		{"array claim", map[string]any{"groups": []any{"a", "b"}}, []string{"a", "b"}},
		{"string claim", map[string]any{"groups": "a,b"}, []string{"a", "b"}},
		{"absent claim", map[string]any{"sub": "x"}, nil},
		{"wrong type", map[string]any{"groups": 42}, nil},
		{"mixed array ignores non-strings", map[string]any{"groups": []any{"a", 1}}, []string{"a"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rolesFromClaims(tc.claims, cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rolesFromClaims = %v, want %v", got, tc.want)
			}
		})
	}
}
