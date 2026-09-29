# Personalizzazioni del fork — Gestione a runtime delle regole di tagging

Il presente documento descrive le modifiche apportate dal fork IPZS al codice originale di
Mailpit, la loro motivazione, le istruzioni d'uso e le limitazioni note. È il documento di
riferimento RIF-01 del SID «Architettura Mailpit per SMTP di Test».

---

## Sintesi delle modifiche

| File | Tipo | Descrizione |
|------|------|-------------|
| `internal/storage/tagfilters_settings.go` | **NUOVO** | Persistenza su database delle regole di tagging a runtime e applicazione massiva ai messaggi esistenti |
| `internal/storage/tagfilters_file.go` | **NUOVO** | Semina delle regole a runtime dal file `--tags-config` e riscrittura del file a ogni modifica |
| `server/apiv1/tagfilters.go` | **NUOVO** | Endpoint REST per la gestione delle regole e per l'applicazione retroattiva |
| `internal/storage/tagfilters.go` | **MODIFICATO** | `LoadTagFilters()` applica le regole a runtime (database) e quelle eventualmente indicate con il parametro `--tag` |
| `config/tags.go` | **MODIFICATO** | Le regole del file di configurazione sono lette in `TagsConfigFilters` e usate come semina, non applicate direttamente |
| `config/config.go` | **MODIFICATO** | Introduce `TagsConfigFilters`, che raccoglie le regole lette dal file YAML tenendole distinte da quelle del parametro `--tag` |
| `internal/storage/database.go` | **MODIFICATO** | La semina è eseguita una sola volta all'avvio, prima del caricamento dei filtri |
| `server/server.go` | **MODIFICATO** | Registrazione delle tre nuove rotte API |
| `server/ui-src/components/AppSettings.vue` | **MODIFICATO** | Nuova scheda «Tag filters» nella finestra delle impostazioni |

**Estensione complessiva della personalizzazione:** 9 file, +584 / −2 righe rispetto
all'upstream. Le uniche due righe rimosse si trovano in `config/tags.go`. La modifica è
quindi quasi interamente additiva e circoscritta al tagging: **non sono stati modificati il
servizio SMTP, l'analisi dei messaggi, il nucleo dello storage dei messaggi,
l'autenticazione né le funzioni di rilascio e inoltro.** È il dato che sostanzia il
contenimento dell'onere di riallineamento all'upstream.

Fuori dalla personalizzazione, il `Dockerfile` differisce dall'upstream per le sole
versioni delle immagini di base, fissate (`golang:1.25-alpine`, `alpine:3.24`) invece che
lasciate a `latest`: dalla stessa baseline si ottiene così sempre la stessa immagine. Non
tocca il codice del prodotto e non rientra nel conteggio precedente.

---

## Descrizione della funzionalità

### Che cosa fa

Mailpit già consentiva di definire regole di etichettatura automatica tramite parametri di
avvio o file YAML di configurazione. Il fork aggiunge la **gestione delle regole
dall'interfaccia web**, con persistenza su database, senza riavvio del servizio né accesso
al filesystem della macchina.

Le regole sono valutate su tutti i **messaggi in arrivo** via SMTP. Una azione esplicita,
**«Apply to existing messages»**, consente di applicarle retroattivamente ai messaggi già
presenti.

### Come vengono applicati i tag

1. A ogni messaggio in ingresso, `tagFilterMatches(id)` valuta tutti i filtri caricati.
2. I filtri caricati sono le regole a runtime, conservate nel database, più le eventuali
   regole indicate con il parametro di avvio `--tag`.
3. I tag corrispondenti sono aggiunti automaticamente al messaggio.
4. Le regole del file `--tags-config` **non costituiscono una seconda fonte**: sono
   importate nel database alla prima esecuzione dell'istanza e da quel momento sono regole
   a runtime a tutti gli effetti (si veda *Il file di configurazione come sorgente delle
   regole*).

---

## File nuovi

### `internal/storage/tagfilters_settings.go`

Introduce tre funzioni pubbliche:

| Funzione | Descrizione |
|----------|-------------|
| `GetRuntimeTagFilters() []TagFilterRule` | Legge le regole dalla tabella `settings` (chiave `TagFilters`). Restituisce una lista vuota se non ve ne sono. |
| `SetRuntimeTagFilters(rules []TagFilterRule) ([]TagFilterRule, error)` | Valida le regole, elimina i duplicati, le rende persistenti e richiama `LoadTagFilters()` per attivarle immediatamente. Restituisce la lista normalizzata. |
| `ApplyTagFiltersToAll() (int, error)` | Scorre tutti i messaggi presenti, applica i filtri correnti in modo additivo — non rimuove mai tag esistenti — e trasmette un evento WebSocket `update` per ogni messaggio modificato, così che l'interfaccia si aggiorni in tempo reale. Restituisce il numero di messaggi aggiornati. |

