package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/router/awgmbackend"
	sysiptables "github.com/hoaxisr/awg-manager/internal/sys/iptables"
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
	runOut := func(_ context.Context, _ ...string) (string, error) { return jumpsPresentDump(), nil }
	return &IPTables{
		restoreNoflush: fe.restoreNoflush,
		runIPTables:    fe.runIPTables,
		runIPTablesOut: runOut,
		// Legacy-канал — тот же приёмник: объект моделирует движок в
		// legacy-режиме, где активный канал И ЕСТЬ legacy. Без него скраб
		// правил, прибитых к legacy (DNS-RESCUE), молча стал бы no-op в тестах.
		legacyRestoreNoflush: fe.restoreNoflush,
		legacyRun:            fe.runIPTables,
		legacyRunOut:         runOut,
		runIP:                fe.runIP,
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
	script := netfilterHookScript(true)

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

// Uninstall обязан сносить ВСЕ цепочки СВОЕЙ раскладки — обе цепочки перехвата
// и blackhole. Пропущенная пара — это не только сирота в ядре: гейт
// переключения бэкенда увидит остаток и откажет. Чужую раскладку Uninstall не
// трогает намеренно: стек один, и флаш mangle/nat awgm-каналом снёс бы живой
// legacy-перехват (этим занят UninstallForeignRules на объекте с чужой
// раскладкой). Проверяем журнал команд, а не факт вызова.
func TestUninstallFlushesEveryChainOfOwnLayout(t *testing.T) {
	cases := []struct {
		name    string
		useAwgm bool
		want    []chainRef
		foreign []string
	}{
		{
			name: "legacy", useAwgm: false,
			want: []chainRef{
				{"mangle", ChainName}, {"nat", RedirectChain}, {"mangle", BlackholeChain},
			},
			foreign: []string{AwgmTable},
		},
		{
			name: "awgm", useAwgm: true,
			want: []chainRef{
				{AwgmTable, ChainName}, {AwgmTable, RedirectChain}, {AwgmTable, BlackholeChain},
			},
			foreign: []string{"mangle", "nat"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fe := &fakeExec{}
			it := newFakeIPTables(fe)
			if tc.useAwgm {
				it.UseAwgm(stubRunner{
					run:    fe.runIPTables,
					runOut: func(context.Context, ...string) (string, error) { return jumpsPresentDump(), nil },
				})
			}
			if err := it.Uninstall(context.Background()); err != nil {
				t.Fatal(err)
			}

			journal := map[string]bool{}
			for _, c := range fe.calls {
				if c.kind == "iptables" {
					journal[strings.Join(c.args, " ")] = true
				}
			}
			for _, p := range tc.want {
				for _, op := range []string{"-F", "-X"} {
					want := fmt.Sprintf("-t %s %s %s", p.table, op, p.chain)
					if !journal[want] {
						t.Errorf("Uninstall не выполнил %q — цепочка останется в ядре, и гейт переключения увидит остаток", want)
					}
				}
			}
			for cmd := range journal {
				for _, table := range tc.foreign {
					if strings.HasPrefix(cmd, "-t "+table+" -F ") || strings.HasPrefix(cmd, "-t "+table+" -X ") {
						t.Errorf("Uninstall залез в чужую раскладку (%q) — это снос живого перехвата другого режима", cmd)
					}
				}
			}
		})
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
	orig, origCt := netfilterHookPath, netfilterCtCleanPath
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	netfilterCtCleanPath = filepath.Join(tmp, "awgm-ctclean.sh")
	t.Cleanup(func() { netfilterHookPath, netfilterCtCleanPath = orig, origCt })

	if err := writeNetfilterHook(true); err != nil {
		t.Fatalf("writeNetfilterHook: %v", err)
	}
	data, err := os.ReadFile(netfilterHookPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(data)
	// The pidof guard now branches (alive → real interception, dead → fail-closed
	// blackhole) instead of `|| exit 0`, so interception is only restored for a
	// live engine while a dead engine still re-asserts the blackhole.
	if !strings.Contains(body, "if pidof sing-box >/dev/null 2>&1; then") {
		t.Errorf("hook missing pidof branch guard:\n%s", body)
	}
	if !strings.Contains(body, "iptables-restore --noflush") {
		t.Errorf("hook missing restore line:\n%s", body)
	}
}

func TestWriteNetfilterHookPreloadsModules(t *testing.T) {
	tmp := t.TempDir()
	orig, origCt := netfilterHookPath, netfilterCtCleanPath
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	netfilterCtCleanPath = filepath.Join(tmp, "awgm-ctclean.sh")
	t.Cleanup(func() { netfilterHookPath, netfilterCtCleanPath = orig, origCt })

	if err := writeNetfilterHook(true); err != nil {
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
	orig, origCt := netfilterHookPath, netfilterCtCleanPath
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	netfilterCtCleanPath = filepath.Join(tmp, "awgm-ctclean.sh")
	t.Cleanup(func() { netfilterHookPath, netfilterCtCleanPath = orig, origCt })

	if err := writeNetfilterHook(true); err != nil {
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
	orig, origCt, origDNS := netfilterHookPath, netfilterCtCleanPath, netfilterDNSHookPath
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	netfilterCtCleanPath = filepath.Join(tmp, "awgm-ctclean.sh")
	// Узкий хук тоже пиним: refresh теперь его читает, и без подмены тест
	// зависел бы от того, что на машине прогона нет реального 51-…
	netfilterDNSHookPath = filepath.Join(tmp, "51-awgm-dnsrescue.sh")
	t.Cleanup(func() {
		netfilterHookPath, netfilterCtCleanPath, netfilterDNSHookPath = orig, origCt, origDNS
	})

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
	// Скрипт вытеснения хук зовёт после каждого восстановления, и обновляться
	// он обязан тем же поводом — доставка отвязана от хука, но не повод.
	ct, err := os.ReadFile(netfilterCtCleanPath)
	if err != nil || string(ct) != ctCleanScript() {
		t.Errorf("скрипт вытеснения обязан обновляться вместе с хуком: err=%v", err)
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
		persistRules:   func(_, _, _ string) error { return nil },
		persistHook:    func(bool) error { return nil },
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
		BypassUDPPorts: []PortRange{{500, 500}, {4500, 4500}, {1701, 1701}},
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
		BypassTCPPorts: []PortRange{{139, 139}, {445, 445}},
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
		BypassUDPPorts: []PortRange{{500, 500}},
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
		BypassTCPPorts: []PortRange{{445, 445}},
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

func TestBuildRestoreInput_BypassUDPPortRange_AddsReturnRule(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark:     "0xffffaaa",
		BypassUDPPorts: []PortRange{{5000, 5500}},
	}
	out := buildRestoreInput(spec)

	rule := fmt.Sprintf("-A %s -p udp --dport 5000:5500 -j RETURN", ChainName)
	if !strings.Contains(out, rule) {
		t.Errorf("mangle chain missing UDP bypass range rule\ngot:\n%s", out)
	}
}

func TestBuildRestoreInput_BypassTCPPortRange_AddsReturnRule(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark:     "0xffffaaa",
		BypassTCPPorts: []PortRange{{8000, 9000}},
	}
	out := buildRestoreInput(spec)

	rule := fmt.Sprintf("-A %s -p tcp --dport 8000:9000 -j RETURN", RedirectChain)
	if !strings.Contains(out, rule) {
		t.Errorf("nat chain missing TCP bypass range rule\ngot:\n%s", out)
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
	old, oldCt := netfilterHookPath, netfilterCtCleanPath
	netfilterHookPath = filepath.Join(dir, "hook.sh")
	netfilterCtCleanPath = filepath.Join(dir, "awgm-ctclean.sh")
	defer func() { netfilterHookPath, netfilterCtCleanPath = old, oldCt }()

	if err := writeNetfilterHook(true); err != nil {
		t.Fatalf("writeNetfilterHook: %v", err)
	}
	data, _ := os.ReadFile(netfilterHookPath)
	// Scrub must match BOTH quoted and unquoted `iptables -S` comment
	// output (`--comment "AWGM-INGRESS"` and `--comment AWGM-INGRESS`):
	// some iptables builds emit comments unquoted, and a quoted-only
	// `grep -F` misses them, so the netfilter.d reload re-appends a
	// duplicate of the rule it failed to scrub. The robust form is an
	// ERE with an optional quote.
	if !strings.Contains(string(data), `--comment \"?$2`) {
		t.Fatalf("hook script missing robust (quote-optional) comment scrub helper:\n%s", data)
	}
	if !strings.Contains(string(data), "scrub_tagged mangle AWGM-INGRESS") {
		t.Fatalf("hook script missing AWGM-INGRESS scrub call:\n%s", data)
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

func TestBuildRestoreInput_BypassCIDRs(t *testing.T) {
	out := buildRestoreInput(RestoreInputSpec{
		MatchAll:    true,
		BypassCIDRs: []string{"203.0.113.0/24", "10.8.0.5/32"},
	})

	// Присутствует в ОБЕИХ цепочках.
	for _, want := range []string{
		"-A " + ChainName + " -d 203.0.113.0/24 -j RETURN",
		"-A " + RedirectChain + " -d 203.0.113.0/24 -j RETURN",
		"-A " + ChainName + " -d 10.8.0.5/32 -j RETURN",
		"-A " + RedirectChain + " -d 10.8.0.5/32 -j RETURN",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing rule: %q\n--- output ---\n%s", want, out)
		}
	}

	// В mangle bypass обязан стоять ДО перехвата DNS (--dport 53 TPROXY),
	// иначе DNS к bypass-подсети всё равно перехватится.
	bypassIdx := strings.Index(out, "-A "+ChainName+" -d 203.0.113.0/24 -j RETURN")
	dnsIdx := strings.Index(out, "-A "+ChainName+" -p udp --dport 53 -j TPROXY")
	if bypassIdx == -1 || dnsIdx == -1 {
		t.Fatalf("missing rule(s): bypassIdx=%d dnsIdx=%d", bypassIdx, dnsIdx)
	}
	if bypassIdx > dnsIdx {
		t.Errorf("user bypass (%d) must precede DNS intercept (%d) in mangle", bypassIdx, dnsIdx)
	}
}

func TestBuildRestoreInput_SelectiveIPSet_AddsGuardRules(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark:     "0xffffaaa",
		SelectiveIPSet: true,
	}
	out := buildRestoreInput(spec)

	mangleGuard := fmt.Sprintf("-A %s -m set ! --match-set %s dst -j RETURN", ChainName, selectiveSetName)
	natGuard := fmt.Sprintf("-A %s -m set ! --match-set %s dst -j RETURN", RedirectChain, selectiveSetName)

	if !strings.Contains(out, mangleGuard) {
		t.Errorf("mangle chain missing selective guard rule\ngot:\n%s", out)
	}
	if !strings.Contains(out, natGuard) {
		t.Errorf("nat chain missing selective guard rule\ngot:\n%s", out)
	}
}

func TestBuildRestoreInput_SelectiveIPSet_Disabled_NoGuardRules(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark:     "0xffffaaa",
		SelectiveIPSet: false,
	}
	out := buildRestoreInput(spec)

	if strings.Contains(out, "--match-set") {
		t.Errorf("unexpected selective guard rule when SelectiveIPSet=false\ngot:\n%s", out)
	}
}

func TestBuildRestoreInput_SelectiveIPSet_GuardAfterDNS(t *testing.T) {
	// The selective guard must appear AFTER the DNS intercept rule so that
	// DNS (port 53) is always intercepted regardless of ipset membership.
	// This ensures that the hijack-dns action keeps working even when
	// selective mode is on.
	spec := RestoreInputSpec{
		PolicyMark:     "0xffffaaa",
		SelectiveIPSet: true,
	}
	out := buildRestoreInput(spec)

	dnsIdx := strings.Index(out, fmt.Sprintf("-A %s -p udp --dport 53 -j TPROXY", ChainName))
	guardIdx := strings.Index(out, fmt.Sprintf("-A %s -m set ! --match-set %s dst -j RETURN", ChainName, selectiveSetName))
	catchAllIdx := strings.Index(out, fmt.Sprintf("-A %s -p udp -j TPROXY", ChainName))

	if dnsIdx == -1 || guardIdx == -1 || catchAllIdx == -1 {
		t.Fatalf("missing rule(s): dns=%d guard=%d catchAll=%d\n%s", dnsIdx, guardIdx, catchAllIdx, out)
	}
	if guardIdx < dnsIdx {
		t.Errorf("selective guard (%d) must appear AFTER DNS intercept (%d)", guardIdx, dnsIdx)
	}
	if guardIdx > catchAllIdx {
		t.Errorf("selective guard (%d) must appear BEFORE catch-all TPROXY (%d)", guardIdx, catchAllIdx)
	}
}

func TestBuildRestoreInput_SelectiveIPSet_NatTCPDNSBeforeGuard(t *testing.T) {
	// In the nat chain the TCP/53 REDIRECT must appear BEFORE the selective
	// guard: resolver IPs are typically not in AWGM-SELECTIVE, so a guard-first
	// order would RETURN DNS-over-TCP (and truncated-UDP retries) straight to
	// the real upstream, leaking real IPs of proxied domains past hijack-dns.
	spec := RestoreInputSpec{
		PolicyMark:     "0xffffaaa",
		SelectiveIPSet: true,
	}
	out := buildRestoreInput(spec)

	dnsIdx := strings.Index(out, fmt.Sprintf("-A %s -p tcp --dport 53 -j REDIRECT", RedirectChain))
	guardIdx := strings.Index(out, fmt.Sprintf("-A %s -m set ! --match-set %s dst -j RETURN", RedirectChain, selectiveSetName))

	if dnsIdx == -1 || guardIdx == -1 {
		t.Fatalf("missing rule(s): tcpDNS=%d guard=%d\n%s", dnsIdx, guardIdx, out)
	}
	if dnsIdx > guardIdx {
		t.Errorf("nat TCP/53 REDIRECT (%d) must appear BEFORE selective guard (%d)", dnsIdx, guardIdx)
	}
}

func TestBuildRestoreInput_NoSelective_NoNatTCPDNSRule(t *testing.T) {
	// Without the selective guard AND without QoS classes the catch-all
	// REDIRECT covers TCP/53 — the chain must stay a literal port of SKeen's
	// add_redirect_rules.
	spec := RestoreInputSpec{PolicyMark: "0xffffaaa"}
	out := buildRestoreInput(spec)

	if strings.Contains(out, fmt.Sprintf("-A %s -p tcp --dport 53 -j REDIRECT", RedirectChain)) {
		t.Errorf("unexpected TCP/53 rule without SelectiveIPSet/QoS\ngot:\n%s", out)
	}
}

// ── QoS-by-DSCP dispatch (issue #371) ────────────────────────────────────────

func qosTestSpec() RestoreInputSpec {
	return RestoreInputSpec{
		PolicyMark: "0xffffaaa",
		QoSClasses: []QoSClassSpec{
			{DSCP: 46, TProxyPort: 51281, RedirectPort: 51301},
			{DSCP: 26, TProxyPort: 51282, RedirectPort: 51302},
		},
	}
}

func TestBuildRestoreInput_QoSClasses_RulesPresentInBothChains(t *testing.T) {
	out := buildRestoreInput(qosTestSpec())

	expected := []string{
		// mangle: UDP TPROXY per class, numeric --dscp, main fwmark reused.
		"-A AWGM-TPROXY -p udp -m dscp --dscp 46 -j TPROXY --on-port 51281 --on-ip 127.0.0.1 --tproxy-mark 0x1",
		"-A AWGM-TPROXY -p udp -m dscp --dscp 26 -j TPROXY --on-port 51282 --on-ip 127.0.0.1 --tproxy-mark 0x1",
		// nat: TCP REDIRECT per class.
		"-A AWGM-REDIRECT -p tcp -m dscp --dscp 46 -j REDIRECT --to-ports 51301",
		"-A AWGM-REDIRECT -p tcp -m dscp --dscp 26 -j REDIRECT --to-ports 51302",
	}
	for _, line := range expected {
		if !strings.Contains(out, line) {
			t.Errorf("missing QoS rule: %q\nin:\n%s", line, out)
		}
	}
}

func TestBuildRestoreInput_QoSClasses_OrderingWithinMangle(t *testing.T) {
	// Anti-recapture invariant (XKeen PR #81 class of bug): the per-class
	// TPROXY must be in the SAME chain and STRICTLY BEFORE the catch-all —
	// both are terminating targets, so class traffic never also traverses
	// the general path. And it must come AFTER the DNS intercept and AFTER
	// bypass RETURNs so DNS stays on the main port and bypasses still win.
	spec := qosTestSpec()
	spec.BypassCIDRs = []string{"203.0.113.0/24"}
	spec.BypassUDPPorts = []PortRange{{500, 500}}
	spec.WANIPs = []string{"198.51.100.7/32"}
	out := buildRestoreInput(spec)

	qosIdx := strings.Index(out, "-A AWGM-TPROXY -p udp -m dscp --dscp 46")
	dnsIdx := strings.Index(out, "-A AWGM-TPROXY -p udp --dport 53 -j TPROXY")
	catchIdx := strings.Index(out, fmt.Sprintf("-A AWGM-TPROXY -p udp -j TPROXY --on-port %d", TPROXYPort))
	userBypassIdx := strings.Index(out, "-A AWGM-TPROXY -d 203.0.113.0/24 -j RETURN")
	portBypassIdx := strings.Index(out, "-A AWGM-TPROXY -p udp --dport 500 -j RETURN")
	wanIdx := strings.Index(out, "-A AWGM-TPROXY -d 198.51.100.7/32 -j RETURN")
	builtinBypassIdx := strings.Index(out, "-A AWGM-TPROXY -d 192.168.0.0/16 -j RETURN")

	for name, idx := range map[string]int{
		"qos": qosIdx, "dns": dnsIdx, "catch-all": catchIdx,
		"user-bypass": userBypassIdx, "port-bypass": portBypassIdx,
		"wan": wanIdx, "builtin-bypass": builtinBypassIdx,
	} {
		if idx == -1 {
			t.Fatalf("%s rule not found in:\n%s", name, out)
		}
	}
	if qosIdx < dnsIdx {
		t.Errorf("QoS rule (%d) must come AFTER DNS intercept (%d) — DSCP must not hijack UDP/53", qosIdx, dnsIdx)
	}
	for name, idx := range map[string]int{
		"user CIDR bypass": userBypassIdx,
		"port bypass":      portBypassIdx,
		"WAN-IP exclusion": wanIdx,
		"builtin bypass":   builtinBypassIdx,
	} {
		if qosIdx < idx {
			t.Errorf("QoS rule (%d) must come AFTER %s (%d)", qosIdx, name, idx)
		}
	}
	if qosIdx > catchIdx {
		t.Errorf("QoS rule (%d) must come BEFORE catch-all TPROXY (%d)", qosIdx, catchIdx)
	}
}

func TestBuildRestoreInput_QoSClasses_OrderingWithinNat(t *testing.T) {
	spec := qosTestSpec()
	spec.BypassTCPPorts = []PortRange{{445, 445}}
	spec.WANIPs = []string{"198.51.100.7/32"}
	out := buildRestoreInput(spec)

	qosIdx := strings.Index(out, "-A AWGM-REDIRECT -p tcp -m dscp --dscp 46")
	catchIdx := strings.Index(out, fmt.Sprintf("-A AWGM-REDIRECT -p tcp -j REDIRECT --to-ports %d", RedirectPort))
	portBypassIdx := strings.Index(out, "-A AWGM-REDIRECT -p tcp --dport 445 -j RETURN")
	adminIdx := strings.Index(out, "-A AWGM-REDIRECT -p tcp --dport 79 -j RETURN")
	wanIdx := strings.Index(out, "-A AWGM-REDIRECT -d 198.51.100.7/32 -j RETURN")

	for name, idx := range map[string]int{
		"qos": qosIdx, "catch-all": catchIdx, "port-bypass": portBypassIdx,
		"admin-bypass": adminIdx, "wan": wanIdx,
	} {
		if idx == -1 {
			t.Fatalf("%s rule not found in:\n%s", name, out)
		}
	}
	for name, idx := range map[string]int{
		"TCP port bypass":  portBypassIdx,
		"admin-79 bypass":  adminIdx,
		"WAN-IP exclusion": wanIdx,
	} {
		if qosIdx < idx {
			t.Errorf("QoS rule (%d) must come AFTER %s (%d)", qosIdx, name, idx)
		}
	}
	if qosIdx > catchIdx {
		t.Errorf("QoS rule (%d) must come BEFORE catch-all REDIRECT (%d)", qosIdx, catchIdx)
	}
}

func TestBuildRestoreInput_QoSClasses_AfterSelectiveGuard(t *testing.T) {
	// Selective mode narrows what enters sing-box; QoS classifies WITHIN that
	// scope. Both chains: guard first, then the DSCP dispatch.
	spec := qosTestSpec()
	spec.SelectiveIPSet = true
	out := buildRestoreInput(spec)

	mangleGuardIdx := strings.Index(out, fmt.Sprintf("-A %s -m set ! --match-set %s dst -j RETURN", ChainName, selectiveSetName))
	mangleQoSIdx := strings.Index(out, "-A AWGM-TPROXY -p udp -m dscp --dscp 46")
	natGuardIdx := strings.Index(out, fmt.Sprintf("-A %s -m set ! --match-set %s dst -j RETURN", RedirectChain, selectiveSetName))
	natQoSIdx := strings.Index(out, "-A AWGM-REDIRECT -p tcp -m dscp --dscp 46")

	if mangleGuardIdx == -1 || mangleQoSIdx == -1 || natGuardIdx == -1 || natQoSIdx == -1 {
		t.Fatalf("missing rule(s): mGuard=%d mQoS=%d nGuard=%d nQoS=%d\n%s",
			mangleGuardIdx, mangleQoSIdx, natGuardIdx, natQoSIdx, out)
	}
	if mangleQoSIdx < mangleGuardIdx {
		t.Errorf("mangle QoS rule (%d) must come AFTER selective guard (%d)", mangleQoSIdx, mangleGuardIdx)
	}
	if natQoSIdx < natGuardIdx {
		t.Errorf("nat QoS rule (%d) must come AFTER selective guard (%d)", natQoSIdx, natGuardIdx)
	}
}

func TestBuildRestoreInput_NoQoSClasses_NoDscpRules(t *testing.T) {
	out := buildRestoreInput(RestoreInputSpec{PolicyMark: "0xffffaaa"})
	if strings.Contains(out, "-m dscp") {
		t.Errorf("dscp rules must be absent when QoSClasses is empty:\n%s", out)
	}
}

func TestWriteNetfilterHookPreloadsXtDscp(t *testing.T) {
	body := netfilterHookScript(true)
	if !strings.Contains(body, "xt_dscp") {
		t.Errorf("hook preload loop missing xt_dscp:\n%s", body)
	}
}

func TestEnsureXtDscpModule_MissingKoIsNotFatal(t *testing.T) {
	orig := ensureKernelModuleFn
	ensureKernelModuleFn = func(_ context.Context, name string) error {
		if name != "xt_dscp" {
			t.Errorf("expected module xt_dscp, got %q", name)
		}
		return ErrNetfilterComponentMissing
	}
	t.Cleanup(func() { ensureKernelModuleFn = orig })

	if err := EnsureXtDscpModule(context.Background()); err != nil {
		t.Errorf("expected nil when .ko absent (built-in fallback), got %v", err)
	}
}

// TestBuildRestoreInput_QoS_NatTCPDNSCarveOutBeforeClassRules guards the
// DNS carve-out: with QoS classes present, TCP/53 must be REDIRECTed to the
// MAIN redirect port strictly BEFORE the per-class DSCP rules, so DSCP-marked
// DNS (UDP is intercepted by the mangle chain, TCP here) always lands on the
// main inbounds where hijack-dns applies — independent of the managed route
// rules' ordering.
func TestBuildRestoreInput_QoS_NatTCPDNSCarveOutBeforeClassRules(t *testing.T) {
	out := buildRestoreInput(qosTestSpec())

	dnsRule := fmt.Sprintf("-A %s -p tcp --dport 53 -j REDIRECT --to-ports %d", RedirectChain, RedirectPort)
	dnsIdx := strings.Index(out, dnsRule)
	qosIdx := strings.Index(out, fmt.Sprintf("-A %s -p tcp -m dscp --dscp 46", RedirectChain))
	if dnsIdx == -1 {
		t.Fatalf("TCP/53 carve-out missing with QoS classes present:\n%s", out)
	}
	if qosIdx == -1 {
		t.Fatalf("QoS nat rule missing:\n%s", out)
	}
	if dnsIdx > qosIdx {
		t.Errorf("TCP/53 carve-out (%d) must come BEFORE the per-class DSCP rules (%d)", dnsIdx, qosIdx)
	}
	if strings.Count(out, dnsRule) != 1 {
		t.Errorf("expected exactly one TCP/53 intercept, got %d:\n%s", strings.Count(out, dnsRule), out)
	}

	// Mangle side: the UDP/53 intercept to the MAIN tproxy port already
	// precedes the class rules (verified ordering, part of the same DNS
	// invariant).
	udpDNSIdx := strings.Index(out, fmt.Sprintf("-A %s -p udp --dport 53 -j TPROXY --on-port %d", ChainName, TPROXYPort))
	udpQoSIdx := strings.Index(out, fmt.Sprintf("-A %s -p udp -m dscp --dscp 46", ChainName))
	if udpDNSIdx == -1 || udpQoSIdx == -1 || udpDNSIdx > udpQoSIdx {
		t.Errorf("mangle UDP/53 intercept (%d) must precede the class rules (%d)", udpDNSIdx, udpQoSIdx)
	}
}

// TestBuildRestoreInput_QoSWithSelective_SingleTCPDNSIntercept: the selective
// guard already emits the identical TCP/53 intercept ahead of the guard; the
// QoS block must not duplicate it.
func TestBuildRestoreInput_QoSWithSelective_SingleTCPDNSIntercept(t *testing.T) {
	spec := qosTestSpec()
	spec.SelectiveIPSet = true
	out := buildRestoreInput(spec)

	dnsRule := fmt.Sprintf("-A %s -p tcp --dport 53 -j REDIRECT --to-ports %d", RedirectChain, RedirectPort)
	if n := strings.Count(out, dnsRule); n != 1 {
		t.Fatalf("expected exactly one TCP/53 intercept with selective+QoS, got %d:\n%s", n, out)
	}
	dnsIdx := strings.Index(out, dnsRule)
	qosIdx := strings.Index(out, fmt.Sprintf("-A %s -p tcp -m dscp --dscp 46", RedirectChain))
	if dnsIdx > qosIdx {
		t.Errorf("TCP/53 intercept (%d) must precede the class rules (%d)", dnsIdx, qosIdx)
	}
}

// ── xt_dscp probe cache (FIX-7) ──────────────────────────────────────────────

// stubXtDscpProbe builds an *IPTables with an empty probe cache and a counting
// stub in place of the raw probe; returns the tables and the counter. Кеш
// теперь живёт в экземпляре, поэтому глобальное состояние восстанавливать не
// нужно — каждый тест берёт свой.
func stubXtDscpProbe(t *testing.T, moduleOK, matchOK bool) (*IPTables, *int) {
	t.Helper()
	calls := 0
	it := NewIPTables()
	it.xtDscpAvailabilityFn = func(_ context.Context) (bool, bool) {
		calls++
		return moduleOK, matchOK
	}
	return it, &calls
}

func TestIsXtDscpAvailable_NegativeResultCachedWithTTL(t *testing.T) {
	it, calls := stubXtDscpProbe(t, false, false)
	ctx := context.Background()

	// Many availability checks within the TTL window → exactly ONE raw probe
	// (previously: one `iptables -m dscp -h` exec per reconcile tick forever).
	for i := 0; i < 10; i++ {
		if it.IsXtDscpAvailable(ctx) {
			t.Fatal("expected unavailable")
		}
	}
	if *calls != 1 {
		t.Fatalf("expected 1 raw probe within TTL, got %d", *calls)
	}
	// The detailed diagnostics path shares the same cache.
	if m, x := it.cachedXtDscpAvailability(ctx); m || x {
		t.Fatal("expected cached negative detail")
	}
	if *calls != 1 {
		t.Fatalf("detail check must not re-probe within TTL, got %d probes", *calls)
	}

	// TTL expiry → exactly one re-probe.
	it.probeMu.Lock()
	it.xtDscpCheckedAt = time.Now().Add(-xtDscpNegativeTTL - time.Minute)
	it.probeMu.Unlock()
	_ = it.IsXtDscpAvailable(ctx)
	if *calls != 2 {
		t.Fatalf("expected re-probe after TTL, got %d probes", *calls)
	}
}

func TestIsXtDscpAvailable_PositiveResultCachedForever(t *testing.T) {
	it, calls := stubXtDscpProbe(t, true, true)
	ctx := context.Background()
	if !it.IsXtDscpAvailable(ctx) {
		t.Fatal("expected available")
	}
	// Even past the TTL, a positive result never re-probes.
	it.probeMu.Lock()
	it.xtDscpCheckedAt = time.Now().Add(-2 * xtDscpNegativeTTL)
	it.probeMu.Unlock()
	for i := 0; i < 5; i++ {
		if !it.IsXtDscpAvailable(ctx) {
			t.Fatal("expected available")
		}
	}
	if *calls != 1 {
		t.Fatalf("positive result must be cached forever, got %d probes", *calls)
	}
}

// Issue #490 (updated): keendns is no longer an iptables CIDR preset —
// it drives managed DNS rewrites. resolveBypassCIDRs must not emit the
// shared cloud IP (would hijack every foreign *.netcraze.pro on LAN).
func TestBuildRestoreInput_KeenDNSPresetNoCIDR(t *testing.T) {
	cidrs, err := resolveBypassCIDRs([]string{"keendns"}, "")
	if err != nil {
		t.Fatalf("resolveBypassCIDRs: %v", err)
	}
	if len(cidrs) != 0 {
		t.Fatalf("keendns must not contribute BypassCIDRs, got %v", cidrs)
	}
	spec := RestoreInputSpec{PolicyMark: "0xffffaaa", BypassCIDRs: cidrs}
	out := buildRestoreInput(spec)
	if strings.Contains(out, "78.47.125.180") {
		t.Errorf("iptables must not hardcode KeenDNS cloud IP from keendns preset:\n%s", out)
	}
}

// --- issue #627: per-table fast heal + poisoned-flow eviction ---

// The rules blob is split per table so the netfilter.d hook can restore ONLY
// the table NDMS actually wiped, instead of tearing down both. The combined
// blob must stay byte-identical to the concatenation (Install and the full
// fallback path still consume it).
func TestBuildRestoreInput_SplitPerTable(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark:        "0xffffaaa",
		WANIPs:            []string{"1.2.3.4"},
		LANBridges:        []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}},
		IngressInterfaces: []string{"ovpn_br0"},
	}
	mangle := buildInterceptRestoreInput(spec)
	nat := buildNatRestoreInput(spec)
	if mangle+nat != buildRestoreInput(spec) {
		t.Errorf("mangle+nat sections must concatenate to the combined blob")
	}
	if !strings.Contains(mangle, "*mangle\n") || strings.Contains(mangle, "*nat\n") {
		t.Errorf("mangle section must contain only the mangle table:\n%s", mangle)
	}
	if !strings.Contains(mangle, ChainName) || strings.Contains(mangle, RedirectChain) {
		t.Errorf("mangle section chain leakage:\n%s", mangle)
	}
	if !strings.Contains(mangle, IngressTag) {
		t.Errorf("mangle section must carry ingress-scope rules:\n%s", mangle)
	}
	if !strings.Contains(nat, "*nat\n") || strings.Contains(nat, "*mangle\n") {
		t.Errorf("nat section must contain only the nat table:\n%s", nat)
	}
	if !strings.Contains(nat, RedirectChain) || strings.Contains(nat, ChainName) {
		t.Errorf("nat section chain leakage:\n%s", nat)
	}
	if !strings.Contains(nat, DNSRescueTag) {
		t.Errorf("nat section must carry DNS-RESCUE rules:\n%s", nat)
	}
}

// The hook must heal per table: a mangle-only wipe (routine DHCP renew) must
// not tear down the working nat chain and vice versa. The full rebuild stays
// as fallback for the upgrade window when per-table files don't exist yet.
func TestNetfilterHookScript_PerTableFastHeal(t *testing.T) {
	s := netfilterHookScript(true)
	for _, w := range []string{
		netfilterMangleRulesPath,
		netfilterNatRulesPath,
		netfilterCtCleanPath,
		"fast-restored mangle",
		"fast-restored nat",
		"restored AWGM chains after NDMS reload", // full fallback kept
	} {
		if !strings.Contains(s, w) {
			t.Errorf("hook missing %q:\n%s", w, s)
		}
	}
	// Eviction runs ONLY when the mangle (TPROXY) side was down — a nat-only
	// heal never opened a window for UDP flows to leak past tproxy.
	if !strings.Contains(s, `[ "$mangle_ok" -eq 0 ] && [ -x`) {
		t.Errorf("hook must gate ctclean on mangle_ok:\n%s", s)
	}
}

// The eviction script runs on the live router — validate syntax and the
// safety invariants: UDP only, WAN reply-dst scope, policy-mark scope with
// mac= fallback for all-devices mode.
func TestCtCleanScript_ValidShellAndScope(t *testing.T) {
	s := ctCleanScript()

	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(s)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated ctclean script is not valid sh: %v\n%s", err, out)
	}
	for _, w := range []string{"conntrack", "-p udp", "--reply-dst", "mac=", "--mark", ChainName} {
		if !strings.Contains(s, w) {
			t.Errorf("ctclean missing %q:\n%s", w, s)
		}
	}
	// TCP вытесняется ТОЛЬКО в awgm-режиме: там установившийся поток защищён
	// флагом от -j AWGMPPE, и предикат бьёт ровно по незащищённым. В legacy
	// флага нет ни у кого, и тот же запрет остаётся в силе — убийство
	// установившегося TCP ломает его жёстко (issue #627).
	legacyPart := strings.Replace(s, awgmCtCleanBlock(t, s), "", 1)
	// Вырезан обязан быть РОВНО awgm-блок. Срез «до конца скрипта» унёс бы с
	// собой и legacy-вытеснение, и запрет ниже стал бы вакуумным. Якорь на то,
	// что legacy на месте, — его собственный `-p udp`.
	if !strings.Contains(legacyPart, "-p udp") {
		t.Fatalf("вырезан не только awgm-блок — legacy-часть выпала из-под проверки:\n%s", legacyPart)
	}
	if strings.Contains(legacyPart, "-p tcp") {
		t.Errorf("ctclean must never evict TCP flows outside awgm mode:\n%s", legacyPart)
	}
	// Issue #684: the PPE flush must be guarded by a live TPROXY jump (flushing
	// into an absent jump re-teaches the same flows) and by node writability
	// (non-MTK platforms have no such node), and must sit BEFORE the conntrack
	// tool check — the flows it heals carry no NAT to look up.
	flush := strings.Index(s, "ppe_flush")
	if flush < 0 {
		t.Fatalf("ctclean missing PPE flush:\n%s", s)
	}
	if ct := strings.Index(s, `[ -x "$CT" ]`); ct >= 0 && flush > ct {
		t.Errorf("PPE flush must run before the conntrack-tool gate:\n%s", s)
	}
	guard := s[:flush]
	// Якорь на нашу цепочку определён один раз (has_our_jump) — гейт обязан
	// спрашивать именно его, иначе форма якоря разъедется с выбором стека.
	if !strings.Contains(guard, "-[jg] "+ChainName) || !strings.Contains(guard, `if has_our_jump "$prerouting"`) {
		t.Errorf("PPE flush must be gated on a live TPROXY jump:\n%s", s)
	}
	if !strings.Contains(s, "[ -w /proc/sys/net/hwnat/ppe_flush ]") {
		t.Errorf("PPE flush must be gated on node writability:\n%s", s)
	}
}

// Границы awgm-блока в ctclean: открывающий `if` и его СОБСТВЕННЫЙ закрывающий
// `fi` — тот, что стоит в первой колонке (вложенные идут с отступом). Нужен
// двум тестам: одному — чтобы вырезать блок, другому — чтобы доказать, что
// проход лежит внутри него, а не за `fi`.
func awgmCtCleanBlock(t *testing.T, s string) string {
	t.Helper()
	const open = "if [ \"$awgm_mode\" -eq 1 ]; then\n"
	start := strings.Index(s, open)
	if start < 0 {
		t.Fatalf("в скрипте нет awgm-блока:\n%s", s)
	}
	end := strings.Index(s[start:], "\nfi\n")
	if end < 0 {
		t.Fatalf("awgm-блок не закрыт `fi` в первой колонке:\n%s", s)
	}
	return s[start : start+end+len("\nfi\n")]
}

func TestCtCleanScriptEvictsFastnatOnBothProtocolsInAwgmMode(t *testing.T) {
	body := ctCleanScript()
	// Всё, что проверяется ниже, спрашивается у САМОГО блока: проход, вынесенный
	// за `fi`, отработал бы и в legacy, где флага нет ни у одной записи и тот же
	// предикат снёс бы все живые соединения членов политики.
	block := awgmCtCleanBlock(t, body)

	// Предикат уязвимости: запись НЕ прошла через -j AWGMPPE. Ищется ровно
	// awk-регулярка (скобки экранированы), а не текст токена: упоминание
	// [FASTNAT] в комментарии отбора не делает.
	if !strings.Contains(block, `/\[FASTNAT\]/`) {
		t.Fatal("скрипт не отбирает записи по токену [FASTNAT]")
	}
	// TCP пробивает порог привязки к fastpath за доли секунды — он и есть
	// главный класс утечки, ради которого проход добавлен.
	if !strings.Contains(block, `$3 == "tcp" || $3 == "udp"`) {
		t.Fatal("проход по [FASTNAT] не покрывает оба протокола")
	}
	// Признак режима вообще выставляется — блок иначе мёртв.
	if !strings.Contains(body, "awgm_mode=1") {
		t.Fatal("признак awgm-режима нигде не выставляется")
	}
	// Проход обязан стоять ВЫШЕ выхода по отсутствию WAN-адреса: типичный
	// триггер скрипта — DHCP-renew, когда адреса ещё нет.
	pass := strings.Index(body, `if [ "$awgm_mode" -eq 1 ]`)
	wanExit := strings.Index(body, `[ $# -gt 0 ] || exit 0`)
	if pass < 0 || wanExit < 0 || pass > wanExit {
		t.Fatal("awgm-проход стоит ниже выхода по отсутствию WAN-адреса")
	}
	// Локальные назначения не вытесняются: перехват их и не трогает
	// (RETURN на bypass-подсети), а вытеснение оборвало бы веб-сессию, из
	// которой движок и включают. Точки в предикате экранированы обязательно:
	// голая `10.` заматчила бы `100.x` — публичный адрес.
	if !strings.Contains(block, `192\.168\.`) || !strings.Contains(block, `169\.254\.`) {
		t.Fatal("проход не исключает локальные назначения")
	}
}

func TestCtCleanScriptEvictsExplicitSourceIPs(t *testing.T) {
	body := ctCleanScript()
	// Аргументы обязаны сниматься ДО `set -- $wan_ips`, иначе позиционные
	// параметры затираются списком WAN-адресов.
	argIdx := strings.Index(body, `evict_ips="$*"`)
	wanIdx := strings.Index(body, `set -- $wan_ips`)
	if argIdx < 0 {
		t.Fatal("скрипт не принимает адреса аргументами")
	}
	if wanIdx >= 0 && argIdx > wanIdx {
		t.Fatal("аргументы снимаются после set -- $wan_ips: они уже затёрты")
	}
	// Гейт awgm — тот же, что у прохода по [FASTNAT]: в legacy смена состава
	// политики не имеет права рвать соединения устройства. Спрашивается у
	// САМОГО блока: отбор, вынесенный за `fi`, отработал бы и в legacy.
	block := awgmCtCleanBlock(t, body)
	// Отбор по адресу сложен в тот же awk-проход, что и предикат уязвимости:
	// один проход по /proc/net/nf_conntrack вместо двух, а исключение
	// локальных назначений, разбор тупла и требование mac= достаются даром.
	if !strings.Contains(block, `-v evict="$evict_ips"`) {
		t.Fatalf("адреса не доезжают до awk-прохода:\n%s", block)
	}
	if !strings.Contains(block, `src in ev`) {
		t.Fatalf("нет отбора по адресу-источнику:\n%s", block)
	}
	// Предикат [FASTNAT] обязан спрашиваться в ТЕЛЕ, а не в шапке правила:
	// записи вступившего устройства токена не несут, а отобраться обязаны.
	if strings.Contains(block, `/\[FASTNAT\]/ && (`) {
		t.Fatalf("токен отсекает записи до тела — адреса без него не отберутся:\n%s", block)
	}
}

// Поведение отбора проверяется прогоном настоящего awk на фикстуре
// /proc/net/nf_conntrack: текстовые утверждения выше не отличают рабочий
// предикат от опечатки в имени массива.
func TestCtCleanScriptSelectsExplicitSourceIPs(t *testing.T) {
	// Поля — как в /proc/net/nf_conntrack: $3 — протокол, mac= есть у всякого
	// потока из LAN и нет у собственных потоков роутера, [FASTNAT] — флаг
	// «мимо -j AWGMPPE». Заголовок протокол-специфичный: у tcp номер 6 и
	// состояние, у udp — номер 17 и только таймаут.
	entry := func(proto, src, dst, mark, extra string, mac bool) string {
		head := "ipv4 2 tcp 6 431999 ESTABLISHED"
		if proto == "udp" {
			head = "ipv4 2 udp 17 29"
		}
		macField := "mac=aa:bb:cc:dd:ee:ff "
		if !mac {
			macField = ""
		}
		return fmt.Sprintf("%s src=%s dst=%s sport=1111 dport=443 %spackets=1 bytes=1 "+
			"src=%s dst=%s sport=443 dport=1111 %smark=%s use=2\n",
			head, src, dst, extra, dst, src, macField, mark)
	}
	fixture := "" +
		// Вступившее устройство: записи прежней политики — без токена и с
		// чужой меткой. Ровно то, что скоуп по метке не видит.
		entry("tcp", "192.168.1.5", "93.184.216.34", "0", "", true) +
		entry("udp", "192.168.1.5", "93.184.216.35", "0", "", true) +
		// Тот же адрес, но назначение локальное: перехват его не трогает,
		// и вытеснение оборвало бы веб-сессию к самому роутеру.
		entry("tcp", "192.168.1.5", "192.168.1.1", "0", "", true) +
		// Чужой адрес без токена — не наше дело.
		entry("tcp", "192.168.1.9", "93.184.216.36", "0", "", true) +
		// Чужой адрес с токеном и нашей меткой — прежний скоуп, обязан
		// вытесняться и без всяких аргументов.
		entry("tcp", "192.168.1.9", "93.184.216.37", "256", "[FASTNAT] ", true) +
		// Токен есть, но метка чужая: поток не члена политики, вытеснять
		// его нельзя.
		entry("tcp", "192.168.1.9", "93.184.216.38", "999", "[FASTNAT] ", true) +
		// Токен и наша метка есть, но mac= нет — это поток самого роутера,
		// вплоть до аплинка sing-box (issue #627).
		entry("tcp", "10.0.0.55", "93.184.216.39", "256", "[FASTNAT] ", false)
	bin := t.TempDir()
	ctLog := filepath.Join(bin, "conntrack.log")
	logLog := filepath.Join(bin, "logger.log")
	ctFile := filepath.Join(bin, "nf_conntrack")
	if err := os.WriteFile(ctFile, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Пустой дамп mangle — наши правила в таблице awgm. С AWGM_TEST_LEGACY
	// джамп находится штатным бинарём, и режим становится legacy.
	write("iptables", "[ -n \"$AWGM_TEST_LEGACY\" ] && echo '-A PREROUTING -m connmark --mark 0x100 -j "+ChainName+"'\nexit 0\n")
	write("iptables-awgm", "echo '-A PREROUTING -m connmark --mark 0x100 -j "+ChainName+"'\n")
	write("ip", "exit 0\n") // WAN-адреса нет: проход обязан работать и до DHCP
	write("conntrack", fmt.Sprintf("echo \"$*\" >> %q\n", ctLog))
	write("logger", fmt.Sprintf("echo \"$*\" >> %q\n", logLog))

	body := ctCleanScript()
	body = strings.ReplaceAll(body, "/proc/net/nf_conntrack", ctFile)
	body = strings.ReplaceAll(body, "/opt/sbin/", bin+"/")
	body = strings.ReplaceAll(body, awgmbackend.BundleDir+"/sbin/iptables", bin+"/iptables-awgm")
	body = strings.ReplaceAll(body, "logger -t", bin+"/logger -t")
	script := filepath.Join(bin, "ctclean.sh")
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return string(data)
	}
	run := func(env []string, args ...string) (evicted, logged string) {
		_ = os.Remove(ctLog)
		_ = os.Remove(logLog)
		cmd := exec.Command("sh", append([]string{script}, args...)...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("скрипт завершился ошибкой: %v\n%s", err, out)
		}
		return read(ctLog), read(logLog)
	}

	const (
		joinedTCP   = "-D -p tcp -s 192.168.1.5 -d 93.184.216.34 --sport 1111 --dport 443"
		joinedUDP   = "-D -p udp -s 192.168.1.5 -d 93.184.216.35 --sport 1111 --dport 443"
		localDst    = "-d 192.168.1.1 "
		foreign     = "-s 192.168.1.9 -d 93.184.216.36"
		fastnat     = "-s 192.168.1.9 -d 93.184.216.37"
		foreignMark = "-s 192.168.1.9 -d 93.184.216.38"
		noMac       = "-s 10.0.0.55 -d 93.184.216.39"
	)
	got, _ := run(nil, "192.168.1.5")
	for _, want := range []string{joinedTCP, joinedUDP} {
		if !strings.Contains(got, want) {
			t.Errorf("запись адреса из списка не отобрана (токена и метки у неё нет): %q\nвытеснено:\n%s", want, got)
		}
	}
	if strings.Contains(got, localDst) {
		t.Errorf("локальное назначение обязано отбраковываться и у адреса из списка:\n%s", got)
	}
	if strings.Contains(got, foreign) {
		t.Errorf("адрес не из списка и без токена вытеснять нельзя:\n%s", got)
	}
	if !strings.Contains(got, fastnat) {
		t.Errorf("прежний скоуп по [FASTNAT] сломан:\n%s", got)
	}

	// Пустой список не имеет права превращаться в множество, матчащее что
	// попало: без аргументов обязан отбираться ровно прежний скоуп.
	empty, _ := run(nil)
	if !strings.Contains(empty, fastnat) {
		t.Errorf("без аргументов прежний скоуп обязан работать:\n%s", empty)
	}
	for _, unwanted := range []string{joinedTCP, joinedUDP, foreign, localDst} {
		if strings.Contains(empty, unwanted) {
			t.Errorf("без аргументов отобрано лишнее (%q):\n%s", unwanted, empty)
		}
	}

	// Скоуп прежнего прохода — в обоих прогонах: чужая метка и поток самого
	// роутера (нет mac=) не вытесняются, сколько бы аргументов ни пришло.
	for _, out := range []string{got, empty} {
		if strings.Contains(out, foreignMark) {
			t.Errorf("запись с чужой меткой вытеснять нельзя:\n%s", out)
		}
		if strings.Contains(out, noMac) {
			t.Errorf("поток самого роутера (без mac=) вытеснять нельзя:\n%s", out)
		}
	}

	// legacy: вытеснения по адресам нет вовсе, но факт вызова обязан попасть
	// в журнал — молчание неотличимо от «адреса не приехали».
	legacy, logged := run([]string{"AWGM_TEST_LEGACY=1"}, "192.168.1.5")
	if strings.Contains(legacy, "-D ") {
		t.Errorf("в legacy смена состава политики не имеет права рвать соединения:\n%s", legacy)
	}
	if !strings.Contains(logged, "192.168.1.5") || !strings.Contains(logged, "not in awgm mode") {
		t.Errorf("вызов с адресами в legacy обязан попасть в журнал, записано:\n%s", logged)
	}
}

func TestInstallRunsCtCleanWithoutExplicitIPs(t *testing.T) {
	fe := &fakeExec{}
	it := newFakeIPTables(fe)
	// Без этих заглушек Install полезет писать в /opt/etc и упадёт не по делу.
	it.persistRules = func(string, string, string) error { return nil }
	it.persistHook = func(bool) error { return nil }
	it.persistCtClean = func() error { return nil }
	var got [][]string
	it.runCtClean = func(_ context.Context, ips []string) { got = append(got, ips) }

	if err := it.Install(context.Background(), RestoreInputSpec{PolicyMark: "0x100"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("Install обязан звать вытеснение ровно раз и без адресов, получено %v", got)
	}
}

func TestEvictFlowsPassesSourceIPs(t *testing.T) {
	it := &IPTables{}
	var got [][]string
	it.runCtClean = func(_ context.Context, ips []string) { got = append(got, ips) }

	// Пустой вызов не имеет права запускать скрипт: вытеснять нечего, а
	// побочный ppe_flush сбрасывает аппаратный offload всему LAN.
	it.EvictFlows(context.Background())
	if len(got) != 0 {
		t.Fatalf("вызов без адресов обязан быть no-op, получено %v", got)
	}
	it.EvictFlows(context.Background(), "192.168.1.5", "192.168.1.6")
	if len(got) != 1 || strings.Join(got[0], " ") != "192.168.1.5 192.168.1.6" {
		t.Fatalf("адреса обязаны дойти до скрипта, получено %v", got)
	}
}

// Окно «движок поднялся, правила ещё не стояли» существует в обоих режимах:
// потоки, рождённые в окне, получают direct-NAT и аппаратный offload, а
// вылечить уже offloaded поток может только вытеснение записи conntrack.
// Значит скрипт нужен и там, где полного хука нет вовсе.
func TestCtCleanScriptDeliveredWithoutFullHook(t *testing.T) {
	tmp := t.TempDir()
	origHook, origCt := netfilterHookPath, netfilterCtCleanPath
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	netfilterCtCleanPath = filepath.Join(tmp, "awgm-ctclean.sh")
	t.Cleanup(func() { netfilterHookPath, netfilterCtCleanPath = origHook, origCt })

	if err := writeCtCleanScript(); err != nil {
		t.Fatalf("writeCtCleanScript: %v", err)
	}
	fi, err := os.Stat(netfilterCtCleanPath)
	if err != nil {
		t.Fatal("скрипт ctclean обязан появляться независимо от полного хука")
	}
	if fi.Mode().Perm()&0111 == 0 {
		t.Errorf("ctclean script must be executable, mode %v", fi.Mode())
	}
	if _, err := os.Stat(netfilterHookPath); err == nil {
		t.Fatal("полный хук при этом создаваться не должен")
	}
}

// Скрипт извлекает policy-марку из дампа PREROUTING. В awgm-режиме штатный
// iptables этого дампа не видит: правила живут в таблице awgm, про которую
// знает только бинарь из бандла.
func TestCtCleanScriptReadsMarkFromBothTables(t *testing.T) {
	body := ctCleanScript()
	if !strings.Contains(body, `"$AWGM_BIN" -w -t `+AwgmTable+" -S PREROUTING") {
		t.Fatalf("в awgm-режиме марка живёт в таблице awgm, скрипт обязан её смотреть:\n%s", body)
	}
	if !strings.Contains(body, "/opt/sbin/iptables ") {
		t.Fatal("legacy-путь обязан сохраниться: он основной для 27 моделей")
	}
	// Бинарь бандла статический. LD_LIBRARY_PATH ему не нужен, а подсунутый
	// путь к чужим либам сломал бы запуск.
	if strings.Contains(body, "LD_LIBRARY_PATH") {
		t.Errorf("бинарь бандла статический — LD_LIBRARY_PATH здесь лишний:\n%s", body)
	}
	if w := awgmbackend.BundleDir + "/sbin/iptables"; !strings.Contains(body, w) {
		t.Errorf("путь бандла %q обязан совпадать с раскладкой IPK:\n%s", w, body)
	}
	// Форма извлечения (включая якорь на нашу цепочку и конвертацию hex->dec:
	// conntrack --mark ждёт десятичное) обязана сохраниться один в один.
	if !strings.Contains(body, `sed -n 's/.*-m connmark --mark \(0x[0-9a-fA-F]*\).*-[jg] `+ChainName+`.*/\1/p'`) {
		t.Errorf("форма извлечения марки изменилась:\n%s", body)
	}
	if !strings.Contains(body, `pmark="$(printf '%d' "$pmark" 2>/dev/null)"`) {
		t.Errorf("conntrack --mark ждёт десятичное — конвертация обязана сохраниться:\n%s", body)
	}
	pick := strings.Index(body, "AWGM_BIN")
	mark := strings.Index(body, "--mark \\(0x")
	if pick < 0 || mark < 0 || pick > mark {
		t.Fatalf("таблица обязана выбираться до извлечения марки: pick=%d mark=%d", pick, mark)
	}
}

// Скрипт исполняет /bin/sh на роутере — проверяем не текст, а поведение:
// из какой таблицы берётся марка и что уезжает в conntrack. Подменяем все
// внешние бинари заглушками, абсолютные пути переписываем на них.
func TestCtCleanScriptPicksMarkFromTableWithOurChain(t *testing.T) {
	ourJump := "-A PREROUTING -m connmark --mark %s -g " + ChainName
	cases := []struct {
		name    string
		legacy  string
		awgm    string
		wantArg string
	}{
		{
			name:    "legacy-режим: марка из mangle",
			legacy:  fmt.Sprintf(ourJump, "0x1111"),
			awgm:    fmt.Sprintf(ourJump, "0xffffaaa"),
			wantArg: "--mark 4369", // 0x1111 — mangle в приоритете
		},
		{
			name:    "awgm-режим: mangle пуст, марка из таблицы awgm",
			legacy:  "",
			awgm:    fmt.Sprintf(ourJump, "0xffffaaa"),
			wantArg: "--mark 268434090",
		},
		{
			// Якорь на нашу цепочку: без него чужой непустой дамп сошёл бы
			// за наш, и conntrack чистился бы по марке правила ndm.
			name:    "чужой дамп в mangle не считается нашим",
			legacy:  "-A PREROUTING -m connmark --mark 0x7777 -j NDM_CHAIN",
			awgm:    fmt.Sprintf(ourJump, "0xffffaaa"),
			wantArg: "--mark 268434090",
		},
		{
			// Якорь обязан быть по границе имени, а не substring: джамп в
			// цепочку с нашим именем-префиксом — не наш. Тот же класс, от
			// которого защищается jumpToken на Go-стороне.
			name:    "джамп в цепочку с нашим префиксом не считается нашим",
			legacy:  "-A PREROUTING -m connmark --mark 0x7777 -g " + ChainName + "-X",
			awgm:    fmt.Sprintf(ourJump, "0xffffaaa"),
			wantArg: "--mark 268434090",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			ctLog := filepath.Join(bin, "conntrack.log")
			write := func(name, body string) {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write("iptables", fmt.Sprintf("printf '%%s' %q\n", tc.legacy))
			write("iptables-awgm", fmt.Sprintf("printf '%%s' %q\n", tc.awgm))
			write("ip", `case "$*" in
  *"route show default"*) echo "default via 10.0.0.1 dev eth3" ;;
  *"addr show dev"*) echo "    inet 10.0.0.55/24 scope global eth3" ;;
esac
`)
			write("conntrack", fmt.Sprintf("echo \"$*\" >> %q\necho 'conntrack v1.4.6: 3 flow entries have been deleted.' >&2\n", ctLog))
			write("logger", "exit 0\n")

			body := ctCleanScript()
			body = strings.ReplaceAll(body, "/opt/sbin/", bin+"/")
			body = strings.ReplaceAll(body, awgmbackend.BundleDir+"/sbin/iptables", bin+"/iptables-awgm")
			body = strings.ReplaceAll(body, "logger -t", bin+"/logger -t")
			script := filepath.Join(bin, "ctclean.sh")
			if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}

			if out, err := exec.Command("sh", script).CombinedOutput(); err != nil {
				t.Fatalf("скрипт завершился ошибкой: %v\n%s", err, out)
			}
			got, err := os.ReadFile(ctLog)
			if err != nil {
				t.Fatalf("conntrack не вызван — вытеснение не состоялось: %v", err)
			}
			if !strings.Contains(string(got), tc.wantArg) {
				t.Errorf("conntrack вызван с %q, ожидалось %q", strings.TrimSpace(string(got)), tc.wantArg)
			}
			if !strings.Contains(string(got), "--reply-dst 10.0.0.55") {
				t.Errorf("вытеснение обязано ограничиваться WAN-адресом: %q", strings.TrimSpace(string(got)))
			}
		})
	}
}

// Скрипт вытеснения обязан быть на диске и быть актуальным в ОБОИХ режимах,
// независимо от того, был ли раньше legacy-режим: в awgm-режиме полного хука,
// одним куском с которым скрипт ехал раньше, нет вовсе.
func TestInstallRefreshesCtCleanScriptInBothModes(t *testing.T) {
	cases := []struct {
		name     string
		awgmMode bool
		stale    bool
	}{
		{"awgm, чистая установка", true, false},
		{"awgm, после legacy-режима", true, true},
		{"legacy", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			origRules, origMangle, origNat := netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath
			origHook, origDNS, origCt := netfilterHookPath, netfilterDNSHookPath, netfilterCtCleanPath
			netfilterRulesPath = filepath.Join(tmp, "router-netfilter.rules")
			netfilterMangleRulesPath = filepath.Join(tmp, "router-netfilter-mangle.rules")
			netfilterNatRulesPath = filepath.Join(tmp, "router-netfilter-nat.rules")
			netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
			netfilterDNSHookPath = filepath.Join(tmp, "51-awgm-dnsrescue.sh")
			netfilterCtCleanPath = filepath.Join(tmp, "awgm-ctclean.sh")
			t.Cleanup(func() {
				netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath = origRules, origMangle, origNat
				netfilterHookPath, netfilterDNSHookPath, netfilterCtCleanPath = origHook, origDNS, origCt
			})
			if tc.stale {
				if err := os.WriteFile(netfilterCtCleanPath, []byte("#!/bin/sh\n# версия от прежнего режима\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			it := NewIPTables()
			it.restoreNoflush = func(context.Context, string) error { return nil }
			it.legacyRestoreNoflush = func(context.Context, string) error { return nil }
			it.runIPTables = func(context.Context, ...string) error { return nil }
			it.runIPTablesOut = func(context.Context, ...string) (string, error) { return "", nil }
			it.runIP = func(context.Context, ...string) error { return nil }
			it.runIPOut = func(context.Context, ...string) (string, error) { return "", nil }
			it.runCtClean = func(context.Context, []string) {}
			// Раскладка канала обязана совпадать со spec.AwgmMode — Install
			// сверяет их прямо перед restore (гонка Enable/Reconcile).
			it.awgmLayout = tc.awgmMode

			spec := RestoreInputSpec{
				PolicyMark: "0xffffaaa",
				AwgmMode:   tc.awgmMode,
				LANBridges: []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}},
			}
			if err := it.Install(context.Background(), spec); err != nil {
				t.Fatalf("Install: %v", err)
			}

			data, err := os.ReadFile(netfilterCtCleanPath)
			if err != nil {
				t.Fatalf("скрипт вытеснения не доставлен: %v", err)
			}
			if string(data) != ctCleanScript() {
				t.Fatalf("скрипт обязан быть актуальным независимо от истории режимов, на диске:\n%s", data)
			}
		})
	}
}

// Install persists all three rules files (combined for Install/fallback,
// per-table for the hook's fast heal) and runs the eviction script after the
// rules are up — the engine-start window is fail-open just like a reload.
func TestInstall_PersistsPerTableRulesAndRunsCtClean(t *testing.T) {
	fe := &fakeExec{}
	var persisted [3]string
	order := []string{}
	it := &IPTables{
		restoreNoflush: func(ctx context.Context, input string) error {
			order = append(order, "restore")
			return fe.restoreNoflush(ctx, input)
		},
		runIPTables:    fe.runIPTables,
		runIPTablesOut: func(_ context.Context, _ ...string) (string, error) { return jumpsPresentDump(), nil },
		runIP:          fe.runIP,
		persistRules: func(combined, mangle, nat string) error {
			persisted = [3]string{combined, mangle, nat}
			return nil
		},
		persistHook: func(bool) error { return nil },
		runCtClean:  func(_ context.Context, _ []string) { order = append(order, "ctclean") },
	}
	spec := RestoreInputSpec{PolicyMark: "0xffffaaa"}
	if err := it.Install(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if persisted[0] != buildRestoreInput(spec) {
		t.Errorf("combined rules mismatch")
	}
	if persisted[1] != buildInterceptRestoreInput(spec) || persisted[2] != buildNatRestoreInput(spec) {
		t.Errorf("per-table rules mismatch")
	}
	if want := []string{"restore", "ctclean"}; len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("ctclean must run after restore, got order %v", order)
	}
}

// Task 7 rereview (гонка Enable/Reconcile): spec.AwgmMode решён РАНЬШЕ, на
// service-уровне, а канал IPTables мог смениться к моменту restore —
// несериализованная демоция с Enable-пути (см. activeChannelLayout/
// activeRestoreLayout) успевает переключить it.awgmLayout между построением
// спеки и этим вызовом. Блоб чужой раскладки не должен уйти НИ В ОДИН канал —
// отказ обязан быть ruleChannelError, чтобы демоция/повтор его увидели.
func TestInstallRejectsChannelLayoutMismatch(t *testing.T) {
	fe := newFakeExec()
	it := newFakeIPTables(fe) // awgmLayout=false — канал уже демотирован на legacy
	it.persistRules = func(string, string, string) error { return nil }
	it.persistHook = func(bool) error { return nil }
	it.persistDNSHook = func(bool) error { return nil }
	it.persistCtClean = func() error { return nil }

	spec := RestoreInputSpec{PolicyMark: "0xffffaaa", AwgmMode: true} // спека решена РАНЬШЕ, ещё под awgm

	err := it.Install(context.Background(), spec)
	if err == nil {
		t.Fatal("расхождение канала и spec.AwgmMode обязано быть отказом, а не тихим применением")
	}
	if !fromRuleChannel(err) {
		t.Fatalf("отказ обязан быть ruleChannelError — иначе демоция/повтор его не увидят: %v", err)
	}
	for _, c := range fe.calls {
		if c.kind == "restore" {
			t.Fatalf("блоб чужой раскладки не должен уйти ни в один канал: %+v", fe.calls)
		}
	}
}

// Тот же страж для заглушки (reconcileInstalled ставит её именно на этом
// пути, при мёртвом движке — самый чувствительный к тихому окну fail-open
// случай, разобранный ревьюером).
func TestInstallBlackholeRejectsChannelLayoutMismatch(t *testing.T) {
	fe := newFakeExec()
	it := newFakeIPTables(fe) // awgmLayout=false — канал уже демотирован на legacy
	it.persistBlackhole = func(string) error { return nil }

	spec := RestoreInputSpec{PolicyMark: "0xffffaaa", AwgmMode: true} // спека решена РАНЬШЕ, ещё под awgm

	err := it.InstallBlackhole(context.Background(), spec)
	if err == nil {
		t.Fatal("расхождение канала и spec.AwgmMode обязано быть отказом при установке заглушки")
	}
	if !fromRuleChannel(err) {
		t.Fatalf("отказ обязан быть ruleChannelError: %v", err)
	}
	for _, c := range fe.calls {
		if c.kind == "restore" {
			t.Fatalf("блоб заглушки чужой раскладки не должен уйти ни в один канал: %+v", fe.calls)
		}
	}
}

func TestRemoveNetfilterRulesFile_RemovesPerTableFiles(t *testing.T) {
	tmp := t.TempDir()
	orig, origM, origN := netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath
	netfilterRulesPath = filepath.Join(tmp, "router-netfilter.rules")
	netfilterMangleRulesPath = filepath.Join(tmp, "router-netfilter-mangle.rules")
	netfilterNatRulesPath = filepath.Join(tmp, "router-netfilter-nat.rules")
	t.Cleanup(func() {
		netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath = orig, origM, origN
	})

	for _, p := range []string{netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath} {
		if err := os.WriteFile(p, []byte("dummy"), 0644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}
	removeNetfilterRulesFile()
	for _, p := range []string{netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s to be gone, got err=%v", p, err)
		}
	}
}

// Execute-based verification of the hook's branch logic: fake iptables/ip/
// pidof/logger binaries record their calls, and each (mangle_ok × nat_ok ×
// per-table files) combination must take exactly the expected path. The
// /opt/sbin/ prefix is rewritten to the fake bin dir — the only script
// transformation applied.
func TestNetfilterHookScript_BranchExecution(t *testing.T) {
	cases := []struct {
		name          string
		mangleJump    bool
		natJump       bool
		perTableFiles bool
		wantRestores  []string // markers of files fed to iptables-restore, in order
		wantCtClean   bool
	}{
		{"mangle broken → fast mangle only + ctclean", false, true, true, []string{"MANGLE-RULES"}, true},
		{"nat broken → fast nat only, no ctclean", true, false, true, []string{"NAT-RULES"}, false},
		{"mangle broken, no per-table files → full fallback + ctclean", false, true, false, []string{"COMBINED-RULES"}, true},
		{"both broken → fast both + ctclean", false, false, true, []string{"MANGLE-RULES", "NAT-RULES"}, true},
		{"both ok → no restore, no ctclean", true, true, true, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			bin := filepath.Join(tmp, "bin")
			if err := os.Mkdir(bin, 0755); err != nil {
				t.Fatal(err)
			}
			callLog := filepath.Join(tmp, "calls.log")

			origR, origM, origN, origB, origCt := netfilterRulesPath, netfilterMangleRulesPath,
				netfilterNatRulesPath, netfilterBlackholePath, netfilterCtCleanPath
			netfilterRulesPath = filepath.Join(tmp, "combined.rules")
			netfilterMangleRulesPath = filepath.Join(tmp, "mangle.rules")
			netfilterNatRulesPath = filepath.Join(tmp, "nat.rules")
			netfilterBlackholePath = filepath.Join(tmp, "blackhole.rules")
			netfilterCtCleanPath = filepath.Join(tmp, "ctclean.sh")
			t.Cleanup(func() {
				netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath,
					netfilterBlackholePath, netfilterCtCleanPath = origR, origM, origN, origB, origCt
			})

			os.WriteFile(netfilterRulesPath, []byte("COMBINED-RULES\n"), 0644)
			if tc.perTableFiles {
				os.WriteFile(netfilterMangleRulesPath, []byte("MANGLE-RULES\n"), 0644)
				os.WriteFile(netfilterNatRulesPath, []byte("NAT-RULES\n"), 0644)
			}
			os.WriteFile(netfilterCtCleanPath, []byte("#!/bin/sh\necho CTCLEAN >> "+callLog+"\n"), 0755)

			// Fake iptables: -S PREROUTING prints the jump when the table's
			// flag file exists; -nL <chain> always succeeds (chains exist —
			// the NDMS-wipe scenario removes jumps, not chains).
			jump := func(table, chain string, present bool) {
				if present {
					os.WriteFile(filepath.Join(tmp, table+".jump"), []byte(chain), 0644)
				}
			}
			jump("mangle", ChainName, tc.mangleJump)
			jump("nat", RedirectChain, tc.natJump)
			fakeIPTables := `#!/bin/sh
table=""; prev=""
for a in "$@"; do [ "$prev" = "-t" ] && table="$a"; prev="$a"; done
case "$*" in
  *"-S PREROUTING"*) [ -f "` + tmp + `/$table.jump" ] && echo "-A PREROUTING -m conntrack ! --ctstate INVALID -j $(cat "` + tmp + `/$table.jump")"; exit 0 ;;
  *"-nL"*) exit 0 ;;
esac
exit 0
`
			os.WriteFile(filepath.Join(bin, "iptables"), []byte(fakeIPTables), 0755)
			os.WriteFile(filepath.Join(bin, "iptables-restore"),
				[]byte("#!/bin/sh\nhead -n1 | sed 's/^/RESTORE /' >> "+callLog+"\n"), 0755)
			os.WriteFile(filepath.Join(bin, "ip"), []byte("#!/bin/sh\nexit 0\n"), 0755)
			os.WriteFile(filepath.Join(bin, "pidof"), []byte("#!/bin/sh\nexit 0\n"), 0755) // sing-box alive
			os.WriteFile(filepath.Join(bin, "logger"), []byte("#!/bin/sh\nexit 0\n"), 0755)

			script := strings.ReplaceAll(netfilterHookScript(true), "/opt/sbin/", bin+"/")
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "type=iptables", "table=mangle")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("hook execution failed: %v\n%s", err, out)
			}

			data, _ := os.ReadFile(callLog)
			var restores []string
			ctCleanSeen := false
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				switch {
				case strings.HasPrefix(line, "RESTORE "):
					restores = append(restores, strings.TrimPrefix(line, "RESTORE "))
				case line == "CTCLEAN":
					ctCleanSeen = true
				}
			}
			if len(restores) != len(tc.wantRestores) {
				t.Fatalf("restores = %v, want %v\nlog:\n%s", restores, tc.wantRestores, data)
			}
			for i := range restores {
				if restores[i] != tc.wantRestores[i] {
					t.Errorf("restore[%d] = %q, want %q", i, restores[i], tc.wantRestores[i])
				}
			}
			if ctCleanSeen != tc.wantCtClean {
				t.Errorf("ctclean invoked = %v, want %v\nlog:\n%s", ctCleanSeen, tc.wantCtClean, data)
			}
		})
	}
}

