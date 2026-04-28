package accesspolicy

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

const maxPolicies = 64
const maxDescriptionLen = 256

var validDescription = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type ctxKey string

const ctxForceRefresh ctxKey = "forceRefresh"

// ContextWithForceRefresh returns a context that signals cache invalidation.
// Retained as a public API compat: callers in api/accesspolicy.go use it to
// force-refresh before reads. With the new Stores we translate this signal
// into a targeted InvalidateAll on the relevant store.
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

// ServiceImpl implements Service on top of the NDMS CQRS layer
// (command.PolicyCommands for writes, query.Queries for reads).
// InterfaceCommands is plumbed in so SetInterfaceUp can reuse the
// canonical Up/Down implementations — no duplicate behaviour inside
// PolicyCommands.
type ServiceImpl struct {
	policies    *command.PolicyCommands
	interfaces  *command.InterfaceCommands
	queries     *query.Queries
	tracker     PolicyTracker
	log         *logger.Logger
	appLog      *logging.ScopedLogger
	policyMarks PolicyMarkSource
}

// New creates a new access policy service backed by the NDMS CQRS layer.
// Stores handle their own caching and single-flight — no boot-time
// pre-warm is needed.
func New(policies *command.PolicyCommands, interfaces *command.InterfaceCommands, queries *query.Queries, tracker PolicyTracker, log *logger.Logger, appLogger logging.AppLogger, policyMarks PolicyMarkSource) *ServiceImpl {
	return &ServiceImpl{
		policies:    policies,
		interfaces:  interfaces,
		queries:     queries,
		tracker:     tracker,
		log:         log.WithComponent("accesspolicy"),
		appLog:      logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubAccessPolicy),
		policyMarks: policyMarks,
	}
}

