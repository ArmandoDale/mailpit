// Command fakeop is a throwaway OpenID Provider for trying the Mailpit OIDC
// integration locally, without Keycloak or network access.
//
// It is a real OP as far as the protocol goes: it publishes discovery and a
// JWKS, and signs ID tokens with an RSA key generated at startup. What it is
// not is an identity system — it has no passwords and lets you pick a user
// from a list. Never run it anywhere but a developer machine.
//
//	go run ./dev/fakeop
//	go run . --oidc-issuer http://localhost:9000 \
//	         --oidc-client-id mailpit --oidc-client-secret dev \
//	         --oidc-redirect-url http://localhost:8025/auth/callback
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// user is a test identity. The roles carry the "ICTIPZS/" qualifier on
// purpose: that is what a directory-federated WSO2 tenant returns, and
// forgetting to strip it is the failure we want to be able to see.
type user struct {
	Name        string
	Description string
	Roles       []string

	// RolesAsString sends the claim as one separated string rather than an
	// array, the way WSO2 may serialise a multi-valued claim.
	RolesAsString bool
}

var users = []user{
	{
		Name:        "alfa",
		Description: "Progetto Alfa — vede progetto-alfa e le mail senza tag",
		Roles:       []string{"ICTIPZS/mailpit-progetto-alfa", "ICTIPZS/Domain Users"},
	},
	{
		Name:        "beta",
		Description: "Progetto Beta — vede progetto-beta e le mail senza tag",
		Roles:       []string{"ICTIPZS/mailpit-progetto-beta", "ICTIPZS/Domain Users"},
	},
	{
		Name:        "both",
		Description: "Due progetti — claim inviato come stringa separata da virgole",
		Roles: []string{
			"ICTIPZS/mailpit-progetto-alfa",
			"ICTIPZS/mailpit-progetto-beta",
		},
		RolesAsString: true,
	},
	{
		Name:        "admin1",
		Description: "Amministratore — vede tutto",
		Roles:       []string{"ICTIPZS/mailpit-admin"},
	},
	{
		Name:        "nessuno",
		Description: "Nessun ruolo Mailpit — deve vedere SOLO le mail senza tag",
		Roles:       []string{"ICTIPZS/Domain Users"},
	},
}

var (
	listen   = flag.String("listen", "localhost:9000", "bind address")
	clientID = flag.String("client-id", "mailpit", "expected client ID")

	key *rsa.PrivateKey

	// codes maps an issued authorization code to the chosen user.
	codes   = map[string]user{}
	codesMu sync.Mutex
)

func main() {
	flag.Parse()

	var err error
	if key, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/.well-known/openid-configuration", discovery)
	http.HandleFunc("/jwks", jwks)
	http.HandleFunc("/authorize", authorize)
	http.HandleFunc("/token", token)
	http.HandleFunc("/logout", logout)

	log.Printf("fake OIDC provider on http://%s", *listen)
	log.Printf("issuer: http://%s", *listen)
	log.Fatal(http.ListenAndServe(*listen, nil))
}

func issuer() string {
	return "http://" + *listen
}

func discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                issuer(),
		"authorization_endpoint":                issuer() + "/authorize",
		"token_endpoint":                        issuer() + "/token",
		"jwks_uri":                              issuer() + "/jwks",
		"end_session_endpoint":                  issuer() + "/logout",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      []string{"openid", "profile", "email", "groups"},
		"claims_supported":                      []string{"sub", "preferred_username", "email", "groups"},
	})
}

func jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{{
			Key:       key.Public(),
			KeyID:     "fakeop",
			Algorithm: "RS256",
			Use:       "sig",
		}},
	})
}

