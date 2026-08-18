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

### Fase B — il file di configurazione come sorgente delle regole

| # | Caso | Risultato atteso | Verifica |
|---|---|---|---|
| PT-14 | Istanza nuova avviata con `--tags-config` | Le regole del file compaiono tra quelle gestite a runtime | Semina del database dal file |
| PT-15 | Messaggio corrispondente a una regola seminata | Il messaggio è etichettato | La sorgente da file resta efficace |
| PT-16 | Regola creata dal pannello | Il file di configurazione contiene entrambe le regole | Scrittura passante |
| PT-17 | Riavvio sullo stesso database | Le regole restano due | La semina non si ripete |

### Fase C — ricostruzione dell'istanza dal solo file

| # | Caso | Risultato atteso | Verifica |
|---|---|---|---|
| PT-18 | Database nuovo, file di configurazione invariato | Entrambe le regole sono presenti | Ripristino del tagging senza backup del database |
| PT-19 | Messaggio corrispondente alla regola creata a suo tempo dal pannello | Il messaggio è etichettato | La regola sopravvive alla perdita del database |

### Fase D — casi limite

| # | Caso | Risultato atteso | Verifica |
|---|---|---|---|
| PT-20 | Eliminazione di tutte le regole, poi riavvio | Le regole restano zero | L'eliminazione non è annullata da una nuova semina |
| PT-21 | File non scrivibile al momento del salvataggio | La regola è comunque salvata | Il guasto sul file non rende inutilizzabile la funzionalità |
| PT-22 | Messaggio in arrivo con file non aggiornato | Il messaggio è etichettato | Il servizio resta operativo, la sola copia versionabile è disallineata |

**Perché la Fase C conta.** È il caso che giustifica l'intera evoluzione. Le
regole di tagging sono l'unica informazione del database che non si rigenera
ri-eseguendo un test: sono una decisione presa una volta per progetto. Con il
database escluso dal ripristino, un'istanza ricostruita ripartiva sana e smetteva
silenziosamente di etichettare, e il sintomo si manifestava giorni dopo, alla
prima ricerca per tag. PT-18 e PT-19 provano che oggi non accade più.

**Perché PT-20 e PT-21 contano.** Sono i due modi in cui una semina automatica
può fare danno. PT-20 esclude che il file resusciti regole che un amministratore
ha deliberatamente eliminato: la semina avviene una volta sola, e il database
resta da quel momento la fonte autorevole. PT-21 esclude che un problema di
filesystem si trasformi in un'indisponibilità della funzionalità: il salvataggio
riesce, l'errore è registrato nel log, e ciò che resta disallineata è la copia
versionabile — condizione da correggere, non da subire in esercizio.

### Fase E — fedeltà del formato e percorsi di aggiornamento

| # | Caso | Risultato atteso | Verifica |
|---|---|---|---|
| PT-23 | Regole con più tag, frase tra virgolette e negazione, riprese da un database nuovo | Identiche a quelle salvate | Fedeltà del round-trip attraverso il file |
| PT-24 | Messaggio corrispondente a una regola con tre tag, dopo il round-trip | Tutti i tag sono applicati | I tag multipli non si perdono nel formato a lista separata da virgole |
| PT-25 | Messaggio escluso da una negazione nel criterio, dopo il round-trip | Nessun tag | La sintassi articolata sopravvive alla riscrittura |
| PT-26 | Istanza con regole già nel database che adotta un file di configurazione | Le regole preesistenti restano, quelle del file si aggiungono | Percorso di aggiornamento di un'istanza in esercizio |
| PT-27 | Stessa istanza, regola già presente anche nel file | Nessuna duplicazione | Il confronto avviene sul criterio di selezione |
| PT-28 | Riavvio dopo l'adozione | Il file contiene entrambe le regole | Le regole preesistenti diventano versionabili |
| PT-29 | File di configurazione dichiarato ma assente | Il servizio non si avvia | Il ripristino incompleto fallisce in modo evidente |

**Perché PT-26 conta.** È il percorso che ogni istanza già in esercizio compie al
momento dell'aggiornamento: le sue regole sono nel database, il file di
configurazione viene introdotto solo ora. La semina non deve sovrascriverle né
duplicarle, ma assorbire le une nelle altre e rendere versionabili anche quelle
che esistevano prima.

