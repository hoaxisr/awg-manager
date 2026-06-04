package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeExec struct {
	calls    []fakeCall
	err      error
	runIPErr error
}

type fakeCall struct {
	kind  string
	args  []string
	stdin string
}

// errENOENT mimics the kernel's "rule not found" exit so the drain
// loops terminate after a single pass — without this, fakeExec.runIP
// returning nil for `ip rule del` causes the cap-bounded drain loop
// to record N entries (or, before the cap, to OOM the test process).
var errENOENT = errIPRule("RTNETLINK answers: No such file or directory")

type errIPRule string

func (e errIPRule) Error() string { return string(e) }

func (f *fakeExec) restoreNoflush(_ context.Context, input string) error {
	f.calls = append(f.calls, fakeCall{kind: "restore", stdin: input})
	return f.err
}

func (f *fakeExec) runIPTables(_ context.Context, args ...string) error {
	f.calls = append(f.calls, fakeCall{kind: "iptables", args: args})
	return f.err
}

func (f *fakeExec) runIP(_ context.Context, args ...string) error {
	f.calls = append(f.calls, fakeCall{kind: "ip", args: args})
	if f.runIPErr != nil {
		return f.runIPErr
	}
	if f.err != nil {
		return f.err
	}
	// Make `ip rule del fwmark ...` return ENOENT after the first call
	// so drain loops don't append forever.
	if len(args) >= 4 && args[0] == "rule" && args[1] == "del" {
		return errENOENT
	}
	return nil
}

func newFakeIPTables(fe *fakeExec) *IPTables {
	return &IPTables{
		restoreNoflush: fe.restoreNoflush,
		runIPTables:    fe.runIPTables,
		runIPTablesOut: func(_ context.Context, _ ...string) (string, error) { return jumpsPresentDump(), nil },
		runIP:          fe.runIP,
	}
}

// jumpsPresentDump mimics `iptables -S <table>` output for a fully-installed
// engine: both chain declarations AND their PREROUTING jumps. Used as the
// default runIPTablesOut in tests that don't model a jump loss, so Probe
// reports installed+jumps. The same dump serves the mangle and nat probes
// (each scans for its own chain).
func jumpsPresentDump() string {
	return "-P PREROUTING ACCEPT\n" +
		"-N " + ChainName + "\n" +
		"-N " + RedirectChain + "\n" +
		"-A PREROUTING -m conntrack ! --ctstate INVALID -j " + ChainName + "\n" +
		"-A PREROUTING -m conntrack ! --ctstate INVALID -j " + RedirectChain + "\n"
}

func newFakeExec() *fakeExec {
	return &fakeExec{}
}

// The netfilter.d hook runs on the live router on every NDMS reload — a
// syntax error would break on each reload. Validate the generated shell.
func TestNetfilterHookScript_ValidShell(t *testing.T) {
	script := netfilterHookScript()

	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated netfilter.d hook is not valid sh: %v\n%s", err, out)
	}

	// The fix: the install gate must check the PREROUTING jump, not just the
	// chain. Both per-table gates must carry the anchored jump grep.
	if !strings.Contains(script, "grep -qE -- '-[jg] "+ChainName+"($| )'") {
		t.Error("hook missing mangle jump-presence gate")
	}
	if !strings.Contains(script, "grep -qE -- '-[jg] "+RedirectChain+"($| )'") {
		t.Error("hook missing nat jump-presence gate")
	}
}

func TestBuildTProxyModulePath(t *testing.T) {
	got := buildTProxyModulePath("5.15.0-mips")
	want := "/lib/modules/5.15.0-mips/xt_TPROXY.ko"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestKernelModuleName(t *testing.T) {
	if kernelModuleName() != "xt_TPROXY" {
		t.Errorf("got %q", kernelModuleName())
	}
}

// EnsureCommentModule is best-effort: NDMS on some OS 5.x EA builds
// doesn't auto-load xt_comment because it doesn't use `-m comment`
// itself, but our DNS-NOPOLICY rules do. We push the load ourselves
// — and if the .ko file is absent (module possibly built-in), we
// must NOT block Enable: the kernel either accepts comment match
// natively, or iptables-restore later surfaces a concrete error.
//
// Encountered on a Keenetic NC-1812 (MT7988 aarch64, OS 5.00.C.11.0-0
// EA): xt_comment.ko was present in /lib/modules but unloaded, and
// the AWGM router refused to install with "iptables-restore: line N
// failed" until xt_comment was manually insmod'd. See issue #130.
func TestEnsureCommentModule_MissingKoIsNotFatal(t *testing.T) {
	orig := ensureKernelModuleFn
	ensureKernelModuleFn = func(_ context.Context, _ string) error {
		return ErrNetfilterComponentMissing
	}
	t.Cleanup(func() { ensureKernelModuleFn = orig })

	if err := EnsureCommentModule(context.Background()); err != nil {
		t.Errorf("expected nil when .ko absent (built-in fallback), got %v", err)
	}
}

func TestEnsureCommentModule_PassesThroughInsmodErrors(t *testing.T) {
	orig := ensureKernelModuleFn
	insmodErr := errors.New("insmod xt_comment.ko: out of memory")
	ensureKernelModuleFn = func(_ context.Context, _ string) error {
		return insmodErr
	}
	t.Cleanup(func() { ensureKernelModuleFn = orig })

	err := EnsureCommentModule(context.Background())
	if err == nil {
		t.Fatal("expected error to surface, got nil")
	}
	if !errors.Is(err, insmodErr) {
		t.Errorf("expected wrapped insmod error, got %v", err)
	}
}

func TestEnsureCommentModule_LoadsSuccessfully(t *testing.T) {
	orig := ensureKernelModuleFn
	called := false
	ensureKernelModuleFn = func(_ context.Context, name string) error {
		called = true
		if name != "xt_comment" {
			t.Errorf("expected module name xt_comment, got %q", name)
		}
		return nil
	}
	t.Cleanup(func() { ensureKernelModuleFn = orig })

	if err := EnsureCommentModule(context.Background()); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if !called {
		t.Error("expected ensureKernelModuleFn to be invoked")
	}
}

func TestBuildRestoreInput_PolicyMark_JumpHasFilter(t *testing.T) {
	spec := RestoreInputSpec{PolicyMark: "0xffffaaa"}
	out := buildRestoreInput(spec)

	// Literal SKeen jump (set_prerouting_rules, skeen.sh:1383). No `-p`
	// on the jump — SKeen jumps unconditionally and per-proto filtering
	// happens inside the chain. `-j` (not `-g`) so RETURN bypasses unwind
	// cleanly. `-A PREROUTING` (append) so we run AFTER NDMS _NDM_*
	// chains set the connmark.
	wantMangle := "-A PREROUTING -m connmark --mark 0xffffaaa -m conntrack ! --ctstate INVALID -j " + ChainName
	if !strings.Contains(out, wantMangle) {
		t.Errorf("missing mangle PREROUTING jump\nwant: %s\ngot:\n%s", wantMangle, out)
	}
	wantNat := "-A PREROUTING -m connmark --mark 0xffffaaa -m conntrack ! --ctstate INVALID -j " + RedirectChain
	if !strings.Contains(out, wantNat) {
		t.Errorf("missing nat PREROUTING jump\nwant: %s\ngot:\n%s", wantNat, out)
	}
	// JUMP must NOT carry a `-p` matcher (this was our deviation from SKeen).
	for _, bad := range []string{
		"-m conntrack ! --ctstate INVALID -p udp -j " + ChainName,
		"-m conntrack ! --ctstate INVALID -p tcp -j " + RedirectChain,
	} {
		if strings.Contains(out, bad) {
			t.Errorf("PREROUTING jump must not carry `-p` matcher:\nfound: %s\nin:\n%s", bad, out)
		}
	}

	// Legacy/transitional forms MUST be gone:
	//   - `-g chain` (goto): replaced by `-j` for SKeen-style RETURN bypass
	//   - `-I PREROUTING N`: never in restore stdin
	//   - in-chain `-m connmark ! --mark POLICY -j ACCEPT`: filter moved to jump
	for _, bad := range []string{
		"-g " + ChainName,
		"-g " + RedirectChain,
		"-I PREROUTING",
		"-A " + ChainName + " -m connmark !",
		"-A " + RedirectChain + " -m connmark !",
		"-m conntrack --ctdir REPLY",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("forbidden fragment %q must not appear:\n%s", bad, out)
		}
	}
}

