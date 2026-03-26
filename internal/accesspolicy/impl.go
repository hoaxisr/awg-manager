package accesspolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

const maxPolicies = 64
const maxDescriptionLen = 256

var validDescription = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type ctxKey string

const ctxForceRefresh ctxKey = "forceRefresh"

// ContextWithForceRefresh returns a context that signals cache invalidation.
func ContextWithForceRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxForceRefresh, true)
}

func isForceRefresh(ctx context.Context) bool {
	v, _ := ctx.Value(ctxForceRefresh).(bool)
	return v
}

// PolicyTracker tracks which policies were created by AWG Manager.
type PolicyTracker interface {
	AddManagedPolicy(name string) error
	RemoveManagedPolicy(name string) error
	GetManagedPolicies() []string
}

// ServiceImpl implements Service using the NDMS client.
type ServiceImpl struct {
	ndms    ndms.Client
	tracker PolicyTracker
	log     *logger.Logger
	appLog  *logging.ScopedLogger
	cache   *dataCache
}

// New creates a new access policy service.
func New(ndmsClient ndms.Client, tracker PolicyTracker, log *logger.Logger, appLogger logging.AppLogger) *ServiceImpl {
	return &ServiceImpl{
		ndms:    ndmsClient,
		tracker: tracker,
		log:     log.WithComponent("accesspolicy"),
		appLog:  logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubAccessPolicy),
		cache:   newDataCache(30 * time.Second),
	}
}

// List returns all access policies with permitted interfaces and device counts.
func (s *ServiceImpl) List(ctx context.Context) ([]Policy, error) {
	if isForceRefresh(ctx) {
		s.cache.InvalidateAll()
	}

	// Query all policies from NDMS
	raw, err := s.queryPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}

	// Count devices per policy from hotspot
	deviceCounts, err := s.countDevicesPerPolicy(ctx)
	if err != nil {
		s.log.Warnf("failed to count devices per policy: %v", err)
		deviceCounts = map[string]int{}
	}

	// Parse running-config for standalone and permit details
	rcPolicies, err := s.parseRunningConfig(ctx)
	if err != nil {
		s.log.Warnf("failed to parse running-config: %v", err)
		rcPolicies = map[string]rcPolicy{}
	}

	policies := make([]Policy, 0)
	for name, policyRaw := range raw {
		if !strings.HasPrefix(name, "Policy") {
			continue
		}

		var info rciPolicyInfo
		if err := json.Unmarshal(policyRaw, &info); err != nil {
			s.log.Warnf("failed to parse policy %s: %v", name, err)
			continue
		}

		p := Policy{
			Name:        name,
			Description: info.Description,
			Interfaces:  []PermittedIface{},
			DeviceCount: deviceCounts[name],
		}

		// Enrich with running-config data (standalone, interfaces)
		if rc, ok := rcPolicies[name]; ok {
			p.Standalone = rc.standalone
			if rc.interfaces != nil {
				p.Interfaces = rc.interfaces
			}
		}

		policies = append(policies, p)
	}

	// Enrich interface labels from global interface list
	globalIfaces, err := s.ListGlobalInterfaces(ctx)
	if err == nil {
		labelMap := make(map[string]string, len(globalIfaces))
		for _, gi := range globalIfaces {
			labelMap[gi.Name] = gi.Label
		}
		for i := range policies {
			for j := range policies[i].Interfaces {
				if label, ok := labelMap[policies[i].Interfaces[j].Name]; ok {
					policies[i].Interfaces[j].Label = label
				}
			}
		}
	}

	// Stable sort by policy index
	sort.Slice(policies, func(i, j int) bool {
		return policyIndex(policies[i].Name) < policyIndex(policies[j].Name)
	})

	return policies, nil
}

// validateDescription checks that the description conforms to NDMS requirements:
// Latin letters, digits, hyphens, underscores only; max 256 characters.
func validateDescription(description string) error {
	if description == "" {
		return fmt.Errorf("description is required")
	}
	if len(description) > maxDescriptionLen {
		return fmt.Errorf("description too long (%d chars, max %d)", len(description), maxDescriptionLen)
	}
	if !validDescription.MatchString(description) {
		return fmt.Errorf("description contains invalid characters (only Latin letters, digits, hyphens and underscores are allowed)")
	}
	return nil
}

