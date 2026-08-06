#!/usr/bin/env python3
"""Verifica l'isolamento per progetto entrando davvero con ogni account.

Esegue il flusso OIDC completo contro Keycloak (nessuna scorciatoia: stessa
strada del browser), poi chiama l'API con il cookie di sessione e confronta
quello che l'account vede con quello che dovrebbe vedere.

    python dev/testdata/check-isolation.py

Prerequisiti: Keycloak sul realm 'mailpit', Mailpit avviato con OIDC, e il
corpus gia' inviato con send-samples.py.
"""

import http.cookiejar
import json
import re
import sys
import urllib.parse
import urllib.request

MAILPIT = "http://localhost:8025"

# utente -> (tag visibili oltre alle mail senza tag, oppure None = tutto)
ATTESO = {
    "alfa": {"progetto-alfa"},
    "beta": {"progetto-beta"},
    "both": {"progetto-alfa", "progetto-beta"},
    "admin1": None,  # amministratore: nessuno scope
    "nessuno": set(),
}


class _Permissiva(http.cookiejar.DefaultCookiePolicy):
    """http.cookiejar non consegna i cookie a host senza punto come 'localhost',
    e Keycloak risponderebbe 'Cookie not found'. Qui i domini sono due host
    locali noti, quindi la politica di dominio non serve a niente."""

    def set_ok(self, cookie, request):
        return True

    def return_ok(self, cookie, request):
        return True

    def domain_return_ok(self, domain, request):
        return True

    def path_return_ok(self, path, request):
        return True


def opener():
    """Un client con cookie propri e senza proxy: le chiamate sono su localhost."""
    jar = http.cookiejar.CookieJar(_Permissiva())
    return urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(jar),
        urllib.request.ProxyHandler({}),
    )


def login(user, password="password"):
    op = opener()

    # 1. Mailpit rimanda a Keycloak (authorize con PKCE)
    with op.open(f"{MAILPIT}/auth/login", timeout=15) as r:
        page = r.read().decode("utf-8", "replace")

    # 2. la pagina di login di Keycloak espone il form di autenticazione
    m = re.search(r'id="kc-form-login"[^>]*action="([^"]+)"', page) or \
        re.search(r'<form[^>]+action="([^"]+)"[^>]*id="kc-form-login"', page)
    if not m:
        raise RuntimeError(f"{user}: form di login non trovato nella pagina Keycloak")
    action = m.group(1).replace("&amp;", "&")

    # 3. credenziali -> Keycloak rimanda al callback di Mailpit, che apre la sessione
    data = urllib.parse.urlencode({"username": user, "password": password}).encode()
    req = urllib.request.Request(action, data=data, method="POST")
    req.add_header("Content-Type", "application/x-www-form-urlencoded")
    with op.open(req, timeout=15) as r:
        final = r.geturl()
        if "/auth/callback" not in final and not final.startswith(MAILPIT):
            body = r.read().decode("utf-8", "replace")
            hint = "credenziali rifiutate" if "Invalid username" in body or \
                   "Credenziali non valide" in body else f"fermo su {final}"
            raise RuntimeError(f"{user}: login non completato ({hint})")
    return op


def messages(op):
    with op.open(f"{MAILPIT}/api/v1/messages?limit=500", timeout=15) as r:
        return json.loads(r.read().decode())


def main():
    esito = 0
    print(f"{'account':<9} {'visti':>6}  {'atteso':>6}  tag osservati")
    print("-" * 72)

    for user, consentiti in ATTESO.items():
        try:
            op = login(user)
            data = messages(op)
        except Exception as e:  # noqa: BLE001 - vogliamo proseguire con gli altri
            print(f"{user:<9} {'ERRORE':>6}  {'':>6}  {e}")
            esito = 1
            continue

        msgs = data.get("messages", [])
        visti = data.get("messages_count", data.get("total", len(msgs)))

        senza_tag = 0
        fuori = []
        osservati = set()
        for m in msgs:
            tags = set(m.get("Tags") or [])
            osservati.update(tags)
            if not tags:
                senza_tag += 1
                continue
            # Un messaggio e' in scope se ALMENO uno dei suoi tag e' consentito:
            # le comunicazioni trasversali portano i tag di piu' progetti.
            if consentiti is not None and not (tags & consentiti):
                fuori.append((m.get("Subject", "?"), sorted(tags)))

        ok = not fuori
        atteso = "tutto" if consentiti is None else "-"
        nota = ", ".join(sorted(osservati)) or "(solo senza tag)"
        print(f"{user:<9} {visti:>6}  {atteso:>6}  {nota}")
        print(f"{'':<9} {'':>6}  {'':>6}  di cui senza tag: {senza_tag}")
        for subj, tags in fuori[:5]:
            print(f"{'':<9} {'':>6}  {'':>6}  FUORI SCOPE [{','.join(tags)}] {subj[:44]}")

        if not ok:
            esito = 1

    print("-" * 72)
    print("Ogni account deve vedere le mail senza tag; nessun account di progetto")
    print("deve vedere 'progetto-gamma' (solo admin1).")
    return esito


if __name__ == "__main__":
    sys.exit(main())