func TestBuildRestoreInput_AllDevicesMode_UnconditionalPrerouting(t *testing.T) {
	spec := RestoreInputSpec{MatchAll: true}
	out := buildRestoreInput(spec)
	wantMangle := "-A PREROUTING -m conntrack ! --ctstate INVALID -j " + ChainName
	if !strings.Contains(out, wantMangle) {
		t.Errorf("missing unconditional mangle PREROUTING jump\nwant: %s\ngot:\n%s", wantMangle, out)
	}
	wantNat := "-A PREROUTING -m conntrack ! --ctstate INVALID -j " + RedirectChain
	if !strings.Contains(out, wantNat) {
		t.Errorf("missing unconditional nat PREROUTING jump\nwant: %s\ngot:\n%s", wantNat, out)
	}
	if strings.Contains(out, "-m connmark --mark") {
		t.Errorf("all-devices mode must not include policy connmark filter:\n%s", out)
	}
}

func TestBuildRestoreInput_EmptyMark_NoPrerouting(t *testing.T) {
	spec := RestoreInputSpec{PolicyMark: ""}
	out := buildRestoreInput(spec)
	if strings.Contains(out, "-A PREROUTING") || strings.Contains(out, "-I PREROUTING") {
		t.Errorf("expected no PREROUTING entry for empty mark, got:\n%s", out)
	}
}

func TestBuildRestoreInput_NoDNSOffloadChain(t *testing.T) {
	// SKeen-style routing drops AWGM-DNS-OFFLOAD entirely: with policy
	// filter on the jump, non-policy DNS never reaches our chains. No
	// `-m addrtype --dst-type LOCAL` (xt_addrtype dependency), no
	// `-i br+`, no `-I PREROUTING 1`.
	out := buildRestoreInput(RestoreInputSpec{PolicyMark: "0xffffaaa"})
	for _, bad := range []string{
		"AWGM-DNS-OFFLOAD",
		"addrtype",
		"br+",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("forbidden DNS-OFFLOAD fragment %q must not appear:\n%s", bad, out)
		}
	}
}

func TestBuildRestoreInput_BypassUsesReturn(t *testing.T) {
	// With `-j` jump (SKeen-style) bypass rules MUST use RETURN, not
	// ACCEPT — RETURN unwinds back to PREROUTING and lets NDMS rules
	// after our jump (if any) take their course. ACCEPT would terminate
	// the table prematurely.
	out := buildRestoreInput(RestoreInputSpec{PolicyMark: "0xffffaaa"})

	for _, want := range []string{
		"-A AWGM-TPROXY -d 127.0.0.0/8 -j RETURN",
		"-A AWGM-TPROXY -d 192.168.0.0/16 -j RETURN",
		"-A AWGM-REDIRECT -d 127.0.0.0/8 -j RETURN",
		"-A AWGM-REDIRECT -d 192.168.0.0/16 -j RETURN",
		"-A AWGM-REDIRECT -p tcp --dport 79 -j RETURN",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing RETURN bypass: %s\nin:\n%s", want, out)
		}
	}
	// Legacy ACCEPT bypasses (pre-SKeen) must be gone.
	for _, bad := range []string{
		"-A AWGM-TPROXY -d 127.0.0.0/8 -j ACCEPT",
		"-A AWGM-REDIRECT -d 127.0.0.0/8 -j ACCEPT",
		// `-m mark --mark 0xff` not in SKeen — must not appear at all.
		"-m mark --mark 0xff",
		// TCP DNS-specific REDIRECT not in SKeen — catch-all handles it.
		"-A AWGM-REDIRECT -p tcp --dport 53 -j REDIRECT",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("non-SKeen fragment %q must not be present:\n%s", bad, out)
		}
	}
}

func TestBuildRestoreInput_TablesAndRulesPresent(t *testing.T) {
	input := buildRestoreInput(RestoreInputSpec{PolicyMark: "0xffffaaa"})

	expected := []string{
		// mangle table — literal SKeen hybrid mode
		"*mangle",
		":AWGM-TPROXY - [0:0]",
		"-A AWGM-TPROXY -p udp --dport 53 -j TPROXY --on-port 51271 --on-ip 127.0.0.1 --tproxy-mark 0x1",
		"-A AWGM-TPROXY -d 127.0.0.0/8 -j RETURN",
		"-A AWGM-TPROXY -d 192.168.0.0/16 -j RETURN",
		"-A AWGM-TPROXY -p udp -j TPROXY --on-port 51271 --on-ip 127.0.0.1 --tproxy-mark 0x1",
		// nat table — literal SKeen hybrid mode
		"*nat",
		":AWGM-REDIRECT - [0:0]",
		"-A AWGM-REDIRECT -d 127.0.0.0/8 -j RETURN",
		"-A AWGM-REDIRECT -d 192.168.0.0/16 -j RETURN",
		"-A AWGM-REDIRECT -p tcp --dport 79 -j RETURN",
		"-A AWGM-REDIRECT -p tcp -j REDIRECT --to-ports 51272",
		"COMMIT",
	}
	for _, line := range expected {
		if !strings.Contains(input, line) {
			t.Errorf("missing line: %q\nin:\n%s", line, input)
		}
	}
	// TCP TPROXY MUST NOT appear in mangle (we moved TCP to nat REDIRECT).
	if strings.Contains(input, "-A AWGM-TPROXY -p tcp -j TPROXY") {
		t.Errorf("legacy TCP TPROXY rule must not be present:\n%s", input)
	}
}

