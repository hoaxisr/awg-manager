package firc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeIptablesState struct {
	mu           sync.Mutex
	filterChains map[string][]string
	natChains    map[string][]string
	mangleChain  map[string][]string
	failCmdSub   string
	cmdLog       []string
}

func newFakeIptablesState() *fakeIptablesState {
	return &fakeIptablesState{
		filterChains: make(map[string][]string),
		natChains:    make(map[string][]string),
		mangleChain:  make(map[string][]string),
	}
}

func (st *fakeIptablesState) runner(_ context.Context, args ...string) ([]byte, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	if len(args) > 0 && args[0] == "-w" {
		args = args[1:]
	}
	cmdLine := strings.Join(args, " ")
	st.cmdLog = append(st.cmdLog, cmdLine)
	if st.failCmdSub != "" && strings.Contains(cmdLine, st.failCmdSub) {
		// Consume one-shot failure injection
		st.failCmdSub = ""
		return []byte("xtables lock / rule error"), errors.New("exit status 4")
	}

	table := "filter"
	idx := 0
	if len(args) >= 2 && args[0] == "-t" {
		table = args[1]
		idx = 2
	}
	if idx >= len(args) {
		return nil, nil
	}

	chains := st.filterChains
	if chains == nil {
		chains = make(map[string][]string)
		st.filterChains = chains
	}
	switch table {
	case "nat":
		chains = st.natChains
	case "mangle":
		chains = st.mangleChain
	}

	op := args[idx]
	switch op {
	case "-N":
		chain := args[idx+1]
		if _, ok := chains[chain]; !ok {
			chains[chain] = nil
		}
		return nil, nil
	case "-F":
		chain := args[idx+1]
		chains[chain] = nil
		return nil, nil
	case "-X":
		chain := args[idx+1]
		delete(chains, chain)
		return nil, nil
	case "-S":
		if idx+1 >= len(args) {
			keys := make([]string, 0, len(chains))
			for k := range chains {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var sb strings.Builder
			for _, ch := range keys {
				sb.WriteString("-N " + ch + "\n")
				for _, r := range chains[ch] {
					sb.WriteString("-A " + ch + " " + r + "\n")
				}
			}
			return []byte(sb.String()), nil
		}
		chain := args[idx+1]
		rules, ok := chains[chain]
		if !ok {
			return []byte("No chain/target/match by that name"), errors.New("exit status 1")
		}
		var sb strings.Builder
		sb.WriteString("-N " + chain + "\n")
		for _, r := range rules {
			sb.WriteString("-A " + chain + " " + r + "\n")
		}
		return []byte(sb.String()), nil
	case "-C":
		chain := args[idx+1]
		spec := strings.Join(args[idx+2:], " ")
		for _, r := range chains[chain] {
			if r == spec {
				return nil, nil
			}
		}
		return nil, errors.New("exit status 1")
	case "-A":
		chain := args[idx+1]
		spec := strings.Join(args[idx+2:], " ")
		chains[chain] = append(chains[chain], spec)
		return nil, nil
	case "-I":
		chain := args[idx+1]
		rest := args[idx+2:]
		pos := 1
		if len(rest) > 0 {
			if p, err := strconv.Atoi(rest[0]); err == nil && p >= 1 {
				pos = p
				rest = rest[1:]
			}
		}
		spec := strings.Join(rest, " ")
		cur := chains[chain]
		ins := pos - 1
		if ins > len(cur) {
			ins = len(cur)
		}
		next := make([]string, 0, len(cur)+1)
		next = append(next, cur[:ins]...)
		next = append(next, spec)
		next = append(next, cur[ins:]...)
		chains[chain] = next
		return nil, nil
	case "-D":
		chain := args[idx+1]
		if len(args) == idx+3 {
			if ruleNum, err := strconv.Atoi(args[idx+2]); err == nil {
				rules := chains[chain]
				if ruleNum >= 1 && ruleNum <= len(rules) {
					pos := ruleNum - 1
					chains[chain] = append(append([]string(nil), rules[:pos]...), rules[pos+1:]...)
					return nil, nil
				}
				return []byte("Index of deletion too big."), errors.New("exit status 1")
			}
		}
		spec := strings.Join(args[idx+2:], " ")
		rules := chains[chain]
		for i, r := range rules {
			if r == spec {
				chains[chain] = append(append([]string(nil), rules[:i]...), rules[i+1:]...)
				return nil, nil
			}
		}
		return nil, errors.New("exit status 1")
	default:
		return nil, nil
	}
}

func TestSyncNatMirrorChainFaultInjectionAndRepair(t *testing.T) {
	st := newFakeIptablesState()
	SetExtIptablesRunner(st.runner)
	defer SetExtIptablesRunner(nil)
	InvalidateOutputRoutingCache()

	rule1 := "-d 198.18.0.10/32 -p tcp -j DNAT --to-destination 142.250.74.10:443"
	rule2 := "-d 198.18.0.11/32 -p tcp -j DNAT --to-destination 142.250.74.11:443"
	st.natChains["FIRC_DNAT"] = []string{rule1, rule2}
	st.natChains["FEXT_DNAT"] = []string{rule1} // rule1 already exists so chain is non-empty

	// 1. Inject failure when appending rule2
	st.mu.Lock()
	st.failCmdSub = "-t nat -A FEXT_DNAT " + rule2
	st.mu.Unlock()

	_, err := syncNatMirrorChain(context.Background(), "FEXT_DNAT", "FIRC_DNAT")
	if err == nil {
		t.Fatal("expected syncNatMirrorChain to return error when rule insertion fails")
	}

	mirrorNetfilterMu.Lock()
	_, cachedAfterFail := lastNatChainSigs["FEXT_DNAT"]
	mirrorNetfilterMu.Unlock()
	if cachedAfterFail {
		t.Fatal("failed NAT mirror sync must NOT cache signature in lastNatChainSigs")
	}

	// 2. Next reconcile without any change to FIRC_DNAT must NOT short-circuit and must repair rule2
	if hasRules, err := syncNatMirrorChain(context.Background(), "FEXT_DNAT", "FIRC_DNAT"); err != nil || !hasRules {
		t.Fatalf("expected repair syncNatMirrorChain to succeed, got hasRules=%v err=%v", hasRules, err)
	}

	st.mu.Lock()
	gotRules := append([]string(nil), st.natChains["FEXT_DNAT"]...)
	st.mu.Unlock()
	if len(gotRules) != 2 {
		t.Fatalf("expected 2 rules in FEXT_DNAT after repair, got %v", gotRules)
	}

	// 3. Remove rule2 from FIRC_DNAT and inject failure on deleting rule2 from FEXT_DNAT
	st.mu.Lock()
	st.natChains["FIRC_DNAT"] = []string{rule1}
	st.failCmdSub = "-t nat -D FEXT_DNAT " + rule2
	st.mu.Unlock()

	_, err = syncNatMirrorChain(context.Background(), "FEXT_DNAT", "FIRC_DNAT")
	if err == nil {
		t.Fatal("expected syncNatMirrorChain to return error when rule deletion fails")
	}

	// 4. Subsequent reconcile must retry deleting rule2 and succeed
	if hasRules, err := syncNatMirrorChain(context.Background(), "FEXT_DNAT", "FIRC_DNAT"); err != nil || !hasRules {
		t.Fatalf("expected repair of failed deletion to succeed, got hasRules=%v err=%v", hasRules, err)
	}
	st.mu.Lock()
	gotAfterDel := append([]string(nil), st.natChains["FEXT_DNAT"]...)
	st.mu.Unlock()
	if len(gotAfterDel) != 1 || gotAfterDel[0] != rule1 {
		t.Fatalf("expected only rule1 in FEXT_DNAT after deletion repair, got %v", gotAfterDel)
	}
}

func TestSyncGroupMirrorChainFaultInjectionAndRepair(t *testing.T) {
	st := newFakeIptablesState()
	SetExtIptablesRunner(st.runner)
	defer SetExtIptablesRunner(nil)
	InvalidateOutputRoutingCache()

	srcRule1 := "-A FIRC_GRP_1 -m set --match-set firc_g1 dst -j MARK --set-xmark 0x100/0xff00"
	srcRule2 := "-A FIRC_GRP_1 -d 8.8.8.8/32 -j MARK --set-xmark 0x100/0xff00"
	rawRules := "-N FIRC_GRP_1\n" + srcRule1 + "\n" + srcRule2 + "\n"
	dstRule1 := "-m set --match-set firc_g1 dst " + fircNewFlowMatch + " -j MARK --set-xmark 0x100/0xff00"
	dstRule2 := "-d 8.8.8.8/32 " + fircNewFlowMatch + " -j MARK --set-xmark 0x100/0xff00"

	st.mangleChain["FEXT_GRP_1"] = []string{dstRule1}
	st.mu.Lock()
	st.failCmdSub = "-t mangle -A FEXT_GRP_1 " + dstRule2
	st.mu.Unlock()

	if err := syncGroupMirrorChain(context.Background(), "FEXT_GRP_1", "FIRC_GRP_1", "0x100/0xff00", rawRules); err == nil {
		t.Fatal("expected syncGroupMirrorChain to fail on injected -A error")
	}

	// Next call must repair FEXT_GRP_1 rather than short-circuiting
	if err := syncGroupMirrorChain(context.Background(), "FEXT_GRP_1", "FIRC_GRP_1", "0x100/0xff00", rawRules); err != nil {
		t.Fatalf("expected syncGroupMirrorChain repair to succeed, got: %v", err)
	}
	st.mu.Lock()
	got := append([]string(nil), st.mangleChain["FEXT_GRP_1"]...)
	st.mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected 2 rules in FEXT_GRP_1 after repair, got %v", got)
	}
}

func TestConcurrentOutputReconcileAndFakeIPSyncSerialization(t *testing.T) {
	st := newFakeIptablesState()
	SetExtIptablesRunner(st.runner)
	defer SetExtIptablesRunner(nil)
	defer SetOnMirrorInvalidate(nil)

	var invalidations atomic.Int64
	SetOnMirrorInvalidate(func() {
		invalidations.Add(1)
	})
	InvalidateOutputRoutingCache()
	if invalidations.Load() == 0 {
		t.Fatal("expected InvalidateOutputRoutingCache to invoke onMirrorInvalidate callback")
	}

	st.natChains["FIRC_DNAT"] = []string{
		"-d 198.18.0.50/32 -p tcp -j DNAT --to-destination 142.250.74.50:443",
		"-d 198.18.0.51/32 -p tcp -j DNAT --to-destination 142.250.74.51:443",
	}
	st.natChains["FIRC_REDIR"] = nil
	st.mangleChain["FIRC_PREROUTING"] = nil
	st.mangleChain["OUTPUT"] = nil
	st.natChains["OUTPUT"] = nil

	extCfg := ExtConfig{
		ServerRouting: ServerRoutingConfig{
			EnabledServers:   []string{"vless_xhttp_server"},
			VlessXhttpServer: true,
		},
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 32)

	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			mirrorNetfilterMu.Lock()
			reconcileOutputRouting(context.Background(), extCfg, nil)
			mirrorNetfilterMu.Unlock()
		}()
		go func(idx int) {
			defer wg.Done()
			ip := "198.18.0.50"
			if idx%2 == 1 {
				ip = "198.18.0.51"
			}
			ruleSpec, realTarget, _, _, err := SyncFakeIPOutputDnat(context.Background(), ip, false)
			if err != nil {
				errCh <- fmt.Errorf("SyncFakeIPOutputDnat(%s): %w", ip, err)
				return
			}
			if ruleSpec == "" || realTarget == "" {
				errCh <- fmt.Errorf("expected non-empty ruleSpec/realTarget for %s, got ruleSpec=%q realTarget=%q", ip, ruleSpec, realTarget)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestReviewOutputFlushFailureMustNotCommit(t *testing.T) {
	st := newFakeIptablesState()
	SetExtIptablesRunner(st.runner)
	defer SetExtIptablesRunner(nil)
	InvalidateOutputRoutingCache()
	defer InvalidateOutputRoutingCache()

	staleRule := "-d 203.0.113.1/32 -j MARK --set-xmark 0x40010000/0x40ff0000"
	st.mangleChain["FEXT_OUT"] = []string{staleRule}
	st.failCmdSub = "-t mangle -F FEXT_OUT"

	extCfg := ExtConfig{
		ServerRouting: ServerRoutingConfig{
			EnabledServers:   []string{"vless_xhttp_server"},
			VlessXhttpServer: true,
		},
	}

	mirrorNetfilterMu.Lock()
	lastFextOutSig = "previous configuration"
	reconcileOutputRouting(context.Background(), extCfg, nil)
	sig := lastFextOutSig
	mirrorNetfilterMu.Unlock()
	if sig != "" {
		t.Fatalf("failed FEXT_OUT flush was committed as successful: signature=%q rules=%v", sig, st.mangleChain["FEXT_OUT"])
	}

	// Second reconcile (with lastFextOutSig == "") must still flush the stale rule and verify exact order.
	mirrorNetfilterMu.Lock()
	reconcileOutputRouting(context.Background(), extCfg, nil)
	sigAfterRepair := lastFextOutSig
	mirrorNetfilterMu.Unlock()
	if sigAfterRepair == "" {
		t.Fatalf("expected second reconcileOutputRouting to succeed and commit signature, got empty sig; rules=%v", st.mangleChain["FEXT_OUT"])
	}

	wantOrder := []string{
		fircRestoreMarkRule,
		fircSaveMarkRule,
	}
	st.mu.Lock()
	gotOrder := append([]string(nil), st.mangleChain["FEXT_OUT"]...)
	st.mu.Unlock()
	if !equalStringSlices(gotOrder, wantOrder) {
		t.Fatalf("FEXT_OUT exact order mismatch after repair:\ngot:  %v\nwant: %v", gotOrder, wantOrder)
	}

	// Corrupt rule order in-place while keeping lastFextOutSig unchanged; next reconcile must detect order mismatch and fix it.
	st.mu.Lock()
	st.mangleChain["FEXT_OUT"] = []string{wantOrder[1], wantOrder[0]}
	st.mu.Unlock()

	mirrorNetfilterMu.Lock()
	reconcileOutputRouting(context.Background(), extCfg, nil)
	mirrorNetfilterMu.Unlock()

	st.mu.Lock()
	gotFixedOrder := append([]string(nil), st.mangleChain["FEXT_OUT"]...)
	st.mu.Unlock()
	if !equalStringSlices(gotFixedOrder, wantOrder) {
		t.Fatalf("FEXT_OUT order corruption was not repaired:\ngot:  %v\nwant: %v", gotFixedOrder, wantOrder)
	}
}

func TestReviewNoutFlushAndLegacyJumpFailureMustNotCommit(t *testing.T) {
	st := newFakeIptablesState()
	SetExtIptablesRunner(st.runner)
	defer SetExtIptablesRunner(nil)
	InvalidateOutputRoutingCache()
	defer InvalidateOutputRoutingCache()

	st.natChains["FIRC_REDIR"] = []string{"-p tcp -m set --match-set firc_g1 dst -j REDIRECT --to-ports 1080"}
	st.natChains["FIRC_DNAT"] = []string{"-d 198.18.0.10/32 -p tcp -j DNAT --to-destination 142.250.74.10:443"}
	st.natChains["FEXT_NOUT"] = []string{"-d 198.18.0.0/15 -j FIRC_DNAT"}

	extCfg := ExtConfig{
		ServerRouting: ServerRoutingConfig{
			EnabledServers:   []string{"vless_xhttp_server"},
			VlessXhttpServer: true,
		},
	}

	// 1. Inject failure on deleting legacy FIRC_DNAT jump from FEXT_NOUT
	st.mu.Lock()
	st.failCmdSub = "-t nat -D FEXT_NOUT -d 198.18.0.0/15 -j FIRC_DNAT"
	st.mu.Unlock()

	mirrorNetfilterMu.Lock()
	reconcileOutputRouting(context.Background(), extCfg, nil)
	sig1 := lastFextNoutSig
	mirrorNetfilterMu.Unlock()
	if sig1 != "" {
		t.Fatalf("failed FEXT_NOUT legacy jump deletion was committed as successful: sig=%q rules=%v", sig1, st.natChains["FEXT_NOUT"])
	}

	// 2. Inject failure on flushing FEXT_NOUT when stale rule is present
	st.mu.Lock()
	st.natChains["FEXT_NOUT"] = []string{"-d 198.18.0.99/32 -j ACCEPT"}
	st.failCmdSub = "-t nat -F FEXT_NOUT"
	st.mu.Unlock()

	mirrorNetfilterMu.Lock()
	lastFextNoutSig = "previous-nout-sig"
	reconcileOutputRouting(context.Background(), extCfg, nil)
	sig2 := lastFextNoutSig
	mirrorNetfilterMu.Unlock()
	if sig2 != "" {
		t.Fatalf("failed FEXT_NOUT flush was committed as successful: sig=%q rules=%v", sig2, st.natChains["FEXT_NOUT"])
	}

	// 3. Subsequent reconcile (with lastFextNoutSig == "") must repair FEXT_NOUT in exact order
	mirrorNetfilterMu.Lock()
	reconcileOutputRouting(context.Background(), extCfg, nil)
	sig3 := lastFextNoutSig
	mirrorNetfilterMu.Unlock()
	if sig3 == "" {
		t.Fatalf("expected FEXT_NOUT repair reconcile to succeed, got empty sig; rules=%v", st.natChains["FEXT_NOUT"])
	}

	wantNoutOrder := []string{
		"-p tcp -m mark --mark 0x40000000/0x40000000 -m addrtype ! --dst-type LOCAL -j FEXT_REDIR",
		"-d 198.18.0.0/15 -j FEXT_DNAT",
	}
	st.mu.Lock()
	gotNout := append([]string(nil), st.natChains["FEXT_NOUT"]...)
	st.mu.Unlock()
	if !equalStringSlices(gotNout, wantNoutOrder) {
		t.Fatalf("FEXT_NOUT exact order mismatch after repair:\ngot:  %v\nwant: %v", gotNout, wantNoutOrder)
	}
}

type fakeIpsetState struct {
	mu           sync.Mutex
	sets         map[string]map[string]bool
	ipt          *fakeIptablesState
	failOp       string
	sawEmptySwap bool
	swapCount    int
	ops          []string
}

func newFakeIpsetState(ipt *fakeIptablesState) *fakeIpsetState {
	return &fakeIpsetState{
		sets: make(map[string]map[string]bool),
		ipt:  ipt,
	}
}

func (s *fakeIpsetState) resetOps() {
	s.mu.Lock()
	s.ops = nil
	s.mu.Unlock()
}

func (s *fakeIpsetState) opsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ops...)
}

