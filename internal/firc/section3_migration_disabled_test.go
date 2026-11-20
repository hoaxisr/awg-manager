package firc

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func newSection3TestSwitchHooks(cur *string, sbRunning *bool, parked *bool, commitErr, stopErr error) EngineSwitchHooks {
	return EngineSwitchHooks{
		GetCurrentEngine: func(context.Context) (string, error) {
			return *cur, nil
		},
		StartTarget: func(_ context.Context, target string) error {
			if target == "sing-box" {
				*sbRunning = true
			}
			return nil
		},
		StopEngine: func(_ context.Context, target string) error {
			if stopErr != nil {
				return stopErr
			}
			if target == "sing-box" {
				*sbRunning = false
			}
			return nil
		},
		ParkEngineForStandby: func(_ context.Context, target string) error {
			if target == "sing-box" {
				*parked = true
			}
			return nil
		},
		ActivateTarget: func(_ context.Context, target string) error {
			if target == "sing-box" {
				*parked = false
			}
			return nil
		},
		CommitSettings: func(_ context.Context, target string) error {
			if commitErr != nil {
				return commitErr
			}
			*cur = target
			return nil
		},
	}
}

func TestSwitchEngineRespectsDisabledByDefaultAndAllowsExplicitAuthorization(t *testing.T) {
	ctx := context.Background()
	m, handler := newMockFircHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	m.mu.Lock()
	m.failHealth = true
	m.mu.Unlock()

	root := t.TempDir()
	op := NewOperator(Options{ConfigDir: root})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))

	starts := 0
	stops := 0
	op.SetLifecycleHooks(
		func(context.Context) error {
			starts++
			m.mu.Lock()
			m.failHealth = false
			m.mu.Unlock()
			return nil
		},
		func(context.Context) error {
			stops++
			m.mu.Lock()
			m.failHealth = true
			m.mu.Unlock()
			return nil
		},
	)

	if err := op.StopByUser(ctx); err != nil {
		t.Fatalf("StopByUser: %v", err)
	}
	if !op.ManuallyDisabled() {
		t.Fatal("expected ManuallyDisabled=true after StopByUser")
	}

	curEngine := "sing-box"
	sbRunning := true
	sbParked := false
	hooks := newSection3TestSwitchHooks(&curEngine, &sbRunning, &sbParked, nil, nil)

	// Default switch without AuthorizeDisabledStart must respect manual disable.
	if err := op.SwitchEngineWithOptions(ctx, "firc", EngineSwitchOptions{AuthorizeDisabledStart: false}, hooks); !errors.Is(err, ErrManuallyDisabled) {
		t.Fatalf("expected ErrManuallyDisabled when AuthorizeDisabledStart=false, got %v", err)
	}
	if !op.ManuallyDisabled() || curEngine != "sing-box" || !sbRunning || starts != 0 {
		t.Fatalf("expected state preserved after blocked switch (disabled=%v engine=%s sbRunning=%v starts=%d)",
			op.ManuallyDisabled(), curEngine, sbRunning, starts)
	}

	// Internal Start must continue respecting manual disable.
	if err := op.Start(ctx); !errors.Is(err, ErrManuallyDisabled) {
		t.Fatalf("expected internal Start to still fail with ErrManuallyDisabled, got %v", err)
	}

	// Explicit wizard/user switch clears .awgm-disabled inside the transaction.
	if err := op.SwitchEngineWithOptions(ctx, "firc", EngineSwitchOptions{
		SingboxStandby:         false,
		AuthorizeDisabledStart: true,
	}, hooks); err != nil {
		t.Fatalf("explicit SwitchEngineWithOptions(firc) failed: %v", err)
	}
	if op.ManuallyDisabled() {
		t.Fatal("expected .awgm-disabled cleared after successful explicit switch")
	}
	if curEngine != "firc" || sbRunning || starts != 1 {
		t.Fatalf("unexpected post-switch state: engine=%s sbRunning=%v starts=%d", curEngine, sbRunning, starts)
	}

	// Switching back then re-disabling and testing SingboxStandby=true.
	if err := op.SwitchEngine(ctx, "sing-box", hooks); err != nil {
		t.Fatalf("switch back to sing-box: %v", err)
	}
	if err := op.StopByUser(ctx); err != nil {
		t.Fatalf("second StopByUser: %v", err)
	}
	if err := op.SwitchEngineWithOptions(ctx, "firc", EngineSwitchOptions{
		SingboxStandby:         true,
		AuthorizeDisabledStart: true,
	}, hooks); err != nil {
		t.Fatalf("explicit standby switch to firc failed: %v", err)
	}
	if op.ManuallyDisabled() || curEngine != "firc" || !sbRunning || !sbParked {
		t.Fatalf("expected standby switch state (disabled=%v engine=%s sbRunning=%v sbParked=%v)",
			op.ManuallyDisabled(), curEngine, sbRunning, sbParked)
	}
}