func TestIPTablesInstallSequence(t *testing.T) {
	fe := &fakeExec{}
	it := newFakeIPTables(fe)
	if err := it.Install(context.Background(), RestoreInputSpec{PolicyMark: "0xffffaaa"}); err != nil {
		t.Fatal(err)
	}
	// removeSourceHooks scans mangle+nat PREROUTING, then iptables-restore,
	// then `ip rule del` drain, `ip rule add`, `ip route add`. After the
	// SKeen-style port there is NO separate `iptables -t nat -I PREROUTING`
	// call — the only PREROUTING jumps are emitted by iptables-restore.
	var (
		restoreSeen   bool
		ruleAddSeen   bool
		ruleAddArgs   string
		routeAddSeen  bool
		ruleDrainSeen bool
	)
	for _, c := range fe.calls {
		switch c.kind {
		case "restore":
			restoreSeen = true
			if !strings.Contains(c.stdin, "AWGM-TPROXY") {
				t.Errorf("restore stdin missing AWGM-TPROXY:\n%s", c.stdin)
			}
			if !strings.Contains(c.stdin, "AWGM-REDIRECT") {
				t.Errorf("restore stdin missing AWGM-REDIRECT:\n%s", c.stdin)
			}
			if strings.Contains(c.stdin, "AWGM-DNS-OFFLOAD") {
				t.Errorf("DNS-OFFLOAD chain must not appear in restore stdin:\n%s", c.stdin)
			}
		case "iptables":
			args := strings.Join(c.args, " ")
			if strings.Contains(args, "AWGM-DNS-OFFLOAD") {
				t.Errorf("no DNS-OFFLOAD iptables calls expected, got: %q", args)
			}
		case "ip":
			args := strings.Join(c.args, " ")
			if strings.Contains(args, "rule del fwmark") {
				ruleDrainSeen = true
			}
			if strings.Contains(args, "rule add fwmark") {
				ruleAddSeen = true
				ruleAddArgs = args
			}
			if strings.Contains(args, "route add local") {
				routeAddSeen = true
			}
		}
	}
	if !restoreSeen {
		t.Errorf("expected iptables-restore call")
	}
	if !ruleDrainSeen {
		t.Errorf("expected ip rule del drain pass")
	}
	if !ruleAddSeen || !strings.Contains(ruleAddArgs, "priority 30000") {
		t.Errorf("expected ip rule add with priority 30000, got %q", ruleAddArgs)
	}
	if !routeAddSeen {
		t.Errorf("expected ip route add local")
	}
}

func TestIPTablesUninstallSequence(t *testing.T) {
	fe := &fakeExec{err: nil}
	it := newFakeIPTables(fe)
	if err := it.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fe.calls) < 3 {
		t.Errorf("expected >=3 calls, got %d", len(fe.calls))
	}
	// Uninstall must not touch AWGM-DNS-OFFLOAD (it's gone).
	for _, c := range fe.calls {
		if c.kind == "iptables" {
			for _, a := range c.args {
				if strings.Contains(a, "AWGM-DNS-OFFLOAD") {
					t.Errorf("Uninstall referenced removed chain AWGM-DNS-OFFLOAD: %v", c.args)
				}
			}
		}
	}
}

func TestWriteNetfilterHookContainsPidofGuard(t *testing.T) {
	tmp := t.TempDir()
	orig := netfilterHookPath
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	t.Cleanup(func() { netfilterHookPath = orig })

	if err := writeNetfilterHook(); err != nil {
		t.Fatalf("writeNetfilterHook: %v", err)
	}
	data, err := os.ReadFile(netfilterHookPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "pidof sing-box >/dev/null 2>&1 || exit 0") {
		t.Errorf("hook missing pidof guard:\n%s", body)
	}
	if !strings.Contains(body, "iptables-restore --noflush") {
		t.Errorf("hook missing restore line:\n%s", body)
	}
}

func TestWriteNetfilterHookPreloadsModules(t *testing.T) {
	tmp := t.TempDir()
	orig := netfilterHookPath
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	t.Cleanup(func() { netfilterHookPath = orig })

	if err := writeNetfilterHook(); err != nil {
		t.Fatalf("writeNetfilterHook: %v", err)
	}
	data, err := os.ReadFile(netfilterHookPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(data)

	// The hook must contain the module preload loop with all known modules.
	for _, mod := range []string{"xt_TPROXY", "xt_comment", "xt_mark", "xt_connmark", "xt_conntrack", "xt_pkttype"} {
		if !strings.Contains(body, mod) {
			t.Errorf("hook missing module preload entry for %q:\n%s", mod, body)
		}
	}
	// insmod path must use /lib/modules/${KREL}
	if !strings.Contains(body, `"/lib/modules/${KREL}/${mod}.ko"`) {
		t.Errorf("hook missing /lib/modules/${KREL} insmod path:\n%s", body)
	}
	// best-effort: the loop must not fail hard — || true at end of insmod line.
	if !strings.Contains(body, "insmod") || !strings.Contains(body, "|| true") {
		t.Errorf("hook insmod block must use best-effort (|| true):\n%s", body)
	}
}

func TestWriteNetfilterHookHasScrub(t *testing.T) {
	tmp := t.TempDir()
	orig := netfilterHookPath
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	t.Cleanup(func() { netfilterHookPath = orig })

	if err := writeNetfilterHook(); err != nil {
		t.Fatalf("writeNetfilterHook: %v", err)
	}
	data, _ := os.ReadFile(netfilterHookPath)
	body := string(data)

	// Scrub block: NDMS reloads can flush one table but not the other.
	// Without scrubbing existing PREROUTING jumps before iptables-restore,
	// --noflush would append a duplicate jump on top of the surviving one.
	wants := []string{
		"-[jg] AWGM-TPROXY",
		"-[jg] AWGM-REDIRECT",
		"-D PREROUTING",
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("hook missing scrub fragment %q:\n%s", w, body)
		}
	}
	// DNS-OFFLOAD references must be gone from the hook.
	if strings.Contains(body, "AWGM-DNS-OFFLOAD") {
		t.Errorf("hook still references removed AWGM-DNS-OFFLOAD chain:\n%s", body)
	}
	// Scrub must come BEFORE the restore.
	scrubIdx := strings.Index(body, "-D PREROUTING")
	restoreIdx := strings.Index(body, "iptables-restore --noflush")
	if scrubIdx < 0 || restoreIdx < 0 || scrubIdx > restoreIdx {
		t.Errorf("scrub must precede restore: scrub=%d restore=%d", scrubIdx, restoreIdx)
	}
}

