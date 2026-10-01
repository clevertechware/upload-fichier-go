# upload-fichier-go

Dépôt d'exemple commun aux quatre articles de blog sur l'upload de fichier en
Go : recevoir un flux sans exploser la mémoire, valider son contenu, suivre sa
progression et l'annuler proprement, puis le streamer vers S3 sans jamais le
poser sur disque. Go ≥ 1.24 (pour `os.Root`).

Jusqu'à l'article 3, le dépôt était bibliothèque standard uniquement.
L'article 4 introduit ses premières dépendances externes : le SDK AWS pour Go
(`aws-sdk-go-v2`, `feature/s3/transfermanager`), et `gofakes3` pour les tests.
Voir [Dépendances externes](#dépendances-externes-article-4) plus bas.

## Prérequis

- Go 1.24 ou plus récent (`go version`)

## Commandes

```bash
go build ./...                                  # compile tout le module
go vet ./...                                    # analyse statique
go test ./...                                   # tests unitaires et d'intégration
go test -race ./...                             # idem, avec le détecteur de races

go test ./internal/upload/ -bench . -run '^$'   # benchmarks mémoire de l'article 1
go run ./cmd/memprofile -size=1073741824        # pic de heap / fichiers temporaires / débit, un upload de 1 Go
go run ./cmd/memprofile -size=1073741824 -only="FormFile,S3"  # ne comparer que FormFile et le flux S3 (article 4)
go run ./cmd/server                             # serveur d'exemple sur :8080
```

Les résultats bruts des benchmarks et du memprofile sont dans [`BENCHMARK.md`](./BENCHMARK.md).

## Structure

```
client/               client par morceaux (article 3) : UploadInChunks, sendChunk
cmd/server/            serveur HTTP d'exemple : POST /upload (pipeline suivi), PUT /chunks
cmd/memprofile/        mesure comparative des approches de réception (articles 1 et 4)
deploy/                règle de cycle de vie S3 AbortIncompleteMultipartUpload (article 4)
internal/chunkupload/  handler de démo qui réassemble les morceaux envoyés par client/ (ne pas exposer)
internal/genfile/      génère des corps multipart de test en flux, sans les charger en mémoire
internal/upload/       les handlers de réception, le pipeline (validation, version suivie, S3), sniffing, hash, stockage
streamio/              TrackedReader : progression throttlée + annulation par contexte (article 3)
```

## Correspondance article / code

| Article | Sujet | Fichiers |
|---|---|---|
| 1 — Uploader un fichier en Go sans exploser la mémoire | `ReadAll` vs `FormFile` vs `MultipartReader`, `MaxBytesReader` + 413, benchmark mémoire | `internal/upload/handler.go`, `internal/upload/bench_test.go`, `cmd/memprofile/main.go`, `internal/genfile/genfile.go` |
| 2 — Empiler des io.Reader pour valider un upload | `Peek(512)` + `DetectContentType` + liste blanche, `TeeReader` sha256, nom généré côté serveur à partir du type détecté, `os.Root`, nettoyage du fichier partiel | `internal/upload/sniff.go`, `internal/upload/hash.go`, `internal/upload/store.go`, `internal/upload/pipeline.go` (`NewValidatingHandler`) |
| 3 — Un io.Reader maison : progression et annulation | `TrackedReader` throttlé à 200 ms, annulation de contexte, branchement serveur, client par morceaux de 5 Mo avec `io.ReadFull` | `streamio/tracked_reader.go`, `internal/upload/pipeline.go` (`NewTrackedPipelineHandler`), `client/upload.go`, `internal/chunkupload/handler.go` |
| 4 — Streamer un upload Go vers S3 sans toucher le disque | Même validation que l'article 2/3, destination `transfermanager.UploadObject` au lieu d'un fichier confiné, abort multipart sur 413 et sur annulation de contexte, sémaphore de concurrence, mesure du pic de heap contre la borne `Threshold + (Concurrency+1) × PartSize` | `internal/upload/s3.go` (`NewS3PipelineHandler`), `internal/upload/s3_test.go`, `cmd/memprofile/s3discard.go`, `deploy/lifecycle-abort-incomplete-mpu.json` |

`internal/upload/pipeline.go` sépare volontairement les deux premiers articles : `NewValidatingHandler` est le pipeline complet de l'article 2 (aucune trace de `TrackedReader`), et `NewTrackedPipelineHandler` y ajoute la progression de l'article 3 en enveloppant le même reader, sans dupliquer la validation. `cmd/server` fait tourner la version suivie (`NewTrackedPipelineHandler`), qui est un sur-ensemble strict de la version validante. **`pipeline.go` n'a pas été modifié par l'article 4** : `NewS3PipelineHandler` (`internal/upload/s3.go`) réutilise les mêmes fonctions partagées (`nextFilePart`, `SniffType`, `ValidateType`, `GenerateStoredName`, `NewHashingReader`) sans toucher au fichier que les articles 2 et 3 citent.

## Dépendances externes (article 4)

- [`github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager`](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager) `v0.4.11` — successeur de `feature/s3/manager` (déprécié depuis février 2026), encore en pré-1.0 : version épinglée dans `go.mod`, à surveiller à chaque mise à jour.
- [`github.com/aws/aws-sdk-go-v2/service/s3`](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/service/s3) — client S3 requis par `transfermanager`.
- [`github.com/johannesboyne/gofakes3`](https://github.com/johannesboyne/gofakes3) `v1.2.0` — faux serveur S3 en mémoire, utilisé uniquement dans les tests (`internal/upload/s3_test.go`).

### Faux S3 pour les tests : cloudmock vs gofakes3

Deux options ont été évaluées pour tester `NewS3PipelineHandler` sans dépendre d'un vrai compte AWS ni de Docker :

- **[cloudmock](https://github.com/Viridian-Inc/cloudmock)** (`v1.10.0`, licence MIT, 12 étoiles, 663 commits) — un émulateur multi-cloud qui prétend couvrir une centaine de services AWS, dont S3 avec un support multipart réel (`services/s3/multipart.go` implémente `CreateMultipartUpload`/`UploadPart`/`CompleteMultipartUpload`/`AbortMultipartUpload`). Écarté malgré ce support : c'est un module bien plus gros que ce dont cet exemple a besoin (des binaires embarqués pour Terraform, Pulumi et un serveur DNS pèsent à eux seuls plus de 19 Mio dans le module Go), la gestion des trailers de checksum `aws-chunked` du SDK n'y est pas visible dans le code source, et la popularité/maintenance du projet (12 étoiles) est trop incertaine pour un dépôt d'exemple destiné à être copié. Ajouter cette dépendance pour tester une seule fonctionnalité (upload multipart S3) n'aurait pas été proportionné.
- **[gofakes3](https://github.com/johannesboyne/gofakes3)** (`v1.2.0`, backend mémoire `s3mem`, `httptest.NewServer`) — retenu. Il ne décode que l'encodage de corps `STREAMING-AWS4-HMAC-SHA256-PAYLOAD`, pas `STREAMING-UNSIGNED-PAYLOAD-TRAILER` (celui utilisé quand le SDK ajoute un checksum CRC32 en trailer par défaut depuis 2025). Les tests configurent donc le client S3 avec `RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired`, qui désactive ce trailer pour les opérations qui ne l'exigent pas (voir `internal/upload/s3_test.go`, fonction `newFakeS3`).

Pour la mesure de mémoire (`cmd/memprofile`), ni cloudmock ni gofakes3 ne conviennent : un faux S3 en mémoire dans le même processus retiendrait les octets uploadés et fausserait le pic de heap mesuré. `cmd/memprofile/s3discard.go` implémente donc directement l'interface `S3APIClient` du transfermanager avec un `discardS3Client` qui appelle `io.Copy(io.Discard, ...)` sur chaque corps de part sans jamais les conserver ni passer par le réseau — voir [`BENCHMARK.md`](./BENCHMARK.md) pour la méthode complète et les résultats.

## Ce que montrent les tests

- Les trois handlers de réception de l'article 1 stockent bien le fichier reçu, et `MaxBytesReader` renvoie 413 au-delà de la limite configurée.
- Le pipeline de l'article 2 (`NewValidatingHandler`) : un PNG envoyé sous le nom `../../evil.png` est stocké sous un nom généré côté serveur, avec les octets et le sha256 intacts ; le même PNG envoyé sous le nom `evil.html` est quand même stocké avec l'extension `.png`, dérivée du type détecté et non du nom client ; un type hors liste blanche est rejeté en 415 sans laisser de fichier ; un champ texte arrivé avant le champ fichier est ignoré au lieu d'être pris pour le fichier ; un upload coupé par la limite de taille supprime le fichier partiel ; `os.Root` refuse toute tentative d'évasion du dossier de destination.
- Le pipeline suivi de l'article 3 (`NewTrackedPipelineHandler`) stocke correctement un fichier, en plus du comportement hérité de `NewValidatingHandler`.
- `TrackedReader` (article 3) : le nombre d'appels au callback de progression reste borné par le throttling à 200 ms, un dernier appel est garanti à EOF (vérifié par mutation : retirer cette branche fait échouer le test), et l'annulation du contexte est vue avant toute lecture supplémentaire.
- Le client par morceaux (article 3) réassemble correctement un fichier envoyé avec un reader à lectures courtes (`iotest.OneByteReader`, `iotest.HalfReader`), y compris sur le dernier morceau partiel ; un serveur qui répond 500 remonte une erreur mentionnant `server rejected chunk 0` (vérifié par mutation).
- `internal/chunkupload` (démo pour les tests du client, à ne pas exposer) rejette un morceau de plus de 5 Mio et accepte un morceau de 5 Mio pile.
- Le pipeline S3 de l'article 4 (`NewS3PipelineHandler`) : un PNG envoyé stocke un objet avec la clé générée, les octets, le `ContentType` détecté et le sha256 corrects ; un type hors liste blanche est rejeté en 415 sans le moindre appel S3 ; un upload qui dépasse `maxUploadSize` une fois le multipart démarré renvoie 413, ne crée aucun objet et n'a laissé aucun upload multipart ouvert (`ListMultipartUploads`) ; l'annulation du contexte pendant l'envoi produit le même résultat (abort, aucun objet) ; le contexte de la requête est bien celui transmis à `UploadObject` (annuler la requête ferme ce contexte) ; le sémaphore de concurrence répond 503 avec `Retry-After` à une deuxième requête qui ne trouve pas de place dans le délai d'attente `slotWait`, et `NewS3PipelineHandler` panique si `maxConcurrentUploads` < 1 ; aucun fichier temporaire `multipart-*` n'est créé pendant un upload S3, alors que `FormFileHandler` en crée bien un dès qu'il dépasse `maxMemory` (témoin positif).
