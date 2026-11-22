package telemt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeChain models iptables INPUT with exact-spec -C/-I/-D semantics.
type fakeChain struct {
	rules          []string
	noComment      bool  // firmware without xt_comment: tagged specs are rejected
	fail           error // every invocation fails (iptables missing, lock, ...)
	deleteAlwaysOK bool  // pathological -D that never reports "no such rule"
	calls          int
}

func (f *fakeChain) run(_ context.Context, args ...string) error {
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	if len(args) < 2 || args[1] != "INPUT" {
		return errors.New("unexpected chain")
	}
	rest := args[2:]
	if args[0] == "-I" {
		rest = rest[1:] // position
	}
	spec := strings.Join(rest, " ")
	if f.noComment && strings.Contains(spec, "--comment") {
		return errors.New("Couldn't load match `comment'")
	}
	switch args[0] {
	case "-C":
		for _, r := range f.rules {
			if r == spec {
				return nil
			}
		}
		return errors.New("Bad rule (does a matching rule exist in that chain?)")
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
		return errors.New("Bad rule (does a matching rule exist in that chain?)")
	}
	return errors.New("unsupported op " + args[0])
}

func (f *fakeChain) count(spec string) int {
	n := 0
	for _, r := range f.rules {
		if r == spec {
			n++
		}
	}
	return n
}

func useFakeChain(t *testing.T, f *fakeChain) {
	t.Helper()
	prev := firewallRun
	firewallRun = f.run
	t.Cleanup(func() { firewallRun = prev })
}

func spec(port int, tagged bool) string { return strings.Join(telemtRuleArgs(port, tagged), " ") }

// A user-managed `-p tcp --dport 8448 -j ACCEPT` (e.g. from an NDM
// netfilter.d hook) has exactly the untagged fallback spec. Stopping
// standalone telemt must not delete it.
func TestRemoveFirewall_PreservesForeignUntaggedRule(t *testing.T) {
	f := &fakeChain{rules: []string{spec(8448, false), spec(22, false)}}
	useFakeChain(t, f)

	removeFirewall(context.Background(), 8448, false)

	if f.count(spec(8448, false)) != 1 || f.count(spec(22, false)) != 1 {
		t.Fatalf("foreign rules deleted: %q", f.rules)
	}
}

// Without xt_comment every apply used to insert another untagged copy
// because only the tagged form was checked.
func TestApplyFirewall_FallbackIsIdempotent(t *testing.T) {
	f := &fakeChain{noComment: true}
	useFakeChain(t, f)

	for i := 0; i < 3; i++ {
		_, _ = applyFirewall(context.Background(), 8448)
	}
	if n := f.count(spec(8448, false)); n != 1 {
		t.Fatalf("untagged fallback rules = %d, want 1: %q", n, f.rules)
	}
}

// A pathological runner must not make removal spin forever.
func TestRemoveFirewall_BoundedWhenDeleteNeverFails(t *testing.T) {
	f := &fakeChain{deleteAlwaysOK: true}
	useFakeChain(t, f)

	done := make(chan struct{})
	go func() { removeFirewall(context.Background(), 8448, true); close(done) }()
	select {
	case <-done:
	case <-timeAfterShort():
		t.Fatal("removeFirewall did not terminate")
	}
}

func timeAfterShort() <-chan time.Time { return time.After(2 * time.Second) }

func TestApplyFirewall_ReusesForeignUntaggedRule(t *testing.T) {
	f := &fakeChain{rules: []string{spec(8448, false)}}
	useFakeChain(t, f)

	owned, err := applyFirewall(context.Background(), 8448)
	if err != nil || owned {
		t.Fatalf("apply over foreign rule: owned=%v err=%v, want false/nil", owned, err)
	}
	if len(f.rules) != 1 {
		t.Fatalf("apply inserted a rule next to an identical foreign one: %q", f.rules)
	}
	removeFirewall(context.Background(), 8448, owned)
	if f.count(spec(8448, false)) != 1 {
		t.Fatalf("foreign rule removed: %q", f.rules)
	}
}