func TestRemoveNetfilterRulesFile(t *testing.T) {
	tmp := t.TempDir()
	orig := netfilterRulesPath
	netfilterRulesPath = filepath.Join(tmp, "router-netfilter.rules")
	t.Cleanup(func() { netfilterRulesPath = orig })

	if err := os.WriteFile(netfilterRulesPath, []byte("dummy"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	removeNetfilterRulesFile()
	if _, err := os.Stat(netfilterRulesPath); !os.IsNotExist(err) {
		t.Errorf("expected file to be gone, got err=%v", err)
	}
	// Idempotent — second call must not panic.
	removeNetfilterRulesFile()
}

func TestRefreshNetfilterHookIfPresent(t *testing.T) {
	tmp := t.TempDir()
	orig := netfilterHookPath
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	t.Cleanup(func() { netfilterHookPath = orig })

	// No file → no-op (does not create one).
	refreshNetfilterHookIfPresent()
	if _, err := os.Stat(netfilterHookPath); !os.IsNotExist(err) {
		t.Errorf("expected no file, got err=%v", err)
	}

	// File present → rewrite with current content (and our pidof guard).
	if err := os.WriteFile(netfilterHookPath, []byte("# stale old version\n"), 0755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	refreshNetfilterHookIfPresent()
	data, _ := os.ReadFile(netfilterHookPath)
	if !strings.Contains(string(data), "pidof sing-box") {
		t.Errorf("expected refreshed hook with pidof, got:\n%s", data)
	}
}

func TestInstall_IdempotentOnFileExists(t *testing.T) {
	// After the runIP fix (Task 1 of wizard cleanup), stderr from `ip` is
	// appended to err.Error() via sysexec.FormatError. The substring guards
	// in Install() catch "File exists" and silently swallow the error so a
	// re-Install on already-installed routes/rules is a no-op.
	rec := newFakeExec()
	it := &IPTables{
		restoreNoflush: rec.restoreNoflush,
		runIPTables:    rec.runIPTables,
		runIP:          rec.runIP,
		persistRules:   func(string) error { return nil },
		persistHook:    func() error { return nil },
		cleanupHook:    func() {},
	}
	if err := it.Install(context.Background(), RestoreInputSpec{PolicyMark: "0xff"}); err != nil {
		t.Fatalf("first Install: %v", err)
	}

	// Simulate "File exists" failure on subsequent ip-rule/ip-route add.
	rec.runIPErr = errors.New("exit status 2 (exit 2, stderr: RTNETLINK answers: File exists)")
	if err := it.Install(context.Background(), RestoreInputSpec{PolicyMark: "0xff"}); err != nil {
		t.Fatalf("second Install (idempotent): %v", err)
	}
}

func TestBuildRestoreInput_ExpandedBypassCIDRs(t *testing.T) {
	input := buildRestoreInput(RestoreInputSpec{PolicyMark: "0xffffaaa"})

	// New CIDRs that close edge cases SKeen covered:
	// - CGNAT (RFC 6598) — ISPs deploying carrier-grade NAT
	// - 0.0.0.0/8 "this network" (RFC 1122) — never routable
	// - 192.0.0.0/24 IETF Protocol Assignments — includes NAT64 well-known
	expected := []string{
		"-A AWGM-TPROXY -d 100.64.0.0/10 -j RETURN",
		"-A AWGM-TPROXY -d 0.0.0.0/8 -j RETURN",
		"-A AWGM-TPROXY -d 192.0.0.0/24 -j RETURN",
		"-A AWGM-REDIRECT -d 100.64.0.0/10 -j RETURN",
		"-A AWGM-REDIRECT -d 0.0.0.0/8 -j RETURN",
		"-A AWGM-REDIRECT -d 192.0.0.0/24 -j RETURN",
	}
	for _, line := range expected {
		if !strings.Contains(input, line) {
			t.Errorf("missing expanded-bypass line: %q\nin:\n%s", line, input)
		}
	}
}

func TestBuildRestoreInput_DNSInterceptUDP(t *testing.T) {
	input := buildRestoreInput(RestoreInputSpec{PolicyMark: "0xffffaaa"})

	// DNS rule MUST exist in AWGM-TPROXY: -p udp --dport 53 -j TPROXY ...
	wantDNS := "-A AWGM-TPROXY -p udp --dport 53 -j TPROXY --on-port 51271 --on-ip 127.0.0.1 --tproxy-mark 0x1"
	if !strings.Contains(input, wantDNS) {
		t.Errorf("missing DNS UDP TPROXY rule\nwant: %s\ngot:\n%s", wantDNS, input)
	}

	// CRITICAL ORDERING: DNS rule MUST precede the 192.168.0.0/16 bypass.
	// Otherwise DNS-to-router-LAN-IP gets bypassed before the DNS rule fires.
	dnsIdx := strings.Index(input, wantDNS)
	bypassIdx := strings.Index(input, "-A AWGM-TPROXY -d 192.168.0.0/16 -j RETURN")
	if dnsIdx < 0 || bypassIdx < 0 {
		t.Fatalf("DNS or bypass rule not found")
	}
	if dnsIdx > bypassIdx {
		t.Errorf("DNS rule at offset %d must precede 192.168/16 bypass at offset %d", dnsIdx, bypassIdx)
	}
}

func TestBuildRestoreInput_TCPCatchAllHandlesDNS(t *testing.T) {
	input := buildRestoreInput(RestoreInputSpec{PolicyMark: "0xffffaaa"})

	// SKeen's nat chain (`add_redirect_rules`) has NO dport-53-specific
	// rule; the catch-all `-p tcp -j REDIRECT` covers TCP DNS too. Verify
	// (a) the explicit DNS rule is absent and (b) the catch-all is present
	// and lands AFTER the bypasses (so private/router IPs still RETURN).
	if strings.Contains(input, "-A AWGM-REDIRECT -p tcp --dport 53") {
		t.Errorf("explicit TCP DNS rule must not appear (SKeen handles via catch-all):\n%s", input)
	}
	wantCatch := "-A AWGM-REDIRECT -p tcp -j REDIRECT --to-ports 51272"
	if !strings.Contains(input, wantCatch) {
		t.Errorf("missing TCP catch-all REDIRECT:\n%s", input)
	}
	catchIdx := strings.Index(input, wantCatch)
	bypassIdx := strings.Index(input, "-A AWGM-REDIRECT -d 192.168.0.0/16 -j RETURN")
	if catchIdx < bypassIdx {
		t.Errorf("TCP catch-all (%d) must come after bypasses (%d)", catchIdx, bypassIdx)
	}
}

func TestBuildRestoreInput_WANIPsRendered(t *testing.T) {
	// Synthetic RFC 5737 TEST-NET-3 + RFC 1918 — mirrors a real multi-WAN
	// router with public WAN + tunnel addresses.
	spec := RestoreInputSpec{
		PolicyMark: "0xffffaaa",
		WANIPs:     []string{"203.0.113.207/32", "10.8.1.3/32"},
	}
	input := buildRestoreInput(spec)

	// WAN-IP rules MUST appear in BOTH chains as RETURN bypasses.
	expected := []string{
		"-A AWGM-TPROXY -d 203.0.113.207/32 -j RETURN",
		"-A AWGM-TPROXY -d 10.8.1.3/32 -j RETURN",
		"-A AWGM-REDIRECT -d 203.0.113.207/32 -j RETURN",
		"-A AWGM-REDIRECT -d 10.8.1.3/32 -j RETURN",
	}
	for _, line := range expected {
		if !strings.Contains(input, line) {
			t.Errorf("missing WAN-IP line: %q\nin:\n%s", line, input)
		}
	}
}

func TestBuildRestoreInput_EmptyWANIPs_NoExclusions(t *testing.T) {
	spec := RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: nil}
	input := buildRestoreInput(spec)

	// No /32 host-routes should appear other than 255.255.255.255/32.
	for _, line := range strings.Split(input, "\n") {
		if strings.Contains(line, "/32 -j RETURN") && !strings.Contains(line, "255.255.255.255") {
			t.Errorf("unexpected /32 exclusion when WANIPs empty: %s", line)
		}
	}
}

func TestBuildRestoreInput_LANBridges_DNSRescueRules(t *testing.T) {
	// LAN bridges with discovered ndnproxy ports → DNS-RESCUE REDIRECT
	// rules in nat PREROUTING that short-circuit DNS for mark=0 packets
	// to the per-policy ndnproxy port, bypassing NDMS's
	// _NDM_DNS_FLT_REDIR catch-all (which would land them on the
	// sing-box-hijacked :53).
	spec := RestoreInputSpec{
		PolicyMark: "0xffffaae",
		LANBridges: []LANBridgeDNSRedir{
			{Bridge: "br0", Port: 41100},
			{Bridge: "br1", Port: 41100},
		},
	}
	input := buildRestoreInput(spec)

	expected := []string{
		`-I PREROUTING 1 -i br0 -m mark --mark 0x0 -m pkttype --pkt-type unicast -p udp --dport 53 -m comment --comment "AWGM-DNS-RESCUE" -j REDIRECT --to-ports 41100`,
		`-I PREROUTING 1 -i br0 -m mark --mark 0x0 -m pkttype --pkt-type unicast -p tcp --dport 53 -m comment --comment "AWGM-DNS-RESCUE" -j REDIRECT --to-ports 41100`,
		`-I PREROUTING 1 -i br1 -m mark --mark 0x0 -m pkttype --pkt-type unicast -p udp --dport 53 -m comment --comment "AWGM-DNS-RESCUE" -j REDIRECT --to-ports 41100`,
		`-I PREROUTING 1 -i br1 -m mark --mark 0x0 -m pkttype --pkt-type unicast -p tcp --dport 53 -m comment --comment "AWGM-DNS-RESCUE" -j REDIRECT --to-ports 41100`,
	}
	for _, line := range expected {
		if !strings.Contains(input, line) {
			t.Errorf("missing DNS-RESCUE line: %q\nin:\n%s", line, input)
		}
	}
}

func TestBuildRestoreInput_LANBridges_DifferentPortsPerBridge(t *testing.T) {
	// Sanity: per-bridge port wired through when bridges resolve to
	// different ndnproxy ports (different NDMS policies attached to
	// different bridges). Each bridge gets its OWN REDIRECT target.
	spec := RestoreInputSpec{
		PolicyMark: "0xffffaae",
		LANBridges: []LANBridgeDNSRedir{
			{Bridge: "br0", Port: 41100},
			{Bridge: "br1", Port: 41101},
		},
	}
	input := buildRestoreInput(spec)

	if !strings.Contains(input, `-I PREROUTING 1 -i br0 -m mark --mark 0x0 -m pkttype --pkt-type unicast -p udp --dport 53 -m comment --comment "AWGM-DNS-RESCUE" -j REDIRECT --to-ports 41100`) {
		t.Errorf("br0 should redirect to 41100")
	}
	if !strings.Contains(input, `-I PREROUTING 1 -i br1 -m mark --mark 0x0 -m pkttype --pkt-type unicast -p udp --dport 53 -m comment --comment "AWGM-DNS-RESCUE" -j REDIRECT --to-ports 41101`) {
		t.Errorf("br1 should redirect to 41101")
	}
}

func TestBuildRestoreInput_NoLANBridges_NoDNSRescueRules(t *testing.T) {
	// Empty LANBridges → no DNS-RESCUE rules emitted at all. Caller
	// (Service.Enable) skips DNS rescue entirely on routers without
	// _NDM_HOTSPOT_DNSREDIR entries.
	spec := RestoreInputSpec{
		PolicyMark: "0xffffaae",
		LANBridges: nil,
	}
	input := buildRestoreInput(spec)

	for _, marker := range []string{"AWGM-DNS-RESCUE", "--to-ports 41"} {
		if strings.Contains(input, marker) {
			t.Errorf("DNS-RESCUE artifact %q leaked into output when LANBridges empty:\n%s", marker, input)
		}
	}
}

func TestEqualLANBridges(t *testing.T) {
	a := []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}, {Bridge: "br1", Port: 41100}}
	b := []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}, {Bridge: "br1", Port: 41100}}
	c := []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}, {Bridge: "br1", Port: 41101}} // different port
	d := []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}}                               // shorter
	e := []LANBridgeDNSRedir{{Bridge: "br1", Port: 41100}, {Bridge: "br0", Port: 41100}} // different order

	if !equalLANBridges(a, b) {
		t.Error("identical slices must compare equal")
	}
	if equalLANBridges(a, c) {
		t.Error("differing port must not compare equal")
	}
	if equalLANBridges(a, d) {
		t.Error("differing length must not compare equal")
	}
	if equalLANBridges(a, e) {
		t.Error("differing order must not compare equal (caller relies on stable order)")
	}
	if !equalLANBridges(nil, nil) {
		t.Error("nil/nil must compare equal")
	}
	if !equalLANBridges([]LANBridgeDNSRedir{}, nil) {
		t.Error("empty and nil must compare equal")
	}
}

