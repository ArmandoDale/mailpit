#!/usr/bin/env python3
"""Popola Mailpit con un corpus di prova per verificare l'isolamento per progetto.

I tag sono assegnati con l'header X-Tags, che Mailpit riconosce nativamente:
cosi' il corpus non dipende da regole tag configurate. I destinatari usano
comunque il dominio <progetto>.dev.it, in modo che lo stesso corpus serva a
provare la regola regex `@([a-z0-9-]+)\\.dev\\.it$` -> `$1` con "applica agli
esistenti".

    python dev/testdata/send-samples.py [--host localhost] [--port 1025]

Cosa deve vedere ogni account (realm dev/keycloak):

    alfa     -> progetto-alfa + senza tag
    beta     -> progetto-beta + senza tag
    both     -> progetto-alfa + progetto-beta + senza tag
    admin1   -> tutto, compreso progetto-gamma
    nessuno  -> SOLO le mail senza tag

progetto-gamma e' il caso di controllo: nessun utente ha quel ruolo, quindi
quelle mail devono comparire esclusivamente ad admin1.
"""

import argparse
import random
import smtplib
import struct
import zlib
from datetime import datetime, timedelta, timezone
from email.message import EmailMessage
from email.utils import formatdate, make_msgid

# ---------------------------------------------------------------- allegati


def png(width, height, rgb):
    """Un PNG a tinta unita, senza dipendenze esterne."""
    raw = b"".join(
        b"\x00" + bytes(rgb) * width for _ in range(height)
    )

    def chunk(tag, data):
        c = tag + data
        return struct.pack(">I", len(data)) + c + struct.pack(">I", zlib.crc32(c))

    return (
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
        + chunk(b"IDAT", zlib.compress(raw, 9))
        + chunk(b"IEND", b"")
    )


def pdf(title):
    """Un PDF minimo ma valido, che i viewer aprono davvero."""
    text = title[:60].replace("(", "").replace(")", "")
    body = f"BT /F1 18 Tf 60 720 Td ({text}) Tj ET".encode("latin-1", "replace")
    objs = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] "
        b"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
        b"<< /Length " + str(len(body)).encode() + b" >>\nstream\n" + body + b"\nendstream",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    ]
    out = bytearray(b"%PDF-1.4\n")
    offsets = []
    for i, o in enumerate(objs, start=1):
        offsets.append(len(out))
        out += f"{i} 0 obj\n".encode() + o + b"\nendobj\n"
    xref = len(out)
    out += f"xref\n0 {len(objs) + 1}\n".encode() + b"0000000000 65535 f \n"
    for off in offsets:
        out += f"{off:010d} 00000 n \n".encode()
    out += (
        f"trailer\n<< /Size {len(objs) + 1} /Root 1 0 R >>\nstartxref\n{xref}\n".encode()
        + b"%%EOF\n"
    )
    return bytes(out)


CSV = (
    "data,transazione,importo,esito\n"
    "2026-08-01,TRX-88213,1240.50,OK\n"
    "2026-08-02,TRX-88214,89.00,OK\n"
    "2026-08-03,TRX-88215,15600.00,RIFIUTATA\n"
    "2026-08-04,TRX-88216,432.19,OK\n"
).encode()

LOG = (
    "09:14:02 INFO  avvio batch notturno\n"
    "09:14:07 WARN  coda 'firma' sopra soglia: 812 elementi\n"
    "09:15:44 ERROR timeout su gateway PagoPA dopo 30s\n"
    "09:15:45 ERROR batch interrotto, 3 documenti non elaborati\n"
).encode()


# ---------------------------------------------------------------- contenuti

HTML_WRAP = """\
<html><body style="font-family:-apple-system,Segoe UI,Roboto,sans-serif;
 color:#1f2933;line-height:1.5;max-width:600px">
<div style="background:{color};color:#fff;padding:16px 20px;border-radius:6px 6px 0 0">
  <h2 style="margin:0;font-size:19px">{heading}</h2>
</div>
<div style="border:1px solid #e4e7eb;border-top:0;padding:20px;border-radius:0 0 6px 6px">
{body}
</div>
<p style="color:#7b8794;font-size:12px">Messaggio generato da send-samples.py &mdash;
ambiente di test, nessun dato reale.</p>
</body></html>"""