// Create creates a new policy. Finds the first free PolicyN index.
func (s *ServiceImpl) Create(ctx context.Context, description string) (*Policy, error) {
	if err := validateDescription(description); err != nil {
		return nil, err
	}
	existing, err := s.queryPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("create policy: %w", err)
	}

	// Find first free index
	name := ""
	for i := 0; i < maxPolicies; i++ {
		candidate := fmt.Sprintf("Policy%d", i)
		if _, exists := existing[candidate]; !exists {
			name = candidate
			break
		}
	}
	if name == "" {
		return nil, fmt.Errorf("no free policy slot (max %d)", maxPolicies)
	}

	// Create via RCI
	if _, err := s.ndms.RCIPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"policy": map[string]interface{}{
				name: map[string]interface{}{
					"description": description,
				},
			},
		},
	}); err != nil {
		s.appLog.Warn("create", name, fmt.Sprintf("Failed: %v", err))
		return nil, fmt.Errorf("create policy %s: %w", name, err)
	}

	if err := s.ndms.Save(ctx); err != nil {
		s.log.Warnf("failed to save after create: %v", err)
	}

	// Track as managed by AWG Manager
	if s.tracker != nil {
		if err := s.tracker.AddManagedPolicy(name); err != nil {
			s.log.Warnf("failed to track managed policy %s: %v", name, err)
		}
	}

	s.cache.InvalidateRC()
	s.appLog.Info("create", name, fmt.Sprintf("Policy %s created (%s)", name, description))

	return &Policy{
		Name:        name,
		Description: description,
		Interfaces:  []PermittedIface{},
	}, nil
}

// CleanupAll deletes all access policies created by awg-manager.
func (s *ServiceImpl) CleanupAll(ctx context.Context) error {
	managed := s.tracker.GetManagedPolicies()
	for _, name := range managed {
		if err := s.Delete(ctx, name); err != nil {
			continue
		}
	}
	return nil
}

// Delete removes a policy by name.
func (s *ServiceImpl) Delete(ctx context.Context, name string) error {
	if !isValidPolicyName(name) {
		return fmt.Errorf("invalid policy name: %s", name)
	}

	if _, err := s.ndms.RCIPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"policy": map[string]interface{}{
				name: map[string]interface{}{
					"no": true,
				},
			},
		},
	}); err != nil {
		s.appLog.Warn("delete", name, fmt.Sprintf("Failed: %v", err))
		return fmt.Errorf("delete policy %s: %w", name, err)
	}

	if err := s.ndms.Save(ctx); err != nil {
		s.log.Warnf("failed to save after delete: %v", err)
	}

	// Remove from managed list
	if s.tracker != nil {
		_ = s.tracker.RemoveManagedPolicy(name)
	}

	s.cache.InvalidateRC()
	s.appLog.Info("delete", name, fmt.Sprintf("Policy %s deleted", name))

	return nil
}

// SetDescription updates the description of a policy.
func (s *ServiceImpl) SetDescription(ctx context.Context, name, description string) error {
	if !isValidPolicyName(name) {
		return fmt.Errorf("invalid policy name: %s", name)
	}
	if err := validateDescription(description); err != nil {
		return err
	}

	if _, err := s.ndms.RCIPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"policy": map[string]interface{}{
				name: map[string]interface{}{
					"description": description,
				},
			},
		},
	}); err != nil {
		s.appLog.Warn("set-description", name, fmt.Sprintf("Failed: %v", err))
		return fmt.Errorf("set description for %s: %w", name, err)
	}

	if err := s.ndms.Save(ctx); err != nil {
		s.log.Warnf("failed to save after set description: %v", err)
	}

	s.appLog.Full("set-description", name, fmt.Sprintf("Policy %s description updated", name))

	s.cache.InvalidateRC()
	return nil
}

// SetStandalone enables or disables standalone mode on a policy.
func (s *ServiceImpl) SetStandalone(ctx context.Context, name string, enabled bool) error {
	if !isValidPolicyName(name) {
		return fmt.Errorf("invalid policy name: %s", name)
	}

	var standaloneVal interface{}
	if enabled {
		standaloneVal = true
	} else {
		standaloneVal = map[string]interface{}{"no": true}
	}

	if _, err := s.ndms.RCIPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"policy": map[string]interface{}{
				name: map[string]interface{}{
					"standalone": standaloneVal,
				},
			},
		},
	}); err != nil {
		s.appLog.Warn("set-standalone", name, fmt.Sprintf("Failed: %v", err))
		return fmt.Errorf("set standalone for %s: %w", name, err)
	}

	if err := s.ndms.Save(ctx); err != nil {
		s.log.Warnf("failed to save after set standalone: %v", err)
	}

	state := "disabled"
	if enabled {
		state = "enabled"
	}
	s.appLog.Full("set-standalone", name, fmt.Sprintf("Policy %s standalone %s", name, state))

	s.cache.InvalidateRC()
	return nil
}

