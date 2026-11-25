package firc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

var (
	_ uplink.TargetPreparer = (*UplinkAdapter)(nil)
	_ uplink.Releaser       = (*UplinkAdapter)(nil)
)

const uplinkDiagnosticInboundsFile = ".awgm-uplink-inbounds.json"

// UplinkPoolInbound deliberately requires loopback and the entire pool. An
// existing LAN listener or one pinned to a node is never repurposed.
func UplinkPoolInbound(in TunnelInbound) bool {
	return in.Port != 0 && in.Node == TunnelNodePool && (in.Listen == "lo" || in.Listen == "127.0.0.1") && in.User == "" && in.Password == ""
}

// movedUplinkInbound — наш служебный вход, который прежние версии при сохранении
// туннелей переводили с lo на br0. Опознаём только по записанному порту: иначе
// вход на br0 может быть пользовательским.
func movedUplinkInbound(in TunnelInbound, recordedPort uint16) bool {
	return recordedPort != 0 && in.Port == recordedPort && in.Listen == "br0" &&
		in.Node == TunnelNodePool && in.User == "" && in.Password == ""
}

func (o *Operator) uplinkInboundRecordPath() string {
	if o == nil || strings.TrimSpace(o.configDir) == "" {
		return ""
	}
	return filepath.Join(o.configDir, uplinkDiagnosticInboundsFile)
}

func (o *Operator) loadUplinkInboundRecords() map[string]uint16 {
	records := map[string]uint16{}
	path := o.uplinkInboundRecordPath()
	if path == "" {
		return records
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return records
	}
	_ = json.Unmarshal(raw, &records)
	return records
}

func (o *Operator) saveUplinkInboundRecords(records map[string]uint16) error {
	path := o.uplinkInboundRecordPath()
	if path == "" {
		return nil
	}
	if err := o.CheckNotForeign(); err != nil {
		return err
	}
	if len(records) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return storage.AtomicWritePerm(path, data, 0o600)
}

func (a *UplinkAdapter) PrepareUplink(ctx context.Context, t uplink.Target) error {
	if t.Engine != "firc" || t.Kind != "" || a.Operator == nil || a.Active == nil || !a.Active() {
		return uplink.ErrUnsupported
	}
	o := a.Operator
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()
	if !o.IsRunning(ctx) {
		return fmt.Errorf("AWGM Route is stopped")
	}
	tunnels, err := o.Client().GetTunnels(ctx)
	if err != nil {
		return err
	}
	index := -1
	used := map[uint16]bool{}
	for i, tun := range tunnels {
		for _, in := range tun.Inbounds {
			used[in.Port] = true
		}
		if tun.ID == t.Ref {
			index = i
		}
	}
	if index < 0 {
		return uplink.ErrNotFound
	}
	target := tunnels[index]
	if !target.Enable {
		return fmt.Errorf("AWGM Route tunnel is disabled")
	}
	if requiresUplinkDNS(target) {
		h, err := o.Client().Ping(ctx)
		if err != nil {
			return err
		}
		if h.UplinkDNSVersion < 1 || a.DNSForDevice == nil {
			return fmt.Errorf("%w: AWGM Route WAN DNS capability required", uplink.ErrUnsupported)
		}
	}
	for _, in := range target.Inbounds {
		if UplinkPoolInbound(in) {
			return nil
		}
	}
	recordedPort := o.loadUplinkInboundRecords()[t.Ref]
	kept := make([]TunnelInbound, 0, len(target.Inbounds)+1)
	for _, in := range target.Inbounds {
		if !movedUplinkInbound(in, recordedPort) {
			kept = append(kept, in)
		}
	}
	target.Inbounds = kept
	// Keep the reservation until immediately before daemon sync. Its readback
	// and later payload probe reject a failed bind; no foreign port is adopted.
	var listener net.Listener
	var port uint16
	for n := 0; n < 32; n++ {
		l, e := net.Listen("tcp4", "127.0.0.1:0")
		if e != nil {
			return e
		}
		p := uint16(l.Addr().(*net.TCPAddr).Port)
		if used[p] {
			l.Close()
			continue
		}
		listener = l
		port = p
		break
	}
	if listener == nil {
		return fmt.Errorf("no unused diagnostic SOCKS port")
	}
	target.Inbounds = append(target.Inbounds, TunnelInbound{Listen: "lo", Port: port, Node: TunnelNodePool})
	tunnels[index] = target
	listener.Close()
	if _, err = o.applyTunnelsLocked(ctx, tunnels, false); err != nil {
		return err
	}
	fresh, err := o.Client().GetTunnels(ctx)
	if err != nil {
		return err
	}
	for _, tun := range fresh {
		if tun.ID == t.Ref {
			for _, in := range tun.Inbounds {
				if in.Port == port && UplinkPoolInbound(in) {
					records := o.loadUplinkInboundRecords()
					records[t.Ref] = port
					_ = o.saveUplinkInboundRecords(records)
					return nil
				}
			}
		}
	}
	return fmt.Errorf("diagnostic SOCKS prerequisite was not retained by AWGM Route")
}

