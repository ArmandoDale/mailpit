// Package apitoken provides non-interactive access to the HTTP API.
//
// With OIDC enabled every request needs a session cookie, which can only be
// obtained by completing an interactive login in a browser. That leaves no way
// in for CI pipelines and automation, so this package issues long-lived tokens
// presented as `Authorization: Bearer <secret>`.
//
// A token carries the same kind of Scope a project user gets, so automation
// inherits the isolation already enforced everywhere else rather than being a
// privileged side door. Tokens are configured, not minted at runtime: they are
// deployment credentials, and keeping them in the deployment configuration
// makes them versionable and reviewable.
package apitoken

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/axllent/mailpit/internal/scope"
)

// minSecretLength is the shortest token accepted. Tokens are bearer
// credentials with no rate limiting in front of them, so a short one is a
// password waiting to be guessed. 24 characters is roughly 128 bits of entropy
// when generated with `openssl rand -hex 16`, which is what the docs suggest.
const minSecretLength = 24

// unrestrictedTags is the tag list that grants a token the whole mailbox.
// Spelled out rather than implied by an empty list: an unrestricted token
// defeats the point of scoping, so it has to be asked for.
const unrestrictedTags = "*"

// Token is a configured credential and what it may see.
type Token struct {
	// Name identifies the token in logs. It is not a secret and never
	// authenticates anything on its own.
	Name string

	// secret is the value the caller presents.
	secret string

	// Scope is what the token may read and delete.
	Scope scope.Scope
}

// Store holds the configured tokens. A nil Store authenticates nothing, which
// is the state when no tokens are configured.
type Store struct {
	tokens []Token
}

// fieldSplitter separates entries given on one line or one per line.
var fieldSplitter = regexp.MustCompile(`\s+`)

// Parse builds a Store from the configured token definitions.
//
// Each entry is `name:secret:tags`, where tags is a comma-separated list, or
// "*" for an unrestricted token. Entries are separated by whitespace, so both
// a single flag with several tokens and a file with one per line work.
//
// Errors are returned rather than logged and skipped: a token that was meant
// to be scoped but silently dropped turns into a pipeline that mysteriously
// gets 401, and a typo in the tag list must not quietly widen access.
func Parse(s string) (*Store, error) {
	entries := fieldSplitter.Split(strings.TrimSpace(s), -1)

	store := &Store{}
	seenNames := make(map[string]bool)

	for _, entry := range entries {
		if entry == "" {
			continue
		}

		t, err := parseToken(entry)
		if err != nil {
			return nil, err
		}

		if seenNames[t.Name] {
			return nil, fmt.Errorf("[apitoken] duplicate token name %q", t.Name)
		}
		seenNames[t.Name] = true

		store.tokens = append(store.tokens, t)
	}

	if len(store.tokens) == 0 {
		return nil, nil
	}

	return store, nil
}

// parseToken reads one `name:secret:tags` entry.
func parseToken(entry string) (Token, error) {
	// SplitN with 3 so a secret may not contain ":" but a tag list is read
	// whole. Secrets are generated, so forbidding one character costs nothing.
	parts := strings.SplitN(entry, ":", 3)
	if len(parts) != 3 {
		return Token{}, fmt.Errorf("[apitoken] token must be name:secret:tags, got %q", redact(entry))
	}

	name, secret, tagList := parts[0], parts[1], parts[2]

	if name == "" {
		return Token{}, fmt.Errorf("[apitoken] token name is empty in %q", redact(entry))
	}

	if len(secret) < minSecretLength {
		return Token{}, fmt.Errorf("[apitoken] the secret for token %q is shorter than %d characters", name, minSecretLength)
	}

	if tagList == unrestrictedTags {
		return Token{Name: name, secret: secret, Scope: scope.Unrestricted()}, nil
	}

	tags := []string{}
	for _, tag := range strings.Split(tagList, ",") {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			tags = append(tags, tag)
		}
	}

	// A token with no tags would see only untagged messages, which is never
	// what anyone means to configure — it is a typo in the tag list.
	if len(tags) == 0 {
		return Token{}, fmt.Errorf("[apitoken] token %q has no tags: use %q to grant the whole mailbox", name, unrestrictedTags)
	}

	return Token{Name: name, secret: secret, Scope: scope.ForTags(tags)}, nil
}

// redact hides everything after the token name, so a malformed entry can be
// reported without printing the secret into the logs.
func redact(entry string) string {
	if name, _, found := strings.Cut(entry, ":"); found {
		return name + ":..."
	}

	return "..."
}

// Unrestricted reports the names of tokens that see the whole mailbox, so
// startup can say so out loud.
func (s *Store) Unrestricted() []string {
	if s == nil {
		return nil
	}

	names := []string{}
	for _, t := range s.tokens {
		if t.Scope.IsUnrestricted() {
			names = append(names, t.Name)
		}
	}

	return names
}

// Len returns how many tokens are configured.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}

	return len(s.tokens)
}

// Lookup returns the token matching secret.
//
// Every token is compared, and with a constant-time comparison: returning
// early on the first match would leak, through response timing, how much of a
// guessed secret is correct.
func (s *Store) Lookup(secret string) (Token, bool) {
	if s == nil || secret == "" {
		return Token{}, false
	}

	var found Token
	matched := false

	for _, t := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(t.secret), []byte(secret)) == 1 {
			found = t
			matched = true
		}
	}

	return found, matched
}

// bearerSecret extracts the credential from an Authorization header.
func bearerSecret(r *http.Request) string {
	header := r.Header.Get("Authorization")

	// The scheme is case-insensitive per RFC 7235, and clients disagree about
	// how they spell it.
	secret, found := strings.CutPrefix(header, "Bearer ")
	if !found {
		if secret, found = strings.CutPrefix(header, "bearer "); !found {
			return ""
		}
	}

	return strings.TrimSpace(secret)
}

// FromRequest authenticates a request carrying a bearer token. It reports the
// token's scope and whether one was presented and recognised.
func (s *Store) FromRequest(r *http.Request) (Token, bool) {
	if s == nil {
		return Token{}, false
	}

	return s.Lookup(bearerSecret(r))
}

// Presented reports whether the request carries a bearer token at all,
// regardless of whether it is valid.
//
// The caller needs the distinction: a request with a bad token must be
// rejected outright rather than falling through to the session check, which
// would answer a script with a redirect to an interactive login page.
func Presented(r *http.Request) bool {
	return bearerSecret(r) != ""
}