// Режим policy-tun (DSCPOnly): netfilter нужен ТОЛЬКО для QoS-DSCP-классов —
// основной трафик уходит NDMS-политикой в tun. Ни catch-all, ни перехвата DNS
// на основные порты быть не должно: этих инбаундов в режиме просто нет.
func TestBuildRestoreInput_DSCPOnly(t *testing.T) {
	spec := RestoreInputSpec{
		DSCPOnly: true, MatchAll: true,
		WANIPs:      []string{"1.2.3.4/32"},
		BypassCIDRs: []string{"203.0.113.0/24"},
		QoSClasses:  []QoSClassSpec{{DSCP: 34, TProxyPort: 51281, RedirectPort: 51301}},
	}
	mangle := buildInterceptRestoreInput(spec)
	nat := buildNatRestoreInput(spec)
	// НЕТ catch-all и НЕТ перехвата DNS на основной порт
	for _, banned := range []string{
		"-p udp -j TPROXY --on-port 51271",
		"--dport 53 -j TPROXY",
		"-p tcp -j REDIRECT --to-ports 51272",
		"--dport 53 -j REDIRECT",
	} {
		if strings.Contains(mangle+nat, banned) {
			t.Errorf("dscp-only must not contain %q", banned)
		}
	}
	// DNS вообще не входит в QoS-диспатчинг: ранний RETURN в обеих цепочках
	if !strings.Contains(mangle, "-A "+ChainName+" -p udp --dport 53 -j RETURN") {
		t.Error("mangle: no dns return")
	}
	if !strings.Contains(nat, "-A "+RedirectChain+" -p tcp --dport 53 -j RETURN") {
		t.Error("nat: no dns return")
	}
	// DSCP-правила и bypass на месте, jump MatchAll
	if !strings.Contains(mangle, "-m dscp --dscp 34 -j TPROXY --on-port 51281") {
		t.Error("mangle: no dscp rule")
	}
	if !strings.Contains(nat, "-m dscp --dscp 34 -j REDIRECT --to-ports 51301") {
		t.Error("nat: no dscp rule")
	}
	if !strings.Contains(mangle, "-d 10.0.0.0/8 -j RETURN") || !strings.Contains(mangle, "-d 1.2.3.4/32 -j RETURN") {
		t.Error("mangle: builtin bypass/WANIP missing")
	}
	if !strings.Contains(mangle, "-A PREROUTING -m conntrack ! --ctstate INVALID -j "+ChainName) {
		t.Error("mangle: no MatchAll jump")
	}
	// пользовательский bypass раньше dscp
	if strings.Index(mangle, "203.0.113.0/24") > strings.Index(mangle, "--dscp 34") {
		t.Error("user bypass after dscp")
	}
}

