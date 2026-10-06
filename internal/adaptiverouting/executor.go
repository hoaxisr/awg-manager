package adaptiverouting

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

const (
	TunInterfaceName = "awgsus0"
)

var ifaceByName = net.InterfaceByName

type Executor interface {
	Prepare(ctx context.Context, egress ResolvedEgress) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	TearDown(ctx context.Context) error
	InterfaceName() string
}

// SystemExecutor handles kernel-managed tunnels (e.g. Wireguard2, nwg0).
type SystemExecutor struct {
	iface string
	mu    sync.Mutex
}

func NewSystemExecutor() *SystemExecutor {
	return &SystemExecutor{}
}

func (s *SystemExecutor) Prepare(ctx context.Context, egress ResolvedEgress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dev := resolveKernelDev(egress.Interface)
	if dev == "" {
		return fmt.Errorf("system tunnel %s has no kernel interface", egress.DisplayName)
	}
	s.iface = dev
	return nil
}

func (s *SystemExecutor) Commit(ctx context.Context) error {
	return nil
}

func (s *SystemExecutor) Rollback(ctx context.Context) error {
	return nil
}

func (s *SystemExecutor) TearDown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.iface = ""
	return nil
}

func (s *SystemExecutor) InterfaceName() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.iface
}

// MihomoAdaptiveConfig describes the dynamic egress configuration for Mihomo.
type MihomoAdaptiveConfig struct {
	Enabled       bool   `json:"enabled"`
	Device        string `json:"device"`
	SelectedGroup string `json:"selectedGroup"`
}

// MihomoExecutor configures Mihomo to host awgsus0 and route to the selected group/proxy.
type MihomoExecutor struct {
	nativeStore MihomoNativeProvider
	reloadFn    func(ctx context.Context) error
	groupName   string
	committed   bool
	mu          sync.Mutex
}

func NewMihomoExecutor(nativeStore MihomoNativeProvider) *MihomoExecutor {
	return &MihomoExecutor{
		nativeStore: nativeStore,
	}
}

func isNilProvider(p MihomoNativeProvider) bool {
	if p == nil {
		return true
	}
	v := reflect.ValueOf(p)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice, reflect.UnsafePointer:
		return v.IsNil()
	default:
		return false
	}
}

func (m *MihomoExecutor) SetReloadFunc(fn func(ctx context.Context) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reloadFn = fn
}

func (m *MihomoExecutor) Prepare(ctx context.Context, egress ResolvedEgress) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	targetName := egress.DisplayName
	if !isNilProvider(m.nativeStore) {
		switch egress.Ref.Kind {
		case EgressKindMihomoGroup:
			for _, g := range m.nativeStore.ListGroups() {
				if g.ID == egress.Ref.ResourceID || g.Name == egress.Ref.ResourceID {
					targetName = g.Name
					break
				}
			}
		case EgressKindMihomoProxy:
			for _, p := range m.nativeStore.ListProxies() {
				if p.ID == egress.Ref.ResourceID || p.Name == egress.Ref.ResourceID {
					targetName = p.Name
					break
				}
			}
		case EgressKindMihomoSubscription:
			for _, sub := range m.nativeStore.ListSubscriptions() {
				if sub.ID == egress.Ref.ResourceID || sub.Name == egress.Ref.ResourceID {
					targetName = sub.Name
					break
				}
			}
		}
	}

	if targetName == "" {
		if egress.DisplayName != "" {
			targetName = egress.DisplayName
		} else if egress.Ref.ResourceID != "" {
			targetName = egress.Ref.ResourceID
		} else {
			return fmt.Errorf("unable to resolve Mihomo target name for %s", egress.Ref.ResourceID)
		}
	}

	m.groupName = targetName
	return nil
}

func (m *MihomoExecutor) Commit(ctx context.Context) error {
	m.mu.Lock()
	m.committed = true
	reload := m.reloadFn
	m.mu.Unlock()

	if reload != nil {
		if err := reload(ctx); err != nil {
			m.mu.Lock()
			m.committed = false
			m.mu.Unlock()
			return fmt.Errorf("перезагрузка Mihomo с адаптивным выходом: %w", err)
		}
	}

	// Wait up to 5 seconds for awgsus0 interface to be created by Mihomo
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := ifaceByName(TunInterfaceName); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			m.mu.Lock()
			m.committed = false
			m.mu.Unlock()
			_ = m.TearDown(context.Background())
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	m.mu.Lock()
	m.committed = false
	m.mu.Unlock()
	_ = m.TearDown(context.Background())
	return fmt.Errorf("интерфейс %s не был поднят Mihomo в течение 5 секунд", TunInterfaceName)
}