func TestParseDNSRedirRule(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		wantOK    bool
		wantIface string
		wantMark  string
		wantPort  int
	}{
		{
			name:      "udp 53 redirect — match (sing-box mark)",
			line:      "-A _NDM_HOTSPOT_DNSREDIR -d 192.168.0.1/32 -i br0 -p udp -m mark --mark 0xffffaae -m pkttype --pkt-type unicast -m udp --dport 53 -j REDIRECT --to-ports 41104",
			wantOK:    true,
			wantIface: "br0",
			wantMark:  "0xffffaae",
			wantPort:  41104,
		},
		{
			name:      "tcp 53 redirect — match (provider mark)",
			line:      "-A _NDM_HOTSPOT_DNSREDIR -d 192.168.2.1/32 -i br1 -p tcp -m mark --mark 0xffffaaa -m pkttype --pkt-type unicast -m tcp --dport 53 -j REDIRECT --to-ports 41100",
			wantOK:    true,
			wantIface: "br1",
			wantMark:  "0xffffaaa",
			wantPort:  41100,
		},
		{
			name:   "port 1900 (SSDP) — skip",
			line:   "-A _NDM_HOTSPOT_DNSREDIR -d 192.168.0.1/32 -i br0 -p udp -m mark --mark 0xffffaae -m pkttype --pkt-type unicast -m udp --dport 1900 -j REDIRECT --to-ports 41308",
			wantOK: false,
		},
		{
			name:   "port 5351 (NAT-PMP) — skip",
			line:   "-A _NDM_HOTSPOT_DNSREDIR -d 192.168.0.1/32 -i br0 -p udp -m mark --mark 0xffffaae -m pkttype --pkt-type unicast -m udp --dport 5351 -j REDIRECT --to-ports 41309",
			wantOK: false,
		},
		{
			name:   "chain declaration — skip",
			line:   "-N _NDM_HOTSPOT_DNSREDIR",
			wantOK: false,
		},
		{
			name:   "unrelated chain — skip",
			line:   "-A _NDM_HOTSPOT_PREROUTING_MANGL -i br0 -j MARK --set-xmark 0xffffaaa/0xffffffff",
			wantOK: false,
		},
		{
			name:   "missing -j REDIRECT — skip",
			line:   "-A _NDM_HOTSPOT_DNSREDIR -i br0 -m mark --mark 0xffffaaa -p udp --dport 53 -j RETURN",
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			iface, mark, port, ok := parseDNSRedirRule(c.line)
			if ok != c.wantOK {
				t.Fatalf("ok=%v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if iface != c.wantIface {
				t.Errorf("iface=%q, want %q", iface, c.wantIface)
			}
			if mark != c.wantMark {
				t.Errorf("mark=%q, want %q", mark, c.wantMark)
			}
			if port != c.wantPort {
				t.Errorf("port=%d, want %d", port, c.wantPort)
			}
		})
	}
}

