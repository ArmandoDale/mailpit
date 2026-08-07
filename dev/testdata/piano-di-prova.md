# Piano di prova — isolamento per progetto e autenticazione OIDC

Casi da eseguire a mano sull'ambiente locale. Le prove marcate **⛔ bypass**
sono quelle che contano: verificano che il confine regga quando qualcuno prova
ad aggirarlo, non solo quando l'interfaccia si comporta bene.

Ogni caso ha un risultato atteso concreto. Se un conteggio non torna, non
"aggiustare l'attesa": è il prodotto che sta sbagliando.

---

## 0. Preparazione

```powershell
# 1. identity provider
$env:KC_BOOTSTRAP_ADMIN_USERNAME='admin'; $env:KC_BOOTSTRAP_ADMIN_PASSWORD='admin'
..\..\keycloak-26.0.8\bin\kc.bat start-dev --import-realm --http-port=8080

# 2. Mailpit
go run . --oidc-issuer http://localhost:8080/realms/mailpit `
         --oidc-client-id mailpit --oidc-client-secret mailpit-dev-secret `
         --oidc-redirect-url http://localhost:8025/auth/callback

# 3. dati di prova (40 messaggi)
python dev/testdata/send-samples.py
```

I messaggi stanno **in memoria**: ogni riavvio di Mailpit azzera tutto e il
corpus va rimandato. Utenti: `alfa`, `beta`, `both`, `admin1`, `nessuno`,
password `password` per tutti. Console Keycloak: <http://localhost:8080>,
`admin` / `admin`.

**Verifica preliminare**: `python dev/testdata/check-isolation.py` deve uscire
con codice 0. Se fallisce qui, non proseguire con le prove manuali.

---

## 1. Accesso

| # | Caso | Come | Atteso |
|---|---|---|---|
| 1.1 | Senza sessione l'API non risponde | `curl -i http://localhost:8025/api/v1/messages` | **401**, nessun dato |
| 1.2 | Navigazione anonima | apri <http://localhost:8025> | reindirizza a Keycloak, chiede le credenziali |
| 1.3 | Login riuscito | entra come `alfa` | atterri sulla casella, in basso a sinistra il tuo nome e "progetti visibili: progetto-alfa" |
| 1.4 | Il token non è nel browser | DevTools → Application → Cookie | c'è **solo** `mp_session`, marcato `HttpOnly`. Nessun token in localStorage o sessionStorage |
| 1.5 | ⛔ Cookie inventato | sostituisci il valore di `mp_session` con testo a caso, ricarica | **401** / rimando al login, nessun contenuto |
| 1.6 | Scadenza sessione | riavvia con `--oidc-session-ttl 2m`, entra, aspetta 2 minuti, ricarica | rimando al login |

---

## 2. Isolamento in lettura

Conteggi attesi, da leggere nel contatore dell'elenco:

| Account | Messaggi | Di cui senza tag | Vede `progetto-gamma` |
|---|---|---|---|
| `alfa` | **23** | 8 | no |
| `beta` | **23** | 8 | no |
| `both` | **35** | 8 | no |
| `admin1` | **40** | 8 | **sì** |
| `nessuno` | **8** | 8 | no |

| # | Caso | Come | Atteso |
|---|---|---|---|
| 2.1 | Conteggi | entra con ciascun account | i numeri qui sopra, esatti |
| 2.2 | Elenco tag | guarda la barra laterale con `alfa` | compaiono `progetto-alfa` e `progetto-beta`, **mai** `progetto-gamma` |
| 2.3 | Mail trasversali | con `alfa`, apri "Fermo dei sistemi il 15 agosto" | visibile, e porta **entrambi** i tag: è corretto, non è una falla |
| 2.4 | Utente senza ruoli | entra come `nessuno` | 8 messaggi, tutti senza tag. **Non** deve diventare amministratore |
| 2.5 | Il caso di controllo | cerca "Gamma" con ogni account | solo `admin1` trova risultati |
| 2.6 | Ricerca | con `alfa` cerca una parola contenuta in una mail di beta (es. "Tracking") | nessun risultato |
| 2.7 | Contatori | confronta il totale con la somma dei tag mostrati | coerenti fra loro, nessun conteggio "di troppo" |

