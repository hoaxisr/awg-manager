package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox"
)

func proxyOwnerMatches(description, owner string, legacyOwners []string) bool {
	if description == owner {
		return true
	}
	descToken := ExtractProxyOwnerToken(description)
	ownerToken := ExtractProxyOwnerToken(owner)
	if descToken != "" && ownerToken != "" && descToken == ownerToken {
		return true
	}
	if ownerToken != "" && (description == ownerToken || strings.Contains(description, "["+ownerToken+"]")) {
		return true
	}
	if descToken != "" && (owner == descToken || strings.Contains(owner, "["+descToken+"]")) {
		return true
	}
	for _, legacy := range legacyOwners {
		if legacy == allowAdoptEmptyDescription && description == "" {
			return true
		}
		if legacy != "" {
			if description == legacy || (descToken != "" && descToken == legacy) {
				return true
			}
		}
	}
	return false
}

type ndmsBridgeRegistrarAdapter struct {
	pm       *singbox.ProxyManager
	queries  *ndmsquery.Queries
	commands *ndmscommand.Commands

	mu      sync.Mutex
	pending map[int]time.Time
}

func newNDMSBridgeRegistrarAdapter(
	pm *singbox.ProxyManager,
	queries *ndmsquery.Queries,
	commands *ndmscommand.Commands,
) *ndmsBridgeRegistrarAdapter {
	return &ndmsBridgeRegistrarAdapter{
		pm:       pm,
		queries:  queries,
		commands: commands,
		pending:  make(map[int]time.Time),
	}
}

func (a *ndmsBridgeRegistrarAdapter) NextFreeIndex(ctx context.Context, reserved map[int]bool) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	if a.pending == nil {
		a.pending = make(map[int]time.Time)
	}
	for idx, reservedAt := range a.pending {
		if now.Sub(reservedAt) >= 30*time.Second {
			delete(a.pending, idx)
		}
	}
	combined := make(map[int]bool)
	for k, v := range reserved {
		combined[k] = v
	}
	for idx := range a.pending {
		combined[idx] = true
	}

	idx, err := a.pm.NextFreeIndex(ctx, combined)
	if err != nil {
		return 0, err
	}
	a.pending[idx] = now
	return idx, nil
}

func (a *ndmsBridgeRegistrarAdapter) ReleaseProxyIndex(index int) {
	a.mu.Lock()
	delete(a.pending, index)
	a.mu.Unlock()
}

func (a *ndmsBridgeRegistrarAdapter) LookupProxy(ctx context.Context, index int) (string, bool, error) {
	if a.queries == nil || a.queries.Interfaces == nil {
		return "", false, nil
	}
	name := fmt.Sprintf("Proxy%d", index)
	info, err := a.queries.Interfaces.GetProxy(ctx, name)
	if err != nil {
		return "", false, err
	}
	if info == nil || !info.Exists {
		return "", false, nil
	}
	return info.Description, true, nil
}

func (a *ndmsBridgeRegistrarAdapter) EnsureProxy(ctx context.Context, index, port int, description string) error {
	defer a.ReleaseProxyIndex(index)
	return a.pm.EnsureProxy(ctx, index, port, description, description)
}

func (a *ndmsBridgeRegistrarAdapter) EnsureProxyIfOwned(
	ctx context.Context,
	index, port int,
	owner string,
	legacyOwners ...string,
) (bool, error) {
	defer a.ReleaseProxyIndex(index)
	desc, exists, err := a.LookupProxy(ctx, index)
	if err != nil {
		return false, err
	}
	if exists {
		if !proxyOwnerMatches(desc, owner, legacyOwners) {
			return false, nil
		}
		if desc == owner {
			currentPort := 0
			if a.queries != nil && a.queries.RunningConfig != nil {
				if lines, err := a.queries.RunningConfig.Lines(ctx); err == nil {
					name := fmt.Sprintf("Proxy%d", index)
					currentPort = parseProxyPortFromRunningConfig(lines, name)
				}
			}
			if currentPort == port || (currentPort == 0 && a.queries == nil) {
				return true, nil
			}
		}
		if err := a.pm.EnsureProxy(ctx, index, port, owner, desc); err != nil {
			return true, err
		}
		return true, nil
	}
	if err := a.pm.EnsureProxy(ctx, index, port, owner, owner); err != nil {
		return true, err
	}
	return true, nil
}