def html(heading, body, color="#2f6fed"):
    return HTML_WRAP.format(heading=heading, body=body, color=color)


# (oggetto, corpo testuale, html|None, allegati, cc, inline)
def corpus():
    """Ritorna la lista (tags, messaggio) del corpus completo."""
    m = []

    def add(tags, subject, text, html_body=None, attach=(), cc=(), inline=None,
            sender=None, to=None, thread=None, priority=None):
        m.append(dict(tags=tags, subject=subject, text=text, html=html_body,
                      attach=list(attach), cc=list(cc), inline=inline,
                      sender=sender, to=to, thread=thread, priority=priority))

    # ---------------- progetto-alfa (12)
    a = ["progetto-alfa"]
    add(a, "Benvenuto in Progetto Alfa",
        "Ciao,\n\nl'ambiente di test per Progetto Alfa e' attivo.\n\nBuon lavoro.",
        html("Ambiente attivo", "<p>L'ambiente di test per <b>Progetto Alfa</b> "
             "e' attivo.</p><p>Le credenziali arrivano con una mail separata.</p>"))
    add(a, "Reimposta la tua password",
        "Per reimpostare la password apri questo collegamento entro 30 minuti:\n"
        "https://alfa.dev.it/reset?token=8f3a91c2\n\nSe non hai richiesto tu il "
        "cambio, ignora il messaggio.",
        html("Reimposta la password",
             '<p>Apri il collegamento entro <b>30 minuti</b>:</p>'
             '<p><a href="https://alfa.dev.it/reset?token=8f3a91c2" '
             'style="background:#2f6fed;color:#fff;padding:10px 18px;'
             'border-radius:4px;text-decoration:none;display:inline-block">'
             'Reimposta la password</a></p>', "#2f6fed"))
    add(a, "Fattura 2026/0188 - scadenza 31/08",
        "In allegato la fattura 2026/0188.\nImporto: 1.240,50 EUR\nScadenza: 31/08/2026",
        html("Fattura 2026/0188",
             "<p>Importo: <b>1.240,50 EUR</b><br>Scadenza: 31/08/2026</p>", "#0b7285"),
        attach=[("fattura-2026-0188.pdf", "application", "pdf", pdf("Fattura 2026/0188"))])
    add(a, "Riepilogo transazioni di agosto",
        "In allegato il riepilogo in formato CSV.",
        attach=[("transazioni-agosto.csv", "text", "csv", CSV)])
    add(a, "[ALLARME] Gateway PagoPA non raggiungibile",
        "Il batch notturno si e' interrotto.\nVedi il log allegato.",
        html("Gateway non raggiungibile",
             "<p>Il batch notturno si e' interrotto alle <b>09:15</b>.</p>"
             "<p>3 documenti non elaborati.</p>", "#c92a2a"),
        attach=[("batch.log", "text", "plain", LOG)], priority="1")
    add(a, "Verbale riunione del 4 agosto — presenti: Rossi, Bianchi, Verdi",
        "Punti discussi:\n\n1. Migrazione su Mailpit\n2. Isolamento per progetto\n"
        "3. Registrazione client OIDC su WSO2\n\nProssima riunione: 11/08.")
    add(a, "Il tuo report settimanale e' pronto \U0001F4C8",
        "Il report della settimana 31 e' disponibile in dashboard.",
        html("Report settimana 31",
             "<ul><li>Documenti firmati: <b>1.204</b></li>"
             "<li>Scarti: <b>17</b> (1,4%)</li>"
             "<li>Tempo medio: <b>2,3 s</b></li></ul>", "#5f3dc4"))
    add(a, "Conferma indirizzo email",
        "Conferma il tuo indirizzo cliccando il link nel messaggio HTML.",
        html("Conferma indirizzo",
             '<p>Ci siamo quasi. <a href="https://alfa.dev.it/verify?c=aa11">'
             'Conferma il tuo indirizzo</a>.</p>'))
    add(a, "Screenshot dell'errore in produzione",
        "Allego lo screenshot come richiesto.",
        html("Screenshot", '<p>Ecco cosa vede l\'utente:</p>'
             '<p><img src="cid:{cid}" width="220" alt="schermata"></p>', "#e8590c"),
        inline=("schermata.png", png(220, 120, (232, 89, 12))))
    add(a, "Re: Verbale riunione del 4 agosto",
        "Aggiungo un punto: va deciso chi registra il client su WSO2.\n\n"
        "> Punti discussi:\n> 1. Migrazione su Mailpit",
        thread=True)
    add(a, "Manutenzione programmata sabato 9 agosto, 22:00-02:00",
        "L'ambiente sara' indisponibile per manutenzione.\n"
        "Non e' richiesta alcuna azione da parte vostra.")
    add(a, "Notifica con caratteri accentati: perche' l'accento e' importante",
        "Citta', universita', perche', pero'. Se leggi questa riga senza caratteri "
        "strani, la codifica UTF-8 funziona.\n\nSimboli: € 100 — © 2026 — ½")

    # ---------------- progetto-beta (12)
    b = ["progetto-beta"]
    add(b, "Benvenuto in Progetto Beta",
        "L'ambiente di test per Progetto Beta e' attivo.",
        html("Ambiente attivo", "<p>L'ambiente di <b>Progetto Beta</b> e' pronto.</p>",
             "#2b8a3e"))
    add(b, "Codice di verifica: 481-902",
        "Il tuo codice di verifica e' 481-902.\nScade fra 10 minuti.",
        html("Codice di verifica",
             '<p style="font-size:30px;letter-spacing:6px;font-weight:700;'
             'text-align:center;margin:20px 0">481-902</p>'
             '<p style="text-align:center;color:#7b8794">Scade fra 10 minuti</p>',
             "#2b8a3e"))
    add(b, "Ordine BET-5512 spedito",
        "Il tuo ordine e' stato spedito.\nTracking: IT884120993",
        html("Ordine spedito",
             "<p>Tracking: <code>IT884120993</code></p>"
             "<p>Consegna prevista: <b>8 agosto</b></p>", "#2b8a3e"))
    add(b, "Contratto da firmare — copia in allegato",
        "In allegato la copia del contratto.",
        attach=[("contratto-beta.pdf", "application", "pdf", pdf("Contratto Beta 2026"))])
    add(b, "[ALLARME] Spazio su disco sotto il 10%",
        "Il volume /var/lib e' al 92%.\nIntervenire entro fine giornata.",
        html("Spazio su disco", "<p>Volume <code>/var/lib</code> al <b>92%</b>.</p>",
             "#c92a2a"), priority="1")
    add(b, "Newsletter Beta — agosto 2026",
        "Le novita' del mese: nuovo cruscotto, export CSV, API v2.",
        html("Novita' di agosto",
             "<h3 style='margin-top:0'>Nuovo cruscotto</h3>"
             "<p>Filtri salvabili e grafici per progetto.</p>"
             "<h3>Export CSV</h3><p>Su tutte le viste.</p>"
             "<h3>API v2</h3><p>In beta, retrocompatibile.</p>", "#2b8a3e"))
    add(b, "Riepilogo transazioni (stesso CSV, progetto diverso)",
        "Allego il riepilogo.",
        attach=[("transazioni-beta.csv", "text", "csv", CSV)])
    add(b, "Logo aggiornato per la nuova intestazione",
        "Allego il logo nella versione definitiva.",
        html("Nuovo logo", '<p><img src="cid:{cid}" width="180" alt="logo"></p>',
             "#2b8a3e"),
        inline=("logo-beta.png", png(180, 180, (43, 138, 62))))
    add(b, "Invito: revisione di sprint, giovedi 14:30",
        "Sala 3 oppure in videochiamata.\nOrdine del giorno in allegato.",
        attach=[("odg.pdf", "application", "pdf", pdf("Ordine del giorno"))])
    add(b, "Messaggio con destinatari multipli e copia conoscenza",
        "Questo messaggio ha piu' destinatari e due indirizzi in copia.",
        cc=["supervisione@progetto-beta.dev.it", "qualita@progetto-beta.dev.it"])
    add(b, "Re: Codice di verifica: 481-902",
        "Non mi e' arrivato nulla, potete rimandarlo?", thread=True)
    add(b, "Oggetto volutamente lunghissimo per vedere come si comporta l'elenco "
           "quando il testo non ci sta nella colonna e deve essere troncato in qualche modo",
        "Serve solo a mettere alla prova il troncamento nell'interfaccia.")

    # ---------------- alfa + beta insieme (3): visibili a entrambi
    ab = ["progetto-alfa", "progetto-beta"]
    add(ab, "Comunicazione trasversale: fermo dei sistemi il 15 agosto",
        "Riguarda entrambi i progetti.\nFermo totale dalle 08:00 alle 20:00.",
        html("Fermo dei sistemi", "<p>Riguarda <b>entrambi</b> i progetti.</p>"
             "<p>15 agosto, 08:00-20:00.</p>", "#e8590c"))
    add(ab, "Nuova procedura di rilascio, valida per tutti i progetti",
        "La procedura entra in vigore dal 1 settembre.\nDettagli in allegato.",
        attach=[("procedura-rilascio.pdf", "application", "pdf", pdf("Procedura di rilascio"))])
    add(ab, "Sondaggio interno sugli strumenti di sviluppo",
        "Tre minuti, risposte anonime: https://sondaggi.dev.it/strumenti")

    # ---------------- progetto-gamma (5): nessun utente ha il ruolo -> solo admin1
    g = ["progetto-gamma"]
    add(g, "Gamma — se vedi questa mail hai i permessi di amministratore",
        "Nessun utente di progetto ha il ruolo su Gamma.\n"
        "Questa mail deve comparire SOLO ad admin1.",
        html("Riservata agli amministratori",
             "<p>Nessun utente di progetto ha il ruolo su <b>Gamma</b>.</p>"
             "<p>Se la stai leggendo con un account diverso da <code>admin1</code>, "
             "l'isolamento non funziona.</p>", "#862e9c"))
    add(g, "Gamma — credenziali di servizio",
        "utente: svc-gamma\npassword: non-usare-in-produzione")
    add(g, "Gamma — esito della migrazione notturna",
        "Migrati 41.882 record, 0 errori.")
    add(g, "Gamma — certificato in scadenza fra 14 giorni",
        "Il certificato di gamma.dev.it scade il 20/08/2026.", priority="1")
    add(g, "Gamma — verbale riservato",
        "Contenuto riservato.",
        attach=[("verbale-gamma.pdf", "application", "pdf", pdf("Verbale riservato"))])

    # ---------------- senza tag (8): visibili a TUTTI, anche a 'nessuno'
    n = []
    add(n, "Mail senza tag — deve vederla chiunque, anche 'nessuno'",
        "Questa mail non ha tag.\nPer decisione esplicita e' visibile a tutti: "
        "evita il fallimento silenzioso 'la mail non arriva'.",
        html("Senza tag", "<p>Nessun tag applicato: <b>visibile a tutti</b>, "
             "compreso l'utente senza ruoli.</p>", "#616e7c"),
        to="destinatario@example.com")
    add(n, "Prova SMTP dal servizio di CI",
        "Messaggio di prova inviato dalla pipeline.", to="ci@example.com")
    add(n, "Messaggio di solo testo, senza parte HTML",
        "Nessun HTML qui dentro.\nSolo testo semplice, come lo mandano molti "
        "sistemi legacy.\n\n--\nSistema di notifica v1.2",
        to="legacy@example.com")
    add(n, "Messaggio di solo HTML, senza parte testuale",
        None,
        html("Solo HTML", "<p>Questo messaggio non ha una parte in testo semplice: "
             "utile per provare la vista <i>Text</i> dell'interfaccia.</p>", "#616e7c"),
        to="html@example.com")
    add(n, "Indirizzo mittente con nome contenente virgola",
        "Il mittente e' \"Rossi, Mario\" <mario.rossi@example.com>.",
        sender='"Rossi, Mario" <mario.rossi@example.com>', to="parsing@example.com")
    add(n, "Messaggio con allegato di dimensione non banale",
        "Allego un PNG piu' grande per provare anteprima e download.",
        attach=[("grande.png", "image", "png", png(640, 480, (97, 110, 124)))],
        to="allegati@example.com")
    add(n, "\U0001F4E7 Oggetto con emoji e simboli — è gestito bene?",
        "Emoji nell'oggetto e nel corpo: \U0001F680 \U0001F41B ✅\n"
        "Serve a verificare la codifica.", to="unicode@example.com")
    add(n, "Benvenuto: questa mail non appartiene ad alcun progetto",
        "Nessun tag, nessun progetto.\nVisibile a chiunque acceda.",
        to="tutti@example.com")

    return m