---

## 3. ⛔ Aggiramento in lettura

Il punto non è l'interfaccia: è che il server rifiuti anche quando la richiesta
non passa dall'interfaccia. Serve l'ID di un messaggio di un altro progetto —
prendilo dall'elenco di `admin1`.

| # | Caso | Come | Atteso |
|---|---|---|---|
| 3.1 | Messaggio altrui per ID | da `alfa`: `GET /api/v1/message/<id-di-beta>` | **404**, non 403: l'esistenza dell'ID non va rivelata |
| 3.2 | Sorgente grezza | `GET /api/v1/message/<id-altrui>/raw` | 404 |
| 3.3 | Intestazioni | `GET /api/v1/message/<id-altrui>/headers` | 404 |
| 3.4 | Allegati | `GET /api/v1/message/<id-altrui>/part/1` | 404 |
| 3.5 | Anteprima HTML | `GET /view/<id-altrui>.html` | 404 |
| 3.6 | Ricerca con tag altrui | `GET /api/v1/search?query=tag:progetto-beta` da `alfa` | solo le 3 trasversali, non le 12 di beta |

---

## 4. ⛔ Aggiramento in scrittura

Qui stava una delle falle trovate: gli ID arrivano nel **corpo** della
richiesta, non nell'URL, e vanno filtrati lo stesso.

| # | Caso | Come | Atteso |
|---|---|---|---|
| 4.1 | "Elimina le mie" | da `alfa`, elimina tutto dall'interfaccia | spariscono i suoi 23; `beta` continua a vederne 23 |
| 4.2 | Eliminazione mirata altrui | da `alfa`: `DELETE /api/v1/messages` con `{"IDs":["<id-di-beta>"]}` | il messaggio di beta **resta**: verificalo rientrando come `beta` |
| 4.3 | Segna letto in blocco | da `alfa`, con l'ID di un messaggio di beta | lo stato di beta non cambia |
| 4.4 | Eliminazione per ricerca | `DELETE /api/v1/search?query=...` da `alfa` | tocca solo il suo scope |

Dopo questo gruppo, rimanda il corpus: `python dev/testdata/send-samples.py`.

---

## 5. Permessi sui tag

| # | Caso | Come | Atteso |
|---|---|---|---|
| 5.1 | Tab regole | guarda le impostazioni con `alfa` | "Tag filters" non compare |
| 5.2 | ⛔ Regole via API | da `alfa`: `GET /api/v1/tag-filters` | **403** |
| 5.3 | ⛔ Scrittura regole | da `alfa`: `PUT /api/v1/tag-filters` | **403** |
| 5.4 | ⛔ Applica agli esistenti | da `alfa`: `POST /api/v1/tag-filters/apply` | **403** |
| 5.5 | Admin sulle regole | le stesse tre da `admin1` | 200, funzionano |
| 5.6 | ⛔ Svuotare i tag | da `alfa`: `PUT /api/v1/tags` con `{"IDs":["<suo-id>"],"Tags":[]}` | **403**: senza tag sarebbe visibile a tutti |
| 5.7 | ⛔ Tag di un altro progetto | da `alfa`, `Tags:["progetto-beta"]` su un suo messaggio | **403** |
| 5.8 | ⛔ Sottrarre un tag condiviso | da `alfa`, `Tags:["progetto-alfa"]` su una mail alfa+beta | 200, **ma** `beta` continua a vederla: il conteggio di beta resta 23 |
| 5.9 | Rinomina / elimina tag | da `alfa`: `PUT` e `DELETE /api/v1/tags/progetto-alfa` | **403** |

