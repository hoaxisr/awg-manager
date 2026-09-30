package adaptiverouting

import (
	"context"
	"strings"
	"sync"
	"testing"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

type mockCmdCall struct {
	bin  string
	args []string
}

func TestDatapath_EnsureSets(t *testing.T) {
	var mu sync.Mutex
	var calls []mockCmdCall

	mockRunner := func(ctx context.Context, bin string, args ...string) (*sysexec.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, mockCmdCall{bin: bin, args: args})
		return &sysexec.Result{ExitCode: 0}, nil
	}

	dp := NewDatapathController(mockRunner)
	err := dp.EnsureSets(context.Background())
	if err != nil {
		t.Fatalf("EnsureSets failed: %v", err)
	}

	var createCalls []mockCmdCall
	var addCalls []mockCmdCall
	for _, c := range calls {
		if len(c.args) > 0 && c.args[0] == "create" {
			createCalls = append(createCalls, c)
		} else if len(c.args) > 0 && c.args[0] == "add" {
			addCalls = append(addCalls, c)
		}
	}

	if len(createCalls) != 6 {
		t.Fatalf("expected 6 calls to ipset create, got %d", len(createCalls))
	}
	expectedSets := []string{SetOkTcp, SetOkUdp, SetTestTcp, SetTestUdp, SetOkNet, SetNever}
	for i, s := range expectedSets {
		if createCalls[i].args[1] != s {
			t.Errorf("call %d expected set %s, got %s", i, s, createCalls[i].args[1])
		}
	}

	if len(addCalls) != 0 {
		t.Errorf("expected 0 calls to ipset add in EnsureSets, got %d", len(addCalls))
	}
}

type mockPolicyResolver struct {
	marks map[string]string
	err   error
}

func (m *mockPolicyResolver) Get(ctx context.Context, policyName string) (string, error) {
	return m.marks[policyName], m.err
}

func TestDatapath_EnsureChain_PolicyFailsClosed(t *testing.T) {
	dp := NewDatapathController(func(ctx context.Context, bin string, args ...string) (*sysexec.Result, error) {
		return &sysexec.Result{ExitCode: 0}, nil
	})
	settings := DefaultSettings()
	settings.Source = SourceScope{Type: "policy", PolicyID: "missing"}

	if err := dp.EnsureChain(context.Background(), settings, []string{"br0"}, nil); err == nil {
		t.Fatal("expected missing policy resolver to fail closed")
	}

	dp.SetPolicyMarkResolver(&mockPolicyResolver{marks: map[string]string{}})
	if err := dp.EnsureChain(context.Background(), settings, []string{"br0"}, nil); err == nil {
		t.Fatal("expected empty policy mark to fail closed")
	}
}

func TestDatapath_EnsureChain_InterfacesUsesOnlySelectedInterfaces(t *testing.T) {
	var calls []mockCmdCall
	dp := NewDatapathController(func(ctx context.Context, bin string, args ...string) (*sysexec.Result, error) {
		calls = append(calls, mockCmdCall{bin: bin, args: args})
		return &sysexec.Result{ExitCode: 0}, nil
	})
	settings := DefaultSettings()
	settings.Source = SourceScope{Type: "interfaces", Interfaces: []string{"br1", "opkg0"}}

	if err := dp.EnsureChain(context.Background(), settings, []string{"br0"}, nil); err != nil {
		t.Fatalf("EnsureChain failed: %v", err)
	}
	var all string
	for _, call := range calls {
		all += strings.Join(call.args, " ") + ";"
	}
	if !strings.Contains(all, "PREROUTING -i br1 -j SUSANIN") || !strings.Contains(all, "PREROUTING -i opkg0 -j SUSANIN") {
		t.Fatalf("selected interface jumps missing: %s", all)
	}
	if strings.Contains(all, "-A PREROUTING -i br0 -j SUSANIN") {
		t.Fatalf("all-LAN interface leaked into selected source: %s", all)
	}
}

