package tgwebproxy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func stubDirectWANSeams(t *testing.T, netfilterDir, hookPath string, execFn func(env []string, name string, args ...string) ([]byte, error)) {
	t.Helper()
	origDir := directWANNetfilterDir
	origHook := directWANHookPath
	origStat := directWANStat
	origExec := directWANExecOutput
	origIpt := directWANIptablesBin

	directWANNetfilterDir = netfilterDir
	directWANHookPath = hookPath
	directWANStat = os.Stat
	if execFn != nil {
		directWANExecOutput = execFn
	}
	directWANIptablesBin = func() string { return "iptables" }

	t.Cleanup(func() {
		directWANNetfilterDir = origDir
		directWANHookPath = origHook
		directWANStat = origStat
		directWANExecOutput = origExec
		directWANIptablesBin = origIpt
	})
}

func TestRenderDirectWANHook(t *testing.T) {
	hook := renderDirectWANHook(8443)
	for _, want := range []string{
		`PORT="8443"`,
		`CMT="AWGM_TG_MTPROTO_WAN"`,
		`[ "$type" = "ip6tables" ] && exit 0`,
		`[ "$table" = "filter" ] || [ -z "$table" ] || exit 0`,
		`xt_comment.ko`,
		`insmod`,
		`-w 5`,
		`ok=1`,
		`logger -t awgm-tg-mtproto-wan`,
		`exit 1`,
		`_NDM_INPUT`,
		`_NDM_IP_PUBLIC`,
		`-i "$ifc"`,
		`--dport "$PORT"`,
		`-j ACCEPT`,
	} {
		if !strings.Contains(hook, want) {
			t.Fatalf("rendered hook missing %q:\n%s", want, hook)
		}
	}
	for _, forbidden := range []string{`-I INPUT`, `-C INPUT`} {
		if strings.Contains(hook, forbidden) {
			t.Fatalf("rendered hook must NOT insert unscoped %q:\n%s", forbidden, hook)
		}
	}

	if _, err := exec.LookPath("/bin/sh"); err == nil {
		tmp := t.TempDir()
		hookPath := filepath.Join(tmp, "68-awgm-tg-mtproto-wan.sh")
		logPath := filepath.Join(tmp, "iptables.log")
		mockIpt := filepath.Join(tmp, "iptables")

		// Execute the actual rendered POSIX shell hook against a realistic
		// Keenetic _NDM_INPUT topology including WAN, LAN (br0), Guest (br1), and VPNs.
		mockScript := `#!/bin/sh
echo "$*" >> "` + logPath + `"
shift_args="$*"
case " $shift_args " in
  *" -L _NDM_IP_PUBLIC "*) exit 0 ;;
  *" -S _NDM_INPUT "*)
    cat <<'EOFRULES'
-N _NDM_INPUT
-A _NDM_INPUT -i eth3 -m set --match-set _NDM_I4AL_eth3 dst -m ndmslin --slin 3 (public) -j _NDM_IP_PUBLIC
-A _NDM_INPUT -i br0 -m set --match-set _NDM_I4AL_br0 dst -m ndmslin --slin 1 (private) -j _NDM_IP_PRIVATE
-A _NDM_INPUT -i br1 -m set --match-set _NDM_I4AL_br1 dst -m ndmslin --slin 2 (protected) -j _NDM_IP_PROTECT
-A _NDM_INPUT -i qmi_br0 -m set --match-set _NDM_I4AL_qmi_br0 dst -m ndmslin --slin 3 (public) -j _NDM_IP_PUBLIC
-A _NDM_INPUT -i ppp0 -m set --match-set _NDM_I4AL_ppp0 dst -m ndmslin --slin 3 (public) -j _NDM_IP_PUBLIC
-A _NDM_INPUT -i opkgtun10 -m set --match-set _NDM_I4AL_opkgtun10 dst -m ndmslin --slin 3 (public) -j _NDM_IP_PUBLIC
-A _NDM_INPUT -i t2s0 -m set --match-set _NDM_I4AL_t2s0 dst -m ndmslin --slin 3 (public) -j _NDM_IP_PUBLIC
-A _NDM_INPUT -i nwg2 -m set --match-set _NDM_I4AL_nwg2 dst -m ndmslin --slin 3 (public) -j _NDM_IP_PUBLIC
EOFRULES
    exit 0
    ;;
  *" -C _NDM_IP_PUBLIC "*) exit 1 ;;
  *" -I _NDM_IP_PUBLIC 1 "*) exit 0 ;;
esac
exit 0
`
		if err := os.WriteFile(mockIpt, []byte(mockScript), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(hookPath, []byte(hook), 0755); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command("/bin/sh", hookPath)
		cmd.Env = []string{
			"PATH=" + tmp + ":" + os.Getenv("PATH"),
			"type=iptables",
			"table=filter",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("executing rendered hook failed: %v\n%s", err, string(out))
		}
		loggedBytes, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		logged := string(loggedBytes)
		for _, wantWAN := range []string{
			"-I _NDM_IP_PUBLIC 1 -i eth3 -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT",
			"-I _NDM_IP_PUBLIC 1 -i qmi_br0 -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT",
			"-I _NDM_IP_PUBLIC 1 -i ppp0 -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT",
		} {
			if !strings.Contains(logged, wantWAN) {
				t.Fatalf("expected hook run to insert WAN rule %q, got log:\n%s", wantWAN, logged)
			}
		}
		for _, badInsert := range []string{
			"-I INPUT",
			"-i br0",
			"-i br1",
			"-i opkgtun10",
			"-i t2s0",
			"-i nwg2",
		} {
			if strings.Contains(logged, badInsert) {
				t.Fatalf("hook must NOT allow %q (LAN/Guest/VPN/unscoped INPUT), got log:\n%s", badInsert, logged)
			}
		}
	}
}

func TestSyncDirectWANFirewall_LifecycleAndCleanup(t *testing.T) {
	tmp := t.TempDir()
	nfDir := filepath.Join(tmp, "netfilter.d")
	hookPath := filepath.Join(nfDir, "68-awgm-tg-mtproto-wan.sh")

	// 1. Outside Keenetic (netfilter.d absent): Sync is no-op, CheckDirectWANFirewall returns NotApplicable
	called := 0
	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		called++
		return nil, nil
	})
	if err := SyncDirectWANFirewall(8443, true); err != nil {
		t.Fatalf("expected nil outside Keenetic, got %v", err)
	}
	if called != 0 {
		t.Fatalf("expected no exec outside Keenetic, got %d calls", called)
	}
	state, err := CheckDirectWANFirewall(8443)
	if err != nil || state != DirectWANFirewallNotApplicable {
		t.Fatalf("expected DirectWANFirewallNotApplicable outside Keenetic, got state=%q err=%v", state, err)
	}
	if IsDirectWANPortOpen(8443) {
		t.Fatalf("expected IsDirectWANPortOpen=false when netfilter.d absent (not applicable)")
	}

	// 1b. Non-NotExist os.Stat error (EACCES / EIO) -> CheckDirectWANFirewall returns Unknown, Sync returns error
	stubDirectWANSeams(t, nfDir, hookPath, nil)
	directWANStat = func(string) (os.FileInfo, error) {
		return nil, &os.PathError{Op: "stat", Path: nfDir, Err: syscall.EACCES}
	}
	state, err = CheckDirectWANFirewall(8443)
	if state != DirectWANFirewallUnknown || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected DirectWANFirewallUnknown + permission err, got state=%q err=%v", state, err)
	}
	if err := SyncDirectWANFirewall(8443, true); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected SyncDirectWANFirewall to return stat permission error, got %v", err)
	}
	directWANStat = os.Stat

	// Create Keenetic netfilter dir
	if err := os.MkdirAll(nfDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 2. Enable port 8443 -> writes executable hook and runs /bin/sh hook with type=iptables & table=filter
	t.Setenv("type", "ip6tables")
	t.Setenv("table", "nat")
	var shRuns []string
	var shEnvs [][]string
	var delArgs [][]string
	inputRules := "-P INPUT DROP\n"
	ndmInputRules := strings.Join([]string{
		"-A _NDM_INPUT -i eth3 -m ndmslin --slin 3 (public) -j _NDM_IP_PUBLIC",
		"-A _NDM_INPUT -i qmi_br0 -m ndmslin --slin 3 (public) -j _NDM_IP_PUBLIC",
		"-A _NDM_INPUT -i br0 -m ndmslin --slin 1 (private) -j _NDM_IP_PRIVATE",
		"-A _NDM_INPUT -i br1 -m ndmslin --slin 2 (protected) -j _NDM_IP_PROTECT",
		"-A _NDM_INPUT -i opkgtun10 -m ndmslin --slin 3 (public) -j _NDM_IP_PUBLIC",
	}, "\n")
	pubRules := "-P _NDM_IP_PUBLIC RETURN\n"

	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		if name == "/bin/sh" {
			shRuns = append(shRuns, strings.Join(args, " "))
			shEnvs = append(shEnvs, append([]string(nil), env...))
			return nil, nil
		}
		if len(args) >= 4 && args[0] == "-w" && args[1] == "5" && args[2] == "-S" {
			switch args[3] {
			case "INPUT":
				return []byte(inputRules), nil
			case "_NDM_INPUT":
				return []byte(ndmInputRules), nil
			case "_NDM_IP_PUBLIC":
				return []byte(pubRules), nil
			}
		}
		if len(args) >= 3 && args[0] == "-w" && args[1] == "5" && args[2] == "-D" {
			delArgs = append(delArgs, append([]string(nil), args...))
		}
		return nil, nil
	})

	if err := SyncDirectWANFirewall(8443, true); err != nil {
		t.Fatalf("SyncDirectWANFirewall(8443, true): %v", err)
	}
	st, err := os.Lstat(hookPath)
	if err != nil {
		t.Fatalf("expected hook file %s to be created: %v", hookPath, err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0755 {
		t.Fatalf("expected regular 0755 hook script, mode=%v", st.Mode())
	}
	if len(shRuns) != 1 || shRuns[0] != hookPath {
		t.Fatalf("expected /bin/sh %s execution, got %v", hookPath, shRuns)
	}
	envJoined := strings.Join(shEnvs[0], "\n")
	if !strings.Contains(envJoined, "type=iptables") || !strings.Contains(envJoined, "table=filter") ||
		strings.Contains(envJoined, "type=ip6tables") || strings.Contains(envJoined, "table=nat") {
		t.Fatalf("expected sanitized hook env with type=iptables and table=filter, got %v", shEnvs[0])
	}

	// 2b. Degraded 0644 permission or symlink at hookPath must be repaired atomically to regular 0755
	if err := os.Chmod(hookPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := SyncDirectWANFirewall(8443, true); err != nil {
		t.Fatalf("SyncDirectWANFirewall repair 0644: %v", err)
	}
	st, err = os.Lstat(hookPath)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0755 {
		t.Fatalf("expected 0644 hook repaired to regular 0755, got mode=%v err=%v", st.Mode(), err)
	}

	decoyTarget := filepath.Join(tmp, "decoy-target")
	if err := os.WriteFile(decoyTarget, []byte("untouched"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(hookPath)
	if err := os.Symlink(decoyTarget, hookPath); err != nil {
		t.Fatal(err)
	}
	if err := SyncDirectWANFirewall(8443, true); err != nil {
		t.Fatalf("SyncDirectWANFirewall repair symlink: %v", err)
	}
	st, err = os.Lstat(hookPath)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0755 {
		t.Fatalf("expected symlink hook replaced atomically with regular 0755 file, got mode=%v err=%v", st.Mode(), err)
	}
	decoyData, _ := os.ReadFile(decoyTarget)
	if string(decoyData) != "untouched" {
		t.Fatalf("symlink target was modified! got %q", string(decoyData))
	}

	// 3. Port change 8443 -> 9443 deletes ALL legacy INPUT rules (even matching 9443) AND old/unscoped _NDM_IP_PUBLIC rules,
	//    while preserving properly WAN-scoped rules for keepPort 9443 and unrelated AWGM rules.
	inputRules = strings.Join([]string{
		"-A INPUT -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT",
		"-A INPUT -p tcp -m tcp --dport 2053 -m comment --comment AWGM_REALITY_WAN -j ACCEPT",
		"-A INPUT -p tcp -m tcp --dport 9443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT",
	}, "\n")
	pubRules = strings.Join([]string{
		`-A _NDM_IP_PUBLIC -i qmi_br0 -p tcp -m tcp --dport 8443 -m comment --comment "AWGM_TG_MTPROTO_WAN" -j ACCEPT`,
		`-A _NDM_IP_PUBLIC -p tcp -m tcp --dport 9443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT`,
		`-A _NDM_IP_PUBLIC -i qmi_br0 -p tcp -m tcp --dport 9443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT`,
		`-A _NDM_IP_PUBLIC -p tcp -m tcp --dport 9009 -m comment --comment AWGM_INGRESS_WAN -j ACCEPT`,
	}, "\n")
	shRuns = nil
	delArgs = nil

	if err := SyncDirectWANFirewall(9443, true); err != nil {
		t.Fatalf("SyncDirectWANFirewall(9443, true): %v", err)
	}
	hookBytes, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read updated hook: %v", err)
	}
	if !strings.Contains(string(hookBytes), `PORT="9443"`) {
		t.Fatalf("expected hook updated to 9443, got:\n%s", string(hookBytes))
	}
	if len(delArgs) != 4 {
		t.Fatalf("expected 4 deletions (INPUT 8443, INPUT 9443, _NDM_IP_PUBLIC qmi_br0 8443, unscoped _NDM_IP_PUBLIC 9443), got %+v", delArgs)
	}
	wantDel0 := []string{"-w", "5", "-D", "INPUT", "-p", "tcp", "-m", "tcp", "--dport", "8443", "-m", "comment", "--comment", "AWGM_TG_MTPROTO_WAN", "-j", "ACCEPT"}
	wantDel1 := []string{"-w", "5", "-D", "INPUT", "-p", "tcp", "-m", "tcp", "--dport", "9443", "-m", "comment", "--comment", "AWGM_TG_MTPROTO_WAN", "-j", "ACCEPT"}
	wantDel2 := []string{"-w", "5", "-D", "_NDM_IP_PUBLIC", "-i", "qmi_br0", "-p", "tcp", "-m", "tcp", "--dport", "8443", "-m", "comment", "--comment", "AWGM_TG_MTPROTO_WAN", "-j", "ACCEPT"}
	wantDel3 := []string{"-w", "5", "-D", "_NDM_IP_PUBLIC", "-p", "tcp", "-m", "tcp", "--dport", "9443", "-m", "comment", "--comment", "AWGM_TG_MTPROTO_WAN", "-j", "ACCEPT"}
	if !reflect.DeepEqual(delArgs[0], wantDel0) || !reflect.DeepEqual(delArgs[1], wantDel1) ||
		!reflect.DeepEqual(delArgs[2], wantDel2) || !reflect.DeepEqual(delArgs[3], wantDel3) {
		t.Fatalf("unexpected delete args: got %+v", delArgs)
	}

	// 4. CheckDirectWANFirewall & IsDirectWANPortOpen inspect _NDM_IP_PUBLIC scoped to WAN interfaces without requiring INPUT
	inputRules = "-P INPUT DROP\n"
	pubRules = "-A _NDM_IP_PUBLIC -i qmi_br0 -p tcp -m tcp --dport 9443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n"
	if st, err := CheckDirectWANFirewall(9443); err != nil || st != DirectWANFirewallOpen {
		t.Fatalf("expected CheckDirectWANFirewall(9443)=open, got state=%q err=%v", st, err)
	}
	if !IsDirectWANPortOpen(9443) {
		t.Fatalf("expected IsDirectWANPortOpen(9443)=true when _NDM_IP_PUBLIC allows WAN interface")
	}
	if st, err := CheckDirectWANFirewall(8443); err != nil || st != DirectWANFirewallClosed {
		t.Fatalf("expected CheckDirectWANFirewall(8443)=closed when absent, got state=%q err=%v", st, err)
	}
	// Rule scoped only to guest br1 / VPN opkgtun10 does NOT count as WAN open
	pubRules = "-A _NDM_IP_PUBLIC -i br1 -p tcp -m tcp --dport 9443 -j ACCEPT\n" +
		"-A _NDM_IP_PUBLIC -i opkgtun10 -p tcp -m tcp --dport 9443 -j ACCEPT\n"
	if st, err := CheckDirectWANFirewall(9443); err != nil || st != DirectWANFirewallClosed {
		t.Fatalf("expected CheckDirectWANFirewall(9443)=closed when only br1/opkgtun10 present, got state=%q err=%v", st, err)
	}

	// 5. Disable -> deletes hook and cleans up rules
	delArgs = nil
	inputRules = "-A INPUT -p tcp -m tcp --dport 9443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n"
	pubRules = "-A _NDM_IP_PUBLIC -i qmi_br0 -p tcp -m tcp --dport 9443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n"
	if err := SyncDirectWANFirewall(0, false); err != nil {
		t.Fatalf("SyncDirectWANFirewall(0, false): %v", err)
	}
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Fatalf("expected hook file to be removed when disabled")
	}
	if len(delArgs) != 2 {
		t.Fatalf("expected cleanup of both chains on disable, got %+v", delArgs)
	}
}

func TestService_DirectWANFirewallLifecycle(t *testing.T) {
	svc, _, _ := setupTestService(t)

	type fwCall struct {
		port    int
		enabled bool
	}
	var calls []fwCall
	svc.directWANFirewall = func(port int, enabled bool) error {
		calls = append(calls, fwCall{port: port, enabled: enabled})
		return nil
	}

	// Start in default ScenarioDual (port 8443) -> opens 8443
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(calls) != 1 || calls[0] != (fwCall{port: 8443, enabled: true}) {
		t.Fatalf("expected Start to open port 8443, got %+v", calls)
	}

	// Switch to cdn_http -> closes direct WAN port
	if err := svc.UpdateConfig(Config{Scenario: ScenarioCDNHTTP}); err != nil {
		t.Fatalf("UpdateConfig cdn_http: %v", err)
	}
	if got := calls[len(calls)-1]; got != (fwCall{port: 0, enabled: false}) {
		t.Fatalf("expected cdn_http switch to close WAN firewall, got %+v", got)
	}

	// Switch to direct_fake_tls on custom port 9443 -> opens 9443
	if err := svc.UpdateConfig(Config{Scenario: ScenarioDirectFakeTLS, DirectPort: 9443}); err != nil {
		t.Fatalf("UpdateConfig direct_fake_tls: %v", err)
	}
	if got := calls[len(calls)-1]; got != (fwCall{port: 9443, enabled: true}) {
		t.Fatalf("expected direct_fake_tls to open 9443, got %+v", got)
	}

	// Candidate CommitPrepared -> opens new port 10443; RollbackPrepared -> restores 9443
	cand := svc.GetInternalConfig()
	cand.DirectPort = 10443
	txID, err := svc.PrepareCandidate("tx-fw-test", cand)
	if err != nil {
		t.Fatalf("PrepareCandidate: %v", err)
	}
	if err := svc.CommitPrepared(txID); err != nil {
		t.Fatalf("CommitPrepared: %v", err)
	}
	if got := calls[len(calls)-1]; got != (fwCall{port: 10443, enabled: true}) {
		t.Fatalf("expected CommitPrepared to open 10443, got %+v", got)
	}
	if err := svc.RollbackPrepared(txID); err != nil {
		t.Fatalf("RollbackPrepared: %v", err)
	}
	if got := calls[len(calls)-1]; got != (fwCall{port: 9443, enabled: true}) {
		t.Fatalf("expected RollbackPrepared to restore 9443, got %+v", got)
	}

	// ShutdownRuntime must NOT close firewall rules (restart/upgrade persistence)
	callsBeforeShutdown := len(calls)
	if err := svc.ShutdownRuntime(context.Background()); err != nil {
		t.Fatalf("ShutdownRuntime: %v", err)
	}
	if len(calls) != callsBeforeShutdown {
		t.Fatalf("ShutdownRuntime must not mutate WAN firewall rules, got %+v", calls[callsBeforeShutdown:])
	}

	// Explicit Stop DOES close WAN firewall
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := calls[len(calls)-1]; got != (fwCall{port: 0, enabled: false}) {
		t.Fatalf("expected explicit Stop to close WAN firewall, got %+v", got)
	}

	// Serialized public SyncDirectWANFirewall() uses live s.config
	callsBeforeSync := len(calls)
	if err := svc.SyncDirectWANFirewall(); err != nil {
		t.Fatalf("SyncDirectWANFirewall: %v", err)
	}
	if len(calls) != callsBeforeSync+1 || calls[len(calls)-1] != (fwCall{port: 0, enabled: false}) {
		t.Fatalf("expected SyncDirectWANFirewall() to sync current disabled state, got %+v", calls[callsBeforeSync:])
	}

	// UpdateConfig rejects invalid / reserved DirectPort
	for _, badPort := range []int{-1, 22, 80, 443, 1023, 1099, 8084, 8085, 9009, 51820, 65536} {
		if err := svc.UpdateConfig(Config{Scenario: ScenarioDirectFakeTLS, DirectPort: badPort}); err == nil {
			t.Fatalf("expected UpdateConfig to reject unsafe DirectPort %d", badPort)
		}
		if _, err := svc.PrepareCandidate("tx-bad-disabled", Config{Enabled: false, Scenario: ScenarioDirectFakeTLS, DirectPort: badPort}); err == nil {
			t.Fatalf("expected PrepareCandidate to reject unsafe DirectPort %d even when disabled", badPort)
		}
	}

	// applyConfigLocked rejects invalid DirectPort even on Start()
	svc.mu.Lock()
	svc.config.Scenario = ScenarioDirectFakeTLS
	svc.config.DirectPort = 22
	svc.mu.Unlock()
	if err := svc.Start(); err == nil {
		t.Fatalf("expected Start() / applyConfigLocked to reject unsafe DirectPort 22")
	}
	svc.mu.Lock()
	svc.config.DirectPort = 9443
	svc.mu.Unlock()
	if err := svc.Start(); err != nil {
		t.Fatalf("Start recovery on 9443: %v", err)
	}

	// Restart reconciles WAN firewall
	callsBeforeRestart := len(calls)
	if err := svc.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if len(calls) != callsBeforeRestart+1 || calls[len(calls)-1] != (fwCall{port: 9443, enabled: true}) {
		t.Fatalf("expected Restart to reconcile port 9443, got %+v", calls[callsBeforeRestart:])
	}

	// StartConfigured closes WAN firewall when ApplyWorkers returns an error
	origMtg := svc.workers.mtg
	svc.workers.mtg = &failingDirectMtg{next: origMtg}
	defer func() { svc.workers.mtg = origMtg }()
	callsBeforeFail := len(calls)
	if err := svc.StartConfigured(); err == nil {
		t.Fatalf("expected StartConfigured to fail with failing worker engine")
	}
	if len(calls) != callsBeforeFail+1 || calls[len(calls)-1] != (fwCall{port: 0, enabled: false}) {
		t.Fatalf("expected StartConfigured failure to close WAN firewall, got %+v", calls[callsBeforeFail:])
	}
}

type failingDirectMtg struct {
	next mtgRuntime
}

func (f *failingDirectMtg) Apply(cfg Config) error {
	if cfg.Enabled {
		return errors.New("simulated worker start failure")
	}
	return f.next.Apply(cfg)
}

func (f *failingDirectMtg) Restart() error        { return f.next.Restart() }
func (f *failingDirectMtg) Stop()                 { f.next.Stop() }
func (f *failingDirectMtg) IsDirectRunning() bool { return f.next.IsDirectRunning() }
func (f *failingDirectMtg) IsRawRunning() bool    { return f.next.IsRawRunning() }

func TestBuildTgLinks_DirectFakeTLSOmitsWebLinks(t *testing.T) {
	rev, err := BuildTgLinks(Config{
		Enabled:        true,
		Scenario:       ScenarioDirectFakeTLS,
		DirectHost:     "203.0.113.10",
		DirectPort:     8443,
		TlsDomain:      "www.google.com",
		PublicHostname: "stale.example.com",
		Endpoints: []WebProxyEndpoint{
			{ID: "ep-1", Hostname: "stale.example.com", Enabled: true},
		},
		Secret: "00112233445566778899aabbccddeeff",
	}, "")
	if err != nil {
		t.Fatalf("BuildTgLinks: %v", err)
	}
	if rev.TgURL != "" || rev.TmeURL != "" || rev.BridgeURL != "" || len(rev.WebProxies) != 0 {
		t.Fatalf("expected no web links in direct_fake_tls, got tg=%q tme=%q bridge=%q eps=%+v", rev.TgURL, rev.TmeURL, rev.BridgeURL, rev.WebProxies)
	}
	if rev.MtproxyURL == "" {
		t.Fatalf("expected MtproxyURL to be populated")
	}
}

func TestValidateDirectPort_AndHardenedWANFirewall(t *testing.T) {
	for _, bad := range []int{0, -10, 22, 53, 80, 443, 1023, 1099, 2398, 3553, 8084, 8085, 8086, 9007, 9008, 9009, 51820, 65536} {
		if err := ValidateDirectPort(bad); err == nil {
			t.Fatalf("expected ValidateDirectPort(%d) to fail", bad)
		}
	}
	for _, good := range []int{1024, 2053, 8443, 9443, 65535} {
		if err := ValidateDirectPort(good); err != nil {
			t.Fatalf("expected ValidateDirectPort(%d) to succeed, got %v", good, err)
		}
	}

	tmp := t.TempDir()
	nfDir := filepath.Join(tmp, "netfilter.d")
	hookPath := filepath.Join(nfDir, "68-awgm-tg-mtproto-wan.sh")
	if err := os.MkdirAll(nfDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. SyncDirectWANFirewall refuses privileged port 22 (does NOT write hook, cleans up existing rules, returns error)
	if err := os.WriteFile(hookPath, []byte("stale"), 0755); err != nil {
		t.Fatal(err)
	}
	var delArgs [][]string
	inputRules := "-A INPUT -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n" +
		"-A INPUT -p tcp -m tcp --dport 8444 -m comment --comment AWGM_TG_MTPROTO_WAN_EXTRA -j ACCEPT\n"
	ndmInputRules := "-A _NDM_INPUT -i eth3 -m ndmslin --slin 3 (public) -j _NDM_IP_PUBLIC\n" +
		"-A _NDM_INPUT -i ppp0 -m ndmslin --slin 3 (public) -j _NDM_IP_PUBLIC\n"
	pubRules := "-P _NDM_IP_PUBLIC RETURN\n"
	failWithW := false

	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		if failWithW && len(args) > 0 && args[0] == "-w" {
			return nil, errors.New("unknown option -w")
		}
		chain := ""
		if len(args) >= 2 {
			chain = args[len(args)-1]
		}
		if (len(args) >= 2 && args[0] == "-S") ||
			(len(args) >= 3 && args[0] == "-w" && args[1] == "-S") ||
			(len(args) >= 4 && args[0] == "-w" && args[1] == "5" && args[2] == "-S") {
			switch chain {
			case "INPUT":
				return []byte(inputRules), nil
			case "_NDM_INPUT":
				return []byte(ndmInputRules), nil
			default:
				return []byte(pubRules), nil
			}
		}
		if (len(args) >= 2 && args[0] == "-D") ||
			(len(args) >= 3 && args[0] == "-w" && args[1] == "-D") ||
			(len(args) >= 4 && args[0] == "-w" && args[1] == "5" && args[2] == "-D") {
			delArgs = append(delArgs, append([]string(nil), args...))
		}
		return nil, nil
	})

	if err := SyncDirectWANFirewall(22, true); !errors.Is(err, ErrInvalidDirectPort) {
		t.Fatalf("expected SyncDirectWANFirewall(22, true) to return ErrInvalidDirectPort, got %v", err)
	}
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Fatalf("expected unsafe port 22 not to create/retain hook")
	}
	// Exact comment token check: AWGM_TG_MTPROTO_WAN deleted, AWGM_TG_MTPROTO_WAN_EXTRA untouched!
	if len(delArgs) != 1 || !strings.Contains(strings.Join(delArgs[0], " "), "8443") {
		t.Fatalf("expected only exact AWGM_TG_MTPROTO_WAN rule deleted, got %+v", delArgs)
	}

	// 2. Fallback when `iptables -w` fails -> succeeds without `-w`
	failWithW = true
	delArgs = nil
	inputRules = "-A INPUT -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n"
	pubRules = "-A _NDM_IP_PUBLIC -i ppp0 -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n"
	if st, err := CheckDirectWANFirewall(8443); err != nil || st != DirectWANFirewallOpen {
		t.Fatalf("expected CheckDirectWANFirewall to succeed via fallback without -w, got state=%q err=%v", st, err)
	}
	if err := cleanupDirectWANRules(0, nil); err != nil {
		t.Fatalf("expected cleanupDirectWANRules fallback without -w to succeed, got %v", err)
	}
	if len(delArgs) != 2 || delArgs[0][0] != "-D" || delArgs[1][0] != "-D" {
		t.Fatalf("expected cleanup fallback without -w to delete both rules, got %+v", delArgs)
	}

	// 3. Rejects LAN / guest / VPN / source-scoped / negated rules, accepts PPP WAN interface in _NDM_IP_PUBLIC
	failWithW = false
	for _, badRule := range []string{
		"-A _NDM_IP_PUBLIC -i br0 -p tcp -m tcp --dport 8443 -j ACCEPT\n",
		"-A _NDM_IP_PUBLIC -i br1 -p tcp -m tcp --dport 8443 -j ACCEPT\n",
		"-A _NDM_IP_PUBLIC -i lo -p tcp -m tcp --dport 8443 -j ACCEPT\n",
		"-A _NDM_IP_PUBLIC -i nwg0 -p tcp -m tcp --dport 8443 -j ACCEPT\n",
		"-A _NDM_IP_PUBLIC -i opkgtun10 -p tcp -m tcp --dport 8443 -j ACCEPT\n",
		"-A _NDM_IP_PUBLIC -s 192.168.1.0/24 -p tcp -m tcp --dport 8443 -j ACCEPT\n",
		"-A _NDM_IP_PUBLIC -p tcp -m tcp ! --dport 8443 -j ACCEPT\n",
		"-A _NDM_IP_PUBLIC ! -p tcp --dport 8443 -j ACCEPT\n",
	} {
		pubRules = badRule
		if st, err := CheckDirectWANFirewall(8443); err != nil || st != DirectWANFirewallClosed {
			t.Fatalf("expected CheckDirectWANFirewall to treat %q as closed, got state=%q err=%v", badRule, st, err)
		}
	}
	pubRules = "-A _NDM_IP_PUBLIC -i ppp0 -p tcp -m tcp --dport 8443 -j ACCEPT\n"
	if st, err := CheckDirectWANFirewall(8443); err != nil || st != DirectWANFirewallOpen {
		t.Fatalf("expected CheckDirectWANFirewall to accept explicit WAN interface rule, got state=%q err=%v", st, err)
	}

	// 4. Distinguishes missing _NDM_IP_PUBLIC chain (Closed) from transient xtables lock/exec error (Unknown)
	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		return []byte("iptables: No chain/target/match by that name.\n"), errors.New("exit status 1")
	})
	if st, err := CheckDirectWANFirewall(8443); err != nil || st != DirectWANFirewallClosed {
		t.Fatalf("expected Closed/nil when _NDM_IP_PUBLIC chain does not exist, got state=%q err=%v", st, err)
	}

	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		chain := args[len(args)-1]
		if chain == "_NDM_IP_PUBLIC" {
			return []byte("Another app is currently holding the xtables lock.\n"), errors.New("exit status 4")
		}
		return []byte(ndmInputRules), nil
	})
	if st, err := CheckDirectWANFirewall(8443); st != DirectWANFirewallUnknown || err == nil {
		t.Fatalf("expected Unknown + err on transient _NDM_IP_PUBLIC error, got state=%q err=%v", st, err)
	}

	// 5. Unknown when _NDM_INPUT fails on Keenetic
	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		chain := args[len(args)-1]
		if chain == "_NDM_IP_PUBLIC" {
			return []byte("-A _NDM_IP_PUBLIC -i ppp0 -p tcp -m tcp --dport 8443 -j ACCEPT\n"), nil
		}
		return []byte("iptables unavailable"), errors.New("iptables unavailable")
	})
	if st, err := CheckDirectWANFirewall(8443); st != DirectWANFirewallUnknown || err == nil {
		t.Fatalf("expected CheckDirectWANFirewall to return Unknown when _NDM_INPUT query fails, got state=%q err=%v", st, err)
	}
}