// ── канал команд: наш бэкенд vs таблицы ndm ──────────────────────────────────

// stubRunner — заглушка RuleRunner: подставляется вместо *awgmbackend.Backend,
// чтобы проверять маршрутизацию вызовов без реального бинаря из бандла.
type stubRunner struct {
	restore func(context.Context, string) error
	run     func(context.Context, ...string) error
	runOut  func(context.Context, ...string) (string, error)
}

func (s stubRunner) RestoreNoflush(ctx context.Context, input string) error {
	if s.restore == nil {
		return nil
	}
	return s.restore(ctx, input)
}

func (s stubRunner) Run(ctx context.Context, args ...string) error {
	if s.run == nil {
		return nil
	}
	return s.run(ctx, args...)
}

func (s stubRunner) RunOutput(ctx context.Context, args ...string) (string, error) {
	if s.runOut == nil {
		return "", nil
	}
	return s.runOut(ctx, args...)
}

func TestUseAwgmRoutesOurCommandsThroughBackend(t *testing.T) {
	// Тест обязан вызывать UseAwgm и настоящий метод IPTables, а не дёргать
	// собственные стабы: иначе он тавтологичен и проходит при любой
	// реализации переключателя.
	it := NewIPTables()
	var viaBackend int
	stub := stubRunner{
		run:    func(context.Context, ...string) error { viaBackend++; return nil },
		runOut: func(context.Context, ...string) (string, error) { viaBackend++; return "", nil },
	}
	// Uninstall внутри дёргает drainFwmarkRules — без стаба это exec
	// настоящего `ip` в юнит-тесте.
	it.runIP = func(context.Context, ...string) error { return nil }
	it.runIPOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.cleanupHook = func() {}
	it.cleanupBlackhole = func() {}
	it.UseAwgm(stub)

	_ = it.Uninstall(context.Background())

	if viaBackend == 0 {
		t.Fatal("после UseAwgm команды над нашими цепочками обязаны идти через бэкенд")
	}
}

