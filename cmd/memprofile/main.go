// Command memprofile measures heap usage, multipart-* temporary file
// creation and throughput for the upload-receiving approaches compared in
// articles 1 and 4, and prints a Markdown table ready to paste into them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"time"

	transfermanager "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/clevertechware/upload-fichier-go/internal/upload"
)

const (
	mebibyte = 1 << 20

	defaultPayloadSize = 256 * mebibyte
	defaultSampleEvery = 5 * time.Millisecond
	formFileMaxMemory  = 32 * mebibyte
	sizeMargin         = mebibyte

	s3MaxUploads     = 4
	s3SlotWait       = time.Minute
	s3Bucket         = "memprofile-bucket"
	multipartPrefix  = "multipart-"
	payloadFileName  = "payload.png"
	payloadFieldName = "file"
)

var errUnexpectedStatus = errors.New("unexpected status")

type approach struct {
	name    string
	handler func(dest string) http.HandlerFunc
}

type result struct {
	approach      string
	peakHeapBytes uint64
	peakHeapDelta int64
	tempFiles     int
	duration      time.Duration
	throughputMBs float64
}

func main() {
	size := flag.Int64("size", defaultPayloadSize, "payload size in bytes")
	sampleEvery := flag.Duration("sample-every", defaultSampleEvery, "memory/disk sampling interval")
	only := flag.String("only", "", "comma-separated substring filter on approach names; empty runs all")
	flag.Parse()

	approaches := newApproaches(*size + sizeMargin)
	if *only != "" {
		approaches = filterApproaches(approaches, strings.Split(*only, ","))
	}

	results := make([]result, 0, len(approaches))
	for _, candidate := range approaches {
		res, err := measure(candidate, *size, *sampleEvery)
		if err != nil {
			log.Fatalf("measuring %s: %v", candidate.name, err)
		}
		results = append(results, res)
	}

	report := fmt.Sprintf("Taille testée : %d octets (%.1f MiB)\n\n%s", *size, mib(float64(*size)), markdownTable(results))
	if _, err := os.Stdout.WriteString(report); err != nil {
		log.Fatalf("write report: %v", err)
	}
}

// newApproaches lists the compared handlers. The S3 one uses the settings of upload.ConfigureS3Uploader.
func newApproaches(maxUploadSize int64) []approach {
	return []approach{
		{"io.ReadAll", upload.NewReadAllHandler},
		{"ParseMultipartForm / FormFile", func(dest string) http.HandlerFunc {
			return upload.NewFormFileHandler(dest, formFileMaxMemory)
		}},
		{"MultipartReader en flux", func(dest string) http.HandlerFunc {
			return upload.NewMultipartReaderHandler(dest, maxUploadSize)
		}},
		{"Flux vers S3 (transfermanager)", func(string) http.HandlerFunc {
			uploader := transfermanager.New(discardS3Client{}, upload.ConfigureS3Uploader)
			return upload.NewS3PipelineHandler(
				uploader, s3Bucket, maxUploadSize, s3MaxUploads, s3SlotWait, func(string, ...any) {},
			)
		}},
	}
}

func markdownTable(results []result) string {
	var table strings.Builder
	table.WriteString("| Approche | Pic mémoire (heap) | Fichiers temporaires | Débit |\n")
	table.WriteString("|---|---|---|---|\n")
	for i := range results {
		res := &results[i]
		tempFiles := "Non"
		if res.tempFiles > 0 {
			tempFiles = fmt.Sprintf("Oui (%d)", res.tempFiles)
		}
		fmt.Fprintf(&table, "| `%s` | %.1f MiB (delta %.1f MiB) | %s | %.1f MiB/s |\n",
			res.approach, mib(float64(res.peakHeapBytes)), mib(float64(res.peakHeapDelta)), tempFiles, res.throughputMBs)
	}
	return table.String()
}

func mib(bytes float64) float64 {
	return bytes / mebibyte
}

