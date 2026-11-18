// Package systemwg binds existing NDMS client interfaces to the common uplink
// controller. It never imports interfaces or takes ownership of server keys.
package systemwg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/wireguard"
)

type Deps struct {
	Directory string
	Transport *transport.Client
	Queries   *query.Queries
	Commands  *command.Commands
	Providers func() []uplink.Provider
	Excluded  func(context.Context, string) (bool, error)
}
type Adapter struct {
	deps    Deps
	queries *query.Queries
	manager *wireguard.Managed
	op      *nwg.OperatorNativeWG
	mu      sync.Mutex
}

func New(d Deps) (*Adapter, error) {
	if d.Directory == "" || d.Transport == nil || d.Commands == nil || d.Providers == nil || d.Excluded == nil {
		return nil, errors.New("incomplete system WG dependencies")
	}
	q := d.Queries
	if q == nil {
		q = query.NewQueries(query.Deps{Getter: d.Transport, Logger: query.NopLogger()})
	}
	a := &Adapter{deps: d, queries: q, op: nwg.NewOperator(q, d.Commands, d.Transport, nil)}
	m, err := wireguard.NewManaged(wireguard.ManagedDeps{
		Directory: filepath.Join(d.Directory, "uplink-system-wireguard"),
		Providers: d.Providers,
		Lookup: func(ctx context.Context, id string) (wireguard.ManagedTarget, error) {
			_, t, e := a.lookup(ctx, id)
			return t, e
		},
		List: func(ctx context.Context) ([]wireguard.ManagedTarget, error) { return a.list(ctx) },
		WithLock: func(ctx context.Context, _ string, fn func() error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			return fn()
		},
		PeerMark: func(ctx context.Context, target wireguard.ManagedTarget) (uint32, error) {
			n, e := index(target.Ref)
			if e != nil {
				return 0, e
			}
			s := &storage.AWGTunnel{ID: target.Ref, Backend: "systemwg", NWGIndex: n}
			s.Peer.PublicKey, s.Peer.Endpoint = target.Peer, target.Endpoint
			return a.op.NativeUplinkPeerMark(ctx, s)
		},
		PeerMarks: a.op.NativeUplinkPeerMarks,
		ApplyBinding: func(ctx context.Context, id, provider, _ string) error {
			s, _, e := a.lookup(ctx, id)
			if e != nil {
				return e
			}
			return a.op.ApplySystemUplink(ctx, s, provider)
		},
	})
	if err != nil {
		return nil, err
	}
	a.manager = m
	return a, nil
}

func index(id string) (int, error) {
	if !strings.HasPrefix(id, "Wireguard") {
		return 0, fmt.Errorf("%w: non-Wireguard id %q", uplink.ErrUnsupported, id)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(id, "Wireguard"))
	if err != nil || n < 0 || n >= nwg.MaxTunnels || fmt.Sprint("Wireguard", n) != id {
		return 0, fmt.Errorf("%w: out-of-range Wireguard id %q", uplink.ErrUnsupported, id)
	}
	return n, nil
}

func (a *Adapter) lookup(ctx context.Context, id string) (*storage.AWGTunnel, wireguard.ManagedTarget, error) {
	var target wireguard.ManagedTarget
	n, err := index(id)
	if err != nil {
		return nil, target, err
	}
	excluded, err := a.deps.Excluded(ctx, id)
	if err != nil {
		return nil, target, err
	}
	if excluded {
		return nil, target, fmt.Errorf("%w: managed, external or server interface", uplink.ErrUnsupported)
	}
	raw, ok, err := a.queries.WGServers.InterfaceRuntimeLive(ctx, id)
	if err != nil {
		return nil, target, err
	}
	if !ok {
		return nil, target, fmt.Errorf("%w: system WG interface %q absent in runtime snapshot", uplink.ErrUnsupported, id)
	}
	rcRaw, rcOK, err := a.queries.WGServers.InterfaceRCRawRecent(ctx, id, 2*time.Second)
	if err != nil {
		return nil, target, err
	}
	if !rcOK {
		return nil, target, fmt.Errorf("%w: system WG interface %q absent in running config", uplink.ErrUnsupported, id)
	}
	return a.parseTarget(id, n, raw, rcRaw)
}

