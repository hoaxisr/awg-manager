package wg

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// ClientImpl is the WireGuard client implementation.
type ClientImpl struct{}

// New creates a new WireGuard client.
func New() *ClientImpl {
	return &ClientImpl{}
}

// SetConf applies a configuration file to an interface.
func (c *ClientImpl) SetConf(ctx context.Context, iface, confPath string) error {
	result, err := exec.Run(ctx, "/opt/sbin/awg", "setconf", iface, confPath)
	if err != nil {
		return fmt.Errorf("awg setconf %s: %w", iface, exec.FormatError(result, err))
	}
	return nil
}

// Show retrieves the current state of an interface.
func (c *ClientImpl) Show(ctx context.Context, iface string) (*ShowResult, error) {
	result, err := exec.Run(ctx, "/opt/sbin/awg", "show", iface)
	if err != nil {
		return nil, fmt.Errorf("awg show %s: %w", iface, exec.FormatError(result, err))
	}
	if result == nil {
		return nil, fmt.Errorf("awg show %s returned nil", iface)
	}

	return parseShowOutput(result.Stdout), nil
}

// RemovePeer removes a peer from an interface.
func (c *ClientImpl) RemovePeer(ctx context.Context, iface, publicKey string) error {
	if publicKey == "" {
		return nil // Nothing to remove
	}
	result, err := exec.Run(ctx, "/opt/sbin/awg", "set", iface, "peer", publicKey, "remove")
	if err != nil {
		return fmt.Errorf("awg remove peer %s: %w", iface, exec.FormatError(result, err))
	}
	return nil
}

// GetPeerPublicKey extracts the peer public key from interface state.
func (c *ClientImpl) GetPeerPublicKey(ctx context.Context, iface string) (string, error) {
	result, err := c.Show(ctx, iface)
	if err != nil {
		return "", err
	}
	if !result.HasPeer {
		return "", nil
	}
	return result.PeerPublicKey, nil
}

// parseShowOutput parses the output of awg show.
func parseShowOutput(output string) *ShowResult {
	result := &ShowResult{}

	lines := strings.Split(output, "\n")
	inPeerSection := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Check for peer section start
		if strings.HasPrefix(strings.ToLower(line), "peer:") {
			inPeerSection = true
			result.HasPeer = true
			result.PeerPublicKey = parseField(line, "peer")
			continue
		}

		if inPeerSection {
			// Parse peer fields
			switch {
			case strings.HasPrefix(strings.ToLower(line), "endpoint:"):
				result.Endpoint = parseField(line, "endpoint")
			case strings.HasPrefix(strings.ToLower(line), "allowed ips:"):
				ips := parseField(line, "allowed ips")
				if ips != "" {
					result.AllowedIPs = strings.Split(ips, ", ")
				}
			case strings.HasPrefix(strings.ToLower(line), "latest handshake:"):
				result.LastHandshake = parseHandshakeTime(parseField(line, "latest handshake"))
			case strings.HasPrefix(strings.ToLower(line), "transfer:"):
				result.RxBytes, result.TxBytes = parseTransfer(parseField(line, "transfer"))
			}
		} else {
			// Parse interface fields
			switch {
			case strings.HasPrefix(strings.ToLower(line), "public key:"):
				result.PublicKey = parseField(line, "public key")
			case strings.HasPrefix(strings.ToLower(line), "listening port:"):
				portStr := parseField(line, "listening port")
				if port, err := strconv.Atoi(portStr); err == nil {
					result.ListenPort = port
				}
			}
		}
	}

	return result
}

// parseField extracts the value after "field:" from a line.
func parseField(line, field string) string {
	fieldLower := strings.ToLower(field)
	lineLower := strings.ToLower(line)

	if idx := strings.Index(lineLower, fieldLower+":"); idx != -1 {
		colonIdx := strings.Index(line[idx:], ":")
		if colonIdx != -1 {
			return strings.TrimSpace(line[idx+colonIdx+1:])
		}
	}
	return ""
}

// parseHandshakeTime parses handshake time from awg show output.
// Format: "X minutes, Y seconds ago" or "X hours, Y minutes ago" etc.
func parseHandshakeTime(s string) time.Time {
	if s == "" || s == "none" {
		return time.Time{}
	}

	// Parse relative time
	var totalSeconds int64
	parts := strings.Split(s, ",")

	for _, part := range parts {
		part = strings.TrimSpace(part)
		fields := strings.Fields(part)
		if len(fields) < 2 {
			continue
		}

		value, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}

		unit := strings.ToLower(fields[1])
		switch {
		case strings.HasPrefix(unit, "second"):
			totalSeconds += value
		case strings.HasPrefix(unit, "minute"):
			totalSeconds += value * 60
		case strings.HasPrefix(unit, "hour"):
			totalSeconds += value * 3600
		case strings.HasPrefix(unit, "day"):
			totalSeconds += value * 86400
		}
	}

	if totalSeconds > 0 {
		return time.Now().Add(-time.Duration(totalSeconds) * time.Second)
	}
	return time.Time{}
}

// parseTransfer parses transfer stats from awg show output.
// Format: "123.45 KiB received, 67.89 KiB sent"
func parseTransfer(s string) (rx, tx int64) {
	if s == "" {
		return 0, 0
	}

	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "received") {
			rx = parseBytes(part)
		} else if strings.Contains(part, "sent") {
			tx = parseBytes(part)
		}
	}
	return
}

// parseBytes converts "123.45 KiB" to bytes.
func parseBytes(s string) int64 {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return 0
	}

	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}

	unit := strings.ToLower(fields[1])
	switch {
	case strings.HasPrefix(unit, "kib"):
		return int64(value * 1024)
	case strings.HasPrefix(unit, "mib"):
		return int64(value * 1024 * 1024)
	case strings.HasPrefix(unit, "gib"):
		return int64(value * 1024 * 1024 * 1024)
	case strings.HasPrefix(unit, "b"):
		return int64(value)
	}
	return int64(value)
}

// Ensure ClientImpl implements Client interface.
var _ Client = (*ClientImpl)(nil)