func TestPickPort(t *testing.T) {
	cases := []struct {
		name      string
		markPorts map[string]int
		singbox   string
		want      int
	}{
		{
			name:      "single mark, equals sing-box — fall back to it",
			markPorts: map[string]int{"0xffffaae": 41104},
			singbox:   "0xffffaae",
			want:      41104,
		},
		{
			name:      "two marks, prefer non-sing-box",
			markPorts: map[string]int{"0xffffaaa": 41100, "0xffffaae": 41104},
			singbox:   "0xffffaae",
			want:      41100,
		},
		{
			name:      "sing-box mark empty — pick smallest mark's port deterministically",
			markPorts: map[string]int{"0xffffaab": 41101, "0xffffaaa": 41100},
			singbox:   "",
			want:      41100,
		},
		{
			name:      "case-insensitive sing-box match",
			markPorts: map[string]int{"0xFFFFAAE": 41104, "0xffffaaa": 41100},
			singbox:   "0xffffaae",
			want:      41100,
		},
		{
			name:      "empty map — zero port (caller filters)",
			markPorts: map[string]int{},
			singbox:   "0xffffaae",
			want:      0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickPort(c.markPorts, c.singbox)
			if got != c.want {
				t.Errorf("pickPort()=%d, want %d", got, c.want)
			}
		})
	}
}

func TestSplitLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a\nb\nc", []string{"a", "b", "c"}},
		{"a\nb\n", []string{"a", "b"}}, // trailing \n produces no empty entry
		{"\na", []string{"a"}},         // leading \n produces no empty entry
	}
	for _, c := range cases {
		got := splitLines(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitLines(%q): got %d lines, want %d (%+v vs %+v)", c.in, len(got), len(c.want), got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitLines(%q)[%d]: got %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestIsInstalled_ChecksBothChains(t *testing.T) {
	// Both chains present → true.
	fe := &fakeExec{err: nil}
	it := newFakeIPTables(fe)
	if !it.IsInstalled(context.Background()) {
		t.Error("expected true when both chain checks return nil")
	}

	// Mangle chain missing → false, nat chain not consulted.
	fe2 := &fakeExec{err: errors.New("no such chain")}
	fe2.calls = nil
	it2 := newFakeIPTables(fe2)
	if it2.IsInstalled(context.Background()) {
		t.Error("expected false when mangle chain lookup fails")
	}
	foundMangle := false
	for _, c := range fe2.calls {
		if c.kind == "iptables" && len(c.args) >= 4 && c.args[0] == "-t" && c.args[1] == "mangle" && c.args[2] == "-nL" && c.args[3] == ChainName {
			foundMangle = true
		}
	}
	if !foundMangle {
		t.Errorf("expected mangle chain check call, got: %+v", fe2.calls)
	}

	// Nat chain missing → false. Mangle must succeed and nat must fail;
	// IsInstalled short-circuits on the first failure.
	var natChecked bool
	it3 := &IPTables{
		runIPTables: func(_ context.Context, args ...string) error {
			if len(args) >= 4 && args[0] == "-t" && args[1] == "nat" && args[2] == "-nL" && args[3] == RedirectChain {
				natChecked = true
				return errors.New("no such chain")
			}
			return nil // mangle and everything else OK
		},
	}
	if it3.IsInstalled(context.Background()) {
		t.Error("expected false when nat chain lookup fails")
	}
	if !natChecked {
		t.Error("expected nat chain to be consulted")
	}
}

func TestHasAnyInstalled_MangleOnly_ReturnsTrue(t *testing.T) {
	it := &IPTables{
		runIPTables: func(_ context.Context, args ...string) error {
			if len(args) >= 4 &&
				args[0] == "-t" &&
				args[1] == "mangle" &&
				args[2] == "-nL" &&
				args[3] == ChainName {
				return nil
			}
			if len(args) >= 4 &&
				args[0] == "-t" &&
				args[1] == "nat" &&
				args[2] == "-nL" &&
				args[3] == RedirectChain {
				return errors.New("no such chain")
			}
			return errors.New("unexpected call")
		},
	}
	if !it.HasAnyInstalled(context.Background()) {
		t.Error("expected true when only mangle chain exists")
	}
}

func TestHasAnyInstalled_NatOnly_ReturnsTrue(t *testing.T) {
	it := &IPTables{
		runIPTables: func(_ context.Context, args ...string) error {
			if len(args) >= 4 &&
				args[0] == "-t" &&
				args[1] == "mangle" &&
				args[2] == "-nL" &&
				args[3] == ChainName {
				return errors.New("no such chain")
			}
			if len(args) >= 4 &&
				args[0] == "-t" &&
				args[1] == "nat" &&
				args[2] == "-nL" &&
				args[3] == RedirectChain {
				return nil
			}
			return errors.New("unexpected call")
		},
	}
	if !it.HasAnyInstalled(context.Background()) {
		t.Error("expected true when only nat chain exists")
	}
}

func TestHasAnyInstalled_None_ReturnsFalse(t *testing.T) {
	fe := &fakeExec{err: errors.New("no such chain")}
	it := newFakeIPTables(fe)
	if it.HasAnyInstalled(context.Background()) {
		t.Error("expected false when no chains exist")
	}
}

func TestProbe(t *testing.T) {
	// Builds an IPTables whose `-S <table>` output declares the chain and/or
	// emits its PREROUTING jump, per table. err short-circuits to the error path.
	mk := func(mangleChain, mangleJump, natChain, natJump bool, err error) *IPTables {
		return &IPTables{
			runIPTablesOut: func(_ context.Context, args ...string) (string, error) {
				if err != nil {
					return "", err
				}
				table := ""
				if len(args) >= 2 && args[0] == "-t" {
					table = args[1]
				}
				out := "-P PREROUTING ACCEPT\n"
				if table == "mangle" {
					if mangleChain {
						out += "-N " + ChainName + "\n"
					}
					if mangleJump {
						out += "-A PREROUTING -m conntrack ! --ctstate INVALID -j " + ChainName + "\n"
					}
				}
				if table == "nat" {
					if natChain {
						out += "-N " + RedirectChain + "\n"
					}
					if natJump {
						out += "-A PREROUTING -m conntrack ! --ctstate INVALID -j " + RedirectChain + "\n"
					}
				}
				return out, nil
			},
		}
	}

	cases := []struct {
		name                         string
		mChain, mJump, nChain, nJump bool
		wantInstalled, wantJumps     bool
	}{
		{"all present", true, true, true, true, true, true},
		{"chains exist, mangle jump wiped", true, false, true, true, true, false},
		{"chains exist, nat jump wiped", true, true, true, false, true, false},
		{"mangle chain missing", false, false, true, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			installed, jumps, err := mk(c.mChain, c.mJump, c.nChain, c.nJump, nil).Probe(context.Background())
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if installed != c.wantInstalled || jumps != c.wantJumps {
				t.Errorf("installed=%v jumps=%v, want installed=%v jumps=%v", installed, jumps, c.wantInstalled, c.wantJumps)
			}
		})
	}

	t.Run("query error surfaces", func(t *testing.T) {
		_, _, err := mk(true, true, true, true, errors.New("iptables query failed")).Probe(context.Background())
		if err == nil {
			t.Error("want error from Probe when the -S query fails")
		}
	})

	// AWGM-TPROXY must not match a longer chain name sharing its prefix.
	t.Run("anchored jump match", func(t *testing.T) {
		it := &IPTables{
			runIPTablesOut: func(_ context.Context, args ...string) (string, error) {
				out := "-P PREROUTING ACCEPT\n-N " + ChainName + "\n-N " + RedirectChain + "\n"
				out += "-A PREROUTING -j " + ChainName + "-V2\n" // decoy: longer name
				out += "-A PREROUTING -j " + RedirectChain + "\n"
				return out, nil
			},
		}
		_, jumps, _ := it.Probe(context.Background())
		if jumps {
			t.Error("`-j AWGM-TPROXY-V2` must not satisfy the AWGM-TPROXY jump check")
		}
	})
}

func TestBuildRestoreInput_BypassUDPPorts_AddsReturnRules(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark:     "0xffffaaa",
		BypassUDPPorts: []int{500, 4500, 1701},
	}
	out := buildRestoreInput(spec)

	for _, port := range []int{500, 4500, 1701} {
		rule := fmt.Sprintf("-A %s -p udp --dport %d -j RETURN", ChainName, port)
		if !strings.Contains(out, rule) {
			t.Errorf("mangle chain missing UDP bypass rule for port %d\ngot:\n%s", port, out)
		}
	}
}

func TestBuildRestoreInput_BypassTCPPorts_AddsReturnRules(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark:     "0xffffaaa",
		BypassTCPPorts: []int{139, 445},
	}
	out := buildRestoreInput(spec)

	for _, port := range []int{139, 445} {
		rule := fmt.Sprintf("-A %s -p tcp --dport %d -j RETURN", RedirectChain, port)
		if !strings.Contains(out, rule) {
			t.Errorf("nat chain missing TCP bypass rule for port %d\ngot:\n%s", port, out)
		}
	}
}

func TestBuildRestoreInput_EmptyBypassPorts_NoExtraReturnRules(t *testing.T) {
	spec := RestoreInputSpec{PolicyMark: "0xffffaaa"}
	out := buildRestoreInput(spec)

	// port 500 should NOT appear as a bypass rule when no BypassUDPPorts set
	if strings.Contains(out, "--dport 500 -j RETURN") {
		t.Errorf("unexpected bypass rule for port 500 when BypassUDPPorts is empty\ngot:\n%s", out)
	}
}