func TestUseLegacyRestoresDefaultRunners(t *testing.T) {
	it := NewIPTables()
	it.UseAwgm(stubRunner{
		run:    func(context.Context, ...string) error { return nil },
		runOut: func(context.Context, ...string) (string, error) { return "", nil },
	})
	it.UseLegacy()

	// Сравнение указателей: после UseLegacy раннеры обязаны вернуться к
	// штатным, иначе переключение туда-обратно оставит систему на бандле.
	if reflect.ValueOf(it.runIPTables).Pointer() != reflect.ValueOf(sysiptables.Run).Pointer() {
		t.Fatal("UseLegacy не вернул штатный раннер")
	}
	if reflect.ValueOf(it.runIPTablesOut).Pointer() != reflect.ValueOf(sysiptables.RunOutput).Pointer() {
		t.Fatal("UseLegacy не вернул штатный раннер чтения")
	}
	if reflect.ValueOf(it.restoreNoflush).Pointer() != reflect.ValueOf(sysiptables.RestoreNoflush).Pointer() {
		t.Fatal("UseLegacy не вернул штатный restore")
	}
}

func TestFakeIPIngressAlwaysUsesLegacy(t *testing.T) {
	// fakeip ставит DNAT :53 в nat-таблицу ndm. Канал прибит к штатному
	// бинарю независимо от того, каким бэкендом работает перехват.
	it := NewIPTables()
	it.runIPTables = func(context.Context, ...string) error {
		t.Error("fakeip не должен ходить через активный бэкенд")
		return nil
	}
	it.runIPTablesOut = func(context.Context, ...string) (string, error) {
		t.Error("fakeip не должен ходить через активный бэкенд")
		return "", nil
	}
	var viaLegacy int
	it.legacyRun = func(context.Context, ...string) error { viaLegacy++; return nil }
	it.legacyRunOut = func(context.Context, ...string) (string, error) { viaLegacy++; return "", nil }
	it.runIP = func(context.Context, ...string) error { return nil }
	it.runIPOut = func(context.Context, ...string) (string, error) { return "", nil }

	_ = it.EnsureFakeIPIngress(context.Background(), FakeIPIngressSpec{
		TunIface: "opkgtun0",
		TunDNS:   "198.18.0.1",
		Ifaces:   []string{"opkgtun17"},
	})

	if viaLegacy == 0 {
		t.Fatal("вызов не дошёл до legacy-раннера")
	}
}

