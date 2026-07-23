package hydraroute

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGeoDataStore_DownloadWithClient_RespectsContextCancel(t *testing.T) {
	store := newTestGeoStore(t)
	started := make(chan struct{})
	var startedOnce sync.Once
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			startedOnce.Do(func() { close(started) })
			<-r.Context().Done()
			return nil, r.Context().Err()
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := store.DownloadWithClient(ctx, "geosite", "https://example.com/geosite.dat", client)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not reach transport")
	}
	cancel()
	var err error
	select {
	case err = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not finish after context cancel")
	}
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got: %v", err)
	}
}

func TestGeoDataStore_List_NotBlockedByInFlightDownload(t *testing.T) {
	store := newTestGeoStore(t)
	block := make(chan struct{})
	started := make(chan struct{})
	var startedOnce sync.Once
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			startedOnce.Do(func() { close(started) })
			select {
			case <-block:
				<-r.Context().Done()
				return nil, r.Context().Err()
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = store.DownloadWithClient(ctx, "geoip", "https://example.com/geoip.dat", client)
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not reach transport")
	}
	listed := make(chan struct{})
	go func() {
		_ = store.List()
		close(listed)
	}()
	select {
	case <-listed:
	case <-time.After(5 * time.Second):
		t.Fatal("List blocked while download in flight")
	}
	cancel()
	close(block)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("download goroutine did not finish after cancel")
	}
}

func TestGeoDataStore_DownloadWithClient_LimitErrorDoesNotKeepLock(t *testing.T) {
	store := newTestGeoStore(t)

	store.mu.Lock()
	for i := 0; i < maxGeoFiles; i++ {
		store.entries = append(store.entries, GeoFileEntry{
			Type: "geosite",
			Path: filepath.Join(store.geoDir, fmt.Sprintf("geosite-%d.dat", i)),
		})
	}
	store.mu.Unlock()

	_, err := store.DownloadWithClient(context.Background(), "geosite", "https://example.com/geosite-extra.dat", nil)
	if err == nil || !strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("expected limit reached error, got: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_ = store.List()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("List appears blocked after limit error; lock likely not released")
	}
}

func TestGeoDataStore_List_DuringUpdateSwap_DoesNotDropEntry(t *testing.T) {
	store := newTestGeoStore(t)
	path := filepath.Join(store.geoDir, "route-a.dat")
	if err := os.WriteFile(path, []byte("old-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.entries = []GeoFileEntry{{Type: "geosite", Path: path, URL: "https://example.com/geosite.dat"}}
	store.mu.Unlock()

	dat := buildGeoDAT([][]byte{buildGeoEntry(1, "GOOGLE", 2, 2)})
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(dat)),
				Request:    r,
			}, nil
		}),
	}

	listDone := make(chan struct{})
	swapReached := make(chan struct{})
	var swapOnce sync.Once
	prevHook := geoUpdateSwapHook
	geoUpdateSwapHook = func(stage string) {
		if stage != "after_backup_rename" {
			return
		}
		swapOnce.Do(func() { close(swapReached) })
	}
	defer func() { geoUpdateSwapHook = prevHook }()

	go func() {
		<-swapReached
		_ = store.List()
		close(listDone)
	}()

	if _, err := store.UpdateWithClient(context.Background(), path, client); err != nil {
		t.Fatalf("UpdateWithClient: %v", err)
	}
	select {
	case <-listDone:
	case <-time.After(5 * time.Second):
		t.Fatal("List did not complete after update swap stage")
	}
	entries := store.List()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Path != path {
		t.Fatalf("entry path = %q, want %q", entries[0].Path, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("updated file missing: %v", err)
	}
}

func TestGeoDataStore_UpdateSaveFailureRollback_HoldsStateForList(t *testing.T) {
	store := newTestGeoStore(t)
	store.storagePath = store.geoDir // force save metadata failure

	path := filepath.Join(store.geoDir, "route-b.dat")
	if err := os.WriteFile(path, []byte("old-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.entries = []GeoFileEntry{{
		Type:     "geosite",
		Path:     path,
		URL:      "https://example.com/geosite.dat",
		Size:     111,
		TagCount: 1,
		Updated:  "old",
	}}
	store.mu.Unlock()

	dat := buildGeoDAT([][]byte{buildGeoEntry(1, "GOOGLE", 2, 2)})
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(dat)),
				Request:    r,
			}, nil
		}),
	}

	swapReached := make(chan struct{})
	listDone := make(chan []GeoFileEntry, 1)
	var swapOnce sync.Once
	prevHook := geoUpdateSwapHook
	geoUpdateSwapHook = func(stage string) {
		if stage != "after_backup_rename" {
			return
		}
		swapOnce.Do(func() { close(swapReached) })
	}
	defer func() { geoUpdateSwapHook = prevHook }()

	go func() {
		<-swapReached
		listDone <- store.List()
	}()

	_, err := store.UpdateWithClient(context.Background(), path, client)
	if err == nil || !strings.Contains(err.Error(), "save metadata") {
		t.Fatalf("expected save metadata error, got %v", err)
	}

	var listed []GeoFileEntry
	select {
	case listed = <-listDone:
	case <-time.After(5 * time.Second):
		t.Fatal("List did not complete during rollback flow")
	}
	if len(listed) != 1 {
		t.Fatalf("listed entries = %d, want 1", len(listed))
	}
	if listed[0].Path != path {
		t.Fatalf("listed path = %q, want %q", listed[0].Path, path)
	}
	if listed[0].Size != 111 || listed[0].TagCount != 1 || listed[0].Updated != "old" {
		t.Fatalf("listed metadata changed after failed update: %+v", listed[0])
	}

	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read restored file: %v", readErr)
	}
	if string(raw) != "old-data" {
		t.Fatalf("file content = %q, want old-data", string(raw))
	}
}
