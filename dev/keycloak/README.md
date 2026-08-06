# Ambiente OIDC locale (Keycloak)

Provider di identità per sviluppare l'integrazione OIDC senza dipendere da WSO2.

## Perché Keycloak e non un mock

È un **OP vero**: discovery reale, JWKS reale, `authorization_code` + PKCE reale.
Codice che funziona qui funziona contro WSO2 a meno della configurazione.

Il realm riproduce di proposito le stranezze che sappiamo avere WSO2:

- i ruoli viaggiano in un claim `groups`, richiesto tramite uno scope `groups`
  (come sul tenant `normattiva.api`);
- i nomi dei ruoli sono prefissati con il qualificatore dello user store
  (`ICTIPZS/`), come si vede sul Dev Portal;
- esiste un gruppo aziendale non pertinente (`ICTIPZS/Domain Users`) che **non**
  deve diventare un tag.

Il client porta due protocol mapper propri per `preferred_username` ed `email`.
Non sono ridondanti: siccome il realm dichiara esplicitamente i propri
`clientScopes`, Keycloak non crea i built-in `profile` ed `email`, e i
riferimenti in `defaultClientScopes` vengono ignorati in silenzio. Senza i
mapper l'ID token arriva con i soli `groups` e Mailpit non ha un nome da
mostrare. È lo stesso genere di catena che su WSO2 governa i claim (§16.5 del
manuale interno): il claim esiste sull'utente, ma non per questo arriva nel
token.

### Cosa NON riproduce

L'**issuer disallineato** dei tenant WSO2: Keycloak riporta l'issuer
correttamente, quindi quel percorso resta coperto solo dal flag
`--oidc-skip-issuer-check` e va verificato contro il provider vero.

Non riproduce nemmeno la variante in cui il claim dei ruoli arriva come
**stringa separata da virgole** invece che come array: Keycloak usa sempre
l'array. Quel caso è coperto dai test unitari in `internal/oidc/roles_test.go`.

## Avvio

```bash
cd dev/keycloak
docker compose up -d
```

Console: <http://localhost:8080> — `admin` / `admin`

### Senza Docker (rete aziendale IPZS)

In rete IPZS il pull fallisce: Docker Desktop non riesce a uscire verso i
registry. Il ripiego è la distribuzione ZIP, che gira su Java 17+ (sul
portatile c'è Java 21) e importa **lo stesso** `realm-mailpit.json`:

```powershell
# una volta sola: scarica ed estrai
Invoke-WebRequest `
  -Uri https://github.com/keycloak/keycloak/releases/download/26.0.8/keycloak-26.0.8.zip `
  -OutFile keycloak-26.0.8.zip -Proxy http://proxy.ipzs.it:8080
Expand-Archive keycloak-26.0.8.zip -DestinationPath .
copy dev\keycloak\realm-mailpit.json keycloak-26.0.8\data\import\

# avvio
$env:KC_BOOTSTRAP_ADMIN_USERNAME='admin'; $env:KC_BOOTSTRAP_ADMIN_PASSWORD='admin'
keycloak-26.0.8\bin\kc.bat start-dev --import-realm --http-port=8080
```

Console, realm, utenti e credenziali client sono identici a quelli del compose:
la versione 26.0.8 è la stessa linea `26.0` dell'immagine.

## Utenti di prova

Password per tutti: `password`

| Utente | Ruoli | Cosa vede |
|---|---|---|
| `alfa` | `ICTIPZS/mailpit-progetto-alfa` | tag `progetto-alfa` + mail senza tag |
| `beta` | `ICTIPZS/mailpit-progetto-beta` | tag `progetto-beta` + mail senza tag |
| `both` | entrambi i progetti | entrambi i tag + mail senza tag |
| `admin1` | `ICTIPZS/mailpit-admin` | tutto |
| `nessuno` | solo `ICTIPZS/Domain Users` | **solo** le mail senza tag |

`nessuno` è il caso che vale la pena provare: un utente senza ruoli Mailpit non
deve diventare amministratore per errore.

## Avviare Mailpit contro Keycloak

```bash
go run . \
  --oidc-issuer http://localhost:8080/realms/mailpit \
  --oidc-client-id mailpit \
  --oidc-client-secret mailpit-dev-secret \
  --oidc-redirect-url http://localhost:8025/auth/callback
```

Poi apri <http://localhost:8025>: la navigazione non autenticata viene rediretta
su `/auth/login`.

Gli stessi valori funzionano come variabili d'ambiente `MP_OIDC_*`.

## Verifiche utili

```bash
# chi sono e cosa vedo
curl -b cookies.txt http://localhost:8025/auth/session

# il test che conta: senza sessione l'API non risponde
curl -i http://localhost:8025/api/v1/messages   # atteso: 401
```
