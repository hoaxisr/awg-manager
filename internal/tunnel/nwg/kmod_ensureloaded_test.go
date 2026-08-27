package nwg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// fakeRunner records every external command EnsureLoaded fires and replays
// canned results keyed by command name ("modprobe", "insmod", "rmmod").
type fakeRunner struct {
	calls   []string // "name arg1 arg2 ..."
	results map[string]fakeResult
}

type fakeResult struct {
	res *exec.Result
	err error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{results: map[string]fakeResult{}}
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (*exec.Result, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	if r, ok := f.results[name]; ok {
		return r.res, r.err
	}
	return &exec.Result{ExitCode: 0}, nil
}

func (f *fakeRunner) callsWith(name string) []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, name+" ") || c == name {
			out = append(out, c)
		}
	}
	return out
}

// newEnsureLoadedTestKM wires a KmodManager whose external world is fully
// stubbed: proc I/O via procStub, commands via fakeRunner, module presence
// via booleans.
func newEnsureLoadedTestKM(loaded bool) (*KmodManager, *procStub, *fakeRunner) {
	km, stub := newKmodManagerForTest()
	fr := newFakeRunner()
	km.koPath = "/opt/etc/awg-manager/modules/awg_proxy.ko"
	km.execFn = fr.run
	km.isLoadedFn = func() bool { return loaded }
	km.modLoadedFn = func(string) bool { return false }
	return km, stub, fr
}

// FIX-2: udp_tunnel/udp_tunnel6 must be preflight-loaded BEFORE insmod of
// awg_proxy — bare insmod resolves no dependencies, and on a kernel where
// NET_UDP_TUNNEL is modular the module would otherwise fail with
// «Unknown symbol» for a v4-only user who never needed IPv6.
func TestEnsureLoaded_PreloadsDepsBeforeInsmod(t *testing.T) {
	km, _, fr := newEnsureLoadedTestKM(false)

	if err := km.EnsureLoaded(); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	insmodIdx := -1
	depIdx := map[string]int{}
	for i, c := range fr.calls {
		if strings.HasPrefix(c, "insmod "+km.koPath) {
			insmodIdx = i
		}
		for _, dep := range awgProxyDeps {
			if c == "modprobe "+dep {
				depIdx[dep] = i
			}
		}
	}
	if insmodIdx == -1 {
		t.Fatalf("insmod of awg_proxy never fired; calls=%v", fr.calls)
	}
	for _, dep := range awgProxyDeps {
		i, ok := depIdx[dep]
		if !ok {
			t.Errorf("dep %s load never attempted; calls=%v", dep, fr.calls)
			continue
		}
		if i > insmodIdx {
			t.Errorf("dep %s loaded AFTER awg_proxy insmod (idx %d > %d)", dep, i, insmodIdx)
		}
	}
}

// A dep that cannot load must NOT block the awg_proxy insmod — the insmod
// error is the authoritative verdict (the dep may be built-in).
func TestEnsureLoaded_DepFailureStillProceedsToInsmod(t *testing.T) {
	km, _, fr := newEnsureLoadedTestKM(false)
	fr.results["modprobe"] = fakeResult{
		res: &exec.Result{ExitCode: 1, Stderr: "modprobe: module udp_tunnel6 not found"},
		err: errors.New("exit status 1"),
	}

	if err := km.EnsureLoaded(); err != nil {
		t.Fatalf("EnsureLoaded must succeed when insmod succeeds despite dep failure: %v", err)
	}
	if got := fr.callsWith("insmod"); len(got) == 0 {
		t.Fatalf("awg_proxy insmod must still be attempted; calls=%v", fr.calls)
	}
}

// FIX-2: when insmod fails with «unknown symbol», the error must carry the
// Russian hint about the missing IPv6 component.
func TestEnsureLoaded_UnknownSymbolAppendsIPv6Hint(t *testing.T) {
	km, _, fr := newEnsureLoadedTestKM(false)
	fr.results["insmod"] = fakeResult{
		res: &exec.Result{ExitCode: 1, Stderr: "insmod: can't insert 'awg_proxy.ko': unknown symbol in module, or unknown parameter"},
		err: errors.New("exit status 1"),
	}

	err := km.EnsureLoaded()
	if err == nil {
		t.Fatal("EnsureLoaded must fail when insmod fails")
	}
	if !strings.Contains(err.Error(), "ядро без IPv6-компонента") {
		t.Errorf("unknown-symbol failure must append the IPv6-component hint; got: %v", err)
	}
	if !strings.Contains(err.Error(), "udp_tunnel6.ko") {
		t.Errorf("hint must name udp_tunnel6.ko; got: %v", err)
	}
}

// An insmod failure WITHOUT «unknown symbol» must not get the IPv6 hint.
func TestEnsureLoaded_OtherInsmodErrorHasNoIPv6Hint(t *testing.T) {
	km, _, fr := newEnsureLoadedTestKM(false)
	fr.results["insmod"] = fakeResult{
		res: &exec.Result{ExitCode: 1, Stderr: "insmod: can't insert 'awg_proxy.ko': out of memory"},
		err: errors.New("exit status 1"),
	}

	err := km.EnsureLoaded()
	if err == nil {
		t.Fatal("EnsureLoaded must fail when insmod fails")
	}
	if strings.Contains(err.Error(), "IPv6") {
		t.Errorf("non-symbol failure must not carry the IPv6 hint; got: %v", err)
	}
}

// FIX-3: loaded 1.2.0 < expected 1.3.0 with ZERO live slots → rmmod + insmod
// of the on-disk .ko (upgrade happens right away, no reboot needed).
func TestEnsureLoaded_UpgradesOutdatedModuleWhenIdle(t *testing.T) {
	km, stub, fr := newEnsureLoadedTestKM(true)
	stub.version = "1.2.0"
	// stub.listBody empty → loadedSlotCountLocked() == 0

	if err := km.EnsureLoaded(); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	if got := fr.callsWith("rmmod"); len(got) != 1 || got[0] != "rmmod awg_proxy" {
		t.Errorf("idle upgrade must rmmod awg_proxy once; got %v", fr.calls)
	}
	insmods := fr.callsWith("insmod")
	if len(insmods) == 0 || !strings.Contains(insmods[len(insmods)-1], km.koPath) {
		t.Errorf("idle upgrade must insmod the on-disk .ko; got %v", fr.calls)
	}
}

// FIX-3: loaded 1.2.0 < expected 1.3.0 with ACTIVE slots → upgrade deferred:
// no rmmod, no insmod, nil error (running tunnels must survive).
func TestEnsureLoaded_DefersUpgradeWhenSlotsActive(t *testing.T) {
	km, stub, fr := newEnsureLoadedTestKM(true)
	stub.version = "1.2.0"
	stub.setListSlot("203.0.113.10", 5060, 49494) // one live slot

	if err := km.EnsureLoaded(); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	if got := fr.callsWith("rmmod"); len(got) != 0 {
		t.Errorf("busy module must NOT be rmmod'ed; got %v", got)
	}
	if got := fr.callsWith("insmod"); len(got) != 0 {
		t.Errorf("busy module must NOT be re-insmod'ed; got %v", got)
	}
}

// Loaded version already >= expected → nothing to do, no commands at all.
func TestEnsureLoaded_UpToDateIsNoop(t *testing.T) {
	km, stub, fr := newEnsureLoadedTestKM(true)
	stub.version = ExpectedKmodVersion

	if err := km.EnsureLoaded(); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	if len(fr.calls) != 0 {
		t.Errorf("up-to-date module must trigger no external commands; got %v", fr.calls)
	}
}