func measure(candidate approach, size int64, sampleEvery time.Duration) (result, error) {
	storeDir, err := os.MkdirTemp("", "memprofile-store-")
	if err != nil {
		return result{}, fmt.Errorf("create store dir: %w", err)
	}
	defer removeAll(storeDir)

	tmpDir, err := os.MkdirTemp("", "memprofile-tmp-")
	if err != nil {
		return result{}, fmt.Errorf("create tmp dir: %w", err)
	}
	defer removeAll(tmpDir)

	restoreTMPDIR, err := redirectTMPDIR(tmpDir)
	if err != nil {
		return result{}, err
	}
	defer restoreTMPDIR()

	server := httptest.NewServer(candidate.handler(storeDir))
	defer server.Close()

	// PNG-prefixed so the S3 approach's content sniffing accepts the body too;
	// the other approaches store bytes as-is and don't care about the prefix.
	body, contentType := s3MultipartBody(payloadFieldName, payloadFileName, size)

	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	stop := make(chan struct{})
	sampled := make(chan sample)
	go sampleUsage(tmpDir, sampleEvery, stop, sampled)

	duration, err := postBody(server.URL, contentType, body)
	close(stop)
	peak := <-sampled
	if err != nil {
		return result{}, err
	}

	return result{
		approach:      candidate.name,
		peakHeapBytes: peak.peakHeapInuse,
		peakHeapDelta: int64(peak.peakHeapInuse) - int64(baseline.HeapInuse),
		tempFiles:     peak.peakTempFiles,
		duration:      duration,
		throughputMBs: mib(float64(size)) / duration.Seconds(),
	}, nil
}

func postBody(url, contentType string, body io.Reader) (time.Duration, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, body)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	duration := time.Since(start)
	if err != nil {
		return 0, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return 0, fmt.Errorf("%w %d, body unreadable: %w", errUnexpectedStatus, resp.StatusCode, readErr)
		}
		return 0, fmt.Errorf("%w %d: %s", errUnexpectedStatus, resp.StatusCode, respBody)
	}
	return duration, nil
}

// redirectTMPDIR points os.TempDir at dir so mime/multipart spills land where countMultipartFiles looks, and returns
// the function that restores the previous value.
func redirectTMPDIR(dir string) (func(), error) {
	previous, hadPrevious := os.LookupEnv("TMPDIR")
	if err := os.Setenv("TMPDIR", dir); err != nil {
		return nil, fmt.Errorf("set TMPDIR: %w", err)
	}

	return func() {
		var err error
		if hadPrevious {
			err = os.Setenv("TMPDIR", previous)
		} else {
			err = os.Unsetenv("TMPDIR")
		}
		if err != nil {
			log.Printf("restore TMPDIR: %v", err)
		}
	}, nil
}

func removeAll(path string) {
	if err := os.RemoveAll(path); err != nil {
		log.Printf("remove %s: %v", path, err)
	}
}

type sample struct {
	peakHeapInuse uint64
	peakTempFiles int
}

func sampleUsage(tmpDir string, every time.Duration, stop <-chan struct{}, out chan<- sample) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	var peak sample
	for {
		select {
		case <-ticker.C:
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			peak.peakHeapInuse = max(peak.peakHeapInuse, mem.HeapInuse)
			peak.peakTempFiles = max(peak.peakTempFiles, countMultipartFiles(tmpDir))
		case <-stop:
			out <- peak
			return
		}
	}
}

// filterApproaches keeps only the approaches whose name contains at least
// one of substrings, so -only can restrict a run to a couple of approaches
// instead of paying for all of them at large sizes.
func filterApproaches(approaches []approach, substrings []string) []approach {
	kept := make([]approach, 0, len(approaches))
	for _, candidate := range approaches {
		for _, substring := range substrings {
			if strings.Contains(candidate.name, strings.TrimSpace(substring)) {
				kept = append(kept, candidate)
				break
			}
		}
	}
	return kept
}

// countMultipartFiles counts the multipart-* files mime/multipart spills
// large form parts to, ignoring anything else that lands in dir.
func countMultipartFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), multipartPrefix) {
			count++
		}
	}
	return count
}