func TestDatapath_EnsureChain_Policy(t *testing.T) {
	var mu sync.Mutex
	var calls []mockCmdCall

	mockRunner := func(ctx context.Context, bin string, args ...string) (*sysexec.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, mockCmdCall{bin: bin, args: args})
		return &sysexec.Result{ExitCode: 0}, nil
	}

	dp := NewDatapathController(mockRunner)
	dp.SetPolicyMarkResolver(&mockPolicyResolver{
		marks: map[string]string{"Policy4": "0xffffab7"},
	})

	settings := DefaultSettings()
	settings.Source.Type = "policy"
	settings.Source.PolicyID = "Policy4"

	err := dp.EnsureChain(context.Background(), settings, []string{"br0"}, []string{"192.168.90.1"})
	if err != nil {
		t.Fatalf("EnsureChain failed: %v", err)
	}

	foundPolicyJump := false
	foundGuardRule := false
	for _, c := range calls {
		str := strings.Join(c.args, " ")
		if strings.Contains(str, "PREROUTING -m mark --mark 0xffffab7/0x0fffffff -j SUSANIN") {
			foundPolicyJump = true
		}
		if strings.Contains(str, "SUSANIN -m mark ! --mark 0xffffab7/0x0fffffff -j RETURN") {
			foundGuardRule = true
		}
	}
	if !foundPolicyJump {
		t.Errorf("expected PREROUTING jump with mark 0xffffab7/0x0fffffff, none found")
	}
	if !foundGuardRule {
		t.Errorf("expected SUSANIN guard rule with mark 0xffffab7/0x0fffffff, none found")
	}
}

func TestDatapath_EnsureChain_And_Rules(t *testing.T) {
	var mu sync.Mutex
	var calls []mockCmdCall

	mockRunner := func(ctx context.Context, bin string, args ...string) (*sysexec.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, mockCmdCall{bin: bin, args: args})
		return &sysexec.Result{ExitCode: 0}, nil
	}

	dp := NewDatapathController(mockRunner)
	settings := DefaultSettings()

	err := dp.EnsureChain(context.Background(), settings, []string{"br0"}, []string{"192.168.90.1"})
	if err != nil {
		t.Fatalf("EnsureChain failed: %v", err)
	}

	err = dp.EnsureRules(context.Background(), settings, "awgsus0")
	if err != nil {
		t.Fatalf("EnsureRules failed: %v", err)
	}

	// Verify jump was added for br0
	foundJump := false
	for _, c := range calls {
		str := strings.Join(c.args, " ")
		if strings.Contains(str, "PREROUTING -i br0 -j SUSANIN") {
			foundJump = true
			break
		}
	}
	if !foundJump {
		t.Errorf("expected PREROUTING jump for br0, none found")
	}

	// Verify ip rule add pref 95 and pref 96
	foundPriOk := false
	foundPriTest := false
	foundDefaultRoute := false

	for _, c := range calls {
		str := strings.Join(c.args, " ")
		if strings.Contains(str, "rule add pref 95 fwmark 0x20000000/0x30000000 table 105") {
			foundPriOk = true
		}
		if strings.Contains(str, "rule add pref 96 fwmark 0x10000000/0x30000000 table 105") {
			foundPriTest = true
		}
		if strings.Contains(str, "route replace default dev awgsus0 table 105") {
			foundDefaultRoute = true
		}
	}

	if !foundPriOk {
		t.Errorf("expected ip rule for OK (pref 95), not found")
	}
	if !foundPriTest {
		t.Errorf("expected ip rule for TEST (pref 96), not found")
	}
	if !foundDefaultRoute {
		t.Errorf("expected default route replace dev awgsus0 table 105, not found")
	}
}

func TestDatapath_FailOpen_And_Teardown(t *testing.T) {
	var mu sync.Mutex
	var calls []mockCmdCall

	mockRunner := func(ctx context.Context, bin string, args ...string) (*sysexec.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, mockCmdCall{bin: bin, args: args})
		return &sysexec.Result{ExitCode: 0}, nil
	}

	dp := NewDatapathController(mockRunner)
	settings := DefaultSettings()

	// 1. Fail open
	err := dp.SetFailOpen(context.Background(), true, settings)
	if err != nil {
		t.Fatalf("SetFailOpen(true) failed: %v", err)
	}

	foundRuleDel := false
	for _, c := range calls {
		str := strings.Join(c.args, " ")
		if strings.Contains(str, "rule del pref 95") {
			foundRuleDel = true
			break
		}
	}
	if !foundRuleDel {
		t.Errorf("expected rule del pref 95 during fail-open")
	}

	// 2. Restore fail-open
	err = dp.SetFailOpen(context.Background(), false, settings)
	if err != nil {
		t.Fatalf("SetFailOpen(false) failed: %v", err)
	}

	// 3. Teardown
	err = dp.Teardown(context.Background(), settings, []string{"br0"})
	if err != nil {
		t.Fatalf("Teardown failed: %v", err)
	}

	foundChainDelete := false
	for _, c := range calls {
		str := strings.Join(c.args, " ")
		if strings.Contains(str, "mangle -X SUSANIN") {
			foundChainDelete = true
			break
		}
	}
	if !foundChainDelete {
		t.Errorf("expected mangle -X SUSANIN during teardown")
	}
}

