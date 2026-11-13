package awgm

import (
	"context"
	"fmt"
	"strings"
)

type Interface struct {
	Name          string `json:"name"`
	Up            bool   `json:"up"`
	Type          string `json:"type"`
	SecurityLevel string `json:"securityLevel"`
	Label         string `json:"label"`
}

// WANInterfaceInfo mirrors the bindable-interfaces projection.
type WANInterfaceInfo struct {
	Name     string `json:"name"`
	ID       string `json:"id,omitempty"`
	Label    string `json:"label,omitempty"`
	Up       bool   `json:"up,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Type     string `json:"type,omitempty"`
	Foreign  bool   `json:"foreign,omitempty"`
	Absent   bool   `json:"absent,omitempty"`
}

// BindableInterfaces returns the kernel interfaces a direct outbound can
// bind to (GET /api/singbox/router/bindable-interfaces).
func (c *Client) BindableInterfaces(ctx context.Context) ([]string, error) {
	var ifaces []WANInterfaceInfo
	if err := c.get(ctx, "/api/singbox/router/bindable-interfaces", &ifaces); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Name != "" {
			names = append(names, iface.Name)
		}
	}
	return names, nil
}

func (c *Client) LANBridges(ctx context.Context) ([]string, error) {
	var interfaces []Interface
	if err := c.get(ctx, "/api/system/all-interfaces", &interfaces); err != nil {
		return nil, err
	}
	var names []string
	for _, iface := range interfaces {
		if iface.Up && strings.EqualFold(iface.Type, "bridge") && strings.EqualFold(iface.SecurityLevel, "private") {
			names = append(names, iface.Name)
		}
	}
	return names, nil
}

type RouterStatus struct {
	Final string `json:"final"`
}

func (c *Client) RouterStatus(ctx context.Context) (RouterStatus, error) {
	var status RouterStatus
	err := c.get(ctx, "/api/singbox/router/status", &status)
	return status, err
}

type Outbound struct {
	Type          string   `json:"type"`
	Tag           string   `json:"tag"`
	BindInterface string   `json:"bind_interface,omitempty"`
	Outbounds     []string `json:"outbounds,omitempty"`
	Default       string   `json:"default,omitempty"`
	Source        string   `json:"source,omitempty"`
}

func (c *Client) Outbounds(ctx context.Context) ([]Outbound, error) {
	var outbounds []Outbound
	err := c.get(ctx, "/api/singbox/router/outbounds/list", &outbounds)
	return outbounds, err
}

type ProxyGroup struct {
	Tag  string `json:"tag"`
	Type string `json:"type"`
	Now  string `json:"now"`
}

func (c *Client) ProxyGroups(ctx context.Context) ([]ProxyGroup, error) {
	var data struct {
		Groups []ProxyGroup `json:"groups"`
	}
	err := c.get(ctx, "/api/singbox/router/proxies/list", &data)
	return data.Groups, err
}

type Tunnel struct {
	Interface string
	Outbound  string
	Label     string
	Kind      string
	Enabled   bool
	Members   []string
}

func (c *Client) TunnelCatalog(ctx context.Context) ([]Tunnel, error) {
	var tunnels []Tunnel
	var awg []struct {
		Tag   string `json:"tag"`
		Label string `json:"label"`
		Kind  string `json:"kind"`
		Iface string `json:"iface"`
	}
	if err := c.get(ctx, "/api/singbox/awg-outbounds/tags", &awg); err != nil {
		return nil, err
	}
	for _, item := range awg {
		if item.Iface != "" && item.Tag != "" {
			tunnels = append(tunnels, Tunnel{Interface: item.Iface, Outbound: item.Tag, Label: item.Label, Kind: item.Kind, Enabled: true})
		}
	}
	var standalone []struct {
		Tag             string `json:"tag"`
		KernelInterface string `json:"kernelInterface"`
		Running         bool   `json:"running"`
	}
	if err := c.get(ctx, "/api/singbox/tunnels", &standalone); err != nil {
		return nil, err
	}
	for _, item := range standalone {
		if item.KernelInterface != "" && item.Tag != "" {
			tunnels = append(tunnels, Tunnel{Interface: item.KernelInterface, Outbound: item.Tag, Kind: "sing-box", Enabled: item.Running})
		}
	}
	var subscriptions []struct {
		Label       string   `json:"label"`
		SelectorTag string   `json:"selectorTag"`
		ProxyIndex  int      `json:"proxyIndex"`
		MemberTags  []string `json:"memberTags"`
		Enabled     bool     `json:"enabled"`
	}
	if err := c.get(ctx, "/api/singbox/subscriptions", &subscriptions); err != nil {
		return nil, err
	}
	for _, item := range subscriptions {
		if item.SelectorTag != "" && item.ProxyIndex >= 0 {
			tunnels = append(tunnels, Tunnel{Interface: fmt.Sprintf("t2s%d", item.ProxyIndex), Outbound: item.SelectorTag, Label: item.Label, Kind: "subscription", Enabled: item.Enabled, Members: append([]string(nil), item.MemberTags...)})
		}
	}
	var groups []struct {
		Label      string `json:"label"`
		Tag        string `json:"tag"`
		ProxyIndex int    `json:"proxyIndex"`
		Enabled    bool   `json:"enabled"`
		Members    []struct {
			Tag string `json:"tag"`
		} `json:"members"`
	}
	if err := c.get(ctx, "/api/singbox/subscriptions/groups", &groups); err != nil {
		return nil, err
	}
	for _, item := range groups {
		if item.Tag != "" && item.ProxyIndex >= 0 {
			members := make([]string, 0, len(item.Members))
			for _, member := range item.Members {
				members = append(members, member.Tag)
			}
			tunnels = append(tunnels, Tunnel{Interface: fmt.Sprintf("t2s%d", item.ProxyIndex), Outbound: item.Tag, Label: item.Label, Kind: "subscription-group", Enabled: item.Enabled, Members: members})
		}
	}
	return tunnels, nil
}

type DNSGlobals struct {
	Final    string `json:"final"`
	Strategy string `json:"strategy"`
	Timeout  string `json:"timeout"`
}

func (c *Client) DNSGlobals(ctx context.Context) (DNSGlobals, error) {
	var globals DNSGlobals
	err := c.get(ctx, "/api/singbox/router/dns/globals", &globals)
	return globals, err
}

type DNSServer struct {
	Tag              string `json:"tag"`
	Type             string `json:"type"`
	Server           string `json:"server"`
	ServerPort       int    `json:"server_port"`
	Path             string `json:"path"`
	Detour           string `json:"detour"`
	DomainResolver   any    `json:"domain_resolver"`
	Inet4BindAddress string `json:"inet4_bind_address"`
	TLS              *struct {
		ServerName                 string   `json:"server_name"`
		Insecure                   bool     `json:"insecure"`
		ALPN                       []string `json:"alpn"`
		MinVersion                 string   `json:"min_version"`
		MaxVersion                 string   `json:"max_version"`
		CertificatePublicKeySHA256 []string `json:"certificate_public_key_sha256"`
	} `json:"tls"`
}

func (c *Client) DNSServers(ctx context.Context) ([]DNSServer, error) {
	var servers []DNSServer
	err := c.get(ctx, "/api/singbox/router/dns/servers/list", &servers)
	return servers, err
}