func TestSwitchEngineRollbackRestoresDisabledMarkerWithoutDeadlock(t *testing.T) {
	ctx := context.Background()

	t.Run("preflight_missing_binary_leaves_disabled_untouched", func(t *testing.T) {
		root := t.TempDir()
		op := NewOperator(Options{
			ConfigDir:   root,
			FircdBinary: filepath.Join(root, "missing-fircd"),
			TunvlessBin: filepath.Join(root, "missing-tunvless"),
		})
		if err := os.WriteFile(op.disabledPath(), []byte("disabled by user\n"), 0600); err != nil {
			t.Fatal(err)
		}
		curEngine := "sing-box"
		sbRunning := true
		sbParked := false
		hooks := newSection3TestSwitchHooks(&curEngine, &sbRunning, &sbParked, nil, nil)
		err := op.SwitchEngineWithOptions(ctx, "firc", EngineSwitchOptions{AuthorizeDisabledStart: true}, hooks)
		if err == nil {
			t.Fatal("expected preflight failure on missing binaries")
		}
		if !op.ManuallyDisabled() {
			t.Fatal("expected .awgm-disabled preserved on preflight failure")
		}
		if curEngine != "sing-box" || !sbRunning {
			t.Fatalf("expected sing-box untouched, got engine=%s running=%v", curEngine, sbRunning)
		}
	})

	for _, tc := range []struct {
		name      string
		failSync  bool
		commitErr error
		stopErr   error
	}{
		{name: "sync_tunnels_fail", failSync: true},
		{name: "commit_settings_fail", commitErr: errors.New("disk full")},
		{name: "stop_singbox_fail", stopErr: errors.New("stop singbox failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, handler := newMockFircHandler()
			srv := httptest.NewServer(handler)
			defer srv.Close()

			m.mu.Lock()
			m.failHealth = true
			m.failTunnels = tc.failSync
			m.mu.Unlock()

			root := t.TempDir()
			op := NewOperator(Options{ConfigDir: root})
			op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
			if err := op.SaveTunnelsFile([]Tunnel{{
				ID:     "t1",
				Device: "tunvless0",
				Enable: true,
				Sources: []TunnelSource{{
					ID:   "12345678",
					Kind: "link",
					Link: "vless://11111111-2222-3333-4444-555555555555@example.com:443#Node",
				}},
			}}); err != nil {
				t.Fatal(err)
			}

			op.SetLifecycleHooks(
				func(context.Context) error {
					m.mu.Lock()
					m.failHealth = false
					m.mu.Unlock()
					return nil
				},
				func(context.Context) error {
					m.mu.Lock()
					m.failHealth = true
					m.mu.Unlock()
					return nil
				},
			)
			if err := op.StopByUser(ctx); err != nil {
				t.Fatalf("StopByUser: %v", err)
			}

			curEngine := "sing-box"
			sbRunning := true
			sbParked := false
			hooks := newSection3TestSwitchHooks(&curEngine, &sbRunning, &sbParked, tc.commitErr, tc.stopErr)

			err := op.SwitchEngineWithOptions(ctx, "firc", EngineSwitchOptions{
				SingboxStandby:         false,
				AuthorizeDisabledStart: true,
			}, hooks)
			if err == nil {
				t.Fatalf("expected SwitchEngineWithOptions failure for %s", tc.name)
			}
			if !op.ManuallyDisabled() {
				t.Fatalf("expected .awgm-disabled restored on rollback for %s", tc.name)
			}
			if curEngine != "sing-box" {
				t.Fatalf("expected engine restored to sing-box, got %s", curEngine)
			}
			if !sbRunning {
				t.Fatalf("expected sing-box running after rollback for %s", tc.name)
			}
			if op.IsRunning(ctx) {
				t.Fatalf("expected fircd stopped after rollback for %s", tc.name)
			}
		})
	}
}