func (s *fakeIpsetState) snapshot(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.sets[name]
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (s *fakeIpsetState) hasSet(name string) bool {
	s.mu.Lock()
	_, ok := s.sets[name]
	s.mu.Unlock()
	return ok
}

func (s *fakeIpsetState) iptReferencesSetLocked(setName string) bool {
	if s.ipt == nil {
		return false
	}
	s.ipt.mu.Lock()
	defer s.ipt.mu.Unlock()
	needle := "--match-set " + setName + " "
	for _, table := range []map[string][]string{s.ipt.mangleChain, s.ipt.natChains, s.ipt.filterChains} {
		for _, rules := range table {
			for _, r := range rules {
				if strings.Contains(r, needle) {
					return true
				}
			}
		}
	}
	return false
}

func (s *fakeIpsetState) runner(_ context.Context, stdin string, args ...string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(args) == 0 {
		return nil, errors.New("missing ipset subcommand")
	}
	cmd := args[0]
	if s.failOp != "" && s.failOp == cmd {
		s.failOp = ""
		return []byte("mock ipset error on " + cmd), errors.New("exit status 1")
	}

	switch cmd {
	case "list":
		if len(args) < 2 || args[1] == "-n" {
			names := make([]string, 0, len(s.sets))
			for name := range s.sets {
				names = append(names, name)
			}
			sort.Strings(names)
			if len(names) == 0 {
				return []byte(""), nil
			}
			return []byte(strings.Join(names, "\n") + "\n"), nil
		}
		setName := args[1]
		if _, ok := s.sets[setName]; !ok {
			return []byte("ipset v7.15: The set with the given name does not exist"), errors.New("exit status 1")
		}
		return []byte(setName + "\n"), nil
	case "create":
		s.ops = append(s.ops, strings.Join(args[:min(2, len(args))], " "))
		setName := args[1]
		if _, ok := s.sets[setName]; !ok {
			s.sets[setName] = make(map[string]bool)
		}
		return nil, nil
	case "flush":
		s.ops = append(s.ops, strings.Join(args[:min(2, len(args))], " "))
		setName := args[1]
		m, ok := s.sets[setName]
		if !ok {
			return []byte("ipset v7.15: The set with the given name does not exist"), errors.New("exit status 1")
		}
		if setName == FextCloudIpsetName && len(m) > 0 {
			s.sawEmptySwap = true
		}
		s.sets[setName] = make(map[string]bool)
		return nil, nil
	case "restore":
		s.ops = append(s.ops, "restore")
		for _, line := range strings.Split(stdin, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch fields[0] {
			case "create":
				if _, ok := s.sets[fields[1]]; !ok {
					s.sets[fields[1]] = make(map[string]bool)
				}
			case "flush":
				if fields[1] == FextCloudIpsetName && len(s.sets[fields[1]]) > 0 {
					s.sawEmptySwap = true
				}
				s.sets[fields[1]] = make(map[string]bool)
			case "add":
				if _, ok := s.sets[fields[1]]; !ok {
					return []byte("set does not exist"), errors.New("exit status 1")
				}
				if _, ok := ValidateCloudIPv4Entry(fields[2]); !ok {
					return []byte("invalid ipv4 entry: " + fields[2]), errors.New("exit status 1")
				}
				s.sets[fields[1]][fields[2]] = true
			}
		}
		return nil, nil
	case "swap":
		from, to := args[1], args[2]
		s.ops = append(s.ops, "swap "+from+" "+to)
		mFrom, ok1 := s.sets[from]
		mTo, ok2 := s.sets[to]
		if !ok1 || !ok2 {
			return []byte("ipset v7.15: The set with the given name does not exist"), errors.New("exit status 1")
		}
		if to == FextCloudIpsetName && len(mFrom) == 0 {
			s.sawEmptySwap = true
		}
		s.sets[to] = mFrom
		s.sets[from] = mTo
		s.swapCount++
		return nil, nil
	case "destroy":
		setName := args[1]
		s.ops = append(s.ops, "destroy "+setName)
		if _, ok := s.sets[setName]; !ok {
			return []byte("ipset v7.15: The set with the given name does not exist"), errors.New("exit status 1")
		}
		if s.iptReferencesSetLocked(setName) {
			return []byte("ipset v7.15: Set cannot be destroyed: it is in use by a kernel component"), errors.New("exit status 1")
		}
		delete(s.sets, setName)
		return nil, nil
	default:
		return nil, nil
	}
}

func startMockRunningFircServer(t *testing.T, op *Operator, groupsMu *sync.Mutex, groups *[]Group, iptablesSt *fakeIptablesState) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/system" || r.URL.Path == "/api/v1/system/netfilter"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dirty":false}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/groups"):
			groupsMu.Lock()
			snap := cloneGroups(*groups)
			groupsMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": snap})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/groups":
			var req struct {
				Groups []Group `json:"groups"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			groupsMu.Lock()
			*groups = cloneGroups(req.Groups)
			groupsMu.Unlock()
			if iptablesSt != nil {
				iptablesSt.mu.Lock()
				hasKD := false
				for _, g := range req.Groups {
					if g.ID == KeenDNSGroupID && g.Enable {
						hasKD = true
						iptablesSt.mangleChain["FIRC_"+KeenDNSGroupID] = []string{
							"-m set --match-set firc_" + KeenDNSGroupID + " dst -j FIRC_" + KeenDNSGroupID + "D",
							"-d 185.162.93.0/24 -j FIRC_" + KeenDNSGroupID + "D",
						}
						iptablesSt.mangleChain["FIRC_"+KeenDNSGroupID+"D"] = []string{
							"-j MARK --set-xmark 0x40010000/0x40ff0000",
						}
					} else if g.Enable && g.ID != "" {
						iptablesSt.mangleChain["FIRC_"+g.ID] = []string{
							"-m set --match-set firc_" + g.ID + " dst -j FIRC_" + g.ID + "D",
						}
						iptablesSt.mangleChain["FIRC_"+g.ID+"D"] = []string{
							"-j MARK --set-xmark 0x40020000/0x40ff0000",
						}
					}
				}
				if !hasKD {
					delete(iptablesSt.mangleChain, "FIRC_"+KeenDNSGroupID)
					delete(iptablesSt.mangleChain, "FIRC_"+KeenDNSGroupID+"D")
				}
				iptablesSt.mu.Unlock()
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": req.Groups})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	return srv
}

func TestKeenDNSCloudSync_CreationUpdateAndStrictValidation(t *testing.T) {
	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	defer SetExtIptablesRunner(nil)
	SetExtIpsetRunner(ipsetSt.runner)
	defer SetExtIpsetRunner(nil)
	InvalidateOutputRoutingCache()
	defer InvalidateOutputRoutingCache()
	defer SetExtDNSLookupIPv4(nil)

	SetExtDNSLookupIPv4(func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "broker.keenetic.cloud":
			return []netip.Addr{
				netip.MustParseAddr("95.213.212.88"),
				netip.MustParseAddr("2001:db8::1"),    // IPv6 must be rejected
				netip.MustParseAddr("198.18.0.10"),    // FakeIP must be rejected
				netip.MustParseAddr("127.0.0.1"),      // loopback must be rejected
				netip.MustParseAddr("192.168.1.1"),    // RFC1918 must be rejected
				netip.MustParseAddr("::ffff:8.8.4.4"), // v4-mapped IPv6 rejected by !Is4In6
			}, nil
		case "relay1.keenetic.net":
			return []netip.Addr{netip.MustParseAddr("185.162.93.99")}, nil
		default:
			return []netip.Addr{netip.MustParseAddr("82.202.218.55")}, nil
		}
	})

	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})
	op.SetKeenCloudProvider(func(_ context.Context) (KeenCloudRelaySnapshot, error) {
		return KeenCloudRelaySnapshot{
			Enabled: true,
			RelayIPs: []string{
				"91.92.241.12",
				"198.18.1.5",        // FakeIP
				"100.64.1.2",        // CGNAT
				"10.0.0.5",          // RFC1918
				"0.0.0.0/0",         // /0 rejected
				"8.8.8.8; rm -rf /", // shell injection rejected
				"::1",               // IPv6 rejected
			},
			RelayDomains: []string{"relay1.keenetic.net", "bad host;id"},
		}, nil
	})

	var groupsMu sync.Mutex
	groups := []Group{{ID: "11112222", Name: "Default", Interface: []string{"tunvless0"}, Enable: true}}
	srv := startMockRunningFircServer(t, op, &groupsMu, &groups, iptSt)
	defer srv.Close()

	extCfg := ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{
			Enabled:    true,
			Interfaces: []string{"tunvless0"},
		},
	}
	res, err := op.ApplyExtConfig(context.Background(), extCfg)
	if err != nil {
		t.Fatalf("ApplyExtConfig failed: %v", err)
	}
	if res.KeenDNSCloud.LastSyncError != "" || res.KeenDNSCloud.LastSyncAt == 0 {
		t.Fatalf("expected clean LastSyncError and non-zero LastSyncAt, got err=%q at=%d", res.KeenDNSCloud.LastSyncError, res.KeenDNSCloud.LastSyncAt)
	}

	if ipsetSt.sawEmptySwap {
		t.Fatal("observed empty FEXT_CLOUD state during atomic swap")
	}
	if !ipsetSt.hasSet(FextCloudIpsetName) || ipsetSt.hasSet(fextCloudStagingIpsetName) {
		t.Fatalf("expected FEXT_CLOUD present and FEXT_CLOUD_TMP destroyed, hasCloud=%v hasTmp=%v",
			ipsetSt.hasSet(FextCloudIpsetName), ipsetSt.hasSet(fextCloudStagingIpsetName))
	}

	gotSet := ipsetSt.snapshot(FextCloudIpsetName)
	for _, want := range []string{"95.213.212.88", "91.92.241.12", "185.162.93.99", "82.202.218.55", "185.162.93.0/24", "31.135.14.0/24"} {
		if !slices.Contains(gotSet, want) {
			t.Fatalf("FEXT_CLOUD missing expected entry %q (got %v)", want, gotSet)
		}
	}
	for _, forbidden := range []string{"2001:db8::1", "198.18.0.10", "198.18.1.5", "198.51.100.11", "127.0.0.1", "192.168.1.1", "100.64.1.2", "10.0.0.5", "8.8.4.4", "0.0.0.0/0", "::1"} {
		if slices.Contains(gotSet, forbidden) {
			t.Fatalf("FEXT_CLOUD must reject forbidden entry %q (got %v)", forbidden, gotSet)
		}
	}
	if _, ok := ValidateCloudIPv4Entry("198.51.100.11"); ok {
		t.Fatal("ValidateCloudIPv4Entry must reject 198.51.100.0/24 local KeenDNS portal range")
	}

	// Verify no lo:53 -> fircd:3553 DNS redirect loop was created.
	iptSt.mu.Lock()
	for _, table := range []map[string][]string{iptSt.natChains, iptSt.mangleChain} {
		for chain, rules := range table {
			for _, r := range rules {
				if strings.Contains(r, "lo") && strings.Contains(r, "3553") {
					iptSt.mu.Unlock()
					t.Fatalf("unexpected lo:53 -> 3553 redirect rule in chain %s: %s", chain, r)
				}
			}
		}
	}
	iptSt.mu.Unlock()
}

func TestKeenDNSCloudSync_ErrorsConfirmedEmptyAndStaleTTL(t *testing.T) {
	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	defer SetExtIptablesRunner(nil)
	SetExtIpsetRunner(ipsetSt.runner)
	defer SetExtIpsetRunner(nil)
	InvalidateOutputRoutingCache()
	defer InvalidateOutputRoutingCache()
	defer SetExtDNSLookupIPv4(nil)
	defer setExtCloudNow(nil)

	now := time.Unix(1_700_000_000, 0)
	setExtCloudNow(func() time.Time { return now })

	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})
	relayIP := "91.92.241.50"
	rciErr := error(nil)
	rciEnabled := true
	op.SetKeenCloudProvider(func(_ context.Context) (KeenCloudRelaySnapshot, error) {
		if rciErr != nil {
			return KeenCloudRelaySnapshot{}, rciErr
		}
		if !rciEnabled {
			return KeenCloudRelaySnapshot{Enabled: false}, nil
		}
		return KeenCloudRelaySnapshot{Enabled: true, RelayIPs: []string{relayIP}}, nil
	})

	dnsFailHost := ""
	dnsFailAll := false
	brokerIP := "95.213.212.101"
	SetExtDNSLookupIPv4(func(_ context.Context, host string) ([]netip.Addr, error) {
		if dnsFailAll || (dnsFailHost != "" && host == dnsFailHost) {
			return nil, errors.New("dns timeout")
		}
		if host == "broker.keenetic.cloud" {
			return []netip.Addr{netip.MustParseAddr(brokerIP)}, nil
		}
		return []netip.Addr{netip.MustParseAddr("82.202.218.10")}, nil
	})

	// 1. Initial clean sync.
	if err := op.SyncKeenDNSCloud(context.Background(), true); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	initialSet := ipsetSt.snapshot(FextCloudIpsetName)
	initialSwaps := ipsetSt.swapCount
	if !slices.Contains(initialSet, relayIP) || !slices.Contains(initialSet, brokerIP) {
		t.Fatalf("missing initial relay/broker IPs in %v", initialSet)
	}

	// 2. Transient RCI error must return error, surface LastSyncError, and NOT touch FEXT_CLOUD.
	rciErr = errors.New("rci 503 unavailable")
	if err := op.SyncKeenDNSCloud(context.Background(), true); err == nil {
		t.Fatal("expected error when RCI provider fails")
	}
	lastErr, _ := op.keenDNSCloudStatus()
	if !strings.Contains(lastErr, "rci 503 unavailable") {
		t.Fatalf("expected surfaced RCI sync error, got %q", lastErr)
	}
	if ipsetSt.swapCount != initialSwaps || !equalStringSlices(ipsetSt.snapshot(FextCloudIpsetName), initialSet) {
		t.Fatalf("RCI error mutated existing FEXT_CLOUD (swaps %d vs %d)", ipsetSt.swapCount, initialSwaps)
	}
	rciErr = nil

	// 3. Total DNS outage (with confirmed empty relayIPs) must preserve last working set intact.
	rciEnabled = false
	dnsFailAll = true
	if err := op.SyncKeenDNSCloud(context.Background(), true); err == nil {
		t.Fatal("expected error on total DNS outage")
	}
	lastErr, _ = op.keenDNSCloudStatus()
	if !strings.Contains(lastErr, "dns timeout") {
		t.Fatalf("expected DNS sync error surfaced, got %q", lastErr)
	}
	if ipsetSt.swapCount != initialSwaps || !equalStringSlices(ipsetSt.snapshot(FextCloudIpsetName), initialSet) {
		t.Fatal("total DNS outage overwrote last working FEXT_CLOUD")
	}
	dnsFailAll = false

	// 4. Confirmed empty RCI result + healthy DNS succeeds with LastSyncError cleared, while keeping
	// previously seen dynamic entries within the 30m stale TTL.
	now = now.Add(5 * time.Minute)
	if err := op.SyncKeenDNSCloud(context.Background(), true); err != nil {
		t.Fatalf("confirmed empty RCI sync failed: %v", err)
	}
	if lastErr, _ := op.keenDNSCloudStatus(); lastErr != "" {
		t.Fatalf("expected cleared LastSyncError on confirmed empty RCI success, got %q", lastErr)
	}
	if !slices.Contains(ipsetSt.snapshot(FextCloudIpsetName), relayIP) {
		t.Fatalf("expected dynamic relay IP %q retained within 30m TTL window", relayIP)
	}

	// 5. Partial DNS failure: broker.keenetic.cloud fails even after >30m -> paused eviction preserves brokerIP AND returns error.
	now = now.Add(35 * time.Minute)
	dnsFailHost = "broker.keenetic.cloud"
	if err := op.SyncKeenDNSCloud(context.Background(), true); err == nil {
		t.Fatal("expected error on partial DNS failure")
	}
	if !slices.Contains(ipsetSt.snapshot(FextCloudIpsetName), brokerIP) {
		t.Fatalf("partial DNS failure dropped cached IP %q", brokerIP)
	}
	dnsFailHost = ""

	// 6. ipset swap failure returns error and keeps last FEXT_CLOUD state intact.
	ipsetSt.mu.Lock()
	ipsetSt.failOp = "swap"
	ipsetSt.mu.Unlock()
	prevSet := ipsetSt.snapshot(FextCloudIpsetName)
	if err := op.SyncKeenDNSCloud(context.Background(), true); err == nil {
		t.Fatal("expected error on ipset swap failure")
	}
	if !equalStringSlices(ipsetSt.snapshot(FextCloudIpsetName), prevSet) || ipsetSt.hasSet(fextCloudStagingIpsetName) {
		t.Fatal("ipset swap error left staging set behind or corrupted FEXT_CLOUD")
	}

	// 7. Advancing beyond stale TTL on a clean sync retires expired dynamic IP (relayIP).
	brokerIP = "95.213.212.202"
	now = now.Add(31 * time.Minute)
	if err := op.SyncKeenDNSCloud(context.Background(), true); err != nil {
		t.Fatalf("clean post-TTL sync failed: %v", err)
	}
	finalSet := ipsetSt.snapshot(FextCloudIpsetName)
	if slices.Contains(finalSet, relayIP) {
		t.Fatalf("expected expired relay IP %q to be pruned after stale TTL", relayIP)
	}
	if !slices.Contains(finalSet, brokerIP) {
		t.Fatalf("expected fresh broker IP %q in %v", brokerIP, finalSet)
	}
}

func TestKeenDNSAndXHTTPMatrix_ConnmarkSaveRestoreDisableAndReenable(t *testing.T) {
	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	defer SetExtIptablesRunner(nil)
	SetExtIpsetRunner(ipsetSt.runner)
	defer SetExtIpsetRunner(nil)
	InvalidateOutputRoutingCache()
	defer InvalidateOutputRoutingCache()
	defer SetExtDNSLookupIPv4(nil)

	SetExtDNSLookupIPv4(func(_ context.Context, _ string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("95.213.212.111")}, nil
	})

	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})
	op.SetKeenCloudProvider(func(_ context.Context) (KeenCloudRelaySnapshot, error) {
		return KeenCloudRelaySnapshot{Enabled: true, RelayIPs: []string{"91.92.241.111"}}, nil
	})
	var groupsMu sync.Mutex
	groups := []Group{{ID: "22223333", Name: "XHTTPGroup", Interface: []string{"tunvless0"}, Enable: true}}
	srv := startMockRunningFircServer(t, op, &groupsMu, &groups, iptSt)
	defer srv.Close()

	saveMarkRule := fircSaveMarkRule
	restoreMarkRule := fircRestoreMarkRule
	kdJumpRule := fircNewFlowMatch + " -j FEXT_" + KeenDNSGroupID
	kdUDPJumpRule := fircUnmarkedUDPMatch + " -j FEXT_" + KeenDNSGroupID
	cloudIpsetRule := "-m set --match-set " + FextCloudIpsetName + " dst " + fircNewFlowMatch + " -j MARK --set-xmark 0x40010000/0x40ff0000"
	cloudUDPSetRule := "-p udp -m set --match-set " + FextCloudIpsetName + " dst " + fircOrigUnmarkedMatch + " -j MARK --set-xmark 0x40010000/0x40ff0000"
	staticCIDRRule := "-d 185.162.93.0/24 " + fircNewFlowMatch + " -j MARK --set-xmark 0x40010000/0x40ff0000"

	// A. KeenDNS ON, XHTTP OFF -> CONNMARK --restore-mark and --save-mark with exact mask 0x40ff0000 MUST be present.
	if _, err := op.ApplyExtConfig(context.Background(), ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"tunvless0"}},
	}); err != nil {
		t.Fatalf("KD=on/XHTTP=off ApplyExtConfig failed: %v", err)
	}

	iptSt.mu.Lock()
	kdChain := append([]string(nil), iptSt.mangleChain["FEXT_"+KeenDNSGroupID]...)
	outRules := append([]string(nil), iptSt.mangleChain["FEXT_OUT"]...)
	iptSt.mu.Unlock()
	if !slices.Contains(kdChain, cloudIpsetRule) {
		t.Fatalf("FEXT_c100d001 missing FEXT_CLOUD match rule: %v", kdChain)
	}
	if !slices.Contains(kdChain, cloudUDPSetRule) {
		t.Fatalf("FEXT_c100d001 missing FEXT_CLOUD unmarked UDP rule: %v", kdChain)
	}
	if !slices.Contains(kdChain, staticCIDRRule) {
		t.Fatalf("FEXT_c100d001 missing static CIDR NEW match rule: %v", kdChain)
	}
	wantKDOnlyOut := []string{restoreMarkRule, kdJumpRule, kdUDPJumpRule, saveMarkRule}
	if !equalStringSlices(outRules, wantKDOnlyOut) {
		t.Fatalf("KD=on/XHTTP=off FEXT_OUT mismatch:\ngot:  %v\nwant: %v", outRules, wantKDOnlyOut)
	}

	// B. KeenDNS ON, XHTTP ON -> restore -> KeenDNS -> XHTTP -> save order in FEXT_OUT.
	if _, err := op.ApplyExtConfig(context.Background(), ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"tunvless0"}},
		ServerRouting: ServerRoutingConfig{
			EnabledServers:   []string{"vless_xhttp_server"},
			VlessXhttpServer: true,
		},
	}); err != nil {
		t.Fatalf("KD=on/XHTTP=on ApplyExtConfig failed: %v", err)
	}
	iptSt.mu.Lock()
	outBoth := append([]string(nil), iptSt.mangleChain["FEXT_OUT"]...)
	iptSt.mu.Unlock()
	if len(outBoth) < 5 || outBoth[0] != restoreMarkRule || outBoth[1] != kdJumpRule || outBoth[len(outBoth)-1] != saveMarkRule {
		t.Fatalf("KD=on/XHTTP=on FEXT_OUT unexpected: %v", outBoth)
	}

	// C. Disable KeenDNS while XHTTP stays ON -> jump removed first, FEXT_c100d001 flushed, FEXT_CLOUD destroyed, XHTTP intact.
	if _, err := op.ApplyExtConfig(context.Background(), ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: false, Interfaces: []string{"tunvless0"}},
		ServerRouting: ServerRoutingConfig{
			EnabledServers:   []string{"vless_xhttp_server"},
			VlessXhttpServer: true,
		},
	}); err != nil {
		t.Fatalf("KD=off/XHTTP=on ApplyExtConfig failed: %v", err)
	}
	if ipsetSt.hasSet(FextCloudIpsetName) {
		t.Fatal("expected FEXT_CLOUD destroyed when KeenDNS disabled")
	}
	iptSt.mu.Lock()
	_, kdChainStillExists := iptSt.mangleChain["FEXT_"+KeenDNSGroupID]
	outXhttpOnly := append([]string(nil), iptSt.mangleChain["FEXT_OUT"]...)
	xhttpGrp := append([]string(nil), iptSt.mangleChain["FEXT_22223333"]...)
	iptSt.mu.Unlock()
	if kdChainStillExists {
		t.Fatal("expected FEXT_c100d001 deleted on KeenDNS disable")
	}
	if slices.Contains(outXhttpOnly, kdJumpRule) || len(xhttpGrp) == 0 || outXhttpOnly[0] != restoreMarkRule || outXhttpOnly[len(outXhttpOnly)-1] != saveMarkRule {
		t.Fatalf("XHTTP rules broken after KeenDNS disable: FEXT_OUT=%v FEXT_22223333=%v", outXhttpOnly, xhttpGrp)
	}

	// D. Re-enable KeenDNS (with XHTTP on) -> cleanly restores FEXT_CLOUD, FEXT_c100d001, and FEXT_OUT restore+jump.
	if _, err := op.ApplyExtConfig(context.Background(), ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"tunvless0"}},
		ServerRouting: ServerRoutingConfig{
			EnabledServers:   []string{"vless_xhttp_server"},
			VlessXhttpServer: true,
		},
	}); err != nil {
		t.Fatalf("re-enable KeenDNS failed: %v", err)
	}
	if !ipsetSt.hasSet(FextCloudIpsetName) {
		t.Fatal("expected FEXT_CLOUD recreated on KeenDNS re-enable")
	}
	iptSt.mu.Lock()
	outReenabled := append([]string(nil), iptSt.mangleChain["FEXT_OUT"]...)
	kdChainReenabled := append([]string(nil), iptSt.mangleChain["FEXT_"+KeenDNSGroupID]...)
	iptSt.mu.Unlock()
	if len(outReenabled) < 5 || outReenabled[0] != restoreMarkRule || outReenabled[1] != kdJumpRule || !slices.Contains(kdChainReenabled, cloudIpsetRule) {
		t.Fatalf("KeenDNS restore failed: out=%v chain=%v", outReenabled, kdChainReenabled)
	}
}

func TestKeenDNSWatchdog_ManualDisableAndContextCancellation(t *testing.T) {
	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	defer SetExtIptablesRunner(nil)
	SetExtIpsetRunner(ipsetSt.runner)
	defer SetExtIpsetRunner(nil)
	InvalidateOutputRoutingCache()
	defer InvalidateOutputRoutingCache()
	defer SetExtDNSLookupIPv4(nil)

	var fakeNow atomic.Int64
	fakeNow.Store(time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC).Unix())
	setExtCloudNow(func() time.Time { return time.Unix(fakeNow.Load(), 0) })
	defer setExtCloudNow(nil)

	var providerCalls atomic.Int32
	inFlightEntered := make(chan struct{}, 1)
	releaseInFlight := make(chan struct{})
	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})
	op.SetKeenCloudProvider(func(ctx context.Context) (KeenCloudRelaySnapshot, error) {
		if providerCalls.Add(1) == 1 {
			select {
			case inFlightEntered <- struct{}{}:
			default:
			}
			select {
			case <-ctx.Done():
				return KeenCloudRelaySnapshot{}, ctx.Err()
			case <-releaseInFlight:
			}
		}
		fakeNow.Add(int64((keenCloudSyncInterval + 10*time.Second) / time.Second))
		return KeenCloudRelaySnapshot{Enabled: true, RelayIPs: []string{"91.92.241.77"}}, nil
	})
	SetExtDNSLookupIPv4(func(_ context.Context, _ string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("95.213.212.12")}, nil
	})

	// 1. Mid-flight context cancellation aborts sync before creating/swapping FEXT_CLOUD.
	cancelCtx, cancel := context.WithCancel(context.Background())
	syncErrCh := make(chan error, 1)
	go func() {
		syncErrCh <- op.SyncKeenDNSCloud(cancelCtx, true)
	}()
	<-inFlightEntered
	cancel()
	if err := <-syncErrCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on aborted sync, got %v", err)
	}
	if ipsetSt.hasSet(FextCloudIpsetName) {
		t.Fatal("aborted sync must not create FEXT_CLOUD")
	}
	close(releaseInFlight)
	op.clearKeenDNSCloudSyncState()

	// 2. Start watchdog with KeenDNS enabled, wait for at least two periodic watchdog syncs,
	// then invoke StopByUser -> watchdog must stop and issue 0 further RCI calls.
	var groupsMu sync.Mutex
	groups := []Group{{ID: KeenDNSGroupID, Name: "KD", Interface: []string{"tunvless0"}, Enable: true}}
	srv := startMockRunningFircServer(t, op, &groupsMu, &groups, iptSt)
	defer srv.Close()

	if err := op.SaveExtConfig(ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"tunvless0"}},
	}); err != nil {
		t.Fatal(err)
	}
	op.SetLifecycleHooks(func(context.Context) error { return nil }, func(context.Context) error { srv.Close(); return nil })

	op.startExtWatchdog(10 * time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && providerCalls.Load() < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	if providerCalls.Load() < 3 {
		t.Fatalf("expected watchdog to invoke KeenCloudProvider >=2 times after initial cancel (total >=3), got %d calls", providerCalls.Load())
	}

	if err := op.StopByUser(context.Background()); err != nil {
		t.Fatalf("StopByUser: %v", err)
	}
	callsAtStop := providerCalls.Load()
	time.Sleep(80 * time.Millisecond)
	if after := providerCalls.Load(); after != callsAtStop {
		t.Fatalf("watchdog continued running after StopByUser: calls went from %d to %d", callsAtStop, after)
	}
	if err := op.SyncKeenDNSCloud(context.Background(), true); !errors.Is(err, ErrManuallyDisabled) {
		t.Fatalf("expected ErrManuallyDisabled from direct sync after StopByUser, got %v", err)
	}
}

func TestKeenDNSTeardown_PreservesForeignResourcesAndIdempotent(t *testing.T) {
	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	defer SetExtIptablesRunner(nil)
	SetExtIpsetRunner(ipsetSt.runner)
	defer SetExtIpsetRunner(nil)
	InvalidateOutputRoutingCache()
	defer InvalidateOutputRoutingCache()
	defer SetExtDNSLookupIPv4(nil)

	SetExtDNSLookupIPv4(func(_ context.Context, _ string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("95.213.212.66")}, nil
	})

	// Pre-populate foreign ipsets, foreign chains/marks, and a legacy AWGM-CLOUD rule in FEXT_c100d001.
	ipsetSt.mu.Lock()
	ipsetSt.sets["AWGM-CLOUD"] = map[string]bool{"198.51.100.77": true}
	ipsetSt.sets["AWGM-BYPASS"] = map[string]bool{"8.8.8.8": true}
	ipsetSt.mu.Unlock()

	iptSt.mu.Lock()
	iptSt.mangleChain["FOREIGN_CHAIN"] = []string{"-d 198.51.100.1/32 -j MARK --set-xmark 0x1234/0xffff"}
	iptSt.mangleChain["OUTPUT"] = []string{"-j FOREIGN_CHAIN"}
	iptSt.mangleChain["FEXT_"+KeenDNSGroupID] = []string{
		"-m set --match-set AWGM-CLOUD dst -j MARK --set-xmark 0x40010000/0x40ff0000",
	}
	iptSt.mu.Unlock()

	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})
	op.SetKeenCloudProvider(func(_ context.Context) (KeenCloudRelaySnapshot, error) {
		return KeenCloudRelaySnapshot{Enabled: true, RelayIPs: []string{"91.92.241.66"}}, nil
	})
	var groupsMu sync.Mutex
	groups := []Group{}
	srv := startMockRunningFircServer(t, op, &groupsMu, &groups, iptSt)
	defer srv.Close()

	// Reconcile while KeenDNS enabled -> migrates FEXT_c100d001 away from legacy AWGM-CLOUD to FEXT_CLOUD without touching AWGM-CLOUD.
	if _, err := op.ApplyExtConfig(context.Background(), ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"tunvless0"}},
	}); err != nil {
		t.Fatalf("ApplyExtConfig enable failed: %v", err)
	}
	if !equalStringSlices(ipsetSt.snapshot("AWGM-CLOUD"), []string{"198.51.100.77"}) {
		t.Fatalf("foreign AWGM-CLOUD mutated during FIRC reconcile: %v", ipsetSt.snapshot("AWGM-CLOUD"))
	}

	// Disable KeenDNS, then run cleanupExtNetfilterRules 3x -> idempotent and preserves foreign resources.
	if _, err := op.ApplyExtConfig(context.Background(), ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: false},
	}); err != nil {
		t.Fatalf("ApplyExtConfig disable failed: %v", err)
	}
	for i := 0; i < 3; i++ {
		cleanupExtNetfilterRules(context.Background())
		if err := teardownKeenDNSCloudChainAndSet(context.Background()); err != nil {
			t.Fatalf("repeated teardown #%d failed: %v", i+1, err)
		}
	}

	if ipsetSt.hasSet(FextCloudIpsetName) || ipsetSt.hasSet(fextCloudStagingIpsetName) {
		t.Fatal("FIRC-owned cloud ipsets still present after cleanup")
	}
	if !equalStringSlices(ipsetSt.snapshot("AWGM-CLOUD"), []string{"198.51.100.77"}) {
		t.Fatalf("foreign AWGM-CLOUD altered or destroyed: %v", ipsetSt.snapshot("AWGM-CLOUD"))
	}
	if !equalStringSlices(ipsetSt.snapshot("AWGM-BYPASS"), []string{"8.8.8.8"}) {
		t.Fatalf("foreign AWGM-BYPASS altered or destroyed: %v", ipsetSt.snapshot("AWGM-BYPASS"))
	}
	iptSt.mu.Lock()
	foreignChain := append([]string(nil), iptSt.mangleChain["FOREIGN_CHAIN"]...)
	outputChain := append([]string(nil), iptSt.mangleChain["OUTPUT"]...)
	_, fextKdStillPresent := iptSt.mangleChain["FEXT_"+KeenDNSGroupID]
	iptSt.mu.Unlock()
	if fextKdStillPresent {
		t.Fatal("FEXT_c100d001 still present after cleanup")
	}
	if !equalStringSlices(foreignChain, []string{"-d 198.51.100.1/32 -j MARK --set-xmark 0x1234/0xffff"}) {
		t.Fatalf("foreign chain modified: %v", foreignChain)
	}
	if !equalStringSlices(outputChain, []string{"-j FOREIGN_CHAIN"}) {
		t.Fatalf("foreign OUTPUT jump removed: %v", outputChain)
	}
}

func TestKeenDNSDisable_SerializesWithInFlightSyncWhileXHTTPActiveAndReenables(t *testing.T) {
	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	defer SetExtIptablesRunner(nil)
	SetExtIpsetRunner(ipsetSt.runner)
	defer SetExtIpsetRunner(nil)
	InvalidateOutputRoutingCache()
	defer InvalidateOutputRoutingCache()
	defer SetExtDNSLookupIPv4(nil)
	defer setExtCloudNow(nil)

	var fakeNow atomic.Int64
	fakeNow.Store(time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC).Unix())
	setExtCloudNow(func() time.Time { return time.Unix(fakeNow.Load(), 0) })

	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})

	var (
		gateMu          sync.Mutex
		blockProvider   bool
		providerEntered chan struct{}
		providerCtxDone chan struct{}
		releaseProvider chan struct{}
		relayIPToReturn = "91.92.241.201"

		blockDNS   bool
		dnsEntered chan struct{}
		releaseDNS chan struct{}
		dnsIPToRet = "95.213.212.201"
	)

	op.SetKeenCloudProvider(func(rciCtx context.Context) (KeenCloudRelaySnapshot, error) {
		gateMu.Lock()
		blk := blockProvider
		entered := providerEntered
		ctxDone := providerCtxDone
		rel := releaseProvider
		ip := relayIPToReturn
		if blk {
			blockProvider = false
		}
		gateMu.Unlock()

		if blk {
			close(entered)
			select {
			case <-rciCtx.Done():
				close(ctxDone)
			case <-time.After(3 * time.Second):
			}
			<-rel
		}
		return KeenCloudRelaySnapshot{Enabled: true, RelayIPs: []string{ip}}, nil
	})

	SetExtDNSLookupIPv4(func(_ context.Context, host string) ([]netip.Addr, error) {
		gateMu.Lock()
		blk := blockDNS && host == "broker.keenetic.cloud"
		entered := dnsEntered
		rel := releaseDNS
		ip := dnsIPToRet
		if blk {
			blockDNS = false
		}
		gateMu.Unlock()

		if blk {
			close(entered)
			<-rel
		}
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	})

	var groupsMu sync.Mutex
	groups := []Group{{ID: "22223333", Name: "XHTTPGroup", Interface: []string{"tunvless0"}, Enable: true}}
	srv := startMockRunningFircServer(t, op, &groupsMu, &groups, iptSt)
	defer srv.Close()

	saveMarkRule := "-m mark --mark 0x40000000/0x40000000 -j CONNMARK --save-mark --nfmask 0x40ff0000 --ctmask 0x40ff0000"
	xhttpCfg := ServerRoutingConfig{
		EnabledServers:   []string{"vless_xhttp_server"},
		VlessXhttpServer: true,
	}
	cfgEnabled := ExtConfig{
		KeenDNSCloud:  KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"tunvless0"}},
		ServerRouting: xhttpCfg,
	}
	cfgDisabled := ExtConfig{
		KeenDNSCloud:  KeenDNSCloudConfig{Enabled: false, Interfaces: []string{"tunvless0"}},
		ServerRouting: xhttpCfg,
	}

	assertXHTTPIntactKDDisabled := func(stage string) {
		t.Helper()
		if ipsetSt.hasSet(FextCloudIpsetName) || ipsetSt.hasSet(fextCloudStagingIpsetName) {
			t.Fatalf("%s: expected FEXT_CLOUD and FEXT_CLOUD_TMP absent", stage)
		}
		iptSt.mu.Lock()
		_, kdChainExists := iptSt.mangleChain["FEXT_"+KeenDNSGroupID]
		outRules := append([]string(nil), iptSt.mangleChain["FEXT_OUT"]...)
		xhttpChain := append([]string(nil), iptSt.mangleChain["FEXT_22223333"]...)
		iptSt.mu.Unlock()
		if kdChainExists {
			t.Fatalf("%s: expected FEXT_%s deleted", stage, KeenDNSGroupID)
		}
		for _, r := range outRules {
			if strings.Contains(r, "-j FEXT_"+KeenDNSGroupID) {
				t.Fatalf("%s: unexpected jump to FEXT_%s in FEXT_OUT: %v", stage, KeenDNSGroupID, outRules)
			}
		}
		hasXhttpJump := false
		hasFakeIPMark := false
		for _, r := range outRules {
			if strings.Contains(r, "-j FEXT_22223333") {
				hasXhttpJump = true
			}
			if strings.Contains(r, "-d 198.18.0.0/15 ") {
				hasFakeIPMark = true
			}
		}
		if !hasXhttpJump || !hasFakeIPMark || len(xhttpChain) == 0 || len(outRules) == 0 || outRules[len(outRules)-1] != saveMarkRule {
			t.Fatalf("%s: XHTTP rules damaged after KeenDNS disable: FEXT_OUT=%v FEXT_22223333=%v", stage, outRules, xhttpChain)
		}
		lastErr, lastAt := op.keenDNSCloudStatus()
		op.cloudStateMu.RLock()
		setReady := op.cloudSetReady
		op.cloudStateMu.RUnlock()
		statusView := op.LoadExtConfigWithStatus(context.Background())
		if lastErr != "" || lastAt != 0 || setReady || statusView.KeenDNSCloud.LastSyncError != "" || statusView.KeenDNSCloud.LastSyncAt != 0 {
			t.Fatalf("%s: stale status published after disable: lastErr=%q lastAt=%d setReady=%v view=(%q,%d)",
				stage, lastErr, lastAt, setReady, statusView.KeenDNSCloud.LastSyncError, statusView.KeenDNSCloud.LastSyncAt)
		}
	}

	// 1. Initial enable of KeenDNS + XHTTP.
	res1, err := op.ApplyExtConfig(context.Background(), cfgEnabled)
	if err != nil {
		t.Fatalf("initial ApplyExtConfig enable failed: %v", err)
	}
	if res1.KeenDNSCloud.LastSyncAt == 0 || res1.KeenDNSCloud.LastSyncError != "" || !ipsetSt.hasSet(FextCloudIpsetName) {
		t.Fatalf("initial enable incomplete: res=(%q,%d) hasSet=%v",
			res1.KeenDNSCloud.LastSyncError, res1.KeenDNSCloud.LastSyncAt, ipsetSt.hasSet(FextCloudIpsetName))
	}

	// 2. Start in-flight watchdog tick, block inside KeenCloudRelayProvider on a channel,
	// disable KeenDNS (keeping XHTTP enabled), wait for disable to return, then release provider
	// with a non-error snapshot and verify zero late ipset/iptables/status writes.
	fakeNow.Add(int64((keenCloudSyncInterval + 10*time.Second) / time.Second))
	gateMu.Lock()
	blockProvider = true
	providerEntered = make(chan struct{})
	providerCtxDone = make(chan struct{})
	releaseProvider = make(chan struct{})
	relayIPToReturn = "91.92.241.202"
	gateMu.Unlock()

	tickDone := make(chan struct{})
	go func() {
		op.runExtWatchdogTick(context.Background())
		close(tickDone)
	}()
	<-providerEntered

	resOff, err := op.ApplyExtConfig(context.Background(), cfgDisabled)
	if err != nil {
		t.Fatalf("ApplyExtConfig disable while provider blocked failed: %v", err)
	}
	if resOff.KeenDNSCloud.LastSyncAt != 0 || resOff.KeenDNSCloud.LastSyncError != "" {
		t.Fatalf("expected cleared status from ApplyExtConfig disable, got (%q, %d)",
			resOff.KeenDNSCloud.LastSyncError, resOff.KeenDNSCloud.LastSyncAt)
	}
	select {
	case <-providerCtxDone:
	case <-time.After(time.Second):
		t.Fatal("expected disabling KeenDNS to cancel in-flight provider context promptly")
	}

	assertXHTTPIntactKDDisabled("right-after-disable")
	ipsetSt.resetOps()

	close(releaseProvider)
	select {
	case <-tickDone:
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight watchdog tick did not exit after releasing provider")
	}

	if lateOps := ipsetSt.opsSnapshot(); len(lateOps) != 0 {
		t.Fatalf("unexpected late ipset operations after KeenDNS disable: %v", lateOps)
	}
	assertXHTTPIntactKDDisabled("after-provider-release")

	// 3. Repeat race with blocking inside DNS lookup (post-provider) so stale resolved results
	// also cannot create/swap FEXT_CLOUD, mutate FEXT_OUT, or publish status after disable.
	if _, err := op.ApplyExtConfig(context.Background(), cfgEnabled); err != nil {
		t.Fatalf("re-enable before DNS race failed: %v", err)
	}
	fakeNow.Add(int64((keenCloudSyncInterval + 10*time.Second) / time.Second))
	gateMu.Lock()
	blockDNS = true
	dnsEntered = make(chan struct{})
	releaseDNS = make(chan struct{})
	relayIPToReturn = "91.92.241.203"
	dnsIPToRet = "95.213.212.203"
	gateMu.Unlock()

	dnsTickDone := make(chan struct{})
	go func() {
		op.runExtWatchdogTick(context.Background())
		close(dnsTickDone)
	}()
	<-dnsEntered

	if _, err := op.ApplyExtConfig(context.Background(), cfgDisabled); err != nil {
		t.Fatalf("ApplyExtConfig disable during DNS resolve failed: %v", err)
	}
	assertXHTTPIntactKDDisabled("right-after-dns-disable")
	ipsetSt.resetOps()

	close(releaseDNS)
	select {
	case <-dnsTickDone:
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight DNS watchdog tick did not exit after releasing DNS")
	}

	if lateOps := ipsetSt.opsSnapshot(); len(lateOps) != 0 {
		t.Fatalf("unexpected late ipset operations after DNS release post-disable: %v", lateOps)
	}
	assertXHTTPIntactKDDisabled("after-dns-release")

	// 4. Directly verify stale ExtConfig generation guard drops an outdated ExtConfig pass under netfilterMu.
	staleGen := op.currentExtConfigGen() - 1
	staleNfGen := extNetfilterGen.Load() - 1
	_ = op.reconcileExtNetfilterLockedWithCfgGen(context.Background(), cfgEnabled, groups, staleNfGen, staleGen, true, true)
	assertXHTTPIntactKDDisabled("after-stale-cfgGen-reconcile")

	// 5. Re-enable KeenDNS cleanly recreates FEXT_CLOUD, FEXT_c100d001, -j FEXT_c100d001, and LastSyncAt > 0.
	gateMu.Lock()
	relayIPToReturn = "91.92.241.204"
	dnsIPToRet = "95.213.212.204"
	gateMu.Unlock()
	fakeNow.Add(5)

	resReenable, err := op.ApplyExtConfig(context.Background(), cfgEnabled)
	if err != nil {
		t.Fatalf("final re-enable ApplyExtConfig failed: %v", err)
	}
	if resReenable.KeenDNSCloud.LastSyncAt == 0 || resReenable.KeenDNSCloud.LastSyncError != "" {
		t.Fatalf("expected clean LastSyncAt > 0 after re-enable, got err=%q at=%d",
			resReenable.KeenDNSCloud.LastSyncError, resReenable.KeenDNSCloud.LastSyncAt)
	}
	if !ipsetSt.hasSet(FextCloudIpsetName) || ipsetSt.hasSet(fextCloudStagingIpsetName) {
		t.Fatalf("expected FEXT_CLOUD present and FEXT_CLOUD_TMP absent after re-enable, hasCloud=%v hasTmp=%v",
			ipsetSt.hasSet(FextCloudIpsetName), ipsetSt.hasSet(fextCloudStagingIpsetName))
	}
	gotSet := ipsetSt.snapshot(FextCloudIpsetName)
	if !slices.Contains(gotSet, "91.92.241.204") || !slices.Contains(gotSet, "95.213.212.204") {
		t.Fatalf("FEXT_CLOUD missing expected re-enabled entries in %v", gotSet)
	}
	iptSt.mu.Lock()
	outReenabled := append([]string(nil), iptSt.mangleChain["FEXT_OUT"]...)
	kdChainReenabled := append([]string(nil), iptSt.mangleChain["FEXT_"+KeenDNSGroupID]...)
	iptSt.mu.Unlock()
	if len(outReenabled) < 2 || outReenabled[0] != fircRestoreMarkRule || !strings.Contains(outReenabled[1], "-j FEXT_"+KeenDNSGroupID) || outReenabled[len(outReenabled)-1] != saveMarkRule {
		t.Fatalf("unexpected FEXT_OUT after KeenDNS re-enable matrix: %v", outReenabled)
	}
	cloudIpsetRule := "-m set --match-set " + FextCloudIpsetName + " dst " + fircNewFlowMatch + " -j MARK --set-xmark 0x40010000/0x40ff0000"
	if !slices.Contains(kdChainReenabled, cloudIpsetRule) {
		t.Fatalf("FEXT_%s missing FEXT_CLOUD rule after re-enable: %v", KeenDNSGroupID, kdChainReenabled)
	}
}

func TestCutoverBarrier_OrderSuccessRollbackFailureRetentionAndRecovery(t *testing.T) {
	iptSt := newFakeIptablesState()
	SetExtIptablesRunner(iptSt.runner)
	defer SetExtIptablesRunner(nil)
	InvalidateOutputRoutingCache()

	iptSt.mu.Lock()
	iptSt.mangleChain["FORWARD"] = []string{"-j SINGBOX_TPROXY"}
	iptSt.mu.Unlock()

	var fircRunning atomic.Bool
	var failFircStart atomic.Bool
	var failActivate atomic.Bool

	mockMux := http.NewServeMux()
	mockMux.HandleFunc("/api/v1/system/netfilter", func(w http.ResponseWriter, _ *http.Request) {
		if !fircRunning.Load() {
			http.Error(w, "stopped", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(NetfilterHealth{Ok: true})
	})
	mockMux.HandleFunc("/api/v1/tunnels", func(w http.ResponseWriter, _ *http.Request) {
		if !fircRunning.Load() {
			http.Error(w, "stopped", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(PutTunnelsResponse{})
	})
	mockMux.HandleFunc("/api/v1/groups", func(w http.ResponseWriter, _ *http.Request) {
		if !fircRunning.Load() {
			http.Error(w, "stopped", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"groups": []Group{}})
	})
	mockSrv := httptest.NewServer(mockMux)
	defer mockSrv.Close()

	cfgDir := t.TempDir()
	op := NewOperator(Options{ConfigDir: cfgDir})
	op.SetClient(NewHTTPClient(mockSrv.URL, mockSrv.Client()))

	assertCutoverEngagedLocked := func(stage string) {
		t.Helper()
		specsToStrings := func(specs [][]string) []string {
			out := make([]string, len(specs))
			for i, sp := range specs {
				out[i] = strings.Join(sp, " ")
			}
			return out
		}
		iptSt.mu.Lock()
		fwd := append([]string(nil), iptSt.mangleChain["FORWARD"]...)
		guard := append([]string(nil), iptSt.mangleChain[cutoverGuardChain]...)
		pre := append([]string(nil), iptSt.mangleChain["PREROUTING"]...)
		preGuard := append([]string(nil), iptSt.mangleChain[cutoverGuardPreChain]...)
		filterFwd := append([]string(nil), iptSt.filterChains["FORWARD"]...)
		filterGuard := append([]string(nil), iptSt.filterChains[cutoverGuardFwdChain]...)
		iptSt.mu.Unlock()
		if len(fwd) == 0 || fwd[0] != "-j "+cutoverGuardChain {
			t.Fatalf("%s: expected mangle FORWARD[0] == -j %s, got %v", stage, cutoverGuardChain, fwd)
		}
		if want := specsToStrings(cutoverGuardMangleRules(true)); !slices.Equal(guard, want) {
			t.Fatalf("%s: %s rules mismatch:\ngot:  %v\nwant: %v", stage, cutoverGuardChain, guard, want)
		}
		if len(filterFwd) == 0 || filterFwd[0] != "-j "+cutoverGuardFwdChain {
			t.Fatalf("%s: expected filter FORWARD[0] == -j %s, got %v", stage, cutoverGuardFwdChain, filterFwd)
		}
		if want := specsToStrings(cutoverGuardFwdRules()); !slices.Equal(filterGuard, want) {
			t.Fatalf("%s: %s rules mismatch:\ngot:  %v\nwant: %v", stage, cutoverGuardFwdChain, filterGuard, want)
		}
		if len(pre) == 0 || pre[0] != "-j "+cutoverGuardPreChain {
			t.Fatalf("%s: expected mangle PREROUTING[0] == -j %s, got %v", stage, cutoverGuardPreChain, pre)
		}
		if want := specsToStrings(cutoverGuardPreRules()); !slices.Equal(preGuard, want) {
			t.Fatalf("%s: %s rules mismatch:\ngot:  %v\nwant: %v", stage, cutoverGuardPreChain, preGuard, want)
		}
	}
	assertCutoverAbsent := func(stage string) {
		t.Helper()
		iptSt.mu.Lock()
		fwd := append([]string(nil), iptSt.mangleChain["FORWARD"]...)
		_, exists := iptSt.mangleChain[cutoverGuardChain]
		pre := append([]string(nil), iptSt.mangleChain["PREROUTING"]...)
		_, preExists := iptSt.mangleChain[cutoverGuardPreChain]
		filterFwd := append([]string(nil), iptSt.filterChains["FORWARD"]...)
		_, filterExists := iptSt.filterChains[cutoverGuardFwdChain]
		iptSt.mu.Unlock()
		if slices.Contains(fwd, "-j "+cutoverGuardChain) || exists ||
			slices.Contains(pre, "-j "+cutoverGuardPreChain) || preExists ||
			slices.Contains(filterFwd, "-j "+cutoverGuardFwdChain) || filterExists {
			t.Fatalf("%s: expected cutover guards completely removed, mangleFwd=%v/%v pre=%v/%v filterFwd=%v/%v",
				stage, fwd, exists, pre, preExists, filterFwd, filterExists)
		}
	}

	expectCutoverGuard := true
	op.SetLifecycleHooks(
		func(_ context.Context) error {
			if failFircStart.Load() {
				return errors.New("injected firc start failure")
			}
			// Guard must already be at FORWARD index 1 before starting candidate fircd when switching from sing-box.
			if expectCutoverGuard {
				assertCutoverEngagedLocked("firc_start_hook")
			} else {
				assertCutoverAbsent("firc_start_hook")
			}
			fircRunning.Store(true)
			iptSt.mu.Lock()
			iptSt.filterChains["FORWARD"] = append([]string{"-j FIRC_POOLREJECT"}, iptSt.filterChains["FORWARD"]...)
			iptSt.filterChains["FIRC_POOLREJECT"] = []string{"-d 198.18.0.0/15 -j REJECT --reject-with icmp-port-unreachable"}
			iptSt.mu.Unlock()
			return nil
		},
		func(_ context.Context) error {
			fircRunning.Store(false)
			return nil
		},
	)

	currentEngine := "sing-box"
	var orderLog []string
	hooks := EngineSwitchHooks{
		GetCurrentEngine: func(_ context.Context) (string, error) {
			return currentEngine, nil
		},
		StartTarget: func(_ context.Context, target string) error {
			orderLog = append(orderLog, "start_target:"+target)
			if expectCutoverGuard {
				assertCutoverEngagedLocked("start_target:" + target)
			} else {
				assertCutoverAbsent("start_target:" + target)
			}
			return nil
		},
		StopEngine: func(_ context.Context, eng string) error {
			orderLog = append(orderLog, "stop_engine:"+eng)
			if expectCutoverGuard {
				assertCutoverEngagedLocked("stop_engine:" + eng)
			} else {
				assertCutoverAbsent("stop_engine:" + eng)
			}
			return nil
		},
		ParkEngineForStandby: func(_ context.Context, eng string) error {
			orderLog = append(orderLog, "park_standby:"+eng)
			if expectCutoverGuard {
				assertCutoverEngagedLocked("park_standby:" + eng)
			} else {
				assertCutoverAbsent("park_standby:" + eng)
			}
			return nil
		},
		ActivateTarget: func(_ context.Context, target string) error {
			orderLog = append(orderLog, "activate:"+target)
			if expectCutoverGuard {
				assertCutoverEngagedLocked("activate:" + target)
			} else {
				assertCutoverAbsent("activate:" + target)
			}
			if failActivate.CompareAndSwap(true, false) {
				return errors.New("injected activate failure")
			}
			return nil
		},
		CommitSettings: func(_ context.Context, target string) error {
			orderLog = append(orderLog, "commit:"+target)
			currentEngine = target
			return nil
		},
		OnSwitched: func(selected bool) {
			if selected {
				orderLog = append(orderLog, "on_switched:firc")
			} else {
				orderLog = append(orderLog, "on_switched:sing-box")
			}
			// Cutover barrier must remain engaged until OnSwitched finishes.
			if expectCutoverGuard {
				assertCutoverEngagedLocked("on_switched")
			} else {
				assertCutoverAbsent("on_switched")
			}
		},
	}

	ctx := context.Background()

	// 1. Cross-engine switch sing-box -> firc: barrier engages before candidate start/park and releases after completion.
	if err := op.SwitchEngineWithOptions(ctx, "firc", EngineSwitchOptions{SingboxStandby: true}, hooks); err != nil {
		t.Fatalf("SwitchEngineWithOptions(sing-box->firc) failed: %v", err)
	}
	assertCutoverAbsent("after_switch_to_firc")

	// 2. Same-engine convergence firc -> firc MUST NOT install AWGM_CUTOVER_GUARD.
	iptSt.mu.Lock()
	iptSt.cmdLog = nil
	iptSt.mu.Unlock()
	expectCutoverGuard = false
	if err := op.SwitchEngineWithOptions(ctx, "firc", EngineSwitchOptions{}, hooks); err != nil {
		t.Fatalf("same-engine SwitchEngineWithOptions(firc) failed: %v", err)
	}
	expectCutoverGuard = true
	iptSt.mu.Lock()
	for _, cmd := range iptSt.cmdLog {
		if strings.Contains(cmd, cutoverGuardChain) {
			iptSt.mu.Unlock()
			t.Fatalf("same-engine switch unexpectedly touched %s: %s", cutoverGuardChain, cmd)
		}
	}
	iptSt.mu.Unlock()

	// 3. Cross-engine switch firc -> sing-box: removes AWGM_CUTOVER_GUARD and cleans FIRC_POOLREJECT.
	if err := op.SwitchEngineWithOptions(ctx, "sing-box", EngineSwitchOptions{}, hooks); err != nil {
		t.Fatalf("SwitchEngineWithOptions(firc->sing-box) failed: %v", err)
	}
	assertCutoverAbsent("after_switch_to_singbox")
	iptSt.mu.Lock()
	_, poolExists := iptSt.filterChains["FIRC_POOLREJECT"]
	poolJumped := slices.Contains(iptSt.filterChains["FORWARD"], "-j FIRC_POOLREJECT")
	iptSt.mu.Unlock()
	if poolExists || poolJumped {
		t.Fatalf("expected FIRC_POOLREJECT removed on handoff to sing-box, exists=%v jumped=%v", poolExists, poolJumped)
	}

	// 4. Clean rollback on failed sing-box -> firc ActivateTarget releases AWGM_CUTOVER_GUARD and removes FIRC_POOLREJECT.
	failActivate.Store(true)
	if err := op.SwitchEngineWithOptions(ctx, "firc", EngineSwitchOptions{}, hooks); err == nil {
		t.Fatal("expected SwitchEngineWithOptions(sing-box->firc) to fail at ActivateTarget")
	}
	if op.IsRecoveryRequired() {
		t.Fatalf("expected clean rollback without recovery_required")
	}
	assertCutoverAbsent("after_clean_rollback")
	iptSt.mu.Lock()
	_, poolExists = iptSt.filterChains["FIRC_POOLREJECT"]
	poolJumped = slices.Contains(iptSt.filterChains["FORWARD"], "-j FIRC_POOLREJECT")
	iptSt.mu.Unlock()
	if poolExists || poolJumped {
		t.Fatalf("expected FIRC_POOLREJECT removed after clean rollback, exists=%v jumped=%v", poolExists, poolJumped)
	}

	// 5. Rollback failure -> RecoveryRequired retains AWGM_CUTOVER_GUARD until ReconcileRecovery clears it,
	//    and ReconcileRecovery NEVER flushes AWGM_CUTOVER_GUARD while already engaged.
	failActivate.Store(false)
	if err := op.SwitchEngineWithOptions(ctx, "firc", EngineSwitchOptions{}, hooks); err != nil {
		t.Fatalf("setup switch to firc failed: %v", err)
	}
	failActivate.Store(true)
	failFircStart.Store(true)
	err := op.SwitchEngineWithOptions(ctx, "sing-box", EngineSwitchOptions{}, hooks)
	if err == nil || !errors.Is(err, ErrRecoveryRequired) || !op.IsRecoveryRequired() {
		t.Fatalf("expected ErrRecoveryRequired on double fault, got err=%v req=%v", err, op.IsRecoveryRequired())
	}
	assertCutoverEngagedLocked("during_recovery_required")

	failActivate.Store(false)
	failFircStart.Store(false)
	iptSt.mu.Lock()
	iptSt.cmdLog = nil
	iptSt.mu.Unlock()
	if _, err := op.ReconcileRecovery(ctx, "sing-box", hooks); err != nil {
		t.Fatalf("ReconcileRecovery failed: %v", err)
	}
	if op.IsRecoveryRequired() {
		t.Fatal("expected IsRecoveryRequired() == false after ReconcileRecovery")
	}
	iptSt.mu.Lock()
	sawUnlinkMangle := false
	sawUnlinkFilter := false
	sawUnlinkPre := false
	for _, cmd := range iptSt.cmdLog {
		switch cmd {
		case "-t mangle -D FORWARD -j " + cutoverGuardChain:
			sawUnlinkMangle = true
			continue
		case "-t filter -D FORWARD -j " + cutoverGuardFwdChain:
			sawUnlinkFilter = true
			continue
		case "-t mangle -D PREROUTING -j " + cutoverGuardPreChain:
			sawUnlinkPre = true
			continue
		}
		if (cmd == "-t mangle -F "+cutoverGuardChain && !sawUnlinkMangle) || strings.HasPrefix(cmd, "-t mangle -A "+cutoverGuardChain) ||
			(cmd == "-t filter -F "+cutoverGuardFwdChain && !sawUnlinkFilter) || strings.HasPrefix(cmd, "-t filter -A "+cutoverGuardFwdChain) ||
			(cmd == "-t mangle -F "+cutoverGuardPreChain && !sawUnlinkPre) || strings.HasPrefix(cmd, "-t mangle -A "+cutoverGuardPreChain) {
			iptSt.mu.Unlock()
			t.Fatalf("ReconcileRecovery flushed or rebuilt live cutover chain creating fail-open gap: %v", iptSt.cmdLog)
		}
	}
	iptSt.mu.Unlock()
	assertCutoverAbsent("after_reconcile_recovery")

	// 6. Break-glass ForceClearRecoveryAdmin also clears a retained AWGM_CUTOVER_GUARD.
	if err := op.SetRecoveryRequiredWithMeta(RecoveryMarker{
		Reason:       "manual breakglass barrier check",
		TransitionID: "tx-bg-guard",
		TargetEngine: "sing-box",
	}); err != nil {
		t.Fatalf("SetRecoveryRequiredWithMeta: %v", err)
	}
	if err := op.EngageCutoverBarrier(ctx); err != nil {
		t.Fatalf("EngageCutoverBarrier: %v", err)
	}
	assertCutoverEngagedLocked("before_breakglass_clear")
	if err := op.ForceClearRecoveryAdmin("FORCE_CLEAR_UNVERIFIED", BreakGlassOptions{
		Actor:        "test-admin",
		TransitionID: "tx-bg-guard",
	}); err != nil {
		t.Fatalf("ForceClearRecoveryAdmin: %v", err)
	}
	assertCutoverAbsent("after_breakglass_clear")
}

func TestEngageCutoverBarrier_FilterRejectFailureFallsBackFailClosed(t *testing.T) {
	iptSt := newFakeIptablesState()
	SetExtIptablesRunner(iptSt.runner)
	defer SetExtIptablesRunner(nil)
	InvalidateOutputRoutingCache()

	iptSt.mu.Lock()
	iptSt.failCmdSub = "-j REJECT --reject-with tcp-reset"
	iptSt.mu.Unlock()

	op := NewOperator(Options{ConfigDir: t.TempDir()})
	ctx := context.Background()
	if err := op.EngageCutoverBarrier(ctx); err != nil {
		t.Fatalf("EngageCutoverBarrier unexpected error on filter REJECT failure: %v", err)
	}

	iptSt.mu.Lock()
	mangleGuard := append([]string(nil), iptSt.mangleChain[cutoverGuardChain]...)
	filterFwd := append([]string(nil), iptSt.filterChains["FORWARD"]...)
	_, filterExists := iptSt.filterChains[cutoverGuardFwdChain]
	preGuard := append([]string(nil), iptSt.mangleChain[cutoverGuardPreChain]...)
	iptSt.mu.Unlock()

	if slices.Contains(filterFwd, "-j "+cutoverGuardFwdChain) || filterExists {
		t.Fatalf("expected failed %s cleaned up, filterFwd=%v exists=%v", cutoverGuardFwdChain, filterFwd, filterExists)
	}
	wantMangle := make([]string, len(cutoverGuardRules()))
	for i, sp := range cutoverGuardRules() {
		wantMangle[i] = strings.Join(sp, " ")
	}
	if !slices.Equal(mangleGuard, wantMangle) {
		t.Fatalf("expected mangle %s fail-closed without tcp RETURN when filter guard fails:\ngot:  %v\nwant: %v", cutoverGuardChain, mangleGuard, wantMangle)
	}
	if len(preGuard) == 0 {
		t.Fatalf("expected mangle PREROUTING %s installed even when filter guard falls back", cutoverGuardPreChain)
	}
	if err := op.ReleaseCutoverBarrier(ctx); err != nil {
		t.Fatalf("ReleaseCutoverBarrier: %v", err)
	}
}

func TestEngageCutoverBarrier_ConcurrentFlushRetryAndRollbackCleanup(t *testing.T) {
	iptSt := newFakeIptablesState()
	InvalidateOutputRoutingCache()

	var flushedOnce atomic.Bool
	var unloadedOnce atomic.Bool
	SetExtIptablesRunner(func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		if strings.Contains(cmd, "-t mangle -A "+cutoverGuardPreChain+" -i br+ -p tcp") && flushedOnce.CompareAndSwap(false, true) {
			iptSt.mu.Lock()
			delete(iptSt.mangleChain, cutoverGuardPreChain)
			iptSt.mu.Unlock()
			return []byte("iptables: No chain/target/match by that name."), errors.New("exit status 1 (iptables: No chain/target/match by that name.)")
		}
		if strings.Contains(cmd, "-t mangle -I PREROUTING 1 -j "+cutoverGuardPreChain) && unloadedOnce.CompareAndSwap(false, true) {
			iptSt.mu.Lock()
			delete(iptSt.mangleChain, cutoverGuardPreChain)
			iptSt.mu.Unlock()
			return []byte("iptables v1.4.21: Couldn't load target `AWGM_CUTOVER_GUARD_PRE':No such file or directory"), errors.New("exit status 2 (Couldn't load target `AWGM_CUTOVER_GUARD_PRE':No such file or directory)")
		}
		return iptSt.runner(ctx, args...)
	})
	defer SetExtIptablesRunner(nil)

	op := NewOperator(Options{ConfigDir: t.TempDir()})
	ctx := context.Background()
	if err := op.EngageCutoverBarrier(ctx); err != nil {
		t.Fatalf("EngageCutoverBarrier failed to recover from transient table flush: %v", err)
	}
	if !flushedOnce.Load() || !unloadedOnce.Load() {
		t.Fatalf("expected both transient flush errors to trigger: flushed=%v unloaded=%v", flushedOnce.Load(), unloadedOnce.Load())
	}
	iptSt.mu.Lock()
	preGot := len(iptSt.mangleChain[cutoverGuardPreChain])
	iptSt.mu.Unlock()
	if preGot != len(cutoverGuardPreRules()) {
		t.Fatalf("expected %d rules in %s after retry, got %d", len(cutoverGuardPreRules()), cutoverGuardPreChain, preGot)
	}

	_ = op.ReleaseCutoverBarrier(ctx)

	// Permanent failure on PRE must roll back already-created FWD/FORWARD guards cleanly.
	SetExtIptablesRunner(func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		if strings.Contains(cmd, cutoverGuardPreChain) && strings.Contains(cmd, "-A") {
			return nil, errors.New("permanent pre error")
		}
		return iptSt.runner(ctx, args...)
	})
	if err := op.EngageCutoverBarrier(ctx); err == nil {
		t.Fatal("expected EngageCutoverBarrier error on permanent PRE failure")
	}
	iptSt.mu.Lock()
	_, hasMangleFwd := iptSt.mangleChain[cutoverGuardChain]
	_, hasFilterFwd := iptSt.filterChains[cutoverGuardFwdChain]
	_, hasPre := iptSt.mangleChain[cutoverGuardPreChain]
	iptSt.mu.Unlock()
	if hasMangleFwd || hasFilterFwd || hasPre {
		t.Fatalf("expected partial cutover chains cleaned up on error, mangleFwd=%v filterFwd=%v pre=%v", hasMangleFwd, hasFilterFwd, hasPre)
	}
}

func TestPoolRejectAndFextChainTeardown_OwnershipVsManualStop(t *testing.T) {
	iptSt := newFakeIptablesState()
	SetExtIptablesRunner(iptSt.runner)
	defer SetExtIptablesRunner(nil)
	InvalidateOutputRoutingCache()

	iptSt.mu.Lock()
	iptSt.filterChains["FORWARD"] = []string{"-j FIRC_POOLREJECT", "-j OTHER_FILTER"}
	iptSt.filterChains["FIRC_POOLREJECT"] = []string{"-d 198.18.0.0/15 -j REJECT --reject-with icmp-port-unreachable"}
	iptSt.mangleChain["FORWARD"] = []string{"-j FEXT_MSS", "-j FOREIGN_JUMP"}
	iptSt.mangleChain["OUTPUT"] = []string{"-j FEXT_OUT"}
	iptSt.mangleChain["FEXT_MSS"] = []string{"-p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu"}
	iptSt.mangleChain["FEXT_OUT"] = []string{"-j FEXT_youtube", fircSaveMarkRule}
	iptSt.mangleChain["FEXT_youtube"] = []string{"-j MARK --set-xmark 0x40010000/0x40ff0000"}
	iptSt.mangleChain["FOREIGN_MANGLE"] = []string{"-j ACCEPT"}
	iptSt.natChains["PREROUTING"] = []string{"-j FEXT_DNS"}
	iptSt.natChains["OUTPUT"] = []string{"-j FEXT_NOUT"}
	iptSt.natChains["FEXT_DNS"] = []string{"-p udp --dport 53 -j REDIRECT"}
	iptSt.natChains["FEXT_NOUT"] = []string{"-j FEXT_DNAT"}
	iptSt.natChains["FEXT_DNAT"] = []string{"-d 198.18.0.10 -j DNAT --to-destination 1.1.1.1:443"}
	iptSt.natChains["FEXT_REDIR"] = []string{"-p tcp -j REDIRECT --to-ports 1234"}
	iptSt.mu.Unlock()

	op := NewOperator(Options{ConfigDir: t.TempDir()})
	op.SetLifecycleHooks(
		func(_ context.Context) error { return nil },
		func(_ context.Context) error { return nil },
	)

	ctx := context.Background()
	if err := op.StopByUser(ctx); err != nil {
		t.Fatalf("StopByUser failed: %v", err)
	}

	iptSt.mu.Lock()
	for ch := range iptSt.mangleChain {
		if strings.HasPrefix(ch, "FEXT_") {
			iptSt.mu.Unlock()
			t.Fatalf("StopByUser left stale mangle FEXT chain %q: %v", ch, iptSt.mangleChain)
		}
	}
	for ch := range iptSt.natChains {
		if strings.HasPrefix(ch, "FEXT_") {
			iptSt.mu.Unlock()
			t.Fatalf("StopByUser left stale nat FEXT chain %q: %v", ch, iptSt.natChains)
		}
	}
	if _, ok := iptSt.mangleChain["FOREIGN_MANGLE"]; !ok {
		iptSt.mu.Unlock()
		t.Fatalf("StopByUser altered FOREIGN_MANGLE: %v", iptSt.mangleChain)
	}
	if !slices.Contains(iptSt.filterChains["FORWARD"], "-j FIRC_POOLREJECT") {
		iptSt.mu.Unlock()
		t.Fatalf("StopByUser unexpectedly removed FIRC_POOLREJECT jump while FIRC selected: %v", iptSt.filterChains["FORWARD"])
	}
	if _, ok := iptSt.filterChains["FIRC_POOLREJECT"]; !ok {
		iptSt.mu.Unlock()
		t.Fatalf("StopByUser unexpectedly removed FIRC_POOLREJECT chain while FIRC selected")
	}
	iptSt.mu.Unlock()

	// Ownership handoff (or uninstall) MUST remove FIRC_POOLREJECT without disturbing OTHER_FILTER.
	op.CleanupPoolRejectBarrier(ctx)
	iptSt.mu.Lock()
	if slices.Contains(iptSt.filterChains["FORWARD"], "-j FIRC_POOLREJECT") {
		iptSt.mu.Unlock()
		t.Fatalf("CleanupPoolRejectBarrier left FIRC_POOLREJECT jump: %v", iptSt.filterChains["FORWARD"])
	}
	if !slices.Contains(iptSt.filterChains["FORWARD"], "-j OTHER_FILTER") {
		iptSt.mu.Unlock()
		t.Fatalf("CleanupPoolRejectBarrier removed unrelated filter jump: %v", iptSt.filterChains["FORWARD"])
	}
	if _, ok := iptSt.filterChains["FIRC_POOLREJECT"]; ok {
		iptSt.mu.Unlock()
		t.Fatalf("CleanupPoolRejectBarrier left FIRC_POOLREJECT chain")
	}
	iptSt.filterChains["FORWARD"] = []string{"-j FIRC_POOLREJECT"}
	iptSt.filterChains["FIRC_POOLREJECT"] = []string{"-d 198.18.0.0/15 -j REJECT"}
	iptSt.mu.Unlock()

	cleanupPoolRejectBarrierForRoot(ctx, t.TempDir())
	iptSt.mu.Lock()
	sandboxExists := false
	_, sandboxExists = iptSt.filterChains["FIRC_POOLREJECT"]
	sandboxJumped := slices.Contains(iptSt.filterChains["FORWARD"], "-j FIRC_POOLREJECT")
	iptSt.mu.Unlock()
	if !sandboxExists || !sandboxJumped {
		t.Fatalf("cleanupPoolRejectBarrierForRoot unexpectedly touched host iptables for non-root rootDir: exists=%v jumped=%v", sandboxExists, sandboxJumped)
	}

	cleanupPoolRejectBarrierForRoot(ctx, "/")
	iptSt.mu.Lock()
	_, stillExists := iptSt.filterChains["FIRC_POOLREJECT"]
	stillJumped := slices.Contains(iptSt.filterChains["FORWARD"], "-j FIRC_POOLREJECT")
	iptSt.mu.Unlock()
	if stillExists || stillJumped {
		t.Fatalf("cleanupPoolRejectBarrierForRoot left FIRC_POOLREJECT: exists=%v jumped=%v", stillExists, stillJumped)
	}
}

func TestSyncGroupsWithExt_KeenDNSCheckMode(t *testing.T) {
	singleOff := buildKeenDNSGroup([]string{"tunvless0"}, false)
	if singleOff.Check == nil || singleOff.Check.Mode != "none" {
		t.Fatalf("single-iface KeenDNS group (check off) should set Check.Mode=none, got %+v", singleOff.Check)
	}
	singleOn := buildKeenDNSGroup([]string{"tunvless0"}, true)
	if singleOn.Check == nil || singleOn.Check.Mode != "none" {
		t.Fatalf("single-iface KeenDNS group (check on) should set Check.Mode=none, got %+v", singleOn.Check)
	}

	// Default (CloudHealthCheck=false): multi-iface group uses standard fallback liveness (Check.Mode == "none").
	multiDefault := buildKeenDNSGroup([]string{"eth3", "tunvless0"}, false)
	if multiDefault.Check == nil || multiDefault.Check.Mode != "none" {
		t.Fatalf("default multi-iface KeenDNS group (CloudHealthCheck=false) must set Check.Mode=none, got %+v", multiDefault.Check)
	}

	// Opt-in (CloudHealthCheck=true): multi-iface group enables keendns check.
	multi := buildKeenDNSGroup([]string{"eth3", "tunvless0"}, true)
	if multi.Check == nil || multi.Check.Mode != "keendns" || multi.Check.TimeoutMs != 5000 {
		t.Fatalf("opt-in multi-iface KeenDNS group must set mode=keendns timeoutMs=5000, got %+v", multi.Check)
	}

	// Sync with CloudHealthCheck=false on group with Check.Mode=none or Check=nil should report changed=false.
	_, defaultChanged := syncGroupsWithExt([]Group{multiDefault}, ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{
			Enabled:    true,
			Interfaces: []string{"eth3", "tunvless0"},
		},
	})
	if defaultChanged {
		t.Fatal("expected syncGroupsWithExt to keep Check.Mode=none and report changed=false when CloudHealthCheck=false")
	}

	multiNilCheck := multiDefault
	multiNilCheck.Check = nil
	_, nilCheckChanged := syncGroupsWithExt([]Group{multiNilCheck}, ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{
			Enabled:    true,
			Interfaces: []string{"eth3", "tunvless0"},
		},
	})
	if nilCheckChanged {
		t.Fatal("expected syncGroupsWithExt to report changed=false when live group has Check=nil and CloudHealthCheck=false")
	}

	// Simulate enabling strict KeenDNS healthcheck on an existing 2-iface group.
	updated, changed := syncGroupsWithExt([]Group{multiDefault}, ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{
			Enabled:          true,
			CloudHealthCheck: true,
			Interfaces:       []string{"eth3", "tunvless0"},
		},
	})
	if !changed {
		t.Fatal("expected syncGroupsWithExt to mark changed when enabling CloudHealthCheck on multi-iface KeenDNS group")
	}
	if len(updated) != 1 || updated[0].Check == nil || updated[0].Check.Mode != "keendns" {
		t.Fatalf("unexpected upgraded KeenDNS group: %+v", updated)
	}

	// Second sync with identical CloudHealthCheck=true should report changed=false.
	_, changedAgain := syncGroupsWithExt(updated, ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{
			Enabled:          true,
			CloudHealthCheck: true,
			Interfaces:       []string{"eth3", "tunvless0"},
		},
	})
	if changedAgain {
		t.Fatal("expected idempotent syncGroupsWithExt to report changed=false when keendns check already present")
	}

	// Disabling CloudHealthCheck sets Check.Mode="none", marks changed=true, and marshals explicit check field.
	downgraded, downgradedChanged := syncGroupsWithExt(updated, ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{
			Enabled:          true,
			CloudHealthCheck: false,
			Interfaces:       []string{"eth3", "tunvless0"},
		},
	})
	if !downgradedChanged || len(downgraded) != 1 || downgraded[0].Check == nil || downgraded[0].Check.Mode != "none" {
		t.Fatalf("expected disabling CloudHealthCheck to set Check.Mode=none and report changed=true, changed=%v group=%+v", downgradedChanged, downgraded)
	}
	rawDowngraded, err := json.Marshal(downgraded[0])
	if err != nil {
		t.Fatalf("json.Marshal(downgraded[0]): %v", err)
	}
	if !strings.Contains(string(rawDowngraded), `"check":{"mode":"none"}`) {
		t.Fatalf("expected marshaled downgraded KeenDNS group to contain explicit \"check\":{\"mode\":\"none\"}, got %s", string(rawDowngraded))
	}

	// Verify my.keenetic.net and my.netcraze.net are excluded from c100d001 namespace rules
	// and legacy keenetic.net / netcraze.net rules migrate cleanly.
	var hasLegacyNet, hasCloudNet bool
	for _, r := range multi.Rules {
		if r.Type == "namespace" && (r.Rule == "keenetic.net" || r.Rule == "netcraze.net") {
			hasLegacyNet = true
		}
		if r.Type == "namespace" && (r.Rule == "cloud.keenetic.net" || r.Rule == "cloud.netcraze.net") {
			hasCloudNet = true
		}
	}
	if hasLegacyNet || !hasCloudNet {
		t.Fatalf("c100d001 rules must use cloud.keenetic.net/cloud.netcraze.net instead of broad *.net: legacy=%v cloud=%v", hasLegacyNet, hasCloudNet)
	}
	legacyGroup := buildKeenDNSGroup([]string{"eth3", "tunvless0"}, true)
	for i := range legacyGroup.Rules {
		if legacyGroup.Rules[i].Rule == "cloud.keenetic.net" {
			legacyGroup.Rules[i].Rule = "keenetic.net"
		}
		if legacyGroup.Rules[i].Rule == "cloud.netcraze.net" {
			legacyGroup.Rules[i].Rule = "netcraze.net"
		}
	}
	migrated, migratedChanged := syncGroupsWithExt([]Group{legacyGroup}, ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{
			Enabled:          true,
			CloudHealthCheck: true,
			Interfaces:       []string{"eth3", "tunvless0"},
		},
	})
	if !migratedChanged || !keenDNSRulesEqual(migrated[0].Rules, multi.Rules) {
		t.Fatalf("expected syncGroupsWithExt to migrate legacy keenetic.net/netcraze.net rules, changed=%v", migratedChanged)
	}

	// Explicit user-configured url check on c100d001 is preserved when CloudHealthCheck is false.
	explicitURLGroup := buildKeenDNSGroup([]string{"eth3", "tunvless0"}, false)
	explicitURLGroup.Check = &GroupCheck{Mode: "url", URL: "https://example.test/204"}
	preserved, preservedChanged := syncGroupsWithExt([]Group{explicitURLGroup}, ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{
			Enabled:          true,
			CloudHealthCheck: false,
			Interfaces:       []string{"eth3", "tunvless0"},
		},
	})
	if preservedChanged {
		t.Fatalf("expected syncGroupsWithExt to preserve explicit Check.Mode=url without marking changed")
	}
	if preserved[0].Check == nil || preserved[0].Check.Mode != "url" || preserved[0].Check.URL != "https://example.test/204" {
		t.Fatalf("expected explicit url check preserved, got %+v", preserved[0].Check)
	}
}

func TestSyncGroupsWithExt_ProxyGroupCheckNormalizationAndPropagation(t *testing.T) {
	norm := normalizeExtConfig(ExtConfig{
		ProxyGroups: []ProxyGroup{
			{
				ID:         "legacy",
				Name:       "Legacy",
				Interfaces: []string{"eth3", "tunvless0"},
			},
			{
				ID:         "single",
				Name:       "Single",
				Interfaces: []string{"tunvless0"},
				Check:      &GroupCheck{Mode: "url", URL: "https://example.test/204", IntervalSec: 30},
			},
			{
				ID:         "kd",
				Name:       "KD",
				Interfaces: []string{"eth3", "tunvless0"},
				Check:      &GroupCheck{Mode: "keendns", URL: "https://ignored.example"},
			},
			{
				ID:         "off",
				Name:       "Off",
				Interfaces: []string{"eth3", "tunvless1"},
				Check:      &GroupCheck{Mode: "unknown", URL: "https://ignored.example", TimeoutMs: 1234},
			},
		},
	})
	if norm.ProxyGroups[0].Check != nil {
		t.Fatalf("omitted PG check must remain nil, got %+v", norm.ProxyGroups[0].Check)
	}
	if norm.ProxyGroups[1].Check == nil || norm.ProxyGroups[1].Check.Mode != "none" || norm.ProxyGroups[1].Check.URL != "" || norm.ProxyGroups[1].Check.IntervalSec != 0 {
		t.Fatalf("single-iface PG check must normalize to bare mode=none, got %+v", norm.ProxyGroups[1].Check)
	}
	if norm.ProxyGroups[2].Check == nil || norm.ProxyGroups[2].Check.Mode != "keendns" || norm.ProxyGroups[2].Check.TimeoutMs != 5000 || norm.ProxyGroups[2].Check.URL != "" {
		t.Fatalf("keendns PG check must set TimeoutMs=5000 and strip URL, got %+v", norm.ProxyGroups[2].Check)
	}
	if off := norm.ProxyGroups[3].Check; off == nil || off.Mode != "none" || off.URL != "" || off.TimeoutMs != 1234 {
		t.Fatalf("unknown PG check mode must become none and keep thresholds, got %+v", off)
	}

	// KeenDNS inherits PG check when CloudHealthCheck is false, while CloudHealthCheck=true still enforces keendns.
	kdFromPGExt := ExtConfig{
		ProxyGroups: []ProxyGroup{{
			ID:         "pg_kd",
			Name:       "KeenDNS Chain",
			Interfaces: []string{"eth3", "tunvless0"},
			Check:      &GroupCheck{Mode: "url", URL: "https://cloud-probe.example/204"},
		}},
		KeenDNSCloud: KeenDNSCloudConfig{
			Enabled:          true,
			CloudHealthCheck: false,
			ProxyGroupID:     "pg_kd",
		},
	}
	groups, changed := syncGroupsWithExt(nil, kdFromPGExt)
	if !changed || len(groups) != 1 || groups[0].Check == nil || groups[0].Check.Mode != "url" || groups[0].Check.URL != "https://cloud-probe.example/204" {
		t.Fatalf("KeenDNS must inherit PG url check when CloudHealthCheck=false, got changed=%v groups=%+v", changed, groups)
	}
	kdFromPGExt.KeenDNSCloud.CloudHealthCheck = true
	groups, changed = syncGroupsWithExt(groups, kdFromPGExt)
	if !changed || groups[0].Check == nil || groups[0].Check.Mode != "keendns" || groups[0].Check.TimeoutMs != 5000 {
		t.Fatalf("CloudHealthCheck=true must enforce mode=keendns, got changed=%v check=%+v", changed, groups[0].Check)
	}

	// Regular user routing groups inherit the linked PG.Check, while legacy nil Check and unrelated chains stay untouched.
	matchedUserGroup := Group{
		ID:        "aa11bb22",
		Name:      "UserFallback",
		Interface: GroupInterface{"qmi_br0", "tunvless2"},
		Enable:    true,
		Check:     &GroupCheck{Mode: "none"},
	}
	legacyPGGroup := Group{
		ID:        "bb22cc33",
		Name:      "LegacyPGMatch",
		Interface: GroupInterface{"eth3", "tunvless4"},
		Enable:    true,
		Check:     &GroupCheck{Mode: "none"},
	}
	unrelatedGroup := Group{
		ID:        "cc33dd44",
		Name:      "Unrelated",
		Interface: GroupInterface{"tunvless5", "tunvless6"},
		Enable:    true,
		Check:     &GroupCheck{Mode: "none"},
	}
	propExt := ExtConfig{
		GroupLinks: map[string]string{"aa11bb22": "pg_url", "bb22cc33": "pg_legacy"},
		ProxyGroups: []ProxyGroup{
			{
				ID:         "pg_url",
				Name:       "Dacha Fallback",
				Interfaces: []string{"qmi_br0", "tunvless2"},
				Check:      &GroupCheck{Mode: "url", URL: "https://cp.cloudflare.com/generate_204"},
			},
			{
				ID:         "pg_legacy",
				Name:       "Legacy Nil Check",
				Interfaces: []string{"eth3", "tunvless4"},
			},
		},
	}
	synced, changed := syncGroupsWithExt([]Group{matchedUserGroup, legacyPGGroup, unrelatedGroup}, propExt)
	if !changed || len(synced) != 3 {
		t.Fatalf("expected user group matching PG to update, changed=%v groups=%+v", changed, synced)
	}
	if synced[0].Check == nil || synced[0].Check.Mode != "url" || synced[0].Check.URL != "https://cp.cloudflare.com/generate_204" {
		t.Fatalf("matched user group did not receive PG check: %+v", synced[0].Check)
	}
	if synced[1].Check == nil || synced[1].Check.Mode != "none" {
		t.Fatalf("legacy PG (Check==nil) must leave group check untouched, got %+v", synced[1].Check)
	}
	if synced[2].Check == nil || synced[2].Check.Mode != "none" {
		t.Fatalf("unrelated group check must remain untouched, got %+v", synced[2].Check)
	}
	if _, changedAgain := syncGroupsWithExt(synced, propExt); changedAgain {
		t.Fatal("repeated syncGroupsWithExt after PG propagation must be idempotent")
	}
}

// Вкладка со старым фронтом не присылает check: сохранённая проверка остаётся.
func TestStageExtConfigOffline_OmittedProxyGroupCheckKeepsSaved(t *testing.T) {
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	saved := GroupCheck{Mode: "url", URL: "https://probe.example/204", IntervalSec: 30}
	if err := op.SaveExtConfig(ExtConfig{ProxyGroups: []ProxyGroup{
		{ID: "pg1", Name: "Old", Interfaces: []string{"eth3", "tunvless0"}, Check: &saved},
	}}); err != nil {
		t.Fatal(err)
	}

	ext, err := op.StageExtConfigOffline(context.Background(), ExtConfig{ProxyGroups: []ProxyGroup{
		{ID: "pg1", Name: "Renamed", Interfaces: []string{"eth3", "tunvless0"}},
		{ID: "pg2", Name: "New", Interfaces: []string{"eth3", "tunvless1"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ext.ProxyGroups[0]; got.Name != "Renamed" || got.Check == nil || *got.Check != saved {
		t.Fatalf("omitted check must keep the saved one, got %+v check=%+v", got, got.Check)
	}
	if ext.ProxyGroups[1].Check != nil {
		t.Fatalf("new proxy group must stay without check, got %+v", ext.ProxyGroups[1].Check)
	}

	ext, err = op.StageExtConfigOffline(context.Background(), ExtConfig{ProxyGroups: []ProxyGroup{
		{ID: "pg1", Name: "Renamed", Interfaces: []string{"eth3", "tunvless0"}, Check: &GroupCheck{Mode: "none"}},
	}})
	if err != nil || ext.ProxyGroups[0].Check == nil || ext.ProxyGroups[0].Check.Mode != "none" {
		t.Fatalf("explicit none must replace the saved check, got %+v err=%v", ext.ProxyGroups, err)
	}
}

func TestReconcileExtNetfilterStrictRetriesTransientFlush(t *testing.T) {
	resetKeenCloudTestHooks(t)
	InvalidateOutputRoutingCache()

	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	iptSt.mu.Lock()
	iptSt.mangleChain["FIRC_c100d001"] = []string{"-d 198.18.0.0/24 -j MARK --set-xmark 0x40010000/0x40ff0000"}
	iptSt.mu.Unlock()
	ipsetSt.mu.Lock()
	ipsetSt.sets[FextCloudIpsetName] = map[string]bool{"192.0.2.10/32": true}
	ipsetSt.mu.Unlock()

	var flushedOnce atomic.Bool
	SetExtIptablesRunner(func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		// Simulate NDM flushing mangle right after FEXT_OUT rules are written and before verifyChainExactOrder.
		if strings.Contains(cmd, "-t mangle -A OUTPUT -j FEXT_OUT") && flushedOnce.CompareAndSwap(false, true) {
			iptSt.mu.Lock()
			delete(iptSt.mangleChain, "FEXT_OUT")
			iptSt.mu.Unlock()
			return []byte("iptables v1.4.21: Couldn't load target `FEXT_OUT':No such file or directory"), errors.New("exit status 2")
		}
		return iptSt.runner(ctx, args...)
	})
	SetExtIpsetRunner(ipsetSt.runner)

	op := NewOperator(Options{ConfigDir: t.TempDir()})
	var groupsMu sync.Mutex
	groups := []Group{{ID: KeenDNSGroupID, Name: "KeenDNS", Interface: []string{"eth3", "tunvless1"}, Enable: true}}
	srv := startMockRunningFircServer(t, op, &groupsMu, &groups, iptSt)
	defer srv.Close()

	extCfg := ExtConfig{KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"eth3", "tunvless1"}}}
	if err := op.ReconcileExtNetfilterStrict(context.Background(), extCfg, groups); err != nil {
		t.Fatalf("ReconcileExtNetfilterStrict failed after transient NDM flush: %v", err)
	}
	if !flushedOnce.Load() {
		t.Fatal("expected transient FEXT_OUT flush error to be injected")
	}
	iptSt.mu.Lock()
	defer iptSt.mu.Unlock()
	if _, ok := iptSt.mangleChain["FEXT_OUT"]; !ok || !slices.Contains(iptSt.mangleChain["OUTPUT"], "-j FEXT_OUT") {
		t.Fatalf("expected FEXT_OUT and OUTPUT jump restored after retry: out=%v all=%v", iptSt.mangleChain["OUTPUT"], iptSt.mangleChain)
	}
}