func TestDiscoverLANBridgesAlwaysUsesLegacy(t *testing.T) {
	// Чтение _NDM_HOTSPOT_DNSREDIR: это цепочка ndm в таблице nat, и discovery
	// обязан читать её штатным бинарём при любом активном бэкенде.
	it := NewIPTables()
	it.UseAwgm(stubRunner{
		runOut: func(context.Context, ...string) (string, error) {
			t.Error("discovery не должен ходить через активный бэкенд")
			return "", nil
		},
	})
	var viaLegacy int
	it.legacyRunOut = func(context.Context, ...string) (string, error) {
		viaLegacy++
		return "", nil
	}

	if _, err := it.DiscoverLANBridges(context.Background(), "0xffffaaa"); err != nil {
		t.Fatalf("DiscoverLANBridges: %v", err)
	}
	if viaLegacy == 0 {
		t.Fatal("вызов не дошёл до legacy-раннера")
	}
}

func TestUseAwgmResetsAvailabilityCache(t *testing.T) {
	// Положительный результат пробы кешируется навсегда. После смены бэкенда
	// он относится к другому бинарю и стал бы враньём — кеш обязан сброситься.
	it := NewIPTables()
	it.runIPTablesOut = func(context.Context, ...string) (string, error) {
		return "TPROXY target options", nil
	}
	if !it.IsTProxyTargetAvailable(context.Background()) {
		t.Fatal("проба обязана видеть TPROXY в выводе")
	}

	it.UseAwgm(stubRunner{
		runOut: func(context.Context, ...string) (string, error) { return "", errors.New("no such target") },
	})
	if it.IsTProxyTargetAvailable(context.Background()) {
		t.Fatal("после UseAwgm закешированная доступность обязана быть сброшена")
	}
}

