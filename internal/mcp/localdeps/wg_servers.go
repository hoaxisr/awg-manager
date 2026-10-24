package localdeps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/managed"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func wireguardServer(sv *storage.ManagedServer) mcpsrv.WireguardServer {
	return mcpsrv.WireguardServer{
		ID: sv.InterfaceName, Description: sanitizeLabel(sv.Description), Address: sv.Address, Mask: sv.Mask, ListenPort: sv.ListenPort,
		Endpoint: sv.Endpoint, DNS: sv.DNS, MTU: sv.MTU, NATMode: sv.NATMode, LANSegments: append([]string(nil), sv.LANSegments...),
		Policy: sv.Policy, PeerCount: len(sv.Peers),
	}
}

// serverChanged mirrors the REST handlers after a write: the per-server
// NDMS cache is dropped and the server list invalidated.
func (l *Local) serverChanged(id, reason string) {
	l.c.Managed.InvalidateCache(id)
	l.publish(events.ResourceServers, reason)
}

// readServer reads a managed server back; one awg-manager does not manage
// gets the error managedServer gives.
func (l *Local) readServer(ctx context.Context, id string) (mcpsrv.WireguardServer, error) {
	sv, err := l.managedServer(ctx, id)
	if err != nil {
		return mcpsrv.WireguardServer{}, err
	}
	return wireguardServer(sv), nil
}

func (l *Local) ListWireguardServerOptions(ctx context.Context) (mcpsrv.WireguardServerOptions, error) {
	if l.c.Managed == nil {
		return mcpsrv.WireguardServerOptions{}, errUnavailable("managed servers")
	}
	var out mcpsrv.WireguardServerOptions
	segs, err := l.c.Managed.ListLANSegments(ctx)
	if err != nil {
		return out, err
	}
	for _, s := range segs {
		out.LANSegments = append(out.LANSegments, mcpsrv.LANSegment{Name: s.Name, Label: sanitizeLabel(s.Label), Subnet: s.Subnet})
	}
	pols, err := l.c.Managed.ListPolicies(ctx)
	if err != nil {
		return out, err
	}
	for _, p := range pols {
		out.Policies = append(out.Policies, p.ID)
	}
	if addr, mask, err := l.c.Managed.SuggestAddress(ctx); err == nil {
		out.SuggestedAddress, out.SuggestedMask = addr, mask
	}
	return out, nil
}

// CreateWireguardServer fills a missing subnet with SuggestAddress, as the
// web form does. ErrASCUnverified comes with a server that exists and is
// saved: it is a warning, not a failure.
func (l *Local) CreateWireguardServer(ctx context.Context, in mcpsrv.WireguardServerInput) (mcpsrv.WireguardServer, []string, error) {
	if l.c.Managed == nil {
		return mcpsrv.WireguardServer{}, nil, errUnavailable("managed servers")
	}
	req := managed.CreateServerRequest{Address: in.Address, Mask: in.Mask, ListenPort: in.ListenPort, Description: in.Description, Endpoint: in.Endpoint, DNS: in.DNS, MTU: in.MTU}
	if in.AWG3 {
		req.Mode = "3.1"
	}
	if req.Address == "" {
		addr, mask, err := l.c.Managed.SuggestAddress(ctx)
		if err != nil {
			return mcpsrv.WireguardServer{}, nil, fmt.Errorf("no free private subnet was found; pass address and mask: %w", err)
		}
		req.Address, req.Mask = addr, mask
	}
	sv, err := l.c.Managed.Create(ctx, req)
	var warnings []string
	switch {
	case errors.Is(err, managed.ErrASCUnverified) && sv != nil:
		warnings = append(warnings, "the server is created, but its AmneziaWG obfuscation could not be read back to confirm it; check the server in the web interface")
	case err != nil:
		l.serverLog.Warn("create", in.Description, "Failed to create WireGuard server (MCP): "+err.Error())
		return mcpsrv.WireguardServer{}, nil, err
	case sv == nil:
		return mcpsrv.WireguardServer{}, nil, fmt.Errorf("server create returned no record")
	}
	l.serverLog.Info("create", sv.InterfaceName, "WireGuard server created (MCP)")
	l.serverChanged(sv.InterfaceName, "mcp-create")
	return wireguardServer(sv), warnings, nil
}

// UpdateWireguardServer merges over the stored record: Update requires the
// address, the mask and the port on every call.
func (l *Local) UpdateWireguardServer(ctx context.Context, p mcpsrv.WireguardServerPatch) (mcpsrv.WireguardServer, error) {
	if l.c.Managed == nil {
		return mcpsrv.WireguardServer{}, errUnavailable("managed servers")
	}
	sv, err := l.managedServer(ctx, p.ID)
	if err != nil {
		return mcpsrv.WireguardServer{}, err
	}
	req := managed.UpdateServerRequest{Address: sv.Address, Mask: sv.Mask, ListenPort: sv.ListenPort, Description: p.Description, Endpoint: p.Endpoint, DNS: p.DNS, MTU: p.MTU}
	if p.Address != nil {
		req.Address = strings.TrimSpace(*p.Address)
	}
	if p.Mask != nil {
		req.Mask = strings.TrimSpace(*p.Mask)
	}
	if p.ListenPort != nil {
		req.ListenPort = *p.ListenPort
	}
	if err := l.c.Managed.Update(ctx, p.ID, req); err != nil {
		l.serverLog.Warn("update", p.ID, "Failed to update WireGuard server (MCP): "+err.Error())
		return mcpsrv.WireguardServer{}, err
	}
	l.serverLog.Info("update", p.ID, "WireGuard server updated (MCP)")
	l.serverChanged(p.ID, "mcp-update")
	return l.readServer(ctx, p.ID)
}