// PermitInterface adds an interface to a policy's permitted list.
func (s *ServiceImpl) PermitInterface(ctx context.Context, name, iface string, order int) error {
	if !isValidPolicyName(name) {
		return fmt.Errorf("invalid policy name: %s", name)
	}

	if _, err := s.ndms.RCIPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"policy": map[string]interface{}{
				name: map[string]interface{}{
					"permit": map[string]interface{}{
						"global":    true,
						"interface": iface,
						"order":     order,
					},
				},
			},
		},
	}); err != nil {
		s.appLog.Warn("permit", name, fmt.Sprintf("Failed to permit %s: %v", iface, err))
		return fmt.Errorf("permit interface %s on %s: %w", iface, name, err)
	}

	if err := s.ndms.Save(ctx); err != nil {
		s.log.Warnf("failed to save after permit interface: %v", err)
	}

	s.appLog.Info("permit", name, fmt.Sprintf("Policy %s: interface %s permitted (order %d)", name, iface, order))

	s.cache.InvalidateRC()
	return nil
}

// DenyInterface removes an interface from a policy's permitted list.
func (s *ServiceImpl) DenyInterface(ctx context.Context, name, iface string) error {
	if !isValidPolicyName(name) {
		return fmt.Errorf("invalid policy name: %s", name)
	}

	if _, err := s.ndms.RCIPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"policy": map[string]interface{}{
				name: map[string]interface{}{
					"permit": map[string]interface{}{
						"global":    true,
						"interface": iface,
						"no":        true,
					},
				},
			},
		},
	}); err != nil {
		s.appLog.Warn("deny", name, fmt.Sprintf("Failed to deny %s: %v", iface, err))
		return fmt.Errorf("deny interface %s on %s: %w", iface, name, err)
	}

	if err := s.ndms.Save(ctx); err != nil {
		s.log.Warnf("failed to save after deny interface: %v", err)
	}

	s.appLog.Info("deny", name, fmt.Sprintf("Policy %s: interface %s denied", name, iface))

	s.cache.InvalidateRC()
	return nil
}

// AssignDevice assigns a device (by MAC) to a policy.
func (s *ServiceImpl) AssignDevice(ctx context.Context, mac, policyName string) error {
	if !isValidPolicyName(policyName) {
		return fmt.Errorf("invalid policy name: %s", policyName)
	}

	if _, err := s.ndms.RCIPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"hotspot": map[string]interface{}{
				"host": map[string]interface{}{
					"mac":    mac,
					"policy": policyName,
				},
			},
		},
	}); err != nil {
		s.appLog.Warn("assign-device", mac, fmt.Sprintf("Failed to assign to %s: %v", policyName, err))
		return fmt.Errorf("assign device %s to %s: %w", mac, policyName, err)
	}

	if err := s.ndms.Save(ctx); err != nil {
		s.log.Warnf("failed to save after assign device: %v", err)
	}

	s.appLog.Info("assign-device", mac, fmt.Sprintf("Device %s assigned to %s", mac, policyName))

	s.cache.InvalidateHotspot()
	s.cache.InvalidateRC()
	return nil
}

// UnassignDevice removes a device's policy assignment via RCI.
func (s *ServiceImpl) UnassignDevice(ctx context.Context, mac string) error {
	if _, err := s.ndms.RCIPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"hotspot": map[string]interface{}{
				"host": map[string]interface{}{
					"mac": mac,
					"policy": map[string]interface{}{
						"no": true,
					},
				},
			},
		},
	}); err != nil {
		s.appLog.Warn("unassign-device", mac, fmt.Sprintf("Failed: %v", err))
		return fmt.Errorf("unassign device %s: %w", mac, err)
	}

	if err := s.ndms.Save(ctx); err != nil {
		s.log.Warnf("failed to save after unassign device: %v", err)
	}

	s.appLog.Info("unassign-device", mac, fmt.Sprintf("Device %s unassigned", mac))

	s.cache.InvalidateHotspot()
	s.cache.InvalidateRC()
	return nil
}

