#!/usr/bin/env python3
"""Esegue il piano di prova delle regole di tagging a runtime.

Verifica i comportamenti dichiarati e le limitazioni note della personalizzazione
IPZS descritta in FORK_CHANGES.md. I casi e i risultati attesi sono in
dev/testdata/piano-di-prova-tagging.md: questo script li esegue e stampa un
esito per ciascuno.

    python dev/testdata/prova-tagfilters.py [--mailpit ./mailpit]

Lo script avvia e ferma Mailpit da solo, su un database temporaneo e su porte
dedicate, per non interferire con eventuali istanze in esecuzione. Nessuna
dipendenza esterna: solo libreria standard.
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
import urllib.error
import urllib.request
from email.message import EmailMessage
from email.utils import formatdate, make_msgid

HTTP_PORT = 18025
SMTP_PORT = 11025
BASE = f"http://127.0.0.1:{HTTP_PORT}"

esiti = []


def caso(codice, descrizione, atteso, ottenuto):
    """Registra l'esito di un caso di prova confrontando atteso e ottenuto."""
    ok = atteso == ottenuto
    esiti.append((codice, descrizione, ok, atteso, ottenuto))
    stato = "OK  " if ok else "FALLITO"
    print(f"  {stato}  {codice}  {descrizione}")
    if not ok:
        print(f"          atteso:   {atteso}")
        print(f"          ottenuto: {ottenuto}")
    return ok


# ------------------------------------------------------------------ API


def api(metodo, percorso, corpo=None):
    dati = json.dumps(corpo).encode() if corpo is not None else None
    req = urllib.request.Request(
        BASE + percorso, data=dati, method=metodo,
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=20) as r:
        testo = r.read().decode()
    if not testo.strip():
        return None
    try:
        return json.loads(testo)
    except json.JSONDecodeError:
        return testo.strip()  # alcune rotte rispondono in testo semplice


def messaggi():
    """Elenco dei messaggi presenti, con i rispettivi tag."""
    r = api("GET", "/api/v1/messages?limit=200")
    return [(m["Subject"], sorted(m.get("Tags") or []), m["ID"]) for m in r["messages"]]


def tag_di(oggetto):
    """I tag del primo messaggio con quell'oggetto, o None se assente."""
    for sogg, tags, _ in messaggi():
        if sogg == oggetto:
            return tags
    return None


def id_di(oggetto):
    for sogg, _, mid in messaggi():
        if sogg == oggetto:
            return mid
    return None


def senza_tag():
    """Quanti messaggi non hanno alcun tag."""
    return sum(1 for _, tags, _ in messaggi() if not tags)


# ------------------------------------------------------------- processo