**Tipo di dato:**
```go
type TagFilterRule struct {
    Match string   `json:"match"` // sintassi di ricerca di Mailpit
    Tags  []string `json:"tags"`  // uno o più nomi di tag
}
```

---

### `internal/storage/tagfilters_file.go`

Realizza il rapporto nei due sensi fra il file `--tags-config` e il database, descritto nel
paragrafo *Il file di configurazione come sorgente delle regole*. Registra l'avvenuta
semina nella chiave di impostazione `TagFiltersSeeded` e mantiene il formato YAML letto dal
prodotto originale.

---

### `server/apiv1/tagfilters.go`

Tre gestori HTTP:

| Gestore | Metodo | Rotta | Descrizione |
|---------|--------|-------|-------------|
| `GetTagFilters` | `GET` | `/api/v1/tag-filters` | Restituisce le regole a runtime correnti come array JSON |
| `SetTagFilters` | `PUT` | `/api/v1/tag-filters` | Accetta `{"Filters": [...]}`, salva e attiva le regole, e **restituisce la lista salvata**, già normalizzata e priva di duplicati |
| `ApplyTagFilters` | `POST` | `/api/v1/tag-filters/apply` | Applica le regole a tutti i messaggi esistenti e restituisce `{"updated": N}` |

Che la `PUT` restituisca le regole salvate è utile a chi allinea la configurazione da
procedura esterna: la risposta è già lo stato effettivo del servizio, e non richiede una
`GET` successiva per essere verificata.

**Esempio di corpo della richiesta `PUT`:**
```json
{
  "Filters": [
    { "match": "subject:invoice", "tags": ["Fattura", "Finance"] },
    { "match": "has:attachment", "tags": ["Allegato"] },
    { "match": "from:esempio.it", "tags": ["Esempio"] }
  ]
}
```

---

## File modificati

### `internal/storage/tagfilters.go`

**Modifica:** `LoadTagFilters()` costruisce i criteri SQL a partire dalle regole a runtime
oltre che da quelle del parametro `--tag`.

Prima:
```go
for _, t := range config.TagFilters {
    // solo le regole del file di configurazione
}
```

Dopo:
```go
allFilters := make([]TagFilterRule, 0, ...)
for _, t := range config.TagFilters { allFilters = append(...) }
for _, t := range GetRuntimeTagFilters() { allFilters = append(...) }
// elaborazione della lista unificata
```

`config.TagFilters` contiene ora esclusivamente le regole indicate con il parametro `--tag`:
quelle del file YAML sono lette in `config.TagsConfigFilters` e seminate nel database, di
modo che ogni regola applicata dal servizio sia anche una regola che l'interfaccia mostra.

Per chi esercisce il servizio il comportamento resta compatibile: un file `--tags-config`
che funzionava prima produce la stessa etichettatura, con le regole ora visibili nella
finestra delle impostazioni.

---

### `server/server.go`

Tre rotte aggiunte in `apiRoutes()`:

```go
r.HandleFunc("GET "  + config.Webroot + "api/v1/tag-filters",       middleWareFunc(apiv1.GetTagFilters))
r.HandleFunc("PUT "  + config.Webroot + "api/v1/tag-filters",       middleWareFunc(apiv1.SetTagFilters))
r.HandleFunc("POST " + config.Webroot + "api/v1/tag-filters/apply", middleWareFunc(apiv1.ApplyTagFilters))
```

---

### `server/ui-src/components/AppSettings.vue`

Aggiunge una nuova scheda **«Tag filters»** alla finestra delle impostazioni.

**Nuove proprietà reattive:**

| Proprietà | Tipo | Scopo |
|----------|------|-------|
| `tagFilters` | `Array` | Elenco degli oggetti `{match, tags}` mostrati nell'interfaccia |
| `tagFiltersLoading` | `Boolean` | Indicatore di caricamento durante il recupero delle regole |
| `tagFiltersSaving` | `Boolean` | Indicatore di salvataggio durante la `PUT` |
| `tagFiltersLoaded` | `Boolean` | Caricamento differito: le regole sono recuperate al primo accesso alla scheda |
| `tagFiltersApplying` | `Boolean` | Indicatore di elaborazione durante la `POST` di applicazione |
| `tagFiltersApplyResult` | `Number\|null` | Numero di messaggi aggiornati, mostrato a conclusione dell'operazione |