// TestGoldenBlob фиксирует байт в байт полный блоб (mangle + nat) для набора
// характерных spec'ов. Это страж рефакторинга: любая потерянная, лишняя или
// переставленная строка эмиссии валит тест с диффом.
//
// Снимки обновляются осознанно: UPDATE_GOLDEN=1 go test ./internal/singbox/router/
func TestGoldenBlob(t *testing.T) {
	cases := map[string]RestoreInputSpec{
		"plain": {
			PolicyMark: "0xffffaaa",
			WANIPs:     []string{"203.0.113.5/32"},
		},
		"selective": {
			PolicyMark:     "0xffffaaa",
			SelectiveIPSet: true,
			BypassTCPPorts: []PortRange{{From: 22, To: 22}},
		},
		"qos": {
			PolicyMark: "0xffffaaa",
			QoSClasses: []QoSClassSpec{{DSCP: 34, TProxyPort: 51281, RedirectPort: 51282}},
		},
		"dscponly": {
			PolicyMark: "0xffffaaa",
			DSCPOnly:   true,
			QoSClasses: []QoSClassSpec{{DSCP: 34, TProxyPort: 51281, RedirectPort: 51282}},
		},
		// DNS-RESCUE — единственный несдвинутый код рядом с рефакторингом,
		// и без этого кейса он остался бы вообще без стража.
		"dnsrescue": {
			PolicyMark: "0xffffaaa",
			LANBridges: []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}},
		},
		// Проверяет, что DSCPOnly по-прежнему выходит ДО DNS-RESCUE.
		"dscponly_bridges": {
			PolicyMark: "0xffffaaa",
			DSCPOnly:   true,
			LANBridges: []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}},
			QoSClasses: []QoSClassSpec{{DSCP: 34, TProxyPort: 51281, RedirectPort: 51282}},
		},
		// Внутри QoS-блока есть ветвление по !SelectiveIPSet — покрыть оба.
		"selective_qos": {
			PolicyMark:     "0xffffaaa",
			SelectiveIPSet: true,
			QoSClasses:     []QoSClassSpec{{DSCP: 34, TProxyPort: 51281, RedirectPort: 51282}},
		},
		// MatchAll даёт другую форму PREROUTING-джампа.
		"matchall": {
			MatchAll:   true,
			LANBridges: []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}},
		},
		// awgm-раскладка: те же спеки плюс AwgmMode. Фиксируют заголовок
		// *awgm, клоны таргетов, PPE-гард и пустую nat-секцию.
		"plain_awgm": {
			PolicyMark: "0xffffaaa",
			WANIPs:     []string{"203.0.113.5/32"},
			AwgmMode:   true,
		},
		"qos_awgm": {
			PolicyMark: "0xffffaaa",
			QoSClasses: []QoSClassSpec{{DSCP: 34, TProxyPort: 51281, RedirectPort: 51282}},
			AwgmMode:   true,
		},
		"dscponly_awgm": {
			PolicyMark: "0xffffaaa",
			DSCPOnly:   true,
			QoSClasses: []QoSClassSpec{{DSCP: 34, TProxyPort: 51281, RedirectPort: 51282}},
			AwgmMode:   true,
		},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			// Снимаем ОБЕ секции: в awgm-режиме обе цепочки переезжают в
			// таблицу awgm, и страж, смотрящий только на nat, пропустил бы
			// половину дрейфа.
			got := buildInterceptRestoreInput(spec) + buildNatRestoreInput(spec)
			golden := filepath.Join("testdata", "blob_"+name+".golden")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("нет снимка %s — сгенерировать: UPDATE_GOLDEN=1 go test ./internal/singbox/router/", golden)
			}
			if got != string(want) {
				t.Fatalf("блоб изменился.\nбыло:\n%s\nстало:\n%s", want, got)
			}
		})
	}
}

func TestAwgmModeMovesBothChainsIntoAwgmTable(t *testing.T) {
	spec := RestoreInputSpec{PolicyMark: "0xffffaaa", AwgmMode: true}
	blob := buildInterceptRestoreInput(spec)
	nat := buildNatRestoreInput(spec)

	if !strings.HasPrefix(blob, "*"+AwgmTable+"\n") {
		t.Fatalf("блоб перехвата обязан открываться заголовком *%s:\n%s", AwgmTable, blob)
	}
	for _, chain := range []string{ChainName, RedirectChain} {
		if !strings.Contains(blob, ":"+chain+" - [0:0]") {
			t.Fatalf("цепочка %s должна эмититься в таблицу %s:\n%s", chain, AwgmTable, blob)
		}
	}
	if strings.Contains(nat, RedirectChain) || strings.Contains(nat, ChainName) {
		t.Fatalf("в nat цепочек перехвата быть не должно — они уехали в %s:\n%s", AwgmTable, nat)
	}
	// Таргеты таблично связаны: штатные TPROXY/PPE ядро в таблице awgm
	// отвергает вместе со всем блобом, поэтому эмитятся только клоны.
	if strings.Contains(blob, "-j REDIRECT") {
		t.Fatalf("REDIRECT в awgm-режиме недопустим:\n%s", blob)
	}
	if strings.Contains(blob, "-j TPROXY") || strings.Contains(blob, "-j PPE") {
		t.Fatalf("штатные имена таргетов в таблице %s ядро отвергает:\n%s", AwgmTable, blob)
	}
	if !strings.Contains(blob, "-j "+AwgmTProxyTarget) {
		t.Fatalf("перехват обязан идти клоном %s:\n%s", AwgmTProxyTarget, blob)
	}
}