func TestBuildRestoreInput_BypassPortsBeforeCatchAll(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark:     "0xffffaaa",
		BypassUDPPorts: []int{500},
	}
	out := buildRestoreInput(spec)

	// RETURN for port 500 must appear before the catch-all TPROXY rule
	bypassIdx := strings.Index(out, "--dport 500 -j RETURN")
	catchAllIdx := strings.Index(out, fmt.Sprintf("-A %s -p udp -j TPROXY", ChainName))
	if bypassIdx == -1 {
		t.Fatal("bypass rule not found")
	}
	if catchAllIdx == -1 {
		t.Fatal("catch-all TPROXY rule not found")
	}
	if bypassIdx > catchAllIdx {
		t.Errorf("bypass rule appears AFTER catch-all TPROXY — must be before it")
	}
}

func TestBuildRestoreInput_BypassTCPPortsBeforeCatchAll(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark:     "0xffffaaa",
		BypassTCPPorts: []int{445},
	}
	out := buildRestoreInput(spec)

	// RETURN for port 445 must appear before the catch-all REDIRECT rule
	bypassIdx := strings.Index(out, "--dport 445 -j RETURN")
	catchAllIdx := strings.Index(out, fmt.Sprintf("-A %s -p tcp -j REDIRECT", RedirectChain))
	if bypassIdx == -1 {
		t.Fatal("TCP bypass rule not found")
	}
	if catchAllIdx == -1 {
		t.Fatal("TCP catch-all REDIRECT rule not found")
	}
	if bypassIdx > catchAllIdx {
		t.Errorf("TCP bypass rule appears AFTER catch-all REDIRECT — must be before it")
	}
}

func TestBuildRestoreInput_IngressScope(t *testing.T) {
	spec := RestoreInputSpec{PolicyMark: "0xffffaad", IngressInterfaces: []string{"nwg3"}}
	got := buildRestoreInput(spec)

	markRule := "-A PREROUTING -i nwg3 -m comment --comment AWGM-INGRESS -j MARK --set-xmark 0xffffaad/0xffffffff"
	saveRule := "-A PREROUTING -i nwg3 -m comment --comment AWGM-INGRESS -j CONNMARK --save-mark --nfmask 0xffffffff --ctmask 0xffffffff"
	jump := "-A PREROUTING -m connmark --mark 0xffffaad -m conntrack ! --ctstate INVALID -j " + ChainName

	if !strings.Contains(got, markRule) {
		t.Fatalf("missing MARK rule in:\n%s", got)
	}
	if !strings.Contains(got, saveRule) {
		t.Fatalf("missing CONNMARK save rule in:\n%s", got)
	}
	if strings.Index(got, markRule) > strings.Index(got, jump) {
		t.Fatalf("MARK rule must precede the connmark jump")
	}
}

func TestBuildRestoreInput_IngressScope_MatchAllSkips(t *testing.T) {
	spec := RestoreInputSpec{MatchAll: true, IngressInterfaces: []string{"nwg3"}}
	if strings.Contains(buildRestoreInput(spec), "AWGM-INGRESS") {
		t.Fatalf("ingress rules must be skipped in MatchAll mode")
	}
}

func TestBuildRestoreInput_IngressScope_EmptyMarkSkips(t *testing.T) {
	spec := RestoreInputSpec{PolicyMark: "", IngressInterfaces: []string{"nwg3"}}
	if strings.Contains(buildRestoreInput(spec), "AWGM-INGRESS") {
		t.Fatalf("ingress rules must be skipped when PolicyMark empty")
	}
}

func TestWriteNetfilterHook_IngressScrub(t *testing.T) {
	dir := t.TempDir()
	old := netfilterHookPath
	netfilterHookPath = filepath.Join(dir, "hook.sh")
	defer func() { netfilterHookPath = old }()

	if err := writeNetfilterHook(); err != nil {
		t.Fatalf("writeNetfilterHook: %v", err)
	}
	data, _ := os.ReadFile(netfilterHookPath)
	// Scrub must match BOTH quoted and unquoted `iptables -S` comment
	// output (`--comment "AWGM-INGRESS"` and `--comment AWGM-INGRESS`):
	// some iptables builds emit comments unquoted, and a quoted-only
	// `grep -F` misses them, so the netfilter.d reload re-appends a
	// duplicate of the rule it failed to scrub. The robust form is an
	// ERE with an optional quote.
	if !strings.Contains(string(data), `--comment "?AWGM-INGRESS`) {
		t.Fatalf("hook script missing robust (quote-optional) AWGM-INGRESS scrub:\n%s", data)
	}
	if strings.Contains(string(data), `grep -F -- '--comment "AWGM-INGRESS"'`) {
		t.Fatalf("hook still uses fragile quoted-only -F scrub for AWGM-INGRESS:\n%s", data)
	}
}

// TestEmitHelpers_TableSymmetry locks the invariant that mangle (UDP/TPROXY) and
// nat (TCP/REDIRECT) carry an identical bypass set and an identically-gated
// PREROUTING jump — differing only by chain name. Drift here would proxy a
// device on one protocol and bypass it on the other.
func TestEmitHelpers_TableSymmetry(t *testing.T) {
	wan := []string{"203.0.113.5/32"}
	spec := RestoreInputSpec{PolicyMark: "0xabc", WANIPs: wan}

	var mB, nB strings.Builder
	emitBypassReturns(&mB, ChainName, wan)
	emitBypassReturns(&nB, RedirectChain, wan)
	if m, n := strings.ReplaceAll(mB.String(), ChainName, "C"), strings.ReplaceAll(nB.String(), RedirectChain, "C"); m != n {
		t.Errorf("bypass set diverges:\nmangle:\n%s\nnat:\n%s", mB.String(), nB.String())
	}
	if !strings.Contains(mB.String(), "203.0.113.5/32") {
		t.Error("WAN IP not rendered in bypass set")
	}

	var mJ, nJ strings.Builder
	emitPreroutingJump(&mJ, ChainName, spec)
	emitPreroutingJump(&nJ, RedirectChain, spec)
	if m, n := strings.ReplaceAll(mJ.String(), ChainName, "C"), strings.ReplaceAll(nJ.String(), RedirectChain, "C"); m != n {
		t.Errorf("prerouting jump diverges:\nmangle: %q\nnat: %q", mJ.String(), nJ.String())
	}
}
