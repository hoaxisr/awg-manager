package mihomonative

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/strictfs"
	"gopkg.in/yaml.v3"
)

var (
	ErrNotFound          = errors.New("mihomo native: not found")
	ErrRuleNotFound      = errors.New("mihomo native: rule not found")
	ErrIDNotAllowed      = errors.New("mihomo native: rule id must not be specified when creating a rule")
	ErrIDMismatch        = errors.New("mihomo native: payload id does not match route id")
	ErrRulesStale        = errors.New("mihomo native: unsupported rules have changed, snapshot is stale")
	ErrSelectionMismatch = errors.New("mihomo native: rule selection does not match current unsupported rules")
)

const autoNativeGroupName = "Mihomo: Native"

type state struct {
	Version              int             `json:"version"`
	Proxies              []*ProxyNode    `json:"proxies"`
	Subscriptions        []*Subscription `json:"subscriptions"`
	Groups               []*ProxyGroup   `json:"groups,omitempty"`
	Rules                []*Rule         `json:"rules,omitempty"`
	RuleProviders        []*RuleProvider `json:"ruleProviders,omitempty"`
	LegacyGroupsImported bool            `json:"legacyGroupsImported,omitempty"`
	LegacyRulesImported  bool            `json:"legacyRulesImported,omitempty"`
}

type InUseChecker func(kind string, id string, name string) (bool, string)

type Store struct {
	path          string
	mu            sync.RWMutex
	revision      uint64
	data          state
	inUseChecker  InUseChecker
}

func (s *Store) SetInUseChecker(checker InUseChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inUseChecker = checker
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, data: state{Version: 4}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("mihomo native store: parse %s: %w", path, err)
		}
	}
	needsSave := false
	if s.data.Version < 4 {
		s.data.Version = 4
		needsSave = true
	}
	for _, sub := range s.data.Subscriptions {
		if sub.ProviderName == "" && len(sub.ID) >= 8 {
			sub.ProviderName = "mnp-" + sub.ID[:8]
			needsSave = true
		}
		if sub.GroupName == "" {
			sub.GroupName = "Mihomo: " + sub.Name
			needsSave = true
		}
	}
	for _, r := range s.data.Rules {
		if r.Outbound == "Задний вход :)" {
			for _, g := range s.data.Groups {
				if strings.HasPrefix(strings.ToLower(g.Name), "задний") {
					r.Outbound = g.Name
					needsSave = true
					break
				}
			}
		}
	}
	if needsSave && len(b) > 0 {
		if err := s.saveLocked(); err != nil {
			return nil, fmt.Errorf("mihomo native store: persist migration for %s: %w", path, err)
		}
	}
	return s, nil
}

// StoreSnapshot is an opaque, deep snapshot used by the API transaction
// boundary. JSON is deliberate here: it gives us an exact copy of every
// nested nativeConfig/header/slice without leaking the private state type.
type StoreSnapshot struct {
	data     []byte
	revision uint64
	digest   string
}

func (sn StoreSnapshot) Data() []byte {
	return sn.data
}

func (sn StoreSnapshot) Revision() uint64 {
	return sn.revision
}

func (sn StoreSnapshot) Digest() string {
	return sn.digest
}

func (s *Store) Snapshot() (StoreSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := json.Marshal(s.data)
	if err != nil {
		return StoreSnapshot{}, err
	}
	digest := strictfs.ComputeBytesDigest(b)
	return StoreSnapshot{
		data:     b,
		revision: s.revision,
		digest:   digest,
	}, nil
}

func (s *Store) SnapshotRevision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

func (s *Store) RestoreSnapshot(snapshot StoreSnapshot) error {
	var restored state
	if len(snapshot.data) == 0 {
		return fmt.Errorf("mihomo native: empty store snapshot")
	}
	if err := json.Unmarshal(snapshot.data, &restored); err != nil {
		return fmt.Errorf("mihomo native: decode store snapshot: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.data
	s.data = restored
	if err := s.saveLocked(); err != nil {
		s.data = previous
		return err
	}
	return nil
}

func (s *Store) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = state{
		Version:       s.data.Version,
		Proxies:       s.data.Proxies,
		Subscriptions: s.data.Subscriptions,
		Groups:        make([]*ProxyGroup, 0),
		Rules:         make([]*Rule, 0),
		RuleProviders: make([]*RuleProvider, 0),
	}
	return s.saveLocked()
}

type BridgeRef struct {
	Kind        string
	ID          string
	Label       string
	LegacyOwner string
	Enabled     bool
	Bridge      ProxyBridge
}

// ListBridges returns only persisted bridge allocations. Subscription member
// nodes are intentionally omitted: their subscription selector owns one
// bridge for the whole resource.
func (s *Store) ListBridges() []BridgeRef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]BridgeRef, 0, len(s.data.Proxies)+len(s.data.Subscriptions))
	for _, node := range s.data.Proxies {
		if node.SourceID != "" || node.Bridge == nil {
			continue
		}
		out = append(out, BridgeRef{
			Kind: "proxy", ID: node.ID, Label: node.Name,
			LegacyOwner: node.Bridge.LegacyOwner,
			Enabled:     node.Enabled && node.SelectedEngine == EngineMihomo,
			Bridge:      *node.Bridge,
		})
	}
	for _, sub := range s.data.Subscriptions {
		if sub.Bridge == nil {
			continue
		}
		out = append(out, BridgeRef{
			Kind: "subscription", ID: sub.ID, Label: sub.Name,
			LegacyOwner: sub.Bridge.LegacyOwner,
			Enabled:     s.subscriptionExportableLocked(sub),
			Bridge:      *sub.Bridge,
		})
	}
	return out
}

func (s *Store) SetBridge(kind, id string, bridge ProxyBridge) error {
	if err := validateBridge(bridge); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var target **ProxyBridge
	switch kind {
	case "proxy":
		for _, node := range s.data.Proxies {
			if node.ID == id && node.SourceID == "" {
				target = &node.Bridge
				break
			}
		}
	case "subscription":
		for _, sub := range s.data.Subscriptions {
			if sub.ID == id {
				target = &sub.Bridge
				break
			}
		}
	default:
		return fmt.Errorf("mihomo native: invalid bridge kind %q", kind)
	}
	if target == nil {
		return ErrNotFound
	}
	for _, node := range s.data.Proxies {
		if node.Bridge == nil || (kind == "proxy" && node.ID == id) {
			continue
		}
		if node.Bridge.ListenPort == bridge.ListenPort || node.Bridge.ProxyIndex == bridge.ProxyIndex {
			return fmt.Errorf("mihomo native: bridge allocation is already used")
		}
	}
	for _, sub := range s.data.Subscriptions {
		if sub.Bridge == nil || (kind == "subscription" && sub.ID == id) {
			continue
		}
		if sub.Bridge.ListenPort == bridge.ListenPort || sub.Bridge.ProxyIndex == bridge.ProxyIndex {
			return fmt.Errorf("mihomo native: bridge allocation is already used")
		}
	}
	previous := *target
	copy := bridge
	*target = &copy
	if err := s.saveLocked(); err != nil {
		*target = previous
		return err
	}
	return nil
}

// marshalLocked serializes the in-memory store state while holding s.mu (RLock or Lock).
func (s *Store) marshalLocked() ([]byte, error) {
	return json.MarshalIndent(s.data, "", "  ")
}

func (s *Store) saveLocked() error {
	s.revision++
	b, err := s.marshalLocked()
	if err != nil {
		s.revision--
		return err
	}
	if err := strictfs.StrictWriteAtomic(s.path, b, 0600); err != nil {
		s.revision--
		return err
	}
	return nil
}

func (s *Store) SnapshotFilePath(txid string) (string, error) {
	if err := strictfs.ValidateTxID(txid); err != nil {
		return "", err
	}
	dir := filepath.Dir(s.path)
	return filepath.Join(dir, fmt.Sprintf("store.snapshot.%s.json", txid)), nil
}

