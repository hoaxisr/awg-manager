package mihomo

import (
	"encoding/json"
	"reflect"
	"sort"

	"gopkg.in/yaml.v3"
)

// ChangeKind classifies what part of the state tree was mutated.
type ChangeKind string

const (
	ChangeKindUnknown             ChangeKind = "unknown"
	ChangeKindRuleOnly            ChangeKind = "rule_only"
	ChangeKindRuleProviderContent ChangeKind = "rule_provider_content"
	ChangeKindProxyGraph          ChangeKind = "proxy_graph"
	ChangeKindListenerTopology    ChangeKind = "listener_topology"
	ChangeKindEngineMode          ChangeKind = "engine_mode"
)

// ApplyPath represents the deployment path chosen by ApplyCoordinator.
type ApplyPath string

const (
	ApplyPathHotReload   ApplyPath = "hot_reload"
	ApplyPathFullRestart ApplyPath = "full_restart"
	ApplyPathDraftOnly   ApplyPath = "draft_only"
	ApplyPathRollback    ApplyPath = "rollback"
)

type proxyDiffDTO struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Protocol         string                 `json:"protocol"`
	Transport        string                 `json:"transport"`
	EnginePreference string                 `json:"enginePreference"`
	SelectedEngine   string                 `json:"selectedEngine"`
	SourceID         string                 `json:"sourceId,omitempty"`
	RawURI           string                 `json:"rawUri,omitempty"`
	NativeConfig     map[string]interface{} `json:"nativeConfig"`
	Enabled          bool                   `json:"enabled"`
	Bridge           interface{}            `json:"bridge,omitempty"`
}

type subscriptionDiffDTO struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	URL              string              `json:"url,omitempty"`
	Inline           string              `json:"inline,omitempty"`
	Format           string              `json:"format"`
	EnginePreference string              `json:"enginePreference"`
	ProviderName     string              `json:"providerName,omitempty"`
	GroupName        string              `json:"groupName,omitempty"`
	Headers          map[string][]string `json:"headers,omitempty"`
	RefreshHours     int                 `json:"refreshHours"`
	Enabled          bool                `json:"enabled"`
	Mode             string              `json:"mode,omitempty"`
	TestURL          string              `json:"testUrl,omitempty"`
	TestInterval     int                 `json:"testInterval,omitempty"`
	TestTolerance    int                 `json:"testTolerance,omitempty"`
	FilterInclude    string              `json:"filterInclude,omitempty"`
	FilterExclude    string              `json:"filterExclude,omitempty"`
	BindInterface    string              `json:"bindInterface,omitempty"`
	Bridge           interface{}         `json:"bridge,omitempty"`
}

type groupDiffDTO struct {
	ID                  string      `json:"id"`
	Name                string      `json:"name"`
	Type                string      `json:"type"`
	URL                 string      `json:"url,omitempty"`
	Interval            int         `json:"interval,omitempty"`
	Strategy            string      `json:"strategy,omitempty"`
	Members             []string    `json:"members,omitempty"`
	IncludeAllProxies   bool        `json:"includeAllProxies,omitempty"`
	IncludeAllProviders bool        `json:"includeAllProviders,omitempty"`
	Use                 []string    `json:"use,omitempty"`
	Filter              string      `json:"filter,omitempty"`
	ExcludeFilter       string      `json:"excludeFilter,omitempty"`
	ExcludeType         string      `json:"excludeType,omitempty"`
	ExpectedStatus      string      `json:"expectedStatus,omitempty"`
	MaxFailedTimes      int         `json:"maxFailedTimes,omitempty"`
	Timeout             int         `json:"timeout,omitempty"`
	Lazy                bool        `json:"lazy,omitempty"`
	DisableUDP          bool        `json:"disableUDP,omitempty"`
	Tolerance           int         `json:"tolerance,omitempty"`
	BindInterface       string      `json:"bindInterface,omitempty"`
	Bridge              interface{} `json:"bridge,omitempty"`
}

type ruleDiffDTO struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Payload   string `json:"payload,omitempty"`
	Outbound  string `json:"outbound"`
	NoResolve bool   `json:"noResolve,omitempty"`
	Enabled   bool   `json:"enabled"`
}

type ruleProviderDiffDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Behavior   string `json:"behavior"`
	URL        string `json:"url,omitempty"`
	Path       string `json:"path,omitempty"`
	Interval   int    `json:"interval,omitempty"`
	Format     string `json:"format,omitempty"`
	Payload    string `json:"payload,omitempty"`
	Proxy      string `json:"proxy,omitempty"`
	AutoReload bool   `json:"autoReload,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type storeSnapshotDiffDTO struct {
	Version              int                   `json:"version"`
	Proxies              []proxyDiffDTO        `json:"proxies"`
	Subscriptions        []subscriptionDiffDTO `json:"subscriptions"`
	Groups               []groupDiffDTO        `json:"groups"`
	Rules                []ruleDiffDTO         `json:"rules"`
	RuleProviders        []ruleProviderDiffDTO `json:"ruleProviders"`
	LegacyGroupsImported bool                  `json:"legacyGroupsImported"`
	LegacyRulesImported  bool                  `json:"legacyRulesImported"`
}

// ClassifyStoreSnapshotDiff compares two JSON-serialized store snapshots and determines
// what categories of data changed, ignoring volatile runtime timestamps.
func ClassifyStoreSnapshotDiff(preBytes, postBytes []byte) (ChangeKind, error) {
	if len(preBytes) == 0 || len(postBytes) == 0 {
		return ChangeKindUnknown, nil
	}

	var pre, post storeSnapshotDiffDTO
	if err := json.Unmarshal(preBytes, &pre); err != nil {
		return ChangeKindUnknown, err
	}
	if err := json.Unmarshal(postBytes, &post); err != nil {
		return ChangeKindUnknown, err
	}

	if pre.Version != post.Version ||
		pre.LegacyGroupsImported != post.LegacyGroupsImported ||
		pre.LegacyRulesImported != post.LegacyRulesImported {
		return ChangeKindUnknown, nil
	}

	// 1. Check Proxies (order-independent)
	if !proxiesEqual(pre.Proxies, post.Proxies) {
		return ChangeKindProxyGraph, nil
	}

	// 2. Check Subscriptions (order-independent)
	if !subscriptionsEqual(pre.Subscriptions, post.Subscriptions) {
		return ChangeKindProxyGraph, nil
	}

	// 3. Check Groups (order-independent)
	if !groupsEqual(pre.Groups, post.Groups) {
		return ChangeKindProxyGraph, nil
	}

	// 4. Check RuleProviders (order-independent)
	if !ruleProvidersEqual(pre.RuleProviders, post.RuleProviders) {
		return ChangeKindRuleProviderContent, nil
	}

	// 5. Check Rules (EXACT ORDER MATTERS)
	if !rulesEqual(pre.Rules, post.Rules) {
		return ChangeKindRuleOnly, nil
	}

	return ChangeKindUnknown, nil
}

// ClassifyMutation verifies that the entire system mutation is strictly RuleOnly
// before permitting the hot reload fast path.
func ClassifyMutation(
	preSnapBytes, postSnapBytes []byte,
	activeConfigBytes, candidateConfigBytes []byte,
	prevRecord *AppliedGenerationRecord,
	compileResult *CompileResult,
	operator MihomoOperator,
	targetBridgesDigest, previousBridgesDigest string,
) ChangeKind {
	if compileResult == nil {
		return ChangeKindUnknown
	}
	if compileResult.Mode == RuntimeOff {
		return ChangeKindEngineMode
	}
	if prevRecord != nil && compileResult.Mode != prevRecord.RuntimeMode {
		return ChangeKindEngineMode
	}
	if prevRecord != nil && !ListenersEqual(prevRecord.AppliedListeners, compileResult.RequiredListeners) {
		return ChangeKindListenerTopology
	}
	if targetBridgesDigest != previousBridgesDigest {
		return ChangeKindUnknown
	}
	if operator == nil {
		return ChangeKindUnknown
	}
	running, pid := operator.IsRunning()
	if !running || pid <= 0 {
		return ChangeKindUnknown
	}

	storeKind, err := ClassifyStoreSnapshotDiff(preSnapBytes, postSnapBytes)
	if err != nil {
		return ChangeKindUnknown
	}
	if storeKind == ChangeKindRuleOnly && !configsEqualExceptRules(activeConfigBytes, candidateConfigBytes) {
		return ChangeKindUnknown
	}
	return storeKind
}

// configsEqualExceptRules is deliberately fail-closed. It compares the complete
// YAML documents after removing only the top-level rules field, so additions of
// fields unknown to the typed Config model cannot accidentally enter fast path.
func configsEqualExceptRules(active, candidate []byte) bool {
	if len(active) == 0 || len(candidate) == 0 {
		return false
	}
	var a, b map[string]interface{}
	if err := yaml.Unmarshal(active, &a); err != nil {
		return false
	}
	if err := yaml.Unmarshal(candidate, &b); err != nil {
		return false
	}
	delete(a, "rules")
	delete(b, "rules")
	return reflect.DeepEqual(a, b)
}

func proxiesEqual(a, b []proxyDiffDTO) bool {
	if len(a) != len(b) {
		return false
	}
	cA := append([]proxyDiffDTO(nil), a...)
	cB := append([]proxyDiffDTO(nil), b...)
	sort.Slice(cA, func(i, j int) bool { return cA[i].ID < cA[j].ID })
	sort.Slice(cB, func(i, j int) bool { return cB[i].ID < cB[j].ID })
	return reflect.DeepEqual(cA, cB)
}

func subscriptionsEqual(a, b []subscriptionDiffDTO) bool {
	if len(a) != len(b) {
		return false
	}
	cA := append([]subscriptionDiffDTO(nil), a...)
	cB := append([]subscriptionDiffDTO(nil), b...)
	sort.Slice(cA, func(i, j int) bool { return cA[i].ID < cA[j].ID })
	sort.Slice(cB, func(i, j int) bool { return cB[i].ID < cB[j].ID })
	return reflect.DeepEqual(cA, cB)
}

func groupsEqual(a, b []groupDiffDTO) bool {
	if len(a) != len(b) {
		return false
	}
	cA := append([]groupDiffDTO(nil), a...)
	cB := append([]groupDiffDTO(nil), b...)
	sort.Slice(cA, func(i, j int) bool { return cA[i].ID < cA[j].ID })
	sort.Slice(cB, func(i, j int) bool { return cB[i].ID < cB[j].ID })
	return reflect.DeepEqual(cA, cB)
}

func ruleProvidersEqual(a, b []ruleProviderDiffDTO) bool {
	if len(a) != len(b) {
		return false
	}
	cA := append([]ruleProviderDiffDTO(nil), a...)
	cB := append([]ruleProviderDiffDTO(nil), b...)
	sort.Slice(cA, func(i, j int) bool { return cA[i].ID < cA[j].ID })
	sort.Slice(cB, func(i, j int) bool { return cB[i].ID < cB[j].ID })
	return reflect.DeepEqual(cA, cB)
}

func rulesEqual(a, b []ruleDiffDTO) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ListenersEqual checks if two sets of listener specifications match completely.
func ListenersEqual(a, b []ListenerSpec) bool {
	if len(a) != len(b) {
		return false
	}
	cA := append([]ListenerSpec(nil), a...)
	cB := append([]ListenerSpec(nil), b...)
	sort.Slice(cA, func(i, j int) bool {
		if cA[i].Address != cA[j].Address {
			return cA[i].Address < cA[j].Address
		}
		if cA[i].Port != cA[j].Port {
			return cA[i].Port < cA[j].Port
		}
		if cA[i].GetNetwork() != cA[j].GetNetwork() {
			return cA[i].GetNetwork() < cA[j].GetNetwork()
		}
		if cA[i].Family != cA[j].Family {
			return cA[i].Family < cA[j].Family
		}
		return cA[i].Purpose < cA[j].Purpose
	})
	sort.Slice(cB, func(i, j int) bool {
		if cB[i].Address != cB[j].Address {
			return cB[i].Address < cB[j].Address
		}
		if cB[i].Port != cB[j].Port {
			return cB[i].Port < cB[j].Port
		}
		if cB[i].GetNetwork() != cB[j].GetNetwork() {
			return cB[i].GetNetwork() < cB[j].GetNetwork()
		}
		if cB[i].Family != cB[j].Family {
			return cB[i].Family < cB[j].Family
		}
		return cB[i].Purpose < cB[j].Purpose
	})
	for i := range cA {
		if cA[i].Address != cB[i].Address ||
			cA[i].Port != cB[i].Port ||
			cA[i].GetNetwork() != cB[i].GetNetwork() ||
			cA[i].Family != cB[i].Family ||
			cA[i].Purpose != cB[i].Purpose {
			return false
		}
	}
	return true
}
