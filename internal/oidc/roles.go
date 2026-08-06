package oidc

import (
	"encoding/json"
	"strings"

	"github.com/axllent/mailpit/internal/scope"
)

// ParseRoles reads the roles claim, which providers serialise in more than one
// shape. WSO2 in particular may return a JSON array or a single string joined
// by the configured multi-attribute separator, depending on how the claim was
// mapped — so both are accepted rather than assumed.
//
// A claim that is absent yields no roles. Note that this is the normal case
// for a user with no roles: WSO2 populates claims on a best-effort basis and
// omits the key entirely rather than sending an empty value.
func ParseRoles(raw json.RawMessage, separator string) []string {
	if len(raw) == 0 {
		return nil
	}

	// Array form: ["a", "b"]
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return clean(list)
	}

	// String form: "a,b" (or a single value)
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if separator == "" {
			return clean([]string{single})
		}

		return clean(strings.Split(single, separator))
	}

	return nil
}

func clean(in []string) []string {
	out := make([]string, 0, len(in))

	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// NormaliseRole strips the user store domain qualifier a federated directory
// adds, turning "ICTIPZS/mailpit-progetto-x" into "mailpit-progetto-x".
//
// The qualifier is only ever a prefix, so the last separator wins in the
// unlikely event a role name itself contains one.
func NormaliseRole(role string, strip bool) string {
	role = strings.TrimSpace(role)

	if !strip {
		return role
	}

	if i := strings.LastIndex(role, "/"); i >= 0 {
		role = role[i+1:]
	}

	return role
}

// RolesToTags maps provider roles onto Mailpit tags.
//
// Only roles carrying the configured prefix are considered; everything else
// the directory happens to return is ignored, so an unrelated corporate group
// never becomes a tag.
func RolesToTags(roles []string, cfg Config) []string {
	tags := make([]string, 0, len(roles))
	seen := map[string]bool{}

	for _, r := range roles {
		r = NormaliseRole(r, cfg.StripUserStorePrefix)

		if cfg.RolePrefix != "" {
			if !strings.HasPrefix(strings.ToLower(r), strings.ToLower(cfg.RolePrefix)) {
				continue
			}
			r = r[len(cfg.RolePrefix):]
		}

		if r == "" || seen[strings.ToLower(r)] {
			continue
		}

		seen[strings.ToLower(r)] = true
		tags = append(tags, r)
	}

	if len(tags) == 0 {
		return nil
	}

	return tags
}

// IsAdmin reports whether any role grants unrestricted access.
func IsAdmin(roles []string, cfg Config) bool {
	if cfg.AdminRole == "" {
		return false
	}

	want := strings.ToLower(cfg.RolePrefix + cfg.AdminRole)

	for _, r := range roles {
		if strings.EqualFold(NormaliseRole(r, cfg.StripUserStorePrefix), want) {
			return true
		}
	}

	return false
}

// ScopeForRoles turns the roles from a token into the visibility a session
// gets. An admin sees everything; anyone else sees their projects' tags plus
// untagged mail.
//
// A user whose roles map to no tag still gets a valid, empty scope: they see
// untagged mail only. That is deliberate — it is visible evidence of a
// misconfigured role, rather than a login that silently fails.
func ScopeForRoles(roles []string, cfg Config) scope.Scope {
	if IsAdmin(roles, cfg) {
		return scope.Unrestricted()
	}

	return scope.ForTags(RolesToTags(roles, cfg))
}