**Nuovi metodi:**

| Metodo | Descrizione |
|--------|-------------|
| `loadTagFilters()` | `GET` su `/api/v1/tag-filters`; converte l'array dei tag in stringa separata da virgole per la visualizzazione |
| `addTagFilter()` | Aggiunge una riga di regola vuota |
| `removeTagFilter(index)` | Rimuove la regola indicata, garantendo che resti almeno una riga vuota |
| `saveTagFilters()` | Riconverte lo stato dell'interfaccia nel formato dell'API e salva con `PUT` |
| `applyTagFilters()` | `POST` di applicazione delle regole correnti a tutti i messaggi esistenti |

---

## Uso dall'interfaccia web

### Accesso alle impostazioni

1. Aprire Mailpit nel browser (in configurazione predefinita `http://127.0.0.1:8025`)
2. Selezionare l'icona delle impostazioni (⚙️) in alto a destra
3. Selezionare la scheda **«Tag filters»**

### Creazione delle regole

Ogni regola è composta da due campi:

| Campo | Descrizione | Esempio |
|-------|-------------|---------|
| **Match** | Espressione nella sintassi di ricerca di Mailpit | `subject:invoice` |
| **Tags** | Nomi dei tag da applicare, separati da virgola | `Fattura, Finance` |

Il comando **«Add rule»** aggiunge una riga, **«Remove rule»** la elimina.

### Salvataggio

Il comando **«Save rules»** salva le regole nel database e le rende immediatamente attive
per tutti i **nuovi** messaggi in arrivo via SMTP.

### Applicazione ai messaggi esistenti

Il comando **«Apply to existing messages»**:

- scorre tutti i messaggi già presenti nel database;
- aggiunge i tag corrispondenti, senza mai rimuovere quelli esistenti;
- aggiorna l'interfaccia in tempo reale tramite WebSocket, senza ricaricare la pagina;
- al termine riporta il numero di messaggi aggiornati.

---

## Sintassi del campo Match

Il campo `match` accetta l'intera sintassi di ricerca di Mailpit:

| Espressione | Messaggi selezionati |
|---------|---------|
| `subject:invoice` | L'oggetto contiene «invoice» |
| `from:esempio.it` | Il dominio del mittente è esempio.it |
| `to:ops@esempio.it` | Il destinatario è ops@esempio.it |
| `has:attachment` | Il messaggio ha uno o più allegati |
| `has:inline` | Il messaggio ha immagini incorporate |
| `is:unread` | Messaggi non letti |
| `larger:50kb` | Messaggi di dimensione superiore a 50 KB |
| `"frase esatta"` | Corpo od oggetto contengono la frase esatta |
| `-subject:spam` | L'oggetto NON contiene «spam» |
| `subject:alert from:monitoring` | Entrambe le condizioni devono essere soddisfatte |

---

## Il file di configurazione come sorgente delle regole

Le regole a runtime risiedono nel database, nella tabella `settings`. È ciò che ne consente
la modifica senza riavvio, ma le colloca nell'unica parte della configurazione di
un'istanza che una ricostruzione non recupera, poiché il database dei messaggi non è
deliberatamente oggetto di backup: i messaggi si riproducono rieseguendo un test, mentre una
regola di etichettatura è una decisione presa una volta per progetto e non si rigenera.

Un'istanza ricostruita dalla sola configurazione versionata tornava operativa, accettava i
messaggi e smetteva silenziosamente di etichettarli. Il guasto si manifestava giorni dopo,
alla prima ricerca per tag di progetto.

Il fork tratta pertanto il file `--tags-config` come la rappresentazione versionabile delle
regole a runtime:

| Direzione | Momento | Effetto |
|---|---|---|
| Dal file al database | Una sola volta, al primo avvio di un'istanza che dichiari `--tags-config` | Le regole del file sono importate come regole a runtime, visibili e modificabili dall'interfaccia |
| Dal database al file | A ogni modifica delle regole | Il file è riscritto integralmente e in modo atomico, e riflette quindi sempre le regole attive |

Tre proprietà evitano che l'automatismo produca effetti indesiderati.

- **La semina avviene una volta sola.** L'impostazione `TagFiltersSeeded` ne registra
  l'esecuzione. L'eliminazione di tutte le regole dall'interfaccia è quindi definitiva: il
  riavvio successivo non le ripristina. Dopo il primo avvio il database è la fonte
  autoritativa.
- **Il fallimento della scrittura non fa fallire il salvataggio.** Se il file non è
  scrivibile, la regola è comunque memorizzata, l'errore è registrato nel log e un avviso
  segnala che le regole esistono in quel momento solo nel database. Un problema sul
  filesystem compromette la versionabilità, non la funzionalità.