func (a *Adapter) parseTarget(id string, n int, runtimeRaw, rcRaw json.RawMessage) (*storage.AWGTunnel, wireguard.ManagedTarget, error) {
	var target wireguard.ManagedTarget
	var runtime struct {
		ID, State, Description string
		Wireguard              struct {
			PublicKey string `json:"public-key"`
			Peer      []struct {
				Key     string `json:"public-key"`
				Address string `json:"remote-endpoint-address"`
				Port    int    `json:"remote-port"`
			}
		}
	}
	if err := json.Unmarshal(runtimeRaw, &runtime); err != nil {
		return nil, target, err
	}
	if runtime.ID != id || len(runtime.Wireguard.Peer) != 1 {
		return nil, target, fmt.Errorf("%w: system WG runtime id=%q (%q) peers=%d", uplink.ErrUnsupported, runtime.ID, id, len(runtime.Wireguard.Peer))
	}
	var config struct {
		Description string
		IP          struct {
			DNS []struct {
				Address string `json:"name-server"`
			} `json:"name-server"`
		}
		Wireguard struct {
			Peer []struct {
				Key      string
				Endpoint struct{ Address string }
				Allowed  []struct{ Address, Mask string } `json:"allow-ips"`
			}
		}
	}
	if err := json.Unmarshal(rcRaw, &config); err != nil {
		return nil, target, err
	}
	if len(config.Wireguard.Peer) != 1 {
		return nil, target, fmt.Errorf("%w: system WG config peers=%d", uplink.ErrUnsupported, len(config.Wireguard.Peer))
	}
	p, r := config.Wireguard.Peer[0], runtime.Wireguard.Peer[0]
	peerKey, err := base64.StdEncoding.DecodeString(p.Key)
	if err != nil || len(peerKey) != 32 {
		return nil, target, fmt.Errorf("%w: invalid system WG peer key len=%d", uplink.ErrUnsupported, len(peerKey))
	}
	endpoint, err := netip.ParseAddrPort(p.Endpoint.Address)
	if err != nil || !endpoint.Addr().Is4() || !endpoint.Addr().IsGlobalUnicast() || endpoint.Port() == 0 || p.Key != r.Key || endpoint.Addr().String() != r.Address || int(endpoint.Port()) != r.Port {
		return nil, target, fmt.Errorf("%w: system WG peer/endpoint mismatch", uplink.ErrUnsupported)
	}
	var prefixes []string
	for _, v := range p.Allowed {
		switch {
		case v.Address == "0.0.0.0" && v.Mask == "0.0.0.0":
			prefixes = append(prefixes, "0.0.0.0/0")
		case v.Address == "::" && v.Mask == "0":
			prefixes = append(prefixes, "::/0")
		default:
			return nil, target, fmt.Errorf("%w: unsupported system WG allow-ips entry %s/%s", uplink.ErrUnsupported, v.Address, v.Mask)
		}
	}
	if strings.Join(prefixes, " ") != "0.0.0.0/0" && strings.Join(prefixes, " ") != "0.0.0.0/0 ::/0" {
		return nil, target, fmt.Errorf("%w: system WG requires default allow-ips, got %q", uplink.ErrUnsupported, strings.Join(prefixes, " "))
	}
	var dns []string
	for _, d := range config.IP.DNS {
		if _, err = uplink.ParseDNSServer(d.Address); err != nil {
			return nil, target, fmt.Errorf("%w: invalid system tunnel DNS", uplink.ErrUnsupported)
		}
		dns = append(dns, d.Address)
	}
	if len(dns) == 0 {
		return nil, target, fmt.Errorf("%w: explicit system tunnel DNS required", uplink.ErrUnsupported)
	}
	s := &storage.AWGTunnel{ID: id, Backend: "systemwg", NWGIndex: n, Enabled: runtime.State == "up"}
	s.Peer.PublicKey, s.Peer.Endpoint, s.Peer.AllowedIPs = p.Key, endpoint.String(), prefixes
	public, err := base64.StdEncoding.DecodeString(runtime.Wireguard.PublicKey)
	if err != nil || len(public) != 32 {
		return nil, target, fmt.Errorf("%w: system client public identity missing", uplink.ErrUnsupported)
	}
	target = wireguard.ManagedTarget{Ref: id, Label: runtime.Description, Interface: nwg.NewNWGNames(n).IfaceName, Peer: p.Key, Endpoint: endpoint.String(), TunnelDNS: dns, AllowedIPs: prefixes, LocalPublicKey: runtime.Wireguard.PublicKey, Enabled: s.Enabled}
	return s, target, nil
}

