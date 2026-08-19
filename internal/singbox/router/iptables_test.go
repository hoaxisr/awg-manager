package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
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
		runIPOut:       func(_ context.Context, _ ...string) (string, error) { return "", nil },
		persistRules:   func(_, _, _ string) error { return nil },
		persistHook:    func(bool) error { return nil },
		cleanupHook:    func() {},

		persistPolicyTunDNSHook: func(string) error { return nil },
		cleanupPolicyTunDNSHook: func() {},
		persistBlackhole:        func(string) error { return nil },
		cleanupBlackhole:        func() {},
		runCtClean:              func(context.Context) {},
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

// Пре-шаг восстановления набора AWGM-BYPASS: без него iptables-restore с
// `-m set` падает ЦЕЛИКОМ (включая fail-closed blackhole), пока набора нет
// после ребута. Гейт по пустоте обязателен — NDMS дёргает хук до 18-21 раза
// за один flap.
func TestNetfilterHookScript_BypassPreStep(t *testing.T) {
	script := netfilterHookScript(true)

	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated netfilter.d hook is not valid sh: %v\n%s", err, out)
	}

	for _, want := range []string{
		"ipset create " + bypassSetName + " hash:net maxelem 262144 family inet",
		"ipset restore -exist",
		bypassSavePath,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("hook missing %q", want)
		}
	}

	// Гейт по успеху `create` БЕЗ -exist: create с -exist никогда не падает,
	// и restore лил бы на каждый вызов хука (перезаливка живого набора).
	// Счётчик записей для гейта не годится — ядра Keenetic на protocol 6 его
	// не печатают.
	if strings.Contains(script, "family inet -exist") {
		t.Error("bypass pre-step must NOT use `create ... -exist` (gate needs create to fail on a live set)")
	}
	if strings.Contains(script, "Number of entries") {
		t.Error("bypass pre-step must not gate on entry count (absent on Keenetic ipset protocol 6)")
	}

	// Пре-шаг — ДО любого iptables-restore, иначе он бесполезен.
	preIdx := strings.Index(script, "ipset create "+bypassSetName)
	restoreIdx := strings.Index(script, "/opt/sbin/iptables-restore")
	if preIdx == -1 || restoreIdx == -1 || preIdx > restoreIdx {
		t.Fatalf("bypass pre-step (%d) must precede the first iptables-restore (%d)", preIdx, restoreIdx)
	}

	// Всё тело пре-шага — под гейтом наличия save-файла: нет файла → no-op.
	gate := strings.Index(script, "[ -f "+strconv.Quote(bypassSavePath)+" ]")
	if gate == -1 || gate > preIdx {
		t.Fatalf("bypass pre-step must sit inside a `[ -f %s ]` gate (gate=%d)", bypassSavePath, gate)
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
	// Проверяем именно строку цикла: упоминание модуля где-то ещё в скрипте
	// (в правиле или комментарии) прерольным не является. xt_set обязателен —
	// без него iptables-restore правила `-m set --match-set AWGM-BYPASS`
	// падает ЦЕЛИКОМ (вместе с fail-closed blackhole) до первого Install
	// демона, то есть ровно после ребута, где пре-шаг и восстанавливает набор.
	var loopLine string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "for mod in ") {
			loopLine = line
			break
		}
	}
	if loopLine == "" {
		t.Fatalf("hook has no module preload loop:\n%s", body)
	}
	for _, mod := range []string{"xt_TPROXY", "xt_comment", "xt_mark", "xt_connmark", "xt_conntrack", "xt_pkttype", "xt_set"} {
		if !strings.Contains(loopLine, mod) {
			t.Errorf("hook missing module preload entry for %q in %q", mod, loopLine)
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
	orig, origCt := netfilterHookPath, netfilterCtCleanPath
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	netfilterCtCleanPath = filepath.Join(tmp, "awgm-ctclean.sh")
	t.Cleanup(func() { netfilterHookPath, netfilterCtCleanPath = orig, origCt })

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

// geoip-bypass набор AWGM-BYPASS: правило `-m set` стоит РЯДОМ с
// пользовательскими bypass-CIDR — сразу после них и ДО перехвата :53 в mangle
// и до catch-all в nat. Семантика полного обхода, включая DNS.
func TestBuildRestoreInput_BypassGeoIPSetRule(t *testing.T) {
	spec := RestoreInputSpec{
		MatchAll:       true,
		BypassCIDRs:    []string{"203.0.113.0/24"},
		BypassGeoIPSet: true,
	}
	rule := " -m set --match-set " + bypassSetName + " dst -j RETURN"

	mangle := buildMangleRestoreInput(spec)
	setIdx := strings.Index(mangle, "-A "+ChainName+rule)
	userIdx := strings.Index(mangle, "-A "+ChainName+" -d 203.0.113.0/24 -j RETURN")
	dnsIdx := strings.Index(mangle, "-A "+ChainName+" -p udp --dport 53 -j TPROXY")
	if setIdx == -1 || userIdx == -1 || dnsIdx == -1 {
		t.Fatalf("mangle: setIdx=%d userIdx=%d dnsIdx=%d\n%s", setIdx, userIdx, dnsIdx, mangle)
	}
	if setIdx < userIdx {
		t.Errorf("mangle: set rule (%d) must follow user bypass CIDRs (%d)", setIdx, userIdx)
	}
	if setIdx > dnsIdx {
		t.Errorf("mangle: set rule (%d) must precede DNS intercept (%d)", setIdx, dnsIdx)
	}

	nat := buildNatRestoreInput(spec)
	natSetIdx := strings.Index(nat, "-A "+RedirectChain+rule)
	natUserIdx := strings.Index(nat, "-A "+RedirectChain+" -d 203.0.113.0/24 -j RETURN")
	natCatchIdx := strings.Index(nat, fmt.Sprintf("-A %s -p tcp -j REDIRECT --to-ports %d", RedirectChain, RedirectPort))
	if natSetIdx == -1 || natUserIdx == -1 || natCatchIdx == -1 {
		t.Fatalf("nat: setIdx=%d userIdx=%d catchIdx=%d\n%s", natSetIdx, natUserIdx, natCatchIdx, nat)
	}
	if natSetIdx < natUserIdx {
		t.Errorf("nat: set rule (%d) must follow user bypass CIDRs (%d)", natSetIdx, natUserIdx)
	}
	if natSetIdx > natCatchIdx {
		t.Errorf("nat: set rule (%d) must precede catch-all REDIRECT (%d)", natSetIdx, natCatchIdx)
	}
}

// Выключенный набор не должен оставлять в правилах ни одного упоминания
// AWGM-BYPASS: иначе iptables-restore падает целиком, пока набора нет.
func TestBuildRestoreInput_NoBypassGeoIPSetRuleWhenOff(t *testing.T) {
	spec := RestoreInputSpec{MatchAll: true, BypassCIDRs: []string{"203.0.113.0/24"}}
	out := buildRestoreInput(spec) + buildBlackholeRestoreInput(spec)
	if strings.Contains(out, bypassSetName) {
		t.Errorf("rules must not mention %s when the feature is off:\n%s", bypassSetName, out)
	}
}

// policy-tun (DSCPOnly) вне скоупа: основной трафик идёт в tun NDMS-политикой,
// цепочки несут лишь bypass и dscp-диспатч — правила с `-m set` там нет.
func TestBuildRestoreInput_DSCPOnlyHasNoBypassGeoIPSetRule(t *testing.T) {
	spec := RestoreInputSpec{MatchAll: true, DSCPOnly: true, BypassGeoIPSet: true}
	out := buildMangleRestoreInput(spec) + buildNatRestoreInput(spec)
	if strings.Contains(out, bypassSetName) {
		t.Errorf("DSCPOnly chains must not mention %s:\n%s", bypassSetName, out)
	}
}

func TestBuildRestoreInput_NoQoS_NoNatTCPDNSRule(t *testing.T) {
	// Without QoS classes the catch-all REDIRECT covers TCP/53 — the chain
	// must stay a literal port of SKeen's add_redirect_rules.
	spec := RestoreInputSpec{PolicyMark: "0xffffaaa"}
	out := buildRestoreInput(spec)

	if strings.Contains(out, fmt.Sprintf("-A %s -p tcp --dport 53 -j REDIRECT", RedirectChain)) {
		t.Errorf("unexpected TCP/53 rule without QoS\ngot:\n%s", out)
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

// ── xt_dscp probe cache (FIX-7) ──────────────────────────────────────────────

// resetXtDscpProbeCache clears the package-level probe cache and installs a
// counting stub; returns the counter.
func stubXtDscpProbe(t *testing.T, moduleOK, matchOK bool) *int {
	t.Helper()
	calls := 0
	xtDscpMu.Lock()
	origModule, origMatch, origAt := xtDscpModuleOK, xtDscpMatchOK, xtDscpCheckedAt
	origFn := xtDscpAvailabilityFn
	xtDscpModuleOK, xtDscpMatchOK = false, false
	xtDscpCheckedAt = time.Time{}
	xtDscpAvailabilityFn = func(_ context.Context) (bool, bool) {
		calls++
		return moduleOK, matchOK
	}
	xtDscpMu.Unlock()
	t.Cleanup(func() {
		xtDscpMu.Lock()
		xtDscpModuleOK, xtDscpMatchOK, xtDscpCheckedAt = origModule, origMatch, origAt
		xtDscpAvailabilityFn = origFn
		xtDscpMu.Unlock()
	})
	return &calls
}

func TestIsXtDscpAvailable_NegativeResultCachedWithTTL(t *testing.T) {
	calls := stubXtDscpProbe(t, false, false)
	ctx := context.Background()

	// Many availability checks within the TTL window → exactly ONE raw probe
	// (previously: one `iptables -m dscp -h` exec per reconcile tick forever).
	for i := 0; i < 10; i++ {
		if IsXtDscpAvailable(ctx) {
			t.Fatal("expected unavailable")
		}
	}
	if *calls != 1 {
		t.Fatalf("expected 1 raw probe within TTL, got %d", *calls)
	}
	// The detailed diagnostics path shares the same cache.
	if m, x := cachedXtDscpAvailability(ctx); m || x {
		t.Fatal("expected cached negative detail")
	}
	if *calls != 1 {
		t.Fatalf("detail check must not re-probe within TTL, got %d probes", *calls)
	}

	// TTL expiry → exactly one re-probe.
	xtDscpMu.Lock()
	xtDscpCheckedAt = time.Now().Add(-xtDscpNegativeTTL - time.Minute)
	xtDscpMu.Unlock()
	_ = IsXtDscpAvailable(ctx)
	if *calls != 2 {
		t.Fatalf("expected re-probe after TTL, got %d probes", *calls)
	}
}

func TestIsXtDscpAvailable_PositiveResultCachedForever(t *testing.T) {
	calls := stubXtDscpProbe(t, true, true)
	ctx := context.Background()
	if !IsXtDscpAvailable(ctx) {
		t.Fatal("expected available")
	}
	// Even past the TTL, a positive result never re-probes.
	xtDscpMu.Lock()
	xtDscpCheckedAt = time.Now().Add(-2 * xtDscpNegativeTTL)
	xtDscpMu.Unlock()
	for i := 0; i < 5; i++ {
		if !IsXtDscpAvailable(ctx) {
			t.Fatal("expected available")
		}
	}
	if *calls != 1 {
		t.Fatalf("positive result must be cached forever, got %d probes", *calls)
	}
}

// Issue #490 → #729: адрес KeenDNS в правилах берётся с роутера, а не из
// таблицы пресетов. Сам пресет не приносит ни одного CIDR (иначе адрес был бы
// зашит в код), а переданный рантаймом — попадает в цепочки как RETURN.
func TestBuildRestoreInput_KeenDNSCIDRFromRouterOnly(t *testing.T) {
	cidrs, err := resolveBypassCIDRs([]string{"keendns"}, "", nil)
	if err != nil {
		t.Fatalf("resolveBypassCIDRs: %v", err)
	}
	if len(cidrs) != 0 {
		t.Fatalf("пресет не должен приносить BypassCIDRs, got %v", cidrs)
	}
	if out := buildRestoreInput(RestoreInputSpec{PolicyMark: "0xffffaaa", BypassCIDRs: cidrs}); strings.Contains(out, "78.47.125.180") {
		t.Errorf("адрес KeenDNS не должен быть зашит в код:\n%s", out)
	}

	withAddr, err := resolveBypassCIDRs([]string{"keendns"}, "", []string{"78.47.125.180/32"})
	if err != nil {
		t.Fatalf("resolveBypassCIDRs: %v", err)
	}
	out := buildRestoreInput(RestoreInputSpec{PolicyMark: "0xffffaaa", BypassCIDRs: withAddr})
	if !strings.Contains(out, "-A "+ChainName+" -d 78.47.125.180/32 -j RETURN") {
		t.Errorf("адрес с роутера обязан стать RETURN в mangle:\n%s", out)
	}
	if !strings.Contains(out, "-A "+RedirectChain+" -d 78.47.125.180/32 -j RETURN") {
		t.Errorf("адрес с роутера обязан стать RETURN в nat:\n%s", out)
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
	mangle := buildMangleRestoreInput(spec)
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
	if strings.Contains(s, "-p tcp") {
		t.Errorf("ctclean must never evict TCP flows:\n%s", s)
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
	if !strings.Contains(guard, "-[jg] "+ChainName) {
		t.Errorf("PPE flush must be gated on a live TPROXY jump:\n%s", s)
	}
	if !strings.Contains(s, "[ -w /proc/sys/net/hwnat/ppe_flush ]") {
		t.Errorf("PPE flush must be gated on node writability:\n%s", s)
	}
}

func TestWriteNetfilterHook_WritesCtCleanScript(t *testing.T) {
	tmp := t.TempDir()
	origHook, origCt := netfilterHookPath, netfilterCtCleanPath
	netfilterHookPath = filepath.Join(tmp, "50-awgm-tproxy.sh")
	netfilterCtCleanPath = filepath.Join(tmp, "awgm-ctclean.sh")
	t.Cleanup(func() { netfilterHookPath, netfilterCtCleanPath = origHook, origCt })

	if err := writeNetfilterHook(true); err != nil {
		t.Fatalf("writeNetfilterHook: %v", err)
	}
	fi, err := os.Stat(netfilterCtCleanPath)
	if err != nil {
		t.Fatalf("ctclean script not written: %v", err)
	}
	if fi.Mode().Perm()&0111 == 0 {
		t.Errorf("ctclean script must be executable, mode %v", fi.Mode())
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
		runCtClean:  func(_ context.Context) { order = append(order, "ctclean") },
	}
	spec := RestoreInputSpec{PolicyMark: "0xffffaaa"}
	if err := it.Install(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if persisted[0] != buildRestoreInput(spec) {
		t.Errorf("combined rules mismatch")
	}
	if persisted[1] != buildMangleRestoreInput(spec) || persisted[2] != buildNatRestoreInput(spec) {
		t.Errorf("per-table rules mismatch")
	}
	if want := []string{"restore", "ctclean"}; len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Errorf("ctclean must run after restore, got order %v", order)
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
	mangle := buildMangleRestoreInput(spec)
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

func TestPolicyTunDNSHookScriptShellValid(t *testing.T) {
	spec := FakeIPIngressSpec{
		TunIface: "opkgtun0", TunDNS: "172.18.0.2", Tag: PolicyTunDNSTag,
		Marks: []string{"0xffffaab"}, Ifaces: []string{"opkgtun17"},
	}
	script := policyTunDNSHookScript(spec)

	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s\n---\n%s", err, out, script)
	}

	for _, want := range []string{
		`[ "$table" = "nat" ] || exit 0`,
		"-m connmark --mark 0xffffaab -p udp --dport 53",
		"-m connmark --mark 0xffffaab -p tcp --dport 53",
		"-i opkgtun17 -p udp --dport 53",
		"--comment " + PolicyTunDNSTag,
		"--to-destination 172.18.0.2:53",
		"xt_comment",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("в хуке нет %q", want)
		}
	}
	// Гейта на живость движка быть НЕ должно: решение fail-closed (спека §7.3).
	if strings.Contains(script, "pidof sing-box") {
		t.Error("хук гейтится на pidof sing-box — это ломает fail-closed")
	}
}

func TestPolicyTunDNSHookScriptStableAcrossCalls(t *testing.T) {
	spec := FakeIPIngressSpec{
		TunIface: "opkgtun0", TunDNS: "172.18.0.2", Tag: PolicyTunDNSTag,
		Marks: []string{"0xffffaab", "0xffffaac"},
	}
	if policyTunDNSHookScript(spec) != policyTunDNSHookScript(spec) {
		t.Error("рендер не детерминирован")
	}
}

func TestWritePolicyTunDNSHookSkipsIdenticalContent(t *testing.T) {
	dir := t.TempDir()
	prev := netfilterPolicyTunDNSHookPath
	netfilterPolicyTunDNSHookPath = filepath.Join(dir, "52-awgm-policytun-dns.sh")
	t.Cleanup(func() { netfilterPolicyTunDNSHookPath = prev })

	script := "#!/bin/sh\nexit 0\n"
	if err := writePolicyTunDNSHook(script); err != nil {
		t.Fatalf("первая запись: %v", err)
	}
	st1, err := os.Stat(netfilterPolicyTunDNSHookPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st1.Mode().Perm() != 0755 {
		t.Errorf("права %v, хотели 0755", st1.Mode().Perm())
	}

	if err := writePolicyTunDNSHook(script); err != nil {
		t.Fatalf("повторная запись: %v", err)
	}
	st2, _ := os.Stat(netfilterPolicyTunDNSHookPath)
	if st1.ModTime() != st2.ModTime() {
		t.Error("файл переписан при неизменном содержимом")
	}

	// Сбитые снаружи права обязаны чиниться, даже когда байты совпадают.
	if err := os.Chmod(netfilterPolicyTunDNSHookPath, 0644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := writePolicyTunDNSHook(script); err != nil {
		t.Fatalf("запись при сбитых правах: %v", err)
	}
	st3, _ := os.Stat(netfilterPolicyTunDNSHookPath)
	if st3.Mode().Perm() != 0755 {
		t.Errorf("права не восстановлены: %v", st3.Mode().Perm())
	}

	if err := writePolicyTunDNSHook(script + "# tail\n"); err != nil {
		t.Fatalf("запись изменённого: %v", err)
	}
	got, _ := os.ReadFile(netfilterPolicyTunDNSHookPath)
	if !strings.HasSuffix(string(got), "# tail\n") {
		t.Error("изменённое содержимое не записалось")
	}

	removePolicyTunDNSHook()
	if _, err := os.Stat(netfilterPolicyTunDNSHookPath); !os.IsNotExist(err) {
		t.Error("файл не снят")
	}
}

// equalRestoreInputSpec — рукописный компаратор, и у него ровно тот дефект,
// который мы чиним снимком: «забыли поле». Тест перебирает поля типа
// рефлексией и требует, чтобы КАЖДОЕ давало неравенство. Новое поле в
// RestoreInputSpec роняет тест, пока его не заведут в компаратор.
func TestEqualRestoreInputSpec_CoversEveryField(t *testing.T) {
	rt := reflect.TypeOf(RestoreInputSpec{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		var a, b RestoreInputSpec
		fv := reflect.ValueOf(&b).Elem().Field(i)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString("x")
		case reflect.Bool:
			fv.SetBool(true)
		case reflect.Slice:
			// Слайс из одного НУЛЕВОГО элемента: slices.Equal(nil, []T{zero})
			// == false, значит поле обязано быть замечено.
			fv.Set(reflect.MakeSlice(fv.Type(), 1, 1))
		default:
			t.Fatalf("поле %s: неподдержанный вид %s — расширить тест и компаратор",
				f.Name, fv.Kind())
		}
		if equalRestoreInputSpec(&a, &b) {
			t.Errorf("поле %s не участвует в equalRestoreInputSpec", f.Name)
		}
	}
}

// nil = «ничего не установлено»: не равен ни одному спеку, равен только nil.
func TestEqualRestoreInputSpec_NilSemantics(t *testing.T) {
	if !equalRestoreInputSpec(nil, nil) {
		t.Error("nil == nil")
	}
	if equalRestoreInputSpec(nil, &RestoreInputSpec{}) || equalRestoreInputSpec(&RestoreInputSpec{}, nil) {
		t.Error("nil не равен установленному спеку")
	}
}

// Пустой и nil-слайс — ОДНО И ТО ЖЕ. На этом стоят все существующие
// precondition-сиды тестов: они задают только PolicyMark и WANIPs, а
// остальные входы приходят из продакшн-кода как nil либо как пустой слайс.
// reflect.DeepEqual здесь дал бы переустановку правил на каждом тике.
func TestEqualRestoreInputSpec_NilSliceEqualsEmpty(t *testing.T) {
	a := RestoreInputSpec{PolicyMark: "0xffffaaa"}
	b := RestoreInputSpec{
		PolicyMark:        "0xffffaaa",
		WANIPs:            []string{},
		LANBridges:        []LANBridgeDNSRedir{},
		BypassUDPPorts:    []PortRange{},
		BypassTCPPorts:    []PortRange{},
		BypassCIDRs:       []string{},
		IngressInterfaces: []string{},
		QoSClasses:        []QoSClassSpec{},
	}
	if !equalRestoreInputSpec(&a, &b) {
		t.Error("nil-слайс и пустой слайс обязаны быть равны")
	}
}

// СТРАХОВКА: блокировка читается из ТОГО ЖЕ дампа mangle, что и джампы
// перехвата, — два `-S` на probe, как и раньше. Живой блокировкой считается
// цепочка ВМЕСТЕ с её PREROUTING-джампом: в цепочку без джампа никто не
// заходит, дропа нет, трафик течёт в WAN.
func TestProbeAll_BlackholeFromSameDump(t *testing.T) {
	for _, tc := range []struct {
		name        string
		chain, jump bool
		want        bool
	}{
		{"цепочка и джамп", true, true, true},
		{"цепочка без джампа", true, false, false},
		{"джамп без цепочки", false, true, false},
		{"ничего нашего", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			it := &IPTables{runIPTablesOut: func(_ context.Context, args ...string) (string, error) {
				calls++
				out := jumpsPresentDump()
				if len(args) >= 2 && args[1] == "mangle" {
					if tc.chain {
						out += "-N " + BlackholeChain + "\n"
					}
					if tc.jump {
						out += "-A PREROUTING -m conntrack ! --ctstate INVALID -j " + BlackholeChain + "\n"
					}
				}
				return out, nil
			}}
			installed, jumps, blackhole, err := it.probeAll(context.Background())
			if err != nil {
				t.Fatalf("probeAll: %v", err)
			}
			if !installed || !jumps {
				t.Errorf("installed=%v jumps=%v, want true/true", installed, jumps)
			}
			if blackhole != tc.want {
				t.Errorf("blackhole=%v, want %v", blackhole, tc.want)
			}
			if calls != 2 {
				t.Errorf("дампов %d, want 2 (mangle+nat)", calls)
			}
		})
	}
}