func (s *Store) CreateSnapshotFileAt(txid, targetPath string) (string, error) {
	expectedPath, err := s.SnapshotFilePath(txid)
	if err != nil {
		return "", err
	}
	cleanTarget := filepath.Clean(targetPath)
	if cleanTarget != expectedPath {
		return "", fmt.Errorf("target snapshot path %q does not match canonical %q", targetPath, expectedPath)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	b, err := s.marshalLocked()
	if err != nil {
		return "", fmt.Errorf("mihomo native: marshal store snapshot: %w", err)
	}

	digest := strictfs.ComputeBytesDigest(b)
	if err := strictfs.StrictWriteAtomic(cleanTarget, b, 0600); err != nil {
		return "", fmt.Errorf("mihomo native: write snapshot %s: %w", cleanTarget, err)
	}
	return digest, nil
}

func (s *Store) CreateSnapshotFile(txid string) (string, error) {
	snapshotPath, err := s.SnapshotFilePath(txid)
	if err != nil {
		return "", err
	}
	if _, err := s.CreateSnapshotFileAt(txid, snapshotPath); err != nil {
		return "", err
	}
	return snapshotPath, nil
}

func (s *Store) RestoreSnapshotFile(snapshotPath string) error {
	b, err := os.ReadFile(snapshotPath)
	if err != nil {
		return fmt.Errorf("mihomo native: read snapshot %s: %w", snapshotPath, err)
	}

	var restored state
	if err := json.Unmarshal(b, &restored); err != nil {
		return fmt.Errorf("mihomo native: unmarshal snapshot %s: %w", snapshotPath, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	previous := s.data
	s.data = restored
	if err := s.saveLocked(); err != nil {
		s.data = previous
		return fmt.Errorf("mihomo native: save restored store: %w", err)
	}
	return nil
}

func (s *Store) RemoveSnapshotFile(snapshotPath string) error {
	return strictfs.StrictUnlink(snapshotPath)
}

type desiredProxy struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Protocol         string                 `json:"protocol"`
	Transport        string                 `json:"transport"`
	EnginePreference EnginePreference       `json:"enginePreference"`
	SelectedEngine   EnginePreference       `json:"selectedEngine"`
	SourceID         string                 `json:"sourceId,omitempty"`
	RawURI           string                 `json:"rawUri,omitempty"`
	NativeConfig     map[string]interface{} `json:"nativeConfig"`
	Compatibility    Compatibility          `json:"compatibility"`
	Enabled          bool                   `json:"enabled"`
	Bridge           *ProxyBridge           `json:"bridge,omitempty"`
}

type desiredSubscription struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	URL              string              `json:"url,omitempty"`
	Inline           string              `json:"inline,omitempty"`
	Format           SubscriptionFormat  `json:"format"`
	EnginePreference EnginePreference    `json:"enginePreference"`
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
	Bridge           *ProxyBridge        `json:"bridge,omitempty"`
	Members          []MemberInfo        `json:"members,omitempty"`
}

type desiredState struct {
	Version              int                   `json:"version"`
	Proxies              []desiredProxy        `json:"proxies,omitempty"`
	Subscriptions        []desiredSubscription `json:"subscriptions,omitempty"`
	Groups               []*ProxyGroup         `json:"groups,omitempty"`
	Rules                []*Rule               `json:"rules,omitempty"`
	RuleProviders        []*RuleProvider       `json:"ruleProviders,omitempty"`
	LegacyGroupsImported bool                  `json:"legacyGroupsImported,omitempty"`
	LegacyRulesImported  bool                  `json:"legacyRulesImported,omitempty"`
}

func (s *Store) desiredDigestLocked() (string, error) {
	proj := desiredState{
		Version:              s.data.Version,
		LegacyGroupsImported: s.data.LegacyGroupsImported,
		LegacyRulesImported:  s.data.LegacyRulesImported,
	}

	if len(s.data.Proxies) > 0 {
		proj.Proxies = make([]desiredProxy, len(s.data.Proxies))
		for i, p := range s.data.Proxies {
			proj.Proxies[i] = desiredProxy{
				ID:               p.ID,
				Name:             p.Name,
				Protocol:         p.Protocol,
				Transport:        p.Transport,
				EnginePreference: p.EnginePreference,
				SelectedEngine:   p.SelectedEngine,
				SourceID:         p.SourceID,
				RawURI:           p.RawURI,
				NativeConfig:     p.NativeConfig,
				Compatibility:    p.Compatibility,
				Enabled:          p.Enabled,
				Bridge:           p.Bridge,
			}
		}
		sort.Slice(proj.Proxies, func(i, j int) bool {
			return proj.Proxies[i].ID < proj.Proxies[j].ID
		})
	}

	if len(s.data.Subscriptions) > 0 {
		proj.Subscriptions = make([]desiredSubscription, len(s.data.Subscriptions))
		for i, sub := range s.data.Subscriptions {
			proj.Subscriptions[i] = desiredSubscription{
				ID:               sub.ID,
				Name:             sub.Name,
				URL:              sub.URL,
				Inline:           sub.Inline,
				Format:           sub.Format,
				EnginePreference: sub.EnginePreference,
				ProviderName:     sub.ProviderName,
				GroupName:        sub.GroupName,
				Headers:          sub.Headers,
				RefreshHours:     sub.RefreshHours,
				Enabled:          sub.Enabled,
				Mode:             sub.Mode,
				TestURL:          sub.TestURL,
				TestInterval:     sub.TestInterval,
				TestTolerance:    sub.TestTolerance,
				FilterInclude:    sub.FilterInclude,
				FilterExclude:    sub.FilterExclude,
				BindInterface:    sub.BindInterface,
				Bridge:           sub.Bridge,
				Members:          sub.Members,
			}
		}
		sort.Slice(proj.Subscriptions, func(i, j int) bool {
			return proj.Subscriptions[i].ID < proj.Subscriptions[j].ID
		})
	}

	if len(s.data.Groups) > 0 {
		proj.Groups = make([]*ProxyGroup, len(s.data.Groups))
		copy(proj.Groups, s.data.Groups)
		sort.Slice(proj.Groups, func(i, j int) bool {
			return proj.Groups[i].ID < proj.Groups[j].ID
		})
	}

	if len(s.data.Rules) > 0 {
		proj.Rules = make([]*Rule, len(s.data.Rules))
		copy(proj.Rules, s.data.Rules)
	}

	if len(s.data.RuleProviders) > 0 {
		proj.RuleProviders = make([]*RuleProvider, len(s.data.RuleProviders))
		copy(proj.RuleProviders, s.data.RuleProviders)
		sort.Slice(proj.RuleProviders, func(i, j int) bool {
			return proj.RuleProviders[i].Name < proj.RuleProviders[j].Name
		})
	}

	b, err := json.MarshalIndent(proj, "", "  ")
	if err != nil {
		return "", fmt.Errorf("mihomo native: marshal desired state for digest: %w", err)
	}
	return strictfs.ComputeBytesDigest(b), nil
}

func (s *Store) CurrentDesiredDigest() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.desiredDigestLocked()
}

func (s *Store) CurrentSnapshotDigest() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	b, err := s.marshalLocked()
	if err != nil {
		return "", fmt.Errorf("mihomo native: marshal data for snapshot digest: %w", err)
	}
	return strictfs.ComputeBytesDigest(b), nil
}

func (s *Store) CurrentDigest() (string, error) {
	return s.CurrentSnapshotDigest()
}

func (s *Store) DraftJournalPath() string {
	return filepath.Join(filepath.Dir(s.path), "store.draft.json")
}

func (s *Store) SaveDraftJournal(journal mihomo.DraftJournal) error {
	b, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	return strictfs.StrictWriteAtomic(s.DraftJournalPath(), b, 0600)
}

func (s *Store) LoadDraftJournal() (*mihomo.DraftJournal, error) {
	p := s.DraftJournalPath()
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var dj mihomo.DraftJournal
	if err := json.Unmarshal(b, &dj); err != nil {
		return nil, fmt.Errorf("unmarshal draft journal %s: %w", p, err)
	}
	return &dj, nil
}

func (s *Store) RemoveDraftJournal() error {
	return strictfs.StrictUnlink(s.DraftJournalPath())
}

// StoreTxAdapter wraps *Store to satisfy mihomo.NativeStoreTx.
type StoreTxAdapter struct {
	store *Store
}

func NewStoreTxAdapter(store *Store) *StoreTxAdapter {
	return &StoreTxAdapter{store: store}
}

func (s *Store) TxAdapter() *StoreTxAdapter {
	return NewStoreTxAdapter(s)
}

func (a *StoreTxAdapter) SnapshotFilePath(txid string) (string, error) {
	return a.store.SnapshotFilePath(txid)
}

func (a *StoreTxAdapter) CreateSnapshotFile(txid string) (string, error) {
	return a.store.CreateSnapshotFile(txid)
}

func (a *StoreTxAdapter) CreateSnapshotFileAt(txid, targetPath string) (string, error) {
	return a.store.CreateSnapshotFileAt(txid, targetPath)
}

func (a *StoreTxAdapter) RestoreSnapshotFile(snapshotPath string) error {
	return a.store.RestoreSnapshotFile(snapshotPath)
}

func (a *StoreTxAdapter) RemoveSnapshotFile(snapshotPath string) error {
	return a.store.RemoveSnapshotFile(snapshotPath)
}

func (a *StoreTxAdapter) CurrentDigest() (string, error) {
	return a.store.CurrentDigest()
}

func (a *StoreTxAdapter) CurrentDesiredDigest() (string, error) {
	return a.store.CurrentDesiredDigest()
}

func (a *StoreTxAdapter) CurrentSnapshotDigest() (string, error) {
	return a.store.CurrentSnapshotDigest()
}

func (a *StoreTxAdapter) ListBridges() []mihomo.BridgeRef {
	nativeBridges := a.store.ListBridges()
	out := make([]mihomo.BridgeRef, len(nativeBridges))
	for i, nb := range nativeBridges {
		out[i] = mihomo.BridgeRef{
			ProxyIndex:      nb.Bridge.ProxyIndex,
			ProxyInterface:  nb.Bridge.ProxyInterface,
			KernelInterface: nb.Bridge.KernelInterface,
			ListenPort:      nb.Bridge.ListenPort,
			LegacyOwner:     nb.LegacyOwner,
			OwnerUUID:       BridgeOwnershipDescription(nb.Kind, nb.ID),
		}
	}
	return out
}

func (s *Store) ListProxies() []ProxyNode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProxyNode, 0, len(s.data.Proxies))
	for _, node := range s.data.Proxies {
		out = append(out, cloneNode(node))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (s *Store) GetProxy(id string) (ProxyNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, node := range s.data.Proxies {
		if node.ID == id {
			return cloneNode(node), nil
		}
	}
	return ProxyNode{}, ErrNotFound
}

// RestoreProxy is used to roll back a failed transactional delete.
func (s *Store) RestoreProxy(node ProxyNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.data.Proxies {
		if existing.ID == node.ID {
			return nil
		}
	}
	restored := cloneNode(&node)
	s.data.Proxies = append(s.data.Proxies, &restored)
	if err := s.saveLocked(); err != nil {
		s.data.Proxies = s.data.Proxies[:len(s.data.Proxies)-1]
		return err
	}
	return nil
}

// ConfigProxies returns enabled nodes executed natively by Mihomo. Callers
// receive deep copies so config generation cannot mutate persisted state.
func (s *Store) ConfigProxies() []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	enabledSubscriptions := make(map[string]bool, len(s.data.Subscriptions))
	for _, sub := range s.data.Subscriptions {
		enabledSubscriptions[sub.ID] = sub.Enabled && sub.EnginePreference != EngineSingbox
	}
	out := make([]map[string]interface{}, 0, len(s.data.Proxies))
	for _, node := range s.data.Proxies {
		if !node.Enabled || node.SelectedEngine != EngineMihomo {
			continue
		}
		if node.SourceID != "" && !enabledSubscriptions[node.SourceID] {
			continue
		}
		clone := cloneNode(node)
		out = append(out, clone.NativeConfig)
	}
	return out
}

func (s *Store) ConfigProviders() map[string]map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]map[string]interface{})
	for _, sub := range s.data.Subscriptions {
		if !sub.Enabled || sub.URL == "" || sub.EnginePreference == EngineSingbox || sub.Format != FormatMihomoProvider {
			continue
		}
		interval := sub.RefreshHours * 3600
		out[sub.ProviderName] = map[string]interface{}{
			"type": "http", "url": sub.URL,
			"path":     "./providers/" + sub.ProviderName + ".yaml",
			"interval": interval,
			"health-check": map[string]interface{}{
				"enable": true, "url": "https://www.gstatic.com/generate_204", "interval": 300,
			},
		}
		if len(sub.Headers) > 0 {
			out[sub.ProviderName]["header"] = cloneHeaders(sub.Headers)
		}
		if strings.TrimSpace(sub.FilterInclude) != "" {
			out[sub.ProviderName]["filter"] = strings.TrimSpace(sub.FilterInclude)
		}
		if strings.TrimSpace(sub.FilterExclude) != "" {
			out[sub.ProviderName]["exclude-filter"] = strings.TrimSpace(sub.FilterExclude)
		}
	}
	return out
}

