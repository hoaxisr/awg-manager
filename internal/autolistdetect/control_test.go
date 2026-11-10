package autolistdetect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSetEnabledPersistsIntentAndCallsInit(t *testing.T) {
	dir := t.TempDir()
	initPath := filepath.Join(dir, "S99awgmautolist")
	if err := os.WriteFile(initPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	s := NewService()
	s.initPath = initPath
	s.statePath = filepath.Join(dir, "awgmautolist.enabled")
	var actions []string
	s.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != initPath || len(args) != 1 {
			t.Fatalf("command = %s %v", name, args)
		}
		actions = append(actions, args[0])
		return nil, nil
	}
	s.confirm = func(context.Context, bool) error { return nil }
	if err := s.SetEnabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(s.statePath); err != nil || string(b) != "ENABLED=no\n" {
		t.Fatalf("disabled state = %q, %v", b, err)
	}
	if s.desiredEnabled() {
		t.Fatal("disabled intent was not retained")
	}
	if err := s.SetEnabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(s.statePath); err != nil || string(b) != "ENABLED=yes\n" {
		t.Fatalf("enabled state = %q, %v", b, err)
	}
	if strings.Join(actions, ",") != "stop,start" {
		t.Fatalf("init actions = %v", actions)
	}
}

func TestSetEnabledRequiresInstalledInitScript(t *testing.T) {
	s := NewService()
	s.initPath = filepath.Join(t.TempDir(), "missing")
	s.statePath = s.initPath + ".state"
	if err := s.SetEnabled(context.Background(), false); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("SetEnabled error = %v", err)
	}
	if _, err := os.Stat(s.statePath); !os.IsNotExist(err) {
		t.Fatal("state file created without an installed init script")
	}
}

func TestConfirmStateWaitsForHTTPTransition(t *testing.T) {
	var calls atomic.Int32
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[],"history":[]}`))
	}))
	defer sat.Close()
	statusURLForTest = func() string { return strings.TrimPrefix(sat.URL, "http://") }
	defer func() { statusURLForTest = nil }()
	s := NewService()
	if err := s.confirmState(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got < 3 {
		t.Fatalf("confirm returned before running HTTP status, calls=%d", got)
	}
}

func TestGetStatusIncludesHistoryAndDisabledIntent(t *testing.T) {
	sat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[],"history":[{"domain":"example.com","action":"excluded","source":"user","at":"2026-11-07T10:00:00Z"}],"probesUsedHour":30,"probesLimit":30,"addsUsedHour":50,"addsLimit":50,"targetInterface":"nwg0"}`))
	}))
	defer sat.Close()
	statusURLForTest = func() string { return strings.TrimPrefix(sat.URL, "http://") }
	defer func() { statusURLForTest = nil }()

	dir := t.TempDir()
	s := NewService()
	s.initPath = filepath.Join(dir, "S99awgmautolist")
	s.statePath = filepath.Join(dir, "awgmautolist.enabled")
	if err := os.WriteFile(s.initPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.statePath, []byte("ENABLED=no\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st := s.GetStatus(context.Background())
	if !st.Running || !st.Installed || st.Enabled {
		t.Fatalf("status lifecycle = %+v", st)
	}
	if len(st.History) != 1 || st.History[0].Domain != "example.com" || st.History[0].Action != "excluded" {
		t.Fatalf("history = %+v", st.History)
	}
	if st.ProbesUsedHour != 30 || st.ProbesLimit != 30 || st.AddsUsedHour != 50 || st.AddsLimit != 50 || st.TargetInterface != "nwg0" {
		t.Fatalf("hourly limits = %+v", st)
	}
}
