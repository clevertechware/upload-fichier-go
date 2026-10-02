// Command server exposes every example of the upload series under
// /articles/<slug>/, one prefix per blog article. See routes.go for the list.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	transfermanager "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/clevertechware/upload-fichier-go/internal/upload"
)

const (
	defaultMaxUploadSize = 32 << 20
	readHeaderTimeout    = 10 * time.Second
)

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

	cfg := config{dest: *dest, maxUploadSize: *maxUploadSize, logf: log.Printf, s3Bucket: *s3Bucket}
	if *s3Bucket != "" {
		uploader, err := newS3Uploader(context.Background())
		if err != nil {
			return err
		}
		cfg.s3Uploader = uploader
	}

	routes, err := newRouter(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := routes.close(); closeErr != nil {
			log.Printf("close upload directories: %v", closeErr)
		}
	}()

	srv := &http.Server{
		Addr:    *addr,
		Handler: routes.mux,
		// ReadTimeout would also bound the time to read the body, which
		// breaks large uploads; use http.ResponseController.SetReadDeadline
		// per request instead if a per-request read deadline is needed.
		ReadHeaderTimeout: readHeaderTimeout,
	}

	log.Printf("listening on %s, storing uploads under %s", *addr, *dest)
	return srv.ListenAndServe()
}

// newS3Uploader builds the article 4 uploader with upload.ConfigureS3Uploader.
func newS3Uploader(ctx context.Context) (*transfermanager.Client, error) {
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return transfermanager.New(s3.NewFromConfig(awsConfig), upload.ConfigureS3Uploader), nil
}