func (s *Store) ConfigProviderGroups() []map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []map[string]interface{}
	var nativeNames []string
	for _, node := range s.data.Proxies {
		if node.Enabled && node.SelectedEngine == EngineMihomo && node.SourceID == "" {
			nativeNames = append(nativeNames, node.Name)
		}
	}
	if len(nativeNames) > 0 {
		sort.Slice(nativeNames, func(i, j int) bool { return strings.ToLower(nativeNames[i]) < strings.ToLower(nativeNames[j]) })
		out = append(out, map[string]interface{}{
			"name": autoNativeGroupName, "type": "select", "proxies": nativeNames,
		})
	}
	validNames := map[string]bool{
		"DIRECT": true, "REJECT": true, "GLOBAL": true, "COMPATIBLE": true,
		autoNativeGroupName: true,
	}
	for _, n := range nativeNames {
		validNames[n] = true
	}
	for _, group := range s.data.Groups {
		if group.Enabled {
			validNames[group.Name] = true
		}
	}
	for _, sub := range s.data.Subscriptions {
		if sub.Enabled && sub.EnginePreference != EngineSingbox {
			validNames[sub.GroupName] = true
		}
	}

	for _, group := range s.data.Groups {
		if !group.Enabled {
			continue
		}
		var validProxies []string
		for _, p := range group.Proxies {
			p = strings.TrimSpace(p)
			if p != "" {
				validProxies = append(validProxies, p)
			}
		}
		if len(validProxies) == 0 && len(group.Use) == 0 && !group.IncludeAll && !group.IncludeAllProxies && !group.IncludeAllProviders {
			validProxies = []string{"DIRECT"}
		}
		lazy := group.Lazy
		interval := group.Interval
		if group.Type == "fallback" || group.Type == "url-test" {
			if interval <= 0 {
				interval = 60
			}
			if group.Type == "fallback" {
				lazy = false
			} else {
				for _, p := range validProxies {
					if strings.EqualFold(p, "DIRECT") {
						lazy = false
						break
					}
				}
			}
		}
		out = append(out, map[string]interface{}{
			"name": group.Name, "type": group.Type, "proxies": validProxies,
			"use": append([]string(nil), group.Use...), "url": group.URL, "interval": interval,
			"lazy": lazy, "strategy": group.Strategy, "tolerance": group.Tolerance,
			"timeout": group.Timeout, "max-failed-times": group.MaxFailedTimes, "disable-udp": group.DisableUDP,
			"include-all": group.IncludeAll, "include-all-proxies": group.IncludeAllProxies, "include-all-providers": group.IncludeAllProviders,
			"filter": group.Filter, "exclude-filter": group.ExcludeFilter, "exclude-type": group.ExcludeType,
			"expected-status": group.ExpectedStatus, "hidden": group.Hidden, "icon": group.Icon,
		})
	}
	for _, sub := range s.data.Subscriptions {
		if !sub.Enabled || sub.EnginePreference == EngineSingbox {
			continue
		}
		groupType := sub.Mode
		if groupType == "" || groupType == "selector" {
			groupType = "select"
		} else if groupType == "urltest" {
			groupType = "url-test"
		}
		groupMap := map[string]interface{}{
			"name": sub.GroupName,
			"type": groupType,
		}
		if groupType == "url-test" || groupType == "fallback" {
			testURL := sub.TestURL
			if testURL == "" {
				testURL = "https://www.gstatic.com/generate_204"
			}
			interval := sub.TestInterval
			if interval <= 0 {
				interval = 60
			}
			groupMap["url"] = testURL
			groupMap["interval"] = interval
			groupMap["lazy"] = false
			if sub.TestTolerance > 0 {
				groupMap["tolerance"] = sub.TestTolerance
			}
		}
		if sub.FilterInclude != "" {
			groupMap["filter"] = sub.FilterInclude
		}
		if sub.FilterExclude != "" {
			groupMap["exclude-filter"] = sub.FilterExclude
		}
		if sub.BindInterface != "" {
			groupMap["interface-name"] = sub.BindInterface
		}

		if sub.Format == FormatMihomoProvider && sub.URL != "" {
			groupMap["use"] = []string{sub.ProviderName}
			out = append(out, groupMap)
			continue
		}
		if sub.Format == FormatShareLinks {
			members := make([]string, 0)
			for _, node := range s.data.Proxies {
				if node.Enabled && node.SelectedEngine == EngineMihomo && node.SourceID == sub.ID {
					members = append(members, node.Name)
				}
			}
			if len(members) == 0 {
				members = []string{"DIRECT"}
			} else {
				sort.Slice(members, func(i, j int) bool { return strings.ToLower(members[i]) < strings.ToLower(members[j]) })
			}
			groupMap["proxies"] = members
			out = append(out, groupMap)
		}
	}
	return out
}

// ConfigBridgeListeners materializes one loopback-only mixed listener per
// enabled exported resource. The listener's proxy option makes it an exact
// egress, rather than feeding traffic back through the global MATCH rules.
func (s *Store) ConfigBridgeListeners() []BridgeListener {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]BridgeListener, 0, len(s.data.Proxies)+len(s.data.Subscriptions))
	for _, node := range s.data.Proxies {
		if node.SourceID != "" || !node.Enabled || node.SelectedEngine != EngineMihomo || node.Bridge == nil {
			continue
		}
		out = append(out, BridgeListener{Name: "mihomo-native-p-" + node.ID, Port: node.Bridge.ListenPort, Proxy: node.Name})
	}
	for _, sub := range s.data.Subscriptions {
		if sub.Bridge == nil || !s.subscriptionExportableLocked(sub) {
			continue
		}
		out = append(out, BridgeListener{Name: "mihomo-native-s-" + sub.ID, Port: sub.Bridge.ListenPort, Proxy: sub.GroupName})
	}
	return out
}

// IsSubscriptionExportable reports whether the subscription's Mihomo target
// actually exists. An inline auto subscription whose members all selected
// sing-box has no Mihomo group and must not receive a bridge listener.
func (s *Store) IsSubscriptionExportable(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sub := range s.data.Subscriptions {
		if sub.ID == id {
			return s.subscriptionExportableLocked(sub)
		}
	}
	return false
}

func (s *Store) subscriptionExportableLocked(sub *Subscription) bool {
	if sub == nil || !sub.Enabled || sub.EnginePreference == EngineSingbox || sub.GroupName == "" {
		return false
	}
	if sub.Format == FormatMihomoProvider {
		return sub.URL != "" && sub.ProviderName != ""
	}
	if sub.Format != FormatShareLinks {
		return false
	}
	for _, node := range s.data.Proxies {
		if node.SourceID == sub.ID && node.Enabled && node.SelectedEngine == EngineMihomo {
			return true
		}
	}
	return false
}

func (s *Store) ListGroups() []ProxyGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProxyGroup, 0, len(s.data.Groups))
	for _, group := range s.data.Groups {
		cp := *group
		cp.Proxies = append([]string(nil), group.Proxies...)
		cp.Use = append([]string(nil), group.Use...)
		out = append(out, cp)
	}
	return out
}

func (s *Store) HasGroups() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.LegacyGroupsImported
}

// ImportLegacyGroups performs the one-time ownership transfer from the old
// shared settings model. The source remains untouched for sing-box, while the
// copied Mihomo definitions become independently editable and authoritative.
func (s *Store) ImportLegacyGroups(groups []storage.ProxyGroup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.LegacyGroupsImported {
		return nil
	}
	oldGroups := s.data.Groups
	oldImported := s.data.LegacyGroupsImported
	existing := make(map[string]bool, len(s.data.Groups))
	for _, group := range s.data.Groups {
		existing[strings.ToLower(group.Name)] = true
	}
	for _, legacy := range groups {
		name := strings.TrimSpace(legacy.Name)
		if name == "" || existing[strings.ToLower(name)] {
			continue
		}
		members := append([]string(nil), legacy.Proxies...)
		for i, member := range members {
			members[i] = normalizeBuiltin(member)
		}
		groupType := legacy.Type
		if groupType == "selector" {
			groupType = "select"
		} else if groupType == "urltest" {
			groupType = "url-test"
		} else if groupType == "loadbalance" {
			groupType = "load-balance"
		}
		if len(members) == 0 {
			continue
		}
		group := &ProxyGroup{
			ID: newID(), Name: name, Type: groupType, Proxies: members, URL: legacy.URL,
			Interval: legacy.Interval, Lazy: legacy.Lazy, Strategy: legacy.Strategy,
			Tolerance: legacy.Tolerance, DisableUDP: legacy.DisableUDP, Enabled: true,
		}
		s.data.Groups = append(s.data.Groups, group)
		existing[strings.ToLower(name)] = true
	}
	s.data.LegacyGroupsImported = true
	if err := s.saveLocked(); err != nil {
		s.data.Groups = oldGroups
		s.data.LegacyGroupsImported = oldImported
		return err
	}
	return nil
}