def avvia(binario, db, extra=()):
    p = subprocess.Popen(
        [binario, "--database", db,
         "--listen", f"127.0.0.1:{HTTP_PORT}",
         "--smtp", f"127.0.0.1:{SMTP_PORT}",
         "--max", "0", *extra],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    for _ in range(60):
        try:
            urllib.request.urlopen(BASE + "/readyz", timeout=1).read()
            return p
        except Exception:
            time.sleep(0.25)
    p.kill()
    raise SystemExit("Mailpit non risponde dopo l'avvio")


def ferma(p):
    p.terminate()
    try:
        p.wait(timeout=10)
    except subprocess.TimeoutExpired:
        p.kill()
    time.sleep(0.5)


def invia(mittente, oggetto, allegato=False):
    m = EmailMessage()
    m["From"] = mittente
    m["To"] = "utente@test.local"
    m["Subject"] = oggetto
    m["Date"] = formatdate(localtime=True)
    m["Message-ID"] = make_msgid(domain="test.local")
    m.set_content(f"Messaggio di prova: {oggetto}")
    if allegato:
        m.add_attachment(b"\x00" * 64, maintype="application",
                         subtype="octet-stream", filename="prova.bin")
    with smtplib.SMTP("127.0.0.1", SMTP_PORT, timeout=10) as s:
        s.send_message(m)
    time.sleep(0.6)  # il tagging avviene alla memorizzazione


# ---------------------------------------------------------------- prove

REGOLA_FATTURE = {"match": "subject:Fattura", "tags": ["Fatturazione"]}
REGOLA_ALLEGATI = {"match": "has:attachment", "tags": ["Con-allegato"]}


def prove_runtime(binario, tmp):
    db = os.path.join(tmp, "prova.db")
    p = avvia(binario, db)
    try:
        print("\nFase A - regole gestite a runtime\n")

        caso("PT-01", "istanza nuova: nessuna regola configurata",
             [], api("GET", "/api/v1/tag-filters"))

        # corpus inviato PRIMA di definire le regole
        invia("fatturazione@dev.test.local", "Fattura 2026/0001")
        invia("anagrafica@dev.test.local", "Conferma registrazione")
        invia("notifiche@dev.test.local", "Report giornaliero", allegato=True)
        caso("PT-02", "senza regole nessun messaggio viene etichettato",
             3, senza_tag())

        api("PUT", "/api/v1/tag-filters",
            {"Filters": [REGOLA_FATTURE, REGOLA_ALLEGATI]})
        caso("PT-03", "le regole salvate sono rilette identiche via API",
             [REGOLA_FATTURE, REGOLA_ALLEGATI], api("GET", "/api/v1/tag-filters"))

        caso("PT-04", "le regole non toccano i messaggi gia' presenti",
             [], tag_di("Fattura 2026/0001"))

        # attivazione immediata: nessun riavvio tra il salvataggio e l'invio
        invia("fatturazione@dev.test.local", "Fattura 2026/0002")
        caso("PT-05", "regola attiva sul messaggio successivo, senza riavvio",
             ["Fatturazione"], tag_di("Fattura 2026/0002"))

        invia("anagrafica@dev.test.local", "Fattura di cortesia", allegato=True)
        caso("PT-06", "due regole corrispondenti applicano entrambi i tag",
             ["Con-allegato", "Fatturazione"], tag_di("Fattura di cortesia"))

        caso("PT-07", "un messaggio non corrispondente resta senza tag",
             [], tag_di("Conferma registrazione"))

        # tag manuale, per verificare che l'applicazione retroattiva non rimuova
        mid = id_di("Conferma registrazione")
        api("PUT", "/api/v1/tags", {"IDs": [mid], "Tags": ["Messo-a-mano"]})

        agg = api("POST", "/api/v1/tag-filters/apply")
        caso("PT-08", "applicazione retroattiva: due messaggi preesistenti aggiornati",
             2, agg.get("updated"))
        caso("PT-09", "applicazione retroattiva: il messaggio preesistente e' etichettato",
             ["Fatturazione"], tag_di("Fattura 2026/0001"))
        caso("PT-10", "applicazione retroattiva additiva: il tag manuale resta",
             ["Messo-a-mano"], tag_di("Conferma registrazione"))

        # rinomina di un tag referenziato da una regola
        api("PUT", "/api/v1/tags/Fatturazione", {"Name": "Fatture-2026"})
        regole = api("GET", "/api/v1/tag-filters")
        caso("PT-11", "la rinomina di un tag non aggiorna le regole che lo citano",
             "Fatturazione", regole[0]["tags"][0])
        invia("fatturazione@dev.test.local", "Fattura 2026/0003")
        caso("PT-12", "il vecchio tag viene ricreato dal primo messaggio successivo",
             ["Fatturazione"], tag_di("Fattura 2026/0003"))
    finally:
        ferma(p)

    # riavvio sullo stesso database
    p = avvia(binario, db)
    try:
        regole = api("GET", "/api/v1/tag-filters")
        caso("PT-13", "le regole sopravvivono al riavvio del servizio",
             2, len(regole))
    finally:
        ferma(p)


def prove_file_configurazione(binario, tmp):
    """Regole da file: convivenza con quelle a runtime e loro invisibilita'."""
    print("\nFase B - regole definite da file di configurazione\n")
    yml = os.path.join(tmp, "tags.yaml")
    with open(yml, "w", encoding="utf-8") as f:
        f.write('filters:\n  - match: "subject:Contratto"\n    tags: "Da-file"\n')

    db = os.path.join(tmp, "prova-file.db")
    p = avvia(binario, db, extra=("--tags-config", yml))
    try:
        invia("legale@dev.test.local", "Contratto quadro")
        caso("PT-14", "la regola da file etichetta i messaggi in ingresso",
             ["Da-file"], tag_di("Contratto quadro"))
        caso("PT-15", "la regola da file non compare tra quelle gestite a runtime",
             [], api("GET", "/api/v1/tag-filters"))

        # una regola runtime convive con quella da file
        api("PUT", "/api/v1/tag-filters", {"Filters": [REGOLA_FATTURE]})
        invia("fatturazione@dev.test.local", "Fattura e Contratto allegati")
        caso("PT-16", "le due sorgenti di regole coesistono sullo stesso messaggio",
             ["Da-file", "Fatturazione"], tag_di("Fattura e Contratto allegati"))
    finally:
        ferma(p)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mailpit", default=None, help="percorso del binario")
    args = ap.parse_args()

    binario = args.mailpit or shutil.which("mailpit") or (
        "./mailpit.exe" if os.name == "nt" else "./mailpit")
    if not os.path.exists(binario) and not shutil.which(binario):
        raise SystemExit(f"binario non trovato: {binario}")

    print(f"Binario sotto prova: {binario}")
    tmp = tempfile.mkdtemp(prefix="mp-prova-tag-")
    try:
        prove_runtime(binario, tmp)
        prove_file_configurazione(binario, tmp)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)

    ok = sum(1 for *_, esito, _, _ in ((c, d, e, a, o) for c, d, e, a, o in esiti) if esito)
    print(f"\n{'='*66}\nEsito: {ok}/{len(esiti)} casi superati")
    if ok != len(esiti):
        print("Casi falliti: " + ", ".join(c for c, _, e, _, _ in esiti if not e))
    sys.exit(0 if ok == len(esiti) else 1)


if __name__ == "__main__":
    main()
