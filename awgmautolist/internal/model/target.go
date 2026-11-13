// Package model defines the small set of types shared by capture, probes and UI.
package model

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	domainpolicy "github.com/hoaxisr/awg-manager/awgmautolist/internal/domain"
)

// Network is the transport observed by AF_PACKET.
type Network string

const (
	NetworkTCP Network = "tcp"
	NetworkUDP Network = "udp"
)

// Verdict is the result of one active check. Error never proves a block.
type Verdict string

const (
	VerdictNotChecked Verdict = "not-checked"
	VerdictReachable  Verdict = "reachable"
	VerdictBlocked    Verdict = "blocked"
	VerdictError      Verdict = "error"
)

// Target identifies an observed destination. Domain is the registered zone used
// by candidates/rules; Host is the exact SNI used by probes. Domain is empty for
// IP-only. IP is the AF_PACKET address retained by both direct and tunnel probes.
type Target struct {
	Domain  string  `json:"domain,omitempty"`
	Host    string  `json:"host,omitempty"`
	IP      string  `json:"ip,omitempty"`
	Network Network `json:"network"`
	Port    uint16  `json:"port"`
}

// NewTarget validates and canonicalizes an observed target.
func NewTarget(domain, ip string, network Network, port uint16) (Target, error) {
	t := Target{
		Domain:  strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), ".")),
		Network: network,
		Port:    port,
	}
	if ip != "" {
		addr, err := netip.ParseAddr(strings.TrimSpace(ip))
		if err != nil {
			return Target{}, fmt.Errorf("invalid observed IP %q", ip)
		}
		t.IP = addr.Unmap().String()
	}
	if t.Domain == "" && t.IP == "" {
		return Target{}, fmt.Errorf("domain or IP is required")
	}
	if t.Domain != "" {
		if net.ParseIP(t.Domain) != nil {
			return Target{}, fmt.Errorf("IP literal must be in the IP field")
		}
		if err := validateDomain(t.Domain); err != nil {
			return Target{}, err
		}
	}
	if network != NetworkTCP && network != NetworkUDP {
		return Target{}, fmt.Errorf("unsupported network %q", network)
	}
	if port == 0 {
		return Target{}, fmt.Errorf("port must be positive")
	}
	return t, nil
}

// NewObservedTarget retains the exact SNI while using its safe registered zone
// as the candidate/rule identity.
func NewObservedTarget(host, ip string, network Network, port uint16) (Target, error) {
	t, err := NewTarget(host, ip, network, port)
	if err != nil {
		return Target{}, err
	}
	if host != "" {
		t.Host = t.Domain
		t.Domain = domainpolicy.Zone(t.Host)
	}
	return t, nil
}

// Names returns exact host and registered zone for coverage/exclusions.
func (t Target) Names() []string {
	if t.Host != "" && t.Host != t.Domain {
		return []string{t.Host, t.Domain}
	}
	if t.Domain != "" {
		return []string{t.Domain}
	}
	return nil
}

// IPOnly reports that routing identity is the whole address, not one port.
func (t Target) IPOnly() bool { return t.Domain == "" }

// ProbeKey separates active endpoint checks. For an IP-only target the port is
// retained until one blocked endpoint is confirmed through the tunnel.
func (t Target) ProbeKey() string {
	host := t.Host
	if host == "" {
		host = t.Domain
	}
	if host == "" {
		host = t.IP
	}
	return host + "|" + string(t.Network) + "|" + strconv.Itoa(int(t.Port))
}

// CandidateKey implements the UI identity. A confirmed IP-only decision covers
// every port and protocol of the address, so its key deliberately omits both.
func (t Target) CandidateKey() string {
	if t.IPOnly() {
		return "ip|" + t.IP
	}
	return "domain|" + t.Domain + "|" + string(t.Network) + "|" + strconv.Itoa(int(t.Port))
}

// NormalizeDomain returns the canonical lower-case DNS name used by settings.
func NormalizeDomain(domain string) (string, error) {
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if net.ParseIP(name) != nil {
		return "", fmt.Errorf("IP literal is not a domain")
	}
	if err := validateDomain(name); err != nil {
		return "", err
	}
	return name, nil
}

func validateDomain(name string) error {
	if name == "" || len(name) > 253 || strings.ContainsAny(name, "/:@") {
		return fmt.Errorf("invalid domain %q", name)
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid domain %q", name)
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return fmt.Errorf("invalid domain %q", name)
			}
		}
	}
	return nil
}