---

## 6. Regole tag regex

| # | Caso | Come | Atteso |
|---|---|---|---|
| 6.1 | Un tag per progetto | da `admin1`, regola tipo `regex`, campo `to`, match `@([a-z0-9-]+)\.dev\.it$`, tag `$1` | il corpus usa destinatari `team@progetto-alfa.dev.it`: una sola regola genera un tag per progetto |
| 6.2 | Applica agli esistenti | premi "applica" | il numero di messaggi aggiornati è coerente |
| 6.3 | Regex non valida | salva `@([a-z` | rifiutata al salvataggio, con messaggio |
| 6.4 | Retrocompatibilità | una regola di tipo `search` già esistente | continua a funzionare come prima |

---

## 7. Uscita

| # | Caso | Come | Atteso |
|---|---|---|---|
| 7.1 | Esci | entra come `alfa`, premi "Esci" | finisci sulla pagina di Keycloak e **ti richiede le credenziali** |
| 7.2 | ⛔ Cookie vecchio | riusa il valore di `mp_session` di prima | **401** |
| 7.3 | Cambio utente | esci da `alfa`, entra come `nessuno` | il conteggio passa da 23 a **8** |
| 7.4 | Provider senza end-session | non riproducibile su Keycloak | coperto dai test Go |

Se dopo "Esci" rientri senza che nulla ti venga chiesto, è tornata la
regressione: il logout locale funzionava ma la sessione SSO sopravviveva.

---

## 8. Aggiornamento in tempo reale

| # | Caso | Come | Atteso |
|---|---|---|---|
| 8.1 | Notifica nel proprio scope | `alfa` aperto; manda una mail con `X-Tags: progetto-alfa` | compare senza ricaricare |
| 8.2 | ⛔ Notifica altrui | `alfa` aperto; manda `X-Tags: progetto-beta` | **non** compare, e il contatore non si muove |
| 8.3 | Senza tag | manda una mail senza `X-Tags` | compare a tutti, `nessuno` compreso |
| 8.4 | WebSocket autenticato | apri la casella e guarda la connessione in DevTools | l'handshake porta il cookie, nessun token nell'URL |

Per inviare al volo:
`python -c "import smtplib;from email.message import EmailMessage;m=EmailMessage();m['Subject']='prova';m['From']='a@b.it';m['To']='c@d.it';m['X-Tags']='progetto-beta';m.set_content('x');smtplib.SMTP('localhost',1025).send_message(m)"`

---

## 9. Non regressione a monte

Il fork deve restare Mailpit anche quando l'autenticazione è spenta.

| # | Caso | Come | Atteso |
|---|---|---|---|
| 9.1 | Senza OIDC | avvia senza le opzioni `--oidc-*` | comportamento identico a monte: nessuna sessione richiesta, si vede tutto |
| 9.2 | Basic Auth | avvia con `--ui-auth user:pass` | funziona come prima, per CI e automazioni |
| 9.3 | POP3 | scarica con un client POP3 | invariato |
| 9.4 | Suite | `go test ./...` | 13 pacchetti verdi |

---

## 10. Da provare quando ci sarà WSO2

Non riproducibili su Keycloak, restano aperti fino al client registrato:

- **issuer disallineato** — serve `--oidc-skip-issuer-check`; la firma va comunque verificata contro il JWKS del tenant;
- **claim ruoli come stringa** separata da virgole invece che array — coperto dai test unitari, non dall'ambiente;
- **prefisso user store** `ICTIPZS/` su identità federate da AD/LDAP;
- **claim che non arrivano**: il claim può esistere sull'utente e non comparire nel token, senza alcun errore. Verificare che oltre a `groups` arrivi anche l'identificativo dell'utente;
- **§27.4** — chiamata diretta al workload scavalcando il gateway: deve rispondere 401, non 200.