func TestStopSweepsOrphanFircIpsetsAndReleasesFextFirst(t *testing.T) {
	resetKeenCloudTestHooks(t)
	InvalidateOutputRoutingCache()

	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	iptSt.mu.Lock()
	iptSt.mangleChain["OUTPUT"] = []string{"-j FEXT_OUT"}
	iptSt.mangleChain["FEXT_OUT"] = []string{"-j FEXT_c100d001"}
	iptSt.mangleChain["FEXT_c100d001"] = []string{"-m set --match-set FIRC_c100d001S4 dst -j MARK --set-xmark 0x40010000/0x40ff0000"}
	iptSt.mu.Unlock()
	ipsetSt.mu.Lock()
	ipsetSt.sets["FIRC_c100d001S4"] = map[string]bool{"95.213.212.50": true}
	ipsetSt.sets["FIRC_orphanS6"] = map[string]bool{"192.0.2.1": true}
	ipsetSt.sets["FOREIGN_SET"] = map[string]bool{"198.51.100.99": true}
	ipsetSt.mu.Unlock()

	SetExtIptablesRunner(iptSt.runner)
	SetExtIpsetRunner(ipsetSt.runner)

	op := NewOperator(Options{ConfigDir: t.TempDir()})
	var stopDestroyErr error
	op.SetLifecycleHooks(
		func(_ context.Context) error { return nil },
		func(ctx context.Context) error {
			// When fircd shuts down, FEXT_c100d001 must already be gone so destroying FIRC_c100d001S4 does not fail EBUSY.
			_, stopDestroyErr = runExtIpset(ctx, "", "destroy", "FIRC_c100d001S4")
			return nil
		},
	)

	if err := op.Stop(context.Background()); err != nil {
		t.Fatalf("Operator.Stop failed: %v", err)
	}
	if stopDestroyErr != nil {
		t.Fatalf("fircd stopHook failed destroying FIRC_c100d001S4 (FEXT reference still held): %v", stopDestroyErr)
	}
	if ipsetSt.hasSet("FIRC_c100d001S4") {
		t.Fatal("expected FIRC_c100d001S4 destroyed")
	}
	if ipsetSt.hasSet("FIRC_orphanS6") {
		t.Fatal("expected orphan FIRC_orphanS6 swept after stop")
	}
	if !ipsetSt.hasSet("FOREIGN_SET") {
		t.Fatal("FOREIGN_SET must not be modified by FIRC stop cleanup")
	}
}