func (a *Adapter) list(ctx context.Context) ([]wireguard.ManagedTarget, error) {
	interfaces, err := a.queries.WGServers.WireguardRuntimesMaxAge(ctx, query.SnapshotRecent)
	if err != nil {
		return nil, err
	}
	rcTree, err := a.queries.WGServers.InterfaceRCTreeRecent(ctx, 2*time.Second)
	if err != nil {
		return nil, err
	}
	var result []wireguard.ManagedTarget
	var failures []error
	for id, rtRaw := range interfaces {
		n, e := index(id)
		if e != nil {
			continue
		}
		excluded, e := a.deps.Excluded(ctx, id)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if excluded {
			continue
		}
		rcRaw, rcOK := rcTree[id]
		if !rcOK {
			continue
		}
		_, t, e := a.parseTarget(id, n, rtRaw, rcRaw)
		if errors.Is(e, uplink.ErrUnsupported) {
			continue
		}
		if e != nil {
			failures = append(failures, e)
			continue
		}
		result = append(result, t)
	}
	return result, errors.Join(failures...)
}
func convert(t uplink.Target) (uplink.Target, error) {
	if t.Engine != "system-wireguard" || t.Kind != "" {
		return t, fmt.Errorf("%w: invalid system-wireguard target (%s/%s)", uplink.ErrUnsupported, t.Engine, t.Kind)
	}
	t.Engine = "wireguard"
	return t, nil
}
func (*Adapter) UplinkCapabilities(uplink.Target) uplink.Capabilities {
	return uplink.Capabilities{IndependentEngine: true, RestartScope: "target", ControlledFailback: true, Limitation: "system WireGuard client; IPv4 outer endpoint; explicit DNS; manual or controlled return; external NDMS changes require reconciliation"}
}
func (a *Adapter) UplinkTargets(ctx context.Context) ([]uplink.TargetInfo, error) {
	list, err := a.list(ctx)
	var result []uplink.TargetInfo
	for _, t := range list {
		result = append(result, uplink.TargetInfo{Target: uplink.Target{Engine: "system-wireguard", Ref: t.Ref}, Label: t.Label + " · system WireGuard"})
	}
	return result, err
}
func (a *Adapter) Validate(ctx context.Context, t uplink.Target) error {
	t, e := convert(t)
	if e != nil {
		return e
	}
	return a.manager.Validate(ctx, t)
}
func (a *Adapter) Observe(ctx context.Context, t uplink.Target) (uplink.Observation, error) {
	t, e := convert(t)
	if e != nil {
		return uplink.Observation{}, e
	}
	return a.manager.Observe(ctx, t)
}
func (a *Adapter) Activate(ctx context.Context, t uplink.Target, old, next string) error {
	t, e := convert(t)
	if e != nil {
		return e
	}
	return a.manager.Activate(ctx, t, old, next)
}
func (a *Adapter) Probe(ctx context.Context, t uplink.Target, dev string) error {
	t, e := convert(t)
	if e != nil {
		return e
	}
	return a.manager.Probe(ctx, t, dev)
}
func (a *Adapter) ReleaseUplink(ctx context.Context, t uplink.Target) error {
	t, e := convert(t)
	if e != nil {
		return e
	}
	return a.manager.ReleaseUplink(ctx, t)
}
