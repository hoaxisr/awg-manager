package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"time"

	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/sbconfig"
)

// UplinkAdapter executes AWGM decisions; it does not run an engine fallback
// group. Only explicit standalone outbounds with dedicated SOCKS inbounds are
// accepted, so delay probes cannot silently select a different proxy.
type UplinkAdapter struct {
	Operator     *Operator
	Active       func() bool
	DNSForDevice uplink.DNSForDevice
}

func (a *UplinkAdapter) TargetExists(ctx context.Context, t uplink.Target) (bool, error) {
	_, err := a.Operator.GetTunnel(ctx, t.Ref)
	if errors.Is(err, ErrTunnelNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (a *UplinkAdapter) read(ctx context.Context, t uplink.Target) (map[string]any, int, error) {
	if t.Kind != "" {
		return nil, 0, uplink.ErrUnsupported
	}
	if a.Operator == nil || a.Active == nil || !a.Active() {
		return nil, 0, fmt.Errorf("sing-box is not the active engine")
	}
	if running, _ := a.Operator.IsRunning(); !running {
		return nil, 0, ErrSingboxNotRunning
	}
	raw, err := a.Operator.GetTunnel(ctx, t.Ref)
	if err != nil {
		return nil, 0, err
	}
	var ob map[string]any
	if err = json.Unmarshal(raw, &ob); err != nil {
		return nil, 0, err
	}
	if err = sbconfig.ValidateProxyOptions(ob); err != nil {
		return nil, 0, err
	}
	if ob["detour"] != nil && ob["detour"] != "" {
		return nil, 0, fmt.Errorf("%w: chained outbound", uplink.ErrUnsupported)
	}
	switch ob["type"] {
	case "vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic", "naive", "trusttunnel", "socks", "http", "anytls":
	default:
		return nil, 0, fmt.Errorf("%w: outbound type", uplink.ErrUnsupported)
	}
	// A domain endpoint requires an explicit DNS catalogue for the selected WAN.
	server, _ := ob["server"].(string)
	if net.ParseIP(server) == nil && a.DNSForDevice == nil {
		return nil, 0, fmt.Errorf("%w: endpoint must be an IP address until WAN-aware endpoint DNS is available", uplink.ErrUnsupported)
	}
	port, err := verifyUplinkRoute(a.Operator.configPath, t.Ref)
	if err != nil {
		return nil, 0, err
	}
	return ob, port, nil
}

func (a *UplinkAdapter) Validate(ctx context.Context, t uplink.Target) error {
	_, _, err := a.read(ctx, t)
	return err
}
func (a *UplinkAdapter) Observe(ctx context.Context, t uplink.Target) (uplink.Observation, error) {
	ob, _, err := a.read(ctx, t)
	if err != nil {
		return uplink.Observation{}, err
	}
	device, _ := ob["bind_interface"].(string)
	address4, _ := ob["inet4_bind_address"].(string)
	address6, _ := ob["inet6_bind_address"].(string)
	refresh := uplink.BindingNeedsRefresh(uplink.Binding{Device: device, IPv4: address4, IPv6: address6})
	return uplink.Observation{Device: device, Enabled: true, NeedsRebind: refresh}, nil
}
func (a *UplinkAdapter) Activate(ctx context.Context, t uplink.Target, expected, device string) error {
	o := a.Operator
	o.migrationMu.Lock()
	defer o.migrationMu.Unlock()
	if _, err := net.InterfaceByName(device); err != nil {
		return fmt.Errorf("%w: %w", uplink.ErrPathUnavailable, err)
	}
	ob, _, err := a.read(ctx, t)
	if err != nil {
		return err
	}
	bound, _ := ob["bind_interface"].(string)
	if bound != expected {
		return uplink.ErrConflict
	}
	binding, err := uplink.InspectBinding(device)
	if err != nil {
		return fmt.Errorf("%w: %w", uplink.ErrPathUnavailable, err)
	}
	server, _ := ob["server"].(string)
	if net.ParseIP(server) == nil {
		binding.DNSServers, err = a.DNSForDevice(ctx, device)
		if err != nil {
			return fmt.Errorf("%w: %w", uplink.ErrPathUnavailable, err)
		}
	}
	cfg, err := o.loadConfig()
	if err != nil {
		return err
	}
	prepared, err := sbconfig.Apply(cfg.raw, []sbconfig.Spec{{Tag: t.Ref, Binding: binding}})
	if err != nil {
		return fmt.Errorf("%w: %w", uplink.ErrPathUnavailable, err)
	}
	cfg.raw = prepared
	if err = o.ApplyConfig(ctx, cfg); err != nil {
		return err
	}
	// Slot writes schedule a debounced reload; verification MUST wait until
	// the runtime has actually consumed the new configuration.
	if o.orch == nil {
		return fmt.Errorf("sing-box orchestrator is unavailable")
	}
	// A successful SIGHUP does not acknowledge that the new sockets are live.
	// A full restart closes old provider connections before the fresh probe.
	// This affects other sing-box tunnels too and is exposed in the UI.
	return o.restartForUplinkLocked(ctx)
}

// RestartForUplink acknowledges configuration consumption by closing old
// sockets. It respects a concurrent user stop and affects the whole process.
func (o *Operator) RestartForUplink(ctx context.Context) error {
	o.migrationMu.Lock()
	defer o.migrationMu.Unlock()
	return o.restartForUplinkLocked(ctx)
}

func (o *Operator) restartForUplinkLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.orch == nil {
		return fmt.Errorf("sing-box orchestrator is unavailable")
	}
	if running, _ := o.IsRunning(); !running || o.IsManuallyStopped() {
		return ErrSingboxNotRunning
	}
	if err := o.proc.Stop(); err != nil {
		return err
	}
	return o.orch.ReloadNow()
}

// waitUplinkSOCKS waits for this process's listener, rather than interpreting
// a still-starting engine as a failed provider. Never adopt a replacement PID
// or a listener belonging to another process during this probe.
func (o *Operator) waitUplinkSOCKS(ctx context.Context, port uint16) (int, error) {
	if port == 0 {
		return 0, fmt.Errorf("%w: dedicated SOCKS port required", uplink.ErrUnsupported)
	}
	running, pid := o.IsRunning()
	if !running {
		return 0, ErrSingboxNotRunning
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("dedicated SOCKS listener is not ready: %w", err)
		}
		alive, current := o.IsRunning()
		if !alive {
			return 0, ErrSingboxNotRunning
		}
		if current != pid {
			return 0, fmt.Errorf("%w: active SOCKS process changed while starting", uplink.ErrConflict)
		}
		if candidateOwnsPort(pid, int(port)) {
			return pid, nil
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("dedicated SOCKS listener is not owned by the active process: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// ProbeUplinkSOCKS includes startup readiness and process ownership before and
// after the real HTTPS probe. A connected process alone does not prove its
// dedicated listener is available, especially after a restart on MIPS.
func (o *Operator) ProbeUplinkSOCKS(ctx context.Context, port uint16) error {
	pid, err := o.waitUplinkSOCKS(ctx, port)
	if err != nil {
		return err
	}
	if err = uplink.ProbeSOCKS(ctx, port, "", ""); err != nil {
		return err
	}
	alive, current := o.IsRunning()
	if !alive || current != pid || !candidateOwnsPort(pid, int(port)) {
		return fmt.Errorf("%w: active SOCKS process changed during probe", uplink.ErrConflict)
	}
	return nil
}

func (a *UplinkAdapter) Probe(ctx context.Context, t uplink.Target, device string) error {
	a.Operator.migrationMu.Lock()
	defer a.Operator.migrationMu.Unlock()
	ob, port, err := a.read(ctx, t)
	if err != nil {
		return err
	}
	if ob["bind_interface"] != device {
		return fmt.Errorf("binding changed during probe")
	}
	cfg, err := a.Operator.loadConfig()
	if err != nil {
		return err
	}
	if err = sbconfig.Verify(cfg.raw, t.Ref, device, false); err != nil {
		return err
	}
	address4, _ := ob["inet4_bind_address"].(string)
	address6, _ := ob["inet6_bind_address"].(string)
	if uplink.BindingNeedsRefresh(uplink.Binding{Device: device, IPv4: address4, IPv6: address6}) {
		return fmt.Errorf("provider source address changed during probe")
	}
	if err := a.Operator.ProbeUplinkSOCKS(ctx, uint16(port)); err != nil {
		return err
	}
	fresh, freshPort, err := a.read(ctx, t)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(ob, fresh) || freshPort != port {
		return fmt.Errorf("configuration changed during probe")
	}
	return nil
}