// SetWireguardServerAccess checks the segments before any write; the three
// writes are separate router calls, so one failing after another succeeded
// still drops the cache and publishes, and says what was applied.
func (l *Local) SetWireguardServerAccess(ctx context.Context, a mcpsrv.WireguardServerAccess) (mcpsrv.WireguardServer, error) {
	if l.c.Managed == nil {
		return mcpsrv.WireguardServer{}, errUnavailable("managed servers")
	}
	if _, err := l.managedServer(ctx, a.ID); err != nil {
		return mcpsrv.WireguardServer{}, err
	}
	if a.LANSegments != nil {
		segs, err := l.c.Managed.ListLANSegments(ctx)
		if err != nil {
			return mcpsrv.WireguardServer{}, err
		}
		known := map[string]bool{}
		for _, s := range segs {
			known[s.Name] = true
		}
		for _, name := range *a.LANSegments {
			if !known[name] {
				return mcpsrv.WireguardServer{}, fmt.Errorf("%q is not a LAN segment (use list_wireguard_server_options)", name)
			}
		}
	}
	var applied []string
	fail := func(what string, err error) (mcpsrv.WireguardServer, error) {
		if len(applied) == 0 {
			return mcpsrv.WireguardServer{}, err
		}
		l.serverChanged(a.ID, "mcp-access")
		return mcpsrv.WireguardServer{}, fmt.Errorf("%s was changed, but %s was not: %w", strings.Join(applied, " and "), what, err)
	}
	if a.NATMode != nil {
		if err := l.c.Managed.SetNATMode(ctx, a.ID, *a.NATMode); err != nil {
			return fail("NAT", err)
		}
		applied = append(applied, "NAT")
	}
	if a.LANSegments != nil {
		if err := l.c.Managed.SetLANSegments(ctx, a.ID, append([]string{}, *a.LANSegments...)); err != nil {
			return fail("the LAN segments", err)
		}
		applied = append(applied, "the LAN segments")
	}
	if a.Policy != nil {
		if err := l.c.Managed.SetPolicy(ctx, a.ID, strings.TrimSpace(*a.Policy)); err != nil {
			return fail("the policy", err)
		}
	}
	l.serverLog.Info("access", a.ID, "WireGuard server access changed (MCP)")
	l.serverChanged(a.ID, "mcp-access")
	return l.readServer(ctx, a.ID)
}

func (l *Local) SetWireguardServerEnabled(ctx context.Context, id string, enabled bool) (mcpsrv.WireguardServer, error) {
	if l.c.Managed == nil {
		return mcpsrv.WireguardServer{}, errUnavailable("managed servers")
	}
	if _, err := l.managedServer(ctx, id); err != nil {
		return mcpsrv.WireguardServer{}, err
	}
	if err := l.c.Managed.SetEnabled(ctx, id, enabled); err != nil {
		return mcpsrv.WireguardServer{}, err
	}
	l.serverLog.Info("enable", id, "WireGuard server switched "+onOff(enabled)+" (MCP)")
	l.serverChanged(id, "mcp-enable")
	return l.readServer(ctx, id)
}

// headerKeys are the AmneziaWG headers, strings in NDMS's ASC JSON; the
// other parameters are integers there.
var headerKeys = map[string]bool{"h1": true, "h2": true, "h3": true, "h4": true}

// SetWireguardServerObfuscation lays the parameters over the server's
// current ones and writes them whole. A successful write is not followed by
// InvalidateCache, as in the REST handler (F600): the service read the
// result back already. Both refusals after the router took the write drop
// the cache and publish, as REST does: part may have applied (F601, R44).
func (l *Local) SetWireguardServerObfuscation(ctx context.Context, id string, params map[string]any) (mcpsrv.WireguardServer, error) {
	if l.c.Managed == nil {
		return mcpsrv.WireguardServer{}, errUnavailable("managed servers")
	}
	if _, err := l.managedServer(ctx, id); err != nil {
		return mcpsrv.WireguardServer{}, err
	}
	raw, err := l.c.Managed.GetASCParams(ctx, id)
	if err != nil {
		return mcpsrv.WireguardServer{}, err
	}
	current := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &current); err != nil {
			return mcpsrv.WireguardServer{}, fmt.Errorf("the server's obfuscation could not be read: %w", err)
		}
	}
	for k, v := range params {
		if headerKeys[k] {
			switch val := v.(type) {
			case float64:
				current[k] = strconv.FormatInt(int64(val), 10)
			default:
				current[k] = strings.TrimSpace(fmt.Sprint(val))
			}
			continue
		}
		switch val := v.(type) {
		case float64:
			current[k] = int64(val)
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return mcpsrv.WireguardServer{}, fmt.Errorf("%s must be a whole number", k)
			}
			current[k] = n
		}
	}
	merged, err := json.Marshal(current)
	if err != nil {
		return mcpsrv.WireguardServer{}, err
	}
	err = l.c.Managed.SetASCParams(ctx, id, merged)
	switch {
	case errors.Is(err, managed.ErrASCUnverified):
		l.serverChanged(id, "mcp-asc")
		return mcpsrv.WireguardServer{}, fmt.Errorf("the router took the parameters but they could not be read back to confirm; check the server in the web interface")
	case errors.Is(err, managed.ErrASCNotApplied):
		l.serverChanged(id, "mcp-asc")
		return mcpsrv.WireguardServer{}, fmt.Errorf("the router did not apply the parameters as sent — some may have applied; read the server again (list_managed_servers) before retrying")
	case err != nil:
		return mcpsrv.WireguardServer{}, err
	}
	l.serverLog.Info("asc", id, "WireGuard server obfuscation changed (MCP)")
	l.publish(events.ResourceServers, "mcp-asc")
	return l.readServer(ctx, id)
}
