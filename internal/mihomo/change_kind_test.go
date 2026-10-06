package mihomo

import (
	"context"
	"encoding/json"
	"testing"
)

func TestClassifyStoreSnapshotDiff_RuleOnly(t *testing.T) {
	pre := storeSnapshotDiffDTO{
		Version: 4,
		Rules: []ruleDiffDTO{
			{ID: "r1", Type: "DOMAIN", Payload: "a.com", Outbound: "DIRECT", Enabled: true},
			{ID: "r2", Type: "DOMAIN", Payload: "b.com", Outbound: "REJECT", Enabled: true},
		},
	}
	preBytes, _ := json.Marshal(pre)

	// 1. Reorder
	postReorder := pre
	postReorder.Rules = []ruleDiffDTO{
		{ID: "r2", Type: "DOMAIN", Payload: "b.com", Outbound: "REJECT", Enabled: true},
		{ID: "r1", Type: "DOMAIN", Payload: "a.com", Outbound: "DIRECT", Enabled: true},
	}
	postBytes, _ := json.Marshal(postReorder)

	kind, err := ClassifyStoreSnapshotDiff(preBytes, postBytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kind != ChangeKindRuleOnly {
		t.Fatalf("expected ChangeKindRuleOnly, got %v", kind)
	}

	// 2. Add rule
	postAdd := pre
	postAdd.Rules = append(postAdd.Rules, ruleDiffDTO{ID: "r3", Type: "MATCH", Outbound: "DIRECT", Enabled: true})
	postBytes, _ = json.Marshal(postAdd)
	kind, err = ClassifyStoreSnapshotDiff(preBytes, postBytes)
	if err != nil || kind != ChangeKindRuleOnly {
		t.Fatalf("expected ChangeKindRuleOnly for add, got %v (err: %v)", kind, err)
	}

	// 3. Update rule
	postUpdate := pre
	postUpdate.Rules[0].Outbound = "PROXY"
	postBytes, _ = json.Marshal(postUpdate)
	kind, err = ClassifyStoreSnapshotDiff(preBytes, postBytes)
	if err != nil || kind != ChangeKindRuleOnly {
		t.Fatalf("expected ChangeKindRuleOnly for update, got %v (err: %v)", kind, err)
	}

	// 4. Delete rule
	postDel := pre
	postDel.Rules = pre.Rules[:1]
	postBytes, _ = json.Marshal(postDel)
	kind, err = ClassifyStoreSnapshotDiff(preBytes, postBytes)
	if err != nil || kind != ChangeKindRuleOnly {
		t.Fatalf("expected ChangeKindRuleOnly for delete, got %v (err: %v)", kind, err)
	}
}

func TestClassifyStoreSnapshotDiff_OtherKinds(t *testing.T) {
	base := storeSnapshotDiffDTO{
		Version: 4,
		Proxies: []proxyDiffDTO{{ID: "p1", Name: "Proxy1"}},
		Rules:   []ruleDiffDTO{{ID: "r1", Type: "DOMAIN", Payload: "a.com", Outbound: "DIRECT"}},
	}
	baseBytes, _ := json.Marshal(base)

	// Proxy changed -> ProxyGraph
	changedProxy := base
	changedProxy.Proxies = []proxyDiffDTO{{ID: "p1", Name: "Proxy1Updated"}}
	cpBytes, _ := json.Marshal(changedProxy)
	kind, _ := ClassifyStoreSnapshotDiff(baseBytes, cpBytes)
	if kind != ChangeKindProxyGraph {
		t.Fatalf("expected ProxyGraph, got %v", kind)
	}

	// Subscription changed -> ProxyGraph
	changedSub := base
	changedSub.Subscriptions = []subscriptionDiffDTO{{ID: "s1", Name: "Sub1"}}
	csBytes, _ := json.Marshal(changedSub)
	kind, _ = ClassifyStoreSnapshotDiff(baseBytes, csBytes)
	if kind != ChangeKindProxyGraph {
		t.Fatalf("expected ProxyGraph, got %v", kind)
	}

	// Group changed -> ProxyGraph
	changedGrp := base
	changedGrp.Groups = []groupDiffDTO{{ID: "g1", Name: "Grp1"}}
	cgBytes, _ := json.Marshal(changedGrp)
	kind, _ = ClassifyStoreSnapshotDiff(baseBytes, cgBytes)
	if kind != ChangeKindProxyGraph {
		t.Fatalf("expected ProxyGraph, got %v", kind)
	}

	// RuleProvider changed -> RuleProviderContent
	changedRP := base
	changedRP.RuleProviders = []ruleProviderDiffDTO{{ID: "rp1", Name: "RP1"}}
	crpBytes, _ := json.Marshal(changedRP)
	kind, _ = ClassifyStoreSnapshotDiff(baseBytes, crpBytes)
	if kind != ChangeKindRuleProviderContent {
		t.Fatalf("expected RuleProviderContent, got %v", kind)
	}
}

type mockOperatorRunning struct {
	running bool
	pid     int
}

func (m mockOperatorRunning) IsRunning() (bool, int)            { return m.running, m.pid }
func (m mockOperatorRunning) StopAndWait(context.Context) error { return nil }
func (m mockOperatorRunning) Start() error                      { return nil }

func TestClassifyMutation(t *testing.T) {
	pre := storeSnapshotDiffDTO{
		Version: 4,
		Rules:   []ruleDiffDTO{{ID: "r1", Type: "DOMAIN", Payload: "a.com"}},
	}
	post := pre
	post.Rules = []ruleDiffDTO{{ID: "r1", Type: "DOMAIN", Payload: "b.com"}}
	preBytes, _ := json.Marshal(pre)
	postBytes, _ := json.Marshal(post)

	prevRec := &AppliedGenerationRecord{
		RuntimeMode:      RuntimeEnforced,
		AppliedListeners: []ListenerSpec{{Purpose: "mixed", Port: 7890, Address: "127.0.0.1", Network: "tcp"}},
	}
	compileRes := &CompileResult{
		Mode:              RuntimeEnforced,
		RequiredListeners: []ListenerSpec{{Purpose: "mixed", Port: 7890, Address: "127.0.0.1", Network: "tcp"}},
	}
	activeConfig := []byte("mode: rule\nlog-level: info\nrules:\n  - DOMAIN,a.com,DIRECT\n")
	candidateConfig := []byte("mode: rule\nlog-level: info\nrules:\n  - DOMAIN,b.com,DIRECT\n")

	// 1. Process running, same listeners, same bridges -> RuleOnly
	kind := ClassifyMutation(preBytes, postBytes, activeConfig, candidateConfig, prevRec, compileRes, mockOperatorRunning{running: true, pid: 1234}, "b1", "b1")
	if kind != ChangeKindRuleOnly {
		t.Fatalf("expected ChangeKindRuleOnly, got %v", kind)
	}

	// 2. Process not running -> Unknown (full restart needed)
	kind = ClassifyMutation(preBytes, postBytes, activeConfig, candidateConfig, prevRec, compileRes, mockOperatorRunning{running: false, pid: 0}, "b1", "b1")
	if kind != ChangeKindUnknown {
		t.Fatalf("expected ChangeKindUnknown when process not running, got %v", kind)
	}

	// 3. Listener changed -> ListenerTopology
	compileResChangedListener := &CompileResult{
		Mode:              RuntimeEnforced,
		RequiredListeners: []ListenerSpec{{Purpose: "mixed", Port: 7891, Address: "127.0.0.1", Network: "tcp"}},
	}
	kind = ClassifyMutation(preBytes, postBytes, activeConfig, candidateConfig, prevRec, compileResChangedListener, mockOperatorRunning{running: true, pid: 1234}, "b1", "b1")
	if kind != ChangeKindListenerTopology {
		t.Fatalf("expected ChangeKindListenerTopology, got %v", kind)
	}

	// 4. Bridge digest mismatch -> Unknown (bridge sync needed)
	kind = ClassifyMutation(preBytes, postBytes, activeConfig, candidateConfig, prevRec, compileRes, mockOperatorRunning{running: true, pid: 1234}, "b2", "b1")
	if kind != ChangeKindUnknown {
		t.Fatalf("expected ChangeKindUnknown on bridge digest mismatch, got %v", kind)
	}

	// 5. Engine mode changed -> EngineMode
	compileResMode := &CompileResult{
		Mode:              RuntimeOff,
		RequiredListeners: []ListenerSpec{{Purpose: "mixed", Port: 7890, Address: "127.0.0.1", Network: "tcp"}},
	}
	kind = ClassifyMutation(preBytes, postBytes, activeConfig, candidateConfig, prevRec, compileResMode, mockOperatorRunning{running: true, pid: 1234}, "b1", "b1")
	if kind != ChangeKindEngineMode {
		t.Fatalf("expected ChangeKindEngineMode, got %v", kind)
	}
}

func TestClassifyMutationRejectsNonRuleConfigChange(t *testing.T) {
	pre := storeSnapshotDiffDTO{Version: 4, Rules: []ruleDiffDTO{{ID: "r1", Type: "DOMAIN", Payload: "a.com"}}}
	post := pre
	post.Rules = []ruleDiffDTO{{ID: "r1", Type: "DOMAIN", Payload: "b.com"}}
	preBytes, _ := json.Marshal(pre)
	postBytes, _ := json.Marshal(post)
	listeners := []ListenerSpec{{Purpose: "mixed", Port: 7890, Address: "127.0.0.1", Network: "tcp"}}
	prevRec := &AppliedGenerationRecord{RuntimeMode: RuntimeEnforced, AppliedListeners: listeners}
	compileRes := &CompileResult{Mode: RuntimeEnforced, RequiredListeners: listeners}

	kind := ClassifyMutation(preBytes, postBytes,
		[]byte("mode: rule\nlog-level: info\nrules: [DIRECT]\n"),
		[]byte("mode: rule\nlog-level: debug\nrules: [REJECT]\n"),
		prevRec, compileRes, mockOperatorRunning{running: true, pid: 1234}, "b1", "b1")
	if kind != ChangeKindUnknown {
		t.Fatalf("expected non-rule config change to force full restart, got %v", kind)
	}
}

func TestListenersEqualOrderIndependentAndSensitiveToTopology(t *testing.T) {
	base := []ListenerSpec{
		{Purpose: "mixed", Address: "127.0.0.1", Port: 7890, Network: "tcp", Family: "ipv4"},
		{Purpose: "tproxy", Address: "::1", Port: 7891, Network: "udp", Family: "ipv6"},
	}
	reversed := []ListenerSpec{base[1], base[0]}
	if !ListenersEqual(base, reversed) {
		t.Fatal("same listener set in a different order must compare equal")
	}

	for name, mutate := range map[string]func(*ListenerSpec){
		"address": func(l *ListenerSpec) { l.Address = "0.0.0.0" },
		"port":    func(l *ListenerSpec) { l.Port++ },
		"network": func(l *ListenerSpec) { l.Network = "udp" },
		"family":  func(l *ListenerSpec) { l.Family = "ipv6" },
		"purpose": func(l *ListenerSpec) { l.Purpose = "redir" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := append([]ListenerSpec(nil), base...)
			mutate(&changed[0])
			if ListenersEqual(base, changed) {
				t.Fatalf("listener %s change must be detected", name)
			}
		})
	}
}