// List returns all access policies with permitted interfaces and device counts.
func (s *ServiceImpl) List(ctx context.Context) ([]Policy, error) {
	if isForceRefresh(ctx) {
		s.queries.Policies.InvalidateAll()
		s.queries.Hotspot.InvalidateAll()
		s.queries.RunningConfig.InvalidateAll()
	}

	rcPolicies, err := s.queries.Policies.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}

	// Count devices per policy from hotspot
	deviceCounts, err := s.countDevicesPerPolicy(ctx)
	if err != nil {
		s.log.Warnf("failed to count devices per policy: %v", err)
		deviceCounts = map[string]int{}
	}

	policies := make([]Policy, 0, len(rcPolicies))
	for _, rc := range rcPolicies {
		p := Policy{
			Name:        rc.Name,
			Description: rc.Description,
			Standalone:  rc.Standalone,
			Interfaces:  make([]PermittedIface, 0, len(rc.Interfaces)),
			DeviceCount: deviceCounts[rc.Name],
		}
		for _, pi := range rc.Interfaces {
			p.Interfaces = append(p.Interfaces, PermittedIface{
				Name:   pi.Name,
				Label:  pi.Label,
				Order:  pi.Order,
				Denied: pi.Denied,
			})
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

	// Stable sort: PolicyN by number first, then custom policies alphabetically
	sort.Slice(policies, func(i, j int) bool {
		pi, pj := policyIndex(policies[i].Name), policyIndex(policies[j].Name)
		if pi != pj {
			return pi < pj
		}
		return policies[i].Name < policies[j].Name
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
	existing, err := s.queries.Policies.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("create policy: %w", err)
	}
	existingNames := make(map[string]struct{}, len(existing))
	for _, p := range existing {
		existingNames[p.Name] = struct{}{}
	}

	// Find first free index
	name := ""
	for i := 0; i < maxPolicies; i++ {
		candidate := fmt.Sprintf("Policy%d", i)
		if _, exists := existingNames[candidate]; !exists {
			name = candidate
			break
		}
	}
	if name == "" {
		return nil, fmt.Errorf("no free policy slot (max %d)", maxPolicies)
	}

	if err := s.policies.CreatePolicy(ctx, name, description); err != nil {
		s.appLog.Warn("create", name, fmt.Sprintf("Failed: %v", err))
		return nil, err
	}

	// Track as managed by AWG Manager
	if s.tracker != nil {
		if err := s.tracker.AddManagedPolicy(name); err != nil {
			s.log.Warnf("failed to track managed policy %s: %v", name, err)
		}
	}

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

	if err := s.policies.DeletePolicy(ctx, name); err != nil {
		s.appLog.Warn("delete", name, fmt.Sprintf("Failed: %v", err))
		return err
	}

	// Remove from managed list
	if s.tracker != nil {
		_ = s.tracker.RemoveManagedPolicy(name)
	}

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

	if err := s.policies.SetDescription(ctx, name, description); err != nil {
		s.appLog.Warn("set-description", name, fmt.Sprintf("Failed: %v", err))
		return err
	}

	s.appLog.Full("set-description", name, fmt.Sprintf("Policy %s description updated", name))
	return nil
}

// SetStandalone enables or disables standalone mode on a policy.
func (s *ServiceImpl) SetStandalone(ctx context.Context, name string, enabled bool) error {
	if !isValidPolicyName(name) {
		return fmt.Errorf("invalid policy name: %s", name)
	}

	if err := s.policies.SetStandalone(ctx, name, enabled); err != nil {
		s.appLog.Warn("set-standalone", name, fmt.Sprintf("Failed: %v", err))
		return err
	}

	state := "disabled"
	if enabled {
		state = "enabled"
	}
	s.appLog.Full("set-standalone", name, fmt.Sprintf("Policy %s standalone %s", name, state))
	return nil
}

// PermitInterface adds an interface to a policy's permitted list.
func (s *ServiceImpl) PermitInterface(ctx context.Context, name, iface string, order int) error {
	if !isValidPolicyName(name) {
		return fmt.Errorf("invalid policy name: %s", name)
	}

	if err := s.policies.PermitInterface(ctx, name, iface, order); err != nil {
		s.appLog.Warn("permit", name, fmt.Sprintf("Failed to permit %s: %v", iface, err))
		return err
	}

	s.appLog.Info("permit", name, fmt.Sprintf("Policy %s: interface %s permitted (order %d)", name, iface, order))
	return nil
}

// DenyInterface removes an interface from a policy's permitted list.
func (s *ServiceImpl) DenyInterface(ctx context.Context, name, iface string) error {
	if !isValidPolicyName(name) {
		return fmt.Errorf("invalid policy name: %s", name)
	}

	if err := s.policies.DenyInterface(ctx, name, iface); err != nil {
		s.appLog.Warn("deny", name, fmt.Sprintf("Failed to deny %s: %v", iface, err))
		return err
	}

	s.appLog.Info("deny", name, fmt.Sprintf("Policy %s: interface %s denied", name, iface))
	return nil
}

// AssignDevice assigns a device (by MAC) to a policy.
func (s *ServiceImpl) AssignDevice(ctx context.Context, mac, policyName string) error {
	if !isValidPolicyName(policyName) {
		return fmt.Errorf("invalid policy name: %s", policyName)
	}

	if err := s.policies.AssignDevice(ctx, mac, policyName); err != nil {
		s.appLog.Warn("assign-device", mac, fmt.Sprintf("Failed to assign to %s: %v", policyName, err))
		return err
	}

	s.appLog.Info("assign-device", mac, fmt.Sprintf("Device %s assigned to %s", mac, policyName))
	return nil
}

// UnassignDevice removes a device's policy assignment via RCI.
func (s *ServiceImpl) UnassignDevice(ctx context.Context, mac string) error {
	if err := s.policies.UnassignDevice(ctx, mac); err != nil {
		s.appLog.Warn("unassign-device", mac, fmt.Sprintf("Failed: %v", err))
		return err
	}

	s.appLog.Info("unassign-device", mac, fmt.Sprintf("Device %s unassigned", mac))
	return nil
}

// ListDevices returns all known LAN devices with their policy assignments.
func (s *ServiceImpl) ListDevices(ctx context.Context) ([]Device, error) {
	if isForceRefresh(ctx) {
		s.queries.Hotspot.InvalidateAll()
		s.queries.RunningConfig.InvalidateAll()
	}

	hosts, err := s.queries.Hotspot.List(ctx)
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

	devices := make([]Device, 0, len(hosts))
	for _, h := range hosts {
		hostname := h.Name
		if hostname == "" {
			hostname = h.Hostname
		}
		policy := h.Policy
		if policy == "" && rcHostPolicies != nil {
			policy = rcHostPolicies[strings.ToLower(h.MAC)]
		}
		devices = append(devices, Device{
			MAC:      h.MAC,
			IP:       h.IP,
			Name:     h.Name,
			Hostname: hostname,
			Active:   h.Active,
			Link:     h.Link,
			Policy:   policy,
		})
	}

	return devices, nil
}

// ListGlobalInterfaces returns router interfaces for policy routing.
// Returns NDMS IDs (e.g. "Wireguard0", "PPPoE0") because ip policy permit
// requires NDMS names, not kernel names.
// Sorted: active interfaces first, then by category (tunnels, WAN, other).
func (s *ServiceImpl) ListGlobalInterfaces(ctx context.Context) ([]GlobalInterface, error) {
	all, err := s.queries.Interfaces.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}

	result := make([]GlobalInterface, 0, len(all))
	for _, iface := range all {
		// Skip internal interfaces (private security level = LAN/bridge)
		if iface.SecurityLevel == "private" || iface.SecurityLevel == "" {
			continue
		}
		up := iface.State == "up" && iface.IPv4 == "running"
		label := interfaceLabel(iface.Type, iface.SystemName, iface.Description)

		result = append(result, GlobalInterface{
			Name:  iface.ID, // NDMS ID for ip policy permit
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

// SetInterfaceUp brings an NDMS interface up or down via InterfaceCommands.
// Both up and down register the expected hook automatically.
func (s *ServiceImpl) SetInterfaceUp(ctx context.Context, ndmsName string, up bool) error {
	action := "up"
	if !up {
		action = "down"
	}
	var err error
	if up {
		err = s.interfaces.InterfaceUp(ctx, ndmsName)
	} else {
		err = s.interfaces.InterfaceDown(ctx, ndmsName)
	}
	if err != nil {
		s.appLog.Warn("set-interface", ndmsName, fmt.Sprintf("Failed to set %s: %v", action, err))
		return err
	}
	s.appLog.Full("set-interface", ndmsName, fmt.Sprintf("Interface %s set %s", ndmsName, action))
	return nil
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

// countDevicesPerPolicy counts how many devices are assigned to each policy.
func (s *ServiceImpl) countDevicesPerPolicy(ctx context.Context) (map[string]int, error) {
	hosts, err := s.queries.Hotspot.List(ctx)
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
	seen := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		// HotspotStore already dedupes, but guard once more in case.
		if _, dup := seen[h.MAC]; dup {
			continue
		}
		seen[h.MAC] = struct{}{}
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

// parseHotspotPolicies parses running-config to extract host→policy mappings
// from the "ip hotspot" block. Returns map[mac]policyName with lowercase MACs.
// Used as fallback on firmware < 5.01A where /show/ip/hotspot doesn't include "policy".
func (s *ServiceImpl) parseHotspotPolicies(ctx context.Context) (map[string]string, error) {
	lines, err := s.queries.RunningConfig.Lines(ctx)
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

// isValidPolicyName checks that the policy name is non-empty.
// Accepts both standard PolicyN names and custom names (e.g. from HR Neo).
func isValidPolicyName(name string) bool {
	return name != ""
}

// policyIndex extracts a sort key from a policy name.
// Standard PolicyN names sort by number (0-63), custom names sort after them (1000+).
func policyIndex(name string) int {
	if strings.HasPrefix(name, "Policy") {
		numStr := strings.TrimPrefix(name, "Policy")
		if n, err := strconv.Atoi(numStr); err == nil {
			return n
		}
	}
	return 1000 // custom policies sort after standard PolicyN
}

// Ensure ServiceImpl implements Service.
var _ Service = (*ServiceImpl)(nil)