func TestSyncDirectWANFirewall_CleanupErrorPropagation(t *testing.T) {
	tmp := t.TempDir()
	nfDir := filepath.Join(tmp, "netfilter.d")
	hookPath := filepath.Join(nfDir, "68-awgm-tg-mtproto-wan.sh")
	if err := os.MkdirAll(nfDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. iptables -D failure on disable (SyncDirectWANFirewall(0, false)) returns non-nil error
	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[2] == "-S" {
			if args[3] == "_NDM_IP_PUBLIC" {
				return []byte("-A _NDM_IP_PUBLIC -i eth3 -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n"), nil
			}
			return []byte("-P INPUT DROP\n"), nil
		}
		if len(args) >= 3 && (args[0] == "-D" || (args[0] == "-w" && (args[1] == "-D" || (len(args) >= 3 && args[2] == "-D")))) {
			return []byte("iptables: Resource temporarily unavailable"), errors.New("exit status 1")
		}
		return nil, nil
	})
	err := SyncDirectWANFirewall(0, false)
	if err == nil || !strings.Contains(err.Error(), "delete _NDM_IP_PUBLIC rule") {
		t.Fatalf("expected SyncDirectWANFirewall(0, false) to propagate iptables -D error, got %v", err)
	}

	// 2. iptables -D failure on port change (8443 -> 9443) returns non-nil error
	err = SyncDirectWANFirewall(9443, true)
	if err == nil || !strings.Contains(err.Error(), "delete _NDM_IP_PUBLIC rule") {
		t.Fatalf("expected SyncDirectWANFirewall(9443, true) to propagate stale rule delete error, got %v", err)
	}

	// 3. iptables -S read error (non-NoSuchChain) propagates instead of silently skipping chain
	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[len(args)-2] == "-S" && args[len(args)-1] == "_NDM_IP_PUBLIC" {
			return []byte("Another app is currently holding the xtables lock."), errors.New("exit status 4")
		}
		if len(args) >= 2 && args[len(args)-2] == "-S" && args[len(args)-1] == "_NDM_IP_PUBLIC" {
			return []byte("Another app is currently holding the xtables lock."), errors.New("exit status 4")
		}
		return nil, nil
	})
	if err := cleanupDirectWANRules(0, nil); err == nil || !strings.Contains(err.Error(), "read _NDM_IP_PUBLIC rules") {
		t.Fatalf("expected cleanupDirectWANRules to return chain read error, got %v", err)
	}

	// 4. Service lifecycle methods propagate directWANFirewall cleanup/sync errors
	svc, _, _ := setupTestService(t)
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	simErr := errors.New("simulated iptables delete failure")
	svc.directWANFirewall = func(port int, enabled bool) error {
		return simErr
	}

	if err := svc.SyncDirectWANFirewall(); !errors.Is(err, simErr) {
		t.Fatalf("expected Service.SyncDirectWANFirewall to propagate error, got %v", err)
	}
	if err := svc.UpdateConfig(Config{Scenario: ScenarioCDNHTTP}); !errors.Is(err, simErr) {
		t.Fatalf("expected UpdateConfig(cdn_http) to propagate firewall cleanup error, got %v", err)
	}
	if err := svc.Stop(); !errors.Is(err, simErr) {
		t.Fatalf("expected Service.Stop to propagate firewall cleanup error, got %v", err)
	}

	cand := svc.GetInternalConfig()
	cand.Enabled = true
	cand.Scenario = ScenarioDirectFakeTLS
	cand.DirectPort = 9443
	txID, err := svc.PrepareCandidate("tx-fw-err", cand)
	if err != nil {
		t.Fatalf("PrepareCandidate: %v", err)
	}
	if err := svc.CommitPrepared(txID); !errors.Is(err, simErr) {
		t.Fatalf("expected CommitPrepared to propagate firewall sync error and roll back, got %v", err)
	}

	if err := svc.FactoryReset(context.Background()); !errors.Is(err, simErr) {
		t.Fatalf("expected FactoryReset to propagate firewall cleanup error, got %v", err)
	}
}