func TestDatapath_FailClosed_InstallsBlackholeAndRestoresRoute(t *testing.T) {
	var calls []mockCmdCall
	dp := NewDatapathController(func(ctx context.Context, bin string, args ...string) (*sysexec.Result, error) {
		calls = append(calls, mockCmdCall{bin: bin, args: args})
		return &sysexec.Result{ExitCode: 0}, nil
	})
	settings := DefaultSettings()

	if err := dp.SetFailClosed(context.Background(), true, settings); err != nil {
		t.Fatalf("SetFailClosed(true) failed: %v", err)
	}
	if err := dp.EnsureRules(context.Background(), settings, "awgsus0"); err != nil {
		t.Fatalf("EnsureRules after fail-closed failed: %v", err)
	}

	var all string
	for _, call := range calls {
		all += strings.Join(call.args, " ") + ";"
	}
	if !strings.Contains(all, "route replace blackhole default table 105") {
		t.Fatalf("kill-switch blackhole route missing: %s", all)
	}
	if !strings.Contains(all, "route replace default dev awgsus0 table 105") {
		t.Fatalf("healthy egress route was not restored: %s", all)
	}
}

func TestDatapath_GetStats(t *testing.T) {
	mockRunner := func(ctx context.Context, bin string, args ...string) (*sysexec.Result, error) {
		if len(args) == 2 && args[0] == "list" {
			setName := args[1]
			switch setName {
			case SetOkTcp:
				return &sysexec.Result{ExitCode: 0, Stdout: "Name: susanin_ok_tcp\nType: hash:ip\nRevision: 4\nHeader: family inet hashsize 1024 maxelem 65536\nSize in memory: 408\nReferences: 1\nNumber of entries: 42\n"}, nil
			case SetOkUdp:
				return &sysexec.Result{ExitCode: 0, Stdout: "Name: susanin_ok_udp\nType: hash:ip\nNumber of entries: 7\n"}, nil
			default:
				return &sysexec.Result{ExitCode: 0, Stdout: "Number of entries: 0\n"}, nil
			}
		}
		return &sysexec.Result{ExitCode: 0}, nil
	}

	dp := NewDatapathController(mockRunner)
	stats, err := dp.GetStats(context.Background())
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if stats.OkTcpCount != 42 {
		t.Errorf("expected OkTcpCount 42, got %d", stats.OkTcpCount)
	}
	if stats.OkUdpCount != 7 {
		t.Errorf("expected OkUdpCount 7, got %d", stats.OkUdpCount)
	}
}

func TestDatapath_GetStats_KeeneticListingWithoutEntryCount(t *testing.T) {
	runner := func(_ context.Context, _ string, args ...string) (*sysexec.Result, error) {
		if len(args) >= 2 && args[0] == "list" {
			if args[1] == SetOkTcp {
				return &sysexec.Result{ExitCode: 0, Stdout: "Name: susanin_ok_tcp\nType: hash:ip\nRevision: 4\nHeader: family inet hashsize 1024 maxelem 65536 timeout 0\nSize in memory: 480\nReferences: 4\nMembers:\n57.144.223.33 timeout 0\n157.240.205.1 timeout 0\n"}, nil
			}
			return &sysexec.Result{ExitCode: 0, Stdout: "Name: empty\nType: hash:ip\nMembers:\n"}, nil
		}
		return &sysexec.Result{ExitCode: 0}, nil
	}

	dp := NewDatapathController(runner)
	stats, err := dp.GetStats(context.Background())
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if stats.OkTcpCount != 2 {
		t.Fatalf("OkTcpCount = %d, want 2", stats.OkTcpCount)
	}
}