var picker = template.Must(template.New("picker").Parse(`<!doctype html>
<html lang="it"><head><meta charset="utf-8"><title>Accedi (provider finto)</title>
<style>
 body{font:16px system-ui,sans-serif;max-width:44rem;margin:3rem auto;padding:0 1rem;color:#222}
 h1{font-size:1.4rem;margin-bottom:.2rem}
 p.sub{color:#666;margin-top:0}
 a.user{display:block;border:1px solid #ddd;border-radius:8px;padding:.9rem 1.1rem;
        margin:.6rem 0;text-decoration:none;color:inherit}
 a.user:hover{border-color:#888;background:#fafafa}
 .n{font-weight:600}
 .d{color:#666;font-size:.9rem}
 code{background:#f3f3f3;padding:.1rem .3rem;border-radius:3px;font-size:.85rem}
 .warn{background:#fff6e5;border:1px solid #f0d090;border-radius:8px;padding:.7rem 1rem;font-size:.9rem}
</style></head><body>
<h1>Provider di identità finto</h1>
<p class="sub">Scegli con quale utenza entrare in Mailpit.</p>
<div class="warn">Nessuna password: serve solo a vedere l'isolamento per progetto.
I ruoli sono prefissati <code>ICTIPZS/</code> come li restituisce un tenant WSO2 federato.</div>
{{range .Users}}
<a class="user" href="{{$.Action}}&user={{.Name}}">
  <span class="n">{{.Name}}</span><br>
  <span class="d">{{.Description}}</span>
</a>
{{end}}
</body></html>`))

func authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	if q.Get("client_id") != *clientID {
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return
	}

	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" {
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	}

	name := q.Get("user")
	if name == "" {
		// No user chosen yet: show the picker, preserving the request.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		_ = picker.Execute(w, map[string]any{
			"Users":  users,
			"Action": "/authorize?" + r.URL.RawQuery,
		})

		return
	}

	u, ok := findUser(name)
	if !ok {
		http.Error(w, "unknown user", http.StatusBadRequest)
		return
	}

	code := fmt.Sprintf("code-%d", time.Now().UnixNano())

	codesMu.Lock()
	codes[code] = u
	codesMu.Unlock()

	sep := "?"
	if strings.Contains(redirectURI, "?") {
		sep = "&"
	}

	http.Redirect(w, r, redirectURI+sep+
		"code="+url.QueryEscape(code)+
		"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
}

func token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	code := r.Form.Get("code")

	codesMu.Lock()
	u, ok := codes[code]
	delete(codes, code) // authorization codes are single use
	codesMu.Unlock()

	if !ok {
		writeJSON(w, map[string]any{"error": "invalid_grant"})
		return
	}

	claims := map[string]any{
		"iss":                issuer(),
		"aud":                *clientID,
		"sub":                "sub-" + u.Name,
		"preferred_username": u.Name,
		"email":              u.Name + "@example.it",
		"iat":                time.Now().Unix(),
		"exp":                time.Now().Add(time.Hour).Unix(),
	}

	if u.RolesAsString {
		claims["groups"] = strings.Join(u.Roles, ",")
	} else {
		claims["groups"] = u.Roles
	}

	writeJSON(w, map[string]any{
		"access_token": "opaque-" + u.Name,
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     sign(claims),
	})
}

// logout exists so the end_session_endpoint in discovery resolves; it simply
// bounces back to wherever it was told.
func logout(w http.ResponseWriter, r *http.Request) {
	to := r.URL.Query().Get("post_logout_redirect_uri")
	if to == "" {
		to = "/"
	}

	http.Redirect(w, r, to, http.StatusFound)
}

func findUser(name string) (user, bool) {
	for _, u := range users {
		if u.Name == name {
			return u, true
		}
	}

	return user{}, false
}

func sign(claims map[string]any) string {
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "fakeop"),
	)
	if err != nil {
		log.Fatal(err)
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		log.Fatal(err)
	}

	obj, err := signer.Sign(payload)
	if err != nil {
		log.Fatal(err)
	}

	s, err := obj.CompactSerialize()
	if err != nil {
		log.Fatal(err)
	}

	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
