# Piano di prova — regole di tagging a runtime

Verifica funzionale della personalizzazione IPZS descritta in `FORK_CHANGES.md`.

Il piano copre due cose: i **comportamenti dichiarati** della funzionalità e le
**limitazioni note** elencate in `FORK_CHANGES.md`. Le limitazioni sono incluse
deliberatamente: una limitazione dichiarata e non provata resta un'affermazione,
una limitazione provata è un comportamento accertato. Diversi casi verificano
quindi che il prodotto faccia *proprio quello che il documento dice che non fa*.

## Come si esegue

```
go build -o mailpit .
python dev/testdata/prova-tagfilters.py --mailpit ./mailpit
```

Lo script avvia e ferma Mailpit da solo, su un database temporaneo e sulle porte
18025/11025, per non interferire con istanze già in esecuzione. Stampa un esito
per ciascun caso e termina con codice diverso da zero se anche uno solo fallisce.

Per una verifica manuale dall'interfaccia è disponibile `dev/testdata/send-samples.py`,
che popola la casella con un corpus in cui ogni regola ha sia messaggi che deve
intercettare sia messaggi che non deve intercettare.

## Casi di prova

### Fase A — regole gestite a runtime

| # | Caso | Risultato atteso | Verifica |
|---|---|---|---|
| PT-01 | Istanza nuova, nessuna regola salvata | `GET /api/v1/tag-filters` restituisce un elenco vuoto | Stato iniziale pulito |
| PT-02 | Messaggi ricevuti in assenza di regole | Nessun messaggio riceve tag | Il tagging non avviene per altre vie |
| PT-03 | Salvataggio di due regole e rilettura | Le regole rilette sono identiche a quelle salvate | Gestione via API (versionamento) |
| PT-04 | Messaggi già presenti al momento del salvataggio | Restano privi di tag | Limitazione nota 1 |
| PT-05 | Messaggio ricevuto dopo il salvataggio, senza riavvio | Riceve il tag della regola | Attivazione immediata |
| PT-06 | Messaggio che soddisfa due regole | Riceve i tag di entrambe | Limitazione nota 2 (nessuna priorità) |
| PT-07 | Messaggio che non soddisfa alcuna regola | Resta privo di tag | Selettività delle regole |
| PT-08 | Applicazione retroattiva | Restituisce il numero di messaggi aggiornati | Conteggio come riscontro dell'operazione |
| PT-09 | Applicazione retroattiva sui preesistenti | I messaggi corrispondenti risultano etichettati | Recupero dei messaggi di PT-04 |
| PT-10 | Applicazione retroattiva su un messaggio con tag manuale | Il tag manuale non viene rimosso | Additività (limitazione nota 3) |
| PT-11 | Rinomina di un tag citato da una regola | La regola continua a citare il nome vecchio | Limitazione nota 4 |
| PT-12 | Messaggio ricevuto dopo la rinomina | Il tag con il nome vecchio viene ricreato | Effetto operativo di PT-11 |
| PT-13 | Riavvio del servizio | Le regole salvate sono ancora presenti | Persistenza nel database |

### Fase B — regole definite da file di configurazione

| # | Caso | Risultato atteso | Verifica |
|---|---|---|---|
| PT-14 | Istanza avviata con `--tags-config` | I messaggi corrispondenti sono etichettati | La sorgente da file resta attiva |
| PT-15 | Elenco delle regole gestite a runtime | La regola da file **non** compare | Limitazione nota 6 |
| PT-16 | Regola da file e regola a runtime sullo stesso messaggio | Il messaggio riceve entrambi i tag | Coesistenza delle due sorgenti |

**Perché PT-15 conta.** È il caso meno intuitivo del piano: l'amministratore che
apre il pannello vede solo una parte delle regole attive, ma l'azione di
applicazione retroattiva agisce sull'insieme completo. Non è un problema di
sicurezza — il file di configurazione lo scrive chi gestisce il deployment — ma
di diagnosi: senza saperlo, non si spiega da dove arrivi un tag. È la ragione per
cui si raccomanda di adottare l'interfaccia come fonte unica delle regole attive.

## Esito dell'esecuzione

| Voce | Valore |
|---|---|
| Data | 18/08/2026 |
| Baseline | ramo `develop`, commit `07fad2b`, tag `1.0.0` |
| Ambiente | binario compilato dalla baseline, database SQLite locale, istanza isolata su porte dedicate |
| Esito | **16 casi su 16 superati** |

Nessun caso fallito e nessuno scostamento rispetto ai comportamenti e alle
limitazioni dichiarati in `FORK_CHANGES.md`.

## Cosa il piano non copre

- **Prestazioni dell'applicazione retroattiva su caselle molto popolate.** Il
  piano prova che l'operazione è corretta, non quanto dura. L'operazione è
  sincrona: su archivi grandi va eseguita fuori dai momenti di picco.
- **Interfaccia grafica.** I casi agiscono sulle API, che sono lo stesso percorso
  usato dal pannello. La verifica visiva del pannello si fa con
  `send-samples.py` e l'interfaccia web.
- **Concorrenza.** Le prove sono a utente singolo.