func TestAwgmTproxyDispatchCarriesOnIP(t *testing.T) {
	// tproxy-inbound слушает 127.0.0.1 (tproxyListen). Без --on-ip клон-модуль
	// читает net_device.ip_ptr (смещение 744) и дропает КАЖДЫЙ пакет.
	spec := RestoreInputSpec{PolicyMark: "0xffffaaa", AwgmMode: true}
	blob := buildInterceptRestoreInput(spec)

	seen := 0
	for _, line := range strings.Split(blob, "\n") {
		if !strings.Contains(line, "-j "+AwgmTProxyTarget) {
			continue
		}
		seen++
		if !strings.Contains(line, "--on-ip 127.0.0.1") {
			t.Fatalf("%s без --on-ip 127.0.0.1: %s", AwgmTProxyTarget, line)
		}
	}
	// Без этого счётчика тест проходит вакуумно: пустой блоб (или блоб, где
	// перехват вообще не эмитился) даёт ноль итераций цикла и «успех».
	if seen == 0 {
		t.Fatalf("в блобе нет ни одного правила -j %s — проверять было нечего", AwgmTProxyTarget)
	}
}

// Правило продукта: КАЖДОЕ правило AWGMTPROXY несёт --on-ip 127.0.0.1. Без
// флага клон-модуль читает net_device.ip_ptr (смещение 744) и перехват мёртв,
// а забыть флаг легко в любом из четырёх сайтов эмиссии — отсюда сквозной
// страж по всему блобу, а не проверка отдельного правила.
func TestAwgmBlobEveryTproxyRuleHasOnIP(t *testing.T) {
	spec := RestoreInputSpec{
		PolicyMark: "0xffffaaa", AwgmMode: true, SelectiveIPSet: true,
		QoSClasses: []QoSClassSpec{{DSCP: 34, TProxyPort: 51281, RedirectPort: 51282}},
	}
	blob := buildInterceptRestoreInput(spec) + buildNatRestoreInput(spec)
	seen := 0
	for _, line := range strings.Split(blob, "\n") {
		if !strings.Contains(line, "-j "+AwgmTProxyTarget) {
			continue
		}
		seen++
		if !strings.Contains(line, "--on-ip 127.0.0.1") {
			t.Fatalf("правило AWGMTPROXY без --on-ip (модуль полез бы в net_device.ip_ptr): %s", line)
		}
	}
	if seen == 0 {
		t.Fatal("ни одного правила AWGMTPROXY не найдено — страж проходит вакуумно")
	}
}

func TestAwgmModePreservesAllTCPBranches(t *testing.T) {
	// Страж против «заодно причесал»: ветки, потерянные при первом подходе.
	spec := RestoreInputSpec{
		PolicyMark:     "0xffffaaa",
		AwgmMode:       true,
		SelectiveIPSet: true,
	}
	blob := buildInterceptRestoreInput(spec)

	if !strings.Contains(blob, "--dport 79 -j RETURN") {
		t.Fatal("bypass админского порта 79 потерян")
	}
	if !strings.Contains(blob, "! --match-set "+selectiveSetName+" dst -j RETURN") {
		t.Fatal("selective-гард потерян: весь TCP уедет в sing-box вместо подмножества")
	}
	if strings.Contains(blob, "-p tcp --dport 53 -j RETURN") {
		t.Fatal("RETURN для TCP/53 вне DSCPOnly — это утечка DNS-over-TCP мимо sing-box")
	}
}

func TestAwgmDSCPOnlyKeepsTCP53Return(t *testing.T) {
	// В DSCPOnly RETURN для TCP/53 наоборот обязателен: основных инбаундов
	// в этом режиме нет, перехваченный DNS ушёл бы в никуда.
	spec := RestoreInputSpec{
		PolicyMark: "0xffffaaa",
		AwgmMode:   true,
		DSCPOnly:   true,
		QoSClasses: []QoSClassSpec{{DSCP: 34, TProxyPort: 51281, RedirectPort: 51282}},
	}
	if !strings.Contains(buildInterceptRestoreInput(spec), "-p tcp --dport 53 -j RETURN") {
		t.Fatal("в DSCPOnly RETURN для TCP/53 обязателен")
	}
}

func TestAwgmModeEmitsSingleTaggedPPE(t *testing.T) {
	spec := RestoreInputSpec{PolicyMark: "0xffffaaa", AwgmMode: true}
	blob := buildInterceptRestoreInput(spec)

	if n := strings.Count(blob, "-j "+AwgmPPETarget); n != 1 {
		t.Fatalf("ожидали ровно одно правило %s, получили %d", AwgmPPETarget, n)
	}
	if !strings.Contains(blob, "--comment "+PPETag) {
		t.Fatal("PPE обязан нести comment-тег: без него скраб его не найдёт и правило будет накапливаться на каждом Install")
	}
	// Проверять порядок только против UDP-джампа мало: PPE, эмитированный
	// между TCP-цепочкой и джампом в AWGM-TPROXY, прошёл бы такую проверку,
	// хотя TCP-потоки при этом уходят в fastpath мимо перехвата. PPE обязан
	// стоять до ОБОИХ джампов.
	ppe := strings.Index(blob, "-j "+AwgmPPETarget)
	for _, chain := range []string{ChainName, RedirectChain} {
		jump := strings.Index(blob, "-j "+chain)
		if jump < 0 {
			t.Fatalf("в блобе нет джампа в %s — проверять порядок было нечего", chain)
		}
		if ppe > jump {
			t.Fatalf("PPE обязан стоять до джампа в %s: иначе fastpath уносит поток мимо перехвата", chain)
		}
	}
}

func TestAwgmDSCPOnlyAlsoGetsPPE(t *testing.T) {
	// QoS-классы в awgm тоже идут через tproxy — без PPE установившиеся
	// потоки пройдут мимо.
	spec := RestoreInputSpec{
		PolicyMark: "0xffffaaa",
		AwgmMode:   true,
		DSCPOnly:   true,
		QoSClasses: []QoSClassSpec{{DSCP: 34, TProxyPort: 51281, RedirectPort: 51282}},
	}
	if !strings.Contains(buildInterceptRestoreInput(spec), "-j "+AwgmPPETarget) {
		t.Fatal("в DSCPOnly PPE тоже нужен")
	}
}

func TestRemoveSourceHooksScrubsPPE(t *testing.T) {
	// Проверять «читает ли mangle PREROUTING» бесполезно — он и так читает
	// его ради DNS-NOPOLICY и ingress-тегов, тест прошёл бы без реализации.
	// Честная проверка: скормить дамп с PPE-правилом и убедиться, что ушла
	// команда на его удаление.
	it := NewIPTables()
	it.runIPTablesOut = func(_ context.Context, args ...string) (string, error) {
		if slices.Contains(args, "mangle") {
			return "-A PREROUTING -m connmark --mark 0xffffaaa -m comment --comment " +
				PPETag + " -j PPE\n", nil
		}
		return "", nil
	}
	var deleted []string
	it.runIPTables = func(_ context.Context, args ...string) error {
		deleted = append(deleted, strings.Join(args, " "))
		return nil
	}

	it.removeSourceHooks(context.Background(), true)

	if !slices.ContainsFunc(deleted, func(s string) bool {
		return strings.Contains(s, "-D") && strings.Contains(s, PPETag)
	}) {
		t.Fatalf("PPE-правило не снято, команды: %v", deleted)
	}
}

func TestRemoveSourceHooksScrubsBothJumpsInOwnLayout(t *testing.T) {
	// В awgm-режиме джампы в ОБЕ наши цепочки стоят в PREROUTING таблицы awgm.
	// Скраб обязан идти по раскладке: искать их в mangle/nat бессмысленно, а
	// флашить чужие таблицы этим каналом — значит снести живой legacy-перехват.
	// Без снятия джамп копился бы на каждый Install (restore --noflush флашит
	// саму цепочку, но не PREROUTING).
	it := NewIPTables()
	runOut := func(_ context.Context, args ...string) (string, error) {
		if slices.Contains(args, AwgmTable) {
			return "-A PREROUTING -m connmark --mark 0xffffaaa -m conntrack ! --ctstate INVALID -j " + ChainName + "\n" +
				"-A PREROUTING -m connmark --mark 0xffffaaa -m conntrack ! --ctstate INVALID -j " + RedirectChain + "\n", nil
		}
		return "", nil
	}
	var deleted []string
	run := func(_ context.Context, args ...string) error {
		deleted = append(deleted, strings.Join(args, " "))
		return nil
	}
	// Legacy-канал тоже на стабах: иначе скраб DNS-RESCUE/DNS-NOPOLICY ушёл бы
	// в настоящий exec штатного бинаря прямо из юнит-теста.
	it.legacyRun, it.legacyRunOut = run, func(context.Context, ...string) (string, error) { return "", nil }
	it.UseAwgm(stubRunner{run: run, runOut: runOut})

	it.removeSourceHooks(context.Background(), true)

	for _, chain := range []string{ChainName, RedirectChain} {
		if !slices.ContainsFunc(deleted, func(s string) bool {
			return strings.HasPrefix(s, "-t "+AwgmTable+" -D PREROUTING") && strings.Contains(s, chain)
		}) {
			t.Fatalf("джамп в %s из таблицы %s не снят, команды: %v", chain, AwgmTable, deleted)
		}
	}
	for _, cmd := range deleted {
		if strings.HasPrefix(cmd, "-t mangle -D PREROUTING") && strings.Contains(cmd, RedirectChain) {
			t.Fatalf("скраб залез в чужую раскладку: %q", cmd)
		}
	}
}

func TestAwgmInstallUsesTwoChannels(t *testing.T) {
	it := NewIPTables()
	var awgmBlob, legacyBlob string
	it.restoreNoflush = func(_ context.Context, in string) error { awgmBlob = in; return nil }
	it.legacyRestoreNoflush = func(_ context.Context, in string) error { legacyBlob = in; return nil }
	it.persistRules = func(string, string, string) error { return nil }
	// Без стаба дефолт полезет писать в реальный /opt/etc/ndm/… и Install
	// упадёт на dev-машине.
	it.persistDNSHook = func(bool) error { return nil }
	it.persistCtClean = func() error { return nil }
	it.runIPTables = func(context.Context, ...string) error { return nil }
	it.runIPTablesOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.runIP = func(context.Context, ...string) error { return nil }
	it.runIPOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.awgmLayout = true

	spec := RestoreInputSpec{
		PolicyMark: "0xffffaaa",
		AwgmMode:   true,
		LANBridges: []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}},
	}
	if err := it.Install(context.Background(), spec); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(awgmBlob, "*"+AwgmTable) {
		t.Fatalf("блоб перехвата обязан идти через awgm-канал:\n%s", awgmBlob)
	}
	if strings.Contains(awgmBlob, DNSRescueTag) {
		t.Fatal("DNS-RESCUE в awgm-канал попадать не должен")
	}
	if !strings.Contains(legacyBlob, DNSRescueTag) {
		t.Fatal("DNS-RESCUE обязан идти через legacy-канал")
	}
}

func TestAwgmInstallDoesNotWriteFullHook(t *testing.T) {
	// Полный хук скрабит и восстанавливает из файлов, которых в awgm-режиме
	// нет. Оставить его — значит получить хук, который на каждом ndm-reload
	// сносит наши правила и восстанавливает пустоту.
	it := NewIPTables()
	var fullHookWritten, dnsHookWritten bool
	it.restoreNoflush = func(context.Context, string) error { return nil }
	it.legacyRestoreNoflush = func(context.Context, string) error { return nil }
	it.persistRules = func(string, string, string) error { return nil }
	it.persistHook = func(bool) error { fullHookWritten = true; return nil }
	it.persistDNSHook = func(bool) error { dnsHookWritten = true; return nil }
	it.persistCtClean = func() error { return nil }
	it.runIPTables = func(context.Context, ...string) error { return nil }
	it.runIPTablesOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.runIP = func(context.Context, ...string) error { return nil }
	it.runIPOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.awgmLayout = true

	spec := RestoreInputSpec{
		PolicyMark: "0xffffaaa",
		AwgmMode:   true,
		LANBridges: []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}},
	}
	if err := it.Install(context.Background(), spec); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if fullHookWritten {
		t.Fatal("полный хук в awgm-режиме писаться не должен")
	}
	if !dnsHookWritten {
		t.Fatal("узкий DNS-хук обязан быть записан, когда DNS-RESCUE активен")
	}
}

func TestAwgmInstallRemovesDNSHookWhenNoBridges(t *testing.T) {
	it := NewIPTables()
	var want bool
	it.restoreNoflush = func(context.Context, string) error { return nil }
	it.legacyRestoreNoflush = func(context.Context, string) error { return nil }
	it.persistRules = func(string, string, string) error { return nil }
	it.persistDNSHook = func(w bool) error { want = w; return nil }
	it.persistCtClean = func() error { return nil }
	it.runIPTables = func(context.Context, ...string) error { return nil }
	it.runIPTablesOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.runIP = func(context.Context, ...string) error { return nil }
	it.runIPOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.awgmLayout = true

	spec := RestoreInputSpec{PolicyMark: "0xffffaaa", AwgmMode: true} // LANBridges пуст
	if err := it.Install(context.Background(), spec); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if want {
		t.Fatal("без LAN-мостов DNS-RESCUE не эмитится — хук обязан быть снят, а не оставлен висеть")
	}
}

