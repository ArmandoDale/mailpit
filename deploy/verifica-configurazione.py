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


def rilascio_attivabile_da_variabili(binario, tmp):
    """Se il rilascio si attivi con il solo MP_SMTP_RELAY_HOST, senza file YAML.

    Non e' una verifica della configurazione di riferimento ma della ragione per
    cui essa dichiara vuote anche le variabili del rilascio e dell'inoltro:
    config/validators.go:87 accetta la configurazione appena l'host e' valorizzato
    e imposta ReleaseEnabled. Vincolare il solo MP_SMTP_RELAY_CONFIG lascerebbe
    aperta questa seconda via. Restituisce True se l'azione di rilascio compare.
    """
    porta_http, porta_smtp = 18227, 11227
    env = dict(os.environ)
    env["MP_SMTP_RELAY_HOST"] = "smtp.esempio.invalid"
    env["MP_SMTP_RELAY_PORT"] = "25"
    p = subprocess.Popen(
        [binario, "--database", os.path.join(tmp, "relay-env.db"),
         "--listen", f"127.0.0.1:{porta_http}",
         "--smtp", f"127.0.0.1:{porta_smtp}"],
        env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    try:
        for _ in range(60):
            try:
                urllib.request.urlopen(
                    f"http://127.0.0.1:{porta_http}/readyz", timeout=1).read()
                break
            except Exception:
                time.sleep(0.25)
        dati = json.loads(urllib.request.urlopen(
            f"http://127.0.0.1:{porta_http}/api/v1/webui", timeout=20).read())
        return bool(dati.get("MessageRelay", {}).get("Enabled"))
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

            # RS-04: interfaccia e API protette da autenticazione
            try:
                http("/api/v1/info", con_auth=False)
                anonimo = "consentito"
            except urllib.error.HTTPError as e:
                anonimo = f"respinto {e.code}"
            verifica("RS-04", "l'accesso senza credenziali e' respinto",
                     "respinto 401", anonimo)

            info = json.loads(http("/api/v1/info").read())
            verifica("RS-04", "l'accesso con credenziali valide e' consentito",
                     True, "Version" in info)

            # RS-01: nessun percorso di uscita, l'azione di rilascio non e' offerta
            webui = json.loads(http("/api/v1/webui").read())
            verifica("RS-01", "l'interfaccia non espone l'azione di rilascio",
                     False, bool(webui.get("MessageRelay", {}).get("Enabled")))

            # RS-01: l'inoltro automatico dell'intero flusso non e' attivo.
            # E' un meccanismo distinto dal rilascio e non compare nella
            # configurazione esposta dall'interfaccia: si osserva sul log, dove
            # l'attivazione scrive una riga esplicita (config/validators.go:254).
            log.flush()
            log.seek(0)
            avvio = log.read()
            verifica("RS-01", "l'inoltro automatico non e' attivo",
                     False, "[forward] enabling message forwarding" in avvio)

            # RS-04: l'API di invio non e' raggiungibile senza credenziali.
            # Ha un proprio percorso di autenticazione (server/server.go:243) e
            # una variabile che lo disattiva: va verificata a parte.
            try:
                req = urllib.request.Request(
                    BASE + "/api/v1/send", method="POST",
                    data=b'{"From":{"Email":"a@dev.test.local"},"To":[{"Email":"b@esterno.example.com"}]}',
                    headers={"Content-Type": "application/json"})
                urllib.request.urlopen(req, timeout=5).read()
                invio_anonimo = "consentito"
            except urllib.error.HTTPError as e:
                invio_anonimo = f"respinto {e.code}"
            verifica("RS-04", "l'API di invio senza credenziali e' respinta",
                     "respinto 401", invio_anonimo)

            # RS-11: il recupero di risorse remote all'apertura di un messaggio
            # avviene dal browser di chi lo apre, non dalla VM, ed e' quindi
            # governato dalla Content Security Policy e non dalle regole di rete.
            csp = http("/").headers.get("Content-Security-Policy", "")
            direttiva = lambda nome: next(
                (d.strip() for d in csp.split(";") if d.strip().startswith(nome)), "")
            stile, font = direttiva("style-src"), direttiva("font-src")
            verifica("RS-11", "la politica vieta stili di origine remota",
                     True, bool(stile) and "'self'" in stile and "http" not in stile)
            verifica("RS-11", "la politica vieta caratteri di origine remota",
                     True, bool(font) and "'self'" in font and "http" not in font)

            # Limite dichiarato nel SID, verificato qui perche' una limitazione
            # provata e' un comportamento accertato: il prodotto non consente di
            # restringere img-src, quindi le immagini remote di un messaggio
            # vengono caricate dalla postazione che lo apre.
            verifica("RS-11", "le immagini remote restano consentite (limite dichiarato)",
                     True, "*" in direttiva("img-src"))

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

            # RS-13 / RS-12: POP3 e webhook non attivi
            try:
                import socket
                s = socket.create_connection(("127.0.0.1", 1110), timeout=2)
                s.close()
                pop3 = "in ascolto"
            except Exception:
                pop3 = "non in ascolto"
            verifica("RS-13", "il servizio POP3 non e' in ascolto", "non in ascolto", pop3)

            # RS-10: il log non contiene l'oggetto del messaggio ricevuto
            log.flush()
            log.seek(0)
            contenuto = log.read()
            verifica("RS-10", "il log non riporta l'oggetto dei messaggi",
                     False, "Verifica di configurazione" in contenuto)
            verifica("4.2.2.2", "il log documenta la semina delle regole dal file",
                     True, "seeded" in contenuto)

            verifica("RF-13", "il meccanismo di eliminazione per numerosita' e' operante",
                     3, pruning_per_numerosita(binario, tmp))

            # RS-01: la seconda via di attivazione del rilascio esiste davvero.
            # Il caso non verifica la configurazione di riferimento ma la ragione
            # per cui essa dichiara vuote anche le variabili del rilascio: se
            # questo controllo smettesse di riuscire, quelle righe sarebbero
            # diventate superflue e andrebbero rilette, non tolte in silenzio.
            verifica("RS-01", "il rilascio si attiva anche senza file, con il solo host",
                     True, rilascio_attivabile_da_variabili(binario, tmp))
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
