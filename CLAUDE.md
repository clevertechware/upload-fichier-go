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
golangci-lint run                                # strict v2 config in .golangci.yml (lll 120, funlen 80, mnd, err113...)
go test ./...
go test -race ./...
go test ./internal/upload/ -run TestName         # single test
go test ./internal/upload/ -bench . -run '^$'    # article 1 memory benchmarks
go run ./cmd/memprofile -size=1073741824 -only="FormFile,S3"   # peak heap comparison, 1 GB upload
go run ./cmd/server                              # example server on :8080
```

## Architecture

- `internal/upload/` holds every receiving handler. The files are layered on purpose, one per article:
  - `handler.go`: article 1 handlers (`ReadAll`, `FormFile`, `MultipartReader`) with `MaxBytesReader` and 413.
  - `pipeline.go`: `NewValidatingHandler` (article 2: sniff, whitelist, sha256, server-generated name, `os.Root`) and
    `NewTrackedPipelineHandler` (article 3: the same pipeline plus `streamio.TrackedReader`). The tracked version
    wraps the same reader, so validation is not duplicated. `cmd/server` runs the tracked one.
  - `s3.go`: `NewS3PipelineHandler` (article 4) streams to S3 through `transfermanager.UploadObject` and a concurrency
    semaphore (503 + `Retry-After`). It reuses the shared helpers (`nextFilePart`, `SniffType`, `ValidateType`,
    `GenerateStoredName`, `NewHashingReader`).
- **Do not modify `pipeline.go` for S3 work.** Articles 2 and 3 cite it, and article 4 deliberately left it untouched.
- `streamio/`: `TrackedReader` (progress throttled to 200 ms, a final callback guaranteed at EOF, context cancellation).
- `client/` and `internal/chunkupload/`: 5 MiB chunked client and its demo reassembly server (test-only, never expose).
- `internal/genfile/`: streams multipart test bodies without loading them in memory.
- `cmd/memprofile/`: measures peak heap per approach. `s3discard.go` implements the S3 client interface with a discard
  sink, because an in-memory fake S3 would retain the bytes and skew the measurement.

## Gotchas

- S3 tests use `gofakes3`, which cannot decode the SDK's default `STREAMING-UNSIGNED-PAYLOAD-TRAILER` checksum
  encoding. The test client sets `RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired`
  (see `newFakeS3` in `internal/upload/s3_test.go`).
- The transfermanager aborts the multipart upload using the caller's context, so the uploader must set
  `Options.FailTimeout` or the abort fails after cancellation and leaves an open multipart upload.
- `transfermanager` is pre-1.0 and pinned in `go.mod`. Check it on every upgrade.
- `.claude/`, `.idea/` and `uploads/` are git-ignored.