func (s *Store) checkGroupCyclesLocked(in ProxyGroup) error {
	groupMembers := make(map[string][]string)
	for _, g := range s.data.Groups {
		if (in.ID != "" && g.ID == in.ID) || strings.EqualFold(g.Name, in.Name) {
			continue
		}
		groupMembers[strings.ToLower(g.Name)] = g.Proxies
	}
	groupMembers[strings.ToLower(in.Name)] = in.Proxies

	visited := make(map[string]int) // 0: unvisited, 1: visiting, 2: visited
	var checkDFS func(name string, path []string) error
	checkDFS = func(name string, path []string) error {
		state := visited[name]
		if state == 1 {
			cyclePath := append(path, name)
			return fmt.Errorf("обнаружен цикл в ссылках групп: %s", strings.Join(cyclePath, " -> "))
		}
		if state == 2 {
			return nil
		}
		visited[name] = 1
		for _, member := range groupMembers[name] {
			lowerMember := strings.ToLower(member)
			if _, exists := groupMembers[lowerMember]; exists {
				if err := checkDFS(lowerMember, append(path, name)); err != nil {
					return err
				}
			}
		}
		visited[name] = 2
		return nil
	}

	for name := range groupMembers {
		if visited[name] == 0 {
			if err := checkDFS(name, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) SaveGroup(in ProxyGroup) (ProxyGroup, error) {
	in.Name, in.Type = strings.TrimSpace(in.Name), strings.TrimSpace(in.Type)
	if in.Name == "" {
		return ProxyGroup{}, fmt.Errorf("mihomo native: group name is required")
	}
	switch in.Type {
	case "select", "url-test", "fallback", "load-balance", "relay":
	default:
		return ProxyGroup{}, fmt.Errorf("mihomo native: unsupported group type %q", in.Type)
	}
	if len(in.Proxies) == 0 && len(in.Use) == 0 && !in.IncludeAll && !in.IncludeAllProxies && !in.IncludeAllProviders {
		return ProxyGroup{}, fmt.Errorf("mihomo native: group requires at least one proxy or provider")
	}
	for _, member := range in.Proxies {
		if strings.EqualFold(strings.TrimSpace(member), in.Name) {
			return ProxyGroup{}, fmt.Errorf("mihomo native: group cannot contain itself")
		}
	}
	if in.Interval < 0 {
		return ProxyGroup{}, fmt.Errorf("mihomo native: interval cannot be negative")
	}
	if in.Type == "fallback" {
		in.Lazy = false
		if in.Interval <= 0 {
			in.Interval = 60
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.EqualFold(in.Name, autoNativeGroupName) {
		return ProxyGroup{}, fmt.Errorf("mihomo native: group name %q is reserved", autoNativeGroupName)
	}
	for _, sub := range s.data.Subscriptions {
		if strings.EqualFold(sub.GroupName, in.Name) {
			return ProxyGroup{}, fmt.Errorf("mihomo native: group name conflicts with subscription %q", sub.Name)
		}
	}
	if err := s.checkGroupCyclesLocked(in); err != nil {
		return ProxyGroup{}, err
	}
	if in.ID == "" {
		for i, group := range s.data.Groups {
			if strings.EqualFold(group.Name, in.Name) {
				in.ID = group.ID
				cp := in
				s.data.Groups[i] = &cp
				if err := s.saveLocked(); err != nil {
					s.data.Groups[i] = group
					return ProxyGroup{}, err
				}
				return cp, nil
			}
		}
		in.ID = newID()
	} else {
		for _, group := range s.data.Groups {
			if group.ID != in.ID && strings.EqualFold(group.Name, in.Name) {
				return ProxyGroup{}, fmt.Errorf("Группа с именем %q уже существует", in.Name)
			}
		}
	}
	if !in.Enabled {
		in.Enabled = true
	}
	for i, group := range s.data.Groups {
		if group.ID == in.ID {
			oldName := group.Name
			cp := in
			s.data.Groups[i] = &cp
			if oldName != "" && oldName != in.Name {
				// Cascade rename to rules
				for _, r := range s.data.Rules {
					if r.Outbound == oldName {
						r.Outbound = in.Name
					}
				}
				// Cascade rename in other group member lists
				for _, g := range s.data.Groups {
					for j, member := range g.Proxies {
						if member == oldName {
							g.Proxies[j] = in.Name
						}
					}
				}
			}
			if err := s.saveLocked(); err != nil {
				s.data.Groups[i] = group
				return ProxyGroup{}, err
			}
			return cp, nil
		}
	}
	cp := in
	s.data.Groups = append(s.data.Groups, &cp)
	if err := s.saveLocked(); err != nil {
		s.data.Groups = s.data.Groups[:len(s.data.Groups)-1]
		return ProxyGroup{}, err
	}
	return cp, nil
}

func (s *Store) DeleteGroup(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, group := range s.data.Groups {
		if group.ID == id {
			if s.inUseChecker != nil {
				if inUse, reason := s.inUseChecker("group", group.ID, group.Name); inUse {
					return fmt.Errorf("нельзя удалить группу %q: %s", group.Name, reason)
				}
			}
			deletedName := group.Name

			oldGroups := s.data.Groups
			oldRules := s.data.Rules
			oldRuleProviders := s.data.RuleProviders

			// 1. Remove group
			s.data.Groups = append(append([]*ProxyGroup(nil), oldGroups[:i]...), oldGroups[i+1:]...)

			// 2. Repoint any rule referencing the deleted group to "DIRECT"
			newRules := make([]*Rule, len(s.data.Rules))
			for ri, r := range s.data.Rules {
				if r.Outbound == deletedName {
					rcp := *r
					rcp.Outbound = "DIRECT"
					newRules[ri] = &rcp
				} else {
					newRules[ri] = r
				}
			}
			s.data.Rules = newRules

			// 3. Remove deleted group name from any other group's Proxies list
			for _, g := range s.data.Groups {
				var newProxies []string
				changed := false
				for _, p := range g.Proxies {
					if p == deletedName {
						changed = true
					} else {
						newProxies = append(newProxies, p)
					}
				}
				if changed {
					if len(newProxies) == 0 && len(g.Use) == 0 {
						newProxies = []string{"DIRECT"}
					}
					g.Proxies = newProxies
				}
			}

			// 4. Repoint any rule provider referencing deleted group to "DIRECT"
			newProviders := make([]*RuleProvider, len(s.data.RuleProviders))
			for pi, rp := range s.data.RuleProviders {
				if rp.Proxy == deletedName {
					rpcp := *rp
					rpcp.Proxy = "DIRECT"
					newProviders[pi] = &rpcp
				} else {
					newProviders[pi] = rp
				}
			}
			s.data.RuleProviders = newProviders

			if err := s.saveLocked(); err != nil {
				s.data.Groups = oldGroups
				s.data.Rules = oldRules
				s.data.RuleProviders = oldRuleProviders
				return err
			}
			return nil
		}
	}
	return ErrNotFound
}

func (s *Store) GetGroupReferences(idOrName string) []GroupReference {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getGroupReferencesLocked(idOrName)
}

func (s *Store) getGroupReferencesLocked(idOrName string) []GroupReference {
	var targetName string
	var targetID string
	for _, g := range s.data.Groups {
		if g.ID == idOrName || strings.EqualFold(g.Name, idOrName) {
			targetName = g.Name
			targetID = g.ID
			break
		}
	}
	if targetName == "" {
		targetName = idOrName
		targetID = idOrName
	}

	var refs []GroupReference
	// Check other groups
	for _, g := range s.data.Groups {
		if g.ID == targetID {
			continue
		}
		for _, member := range g.Proxies {
			if strings.EqualFold(member, targetName) {
				refs = append(refs, GroupReference{
					Kind: "group",
					ID:   g.ID,
					Name: g.Name,
				})
				break
			}
		}
	}
	// Check rules
	for _, r := range s.data.Rules {
		if strings.EqualFold(r.Outbound, targetName) {
			refs = append(refs, GroupReference{
				Kind: "rule",
				ID:   r.ID,
				Name: fmt.Sprintf("%s %s", r.Type, r.Payload),
			})
		}
	}
	// Check Susanin / external inUseChecker
	if s.inUseChecker != nil {
		if inUse, reason := s.inUseChecker("group", targetID, targetName); inUse {
			refs = append(refs, GroupReference{
				Kind: "susanin",
				ID:   targetID,
				Name: reason,
			})
		}
	}
	return refs
}

func (s *Store) ListRules() []Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Rule, 0, len(s.data.Rules))
	for _, rule := range s.data.Rules {
		out = append(out, *rule)
	}
	return out
}

func (s *Store) HasRules() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.LegacyRulesImported
}

// ImportLegacyRules imports the exact Mihomo rule lines produced by the
// compatibility converter. Existing native rules stay first, so work already
// done in the new editor retains priority.
func (s *Store) ImportLegacyRules(lines []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.LegacyRulesImported {
		return nil
	}
	oldRules := s.data.Rules
	oldImported := s.data.LegacyRulesImported
	existing := make(map[string]bool, len(s.data.Rules))
	for _, rule := range s.data.Rules {
		existing[ruleConfigLine(*rule)] = true
	}
	for _, line := range lines {
		rule, err := parseConfigRule(line)
		if err != nil {
			s.data.Rules = oldRules
			s.data.LegacyRulesImported = oldImported
			return err
		}
		if existing[line] {
			continue
		}
		rule.ID, rule.Enabled = newID(), true
		cp := rule
		s.data.Rules = append(s.data.Rules, &cp)
		existing[line] = true
	}
	s.data.LegacyRulesImported = true
	if err := s.saveLocked(); err != nil {
		s.data.Rules = oldRules
		s.data.LegacyRulesImported = oldImported
		return err
	}
	return nil
}

func parseConfigRule(line string) (Rule, error) {
	parts := strings.Split(line, ",")
	if len(parts) < 2 {
		return Rule{}, fmt.Errorf("mihomo native: invalid migrated rule %q", line)
	}
	rule := Rule{Type: parts[0]}
	if rule.Type == "MATCH" {
		rule.Outbound = parts[1]
		return rule, nil
	}
	if len(parts) < 3 {
		return Rule{}, fmt.Errorf("mihomo native: invalid migrated rule %q", line)
	}
	rule.Payload, rule.Outbound = parts[1], parts[2]
	if len(parts) > 3 && parts[3] == "no-resolve" {
		rule.NoResolve = true
	}
	return rule, nil
}

func ruleConfigLine(rule Rule) string {
	line := rule.Type
	if rule.Type != "MATCH" {
		line += "," + rule.Payload
	}
	line += "," + normalizeBuiltin(rule.Outbound)
	if rule.NoResolve {
		line += ",no-resolve"
	}
	return line
}

func validateAndNormalizeRule(in Rule) (Rule, error) {
	in.Type, in.Payload, in.Outbound = strings.ToUpper(strings.TrimSpace(in.Type)), strings.TrimSpace(in.Payload), strings.TrimSpace(in.Outbound)
	if in.Type == "SUB-RULE" {
		return Rule{}, fmt.Errorf("mihomo native: SUB-RULE is not supported in AWG Manager")
	}
	spec, ok := mihomo.LookupRuleSpec(in.Type)
	if !ok {
		return Rule{}, fmt.Errorf("mihomo native: unsupported rule type %q", in.Type)
	}
	if spec.RequiresPayload && in.Payload == "" {
		return Rule{}, fmt.Errorf("mihomo native: rule payload is required")
	}
	if in.Outbound == "" {
		return Rule{}, fmt.Errorf("mihomo native: rule outbound is required")
	}
	if in.ID == "" {
		in.ID = newID()
	}
	return in, nil
}

// ValidateRuntimeRules inspects all active (enabled) rules and returns an error if any
// enabled rule has an unsupported rule type (e.g. SUB-RULE).
func (s *Store) ValidateRuntimeRules() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var invalid []string
	for _, r := range s.data.Rules {
		if !r.Enabled {
			continue
		}
		t := strings.ToUpper(strings.TrimSpace(r.Type))
		if t == "SUB-RULE" {
			invalid = append(invalid, fmt.Sprintf("rule %s: SUB-RULE is not supported", r.ID))
			continue
		}
		if !mihomo.IsSupportedRuleType(t) {
			invalid = append(invalid, fmt.Sprintf("rule %s: unsupported rule type %q", r.ID, r.Type))
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("mihomo native: unsupported active rules found: %s", strings.Join(invalid, "; "))
	}
	return nil
}

type canonicalUnsupportedRule struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Payload   string `json:"payload"`
	Outbound  string `json:"outbound"`
	NoResolve bool   `json:"noResolve"`
	Enabled   bool   `json:"enabled"`
}

func (s *Store) computeUnsupportedRulesSnapshotLocked() ([]Rule, string) {
	var out []Rule
	for _, r := range s.data.Rules {
		t := strings.ToUpper(strings.TrimSpace(r.Type))
		if t == "SUB-RULE" || !mihomo.IsSupportedRuleType(t) {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	canonical := make([]canonicalUnsupportedRule, len(out))
	for i, r := range out {
		canonical[i] = canonicalUnsupportedRule{
			ID:        r.ID,
			Type:      r.Type,
			Payload:   r.Payload,
			Outbound:  r.Outbound,
			NoResolve: r.NoResolve,
			Enabled:   r.Enabled,
		}
	}
	b, _ := json.Marshal(canonical)
	h := sha256.Sum256(b)
	revision := "v1:" + hex.EncodeToString(h[:])
	return out, revision
}

// ComputeUnsupportedRulesSnapshot returns a snapshot of all persisted rules with unsupported rule types
// along with a canonical SHA-256 revision token.
func (s *Store) ComputeUnsupportedRulesSnapshot() ([]Rule, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.computeUnsupportedRulesSnapshotLocked()
}

// UnsupportedRules returns all persisted rules (enabled or disabled) that have an unsupported type.
func (s *Store) UnsupportedRules() []Rule {
	rules, _ := s.ComputeUnsupportedRulesSnapshot()
	return rules
}

// DeleteUnsupportedRules removes rules with unsupported rule types only if expectedIDs matches
// the exact, unique, non-empty full set of currently unsupported rules and expectedRevision matches the snapshot revision.
// It is transactional: if saveLocked() fails, the in-memory slice is rolled back
// and an error is returned. Returns the number of removed rules on success.
func (s *Store) DeleteUnsupportedRules(expectedIDs []string, expectedRevision string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(expectedIDs) == 0 {
		return 0, ErrSelectionMismatch
	}

	current, rev := s.computeUnsupportedRulesSnapshotLocked()
	if rev != expectedRevision {
		return 0, ErrRulesStale
	}

	if len(current) == 0 {
		return 0, ErrSelectionMismatch
	}

	expectedSet := make(map[string]struct{}, len(expectedIDs))
	for _, id := range expectedIDs {
		if id == "" {
			return 0, ErrSelectionMismatch
		}
		if _, exists := expectedSet[id]; exists {
			return 0, ErrSelectionMismatch
		}
		expectedSet[id] = struct{}{}
	}

	if len(expectedSet) != len(current) {
		return 0, ErrSelectionMismatch
	}

	currentMap := make(map[string]bool, len(current))
	for _, r := range current {
		currentMap[r.ID] = true
		if _, ok := expectedSet[r.ID]; !ok {
			return 0, ErrSelectionMismatch
		}
	}

	var toKeep []*Rule
	for _, r := range s.data.Rules {
		if !currentMap[r.ID] {
			toKeep = append(toKeep, r)
		}
	}

	oldRules := s.data.Rules
	s.data.Rules = toKeep
	if err := s.saveLocked(); err != nil {
		s.data.Rules = oldRules
		return 0, err
	}
	return len(current), nil
}

// CreateRule creates a new rule using RuleInput. ID must be empty.
// Defaults Enabled to true if not specified.
func (s *Store) CreateRule(in RuleInput) (Rule, error) {
	if in.ID != "" {
		return Rule{}, ErrIDNotAllowed
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	normalized, err := validateAndNormalizeRule(Rule{
		ID:        newID(),
		Type:      in.Type,
		Payload:   in.Payload,
		Outbound:  in.Outbound,
		NoResolve: in.NoResolve,
		Enabled:   enabled,
	})
	if err != nil {
		return Rule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := normalized
	s.data.Rules = append(s.data.Rules, &cp)
	if err := s.saveLocked(); err != nil {
		s.data.Rules = s.data.Rules[:len(s.data.Rules)-1]
		return Rule{}, err
	}
	return cp, nil
}

// UpdateRule updates an existing rule by ID using RuleInput.
// If in.Enabled is nil, existing Enabled state is preserved.
func (s *Store) UpdateRule(id string, in RuleInput) (Rule, error) {
	if id == "" {
		return Rule{}, ErrRuleNotFound
	}
	if in.ID != "" && in.ID != id {
		return Rule{}, ErrIDMismatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, r := range s.data.Rules {
		if r.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return Rule{}, ErrRuleNotFound
	}

	enabled := s.data.Rules[idx].Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	normalized, err := validateAndNormalizeRule(Rule{
		ID:        id,
		Type:      in.Type,
		Payload:   in.Payload,
		Outbound:  in.Outbound,
		NoResolve: in.NoResolve,
		Enabled:   enabled,
	})
	if err != nil {
		return Rule{}, err
	}

	old := s.data.Rules[idx]
	cp := normalized
	s.data.Rules[idx] = &cp
	if err := s.saveLocked(); err != nil {
		s.data.Rules[idx] = old
		return Rule{}, err
	}
	return cp, nil
}

func (s *Store) SaveRule(in Rule) (Rule, error) {
	normalized, err := validateAndNormalizeRule(in)
	if err != nil {
		return Rule{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, rule := range s.data.Rules {
		if rule.ID == normalized.ID {
			old := rule
			cp := normalized
			s.data.Rules[i] = &cp
			if err := s.saveLocked(); err != nil {
				s.data.Rules[i] = old
				return Rule{}, err
			}
			return cp, nil
		}
	}
	cp := normalized
	s.data.Rules = append(s.data.Rules, &cp)
	if err := s.saveLocked(); err != nil {
		s.data.Rules = s.data.Rules[:len(s.data.Rules)-1]
		return Rule{}, err
	}
	return cp, nil
}

// SaveRulesBatch validates every item upfront and applies the entire batch in a
// single atomic disk/state mutation.
func (s *Store) SaveRulesBatch(items []Rule) ([]Rule, error) {
	if len(items) == 0 {
		return nil, nil
	}
	validated := make([]Rule, 0, len(items))
	for idx, item := range items {
		norm, err := validateAndNormalizeRule(item)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", idx, err)
		}
		validated = append(validated, norm)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	oldRules := s.data.Rules
	rulesCopy := make([]*Rule, len(s.data.Rules))
	copy(rulesCopy, s.data.Rules)

	result := make([]Rule, 0, len(validated))
	for _, norm := range validated {
		found := false
		cp := norm
		for i, existing := range rulesCopy {
			if existing.ID == norm.ID {
				rulesCopy[i] = &cp
				found = true
				break
			}
		}
		if !found {
			rulesCopy = append(rulesCopy, &cp)
		}
		result = append(result, cp)
	}

	s.data.Rules = rulesCopy
	if err := s.saveLocked(); err != nil {
		s.data.Rules = oldRules
		return nil, err
	}
	return result, nil
}

func (s *Store) DeleteRule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, rule := range s.data.Rules {
		if rule.ID == id {
			old := s.data.Rules
			s.data.Rules = append(append([]*Rule(nil), old[:i]...), old[i+1:]...)
			if err := s.saveLocked(); err != nil {
				s.data.Rules = old
				return err
			}
			return nil
		}
	}
	return ErrNotFound
}

// ReorderRulesWithRevision reorders rules if expectedRevision is nil or matches s.revision.
// Returns ErrRulesStale if expectedRevision is non-nil and does not match s.revision.
func (s *Store) ReorderRulesWithRevision(ids []string, expectedRevision *uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectedRevision != nil && *expectedRevision != s.revision {
		return ErrRulesStale
	}
	if len(ids) != len(s.data.Rules) {
		return fmt.Errorf("mihomo native: rule order size mismatch")
	}
	byID := make(map[string]*Rule, len(s.data.Rules))
	for _, rule := range s.data.Rules {
		byID[rule.ID] = rule
	}
	next := make([]*Rule, 0, len(ids))
	for _, id := range ids {
		rule, ok := byID[id]
		if !ok {
			return fmt.Errorf("mihomo native: unknown rule %q", id)
		}
		next = append(next, rule)
		delete(byID, id)
	}
	old := s.data.Rules
	s.data.Rules = next
	if err := s.saveLocked(); err != nil {
		s.data.Rules = old
		return err
	}
	return nil
}

func (s *Store) ReorderRules(ids []string) error {
	return s.ReorderRulesWithRevision(ids, nil)
}

func (s *Store) ConfigRules() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.data.Rules))
	for _, rule := range s.data.Rules {
		if !rule.Enabled {
			continue
		}
		out = append(out, ruleConfigLine(*rule))
	}
	return out
}

func normalizeBuiltin(name string) string {
	if name == "direct" {
		return "DIRECT"
	}
	if name == "block" {
		return "REJECT"
	}
	return name
}

func (s *Store) ListRuleProviders() []RuleProvider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RuleProvider, 0, len(s.data.RuleProviders))
	for _, provider := range s.data.RuleProviders {
		out = append(out, *provider)
	}
	return out
}

func (s *Store) SaveRuleProvider(in RuleProvider) (RuleProvider, error) {
	in.Name, in.Type, in.URL, in.Path = strings.TrimSpace(in.Name), strings.TrimSpace(in.Type), strings.TrimSpace(in.URL), strings.TrimSpace(in.Path)
	if in.Name == "" {
		return RuleProvider{}, fmt.Errorf("mihomo native: rule provider name is required")
	}
	if in.Type == "" {
		in.Type = "http"
	}
	if in.Type != "http" && in.Type != "file" {
		return RuleProvider{}, fmt.Errorf("mihomo native: unsupported rule provider type %q", in.Type)
	}
	if in.Type == "http" && in.URL == "" {
		return RuleProvider{}, fmt.Errorf("mihomo native: HTTP rule provider URL is required")
	}
	if in.Behavior == "" {
		in.Behavior = "classical"
	}
	if in.Behavior != "classical" && in.Behavior != "domain" && in.Behavior != "ipcidr" {
		return RuleProvider{}, fmt.Errorf("mihomo native: invalid rule provider behavior")
	}
	if in.Format == "" {
		in.Format = "yaml"
	}
	if in.Format != "yaml" && in.Format != "text" && in.Format != "mrs" {
		return RuleProvider{}, fmt.Errorf("mihomo native: invalid rule provider format")
	}
	if in.Interval <= 0 {
		in.Interval = 86400
	}
	if in.ID == "" {
		in.ID = newID()
	}
	in.Enabled = true
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, provider := range s.data.RuleProviders {
		if provider.ID != in.ID && strings.EqualFold(provider.Name, in.Name) {
			return RuleProvider{}, fmt.Errorf("mihomo native: duplicate rule provider name")
		}
	}
	for i, provider := range s.data.RuleProviders {
		if provider.ID == in.ID {
			old := provider
			cp := in
			s.data.RuleProviders[i] = &cp
			if err := s.saveLocked(); err != nil {
				s.data.RuleProviders[i] = old
				return RuleProvider{}, err
			}
			return cp, nil
		}
	}
	cp := in
	s.data.RuleProviders = append(s.data.RuleProviders, &cp)
	if err := s.saveLocked(); err != nil {
		s.data.RuleProviders = s.data.RuleProviders[:len(s.data.RuleProviders)-1]
		return RuleProvider{}, err
	}
	return cp, nil
}

func (s *Store) DeleteRuleProvider(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, provider := range s.data.RuleProviders {
		if provider.ID == id {
			old := s.data.RuleProviders
			s.data.RuleProviders = append(append([]*RuleProvider(nil), old[:i]...), old[i+1:]...)
			if err := s.saveLocked(); err != nil {
				s.data.RuleProviders = old
				return err
			}
			return nil
		}
	}
	return ErrNotFound
}

func (s *Store) ConfigRuleProviders() map[string]map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]map[string]interface{})
	for _, provider := range s.data.RuleProviders {
		if !provider.Enabled {
			continue
		}
		cfg := map[string]interface{}{"type": provider.Type, "behavior": provider.Behavior, "format": provider.Format, "interval": provider.Interval}
		if provider.URL != "" {
			cfg["url"] = provider.URL
		}
		if provider.Path != "" {
			cfg["path"] = provider.Path
		} else if provider.Type == "http" {
			cfg["path"] = "./rulesets/" + provider.Name + "." + provider.Format
		}
		if provider.Proxy != "" {
			cfg["proxy"] = normalizeBuiltin(provider.Proxy)
		}
		out[provider.Name] = cfg
	}
	return out
}

