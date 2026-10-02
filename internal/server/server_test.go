package server

import (
	"context"
	"testing"
	"time"
)

func TestRunReturnsNilWhenContextIsCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cfg := Config{Addr: "127.0.0.1:0", Dest: t.TempDir(), MaxUploadSize: 1 << 20, Logf: t.Logf}
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancellation = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}