func TestKeenDNSCloudAgentResetOnSwitch(t *testing.T) {
	resetKeenCloudTestHooks(t)
	now := time.Unix(1700000000, 0)
	setExtCloudNow(func() time.Time { return now })
	defer setExtCloudNow(nil)

	op := NewOperator(Options{ConfigDir: t.TempDir()})
	var resets int
	op.resetCloudAgentFn = func(context.Context) (int, error) {
		resets++
		return 1, nil
	}

	ctx := context.Background()
	makeGroups := func(active string) []Group {
		return []Group{{ID: KeenDNSGroupID, Enable: true, Active: active}}
	}

	// 1. First observation records baseline without resetting coalagent.
	op.maybeResetCloudAgentOnKeenDNSSwitch(ctx, true, makeGroups("eth3"))
	if resets != 0 {
		t.Fatalf("first observation triggered reset (%d)", resets)
	}

	// 2. Empty Active (or transient group miss) preserves previous baseline without reset.
	op.maybeResetCloudAgentOnKeenDNSSwitch(ctx, true, makeGroups(""))
	op.maybeResetCloudAgentOnKeenDNSSwitch(ctx, true, makeGroups("eth3"))
	if resets != 0 {
		t.Fatalf("empty active caused spurious reset: %d", resets)
	}

	// 3. Failover eth3 -> tunvless1 triggers 1 reset.
	op.maybeResetCloudAgentOnKeenDNSSwitch(ctx, true, makeGroups("tunvless1"))
	if resets != 1 {
		t.Fatalf("failover expected 1 reset, got %d", resets)
	}

	// 4. Failback inside 30s cooldown is deferred and fires once cooldown expires.
	now = now.Add(10 * time.Second)
	op.maybeResetCloudAgentOnKeenDNSSwitch(ctx, true, makeGroups("eth3"))
	if resets != 1 {
		t.Fatalf("switch inside 30s cooldown triggered early reset: %d", resets)
	}
	now = now.Add(21 * time.Second)
	op.maybeResetCloudAgentOnKeenDNSSwitch(ctx, true, makeGroups("eth3"))
	if resets != 2 {
		t.Fatalf("deferred switch after cooldown expected 2 resets, got %d", resets)
	}

	// 5. Round-trip eth3 -> tunvless1 -> eth3 completely within cooldown triggers zero extra resets.
	now = now.Add(5 * time.Second)
	op.maybeResetCloudAgentOnKeenDNSSwitch(ctx, true, makeGroups("tunvless1"))
	now = now.Add(5 * time.Second)
	op.maybeResetCloudAgentOnKeenDNSSwitch(ctx, true, makeGroups("eth3"))
	now = now.Add(25 * time.Second)
	op.maybeResetCloudAgentOnKeenDNSSwitch(ctx, true, makeGroups("eth3"))
	if resets != 2 {
		t.Fatalf("round-trip inside cooldown triggered unexpected reset: %d", resets)
	}

	// 6. Disabling wantKeenDNS clears baseline so re-enable first sample does not reset.
	op.maybeResetCloudAgentOnKeenDNSSwitch(ctx, false, nil)
	now = now.Add(40 * time.Second)
	op.maybeResetCloudAgentOnKeenDNSSwitch(ctx, true, makeGroups("tunvless1"))
	if resets != 2 {
		t.Fatalf("re-enable baseline sample triggered reset: %d", resets)
	}
}

