package query

import (
	"context"
	"errors"
	"sync"
	"testing"
)

const versionPath = "/show/version"

const sampleVersionJSON = `{
	"release": "4.2.5",
	"title": "Keenetic",
	"hw_id": "KN-1011",
	"description": "KeeneticOS",
	"components": ["firewall", "wireguard"]
}`

func TestSystemInfoStore_Init_FetchesOnce(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(versionPath, sampleVersionJSON)

	s := NewSystemInfoStore(fg, NopLogger())
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("second Init: %v", err)
	}

	v, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v.Release != "4.2.5" || v.Title != "Keenetic" {
		t.Errorf("got: %#v", v)
	}
	if fg.Calls(versionPath) != 1 {
		t.Errorf("calls: want 1 (second Init is no-op), got %d", fg.Calls(versionPath))
	}
	if v.LastFetched.IsZero() {
		t.Error("LastFetched: want non-zero, got zero")
	}
}

func TestSystemInfoStore_Get_BeforeInitReturnsError(t *testing.T) {
	fg := newFakeGetter()
	s := NewSystemInfoStore(fg, NopLogger())

	_, err := s.Get()
	if !errors.Is(err, ErrNotInitialized) {
		t.Errorf("err: want ErrNotInitialized, got %v", err)
	}
}

func TestSystemInfoStore_Init_ConcurrentCallersDedupHTTP(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(versionPath, sampleVersionJSON)

	s := NewSystemInfoStore(fg, NopLogger())

	const n = 10
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Init(context.Background())
		}()
	}
	wg.Wait()

	if got := fg.Calls(versionPath); got != 1 {
		t.Errorf("HTTP calls under concurrent Init: want 1 (single-flight), got %d", got)
	}

	v, err := s.Get()
	if err != nil || v.Release != "4.2.5" {
		t.Errorf("Get after concurrent Init: %#v err=%v", v, err)
	}
}

func TestSystemInfoStore_Init_ErrorDoesNotCache(t *testing.T) {
	fg := newFakeGetter()
	fg.SetError(versionPath, errors.New("ndms down"))
	s := NewSystemInfoStore(fg, NopLogger())

	err := s.Init(context.Background())
	if err == nil {
		t.Fatalf("Init: want error, got nil")
	}

	_, err = s.Get()
	if !errors.Is(err, ErrNotInitialized) {
		t.Errorf("Get after failed Init: want ErrNotInitialized, got %v", err)
	}

	fg.SetError(versionPath, nil)
	fg.SetJSON(versionPath, sampleVersionJSON)
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("recovery Init: %v", err)
	}
	v, err := s.Get()
	if err != nil || v.Release != "4.2.5" {
		t.Errorf("after recovery: %#v err=%v", v, err)
	}
}