- **I salvataggi sono serializzati.** La scrittura dell'impostazione, il ricaricamento dei
  criteri e la riscrittura del file avvengono sotto un unico mutex. La sola sostituzione
  atomica del file non era sufficiente: due salvataggi concorrenti potevano raggiungere il
  database in un ordine e il file nell'ordine opposto, lasciando il file a descrivere
  regole diverse da quelle attive.

**Percorso di aggiornamento da un'istanza precedente.** Se all'atto della semina il
database contiene già delle regole — caso di un'istanza aggiornata da una versione
antecedente a questa funzionalità — le regole esistenti sono conservate e dal file sono
aggiunte soltanto quelle il cui criterio `match` non è già presente, confrontato
normalizzato (minuscole, spazi iniziali e finali rimossi). È l'unico caso in cui il file
aggiunge regole a un database già popolato, e avviene comunque una sola volta.

Il file mantiene esattamente il formato letto dal prodotto originale: un file scritto dal
servizio resta quindi un `--tags-config` valido, e un file preparato a mano resta una semina
valida.

### Conseguenze per l'esercizio

Il backup del database dei messaggi resta non necessario. È sufficiente conservare il file
`--tags-config` insieme al resto della configurazione versionata: un'istanza ricostruita
recupera la propria etichettatura senza ulteriori interventi.

Si noti che un percorso `--tags-config` inesistente impedisce l'avvio di Mailpit. È un
comportamento del prodotto originale ed è opportuno mantenerlo: in fase di ricostruzione,
dimenticare di ripristinare il file produce un errore immediato ed evidente, anziché
un'istanza apparentemente sana che accetta la posta senza etichettarla.

---

## Limitazioni note

1. **Le regole si applicano per impostazione predefinita ai soli messaggi successivi.**
   I messaggi già presenti nel database al momento della creazione di una regola non sono
   etichettati automaticamente: occorre l'azione **«Apply to existing messages»**.

2. **Non è previsto un ordinamento o una priorità fra le regole.**
   Tutte le regole corrispondenti vengono applicate: non esiste un criterio di arresto alla
   prima corrispondenza. Un messaggio può ricevere tag da più regole.

3. **L'applicazione retroattiva è esclusivamente additiva.**
   `ApplyTagFiltersToAll()` non rimuove mai tag esistenti dai messaggi. La modifica manuale
   dei tag di un messaggio (`SetMessageTags()`) continua invece a sovrascrivere l'intero
   insieme dei tag.

4. **Le regole non sono rivalutate alla rinomina o all'eliminazione di un tag.**
   Rinominando o eliminando un tag referenziato in una regola, la regola continua a
   contenere il nome precedente e va aggiornata manualmente.

5. **Non è disponibile una funzione di prova della regola.**
   Non è possibile conoscere in anticipo quanti messaggi una regola intercetterebbe: il suo
   effetto va verificato dopo il salvataggio.

6. **Le regole indicate con il parametro `--tag` non sono visibili dall'interfaccia.**
   Restano una fonte separata, gestita esclusivamente lato servizio, come nel prodotto
   originale. Le regole del file YAML non sono invece più interessate da questa
   limitazione: sono importate nel database al primo avvio e da quel momento sono visibili
   e modificabili come tutte le altre (si veda *Il file di configurazione come sorgente
   delle regole*).

7. **L'applicazione retroattiva è sincrona.**
   Su caselle molto popolate (decine di migliaia di messaggi) la richiesta HTTP può
   richiedere alcuni secondi. Non è disponibile un avanzamento più fine dello stato del
   pulsante.

8. **L'errore dell'applicazione retroattiva non è riportato nell'interfaccia.**
   Se la `POST` su `/api/v1/tag-filters/apply` fallisce, il pulsante torna disponibile dopo
   dieci secondi, ma all'utente non è mostrato alcun messaggio di errore.

---

## Esecuzione con database persistente

In configurazione predefinita il contenitore conserva il database in memoria, che viene
quindi perduto al riavvio. Per rendere i dati persistenti occorre montare un volume:

```bash
docker run -d \
  --name mailpit \
  -v mailpit-data:/data \
  -e MP_DATA_FILE=/data/mailpit.db \
  -p 8025:8025 \
  -p 1025:1025 \
  mailpit-local:dev
```

---

## Costruzione dell'immagine container

```bash
# 1. Costruzione dell'immagine
docker build -t mailpit-local:dev .

# 2. Esecuzione
docker run -d --name mailpit-local-dev -p 8025:8025 -p 1025:1025 mailpit-local:dev
```
