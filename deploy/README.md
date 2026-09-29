# Configurazione di riferimento delle istanze IPZS

`mailpit.env` è la configurazione con cui le istanze IPZS di Mailpit sono esercite.
È il documento che il SID cita quando afferma che la configurazione è versionata e
soggetta a change management, e ogni sua voce corrisponde a un'affermazione del SID.

## Perché è versionata

Su Kubernetes questo file diventa la ConfigMap dell'istanza. Il resto dello stato sta
sul volume persistente montato su `/data`: il database dei messaggi e il file delle
regole di etichettatura, che il servizio riscrive a ogni modifica. Il volume è
soggetto a backup giornaliero con retention di sette giorni (SID, T-08): i messaggi
si rigenerano ri-eseguendo un test, le regole no, ed è per loro che il backup esiste.
Questo file e il backup del volume sono quindi **quanto basta a ricostruire
un'istanza**.

## Cosa va valorizzato per ambiente

Le voci marcate `[AMBIENTE]` nel file: percorso del file delle credenziali,
eventuale autenticazione SMTP, etichetta dell'ambiente. Tutto il resto è vincolato
dal SID, compresi i percorsi del database e del file delle regole sotto `/data`.

Il file delle credenziali e quello delle regole **non stanno qui**: il primo è
distribuito tramite Secret, montato in sola lettura; il secondo è scritto dal servizio
stesso a ogni modifica delle regole e sta sul volume persistente, non in una ConfigMap.

Il file delle regole deve esistere all'avvio, anche privo di regole: se manca, il
servizio non parte. Su un volume nuovo lo crea quindi un initContainer eseguito prima
del servizio, con la stessa immagine:

    test -f /data/tag-filters.yaml || printf 'filters: []\n' > /data/tag-filters.yaml

Il database non ha questo vincolo: se manca, il servizio lo crea.

## Verifica

    python deploy/verifica-configurazione.py --mailpit ./mailpit

Avvia un'istanza con questa configurazione e osserva il servizio in esecuzione,
un'affermazione del SID alla volta: autenticazione dell'interfaccia e dell'API di
invio, assenza dell'azione di rilascio, assenza dell'inoltro automatico, direttive
della Content Security Policy su stili e caratteri remoti, cattura di un messaggio
verso un dominio esterno, etichettatura dalle regole del file, assenza del servizio
POP3, assenza dell'oggetto dei messaggi nei log, eliminazione automatica per
numerosità.

Due casi non verificano un controllo ma un **limite**, e riescono quando il limite
c'è: le immagini remote di un messaggio restano consentite dalla politica di
sicurezza, e il rilascio si attiva con il solo `MP_SMTP_RELAY_HOST`, senza alcun
file di configurazione. Sono i due motivi per cui il file di riferimento dichiara
vuote anche le variabili del rilascio e per cui il SID attribuisce alla postazione,
e non al pod, il traffico generato all'apertura di un messaggio. Se smettessero di
riuscire, il prodotto sarebbe cambiato e le due affermazioni andrebbero rilette.

Non verifica che il file contenga certe righe — quello sarebbe verificare sé stessi.
Le uniche due voci dichiarate e non osservate sono le soglie di conservazione ai
valori di esercizio, che agirebbero a 100.000 messaggi e dopo sette giorni; il
meccanismo che governano è verificato su un'istanza dedicata con soglia ridotta.

L'esecuzione richiede circa un minuto e mezzo, perché l'eliminazione automatica è
eseguita dal prodotto su un ciclo di 60 secondi.
