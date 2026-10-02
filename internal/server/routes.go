package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/clevertechware/upload-fichier-go/internal/chunkupload"
	"github.com/clevertechware/upload-fichier-go/internal/upload"
)

// Slugs of the blog articles, each one prefixing the routes of its examples.
const (
	slugMemory   = "uploader-fichier-go-sans-exploser-memoire"
	slugValidate = "empiler-des-io-reader-pour-valider-un-upload"
	slugTracking = "ecrire-son-propre-io-reader-en-go"
	slugS3       = "streamer-un-upload-go-vers-s3-sans-toucher-le-disque"
)

const (
	dirPermissions   = 0o750
	formFileMaxBytes = 32 << 20
	s3MaxUploads     = 4
	s3SlotWait       = 30 * time.Second
)

type Config struct {
	Addr          string
	Dest          string
	MaxUploadSize int64
	Logf          upload.Logf
	S3Bucket      string
	S3Uploader    upload.S3Uploader
}

// router mounts every example under /articles/<slug>/ and stores its files in <dest>/<slug>/.
type router struct {
	mux   *http.ServeMux
	cfg   Config
	roots []*os.Root
}

func newRouter(cfg Config) (*router, error) {
	routes := &router{mux: http.NewServeMux(), cfg: cfg}
	mounts := []func() error{routes.mountMemory, routes.mountValidation, routes.mountTracking}
	for _, mount := range mounts {
		if err := mount(); err != nil {
			return nil, errors.Join(err, routes.close())
		}
	}
	routes.mountS3()
	return routes, nil
}

func (rt *router) close() error {
	errs := make([]error, 0, len(rt.roots))
	for _, root := range rt.roots {
		errs = append(errs, root.Close())
	}
	return errors.Join(errs...)
}

func (rt *router) handle(method, slug, name string, handler http.Handler) {
	rt.mux.Handle(fmt.Sprintf("%s /articles/%s/%s", method, slug, name), handler)
}

func (rt *router) articleDir(slug string) (string, error) {
	dir := filepath.Join(rt.cfg.Dest, slug)
	if err := os.MkdirAll(dir, dirPermissions); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return dir, nil
}

func (rt *router) articleRoot(slug string) (*os.Root, error) {
	dir, err := rt.articleDir(slug)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open %s as root: %w", dir, err)
	}
	rt.roots = append(rt.roots, root)
	return root, nil
}

func (rt *router) mountMemory() error {
	dir, err := rt.articleDir(slugMemory)
	if err != nil {
		return err
	}
	rt.handle(http.MethodPost, slugMemory, "read-all", upload.NewReadAllHandler(dir))
	rt.handle(http.MethodPost, slugMemory, "form-file", upload.NewFormFileHandler(dir, formFileMaxBytes))
	rt.handle(http.MethodPost, slugMemory, "multipart-reader",
		upload.NewMultipartReaderHandler(dir, rt.cfg.MaxUploadSize))
	return nil
}

func (rt *router) mountValidation() error {
	root, err := rt.articleRoot(slugValidate)
	if err != nil {
		return err
	}
	rt.handle(http.MethodPost, slugValidate, "upload",
		upload.NewValidatingHandler(root, rt.cfg.MaxUploadSize, rt.cfg.Logf))
	return nil
}

func (rt *router) mountTracking() error {
	root, err := rt.articleRoot(slugTracking)
	if err != nil {
		return err
	}
	rt.handle(http.MethodPost, slugTracking, "upload",
		upload.NewTrackedPipelineHandler(root, rt.cfg.MaxUploadSize, rt.cfg.Logf))
	rt.handle(http.MethodPut, slugTracking, "chunks", chunkupload.NewHandler())
	return nil
}

func (rt *router) mountS3() {
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "S3 is not configured: start the server with -s3-bucket", http.StatusServiceUnavailable)
	})
	if rt.cfg.S3Uploader != nil {
		handler = upload.NewS3PipelineHandler(
			rt.cfg.S3Uploader, rt.cfg.S3Bucket, rt.cfg.MaxUploadSize, s3MaxUploads, s3SlotWait, rt.cfg.Logf,
		)
	}
	rt.handle(http.MethodPost, slugS3, "upload", handler)
}