func (m *MihomoExecutor) Rollback(ctx context.Context) error {
	return m.TearDown(ctx)
}

func (m *MihomoExecutor) TearDown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.committed = false
	m.groupName = ""
	reload := m.reloadFn
	m.mu.Unlock()

	if reload != nil {
		_ = reload(ctx)
	}
	return nil
}

func (m *MihomoExecutor) InterfaceName() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.committed {
		return ""
	}
	if _, err := ifaceByName(TunInterfaceName); err != nil {
		return ""
	}
	return TunInterfaceName
}

func (m *MihomoExecutor) AdaptiveConfig() *MihomoAdaptiveConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.committed || m.groupName == "" {
		return nil
	}
	return &MihomoAdaptiveConfig{
		Enabled:       true,
		Device:        TunInterfaceName,
		SelectedGroup: m.groupName,
	}
}

// SingboxExecutor writes 25-adaptive-egress.json to sing-box orchestrator.
type SingboxExecutor struct {
	orch        *orchestrator.Orchestrator
	outboundTag string
	committed   bool
	mu          sync.Mutex
}

func NewSingboxExecutor(orch *orchestrator.Orchestrator) *SingboxExecutor {
	return &SingboxExecutor{
		orch: orch,
	}
}

func (s *SingboxExecutor) Prepare(ctx context.Context, egress ResolvedEgress) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	targetTag := egress.Ref.ResourceID
	if targetTag == "" {
		targetTag = egress.DisplayName
	}
	s.outboundTag = targetTag
	return nil
}

func (s *SingboxExecutor) Commit(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.orch == nil {
		s.committed = true
		return nil
	}

	_ = s.orch.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotAdaptiveEgress,
		Filename: "25-adaptive-egress.json",
	})

	cfg := map[string]any{
		"inbounds": []map[string]any{
			{
				"type":           "tun",
				"tag":            "awgm-susanin-in",
				"interface_name": TunInterfaceName,
				"inet4_address":  "172.19.0.1/30",
				"auto_route":     false,
				"strict_route":   false,
				"stack":          "mixed",
			},
		},
		"route": map[string]any{
			"rules": []map[string]any{
				{
					"inbound":  []string{"awgm-susanin-in"},
					"outbound": s.outboundTag,
				},
			},
		},
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal adaptive sing-box config: %w", err)
	}

	if err := s.orch.Save(orchestrator.SlotAdaptiveEgress, data); err != nil {
		return fmt.Errorf("save sing-box adaptive slot: %w", err)
	}
	if err := s.orch.SetEnabled(orchestrator.SlotAdaptiveEgress, true); err != nil {
		return fmt.Errorf("enable sing-box adaptive slot: %w", err)
	}
	if err := s.orch.ReloadNow(); err != nil {
		_ = s.orch.SetEnabled(orchestrator.SlotAdaptiveEgress, false)
		return fmt.Errorf("reload sing-box immediately: %w", err)
	}

	s.committed = true

	// Wait up to 5 seconds for awgsus0 interface to be created by sing-box
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := ifaceByName(TunInterfaceName); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			if s.orch != nil {
				_ = s.orch.SetEnabled(orchestrator.SlotAdaptiveEgress, false)
				_ = s.orch.ReloadNow()
			}
			s.committed = false
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	if s.orch != nil {
		_ = s.orch.SetEnabled(orchestrator.SlotAdaptiveEgress, false)
		_ = s.orch.ReloadNow()
	}
	s.committed = false
	return fmt.Errorf("интерфейс %s не был поднят sing-box в течение 5 секунд", TunInterfaceName)
}

func (s *SingboxExecutor) Rollback(ctx context.Context) error {
	return s.TearDown(ctx)
}

func (s *SingboxExecutor) TearDown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.orch != nil {
		_ = s.orch.SetEnabled(orchestrator.SlotAdaptiveEgress, false)
		_ = s.orch.ReloadNow()
	}
	s.committed = false
	s.outboundTag = ""
	return nil
}

func (s *SingboxExecutor) InterfaceName() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.committed {
		return ""
	}
	if _, err := ifaceByName(TunInterfaceName); err != nil {
		return ""
	}
	return TunInterfaceName
}
