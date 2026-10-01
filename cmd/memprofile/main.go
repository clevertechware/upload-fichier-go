// Command memprofile measures heap usage, multipart-* temporary file
// creation and throughput for the upload-receiving approaches compared in
// articles 1 and 4, and prints a Markdown table ready to paste into them.
package main

import (
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
	size := flag.Int64("size", 256<<20, "payload size in bytes")
	sampleEvery := flag.Duration("sample-every", 5*time.Millisecond, "memory/disk sampling interval")
	only := flag.String("only", "", "comma-separated substring filter on approach names; empty runs all")
	flag.Parse()

	approaches := []approach{
		{"io.ReadAll", upload.NewReadAllHandler},
		{"ParseMultipartForm / FormFile", func(dest string) http.HandlerFunc {
			return upload.NewFormFileHandler(dest, 32<<20)
		}},
		{"MultipartReader en flux", func(dest string) http.HandlerFunc {
			return upload.NewMultipartReaderHandler(dest, *size+1<<20)
		}},
		{"Flux vers S3 (transfermanager)", func(dest string) http.HandlerFunc {
			// Same settings recommended in article 4: threshold and part size
			// at 5 MiB, concurrency 2, so the theoretical bound is
			// 5 + (2+1)*5 = 20 MiB per upload, independent of file size.
			uploader := transfermanager.New(discardS3Client{}, func(o *transfermanager.Options) {
				o.PartSizeBytes = 5 << 20
				o.MultipartUploadThreshold = 5 << 20
				o.Concurrency = 2
				o.FailTimeout = 30 * time.Second
			})
			return upload.NewS3PipelineHandler(uploader, "memprofile-bucket", *size+1<<20, 4, time.Minute, func(string, ...any) {})
		}},
	}

	if *only != "" {
		approaches = filterApproaches(approaches, strings.Split(*only, ","))
	}

	fmt.Printf("Taille testée : %d octets (%.1f MiB)\n\n", *size, float64(*size)/(1<<20))

	results := make([]result, 0, len(approaches))
	for _, a := range approaches {
		r, err := measure(a, *size, *sampleEvery)
		if err != nil {
			log.Fatalf("measuring %s: %v", a.name, err)
		}
		results = append(results, r)
	}

	fmt.Println("| Approche | Pic mémoire (heap) | Fichiers temporaires | Débit |")
	fmt.Println("|---|---|---|---|")
	for _, r := range results {
		tmp := "Non"
		if r.tempFiles > 0 {
			tmp = fmt.Sprintf("Oui (%d)", r.tempFiles)
		}
		fmt.Printf("| `%s` | %.1f MiB (delta %.1f MiB) | %s | %.1f MiB/s |\n",
			r.approach, float64(r.peakHeapBytes)/(1<<20), float64(r.peakHeapDelta)/(1<<20), tmp, r.throughputMBs)
	}
}

func measure(a approach, size int64, sampleEvery time.Duration) (result, error) {
	storeDir, err := os.MkdirTemp("", "memprofile-store-")
	if err != nil {
		return result{}, fmt.Errorf("create store dir: %w", err)
	}
	defer os.RemoveAll(storeDir)

	tmpDir, err := os.MkdirTemp("", "memprofile-tmp-")
	if err != nil {
		return result{}, fmt.Errorf("create tmp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	prevTMPDIR, hadTMPDIR := os.LookupEnv("TMPDIR")
	if err := os.Setenv("TMPDIR", tmpDir); err != nil {
		return result{}, fmt.Errorf("set TMPDIR: %w", err)
	}
	defer func() {
		if hadTMPDIR {
			if err := os.Setenv("TMPDIR", prevTMPDIR); err != nil {
				log.Printf("restore TMPDIR: %v", err)
			}
		} else if err := os.Unsetenv("TMPDIR"); err != nil {
			log.Printf("unset TMPDIR: %v", err)
		}
	}()

	handler := a.handler(storeDir)
	server := httptest.NewServer(handler)
	defer server.Close()

	// PNG-prefixed so the S3 approach's content sniffing accepts the body too;
	// the other approaches store bytes as-is and don't care about the prefix.
	body, contentType := s3MultipartBody("file", "payload.png", size)

	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)

	stop := make(chan struct{})
	sampled := make(chan sample)
	go sampleUsage(tmpDir, sampleEvery, stop, sampled)

	start := time.Now()
	req, err := http.NewRequest(http.MethodPost, server.URL, body)
	if err != nil {
		close(stop)
		return result{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	duration := time.Since(start)
	close(stop)
	s := <-sampled

	if err != nil {
		return result{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return result{}, fmt.Errorf("unexpected status %d, body unreadable: %w", resp.StatusCode, readErr)
		}
		return result{}, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, respBody)
	}

	throughputMBs := float64(size) / (1 << 20) / duration.Seconds()

	return result{
		approach:      a.name,
		peakHeapBytes: s.peakHeapInuse,
		peakHeapDelta: int64(s.peakHeapInuse) - int64(baseline.HeapInuse),
		tempFiles:     s.peakTempFiles,
		duration:      duration,
		throughputMBs: throughputMBs,
	}, nil
}

type sample struct {
	peakHeapInuse uint64
	peakTempFiles int
}

func sampleUsage(tmpDir string, every time.Duration, stop <-chan struct{}, out chan<- sample) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	var s sample
	for {
		select {
		case <-ticker.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapInuse > s.peakHeapInuse {
				s.peakHeapInuse = m.HeapInuse
			}
			if n := countMultipartFiles(tmpDir); n > s.peakTempFiles {
				s.peakTempFiles = n
			}
		case <-stop:
			out <- s
			return
		}
	}
}

// filterApproaches keeps only the approaches whose name contains at least
// one of substrings, so -only can restrict a run to a couple of approaches
// instead of paying for all of them at large sizes.
func filterApproaches(approaches []approach, substrings []string) []approach {
	kept := make([]approach, 0, len(approaches))
	for _, a := range approaches {
		for _, s := range substrings {
			if strings.Contains(a.name, strings.TrimSpace(s)) {
				kept = append(kept, a)
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
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "multipart-") {
			count++
		}
	}
	return count
}
