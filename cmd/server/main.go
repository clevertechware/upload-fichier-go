// Command server exposes the upload pipeline built across the three
// articles: POST /upload for a single streamed file, PUT /chunks for the
// chunked client from the third article.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/clevertechware/upload-fichier-go/internal/chunkupload"
	"github.com/clevertechware/upload-fichier-go/internal/upload"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dest := flag.String("dest", "./uploads", "directory uploads are stored under")
	maxUploadSize := flag.Int64("max-upload-size", 32<<20, "maximum accepted upload size in bytes")
	flag.Parse()

	if err := os.MkdirAll(*dest, 0o755); err != nil {
		log.Fatalf("create upload directory: %v", err)
	}

	root, err := os.OpenRoot(*dest)
	if err != nil {
		log.Fatalf("open upload directory as root: %v", err)
	}
	defer root.Close()

	mux := http.NewServeMux()
	mux.Handle("POST /upload", upload.NewTrackedPipelineHandler(root, *maxUploadSize, log.Printf))
	mux.Handle("PUT /chunks", chunkupload.NewHandler())

	srv := &http.Server{
		Addr:    *addr,
		Handler: mux,
		// ReadTimeout would also bound the time to read the body, which
		// breaks large uploads; use http.ResponseController.SetReadDeadline
		// per request instead if a per-request read deadline is needed.
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("listening on %s, storing uploads under %s", *addr, *dest)
	log.Fatal(srv.ListenAndServe())
}
