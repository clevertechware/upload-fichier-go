// Package server wires the HTTP examples of the series: one route per example under /articles/<slug>/, and the
// lifecycle of the http.Server serving them.
package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	transfermanager "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/clevertechware/upload-fichier-go/internal/upload"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 30 * time.Second
)

// NewS3Uploader builds the article 4 uploader from the AWS default credential chain, with the settings of
// upload.ConfigureS3Uploader.
func NewS3Uploader(ctx context.Context) (*transfermanager.Client, error) {
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return transfermanager.New(s3.NewFromConfig(awsConfig), upload.ConfigureS3Uploader), nil
}

// Run serves cfg.Addr until ctx is cancelled, then shuts the server down gracefully and returns nil.
func Run(ctx context.Context, cfg Config) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}
	return serveOn(ctx, listener, cfg)
}

func serveOn(ctx context.Context, listener net.Listener, cfg Config) error {
	routes, err := newRouter(cfg)
	if err != nil {
		_ = listener.Close()
		return err
	}
	defer func() {
		if closeErr := routes.close(); closeErr != nil {
			log.Printf("close upload directories: %v", closeErr)
		}
	}()

	srv := &http.Server{
		Handler: routes.mux,
		// ReadTimeout would also bound the time to read the body, which
		// breaks large uploads; use http.ResponseController.SetReadDeadline
		// per request instead if a per-request read deadline is needed.
		ReadHeaderTimeout: readHeaderTimeout,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()

	cfg.Logf("listening on %s, storing uploads under %s", listener.Addr(), cfg.Dest)
	select {
	case err = <-serveErr:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err = srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err = <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
