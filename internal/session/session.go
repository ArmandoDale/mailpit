// Package session holds authenticated browser sessions.
//
// A session is created after a successful login and referenced by an opaque
// random ID stored in an httpOnly cookie. Nothing about the user — and in
// particular no token — is exposed to JavaScript, and because the cookie
// travels on the WebSocket handshake like any other HTTP request, live
// notifications authenticate without a bearer header.
//
// Sessions live in memory: they are lost on restart, which for a test mail
// server means users log in again after a deploy.
package session

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"github.com/axllent/mailpit/internal/scope"
)

// CookieName is the name of the session cookie.
const CookieName = "mp_session"

// idBytes is the entropy of a session ID. The ID is the bearer of the session,
// so it must not be guessable.
const idBytes = 32

// Session is an authenticated user's server-side state.
type Session struct {
	// ID is the opaque value stored in the cookie.
	ID string

	// Subject is the `sub` claim of the token that created the session.
	Subject string

	// Username is what the UI displays.
	Username string

	// Roles are the normalised role names the identity provider returned,
	// kept for display and troubleshooting.
	Roles []string

	// Scope is what these roles resolve to in terms of visible tags.
	Scope scope.Scope

	// IDToken is the raw ID token from the login, kept solely to be sent back
	// as id_token_hint when ending the session at the provider. It stays
	// server-side like everything else here: the browser only ever holds the
	// opaque session ID.
	IDToken string

	// Expires is an absolute deadline. There is deliberately no sliding
	// renewal: a session ends when the login behind it gets too old.
	Expires time.Time
}

// Expired reports whether the session is no longer valid.
func (s *Session) Expired() bool {
	return time.Now().After(s.Expires)
}

// Store keeps active sessions. The zero value is not usable; use NewStore.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Session

	// ttl is how long a new session remains valid.
	ttl time.Duration

	// secure marks cookies as Secure. Disabled when serving plain HTTP so
	// that local development still works.
	secure bool
}

// NewStore returns a session store issuing sessions valid for ttl.
func NewStore(ttl time.Duration, secure bool) *Store {
	return &Store{
		sessions: make(map[string]*Session),
		ttl:      ttl,
		secure:   secure,
	}
}

// newID returns a cryptographically random session identifier.
func newID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create stores a new session and returns it. idToken may be empty when the
// provider issued none worth keeping; it is only used as a logout hint.
func (st *Store) Create(subject, username, idToken string, roles []string, sc scope.Scope) (*Session, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}

	s := &Session{
		ID:       id,
		Subject:  subject,
		Username: username,
		Roles:    roles,
		Scope:    sc,
		IDToken:  idToken,
		Expires:  time.Now().Add(st.ttl),
	}

	st.mu.Lock()
	st.sessions[id] = s
	st.mu.Unlock()

	return s, nil
}

// Get returns the session for an ID, or nil when it is unknown or expired.
// An expired session is dropped on the way out.
func (st *Store) Get(id string) *Session {
	st.mu.RLock()
	s, ok := st.sessions[id]
	st.mu.RUnlock()

	if !ok {
		return nil
	}

	if s.Expired() {
		st.Delete(id)
		return nil
	}

	return s
}

// Delete removes a session, if present.
func (st *Store) Delete(id string) {
	st.mu.Lock()
	delete(st.sessions, id)
	st.mu.Unlock()
}

// Prune drops expired sessions. Get already discards them lazily; this bounds
// memory for sessions nobody comes back for.
func (st *Store) Prune() int {
	now := time.Now()
	removed := 0

	st.mu.Lock()
	for id, s := range st.sessions {
		if now.After(s.Expires) {
			delete(st.sessions, id)
			removed++
		}
	}
	st.mu.Unlock()

	return removed
}

// Count returns the number of stored sessions, expired ones included.
func (st *Store) Count() int {
	st.mu.RLock()
	defer st.mu.RUnlock()

	return len(st.sessions)
}

// SetCookie writes the session cookie for s.
//
// SameSite=Lax is what stands between a cookie session and CSRF: it stops the
// cookie from being attached to cross-site POST/PUT/DELETE requests. Mailpit's
// destructive routes are all non-GET, so Lax covers them — a GET that mutates
// state would need separate protection.
func (st *Store) SetCookie(w http.ResponseWriter, s *Session, path string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    s.ID,
		Path:     path,
		Expires:  s.Expires,
		HttpOnly: true,
		Secure:   st.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie expires the session cookie in the browser.
func (st *Store) ClearCookie(w http.ResponseWriter, path string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     path,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   st.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// FromRequest returns the session referenced by r's cookie, or nil.
func (st *Store) FromRequest(r *http.Request) *Session {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil
	}

	return st.Get(c.Value)
}