// ListDevices returns all known LAN devices with their policy assignments.
func (s *ServiceImpl) ListDevices(ctx context.Context) ([]Device, error) {
	if isForceRefresh(ctx) {
		s.cache.InvalidateAll()
	}

	resp, err := s.queryHotspot(ctx)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}

	// On firmware < 5.01A, /show/ip/hotspot doesn't include the "policy" field.
	// Fall back to parsing running-config for host→policy mappings.
	var rcHostPolicies map[string]string
	if !osdetect.AtLeast(5, 1) {
		rcHostPolicies, err = s.parseHotspotPolicies(ctx)
		if err != nil {
			s.log.Warnf("failed to parse hotspot policies from running-config: %v", err)
		}
	}

	// Deduplicate by MAC — hotspot may return multiple entries for the same device
	// (e.g. after reconnect with a new IP). Prefer the active entry.
	seen := make(map[string]int) // MAC -> index in devices
	devices := make([]Device, 0)
	for _, h := range resp {
		if h.IP == "" || h.IP == "0.0.0.0" {
			continue
		}
		hostname := h.Name
		if hostname == "" {
			hostname = h.Hostname
		}
		policy := h.Policy
		if policy == "" && rcHostPolicies != nil {
			policy = rcHostPolicies[strings.ToLower(h.MAC)]
		}
		dev := Device{
			MAC:      h.MAC,
			IP:       h.IP,
			Name:     h.Name,
			Hostname: hostname,
			Active:   isActiveHost(h.Active),
			Link:     h.Link,
			Policy:   policy,
		}
		if idx, dup := seen[h.MAC]; dup {
			// Replace if new entry is active (prefer latest active over stale)
			if dev.Active {
				devices[idx] = dev
			}
		} else {
			seen[h.MAC] = len(devices)
			devices = append(devices, dev)
		}
	}

	return devices, nil
}

// ListGlobalInterfaces returns router interfaces for policy routing.
// Returns NDMS IDs (e.g. "Wireguard0", "PPPoE0") because ip policy permit
// requires NDMS names, not kernel names.
// Sorted: active interfaces first, then by category (tunnels, WAN, other).
func (s *ServiceImpl) ListGlobalInterfaces(ctx context.Context) ([]GlobalInterface, error) {
	raw, err := s.ndms.RCIGet(ctx, "/show/interface/")
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}

	var allIfaces map[string]json.RawMessage
	if err := json.Unmarshal(raw, &allIfaces); err != nil {
		return nil, fmt.Errorf("parse interfaces: %w", err)
	}

	type ifaceInfo struct {
		InterfaceName string `json:"interface-name"`
		Type          string `json:"type"`
		Description   string `json:"description"`
		State         string `json:"state"`
		SecurityLevel string `json:"security-level"`
		Summary       struct {
			Layer struct {
				IPv4 string `json:"ipv4"`
			} `json:"layer"`
		} `json:"summary"`
	}

	result := make([]GlobalInterface, 0)
	for ndmsID, rawIface := range allIfaces {
		var info ifaceInfo
		if err := json.Unmarshal(rawIface, &info); err != nil {
			continue
		}
		// Skip internal interfaces (private security level = LAN/bridge)
		if info.SecurityLevel == "private" || info.SecurityLevel == "" {
			continue
		}
		up := info.State == "up" && info.Summary.Layer.IPv4 == "running"
		label := interfaceLabel(info.Type, info.InterfaceName, info.Description)

		result = append(result, GlobalInterface{
			Name:  ndmsID, // NDMS ID for ip policy permit
			Label: label,
			Up:    up,
		})
	}

	// Sort: active first, then by category
	sort.Slice(result, func(i, j int) bool {
		// Active before inactive
		if result[i].Up != result[j].Up {
			return result[i].Up
		}
		// By category: tunnels first, then WAN, then other
		ci, cj := ifaceCategory(result[i].Name), ifaceCategory(result[j].Name)
		if ci != cj {
			return ci < cj
		}
		return result[i].Name < result[j].Name
	})

	return result, nil
}

// SetInterfaceUp brings an NDMS interface up or down via RCI.
func (s *ServiceImpl) SetInterfaceUp(ctx context.Context, ndmsName string, up bool) error {
	action := "up"
	if !up {
		action = "down"
	}
	if _, err := s.ndms.RCIPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ndmsName: map[string]interface{}{
				action: true,
			},
		},
	}); err != nil {
		s.appLog.Warn("set-interface", ndmsName, fmt.Sprintf("Failed to set %s: %v", action, err))
		return fmt.Errorf("set interface %s %s: %w", ndmsName, action, err)
	}

	state := "down"
	if up {
		state = "up"
	}
	s.appLog.Full("set-interface", ndmsName, fmt.Sprintf("Interface %s set %s", ndmsName, state))

	return nil
}

