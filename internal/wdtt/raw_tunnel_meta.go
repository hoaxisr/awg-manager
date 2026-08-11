package wdtt

import (
	"regexp"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

const BackendWdttRaw = "wdtt-raw"

// DefaultRawConnectivityCheck matches managed AWG tunnel cards (HTTP 204).
func DefaultRawConnectivityCheck() *storage.ConnectivityCheckConfig {
	return &storage.ConnectivityCheckConfig{Method: "http"}
}

var rawTunnelIDSafe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// RawTunnelID returns a stable AWG-tunnels list id for a WDTT raw client instance.
func RawTunnelID(clientID string) string {
	id := strings.TrimSpace(clientID)
	if id == "" {
		id = DefaultInstanceID
	}
	safe := rawTunnelIDSafe.ReplaceAllString(id, "-")
	safe = strings.Trim(safe, "-")
	if safe == "" {
		safe = "client"
	}
	if len(safe) > 20 {
		safe = safe[:20]
	}
	return "wdttraw-" + safe
}

// EnrichRawTunnelRecord fills live iface names and safe defaults on a registry row.
func EnrichRawTunnelRecord(record *storage.AWGTunnel, inst ClientInstance, proc ProcessStatus) {
	if record == nil {
		return
	}
	if ndms := strings.TrimSpace(proc.NdmsIface); ndms != "" {
		record.RawNdmsIface = ndms
	} else if ndms := strings.TrimSpace(inst.Config.NdmsIface); ndms != "" {
		record.RawNdmsIface = ndms
	}
	if raw := strings.TrimSpace(proc.RawIface); raw != "" {
		record.RawKernelIface = raw
	} else if raw := strings.TrimSpace(inst.Config.RawIface); raw != "" {
		record.RawKernelIface = raw
	} else if kernel := strings.TrimSpace(inst.Config.kernelRawIface()); kernel != "" {
		record.RawKernelIface = kernel
	}
	if record.ConnectivityCheck == nil {
		record.ConnectivityCheck = DefaultRawConnectivityCheck()
	}
}

// BuildRawTunnelRecord builds or updates the metadata row shown in AWG tunnels.
func BuildRawTunnelRecord(inst ClientInstance, running bool, proc ProcessStatus) storage.AWGTunnel {
	cfg := inst.Config
	tid := RawTunnelID(inst.ID)
	name := TunnelNameFromClient(inst)
	if name == "" {
		name = "WDTT Raw"
	}
	addr := strings.TrimSpace(cfg.RawClientIP)
	if addr != "" && !strings.Contains(addr, "/") {
		addr += "/32"
	}
	kernel := strings.TrimSpace(cfg.RawIface)
	if kernel == "" {
		kernel = cfg.kernelRawIface()
	}
	mtu := 1300
	enabled := running
	record := storage.AWGTunnel{
		ID:           tid,
		Name:         name,
		Type:         "awg",
		Enabled:      enabled,
		DefaultRoute: false,
		Backend:      BackendWdttRaw,
		WdttClientID: inst.ID,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		Interface: storage.AWGInterface{
			Address: addr,
			MTU:     mtu,
		},
		Peer: storage.AWGPeer{
			Endpoint:   strings.TrimSpace(cfg.Peer),
			AllowedIPs: []string{"0.0.0.0/0"},
		},
		ConnectivityCheck: DefaultRawConnectivityCheck(),
	}
	EnrichRawTunnelRecord(&record, inst, proc)
	return record
}
