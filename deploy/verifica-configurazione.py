#!/usr/bin/env python3
"""Verifica che la configurazione di riferimento produca i comportamenti dichiarati nel SID.

Non controlla che il file contenga certe righe — quello sarebbe verificare noi
stessi. Avvia un'istanza con `deploy/mailpit.env` e osserva il servizio in
esecuzione, un'affermazione del SID alla volta.

    python deploy/verifica-configurazione.py [--mailpit ./mailpit]

Esce con codice diverso da zero se una sola verifica fallisce.
"""

import argparse
import json
import os
import shutil
import smtplib
import subprocess
import sys
import tempfile
import time
import urllib.request
from email.message import EmailMessage

HTTP_PORT = 18225
SMTP_PORT = 11225
BASE = f"http://127.0.0.1:{HTTP_PORT}"
UTENTE, PASSWORD = "verifica", "verifica"

esiti = []


def verifica(riferimento, affermazione, atteso, ottenuto):
    ok = atteso == ottenuto
    esiti.append((riferimento, affermazione, ok))
    print(f"  {'OK  ' if ok else 'FALLITO'}  {riferimento:<12} {affermazione}")
    if not ok:
        print(f"            atteso:   {atteso}")
        print(f"            ottenuto: {ottenuto}")
    return ok


def leggi_env(percorso):
    """Le variabili valorizzate del file di configurazione."""
    env = {}
    with open(percorso, encoding="utf-8") as f:
        for riga in f:
            riga = riga.strip()
            if not riga or riga.startswith("#") or "=" not in riga:
                continue
            chiave, valore = riga.split("=", 1)
            if valore.strip():
                env[chiave.strip()] = valore.strip()
    return env


def http(percorso, con_auth=True):
    req = urllib.request.Request(BASE + percorso)
    if con_auth:
        import base64
        token = base64.b64encode(f"{UTENTE}:{PASSWORD}".encode()).decode()
        req.add_header("Authorization", "Basic " + token)
    return urllib.request.urlopen(req, timeout=20)


