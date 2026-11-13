package awgm

import (
	"context"
	"fmt"
	"strings"
	"time"

	simpledns "github.com/hoaxisr/awg-manager/awgmautolist/internal/dns"
)

type DNSCatalogClient interface {
	DNSGlobals(context.Context) (DNSGlobals, error)
	DNSServers(context.Context) ([]DNSServer, error)
}

// NewDNSResolver selects exactly one configured sing-box server. No AWGM or OS
// resolver is consulted for the actual A/AAAA exchange.
func NewDNSResolver(ctx context.Context, client DNSCatalogClient, routes *RouteResolver, resolveVia string) (*simpledns.Resolver, error) {
	globals, err := client.DNSGlobals(ctx)
	if err != nil {
		return nil, err
	}
	servers, err := client.DNSServers(ctx)
	if err != nil {
		return nil, err
	}
	tag := strings.TrimSpace(resolveVia)
	if tag == "" {
		tag = globals.Final
	} else {
		tag = strings.TrimPrefix(tag, "sb:")
	}
	if tag == "" {
		return nil, fmt.Errorf("sing-box dns.final is empty; select sb:<tag>")
	}
	var selected *DNSServer
	for i := range servers {
		if servers[i].Tag == tag {
			if selected != nil {
				return nil, fmt.Errorf("duplicate sing-box DNS tag %q", tag)
			}
			selected = &servers[i]
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("sing-box DNS server %q not found", tag)
	}
	if selected.DomainResolver != nil {
		return nil, fmt.Errorf("DNS %q: domain_resolver bootstrap is not supported; use a numeric server", tag)
	}
	if selected.Inet4BindAddress != "" {
		return nil, fmt.Errorf("DNS %q: inet4_bind_address is not supported; use an unambiguous detour", tag)
	}
	if selected.ServerPort < 0 || selected.ServerPort > 65535 {
		return nil, fmt.Errorf("DNS %q: invalid server_port %d", tag, selected.ServerPort)
	}
	server := simpledns.Server{
		Type:       simpledns.ServerType(selected.Type),
		Server:     selected.Server,
		ServerPort: uint16(selected.ServerPort),
		Path:       selected.Path,
	}
	if selected.TLS != nil {
		if len(selected.TLS.CertificatePublicKeySHA256) > 0 || selected.TLS.MinVersion != "" || selected.TLS.MaxVersion != "" {
			return nil, fmt.Errorf("DNS %q: custom TLS pinning/version is not supported", tag)
		}
		for _, alpn := range selected.TLS.ALPN {
			if alpn != "h2" && alpn != "http/1.1" {
				return nil, fmt.Errorf("DNS %q: unsupported ALPN %q", tag, alpn)
			}
		}
		server.TLSServerName = selected.TLS.ServerName
		server.Insecure = selected.TLS.Insecure
	}
	if globals.Timeout != "" {
		server.Timeout, err = time.ParseDuration(globals.Timeout)
		if err != nil || server.Timeout <= 0 {
			return nil, fmt.Errorf("sing-box DNS timeout %q is invalid", globals.Timeout)
		}
	}
	if selected.Detour != "" && selected.Detour != "direct" {
		if routes == nil {
			return nil, fmt.Errorf("DNS %q: detour %q cannot be resolved", tag, selected.Detour)
		}
		path, err := routes.ResolveOutbound(ctx, selected.Detour)
		if err != nil {
			return nil, fmt.Errorf("DNS %q detour: %w", tag, err)
		}
		server.DetourInterface = path.Interface
	}
	return simpledns.NewResolver(server)
}
