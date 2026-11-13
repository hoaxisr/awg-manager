package main

import (
	"context"
	"errors"
	"log"
	"testing"
	"time"
)

type flakyLANDiscovery struct {
	calls int
}

func (f *flakyLANDiscovery) LANBridges(context.Context) ([]string, error) {
	f.calls++
	if f.calls < 3 {
		return nil, errors.New("AWGM is still booting")
	}
	return []string{"br0"}, nil
}

func TestDiscoverLANBridgesRetriesUntilAvailable(t *testing.T) {
	old := lanDiscoveryRetry
	lanDiscoveryRetry = time.Millisecond
	t.Cleanup(func() { lanDiscoveryRetry = old })
	client := &flakyLANDiscovery{}
	interfaces, err := discoverLANBridges(context.Background(), client, log.New(&discardWriter{}, "", 0))
	if err != nil || len(interfaces) != 1 || client.calls != 3 {
		t.Fatalf("interfaces=%v calls=%d err=%v", interfaces, client.calls, err)
	}
}

func TestDiscoverLANBridgesHonorsCancellation(t *testing.T) {
	old := lanDiscoveryRetry
	lanDiscoveryRetry = 20 * time.Millisecond
	t.Cleanup(func() { lanDiscoveryRetry = old })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := discoverLANBridges(ctx, &flakyLANDiscovery{}, log.New(&discardWriter{}, "", 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
