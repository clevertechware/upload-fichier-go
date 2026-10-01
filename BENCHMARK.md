# Mesures — articles 1 et 4

Deux façons complémentaires de mesurer les trois approches (`ReadAll`, `FormFile`/`ParseMultipartForm`, `MultipartReader` en flux) :

- `go test -bench` (`internal/upload/bench_test.go`) : allocations par requête (`b.ReportAllocs`) et débit (`b.SetBytes`), sur 1 MiB / 64 MiB / 256 MiB.
- `cmd/memprofile` : pic de heap observé par échantillonnage (`runtime.ReadMemStats`), fichiers temporaires créés, débit — sur une seule requête, à la taille passée en `-size`.

## Environnement

- `go version go1.27.1 darwin/arm64`
- macOS Darwin 25.6.0 (arm64)
- CPU : Apple M5 Max (`cpu: Apple M5 Max` rapporté par `go test -bench`)

## `go test -bench` — sortie brute

```
$ go test ./internal/upload/ -run '^$' -bench . -benchtime=3x -timeout 600s

goos: darwin
goarch: arm64
pkg: upload-fichier-go/internal/upload
cpu: Apple M5 Max
BenchmarkReadAllHandler/1MiB-18                	       3	   1973111 ns/op	 531.43 MB/s	 2331994 B/op	     112 allocs/op
BenchmarkReadAllHandler/64MiB-18               	       3	  78357069 ns/op	 856.45 MB/s	165452898 B/op	     131 allocs/op
BenchmarkReadAllHandler/256MiB-18              	       3	 292141542 ns/op	 918.85 MB/s	599930229 B/op	     141 allocs/op
BenchmarkFormFileHandler/1MiB-18               	       3	    934083 ns/op	1122.57 MB/s	 4291250 B/op	     118 allocs/op
BenchmarkFormFileHandler/64MiB-18              	       3	  90857195 ns/op	 738.62 MB/s	134345704 B/op	     136 allocs/op
BenchmarkFormFileHandler/256MiB-18             	       3	 419205667 ns/op	 640.34 MB/s	134348226 B/op	     137 allocs/op
BenchmarkMultipartReaderHandler/1MiB-18        	       3	   1736986 ns/op	 603.68 MB/s	   87000 B/op	      83 allocs/op
BenchmarkMultipartReaderHandler/64MiB-18       	       3	  99933889 ns/op	 671.53 MB/s	   86493 B/op	      78 allocs/op
BenchmarkMultipartReaderHandler/256MiB-18      	       3	 370797930 ns/op	 723.94 MB/s	   90221 B/op	      87 allocs/op
PASS
ok  	upload-fichier-go/internal/upload	5.657s
```

`B/op` (bytes alloués par requête) est la colonne qui compte ici : `ReadAllHandler` grossit avec le fichier (2.3 Mo → 165 Mo → 600 Mo pour 1/64/256 MiB) ; `FormFileHandler` plafonne autour de 134 Mo dès que le fichier dépasse le seuil `maxMemory` de 32 Mo (le débordement part sur disque, pas en mémoire) ; `MultipartReaderHandler` reste sous 100 Ko quelle que soit la taille du fichier.

## `cmd/memprofile` — sortie brute, 1 Go

La plus grande taille praticable dans cet environnement a été testée jusqu'au bout : 1 Go exactement (`-size=1073741824`), sans échantillon plus grand pour ne pas saturer la machine de développement avec `io.ReadAll`, qui accumule tout le corps de la requête dans un buffer avant que le multipart reader n'en fasse une seconde copie lors du parsing.

```
$ go run ./cmd/memprofile -size=1073741824 -sample-every=10ms

Taille testée : 1073741824 octets (1024.0 MiB)

| Approche | Pic mémoire (heap) | Fichiers temporaires | Débit |
|---|---|---|---|
| `io.ReadAll` | 2625.1 MiB (delta 2624.1 MiB) | Non | 681.4 MiB/s |
| `ParseMultipartForm / FormFile` | 97.8 MiB (delta 96.6 MiB) | Oui (1) | 753.1 MiB/s |
| `MultipartReader en flux` | 1.8 MiB (delta 0.5 MiB) | Non | 926.2 MiB/s |
```

## Tableau à coller dans l'article 1

| Approche | Pic mémoire (heap) | Fichiers temporaires | Débit |
|---|---|---|---|
| `io.ReadAll` | ~2,6 Go pour un fichier de 1 Go | Non | 681 Mo/s |
| `ParseMultipartForm` / `FormFile` | ~98 Mo (plafonné par `maxMemory`) | Oui (1 fichier temporaire) | 753 Mo/s |
| `MultipartReader` en flux | ~2 Mo, indépendant de la taille du fichier | Non | 926 Mo/s |

*(mesuré sur un upload de 1 Go, macOS arm64, Apple M5 Max, go1.27.1 ; voir la sortie brute ci-dessus pour la méthode.)*

## Interprétation

Sur ce fichier de 1 Go, `io.ReadAll` consomme environ 2,6 fois la taille du fichier en pic de heap (le buffer qui accumule le corps de la requête, plus la copie que `multipart.NewReader` en refait lors du parsing), là où `MultipartReader` en flux reste sous 2 Mo, confirmant l'ordre attendu par l'article. `FormFile` bascule bien sur un fichier temporaire disque une fois le seuil `maxMemory` (32 Mo) dépassé et son pic mémoire (~98 Mo) ne dépend plus de la taille de l'upload. Le débit, en revanche, ne départage pas nettement les approches : sur les benchmarks à 256 MiB, `ReadAllHandler` (919 Mo/s) fait mieux que `MultipartReaderHandler` (724 Mo/s), alors que c'est l'inverse sur la mesure à 1 Go — l'écart tient plus au bruit de mesure qu'à une différence structurelle entre les trois approches.

