package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

var confDir = "/opt/etc/awg-manager"

// storedToConfig converts storage.AWGTunnel to tunnel.Config.
func storedToConfig(stored *storage.AWGTunnel) tunnel.Config {
	names := tunnel.NewNames(stored.ID)
	ipv4, ipv6 := splitAddresses(stored.Interface.Address)
	var dns []string
	if stored.Interface.DNS != "" {
		for _, part := range strings.Split(stored.Interface.DNS, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				dns = append(dns, part)
			}
		}
	}
	return tunnel.Config{
		ID:           stored.ID,
		Name:         stored.Name,
		Address:      ipv4,
		AddressIPv6:  ipv6,
		MTU:          stored.Interface.MTU,
		DNS:          dns,
		ConfPath:     names.ConfPath,
		ISPInterface: stored.ISPInterface,
	}
}

// splitAddresses splits a WireGuard Address field (which may contain
// comma-separated IPv4 and IPv6 addresses) into separate values.
func splitAddresses(address string) (ipv4, ipv6 string) {
	for _, part := range strings.Split(address, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Strip CIDR prefix for the config — operators add it themselves
		host := part
		if idx := strings.Index(part, "/"); idx != -1 {
			host = part[:idx]
		}
		if strings.Contains(host, ":") {
			ipv6 = host
		} else {
			ipv4 = host
		}
	}
	return
}

// writeConfigFile generates and writes the WireGuard config file for tunnel start.
func writeConfigFile(stored *storage.AWGTunnel) error {
	if err := os.MkdirAll(confDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	content := config.Generate(stored)
	confPath := filepath.Join(confDir, stored.ID+".conf")
	if err := os.WriteFile(confPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	return nil
}

// ifaceNameForTunnel returns the kernel interface name for a tunnel.
func ifaceNameForTunnel(stored *storage.AWGTunnel) string {
	if stored.Backend == "nativewg" {
		return nwg.NewNWGNames(stored.NWGIndex).IfaceName
	}
	return tunnel.NewNames(stored.ID).IfaceName
}

// collectManagedIfaceNames returns interface names for all stored tunnels.
func collectManagedIfaceNames(store *storage.AWGTunnelStore) []string {
	tunnels, err := store.List()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(tunnels))
	for _, t := range tunnels {
		names = append(names, ifaceNameForTunnel(&t))
	}
	return names
}