func TestRefreshHookIsModeAware(t *testing.T) {
	tmp := t.TempDir()
	origHook, origDNS := netfilterHookPath, netfilterDNSHookPath
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	netfilterDNSHookPath = filepath.Join(tmp, "51-awgm-dnsrescue.sh")
	t.Cleanup(func() { netfilterHookPath, netfilterDNSHookPath = origHook, origDNS })

	if err := writeNetfilterDNSHook(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(netfilterDNSHookPath)

	refreshNetfilterHookIfPresent()

	after, _ := os.ReadFile(netfilterDNSHookPath)
	if string(before) != string(after) {
		t.Fatal("узкий DNS-хук не должен перезаписываться полным tproxy-скриптом")
	}
	if _, err := os.Stat(netfilterHookPath); err == nil {
		t.Fatal("полный хук не должен появляться, если его не было")
	}
}

func TestAwgmInstallLeavesNatBlobForDNSHook(t *testing.T) {
	// Регресс, который легко внести: снести файлы блобов ПОСЛЕ записи
	// nat-блоба. Тогда узкий хук читает несуществующий файл и молчит вечно.
	tmp := t.TempDir()
	origRules, origMangle, origNat := netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath
	origHook, origDNS, origCt := netfilterHookPath, netfilterDNSHookPath, netfilterCtCleanPath
	netfilterRulesPath = filepath.Join(tmp, "router-netfilter.rules")
	netfilterMangleRulesPath = filepath.Join(tmp, "router-netfilter-mangle.rules")
	netfilterNatRulesPath = filepath.Join(tmp, "router-netfilter-nat.rules")
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	netfilterDNSHookPath = filepath.Join(tmp, "51-awgm-dnsrescue.sh")
	netfilterCtCleanPath = filepath.Join(tmp, "awgm-ctclean.sh")
	t.Cleanup(func() {
		netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath = origRules, origMangle, origNat
		netfilterHookPath, netfilterDNSHookPath, netfilterCtCleanPath = origHook, origDNS, origCt
	})
	// Полный хук от прежнего режима — должен исчезнуть.
	if err := os.WriteFile(netfilterHookPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	it := NewIPTables()
	it.restoreNoflush = func(context.Context, string) error { return nil }
	it.legacyRestoreNoflush = func(context.Context, string) error { return nil }
	it.runIPTables = func(context.Context, ...string) error { return nil }
	it.runIPTablesOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.runIP = func(context.Context, ...string) error { return nil }
	it.runIPOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.runCtClean = func(context.Context, []string) {} // без стаба Install exec'нул бы реальный скрипт
	it.awgmLayout = true

	spec := RestoreInputSpec{
		PolicyMark: "0xffffaaa",
		AwgmMode:   true,
		LANBridges: []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}},
	}
	if err := it.Install(context.Background(), spec); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Stat(netfilterNatRulesPath); err != nil {
		t.Fatal("nat-блоб обязан пережить Install: узкий хук читает именно его")
	}
	if _, err := os.Stat(netfilterHookPath); err == nil {
		t.Fatal("полный хук прежнего режима обязан быть снесён")
	}
	if _, err := os.Stat(netfilterDNSHookPath); err != nil {
		t.Fatal("узкий хук обязан быть записан")
	}
}

// Узкий хук ndm исполняет на живом роутере после каждой перестройки
// firewall — синтаксическая ошибка ломала бы DNS-RESCUE на каждом reload.
func TestNetfilterDNSHookScript_ValidShell(t *testing.T) {
	s := netfilterDNSHookScript()

	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(s)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated dns-rescue hook is not valid sh: %v\n%s", err, out)
	}

	// Хук обязан ограничиваться nat и выходить, когда блоба нет.
	for _, w := range []string{
		`[ "$table" = "nat" ] || exit 0`,
		`[ -f ` + fmt.Sprintf("%q", netfilterNatRulesPath) + ` ] || exit 0`,
		"grep -q " + DNSRescueTag,
		"iptables-restore --noflush",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("dns-rescue hook missing %q:\n%s", w, s)
		}
	}
	// Полного хука здесь быть не должно: он скрабит и восстанавливает
	// джампы из блобов, которых в awgm-режиме нет.
	for _, w := range []string{ChainName, RedirectChain, "pidof sing-box"} {
		if strings.Contains(s, w) {
			t.Errorf("dns-rescue hook must not touch interception (%q):\n%s", w, s)
		}
	}
	// Guard от дублей обязан стоять ПЕРЕД restore: правила вставляются
	// через -I PREROUTING 1, повторный restore наплодил бы копии.
	guard := strings.Index(s, "grep -q "+DNSRescueTag)
	restore := strings.Index(s, "iptables-restore --noflush")
	if guard < 0 || restore < 0 || guard > restore {
		t.Errorf("dup-guard must precede restore: guard=%d restore=%d", guard, restore)
	}
}

func TestAwgmInstallDropsNatBlobWithoutDNSRescue(t *testing.T) {
	// Без LAN-мостов nat-блоб вырождается в пустую транзакцию. Файл с ней
	// никто не читает (узкий хук снят) — он не должен появляться, а
	// оставшийся от прежней установки обязан быть снесён.
	tmp := t.TempDir()
	origRules, origMangle, origNat := netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath
	origHook, origDNS, origCt := netfilterHookPath, netfilterDNSHookPath, netfilterCtCleanPath
	netfilterRulesPath = filepath.Join(tmp, "router-netfilter.rules")
	netfilterMangleRulesPath = filepath.Join(tmp, "router-netfilter-mangle.rules")
	netfilterNatRulesPath = filepath.Join(tmp, "router-netfilter-nat.rules")
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	netfilterDNSHookPath = filepath.Join(tmp, "51-awgm-dnsrescue.sh")
	netfilterCtCleanPath = filepath.Join(tmp, "awgm-ctclean.sh")
	t.Cleanup(func() {
		netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath = origRules, origMangle, origNat
		netfilterHookPath, netfilterDNSHookPath, netfilterCtCleanPath = origHook, origDNS, origCt
	})
	// Артефакты прежней установки.
	if err := os.WriteFile(netfilterNatRulesPath, []byte("*nat\nCOMMIT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(netfilterDNSHookPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	it := NewIPTables()
	it.restoreNoflush = func(context.Context, string) error { return nil }
	it.legacyRestoreNoflush = func(context.Context, string) error {
		t.Fatal("без DNS-RESCUE legacy-канал трогать нечем")
		return nil
	}
	it.runIPTables = func(context.Context, ...string) error { return nil }
	it.runIPTablesOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.runIP = func(context.Context, ...string) error { return nil }
	it.runIPOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.runCtClean = func(context.Context, []string) {}
	it.awgmLayout = true

	spec := RestoreInputSpec{PolicyMark: "0xffffaaa", AwgmMode: true} // LANBridges пуст
	if err := it.Install(context.Background(), spec); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Stat(netfilterNatRulesPath); err == nil {
		t.Fatal("файл с пустой nat-транзакцией не должен оставаться на диске")
	}
	if _, err := os.Stat(netfilterDNSHookPath); err == nil {
		t.Fatal("узкий хук без DNS-RESCUE обязан быть снят")
	}
}

func TestUninstallRemovesDNSHook(t *testing.T) {
	// Уцелевший узкий хук заставил бы refreshNetfilterHookIfPresent навсегда
	// пропускать обновление полного хука после возврата в обычный режим.
	tmp := t.TempDir()
	origDNS, origBlackhole := netfilterDNSHookPath, netfilterBlackholePath
	netfilterDNSHookPath = filepath.Join(tmp, "51-awgm-dnsrescue.sh")
	netfilterBlackholePath = filepath.Join(tmp, "router-blackhole.rules")
	t.Cleanup(func() { netfilterDNSHookPath, netfilterBlackholePath = origDNS, origBlackhole })

	if err := writeNetfilterDNSHook(); err != nil {
		t.Fatal(err)
	}

	it := NewIPTables()
	it.cleanupHook = func() {}
	it.runIPTables = func(context.Context, ...string) error { return nil }
	it.runIPTablesOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.runIP = func(context.Context, ...string) error { return nil }
	it.runIPOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.restoreNoflush = func(context.Context, string) error { return nil }

	if err := it.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(netfilterDNSHookPath); err == nil {
		t.Fatal("Uninstall обязан снять узкий DNS-хук")
	}
}

// Кросс-рестартная дыра: awgm был активен (узкий хук на диске), демон
// рестартует и SelectBackend демотирует на legacy (например, обновилась
// прошивка и пробный канал бандла отвалился). Без явного снятия узкий хук
// продолжил бы жить рядом с полным legacy-хуком: в режиме MatchAll его guard
// не срабатывает никогда, и на каждом nat-событии он повторно скармливает
// legacy nat-блоб через --noflush — правила AWGM-REDIRECT и джамп копятся
// между стираниями таблиц.
func TestLegacyInstallRemovesStaleDNSHook(t *testing.T) {
	tmp := t.TempDir()
	origRules, origMangle, origNat := netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath
	origHook, origDNS, origCt := netfilterHookPath, netfilterDNSHookPath, netfilterCtCleanPath
	netfilterRulesPath = filepath.Join(tmp, "router-netfilter.rules")
	netfilterMangleRulesPath = filepath.Join(tmp, "router-netfilter-mangle.rules")
	netfilterNatRulesPath = filepath.Join(tmp, "router-netfilter-nat.rules")
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	netfilterDNSHookPath = filepath.Join(tmp, "51-awgm-dnsrescue.sh")
	netfilterCtCleanPath = filepath.Join(tmp, "awgm-ctclean.sh")
	t.Cleanup(func() {
		netfilterRulesPath, netfilterMangleRulesPath, netfilterNatRulesPath = origRules, origMangle, origNat
		netfilterHookPath, netfilterDNSHookPath, netfilterCtCleanPath = origHook, origDNS, origCt
	})
	// Узкий хук — наследство прошлой awgm-жизни этого же демона.
	if err := writeNetfilterDNSHook(); err != nil {
		t.Fatal(err)
	}

	it := NewIPTables()
	it.restoreNoflush = func(context.Context, string) error { return nil }
	it.legacyRestoreNoflush = func(context.Context, string) error { return nil }
	it.runIPTables = func(context.Context, ...string) error { return nil }
	it.runIPTablesOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.runIP = func(context.Context, ...string) error { return nil }
	it.runIPOut = func(context.Context, ...string) (string, error) { return "", nil }
	it.runCtClean = func(context.Context, []string) {}
	it.awgmLayout = false // канал уже демотирован на legacy

	spec := RestoreInputSpec{PolicyMark: "0xffffaaa", AwgmMode: false}
	if err := it.Install(context.Background(), spec); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Stat(netfilterDNSHookPath); err == nil {
		t.Fatal("legacy Install обязан снести узкий DNS-хук прежнего awgm-режима")
	}
}

// ipRuleDumpLine рисует строку `ip rule show` так, как её печатает iproute2:
// приоритет с двоеточием, TAB, селектор, «lookup» вместо «table».
func ipRuleDumpLine(table string) string {
	return fmt.Sprintf("%d:\tfrom all fwmark 0x%x lookup %s\n", IPRulePriority, Fwmark, table)
}

// ipPresentDump имитирует установившееся состояние policy-routing: обе
// половины на месте, поэтому EnsureIPRule на таком дампе ничего не мутирует.
// Одна строка на оба запроса (`ip rule show` и `ip route show table <ours>`) —
// каждый матчер смотрит на свою строку и чужую игнорирует.
func ipPresentDump() string {
	return ipRuleDumpLine(fmt.Sprintf("%d", RoutingTable)) + "local default dev lo scope host\n"
}

// wireEnsureIPRuleSeams стабит оба сида `ip` и возвращает журнал мутаций.
func wireEnsureIPRuleSeams(it *IPTables, rules, routes string) *[]string {
	var added []string
	it.runIPOut = func(_ context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "rule" {
			return rules, nil
		}
		return routes, nil
	}
	it.runIP = func(_ context.Context, args ...string) error {
		added = append(added, strings.Join(args, " "))
		return nil
	}
	return &added
}

func TestEnsureIPRuleRestoresMissingRule(t *testing.T) {
	// Раньше ip rule и local-маршрут переставлял netfilter.d-хук. В
	// awgm-режиме хука нет, а без ip rule перехват молча слепнет: счётчики
	// TPROXY растут, соединений ноль.
	it := NewIPTables()
	added := wireEnsureIPRuleSeams(it,
		"0:\tfrom all lookup local\n32766:\tfrom all lookup main\n",
		"local default dev lo scope host\n")

	if err := it.EnsureIPRule(context.Background()); err != nil {
		t.Fatalf("EnsureIPRule: %v", err)
	}
	want := fmt.Sprintf("rule add fwmark 0x%x table %d priority %d", Fwmark, RoutingTable, IPRulePriority)
	if !slices.Contains(*added, want) {
		t.Fatalf("отсутствующее правило обязано быть восстановлено как %q, мутации: %q", want, *added)
	}
}

func TestEnsureIPRuleIsNoopWhenPresent(t *testing.T) {
	it := NewIPTables()
	it.runIPOut = func(_ context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "rule" {
			return ipRuleDumpLine(fmt.Sprintf("%d", RoutingTable)), nil
		}
		return "local default dev lo scope host\n", nil
	}
	it.runIP = func(_ context.Context, args ...string) error {
		t.Fatalf("правило на месте — трогать его нельзя, а вызвано: ip %s", strings.Join(args, " "))
		return nil
	}

	if err := it.EnsureIPRule(context.Background()); err != nil {
		t.Fatalf("EnsureIPRule: %v", err)
	}
}

func TestEnsureIPRuleIsNoopWhenTableIsNamed(t *testing.T) {
	// При имени таблицы в /etc/iproute2/rt_tables iproute2 печатает имя, а не
	// номер. Матч по «lookup 100» видел бы вечную пропажу и добавлял бы
	// правило каждый тик — ровно те дубли, от которых лечит drainFwmarkRules.
	it := NewIPTables()
	added := wireEnsureIPRuleSeams(it, ipRuleDumpLine("awgm"), "local default dev lo scope host\n")

	if err := it.EnsureIPRule(context.Background()); err != nil {
		t.Fatalf("EnsureIPRule: %v", err)
	}
	if len(*added) != 0 {
		t.Fatalf("правило с именованной таблицей — то же самое правило, мутации: %q", *added)
	}
}

func TestEnsureIPRuleRestoresMissingRoute(t *testing.T) {
	it := NewIPTables()
	added := wireEnsureIPRuleSeams(it,
		ipRuleDumpLine(fmt.Sprintf("%d", RoutingTable)),
		"") // таблица пуста

	if err := it.EnsureIPRule(context.Background()); err != nil {
		t.Fatalf("EnsureIPRule: %v", err)
	}
	want := fmt.Sprintf("route add local 0.0.0.0/0 dev lo table %d", RoutingTable)
	if !slices.Contains(*added, want) {
		t.Fatalf("отсутствующий local-маршрут обязан быть восстановлен как %q, мутации: %q", want, *added)
	}
}