func (s *Store) CreateVLESS(raw string, preference, routingEngine EnginePreference) (ProxyNode, error) {
	node, err := CompileVLESS(raw, preference, routingEngine)
	if err != nil {
		return ProxyNode{}, err
	}
	now := time.Now().UTC()
	node.ID = newID()
	node.CreatedAt, node.UpdatedAt = now, now
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.data.Proxies {
		if existing.RawURI == node.RawURI {
			return ProxyNode{}, fmt.Errorf("mihomo native: duplicate proxy URI")
		}
	}
	s.data.Proxies = append(s.data.Proxies, node)
	if err := s.saveLocked(); err != nil {
		s.data.Proxies = s.data.Proxies[:len(s.data.Proxies)-1]
		return ProxyNode{}, err
	}
	return cloneNode(node), nil
}

// CreateProxy imports every endpoint represented by a share link atomically.
func (s *Store) CreateProxy(raw string, preference, routingEngine EnginePreference) ([]ProxyNode, error) {
	nodes, err := CompileURI(raw, preference, routingEngine)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for _, node := range nodes {
		node.ID = newID()
		node.CreatedAt, node.UpdatedAt = now, now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]bool, len(nodes))
	seenNames := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		key := node.RawURI + "\x00" + node.Name
		if seen[key] {
			return nil, fmt.Errorf("mihomo native: duplicate proxy endpoint")
		}
		seen[key] = true
		foldedName := strings.ToLower(strings.TrimSpace(node.Name))
		if seenNames[foldedName] {
			return nil, fmt.Errorf("mihomo native: duplicate proxy name %q", node.Name)
		}
		seenNames[foldedName] = true
		if err := s.validateProxyNameLocked(node.Name, "", ""); err != nil {
			return nil, err
		}
		for _, existing := range s.data.Proxies {
			if existing.RawURI == node.RawURI && existing.Name == node.Name {
				return nil, fmt.Errorf("mihomo native: duplicate proxy endpoint")
			}
		}
	}
	oldLen := len(s.data.Proxies)
	s.data.Proxies = append(s.data.Proxies, nodes...)
	if err := s.saveLocked(); err != nil {
		s.data.Proxies = s.data.Proxies[:oldLen]
		return nil, err
	}
	result := make([]ProxyNode, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, cloneNode(node))
	}
	return result, nil
}

