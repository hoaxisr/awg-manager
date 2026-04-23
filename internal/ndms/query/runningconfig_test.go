package query

import (
	"context"
	"errors"
	"testing"
	"time"
)

const runningConfigPath = "/show/running-config"

var sampleRCBytes = []byte(`{"message": ["interface Wireguard0", "  description warp", "  up", "!"]}`)

func TestRunningConfigStore_Lines_ParsesAndCaches(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw(runningConfigPath, sampleRCBytes)
	s := NewRunningConfigStore(fg, NopLogger())

	got, err := s.Lines(context.Background())
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(got) != 4 || got[0] != "interface Wireguard0" {
		t.Errorf("lines: %#v", got)
	}
	_, _ = s.Lines(context.Background())
	if fg.Calls(runningConfigPath) != 1 {
		t.Errorf("calls: %d", fg.Calls(runningConfigPath))
	}
}

func TestRunningConfigStore_Lines_ServesStaleOnError(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw(runningConfigPath, sampleRCBytes)
	s := NewRunningConfigStoreWithTTL(fg, NopLogger(), 20*time.Millisecond)
	_, _ = s.Lines(context.Background())
	time.Sleep(30 * time.Millisecond)
	fg.SetError(runningConfigPath, errors.New("boom"))
	got, err := s.Lines(context.Background())
	if err != nil {
		t.Fatalf("stale-ok: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("len: %d", len(got))
	}
}

func TestRunningConfigStore_InvalidateAllForcesRefetch(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw(runningConfigPath, sampleRCBytes)
	s := NewRunningConfigStore(fg, NopLogger())
	_, _ = s.Lines(context.Background())
	s.InvalidateAll()
	_, _ = s.Lines(context.Background())
	if fg.Calls(runningConfigPath) != 2 {
		t.Errorf("calls: %d", fg.Calls(runningConfigPath))
	}
}