def pruning_per_numerosita(binario, tmp):
    """Quanti messaggi restano dopo averne inviati sei con soglia impostata a tre.

    La configurazione di riferimento dichiara 100.000: verificarla in esecuzione
    richiederebbe di popolare l'archivio. Si verifica quindi che il meccanismo
    governato da quella voce funzioni, su un'istanza dedicata con soglia ridotta.
    """
    porta_http, porta_smtp = 18226, 11226
    db = os.path.join(tmp, "pruning.db")
    p = subprocess.Popen(
        [binario, "--database", db,
         "--listen", f"127.0.0.1:{porta_http}",
         "--smtp", f"127.0.0.1:{porta_smtp}",
         "--max", "3"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    try:
        for _ in range(60):
            try:
                urllib.request.urlopen(
                    f"http://127.0.0.1:{porta_http}/readyz", timeout=1).read()
                break
            except Exception:
                time.sleep(0.25)

        for n in range(6):
            m = EmailMessage()
            m["From"] = "prova@dev.test.local"
            m["To"] = "utente@test.local"
            m["Subject"] = f"Messaggio {n}"
            m.set_content("corpo")
            with smtplib.SMTP("127.0.0.1", porta_smtp, timeout=10) as sm:
                sm.send_message(m)
        # l'eliminazione non e' immediata: il prodotto la esegue su un ciclo di
        # 60 secondi, quindi la numerosita' puo' eccedere la soglia per un
        # intervallo breve. E' il rientro graduale descritto al par. 4.2.5.2.
        scadenza = time.time() + 100
        totale = None
        while time.time() < scadenza:
            dati = json.loads(urllib.request.urlopen(
                f"http://127.0.0.1:{porta_http}/api/v1/messages", timeout=20).read())
            totale = dati.get("total")
            if totale == 3:
                break
            time.sleep(5)
        return totale
    finally:
        p.terminate()
        try:
            p.wait(timeout=10)
        except subprocess.TimeoutExpired:
            p.kill()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mailpit", default=None)
    args = ap.parse_args()
    binario = args.mailpit or ("./mailpit.exe" if os.name == "nt" else "./mailpit")
    if not os.path.exists(binario):
        raise SystemExit(f"binario non trovato: {binario}")

    radice = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    env_file = os.path.join(radice, "deploy", "mailpit.env")
    configurata = leggi_env(env_file)

    tmp = tempfile.mkdtemp(prefix="mp-verifica-")
    try:
        # i percorsi del file di riferimento sono sostituiti con equivalenti
        # locali; tutto il resto e' usato tale e quale
        auth = os.path.join(tmp, "ui-auth")
        with open(auth, "w", encoding="utf-8") as f:
            # htpasswd bcrypt di "verifica"
            f.write(f"{UTENTE}:$2a$10$VKdrjWmj6OYSlJLUbZN6gOISOMlqsR91HEiLL4Sg3AlOO95aZn4oq\n")
        tagfile = os.path.join(tmp, "tag-filters.yaml")
        with open(tagfile, "w", encoding="utf-8") as f:
            f.write('filters:\n  - match: "from:progetto-x@dev.test.local"\n    tags: "Progetto-X"\n')

        env = dict(os.environ)
        env.update(configurata)
        env["MP_SMTP_BIND_ADDR"] = f"127.0.0.1:{SMTP_PORT}"
        env["MP_UI_BIND_ADDR"] = f"127.0.0.1:{HTTP_PORT}"
        env["MP_UI_AUTH_FILE"] = auth
        env["MP_TAGS_CONFIG"] = tagfile
        env["MP_DATABASE"] = os.path.join(tmp, "mailpit.db")

        log = open(os.path.join(tmp, "mailpit.log"), "w+", encoding="utf-8")
        p = subprocess.Popen([binario], env=env, stdout=log, stderr=log)

        for _ in range(60):
            try:
                urllib.request.urlopen(BASE + "/readyz", timeout=1).read()
                break
            except Exception:
                time.sleep(0.25)
        else:
            p.kill()
            log.seek(0)
            raise SystemExit("il servizio non risponde:\n" + log.read())

        try:
            print("\nVerifica della configurazione di riferimento contro le affermazioni del SID\n")

            # RS-05: interfaccia e API protette da autenticazione
            try:
                http("/api/v1/info", con_auth=False)
                anonimo = "consentito"
            except urllib.error.HTTPError as e:
                anonimo = f"respinto {e.code}"
            verifica("RS-05", "l'accesso senza credenziali e' respinto",
                     "respinto 401", anonimo)

            info = json.loads(http("/api/v1/info").read())
            verifica("RS-05", "l'accesso con credenziali valide e' consentito",
                     True, "Version" in info)

            # RS-01: nessun percorso di uscita, l'azione di rilascio non e' offerta
            webui = json.loads(http("/api/v1/webui").read())
            verifica("RS-01", "l'interfaccia non espone l'azione di rilascio",
                     False, bool(webui.get("MessageRelay", {}).get("Enabled")))

            # RS-12: il recupero di risorse remote e' inibito
            csp = http("/").headers.get("Content-Security-Policy", "")
            stile = [d for d in csp.split(";") if d.strip().startswith("style-src")]
            verifica("RS-12", "la politica di sicurezza vieta stili di origine remota",
                     True, bool(stile) and "'self'" in stile[0] and "http" not in stile[0])

            # RF-02 / RS-01: il messaggio e' accettato e non consegnato
            m = EmailMessage()
            m["From"] = "progetto-x@dev.test.local"
            m["To"] = "destinatario@esterno.example.com"
            m["Subject"] = "Verifica di configurazione"
            m.set_content("corpo")
            with smtplib.SMTP("127.0.0.1", SMTP_PORT, timeout=10) as sm:
                sm.send_message(m)
            time.sleep(1)

            msgs = json.loads(http("/api/v1/messages").read())
            verifica("RF-02", "il messaggio verso un dominio esterno e' accettato e trattenuto",
                     1, msgs.get("total"))

            # 4.2.2.2: le regole del file sono attive e visibili
            regole = json.loads(http("/api/v1/tag-filters").read())
            verifica("4.2.2.2", "le regole del file sono visibili tra quelle a runtime",
                     ["from:progetto-x@dev.test.local"], [r["match"] for r in regole])
            verifica("4.2.2.2", "il messaggio ricevuto e' stato etichettato dalla regola",
                     ["Progetto-X"], msgs["messages"][0]["Tags"])

            # RF-13: entrambe le politiche di eliminazione sono dichiarate.
            # Sono le sole due voci che questo strumento non puo' osservare in
            # esecuzione: la soglia per numerosita' agirebbe a 100.000 messaggi e
            # quella per anzianita' dopo sette giorni. Il meccanismo e' verificato
            # separatamente qui sotto, con soglie ridotte, su un'istanza dedicata.
            verifica("RF-13", "la soglia per numerosita' e' dichiarata al valore raccomandato",
                     "100000", configurata.get("MP_MAX_MESSAGES"))
            verifica("RF-13", "la soglia per anzianita' e' dichiarata",
                     "7d", configurata.get("MP_MAX_AGE"))

            # RS-14 / RS-13: POP3 e webhook non attivi
            try:
                import socket
                s = socket.create_connection(("127.0.0.1", 1110), timeout=2)
                s.close()
                pop3 = "in ascolto"
            except Exception:
                pop3 = "non in ascolto"
            verifica("RS-14", "il servizio POP3 non e' in ascolto", "non in ascolto", pop3)

            # RS-11: il log non contiene l'oggetto del messaggio ricevuto
            log.flush()
            log.seek(0)
            contenuto = log.read()
            verifica("RS-11", "il log non riporta l'oggetto dei messaggi",
                     False, "Verifica di configurazione" in contenuto)
            verifica("4.2.2.2", "il log documenta la semina delle regole dal file",
                     True, "seeded" in contenuto)

            verifica("RF-13", "il meccanismo di eliminazione per numerosita' e' operante",
                     3, pruning_per_numerosita(binario, tmp))
        finally:
            p.terminate()
            try:
                p.wait(timeout=10)
            except subprocess.TimeoutExpired:
                p.kill()
            log.close()
    finally:
        shutil.rmtree(tmp, ignore_errors=True)

    ok = sum(1 for *_, e in esiti if e)
    print(f"\n{'='*70}\nEsito: {ok}/{len(esiti)} verifiche superate")
    if ok != len(esiti):
        print("Fallite: " + ", ".join(f"{r} ({a})" for r, a, e in esiti if not e))
    sys.exit(0 if ok == len(esiti) else 1)


if __name__ == "__main__":
    main()
