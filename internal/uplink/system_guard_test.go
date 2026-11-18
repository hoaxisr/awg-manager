package uplink

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSystemPolicyGuardRefusesEditsAndIgnoresProxyEngineSwitch(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Put(Policy{ID: "system", Enabled: true, Target: Target{Engine: "system-wireguard", Ref: "Wireguard6"}, Providers: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	c := New(s, nil, map[string]Adapter{"system-wireguard": &nativeTestAdapter{}})
	if _, err = c.GuardNativeMutation(context.Background(), "Wireguard6", "transport"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	g, err := c.AcquireEngineTransition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer g.Release()
	if err = g.CheckTarget("firc"); err != nil {
		t.Fatal(err)
	}
}