# ---------------------------------------------------------------- invio

SENDERS = {
    "progetto-alfa": ["notifiche@progetto-alfa.dev.it", "no-reply@progetto-alfa.dev.it",
                      "amministrazione@progetto-alfa.dev.it"],
    "progetto-beta": ["notifiche@progetto-beta.dev.it", "ordini@progetto-beta.dev.it",
                      "newsletter@progetto-beta.dev.it"],
    "progetto-gamma": ["sistemi@progetto-gamma.dev.it"],
    "": ["sistema@example.com", "noreply@example.com"],
}


def build(spec, when, rnd):
    msg = EmailMessage()
    tags = spec["tags"]
    key = tags[0] if tags else ""

    msg["Subject"] = spec["subject"]
    msg["From"] = spec["sender"] or rnd.choice(SENDERS.get(key, SENDERS[""]))
    if spec["to"]:
        msg["To"] = spec["to"]
    elif tags:
        # Il dominio riflette il progetto: serve anche alla regola regex.
        msg["To"] = f"team@{key.replace('progetto-', 'progetto-')}.dev.it"
    else:
        msg["To"] = "destinatario@example.com"
    if spec["cc"]:
        msg["Cc"] = ", ".join(spec["cc"])
    if tags:
        msg["X-Tags"] = ", ".join(tags)
    if spec["priority"]:
        msg["X-Priority"] = spec["priority"]
        msg["Importance"] = "high"
    msg["Date"] = formatdate(when.timestamp(), localtime=True)
    msg["Message-ID"] = make_msgid(domain="dev.it")
    if spec["thread"]:
        msg["In-Reply-To"] = make_msgid(domain="dev.it")
        msg["References"] = msg["In-Reply-To"]

    cid = make_msgid(domain="dev.it")
    body_html = spec["html"]
    if body_html and "{cid}" in body_html:
        body_html = body_html.replace("{cid}", cid[1:-1])

    if spec["text"] is None and body_html:
        msg.set_content("placeholder")
        msg.clear_content()
        msg.set_content(body_html, subtype="html")
    else:
        msg.set_content(spec["text"] or "")
        if body_html:
            msg.add_alternative(body_html, subtype="html")

    if spec["inline"]:
        name, data = spec["inline"]
        target = msg.get_payload()[-1] if msg.is_multipart() else msg
        target.add_related(data, maintype="image", subtype="png", cid=cid, filename=name)

    for name, maintype, subtype, data in spec["attach"]:
        msg.add_attachment(data, maintype=maintype, subtype=subtype, filename=name)

    return msg


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="localhost")
    ap.add_argument("--port", type=int, default=1025)
    ap.add_argument("--seed", type=int, default=20260806)
    args = ap.parse_args()

    rnd = random.Random(args.seed)
    msgs = corpus()

    # Date distribuite sugli ultimi 10 giorni, in ordine cronologico.
    now = datetime.now(timezone.utc)
    start = now - timedelta(days=10)
    step = (now - start) / max(len(msgs), 1)

    counts = {}
    with smtplib.SMTP(args.host, args.port, timeout=20) as s:
        for i, spec in enumerate(msgs):
            when = start + step * i + timedelta(minutes=rnd.randint(0, 40))
            msg = build(spec, when, rnd)
            s.send_message(msg)
            key = ", ".join(spec["tags"]) if spec["tags"] else "(senza tag)"
            counts[key] = counts.get(key, 0) + 1

    print(f"inviati {len(msgs)} messaggi a {args.host}:{args.port}\n")
    for k in sorted(counts):
        print(f"  {counts[k]:>3}  {k}")
    print("""
Atteso in interfaccia (http://localhost:8025), password 'password':

  alfa     15 messaggi   (12 alfa + 3 trasversali) + 8 senza tag  = 23
  beta     15 messaggi   (12 beta + 3 trasversali) + 8 senza tag  = 23
  both     27 messaggi   (12+12+3) + 8 senza tag                  = 35
  admin1   tutti                                                  = 40
  nessuno  solo le 8 senza tag                                    = 8

Se 'nessuno' vede piu' di 8 messaggi, o se un utente diverso da admin1 vede
un messaggio 'progetto-gamma', l'isolamento non sta funzionando.""")


if __name__ == "__main__":
    main()