func TestDirectPortValidation_ReservedWebUIAndLifecycleFixes(t *testing.T) {
	// 1. Port 2222 (AWG Manager Web UI) and invalid ports wrap ErrInvalidDirectPort
	for _, bad := range []int{22, 1023, 2222, 65536} {
		err := ValidateDirectPort(bad)
		if !errors.Is(err, ErrInvalidDirectPort) {
			t.Fatalf("expected ValidateDirectPort(%d) to wrap ErrInvalidDirectPort, got %v", bad, err)
		}
	}

	// 2. PrepareCandidate skips ValidateDirectPort in cdn_http, but validates after normalizing empty Scenario -> dual
	svc := New(t.TempDir(), nil)
	svc.SetRuntimeDCProbeForTest(func(string) bool { return true })
	t.Cleanup(func() { _ = svc.Close() })

	cdnCfg := svc.GetInternalConfig()
	cdnCfg.Scenario = ScenarioCDNHTTP
	cdnCfg.DirectPort = 2222
	if _, err := svc.PrepareCandidate("tx-cdn", cdnCfg); err != nil {
		t.Fatalf("expected PrepareCandidate(cdn_http) to skip ValidateDirectPort even with DirectPort=2222, got %v", err)
	}

	emptyScenCfg := svc.GetInternalConfig()
	emptyScenCfg.Scenario = ""
	emptyScenCfg.DirectPort = 2222
	if _, err := svc.PrepareCandidate("tx-empty", emptyScenCfg); !errors.Is(err, ErrInvalidDirectPort) {
		t.Fatalf("expected PrepareCandidate with empty scenario (normalized to dual) and DirectPort=2222 to fail ErrInvalidDirectPort, got %v", err)
	}

	// 3. UpdateConfig allows keeping legacy DirectPort=2222 under cdn_http when DirectPort isn't changed,
	//    but rejects changing DirectPort to 2222 or switching to direct_fake_tls with 2222.
	svc.mu.Lock()
	svc.config.Scenario = ScenarioCDNHTTP
	svc.config.DirectPort = 2222
	svc.mu.Unlock()

	if err := svc.UpdateConfig(Config{DirectPort: 2222, TlsDomain: "www.example.com"}); err != nil {
		t.Fatalf("expected UpdateConfig keeping legacy DirectPort=2222 under cdn_http to succeed, got %v", err)
	}
	if err := svc.UpdateConfig(Config{Scenario: ScenarioDirectFakeTLS, DirectPort: 2222}); !errors.Is(err, ErrInvalidDirectPort) {
		t.Fatalf("expected UpdateConfig switching to direct_fake_tls with DirectPort=2222 to fail ErrInvalidDirectPort, got %v", err)
	}

	// 4. FactoryReset removes active WAN firewall hook
	tmp := t.TempDir()
	nfDir := filepath.Join(tmp, "netfilter.d")
	hookPath := filepath.Join(nfDir, "68-awgm-tg-mtproto-wan.sh")
	if err := os.MkdirAll(nfDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		return nil, nil
	})
	svc.mu.Lock()
	svc.config.Enabled = true
	svc.config.Scenario = ScenarioDirectFakeTLS
	svc.config.DirectPort = 8443
	svc.mu.Unlock()
	if err := svc.FactoryReset(context.Background()); err != nil {
		t.Fatalf("FactoryReset: %v", err)
	}
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Fatalf("expected FactoryReset to remove WAN firewall hook, stat err=%v", err)
	}

	// 5. buildStatusWebProxyLinks omits web links in direct_fake_tls
	directWithEndpoint := Config{
		Scenario:       ScenarioDirectFakeTLS,
		PublicHostname: "tg.example.com",
		Endpoints: []WebProxyEndpoint{{
			ID:         "ep-1",
			Hostname:   "tg.example.com",
			ListenPort: 8085,
		}},
	}
	if links := buildStatusWebProxyLinks(directWithEndpoint); len(links) != 0 {
		t.Fatalf("expected buildStatusWebProxyLinks to return no web links for direct_fake_tls, got %+v", links)
	}
}
