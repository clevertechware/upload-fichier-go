// Command server exposes every example of the upload series under
// /articles/<slug>/, one prefix per blog article. See internal/server/routes.go for the list.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/clevertechware/upload-fichier-go/internal/server"
)

const defaultMaxUploadSize = 32 << 20

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := flag.String("addr", ":8080", "listen address")
	dest := flag.String("dest", "./uploads", "directory the examples store uploads under, one sub-directory per article")
	maxUploadSize := flag.Int64("max-upload-size", defaultMaxUploadSize, "maximum accepted upload size in bytes")
	s3Bucket := flag.String("s3-bucket", "", "S3 bucket enabling the article 4 route; credentials come from the AWS "+
		"default chain, and AWS_ENDPOINT_URL_S3 points it at MinIO or LocalStack")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := server.Config{
		Addr: *addr, Dest: *dest, MaxUploadSize: *maxUploadSize, Logf: log.Printf, S3Bucket: *s3Bucket,
	}
	if *s3Bucket != "" {
		uploader, err := server.NewS3Uploader(ctx)
		if err != nil {
			return err
		}
		cfg.S3Uploader = uploader
	}
	return server.Run(ctx, cfg)
}