func TestFirewall_TaggedLifecycle(t *testing.T) {
	f := &fakeChain{rules: []string{spec(8448, false), spec(443, true)}}
	// Legacy duplicates of our tagged rule on the target port.
	f.rules = append(f.rules, spec(9443, true), spec(9443, true))
	useFakeChain(t, f)

	owned, err := applyFirewall(context.Background(), 9443)
	if err != nil || owned {
		t.Fatalf("tagged apply: owned=%v err=%v", owned, err)
	}
	if n := f.count(spec(9443, true)); n != 2 {
		t.Fatalf("apply duplicated tagged rule: %d", n)
	}
	removeFirewall(context.Background(), 9443, owned)
	if n := f.count(spec(9443, true)); n != 0 {
		t.Fatalf("tagged rules left after remove: %d", n)
	}
	if f.count(spec(8448, false)) != 1 || f.count(spec(443, true)) != 1 {
		t.Fatalf("rules for other ports touched: %q", f.rules)
	}
}

func TestFirewall_OwnedFallbackRemoved(t *testing.T) {
	f := &fakeChain{noComment: true}
	useFakeChain(t, f)

	owned, err := applyFirewall(context.Background(), 8448)
	if err != nil || !owned {
		t.Fatalf("fallback apply: owned=%v err=%v, want true/nil", owned, err)
	}
	removeFirewall(context.Background(), 8448, owned)
	if len(f.rules) != 0 {
		t.Fatalf("owned fallback rule left: %q", f.rules)
	}
}

func TestFirewall_IptablesFailure(t *testing.T) {
	f := &fakeChain{fail: errors.New("iptables: not found")}
	useFakeChain(t, f)

	owned, err := applyFirewall(context.Background(), 8448)
	if err == nil || owned {
		t.Fatalf("apply with failing iptables: owned=%v err=%v", owned, err)
	}
	f.calls = 0
	removeFirewall(context.Background(), 8448, true)
	if f.calls > 2 {
		t.Fatalf("remove with failing iptables made %d calls", f.calls)
	}
	if _, err := applyFirewall(context.Background(), 0); err == nil {
		t.Fatal("apply accepted port 0")
	}
}

// Service-level ownership: the untagged rule is removed only by the process
// that inserted it, including after a port change.
func TestService_FirewallOwnership(t *testing.T) {
	ctx := context.Background()

	t.Run("foreign rule survives stop", func(t *testing.T) {
		f := &fakeChain{noComment: true, rules: []string{spec(8448, false)}}
		useFakeChain(t, f)
		svc := New(t.TempDir(), "x86_64")
		svc.config.Port = 8448

		svc.openFirewallLocked(ctx)
		_ = svc.stopProcess(ctx)
		if f.count(spec(8448, false)) != 1 {
			t.Fatalf("foreign rule removed by stop: %q", f.rules)
		}
	})

	t.Run("own fallback removed on stop", func(t *testing.T) {
		f := &fakeChain{noComment: true}
		useFakeChain(t, f)
		svc := New(t.TempDir(), "x86_64")
		svc.config.Port = 8448

		svc.openFirewallLocked(ctx)
		svc.openFirewallLocked(ctx) // "already running" branch re-applies
		if n := f.count(spec(8448, false)); n != 1 {
			t.Fatalf("fallback rules after double open = %d", n)
		}
		_ = svc.stopProcess(ctx)
		if len(f.rules) != 0 {
			t.Fatalf("own fallback left after stop: %q", f.rules)
		}
	})

	t.Run("port change closes old owned rule", func(t *testing.T) {
		f := &fakeChain{noComment: true, rules: []string{spec(1443, false)}}
		useFakeChain(t, f)
		svc := New(t.TempDir(), "x86_64")
		svc.config.Port = 8448
		svc.openFirewallLocked(ctx)

		svc.config.Port = 1443 // user's own rule already opens 1443
		svc.openFirewallLocked(ctx)
		if f.count(spec(8448, false)) != 0 {
			t.Fatalf("old owned rule not closed on port change: %q", f.rules)
		}
		_ = svc.stopProcess(ctx)
		if f.count(spec(1443, false)) != 1 {
			t.Fatalf("foreign 1443 rule removed: %q", f.rules)
		}
	})
}