func (a *ndmsBridgeRegistrarAdapter) RemoveProxyIfOwned(
	ctx context.Context,
	index int,
	owner string,
	legacyOwners ...string,
) (bool, error) {
	defer a.ReleaseProxyIndex(index)
	desc, exists, err := a.LookupProxy(ctx, index)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	if !proxyOwnerMatches(desc, owner, legacyOwners) {
		return false, nil
	}
	if err := a.pm.RemoveProxy(ctx, index, desc); err != nil {
		return true, err
	}
	return true, nil
}

func (a *ndmsBridgeRegistrarAdapter) InspectProxy(ctx context.Context, index int) (ProxyObservation, error) {
	name := fmt.Sprintf("Proxy%d", index)
	var listenPort int
	if a.queries != nil && a.queries.RunningConfig != nil {
		if lines, err := a.queries.RunningConfig.Lines(ctx); err == nil {
			listenPort = parseProxyPortFromRunningConfig(lines, name)
		}
	}

	if a.queries != nil && a.queries.Interfaces != nil {
		iface, err := a.queries.Interfaces.Get(ctx, name)
		if err != nil {
			return ProxyObservation{}, err
		}
		if iface == nil {
			return ProxyObservation{Name: name, Exists: false}, nil
		}
		sysName := iface.SystemName
		if sysName == "" {
			sysName = a.queries.Interfaces.ResolveSystemName(ctx, name)
		}
		return ProxyObservation{
			Name:        iface.ID,
			Exists:      true,
			Description: iface.Description,
			State:       iface.State,
			Link:        iface.Link,
			Up:          iface.State == "up",
			SystemName:  sysName,
			Address:     iface.Address,
			ListenPort:  listenPort,
		}, nil
	}
	return ProxyObservation{Name: name, Exists: false}, nil
}

func (a *ndmsBridgeRegistrarAdapter) ListProxyObservations(ctx context.Context) ([]ProxyObservation, error) {
	if a.queries == nil || a.queries.Interfaces == nil {
		return nil, nil
	}
	ifaces, err := a.queries.Interfaces.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list proxy observations: %w", err)
	}
	var lines []string
	if a.queries.RunningConfig != nil {
		lines, _ = a.queries.RunningConfig.Lines(ctx)
	}
	var observations []ProxyObservation
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.ID, "Proxy") {
			continue
		}
		var port int
		if len(lines) > 0 {
			port = parseProxyPortFromRunningConfig(lines, iface.ID)
		}
		sysName := iface.SystemName
		if sysName == "" {
			sysName = a.queries.Interfaces.ResolveSystemName(ctx, iface.ID)
		}
		observations = append(observations, ProxyObservation{
			Name:        iface.ID,
			Exists:      true,
			Description: iface.Description,
			State:       iface.State,
			Link:        iface.Link,
			Up:          iface.State == "up",
			SystemName:  sysName,
			Address:     iface.Address,
			ListenPort:  port,
		})
	}
	return observations, nil
}

func parseProxyPortFromRunningConfig(lines []string, ifaceName string) int {
	prefix := "interface " + ifaceName
	inIface := false
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r\n")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface ") {
			if line == prefix {
				inIface = true
				continue
			}
			if inIface {
				break
			}
		}
		if !inIface {
			continue
		}
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && !strings.HasPrefix(line, "!") {
			break
		}
		if strings.HasPrefix(trimmed, "proxy upstream ") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 4 {
				if p, err := strconv.Atoi(fields[3]); err == nil && p > 0 {
					return p
				}
			}
		}
	}
	return 0
}