func TestProxyGroupCheckIntervalSecPreservedAndPropagated(t *testing.T) {
	ext := normalizeExtConfig(ExtConfig{
		ProxyGroups: []ProxyGroup{
			{
				ID:         "pg1",
				Name:       "Primary",
				Interfaces: []string{"tunvless0", "eth3"},
				Check: &GroupCheck{
					Mode:             "url",
					URL:              "https://example.com/204",
					IntervalSec:      9999,
					TimeoutMs:        70000,
					FailThreshold:    25,
					RecoverThreshold: 25,
				},
			},
			{
				ID:         "pg2",
				Name:       "InheritGlobal",
				Interfaces: []string{"tunvless1", "eth3"},
				Check: &GroupCheck{
					Mode:        "url",
					URL:         "",
					IntervalSec: 0,
				},
			},
		},
		GlobalTarget: "pg:pg1",
		KeenDNSCloud: KeenDNSCloudConfig{
			Enabled:          true,
			ProxyGroupID:     "pg1",
			CloudHealthCheck: true,
		},
		GroupLinks: map[string]string{
			"abcd0001": "pg1",
		},
	})

	if ext.ProxyGroups[0].Check == nil || ext.ProxyGroups[0].Check.IntervalSec != 3600 {
		t.Fatalf("expected IntervalSec clamped to 3600, got %+v", ext.ProxyGroups[0].Check)
	}
	if ext.ProxyGroups[0].Check.TimeoutMs != 60000 || ext.ProxyGroups[0].Check.FailThreshold != 20 || ext.ProxyGroups[0].Check.RecoverThreshold != 20 {
		t.Fatalf("expected TimeoutMs/thresholds clamped, got %+v", ext.ProxyGroups[0].Check)
	}
	if ext.ProxyGroups[1].Check == nil || ext.ProxyGroups[1].Check.IntervalSec != 0 {
		t.Fatalf("expected zero IntervalSec preserved, got %+v", ext.ProxyGroups[1].Check)
	}

	ext.ProxyGroups[0].Check.IntervalSec = 120
	groups := []Group{
		{
			ID:        "abcd0001",
			Name:      "Rule",
			Interface: GroupInterface{"tunvless0", "eth3"},
			Enable:    true,
			Check:     &GroupCheck{Mode: "url"},
		},
	}
	synced, changed := syncGroupsWithExt(groups, ext)
	if !changed {
		t.Fatal("expected syncGroupsWithExt to mark groups changed")
	}
	byID := make(map[string]Group)
	for _, g := range synced {
		byID[g.ID] = g
	}
	if got := byID["abcd0001"].Check; got == nil || got.IntervalSec != 120 || got.URL != "https://example.com/204" {
		t.Fatalf("unexpected synced rule group check: %+v", got)
	}
	if got := byID[globalGroupID].Check; got == nil || got.IntervalSec != 120 {
		t.Fatalf("unexpected synced global group check: %+v", got)
	}
	if got := byID[KeenDNSGroupID].Check; got == nil || got.Mode != "keendns" || got.IntervalSec != 120 {
		t.Fatalf("unexpected synced KeenDNS group check: %+v", got)
	}

	// A second pass over already synced groups must be a no-op (no reconcile churn).
	if _, again := syncGroupsWithExt(synced, ext); again {
		t.Fatal("expected second syncGroupsWithExt pass to report no change")
	}
}