// isOwnTunnel checks if the kernel interface name belongs to awg-manager.
func isOwnTunnel(kernelName string) bool {
	n := strings.ToLower(kernelName)
	return strings.HasPrefix(n, "opkgtun") || strings.HasPrefix(n, "awgm")
}

// interfaceLabel builds a human-readable label from NDMS interface data.
func interfaceLabel(ifaceType, kernelName, description string) string {
	if description != "" {
		return description
	}
	if ifaceType != "" {
		return ifaceType + " (" + kernelName + ")"
	}
	return kernelName
}

// ifaceCategory returns sort priority: 0=tunnel/VPN, 1=WAN, 2=other.
func ifaceCategory(ndmsID string) int {
	n := strings.ToLower(ndmsID)
	// Tunnels/VPN (including our managed OpkgTun)
	if strings.HasPrefix(n, "opkgtun") || strings.HasPrefix(n, "wireguard") ||
		strings.HasPrefix(n, "ipsec") || strings.HasPrefix(n, "openvpn") ||
		strings.HasPrefix(n, "sstp") || strings.HasPrefix(n, "l2tp") ||
		strings.HasPrefix(n, "pptp") {
		return 0
	}
	// WAN interfaces
	if strings.HasPrefix(n, "pppoe") || strings.HasPrefix(n, "isp") ||
		strings.HasPrefix(n, "lte") || strings.HasPrefix(n, "ethernet") {
		return 1
	}
	return 2
}

// --- internal helpers ---

// rciPolicyInfo represents a single policy from /show/ip/policy.
type rciPolicyInfo struct {
	Description string `json:"description"`
}

// hotspotHost represents a single host from /show/ip/hotspot with policy field.
type hotspotHost struct {
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Active   any    `json:"active"`
	Link     string `json:"link"`
	Policy   string `json:"policy"`
}

// hotspotResponse wraps the /show/ip/hotspot response.
type hotspotResponse struct {
	Host []hotspotHost `json:"host"`
}

// rcPolicy holds parsed running-config data for a policy.
type rcPolicy struct {
	standalone bool
	interfaces []PermittedIface
}

// queryPolicies queries /show/ip/policy via RCI GET and returns raw JSON per policy name.
func (s *ServiceImpl) queryPolicies(ctx context.Context) (map[string]json.RawMessage, error) {
	raw, err := s.ndms.RCIGet(ctx, "/show/ip/policy")
	if err != nil {
		return nil, fmt.Errorf("query policies: %w", err)
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse policy response: %w", err)
	}
	return result, nil
}

// queryHotspot queries /show/ip/hotspot via RCI GET for device data including policy field.
// Results are cached for the duration of cache TTL.
func (s *ServiceImpl) queryHotspot(ctx context.Context) ([]hotspotHost, error) {
	if hosts, ok := s.cache.GetHotspot(); ok {
		return hosts, nil
	}

	raw, err := s.ndms.RCIGet(ctx, "/show/ip/hotspot")
	if err != nil {
		return nil, err
	}
	var resp hotspotResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse hotspot response: %w", err)
	}

	s.cache.SetHotspot(resp.Host)
	// Return copy — same as cache hit path (GetHotspot returns copy)
	cp := make([]hotspotHost, len(resp.Host))
	copy(cp, resp.Host)
	return cp, nil
}

// countDevicesPerPolicy counts how many devices are assigned to each policy.
func (s *ServiceImpl) countDevicesPerPolicy(ctx context.Context) (map[string]int, error) {
	hosts, err := s.queryHotspot(ctx)
	if err != nil {
		return nil, err
	}

	// On firmware < 5.01A, /show/ip/hotspot doesn't include the "policy" field.
	var rcHostPolicies map[string]string
	if !osdetect.AtLeast(5, 1) {
		rcHostPolicies, err = s.parseHotspotPolicies(ctx)
		if err != nil {
			s.log.Warnf("failed to parse hotspot policies from running-config: %v", err)
		}
	}

	counts := make(map[string]int)
	for _, h := range hosts {
		policy := h.Policy
		if policy == "" && rcHostPolicies != nil {
			policy = rcHostPolicies[strings.ToLower(h.MAC)]
		}
		if policy != "" {
			counts[policy]++
		}
	}
	return counts, nil
}