func (s *Store) CreateManual(in ManualProxyInput, routingEngine EnginePreference) (ProxyNode, error) {
	node, err := CompileManual(in, routingEngine)
	if err != nil {
		return ProxyNode{}, err
	}
	now := time.Now().UTC()
	node.ID, node.CreatedAt, node.UpdatedAt = newID(), now, now
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateProxyNameLocked(node.Name, "", ""); err != nil {
		return ProxyNode{}, err
	}
	s.data.Proxies = append(s.data.Proxies, node)
	if err := s.saveLocked(); err != nil {
		s.data.Proxies = s.data.Proxies[:len(s.data.Proxies)-1]
		return ProxyNode{}, err
	}
	return cloneNode(node), nil
}

func (s *Store) DeleteProxy(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, node := range s.data.Proxies {
		if node.ID != id {
			continue
		}
		if ref := s.referenceToLocked(node.Name); ref != "" {
			return fmt.Errorf("mihomo native: cannot delete proxy %q; referenced by %s", node.Name, ref)
		}
		old := s.data.Proxies
		s.data.Proxies = append(append([]*ProxyNode(nil), old[:i]...), old[i+1:]...)
		if err := s.saveLocked(); err != nil {
			s.data.Proxies = old
			return err
		}
		return nil
	}
	return ErrNotFound
}

type UpdateProxyInput struct {
	URI              string
	Manual           *ManualProxyInput
	EnginePreference EnginePreference
	Enabled          bool
	RoutingEngine    EnginePreference
}

// UpdateProxy recompiles a standalone proxy and atomically migrates every
// name-based reference. Subscription member nodes are edited through their
// owning subscription so the selector cannot become inconsistent.
func (s *Store) UpdateProxy(id string, in UpdateProxyInput) (ProxyNode, error) {
	current, err := s.GetProxy(id)
	if err != nil {
		return ProxyNode{}, err
	}
	if current.SourceID != "" {
		return ProxyNode{}, fmt.Errorf("mihomo native: subscription members must be edited through their subscription")
	}
	if in.EnginePreference == "" {
		in.EnginePreference = current.EnginePreference
	}
	var compiled *ProxyNode
	if strings.TrimSpace(in.URI) != "" {
		nodes, compileErr := CompileURI(strings.TrimSpace(in.URI), in.EnginePreference, in.RoutingEngine)
		if compileErr != nil {
			return ProxyNode{}, compileErr
		}
		if len(nodes) != 1 {
			return ProxyNode{}, fmt.Errorf("mihomo native: editing a proxy requires exactly one endpoint, got %d", len(nodes))
		}
		compiled = nodes[0]
	} else if in.Manual != nil {
		manual := *in.Manual
		manual.EnginePreference = in.EnginePreference
		compiled, err = CompileManual(manual, in.RoutingEngine)
		if err != nil {
			return ProxyNode{}, err
		}
	} else {
		return ProxyNode{}, fmt.Errorf("mihomo native: proxy URI or manual config is required")
	}
	compiled.ID = current.ID
	compiled.SourceID = current.SourceID
	compiled.CreatedAt = current.CreatedAt
	compiled.UpdatedAt = time.Now().UTC()
	compiled.Enabled = in.Enabled
	compiled.Bridge = cloneBridge(current.Bridge)

	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, node := range s.data.Proxies {
		if node.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return ProxyNode{}, ErrNotFound
	}
	if err := s.validateProxyNameLocked(compiled.Name, id, ""); err != nil {
		return ProxyNode{}, err
	}
	before, err := json.Marshal(s.data)
	if err != nil {
		return ProxyNode{}, err
	}
	oldName := s.data.Proxies[index].Name
	s.data.Proxies[index] = compiled
	if oldName != compiled.Name {
		s.rewriteReferencesLocked(oldName, compiled.Name)
	}
	if err := s.saveLocked(); err != nil {
		_ = json.Unmarshal(before, &s.data)
		return ProxyNode{}, err
	}
	return cloneNode(compiled), nil
}

func (s *Store) validateProxyNameLocked(name, exceptProxyID, exceptSourceID string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("mihomo native: proxy name is required")
	}
	if strings.EqualFold(name, "DIRECT") || strings.EqualFold(name, "REJECT") || strings.EqualFold(name, autoNativeGroupName) {
		return fmt.Errorf("mihomo native: proxy name %q is reserved", name)
	}
	for _, node := range s.data.Proxies {
		if node.ID == exceptProxyID || (exceptSourceID != "" && node.SourceID == exceptSourceID) {
			continue
		}
		if strings.EqualFold(node.Name, name) {
			return fmt.Errorf("mihomo native: duplicate proxy name %q", name)
		}
	}
	for _, sub := range s.data.Subscriptions {
		if strings.EqualFold(sub.GroupName, name) {
			return fmt.Errorf("mihomo native: proxy name conflicts with subscription group %q", sub.Name)
		}
	}
	for _, group := range s.data.Groups {
		if strings.EqualFold(group.Name, name) {
			return fmt.Errorf("mihomo native: proxy name conflicts with group %q", group.Name)
		}
	}
	return nil
}

func (s *Store) rewriteReferencesLocked(oldName, newName string) {
	if oldName == "" || oldName == newName {
		return
	}
	for _, group := range s.data.Groups {
		for i, name := range group.Proxies {
			if name == oldName {
				group.Proxies[i] = newName
			}
		}
	}
	for _, rule := range s.data.Rules {
		if rule.Outbound == oldName {
			rule.Outbound = newName
		}
	}
	for _, provider := range s.data.RuleProviders {
		if provider.Proxy == oldName {
			provider.Proxy = newName
		}
	}
}

type CreateSubscriptionInput struct {
	Name             string
	URL              string
	Inline           string
	Format           SubscriptionFormat
	EnginePreference EnginePreference
	RefreshHours     int
	Enabled          bool
	RoutingEngine    EnginePreference
	Headers          map[string][]string
	Mode             string
	TestURL          string
	TestInterval     int
	TestTolerance    int
	FilterInclude    string
	FilterExclude    string
	BindInterface    string
}

type UpdateSubscriptionInput = CreateSubscriptionInput