// ReleaseUplink removes any auto-created loopback diagnostic SOCKS inbound when
// a Multi-WAN policy is disabled or deleted, without touching user-configured
// inbounds (such as br0:1099 or preexisting 127.0.0.1 pool listeners).
func (a *UplinkAdapter) ReleaseUplink(ctx context.Context, t uplink.Target) error {
	if t.Engine != "firc" || t.Kind != "" || a.Operator == nil {
		return nil
	}
	o := a.Operator
	o.mutationMu.Lock()
	defer o.mutationMu.Unlock()

	records := o.loadUplinkInboundRecords()
	recordedPort := records[t.Ref]
	isAutoDiagnosticInbound := func(in TunnelInbound) bool {
		if movedUplinkInbound(in, recordedPort) {
			return true
		}
		if in.Listen != "lo" || !UplinkPoolInbound(in) || in.Port == DefaultMixedInboundPort || in.Port == o.inboundPort {
			return false
		}
		if recordedPort != 0 {
			return in.Port == recordedPort
		}
		return in.Port >= 1024
	}
	stripAuto := func(inbounds []TunnelInbound) ([]TunnelInbound, bool) {
		if len(inbounds) == 0 {
			return inbounds, false
		}
		kept := make([]TunnelInbound, 0, len(inbounds))
		removed := false
		for _, in := range inbounds {
			if isAutoDiagnosticInbound(in) {
				removed = true
				continue
			}
			kept = append(kept, in)
		}
		return kept, removed
	}
	clearRecord := func() {
		if _, exists := records[t.Ref]; exists {
			delete(records, t.Ref)
			_ = o.saveUplinkInboundRecords(records)
		}
	}

	if a.Active != nil && a.Active() && o.IsRunning(ctx) {
		tunnels, err := o.Client().GetTunnels(ctx)
		if err != nil {
			return err
		}
		idx := -1
		for i := range tunnels {
			if tunnels[i].ID == t.Ref {
				idx = i
				break
			}
		}
		if idx < 0 {
			clearRecord()
			return nil
		}
		kept, removed := stripAuto(tunnels[idx].Inbounds)
		if !removed {
			clearRecord()
			return nil
		}
		tunnels[idx].Inbounds = kept
		if _, err = o.applyTunnelsLocked(ctx, tunnels, false); err != nil {
			return err
		}
		clearRecord()
		return nil
	}

	tunnels, err := o.LoadTunnelsFile()
	if err != nil || len(tunnels) == 0 {
		clearRecord()
		return nil
	}
	changed := false
	for i := range tunnels {
		if tunnels[i].ID != t.Ref {
			continue
		}
		if kept, removed := stripAuto(tunnels[i].Inbounds); removed {
			tunnels[i].Inbounds = kept
			changed = true
		}
		break
	}
	if changed {
		if err := o.SaveTunnelsFile(tunnels); err != nil {
			return err
		}
	}
	clearRecord()
	return nil
}