**Perché PT-29 conta.** Un file dichiarato e non presente ferma l'avvio invece di
lasciar partire un servizio che accetta posta senza etichettarla. È il
comportamento voluto: in un ripristino, dimenticare il file diventa un errore
immediato e visibile invece di un guasto silenzioso che si manifesta giorni dopo.

### Fase F — concorrenza e permessi sul file

| # | Caso | Risultato atteso | Verifica |
|---|---|---|---|
| PT-30 | Dodici salvataggi simultanei | Nessun errore restituito | Il salvataggio regge l'accesso concorrente |
| PT-31 | Stato del database al termine | Una sola regola, coerente | L'ultimo salvataggio prevale in modo pulito |
| PT-32 | Stato del file al termine | Una sola regola, file leggibile | La scrittura atomica esclude file troncati o interlacciati |
| PT-33 | Confronto fra database e file | Coincidono | Il file descrive le regole realmente attive |
| PT-34 | File presente ma di sola lettura | La regola è comunque salvata | Il permesso negato non blocca la funzionalità |
| PT-35 | Messaggio in arrivo con file di sola lettura | Il messaggio è etichettato | Il servizio resta operativo |

**PT-33 ha trovato un difetto reale.** Alla prima stesura il caso falliva in
quattro esecuzioni su sei. Due salvataggi simultanei potevano raggiungere il
database in un ordine e il file nell'ordine opposto: il database conteneva le
regole di uno, il file quelle dell'altro. La scrittura atomica non bastava,
perché proteggeva il singolo file e non la sequenza delle due scritture. Il
salvataggio è stato quindi serializzato per intero, e il caso passa ora in otto
esecuzioni consecutive.

Vale la pena notare **perché** il difetto era grave malgrado la sua rarità: la
divergenza era silenziosa e si sarebbe manifestata solo alla ricostruzione di
un'istanza, restituendo regole diverse da quelle attive. È esattamente la classe
di guasto che questa evoluzione doveva eliminare.

## Esito dell'esecuzione

| Voce | Valore |
|---|---|
| Data | 18/08/2026 |
| Baseline | ramo `develop`, tag `1.1.2` |
| Ambiente | binario compilato dalla baseline, database SQLite locale, istanza isolata su porte dedicate |
| Esito | **35 casi su 35 superati**, confermati in otto esecuzioni consecutive |

Nessun caso fallito e nessuno scostamento rispetto ai comportamenti e alle
limitazioni dichiarati in `FORK_CHANGES.md`.

Una precedente esecuzione di questo piano, il 18/08/2026 sulla baseline `1.0.0`,
aveva superato 16 casi su 16. In quella versione le regole da file costituivano
una seconda sorgente non visibile nel pannello, e il piano lo verificava come
limitazione nota; la sezione «Cosa il piano non copre» ne dà conto.

## Cosa il piano non copre

- **Prestazioni dell'applicazione retroattiva su caselle molto popolate.** Il
  piano prova che l'operazione è corretta, non quanto dura. L'operazione è
  sincrona: su archivi grandi va eseguita fuori dai momenti di picco.
- **Interfaccia grafica.** I casi agiscono sulle API, che sono lo stesso percorso
  usato dal pannello. La verifica visiva del pannello si fa con
  `send-samples.py` e l'interfaccia web.
- **Concorrenza oltre il salvataggio delle regole.** La Fase F prova i
  salvataggi simultanei. Restano non provati gli accessi concorrenti al resto del
  servizio, per i quali valgono le garanzie del prodotto originale.
- **Race detector.** La verifica della concorrenza è a scatola chiusa, ripetuta.
  L'esecuzione della suite Go con `-race` richiede un compilatore C non presente
  sulla macchina di prova e va condotta sull'ambiente di build.
- **Regole definite con `--tag` da riga di comando.** Restano una sorgente
  aggiuntiva non visibile nel pannello, invariata rispetto al prodotto originale.
  Sono pensate per l'uso locale e non sono impiegate nelle istanze IPZS.