func (s *Store) CreateSubscription(in CreateSubscriptionInput) (Subscription, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.URL, in.Inline = strings.TrimSpace(in.URL), strings.TrimSpace(in.Inline)
	if in.Name == "" {
		return Subscription{}, fmt.Errorf("mihomo native: subscription name is required")
	}
	if (in.URL == "") == (in.Inline == "") {
		return Subscription{}, fmt.Errorf("mihomo native: exactly one of URL or inline is required")
	}
	if in.Format == "" {
		in.Format = FormatAuto
	}
	if in.Format == FormatAuto && in.Inline != "" {
		in.Format = FormatShareLinks
	}
	if in.EnginePreference == "" {
		in.EnginePreference = EngineAuto
	}
	if in.RefreshHours < 0 {
		return Subscription{}, fmt.Errorf("mihomo native: refresh hours cannot be negative")
	}
	if in.Format == FormatMihomoProvider {
		parsed, err := url.Parse(in.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Subscription{}, fmt.Errorf("mihomo native: provider URL must use http or https")
		}
	}
	now := time.Now().UTC()
	mode := in.Mode
	switch mode {
	case "urltest":
		mode = "url-test"
	case "selector":
		mode = "select"
	}
	sub := &Subscription{
		ID: newID(), Name: in.Name, URL: in.URL, Inline: in.Inline,
		Format: in.Format, EnginePreference: in.EnginePreference,
		RefreshHours: in.RefreshHours, Enabled: in.Enabled, Headers: cloneHeaders(in.Headers),
		Mode: mode, TestURL: in.TestURL, TestInterval: in.TestInterval, TestTolerance: in.TestTolerance,
		FilterInclude: in.FilterInclude, FilterExclude: in.FilterExclude, BindInterface: in.BindInterface,
		CreatedAt: now, UpdatedAt: now,
	}
	sub.ProviderName = "mnp-" + sub.ID[:8]
	sub.GroupName = "Mihomo: " + sub.Name
	var imported []*ProxyNode
	if sub.Format == FormatShareLinks {
		for lineNo, raw := range strings.Split(sub.Inline, "\n") {
			raw = strings.TrimSpace(raw)
			if raw == "" || strings.HasPrefix(raw, "#") {
				continue
			}
			nodes, err := CompileURI(raw, sub.EnginePreference, in.RoutingEngine)
			if err != nil {
				return Subscription{}, fmt.Errorf("mihomo native: line %d: %w", lineNo+1, err)
			}
			for _, node := range nodes {
				node.ID = newID()
				node.SourceID = sub.ID
				node.CreatedAt, node.UpdatedAt = now, now
				imported = append(imported, node)
			}
		}
		if len(imported) == 0 {
			return Subscription{}, fmt.Errorf("mihomo native: subscription contains no supported proxy links")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.EqualFold(sub.GroupName, autoNativeGroupName) {
		return Subscription{}, fmt.Errorf("mihomo native: subscription name conflicts with reserved group %q", autoNativeGroupName)
	}
	for _, existing := range s.data.Subscriptions {
		if strings.EqualFold(existing.Name, sub.Name) || strings.EqualFold(existing.GroupName, sub.GroupName) {
			return Subscription{}, fmt.Errorf("mihomo native: duplicate subscription name")
		}
	}
	for _, group := range s.data.Groups {
		if strings.EqualFold(group.Name, sub.GroupName) {
			return Subscription{}, fmt.Errorf("mihomo native: subscription group conflicts with existing group")
		}
	}
	for _, existing := range s.data.Proxies {
		if strings.EqualFold(existing.Name, sub.GroupName) {
			return Subscription{}, fmt.Errorf("mihomo native: subscription group conflicts with proxy %q", existing.Name)
		}
	}
	seenImportedNames := make(map[string]bool, len(imported))
	for _, node := range imported {
		foldedName := strings.ToLower(strings.TrimSpace(node.Name))
		if seenImportedNames[foldedName] {
			return Subscription{}, fmt.Errorf("mihomo native: duplicate proxy name %q in subscription", node.Name)
		}
		seenImportedNames[foldedName] = true
		if strings.EqualFold(node.Name, sub.GroupName) {
			return Subscription{}, fmt.Errorf("mihomo native: proxy name %q conflicts with its subscription group", node.Name)
		}
		if err := s.validateProxyNameLocked(node.Name, "", ""); err != nil {
			return Subscription{}, err
		}
		for _, existing := range s.data.Proxies {
			if existing.RawURI == node.RawURI {
				return Subscription{}, fmt.Errorf("mihomo native: duplicate proxy URI")
			}
		}
	}
	s.data.Subscriptions = append(s.data.Subscriptions, sub)
	oldProxyLen := len(s.data.Proxies)
	s.data.Proxies = append(s.data.Proxies, imported...)
	if err := s.saveLocked(); err != nil {
		s.data.Subscriptions = s.data.Subscriptions[:len(s.data.Subscriptions)-1]
		s.data.Proxies = s.data.Proxies[:oldProxyLen]
		return Subscription{}, err
	}
	return *sub, nil
}

// UpdateSubscription replaces the editable source while preserving the
// provider key, bridge allocation and stable IDs of unchanged inline members.
func (s *Store) UpdateSubscription(id string, in UpdateSubscriptionInput) (Subscription, error) {
	current, err := s.GetSubscription(id)
	if err != nil {
		return Subscription{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	in.URL, in.Inline = strings.TrimSpace(in.URL), strings.TrimSpace(in.Inline)
	if in.Name == "" {
		return Subscription{}, fmt.Errorf("mihomo native: subscription name is required")
	}
	if in.Format == "" {
		in.Format = current.Format
	}
	// Early native-subscription builds persisted URL sources as "auto".
	// That value was never executable by the config generator and also made
	// the resource impossible to edit. Treat it as a one-way schema migration:
	// URL sources become Mihomo providers, inline sources become share-links.
	// Explicit formats remain immutable after creation.
	currentFormat := current.Format
	if currentFormat == FormatAuto {
		if current.Inline != "" {
			currentFormat = FormatShareLinks
		} else {
			currentFormat = FormatMihomoProvider
		}
	}
	if in.Format == FormatAuto {
		in.Format = currentFormat
	}
	if in.Format != currentFormat {
		return Subscription{}, fmt.Errorf("mihomo native: subscription source format cannot be changed")
	}
	if in.EnginePreference == "" {
		in.EnginePreference = current.EnginePreference
	}
	if in.RefreshHours < 0 {
		return Subscription{}, fmt.Errorf("mihomo native: refresh hours cannot be negative")
	}
	if in.Format == FormatMihomoProvider {
		if in.Inline != "" || in.URL == "" {
			return Subscription{}, fmt.Errorf("mihomo native: provider subscription requires URL")
		}
		parsed, parseErr := url.Parse(in.URL)
		if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return Subscription{}, fmt.Errorf("mihomo native: provider URL must use http or https")
		}
	} else if in.Format == FormatShareLinks {
		if in.URL != "" || in.Inline == "" {
			return Subscription{}, fmt.Errorf("mihomo native: inline subscription requires share links")
		}
	} else {
		return Subscription{}, fmt.Errorf("mihomo native: unsupported subscription format %q", current.Format)
	}

	now := time.Now().UTC()
	mode := in.Mode
	switch mode {
	case "urltest":
		mode = "url-test"
	case "selector":
		mode = "select"
	}
	if mode == "" {
		mode = current.Mode
	}
	testURL := in.TestURL
	if testURL == "" {
		testURL = current.TestURL
	}
	testInterval := in.TestInterval
	if testInterval == 0 {
		testInterval = current.TestInterval
	}
	testTolerance := in.TestTolerance
	if testTolerance == 0 {
		testTolerance = current.TestTolerance
	}
	filterInclude := in.FilterInclude
	filterExclude := in.FilterExclude
	bindInterface := in.BindInterface

	replacement := &Subscription{
		ID: current.ID, Name: in.Name, URL: in.URL, Inline: in.Inline,
		Format: in.Format, EnginePreference: in.EnginePreference,
		ProviderName: current.ProviderName, GroupName: "Mihomo: " + in.Name,
		Headers: cloneHeaders(in.Headers), RefreshHours: in.RefreshHours, Enabled: in.Enabled,
		Bridge: cloneBridge(current.Bridge), LastFetched: current.LastFetched, LastError: current.LastError,
		Mode: mode, TestURL: testURL, TestInterval: testInterval, TestTolerance: testTolerance,
		FilterInclude: filterInclude, FilterExclude: filterExclude, BindInterface: bindInterface,
		CreatedAt: current.CreatedAt, UpdatedAt: now,
	}
	var imported []*ProxyNode
	if in.Format == FormatShareLinks {
		seenNames := make(map[string]bool)
		for lineNo, raw := range strings.Split(in.Inline, "\n") {
			raw = strings.TrimSpace(raw)
			if raw == "" || strings.HasPrefix(raw, "#") {
				continue
			}
			nodes, compileErr := CompileURI(raw, in.EnginePreference, in.RoutingEngine)
			if compileErr != nil {
				return Subscription{}, fmt.Errorf("mihomo native: line %d: %w", lineNo+1, compileErr)
			}
			for _, node := range nodes {
				key := strings.ToLower(strings.TrimSpace(node.Name))
				if seenNames[key] {
					return Subscription{}, fmt.Errorf("mihomo native: duplicate proxy name %q in subscription", node.Name)
				}
				seenNames[key] = true
				node.SourceID = id
				node.CreatedAt, node.UpdatedAt = now, now
				imported = append(imported, node)
			}
		}
		if len(imported) == 0 {
			return Subscription{}, fmt.Errorf("mihomo native: subscription contains no supported proxy links")
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	subIndex := -1
	for i, sub := range s.data.Subscriptions {
		if sub.ID == id {
			subIndex = i
			break
		}
	}
	if subIndex < 0 {
		return Subscription{}, ErrNotFound
	}
	if strings.EqualFold(replacement.GroupName, autoNativeGroupName) {
		return Subscription{}, fmt.Errorf("mihomo native: subscription name conflicts with reserved group %q", autoNativeGroupName)
	}
	for _, sub := range s.data.Subscriptions {
		if sub.ID != id && (strings.EqualFold(sub.Name, replacement.Name) || strings.EqualFold(sub.GroupName, replacement.GroupName)) {
			return Subscription{}, fmt.Errorf("mihomo native: duplicate subscription name")
		}
	}
	for _, group := range s.data.Groups {
		if strings.EqualFold(group.Name, replacement.GroupName) {
			return Subscription{}, fmt.Errorf("mihomo native: subscription group conflicts with existing group")
		}
	}
	for _, node := range s.data.Proxies {
		if node.SourceID != id && strings.EqualFold(node.Name, replacement.GroupName) {
			return Subscription{}, fmt.Errorf("mihomo native: subscription group conflicts with proxy %q", node.Name)
		}
	}
	for _, node := range imported {
		if strings.EqualFold(node.Name, replacement.GroupName) {
			return Subscription{}, fmt.Errorf("mihomo native: proxy name %q conflicts with its subscription group", node.Name)
		}
		if err := s.validateProxyNameLocked(node.Name, "", id); err != nil {
			return Subscription{}, err
		}
	}

	oldByName := make(map[string]*ProxyNode)
	oldByURI := make(map[string]*ProxyNode)
	oldNames := make(map[string]bool)
	for _, node := range s.data.Proxies {
		if node.SourceID != id {
			continue
		}
		oldByName[strings.ToLower(node.Name)] = node
		if node.RawURI != "" {
			oldByURI[node.RawURI] = node
		}
		oldNames[node.Name] = true
	}
	newNames := make(map[string]bool, len(imported))
	for _, node := range imported {
		newNames[node.Name] = true
		old := oldByName[strings.ToLower(node.Name)]
		if old == nil && node.RawURI != "" {
			old = oldByURI[node.RawURI]
		}
		if old != nil {
			node.ID, node.CreatedAt = old.ID, old.CreatedAt
		} else {
			node.ID = newID()
		}
	}
	for oldName := range oldNames {
		if !newNames[oldName] {
			if ref := s.referenceToLocked(oldName); ref != "" {
				return Subscription{}, fmt.Errorf("mihomo native: cannot remove subscription member %q; referenced by %s", oldName, ref)
			}
		}
	}

	before, err := json.Marshal(s.data)
	if err != nil {
		return Subscription{}, err
	}
	oldGroup := s.data.Subscriptions[subIndex].GroupName
	s.data.Subscriptions[subIndex] = replacement
	filtered := make([]*ProxyNode, 0, len(s.data.Proxies)+len(imported))
	for _, node := range s.data.Proxies {
		if node.SourceID != id {
			filtered = append(filtered, node)
		}
	}
	s.data.Proxies = append(filtered, imported...)
	if oldGroup != replacement.GroupName {
		s.rewriteReferencesLocked(oldGroup, replacement.GroupName)
	}
	if err := s.saveLocked(); err != nil {
		_ = json.Unmarshal(before, &s.data)
		return Subscription{}, err
	}
	copy := *replacement
	copy.Headers = cloneHeaders(replacement.Headers)
	copy.Bridge = cloneBridge(replacement.Bridge)
	return copy, nil
}

func (s *Store) referenceToLocked(name string) string {
	if s.inUseChecker != nil {
		if inUse, reason := s.inUseChecker("name", "", name); inUse {
			return reason
		}
	}
	for _, group := range s.data.Groups {
		for _, member := range group.Proxies {
			if member == name {
				return "group " + group.Name
			}
		}
	}
	for _, rule := range s.data.Rules {
		if rule.Outbound == name {
			return "routing rule " + rule.ID
		}
	}
	for _, provider := range s.data.RuleProviders {
		if provider.Proxy == name {
			return "rule provider " + provider.Name
		}
	}
	return ""
}

func (s *Store) populateMembersLocked(sub *Subscription) {
	if sub == nil {
		return
	}
	if sub.Format == FormatShareLinks {
		for _, node := range s.data.Proxies {
			if node.SourceID != sub.ID {
				continue
			}
			m := MemberInfo{
				Tag:      node.Name,
				Label:    node.Name,
				Protocol: node.Protocol,
			}
			if srv, ok := node.NativeConfig["server"].(string); ok {
				m.Server = srv
			}
			if pt, ok := node.NativeConfig["port"].(float64); ok {
				m.Port = uint16(pt)
			} else if pt, ok := node.NativeConfig["port"].(int); ok {
				m.Port = uint16(pt)
			}
			if sni, ok := node.NativeConfig["servername"].(string); ok && sni != "" {
				m.SNI = sni
			} else if sni, ok := node.NativeConfig["sni"].(string); ok && sni != "" {
				m.SNI = sni
			}
			if node.Transport != "" {
				m.Transport = node.Transport
			} else if net, ok := node.NativeConfig["network"].(string); ok {
				m.Transport = net
			}
			if node.NativeConfig["reality-opts"] != nil {
				m.Security = "reality"
			} else if tls, ok := node.NativeConfig["tls"].(bool); ok && tls {
				m.Security = "tls"
			}
			sub.Members = append(sub.Members, m)
		}
		return
	}

	providerFile := filepath.Join(filepath.Dir(s.path), "providers", sub.ProviderName+".yaml")
	b, err := os.ReadFile(providerFile)
	if err != nil || len(b) == 0 {
		return
	}
	var doc struct {
		Proxies []map[string]interface{} `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(b, &doc); err == nil && len(doc.Proxies) > 0 {
		for _, p := range doc.Proxies {
			name, _ := p["name"].(string)
			proto, _ := p["type"].(string)
			server, _ := p["server"].(string)
			var port uint16
			if pt, ok := p["port"].(int); ok {
				port = uint16(pt)
			} else if pt, ok := p["port"].(float64); ok {
				port = uint16(pt)
			}
			sni, _ := p["sni"].(string)
			if sni == "" {
				sni, _ = p["servername"].(string)
			}
			transport, _ := p["network"].(string)
			security := ""
			if p["reality-opts"] != nil {
				security = "reality"
			} else if tls, ok := p["tls"].(bool); ok && tls {
				security = "tls"
			}
			sub.Members = append(sub.Members, MemberInfo{
				Tag:       name,
				Label:     name,
				Protocol:  proto,
				Server:    server,
				Port:      port,
				SNI:       sni,
				Transport: transport,
				Security:  security,
			})
		}
	}
}

func (s *Store) ListSubscriptions() []Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Subscription, 0, len(s.data.Subscriptions))
	for _, sub := range s.data.Subscriptions {
		copy := *sub
		copy.Headers = cloneHeaders(sub.Headers)
		copy.Bridge = cloneBridge(sub.Bridge)
		s.populateMembersLocked(&copy)
		out = append(out, copy)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (s *Store) GetSubscription(id string) (Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sub := range s.data.Subscriptions {
		if sub.ID == id {
			copy := *sub
			copy.Headers = cloneHeaders(sub.Headers)
			copy.Bridge = cloneBridge(sub.Bridge)
			s.populateMembersLocked(&copy)
			return copy, nil
		}
	}
	return Subscription{}, ErrNotFound
}

type SubscriptionSnapshot struct {
	Subscription Subscription
	Proxies      []ProxyNode
}

func (s *Store) SnapshotSubscription(id string) (SubscriptionSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var snapshot SubscriptionSnapshot
	found := false
	for _, sub := range s.data.Subscriptions {
		if sub.ID == id {
			snapshot.Subscription = *sub
			snapshot.Subscription.Headers = cloneHeaders(sub.Headers)
			snapshot.Subscription.Bridge = cloneBridge(sub.Bridge)
			found = true
			break
		}
	}
	if !found {
		return SubscriptionSnapshot{}, ErrNotFound
	}
	for _, node := range s.data.Proxies {
		if node.SourceID == id {
			snapshot.Proxies = append(snapshot.Proxies, cloneNode(node))
		}
	}
	return snapshot, nil
}

// RestoreSubscription is used to roll back a failed transactional delete.
func (s *Store) RestoreSubscription(snapshot SubscriptionSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.data.Subscriptions {
		if existing.ID == snapshot.Subscription.ID {
			return nil
		}
	}
	oldSubLen, oldProxyLen := len(s.data.Subscriptions), len(s.data.Proxies)
	sub := snapshot.Subscription
	sub.Headers = cloneHeaders(snapshot.Subscription.Headers)
	sub.Bridge = cloneBridge(snapshot.Subscription.Bridge)
	s.data.Subscriptions = append(s.data.Subscriptions, &sub)
	for i := range snapshot.Proxies {
		node := cloneNode(&snapshot.Proxies[i])
		s.data.Proxies = append(s.data.Proxies, &node)
	}
	if err := s.saveLocked(); err != nil {
		s.data.Subscriptions = s.data.Subscriptions[:oldSubLen]
		s.data.Proxies = s.data.Proxies[:oldProxyLen]
		return err
	}
	return nil
}

// DeleteSubscription also removes nodes imported from that subscription. A
// provider subscription has no persisted nodes, so only its definition goes.
func (s *Store) DeleteSubscription(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, sub := range s.data.Subscriptions {
		if sub.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrNotFound
	}
	sub := s.data.Subscriptions[index]
	if ref := s.referenceToLocked(sub.GroupName); ref != "" {
		return fmt.Errorf("mihomo native: cannot delete subscription %q; group is referenced by %s", sub.Name, ref)
	}
	for _, node := range s.data.Proxies {
		if node.SourceID != id {
			continue
		}
		if ref := s.referenceToLocked(node.Name); ref != "" {
			return fmt.Errorf("mihomo native: cannot delete subscription %q; member %q is referenced by %s", sub.Name, node.Name, ref)
		}
	}
	oldSubs, oldProxies := s.data.Subscriptions, s.data.Proxies
	s.data.Subscriptions = append(append([]*Subscription(nil), oldSubs[:index]...), oldSubs[index+1:]...)
	filtered := make([]*ProxyNode, 0, len(oldProxies))
	for _, node := range oldProxies {
		if node.SourceID != id {
			filtered = append(filtered, node)
		}
	}
	s.data.Proxies = filtered
	if err := s.saveLocked(); err != nil {
		s.data.Subscriptions, s.data.Proxies = oldSubs, oldProxies
		return err
	}
	return nil
}

func (s *Store) RecordSubscriptionRefresh(id string, refreshErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sub := range s.data.Subscriptions {
		if sub.ID != id {
			continue
		}
		oldFetched, oldError, oldUpdated := sub.LastFetched, sub.LastError, sub.UpdatedAt
		sub.UpdatedAt = time.Now().UTC()
		if refreshErr == nil {
			sub.LastFetched, sub.LastError = sub.UpdatedAt, ""
		} else {
			sub.LastError = refreshErr.Error()
		}
		if err := s.saveLocked(); err != nil {
			sub.LastFetched, sub.LastError, sub.UpdatedAt = oldFetched, oldError, oldUpdated
			return err
		}
		return nil
	}
	return ErrNotFound
}

func cloneNode(in *ProxyNode) ProxyNode {
	out := *in
	out.Bridge = cloneBridge(in.Bridge)
	if in.NativeConfig != nil {
		b, _ := json.Marshal(in.NativeConfig)
		out.NativeConfig = nil
		_ = json.Unmarshal(b, &out.NativeConfig)
	}
	return out
}

func cloneBridge(in *ProxyBridge) *ProxyBridge {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneHeaders(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for name, values := range in {
		out[name] = append([]string(nil), values...)
	}
	return out
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