## Mesures — article 4 (flux vers S3)

Même méthode que l'article 1 (`cmd/memprofile`), avec deux ajouts :

- Le corps multipart généré (`s3MultipartBody` dans `cmd/memprofile/s3discard.go`) commence par la signature PNG (8 octets), pour que le sniffing de `NewS3PipelineHandler` accepte le flux ; le reste du contenu est le même motif déterministe que l'article 1.
- L'approche « Flux vers S3 » n'utilise pas de faux serveur S3 en mémoire dans le même processus (ça aurait faussé la mesure de heap, en gardant les octets uploadés). Elle passe un `discardS3Client` — une implémentation directe de l'interface `S3APIClient` du transfermanager, sans aucune requête HTTP, qui fait `io.Copy(io.Discard, ...)` sur chaque corps de part avant de renvoyer une réponse minimale valide. Seule la mémoire côté client (pipeline HTTP du handler + pool de buffers du transfermanager) est mesurée. Ce client court-circuite toute la pile du SDK S3 (signature SigV4, calcul CRC32, transport HTTP, TLS), dont le coût mémoire n'est donc pas compris dans ces chiffres.
- Réglages du transfermanager dans cette mesure, identiques à ceux recommandés dans l'article : `PartSizeBytes` 5 Mio, `MultipartUploadThreshold` 5 Mio, `Concurrency` 2, `FailTimeout` 30 s. Borne théorique : `Threshold + (Concurrency+1) × PartSize` = 5 + 3 × 5 = **20 Mio par upload**.

### Environnement

Identique à la mesure de l'article 1 : `go version go1.27.1 darwin/arm64`, macOS Darwin 25.6.0 (arm64), Apple M5 Max.

### `cmd/memprofile` — sortie brute, 100 Mio

```
$ go run ./cmd/memprofile -size=104857600 -only="FormFile,S3" -sample-every=2ms

Taille testée : 104857600 octets (100.0 MiB)

| Approche | Pic mémoire (heap) | Fichiers temporaires | Débit |
|---|---|---|---|
| `ParseMultipartForm / FormFile` | 113.7 MiB (delta 112.1 MiB) | Oui (1) | 524.4 MiB/s |
| `Flux vers S3 (transfermanager)` | 21.7 MiB (delta 20.1 MiB) | Non | 1205.7 MiB/s |
```

### `cmd/memprofile` — sortie brute, 1 Gio

```
$ go run ./cmd/memprofile -size=1073741824 -only="FormFile,S3" -sample-every=10ms

Taille testée : 1073741824 octets (1024.0 MiB)

| Approche | Pic mémoire (heap) | Fichiers temporaires | Débit |
|---|---|---|---|
| `ParseMultipartForm / FormFile` | 113.5 MiB (delta 112.2 MiB) | Oui (1) | 707.3 MiB/s |
| `Flux vers S3 (transfermanager)` | 22.0 MiB (delta 20.5 MiB) | Non | 1360.9 MiB/s |
```

### Tableau à coller dans l'article 4

| Approche | Pic de heap | Fichiers `multipart-*` |
|---|---|---|
| FormFile + écriture disque | 98 à 114 Mio (indépendant de la taille au-delà de `maxMemory`) | 1 |
| Flux vers S3 (transfermanager) | ~22 Mio (indépendant de la taille du fichier) | 0 |

*(mesuré à 100 Mio et 1 Gio, macOS arm64, Apple M5 Max, go1.27.1, réglages `PartSizeBytes`/`MultipartUploadThreshold` 5 Mio, `Concurrency` 2 ; voir la sortie brute ci-dessus.)*

### Interprétation

Le pic de heap de `FormFile` est bimodal d'un run à l'autre : 97,6 à 98,1 Mio ou 113,5 Mio selon le moment où le GC passe (5 runs de 100 Mio isolés : 113,5 / 97,6 / 97,6 / 97,6 / 97,7 Mio ; 1 Gio isolé : 98,1 Mio), soit un écart de ~16 Mio, une taille de buffer. Les runs 100 Mio et 1 Gio du tableau brut ci-dessus (113,7 et 113,5 Mio) sont donc dans la fourchette haute ; le chiffre de l'article 1 (97,8 Mio) dans la basse. Le pic du flux S3, lui, est stable (21,6 à 22,0 Mio sur tous les runs).

Le pic de heap du flux S3 reste quasi identique entre 100 Mio (21,7 Mio) et 1 Gio (22,0 Mio). Une fois retiré le heap de départ du processus (~1,5 Mio), les deltas mesurés sont de 20,1 Mio (100 Mio) et 20,5 Mio (1 Gio), soit pile sur la borne théorique `Threshold + (Concurrency+1) × PartSize` = 20 Mio : la mémoire est plafonnée par les réglages du transfermanager, pas par la taille du fichier. `FormFile` crée bien un fichier temporaire `multipart-*` dès que le seuil `maxMemory` (32 Mio) est dépassé, quelle que soit la taille testée, alors que le flux S3 n'en crée jamais : la preuve « zéro fichier créé » tient aux deux tailles. Réserve : `discardS3Client` court-circuite toute la pile du SDK S3 (SigV4, CRC32, transport, TLS) et ne fait aucun aller-retour réseau ; un test contre un vrai S3 (ou MinIO) changerait peu ce pic de heap, qui est presque entièrement déterminé côté client avant l'envoi de chaque part, mais il ajouterait le coût mémoire du SDK et du transport, absent ici.
