package uplink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// WANDNS stores manager-only overrides. An absent entry means provider DHCP/RC
// DNS; tunnel-internal DNS and the router's own resolver are never changed.
type WANDNS struct {
	mu    sync.Mutex
	path  string
	value WANDNSConfig
}
type WANDNSConfig struct {
	Revision  uint64              `json:"revision"`
	Overrides map[string][]string `json:"overrides"`
}
type WANDNSUpdate struct {
	Provider string   `json:"provider"`
	Revision uint64   `json:"revision"`
	Servers  []string `json:"servers"` // empty restores automatic selection
}

func normalizeWANDNS(servers []string) ([]string, error) {
	if len(servers) > 8 {
		return nil, fmt.Errorf("specify at most eight DNS servers")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range servers {
		v, err := ParseDNSServer(strings.TrimSpace(raw))
		if err != nil {
			return nil, err
		}
		value := v.String()
		if v.Port() == 53 {
			value = v.Addr().String()
		}
		if seen[value] {
			return nil, fmt.Errorf("duplicate DNS server")
		}
		seen[value] = true
		out = append(out, value)
	}
	return out, nil
}
func OpenWANDNS(path string) (*WANDNS, error) {
	d := &WANDNS{path: path, value: WANDNSConfig{Overrides: map[string][]string{}}}
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return d, nil
	}
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(b, &d.value); e != nil {
		return nil, e
	}
	if d.value.Overrides == nil {
		d.value.Overrides = map[string][]string{}
	}
	for id, servers := range d.value.Overrides {
		if id == "" || len(servers) == 0 {
			return nil, fmt.Errorf("invalid WAN DNS override")
		}
		v, e := normalizeWANDNS(servers)
		if e != nil {
			return nil, e
		}
		d.value.Overrides[id] = v
	}
	return d, nil
}
func cloneWANDNS(v WANDNSConfig) WANDNSConfig {
	out := WANDNSConfig{Revision: v.Revision, Overrides: map[string][]string{}}
	for k, s := range v.Overrides {
		out.Overrides[k] = append([]string(nil), s...)
	}
	return out
}
func (d *WANDNS) Snapshot() WANDNSConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return cloneWANDNS(d.value)
}
func (d *WANDNS) Resolve(ctx context.Context, provider, device string, automatic DNSForDevice) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	servers := append([]string(nil), d.value.Overrides[provider]...)
	d.mu.Unlock()
	if len(servers) > 0 {
		return servers, nil
	}
	if automatic == nil {
		return nil, ErrNoExplicitProviderDNS
	}
	return automatic(ctx, device)
}
func (d *WANDNS) put(u WANDNSUpdate) (WANDNSConfig, error) {
	servers, e := normalizeWANDNS(u.Servers)
	if e != nil {
		return WANDNSConfig{}, e
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if u.Revision != d.value.Revision {
		return WANDNSConfig{}, ErrConflict
	}
	next := cloneWANDNS(d.value)
	next.Revision++
	if len(servers) == 0 {
		delete(next.Overrides, u.Provider)
	} else {
		next.Overrides[u.Provider] = servers
	}
	b, e := json.MarshalIndent(next, "", "  ")
	if e != nil {
		return WANDNSConfig{}, e
	}
	if e = storage.AtomicWritePerm(d.path, b, 0600); e != nil {
		return WANDNSConfig{}, e
	}
	d.value = next
	return cloneWANDNS(next), nil
}

// UpdateWANDNS serializes with switching. Current proxy paths are reapplied
// through their adapter on the next tick, including after a failed save/retry.
// Сериализация — замками ref всех политик, читающих DNS провайдера: иначе
// сверка, начатая до правки, сняла бы флаг перепривязки за старый DNS.
func (c *Controller) UpdateWANDNS(ctx context.Context, u WANDNSUpdate) (WANDNSConfig, error) {
	var refs []string
	for _, p := range c.store.List() {
		if p.Enabled && c.Capabilities(p.Target).ProviderDNS {
			refs = append(refs, p.Target.Ref)
		}
	}
	unlock, err := c.lockRefs(ctx, refs...)
	if err != nil {
		return WANDNSConfig{}, err
	}
	defer unlock()
	if c.paused.Load() {
		return WANDNSConfig{}, fmt.Errorf("%w: uplink controller paused for backup", ErrConflict)
	}
	if c.WANDNS == nil {
		return WANDNSConfig{}, ErrUnsupported
	}
	provider, e := providerFor(c.Providers(), u.Provider)
	if e != nil {
		return WANDNSConfig{}, e
	}
	value, e := c.WANDNS.put(u)
	if e != nil {
		return WANDNSConfig{}, e
	}
	locked := map[string]bool{}
	for _, ref := range refs {
		locked[ref] = true
	}
	c.stateMu.Lock()
	for _, p := range c.store.List() {
		// Политика, появившаяся после снимка refs, сверится по своему
		// Activate: DNS берётся адаптером в момент применения.
		if !p.Enabled || !c.Capabilities(p.Target).ProviderDNS || !locked[p.Target.Ref] {
			continue
		}
		s := c.states[p.ID]
		if s.Device == provider.Device {
			c.dnsDirty[p.ID] = true
			s.NextCheck = c.now()
			c.states[p.ID] = s
		}
	}
	c.stateMu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return value, nil
}
