# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Shared example repo for a four-part French blog series on file upload in Go (memory-safe reception, validation,
progress/cancellation, streaming to S3). README.md and BENCHMARK.md are in French and map each article to its files;
code comments and identifiers are in English. Module `github.com/clevertechware/upload-fichier-go`, Go ≥ 1.24
(`os.Root` is required).

## Commands

```bash
go build ./...
go vet ./...
golangci-lint run ./...                          # strict v2 config in .golangci.yml; must report 0 issues before any commit
go test ./...
go test -race ./...
go test ./internal/upload/ -run TestName         # single test
go test ./internal/upload/ -bench . -run '^$'    # article 1 memory benchmarks
go run ./cmd/memprofile -size=1073741824 -only="FormFile,S3"   # peak heap comparison, 1 GB upload
go run ./cmd/server                              # example server on :8080
```

## Architecture

- `internal/upload/` holds every HTTP receiving handler. The files are layered on purpose, one per article:
  - `handler.go`: article 1 handlers (`ReadAll`, `FormFile`, `MultipartReader`) with `MaxBytesReader` and 413.
  - `pipeline.go`: `NewValidatingHandler` (article 2: sniff, whitelist, sha256, server-generated name, `os.Root`, via `filecheck`) and
    `NewTrackedPipelineHandler` (article 3: the same pipeline plus `streamio.TrackedReader`). The tracked version
    wraps the same reader, so validation is not duplicated.
  - `receive.go`: the reception both pipelines share (`readValidatedPart`: size limit, first file part, sniff,
    validate, generated name) and `writeError`, which maps a `statusError` or `http.MaxBytesError` to the response.
  - `s3.go`: `NewS3PipelineHandler` (article 4) streams to S3 through `transfermanager.UploadObject` and a concurrency
    semaphore (503 + `Retry-After`). It reuses `readValidatedPart`, `writeError` and `filecheck.NewHashingReader`; `ConfigureS3Uploader` holds the
    transfermanager settings shared by `cmd/server` and `cmd/memprofile`.
- `internal/filecheck/`: the HTTP-free building blocks (`SniffType`, `ValidateType`, `AllowedTypes`,
  `GenerateStoredName`, `CreateInRoot`, `HashingReader`). `upload` depends on it, never the reverse.
- `cmd/server/`: one route per example under `/articles/<slug>/...`, slug = the article frontmatter slug, files stored
  in `<dest>/<slug>/`. `routes.go` lists them; a new example is mounted there under its article's slug. The S3 route
  answers 503 unless `-s3-bucket` is set.
- `streamio/`: `TrackedReader` (progress throttled to 200 ms, a final callback guaranteed at EOF, context cancellation).
- `client/` and `internal/chunkupload/`: 5 MiB chunked client and its demo reassembly server (test-only, never expose).
- `internal/genfile/`: streams multipart test bodies without loading them in memory.
- `cmd/memprofile/`: measures peak heap per approach. `s3discard.go` implements the S3 client interface with a discard
  sink, because an in-memory fake S3 would retain the bytes and skew the measurement.

## Conventions

- `golangci-lint run ./...` must pass. Fix the code before touching `.golangci.yml`; every `//nolint` states why.
- Keep the code readable and refactor what isn't. Comment only what the code cannot say by itself.
- Article snippets quote these files verbatim: keep exported names stable.

## Gotchas

- S3 tests use `gofakes3`, which cannot decode the SDK's default `STREAMING-UNSIGNED-PAYLOAD-TRAILER` checksum
  encoding. The test client sets `RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired`
  (see `newFakeS3` in `internal/upload/s3_test.go`).
- The transfermanager aborts the multipart upload using the caller's context, so the uploader must set
  `Options.FailTimeout` or the abort fails after cancellation and leaves an open multipart upload.
- `transfermanager` is pre-1.0 and pinned in `go.mod`. Check it on every upgrade.
- `.claude/`, `.idea/` and `uploads/` are git-ignored.
