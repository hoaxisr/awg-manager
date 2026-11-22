package openflux

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeInputChain struct {
	rules          []string
	noComment      bool
	fail           error
	deleteAlwaysOK bool
}

func (f *fakeInputChain) run(_ context.Context, args ...string) error {
	if f.fail != nil {
		return f.fail
	}
	if len(args) < 2 || args[1] != "INPUT" {
		return errors.New("unexpected chain")
	}
	rest := args[2:]
	if args[0] == "-I" {
		rest = rest[1:]
	}
	spec := strings.Join(rest, " ")
	if f.noComment && strings.Contains(spec, "--comment") {
		return errors.New("xt_comment unavailable")
	}
	switch args[0] {
	case "-C":
		for _, r := range f.rules {
			if r == spec {
				return nil
			}
		}
		return errors.New("no matching rule")
	case "-I":
		f.rules = append([]string{spec}, f.rules...)
		return nil
	case "-D":
		if f.deleteAlwaysOK {
			return nil
		}
		for i, r := range f.rules {
			if r == spec {
				f.rules = append(f.rules[:i], f.rules[i+1:]...)
				return nil
			}
		}
		return errors.New("no matching rule")
	}
	return errors.New("unsupported op")
}

func (f *fakeInputChain) count(spec string) int {
	n := 0
	for _, r := range f.rules {
		if r == spec {
			n++
		}
	}
	return n
}

func ofRuleSpec(port int, tagged bool) string {
	return strings.Join(openfluxRuleArgs(port, tagged), " ")
}

func useFakeInputChain(t *testing.T, f *fakeInputChain) {
	t.Helper()
	prev := firewallRun
	firewallRun = f.run
	t.Cleanup(func() { firewallRun = prev })
}

func TestFirewall_PreservesForeignUntaggedRule(t *testing.T) {
	f := &fakeInputChain{
		rules: []string{
			ofRuleSpec(8445, false),
			ofRuleSpec(22, false),
		},
	}
	useFakeInputChain(t, f)

	owned, err := applyListenFirewall(context.Background(), 8445)
	if err != nil || owned {
		t.Fatalf("applyListenFirewall over foreign rule: owned=%v err=%v, want false/nil", owned, err)
	}
	if len(f.rules) != 2 {
		t.Fatalf("applyListenFirewall inserted duplicate rule: %v", f.rules)
	}

	if err := removeListenFirewall(context.Background(), 8445, owned); err != nil {
		t.Fatalf("removeListenFirewall error: %v", err)
	}
	if f.count(ofRuleSpec(8445, false)) != 1 || f.count(ofRuleSpec(22, false)) != 1 {
		t.Fatalf("foreign untagged rule was removed: %v", f.rules)
	}
}

func TestFirewall_TaggedLifecycle(t *testing.T) {
	f := &fakeInputChain{
		rules: []string{
			ofRuleSpec(8445, false), // unrelated service rule? Even on different/same port, only tagged removed when owned=false
			ofRuleSpec(9000, true),
		},
	}
	useFakeInputChain(t, f)

	owned, err := applyListenFirewall(context.Background(), 9445)
	if err != nil || owned {
		t.Fatalf("applyListenFirewall tagged: owned=%v err=%v", owned, err)
	}
	if f.count(ofRuleSpec(9445, true)) != 1 {
		t.Fatalf("expected tagged rule for 9445, got %v", f.rules)
	}

	if err := removeListenFirewall(context.Background(), 9445, owned); err != nil {
		t.Fatalf("removeListenFirewall error: %v", err)
	}
	if f.count(ofRuleSpec(9445, true)) != 0 {
		t.Fatalf("owned tagged rule still present: %v", f.rules)
	}
	if f.count(ofRuleSpec(8445, false)) != 1 || f.count(ofRuleSpec(9000, true)) != 1 {
		t.Fatalf("unrelated rules modified: %v", f.rules)
	}
}

func TestFirewall_UntaggedFallbackOwnershipAndIdempotency(t *testing.T) {
	f := &fakeInputChain{noComment: true}
	useFakeInputChain(t, f)

	owned, err := applyListenFirewall(context.Background(), 8445)
	if err != nil || !owned {
		t.Fatalf("fallback untagged apply: owned=%v err=%v, want true/nil", owned, err)
	}
	// Second apply sees existing untagged rule and does not duplicate.
	_, err2 := applyListenFirewall(context.Background(), 8445)
	if err2 != nil {
		t.Fatalf("second apply failed: %v", err2)
	}
	if f.count(ofRuleSpec(8445, false)) != 1 {
		t.Fatalf("expected exactly 1 untagged rule, got %v", f.rules)
	}

	if err := removeListenFirewall(context.Background(), 8445, owned); err != nil {
		t.Fatalf("removeListenFirewall fallback error: %v", err)
	}
	if f.count(ofRuleSpec(8445, false)) != 0 {
		t.Fatalf("expected owned untagged fallback rule removed: %v", f.rules)
	}
}

func TestFirewall_BoundedDeleteLoop(t *testing.T) {
	f := &fakeInputChain{deleteAlwaysOK: true}
	useFakeInputChain(t, f)

	done := make(chan struct{})
	go func() {
		_ = removeListenFirewall(context.Background(), 8445, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("removeListenFirewall did not terminate")
	}
}

func TestServiceFirewallErrorSurfacedInStatus(t *testing.T) {
	f := &fakeInputChain{fail: errors.New("iptables permission denied")}
	useFakeInputChain(t, f)

	svc := New(t.TempDir(), "x86_64", nil)
	svc.config = DefaultConfig()
	svc.config.OpenFirewall = true
	svc.config.Transports = []string{"direct"}
	svc.config.DirectListen = "0.0.0.0:8445"

	svc.applyFirewall(context.Background())

	st := svc.GetStatus(context.Background())
	if st.FirewallError == "" || !strings.Contains(st.FirewallError, "iptables permission denied") {
		t.Fatalf("expected FirewallError in Status, got %+v", st)
	}
	if st.Error == "" || !strings.Contains(st.Error, "iptables permission denied") {
		t.Fatalf("expected firewall failure surfaced in Status.Error too, got %+v", st)
	}

	svc.removeFirewall(context.Background())
	st2 := svc.GetStatus(context.Background())
	if st2.FirewallError != "" {
		t.Fatalf("expected FirewallError cleared after removeFirewall, got %q", st2.FirewallError)
	}
}
