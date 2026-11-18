package uplink

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestPolicyCannotPublishWhileOldAutomaticPathRemains(t *testing.T) {
	for _, available := range []bool{false, true} {
		store, err := OpenStore(filepath.Join(t.TempDir(), "policy.json"))
		if err != nil {
			t.Fatal(err)
		}
		a := &testAdapter{enabled: true, prepareUnavailable: map[string]bool{"wan1": !available, "wan2": true}}
		c := New(store, &testResolver{[]Provider{{ID: "a", Device: "wan1", Up: true}, {ID: "b", Device: "wan2", Up: true}}}, map[string]Adapter{"test": a})
		_, err = c.Put(context.Background(), Policy{ID: "test", Target: Target{Engine: "test", Ref: "proxy"}, Providers: []string{"a", "b"}, Enabled: true})
		if !available {
			if !errors.Is(err, ErrPathUnavailable) || len(store.List()) > 0 || a.device != "" {
				t.Fatal(err, store.List(), a.device)
			}
			continue
		}
		if err != nil || a.device != "wan1" || state(c).Phase != "pending" || !state(c).LastSuccess.IsZero() {
			t.Fatal(err, state(c))
		}
		c.Tick(context.Background())
		if state(c).Phase != "healthy" || len(a.activated) != 1 {
			t.Fatal(state(c), a.activated)
		}
	}
}
