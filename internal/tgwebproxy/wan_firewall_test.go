package tgwebproxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

func stubDirectWANSeams(t *testing.T, netfilterDir, hookPath string, execFn func(env []string, name string, args ...string) ([]byte, error)) {
	t.Helper()
	origDir := directWANNetfilterDir
	origHook := directWANHookPath
	origExec := directWANExecOutput
	origIpt := directWANIptablesBin

	directWANNetfilterDir = netfilterDir
	directWANHookPath = hookPath
	if execFn != nil {
		directWANExecOutput = execFn
	}
	directWANIptablesBin = func() string { return "iptables" }

	t.Cleanup(func() {
		directWANNetfilterDir = origDir
		directWANHookPath = origHook
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
		`INPUT`,
		`_NDM_IP_PUBLIC`,
		`--dport "$PORT"`,
		`-j ACCEPT`,
	} {
		if !strings.Contains(hook, want) {
			t.Fatalf("rendered hook missing %q:\n%s", want, hook)
		}
	}
}

func TestSyncDirectWANFirewall_LifecycleAndCleanup(t *testing.T) {
	tmp := t.TempDir()
	nfDir := filepath.Join(tmp, "netfilter.d")
	hookPath := filepath.Join(nfDir, "68-awgm-tg-mtproto-wan.sh")

	// 1. Outside Keenetic (netfilter.d absent): Sync is no-op, IsDirectWANPortOpen returns true
	called := 0
	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		called++
		return nil, nil
	})
	SyncDirectWANFirewall(8443, true)
	if called != 0 {
		t.Fatalf("expected no exec outside Keenetic, got %d calls", called)
	}
	if !IsDirectWANPortOpen(8443) {
		t.Fatalf("expected IsDirectWANPortOpen=true outside Keenetic")
	}

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
	pubRules := "-P _NDM_IP_PUBLIC RETURN\n"

	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		if name == "/bin/sh" {
			shRuns = append(shRuns, strings.Join(args, " "))
			shEnvs = append(shEnvs, append([]string(nil), env...))
			return nil, nil
		}
		if len(args) >= 4 && args[0] == "-w" && args[1] == "5" && args[2] == "-S" {
			if args[3] == "INPUT" {
				return []byte(inputRules), nil
			}
			if args[3] == "_NDM_IP_PUBLIC" {
				return []byte(pubRules), nil
			}
		}
		if len(args) >= 3 && args[0] == "-w" && args[1] == "5" && args[2] == "-D" {
			delArgs = append(delArgs, append([]string(nil), args...))
		}
		return nil, nil
	})

	SyncDirectWANFirewall(8443, true)
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
	SyncDirectWANFirewall(8443, true)
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
	SyncDirectWANFirewall(8443, true)
	st, err = os.Lstat(hookPath)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0755 {
		t.Fatalf("expected symlink hook replaced atomically with regular 0755 file, got mode=%v err=%v", st.Mode(), err)
	}
	decoyData, _ := os.ReadFile(decoyTarget)
	if string(decoyData) != "untouched" {
		t.Fatalf("symlink target was modified! got %q", string(decoyData))
	}

	// 3. Port change 8443 -> 9443 deletes old AWGM_TG_MTPROTO_WAN rules (both chains, quoted & unquoted), keeps unrelated rules
	inputRules = strings.Join([]string{
		"-A INPUT -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT",
		"-A INPUT -p tcp -m tcp --dport 2053 -m comment --comment AWGM_REALITY_WAN -j ACCEPT",
		"-A INPUT -p tcp -m tcp --dport 9443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT",
	}, "\n")
	pubRules = strings.Join([]string{
		`-A _NDM_IP_PUBLIC -p tcp -m tcp --dport 8443 -m comment --comment "AWGM_TG_MTPROTO_WAN" -j ACCEPT`,
		`-A _NDM_IP_PUBLIC -p tcp -m tcp --dport 9009 -m comment --comment AWGM_INGRESS_WAN -j ACCEPT`,
	}, "\n")
	shRuns = nil
	delArgs = nil

	SyncDirectWANFirewall(9443, true)
	hookBytes, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read updated hook: %v", err)
	}
	if !strings.Contains(string(hookBytes), `PORT="9443"`) {
		t.Fatalf("expected hook updated to 9443, got:\n%s", string(hookBytes))
	}
	if len(delArgs) != 2 {
		t.Fatalf("expected exactly 2 stale rule deletions (INPUT 8443 & _NDM_IP_PUBLIC 8443), got %+v", delArgs)
	}
	wantDel0 := []string{"-w", "5", "-D", "INPUT", "-p", "tcp", "-m", "tcp", "--dport", "8443", "-m", "comment", "--comment", "AWGM_TG_MTPROTO_WAN", "-j", "ACCEPT"}
	wantDel1 := []string{"-w", "5", "-D", "_NDM_IP_PUBLIC", "-p", "tcp", "-m", "tcp", "--dport", "8443", "-m", "comment", "--comment", "AWGM_TG_MTPROTO_WAN", "-j", "ACCEPT"}
	if !reflect.DeepEqual(delArgs[0], wantDel0) || !reflect.DeepEqual(delArgs[1], wantDel1) {
		t.Fatalf("unexpected delete args: got %+v, want [%v, %v]", delArgs, wantDel0, wantDel1)
	}

	// 4. IsDirectWANPortOpen inspects both INPUT and _NDM_IP_PUBLIC accurately
	inputRules = "-A INPUT -p tcp -m tcp --dport 9443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n"
	pubRules = "-A _NDM_IP_PUBLIC -p tcp -m tcp --dport 9443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n"
	if !IsDirectWANPortOpen(9443) {
		t.Fatalf("expected IsDirectWANPortOpen(9443)=true when both chains accept it")
	}
	if IsDirectWANPortOpen(8443) {
		t.Fatalf("expected IsDirectWANPortOpen(8443)=false when absent")
	}
	pubRules = "-P _NDM_IP_PUBLIC RETURN\n"
	if IsDirectWANPortOpen(9443) {
		t.Fatalf("expected IsDirectWANPortOpen(9443)=false when missing from _NDM_IP_PUBLIC")
	}

	// 5. Disable -> deletes hook and cleans up rules
	delArgs = nil
	pubRules = "-A _NDM_IP_PUBLIC -p tcp -m tcp --dport 9443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n"
	SyncDirectWANFirewall(0, false)
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

	if err := svc.UpdateConfig(Config{DirectHost: "203.0.113.10"}); err != nil {
		t.Fatalf("UpdateConfig direct host: %v", err)
	}
	calls = nil

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
	svc.SyncDirectWANFirewall()
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

	// Start() rejects a stored invalid DirectPort
	svc.mu.Lock()
	svc.config.Scenario = ScenarioDirectFakeTLS
	svc.config.DirectPort = 22
	svc.mu.Unlock()
	if err := svc.Start(); err == nil {
		t.Fatalf("expected Start() to reject unsafe DirectPort 22")
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

	// 1. SyncDirectWANFirewall refuses privileged port 22 (does NOT write hook, cleans up existing rules)
	if err := os.WriteFile(hookPath, []byte("stale"), 0755); err != nil {
		t.Fatal(err)
	}
	var delArgs [][]string
	inputRules := "-A INPUT -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n" +
		"-A INPUT -p tcp -m tcp --dport 8444 -m comment --comment AWGM_TG_MTPROTO_WAN_EXTRA -j ACCEPT\n"
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
			if chain == "INPUT" {
				return []byte(inputRules), nil
			}
			return []byte(pubRules), nil
		}
		if (len(args) >= 2 && args[0] == "-D") ||
			(len(args) >= 3 && args[0] == "-w" && args[1] == "-D") ||
			(len(args) >= 4 && args[0] == "-w" && args[1] == "5" && args[2] == "-D") {
			delArgs = append(delArgs, append([]string(nil), args...))
		}
		return nil, nil
	})

	SyncDirectWANFirewall(22, true)
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
	pubRules = "-A _NDM_IP_PUBLIC -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n"
	if !IsDirectWANPortOpen(8443) {
		t.Fatalf("expected IsDirectWANPortOpen to succeed via fallback without -w")
	}
	cleanupDirectWANRules(0)
	if len(delArgs) != 2 || delArgs[0][0] != "-D" || delArgs[1][0] != "-D" {
		t.Fatalf("expected cleanup fallback without -w to delete both rules, got %+v", delArgs)
	}

	// 3. Rejects LAN / source-scoped / negated rules, accepts PPP WAN interface
	failWithW = false
	pubRules = "-A _NDM_IP_PUBLIC -p tcp -m tcp --dport 8443 -j ACCEPT\n"
	for _, badRule := range []string{
		"-A INPUT -i br0 -p tcp -m tcp --dport 8443 -j ACCEPT\n",
		"-A INPUT -i lo -p tcp -m tcp --dport 8443 -j ACCEPT\n",
		"-A INPUT -i nwg0 -p tcp -m tcp --dport 8443 -j ACCEPT\n",
		"-A INPUT -s 192.168.1.0/24 -p tcp -m tcp --dport 8443 -j ACCEPT\n",
		"-A INPUT -p tcp -m tcp ! --dport 8443 -j ACCEPT\n",
		"-A INPUT ! -p tcp --dport 8443 -j ACCEPT\n",
	} {
		inputRules = badRule
		if IsDirectWANPortOpen(8443) {
			t.Fatalf("expected IsDirectWANPortOpen to reject scoped/negated rule: %q", badRule)
		}
	}
	inputRules = "-A INPUT -i ppp0 -p tcp -m tcp --dport 8443 -j ACCEPT\n"
	if !IsDirectWANPortOpen(8443) {
		t.Fatalf("expected IsDirectWANPortOpen to accept explicit WAN interface rule")
	}

	// 4. Distinguishes missing _NDM_IP_PUBLIC chain from transient xtables lock/exec error
	inputRules = "-A INPUT -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n"
	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		chain := args[len(args)-1]
		if chain == "INPUT" {
			return []byte(inputRules), nil
		}
		return []byte("iptables: No chain/target/match by that name.\n"), errors.New("exit status 1")
	})
	if !IsDirectWANPortOpen(8443) {
		t.Fatalf("expected IsDirectWANPortOpen=true when _NDM_IP_PUBLIC chain simply does not exist")
	}

	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		chain := args[len(args)-1]
		if chain == "INPUT" {
			return []byte(inputRules), nil
		}
		return []byte("Another app is currently holding the xtables lock.\n"), errors.New("exit status 4")
	})
	if IsDirectWANPortOpen(8443) {
		t.Fatalf("expected IsDirectWANPortOpen to fail closed on transient _NDM_IP_PUBLIC error")
	}

	// 5. Fail-closed when iptables fails completely on Keenetic
	stubDirectWANSeams(t, nfDir, hookPath, func(env []string, name string, args ...string) ([]byte, error) {
		return nil, errors.New("iptables unavailable")
	})
	if IsDirectWANPortOpen(8443) {
		t.Fatalf("expected IsDirectWANPortOpen to fail closed when iptables query fails on Keenetic")
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
	svc.mu.Lock()
	svc.config.Enabled = true
	svc.mu.Unlock()
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

// Мутация: вернуть nil вместо ошибки в любой из веток → красный.
func TestSyncDirectWANFirewall_ReturnsErrors(t *testing.T) {
	nfDir := filepath.Join(t.TempDir(), "netfilter.d")
	hookPath := filepath.Join(nfDir, "68-awgm-tg-mtproto-wan.sh")
	if err := os.MkdirAll(nfDir, 0755); err != nil {
		t.Fatal(err)
	}
	const rule = "-A INPUT -p tcp -m tcp --dport 8443 -m comment --comment AWGM_TG_MTPROTO_WAN -j ACCEPT\n"
	var failOn string
	stubDirectWANSeams(t, nfDir, hookPath, func(_ []string, name string, args ...string) ([]byte, error) {
		call := name + " " + strings.Join(args, " ")
		if failOn != "" && strings.Contains(call, failOn) {
			return []byte("вывод утилиты"), errors.New("boom")
		}
		if strings.Contains(call, "-S INPUT") {
			return []byte(rule), nil
		}
		if strings.Contains(call, "-S _NDM_IP_PUBLIC") {
			return []byte("iptables: No chain/target/match by that name.\n"), errors.New("exit status 1")
		}
		return nil, nil
	})

	if err := SyncDirectWANFirewall(8443, true); err != nil {
		t.Fatalf("отсутствие _NDM_IP_PUBLIC — не ошибка: %v", err)
	}
	for _, tc := range []struct {
		failOn  string
		port    int
		enabled bool
	}{
		{"-S INPUT", 8443, true},
		{"-S INPUT", 0, false},
		{"-D INPUT", 9443, true},
		{"-D INPUT", 0, false},
		{"/bin/sh", 8443, true},
	} {
		failOn = tc.failOn
		err := SyncDirectWANFirewall(tc.port, tc.enabled)
		if err == nil || !strings.Contains(err.Error(), "вывод утилиты") {
			t.Fatalf("%+v: ждали ошибку с выводом утилиты, получили %v", tc, err)
		}
	}
	failOn = ""
	if err := SyncDirectWANFirewall(443, true); !errors.Is(err, ErrInvalidDirectPort) {
		t.Fatalf("недопустимый порт: %v", err)
	}
	// Хук — каталог с содержимым: ни записать, ни снять.
	if err := os.MkdirAll(filepath.Join(hookPath, "x"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		if err := SyncDirectWANFirewall(8443, enabled); err == nil {
			t.Fatalf("enabled=%v: сбой с файлом хука потерян", enabled)
		}
	}
}

type appLogSpy struct{ lines []string }

func (l *appLogSpy) AppLog(level logging.Level, group, subgroup, action, target, message string) {
	l.lines = append(l.lines, string(level)+" "+group+"/"+subgroup+" "+action+" "+target+" "+message)
}

// Мутации: не писать в журнал / не класть в статус / не снимать прошлую ошибку /
// вернуть ошибку файрвола из Start → красный.
func TestService_DirectWANFirewallErrorVisible(t *testing.T) {
	svc, _, _ := setupTestService(t)
	spy := &appLogSpy{}
	svc.SetAppLogger(spy)
	fwErr := errors.New("xtables lock")
	svc.directWANFirewall = func(int, bool) error { return fwErr }

	if err := svc.Start(); err != nil {
		t.Fatalf("сбой файрвола не должен валить Start: %v", err)
	}
	if got := svc.GetStatus().DirectWANError; got != "xtables lock" {
		t.Fatalf("статус: %q", got)
	}
	if len(spy.lines) != 1 || !strings.Contains(spy.lines[0], "server/firewall wan-firewall") || !strings.Contains(spy.lines[0], "xtables lock") {
		t.Fatalf("журнал: %q", spy.lines)
	}
	if err := svc.SyncDirectWANFirewall(); !errors.Is(err, fwErr) {
		t.Fatalf("SyncDirectWANFirewall: %v", err)
	}
	fwErr = nil
	if err := svc.SyncDirectWANFirewall(); err != nil || svc.GetStatus().DirectWANError != "" {
		t.Fatalf("после успеха ошибка осталась: %v / %q", err, svc.GetStatus().DirectWANError)
	}
}

// Порт на WAN открывает только заданный прямой адрес в сценарии с прямым
// подключением; без него прежнее правило снимается.
// Мутации: OpensDirectWAN без проверки DirectHost; синхронизация по
// IsDirectEnabled; UpdateConfigWithDirectHost игнорирует пустую строку → красный.
func TestService_DirectWANNeedsDirectHost(t *testing.T) {
	svc, _, _ := setupTestService(t)
	last := ""
	svc.directWANFirewall = func(port int, enabled bool) error {
		last = fmt.Sprintf("%d/%v", port, enabled)
		return nil
	}
	expect := func(what, want string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if last != want {
			t.Fatalf("%s: файрвол %q, ждали %q", what, last, want)
		}
	}

	expect("dual без адреса", "0/false", svc.Start())
	expect("direct_fake_tls без адреса", "0/false", svc.UpdateConfig(Config{Scenario: ScenarioDirectFakeTLS}))
	expect("direct_fake_tls с адресом", "8443/true", svc.UpdateConfig(Config{DirectHost: "203.0.113.10"}))
	expect("адрес не прислали", "8443/true", svc.UpdateConfig(Config{TlsDomain: "www.example.com"}))
	expect("пустой сценарий с адресом", "8443/true", svc.UpdateConfig(Config{Scenario: ScenarioDual}))
	expect("cdn_http с адресом", "0/false", svc.UpdateConfig(Config{Scenario: ScenarioCDNHTTP}))
	expect("снова dual", "8443/true", svc.UpdateConfig(Config{Scenario: ScenarioDual}))
	expect("адрес снят", "0/false", svc.UpdateConfigWithDirectHost(Config{DirectHost: "  "}))
	if got := svc.GetInternalConfig().DirectHost; got != "" {
		t.Fatalf("адрес не снят: %q", got)
	}
	svc.mu.Lock()
	svc.config.Scenario = ""
	svc.mu.Unlock()
	expect("пустой сценарий без адреса", "0/false", svc.Restart())
}

// Выключенный прокси на загрузке снимает оставшееся правило.
// Мутация: убрать синхронизацию из ветки !Enabled у StartConfigured → красный.
func TestStartConfigured_DisabledClosesDirectWAN(t *testing.T) {
	svc, _, _ := setupTestService(t)
	calls := 0
	svc.directWANFirewall = func(_ int, enabled bool) error {
		if enabled {
			t.Fatal("выключенный прокси открыл порт")
		}
		calls++
		return nil
	}
	if err := svc.StartConfigured(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("синхронизаций %d, ждали 1", calls)
	}
}
