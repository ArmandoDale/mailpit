#!/usr/bin/env python3
"""Popola Mailpit con un piccolo corpus di prova per le regole di tagging.

I messaggi sono costruiti in modo che ciascuna regola del piano di prova
(dev/testdata/piano-di-prova-tagging.md) abbia sia messaggi che deve
intercettare sia messaggi che non deve intercettare: senza i secondi una
regola troppo larga passerebbe la prova.

    python dev/testdata/send-samples.py [--host localhost] [--port 1025]

Nessuna dipendenza esterna: solo libreria standard.
"""

import argparse
import smtplib
import struct
import zlib
from email.message import EmailMessage
from email.utils import formatdate, make_msgid


def png(width, height, rgb):
    """Un PNG a tinta unita, per avere un allegato senza file esterni."""
    raw = b"".join(b"\x00" + bytes(rgb) * width for _ in range(height))

    def chunk(tag, data):
        c = tag + data
        return struct.pack(">I", len(data)) + c + struct.pack(">I", zlib.crc32(c))

    return (
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
        + chunk(b"IDAT", zlib.compress(raw))
        + chunk(b"IEND", b"")
    )


# Il corpus. Ogni voce: (mittente, destinatario, oggetto, allegato si/no, nota)
#
# La colonna "nota" dice a quale caso di prova serve il messaggio: serve a chi
# legge il corpus per capire perche' un messaggio e' fatto cosi'.
CORPUS = [
    ("fatturazione@dev.test.local", "utente1@test.local",
     "Fattura 2026/0001 emessa", False, "PT-01: intercettato da subject:Fattura"),
    ("fatturazione@dev.test.local", "utente2@test.local",
     "Fattura 2026/0002 emessa", False, "PT-01: intercettato da subject:Fattura"),
    ("fatturazione@dev.test.local", "utente3@test.local",
     "Sollecito di pagamento", True, "PT-05: stesso mittente, oggetto diverso"),
    ("anagrafica@dev.test.local", "utente1@test.local",
     "Conferma registrazione", False, "controllo: nessuna regola lo intercetta"),
    ("anagrafica@dev.test.local", "utente4@test.local",
     "Fattura di cortesia allegata", True, "PT-04: intercettato da due regole"),
    ("notifiche@dev.test.local", "ops@test.local",
     "Report giornaliero", True, "PT-05: intercettato da has:attachment"),
    ("notifiche@dev.test.local", "ops@test.local",
     "Avviso di manutenzione", False, "controllo: nessun allegato, nessun tag"),
    ("monitoraggio@qas.test.local", "ops@test.local",
     "Soglia superata su nodo 3", False, "controllo: ambiente diverso"),
]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="localhost")
    ap.add_argument("--port", type=int, default=1025)
    args = ap.parse_args()

    allegato = png(12, 12, (0, 102, 204))

    with smtplib.SMTP(args.host, args.port, timeout=10) as smtp:
        for mittente, destinatario, oggetto, con_allegato, nota in CORPUS:
            m = EmailMessage()
            m["From"] = mittente
            m["To"] = destinatario
            m["Subject"] = oggetto
            m["Date"] = formatdate(localtime=True)
            m["Message-ID"] = make_msgid(domain="test.local")
            m.set_content(
                f"Messaggio di prova.\n\nOggetto: {oggetto}\nScopo: {nota}\n"
            )
            m.add_alternative(
                f"<html><body><p>Messaggio di prova.</p>"
                f"<p>Scopo: {nota}</p></body></html>",
                subtype="html",
            )
            if con_allegato:
                m.add_attachment(
                    allegato, maintype="image", subtype="png", filename="prova.png"
                )
            smtp.send_message(m)
            print(f"inviato  {oggetto:38s}  {nota}")

    print(f"\n{len(CORPUS)} messaggi inviati a {args.host}:{args.port}")


if __name__ == "__main__":
    main()