func TestGroupCheckBoundsClampedForEveryGroup(t *testing.T) {
	low := normalizeProxyGroupCheck(&GroupCheck{Mode: "url", TimeoutMs: 50}, 2)
	if low.TimeoutMs != 100 {
		t.Fatalf("expected TimeoutMs 50 raised to 100, got %d", low.TimeoutMs)
	}
	unset := normalizeProxyGroupCheck(&GroupCheck{Mode: "url"}, 2)
	if unset.IntervalSec != 0 || unset.TimeoutMs != 0 || unset.FailThreshold != 0 || unset.RecoverThreshold != 0 {
		t.Fatalf("expected zero (unset) fields to stay zero, got %+v", unset)
	}

	// A multi-interface group with no matching proxy group still never reaches fircd out of range.
	groups := []Group{{
		ID:        "abcd0002",
		Name:      "Manual",
		Interface: GroupInterface{"tunvless7", "eth3"},
		Enable:    true,
		Check:     &GroupCheck{Mode: "url", IntervalSec: 7200, FailThreshold: 99},
	}}
	synced, changed := syncGroupsWithExt(groups, normalizeExtConfig(ExtConfig{}))
	if !changed {
		t.Fatal("expected out-of-range manual check to be clamped")
	}
	if got := synced[0].Check; got.IntervalSec != 3600 || got.FailThreshold != 20 {
		t.Fatalf("unexpected clamped manual check: %+v", got)
	}
}

func TestKeenDNSCheckFollowsProxyGroupWithoutCloudHealthCheck(t *testing.T) {
	ext := normalizeExtConfig(ExtConfig{
		ProxyGroups: []ProxyGroup{{
			ID:         "pg1",
			Name:       "Primary",
			Interfaces: []string{"tunvless0", "eth3"},
			Check:      &GroupCheck{Mode: "url", URL: "https://example.com/204", IntervalSec: 45, FailThreshold: 3},
		}},
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, ProxyGroupID: "pg1"},
	})
	got := resolveKeenDNSCheck(ext, []string{"tunvless0", "eth3"}, nil)
	if got.Mode != "url" || got.IntervalSec != 45 || got.FailThreshold != 3 || got.URL != "https://example.com/204" {
		t.Fatalf("expected KeenDNS check to follow the proxy group, got %+v", got)
	}
}
