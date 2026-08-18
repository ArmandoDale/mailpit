# Configurazione di riferimento delle istanze IPZS

`mailpit.env` è la configurazione con cui le istanze IPZS di Mailpit sono esercite.
È il documento che il SID cita quando afferma che la configurazione è versionata e
soggetta a change management, e ogni sua voce corrisponde a un'affermazione del SID.

## Perché è versionata

Il database dei messaggi non è oggetto di ripristino: i messaggi si rigenerano
ri-eseguendo un test. Questo file, insieme al file delle regole di etichettatura che
esso dichiara, è quindi **quanto basta a ricostruire un'istanza**.

## Cosa va valorizzato per ambiente

Le voci marcate `[AMBIENTE]` nel file: percorso del file delle credenziali, percorso
del file delle regole, percorso del database, etichetta dell'ambiente. Tutto il resto
è vincolato dal SID.

Il file delle credenziali e quello delle regole **non stanno qui**: il primo è
distribuito tramite Secret, il secondo è scritto dal servizio stesso a ogni modifica
delle regole e va conservato accanto a questo file.

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
e non alla VM, il traffico generato all'apertura di un messaggio. Se smettessero di
riuscire, il prodotto sarebbe cambiato e le due affermazioni andrebbero rilette.

Non verifica che il file contenga certe righe — quello sarebbe verificare sé stessi.
Le uniche due voci dichiarate e non osservate sono le soglie di conservazione ai
valori di esercizio, che agirebbero a 100.000 messaggi e dopo sette giorni; il
meccanismo che governano è verificato su un'istanza dedicata con soglia ridotta.

L'esecuzione richiede circa un minuto e mezzo, perché l'eliminazione automatica è
eseguita dal prodotto su un ciclo di 60 secondi.