// getRunningConfigLines fetches and caches the lines from /show/running-config.
func (s *ServiceImpl) getRunningConfigLines(ctx context.Context) ([]string, error) {
	if lines, ok := s.cache.GetRCLines(); ok {
		return lines, nil
	}

	raw, err := s.ndms.RCIGet(ctx, "/show/running-config")
	if err != nil {
		return nil, err
	}
	var rcResp struct {
		Message []string `json:"message"`
	}
	if err := json.Unmarshal(raw, &rcResp); err != nil {
		return nil, fmt.Errorf("parse running-config: %w", err)
	}

	s.cache.SetRCLines(rcResp.Message)
	return rcResp.Message, nil
}

// parseRunningConfig parses "show running-config" via RCI GET to extract standalone
// and permit details for each policy block.
func (s *ServiceImpl) parseRunningConfig(ctx context.Context) (map[string]rcPolicy, error) {
	lines, err := s.getRunningConfigLines(ctx)
	if err != nil {
		return nil, err
	}

	policies := make(map[string]rcPolicy)
	var currentPolicy string
	var current rcPolicy

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect "ip policy PolicyN" block start
		if strings.HasPrefix(trimmed, "ip policy ") {
			if currentPolicy != "" {
				policies[currentPolicy] = current
			}
			currentPolicy = strings.TrimPrefix(trimmed, "ip policy ")
			current = rcPolicy{}
			continue
		}

		if currentPolicy == "" {
			continue
		}

		// End of block
		if trimmed == "!" {
			policies[currentPolicy] = current
			currentPolicy = ""
			continue
		}

		// Parse standalone
		if trimmed == "standalone" {
			current.standalone = true
			continue
		}

		// Parse "permit global <interface>" and "no permit global <interface>"
		if strings.HasPrefix(trimmed, "permit global ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 3 {
				pi := PermittedIface{
					Name:  parts[2],
					Order: len(current.interfaces),
				}
				current.interfaces = append(current.interfaces, pi)
			}
		} else if strings.HasPrefix(trimmed, "no permit global ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 4 {
				pi := PermittedIface{
					Name:   parts[3],
					Order:  len(current.interfaces),
					Denied: true,
				}
				current.interfaces = append(current.interfaces, pi)
			}
		}
	}

	// Flush last policy if file doesn't end with "!"
	if currentPolicy != "" {
		policies[currentPolicy] = current
	}

	return policies, nil
}

// parseHotspotPolicies parses running-config to extract host→policy mappings
// from the "ip hotspot" block. Returns map[mac]policyName with lowercase MACs.
// Used as fallback on firmware < 5.01A where /show/ip/hotspot doesn't include "policy".
func (s *ServiceImpl) parseHotspotPolicies(ctx context.Context) (map[string]string, error) {
	lines, err := s.getRunningConfigLines(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	inHotspot := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "ip hotspot" {
			inHotspot = true
			continue
		}

		if !inHotspot {
			continue
		}

		// End of hotspot block
		if trimmed == "!" {
			break
		}

		// Parse "host <mac> policy <PolicyN>"
		if strings.HasPrefix(trimmed, "host ") && strings.Contains(trimmed, " policy ") {
			parts := strings.Fields(trimmed)
			// Expected: ["host", "<mac>", "policy", "<PolicyN>"]
			for i := 0; i < len(parts)-1; i++ {
				if parts[i] == "policy" {
					result[strings.ToLower(parts[1])] = parts[i+1]
					break
				}
			}
		}
	}

	return result, nil
}

// isValidPolicyName checks that the name matches PolicyN format.
func isValidPolicyName(name string) bool {
	if !strings.HasPrefix(name, "Policy") {
		return false
	}
	numStr := strings.TrimPrefix(name, "Policy")
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return false
	}
	return n >= 0 && n < maxPolicies
}

// policyIndex extracts the numeric index from a policy name for sorting.
func policyIndex(name string) int {
	numStr := strings.TrimPrefix(name, "Policy")
	n, _ := strconv.Atoi(numStr)
	return n
}

// isActiveHost checks the "active" field which may be bool or string depending on firmware.
func isActiveHost(v any) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return val == "yes"
	}
	return true
}

// Ensure ServiceImpl implements Service.
var _ Service = (*ServiceImpl)(nil)
