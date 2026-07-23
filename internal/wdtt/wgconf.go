package wdtt

import (
	"encoding/json"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

var wgBoxLineRE = regexp.MustCompile(`^║\s*(.*?)\s*║\s*$`)

// ExtractWGConfigFromLog returns WireGuard client config text embedded in wdtt-client
// stdout: either __WDTT_EVENT__|CONFIG|… (WDTT_EVENTS=1) or the decorative box printout.
func ExtractWGConfigFromLog(log string) string {
	if conf := extractWGFromEvents(log); conf != "" {
		return conf
	}
	return extractWGFromBox(log)
}

func extractWGFromEvents(log string) string {
	const prefix = "__WDTT_EVENT__|CONFIG|"
	var last string
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		payload := strings.TrimPrefix(line, prefix)
		var data struct {
			Config string `json:"config"`
		}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			continue
		}
		if c := strings.TrimSpace(data.Config); c != "" {
			last = c
		}
	}
	return last
}

func extractWGFromBox(log string) string {
	lines := strings.Split(log, "\n")
	var blocks [][]string
	var cur []string
	inBox := false
	for _, line := range lines {
		if strings.Contains(line, "WireGuard") && strings.Contains(line, "════") {
			inBox = true
			cur = nil
			continue
		}
		if !inBox {
			continue
		}
		if strings.Contains(line, "╚") {
			if len(cur) > 0 {
				blocks = append(blocks, cur)
			}
			inBox = false
			cur = nil
			continue
		}
		m := wgBoxLineRE.FindStringSubmatch(line)
		if len(m) != 2 {
			continue
		}
		text := strings.TrimSpace(m[1])
		if text != "" {
			cur = append(cur, text)
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	last := blocks[len(blocks)-1]
	conf := strings.Join(last, "\n")
	if !looksLikeWGConf(conf) {
		return ""
	}
	return conf
}

func looksLikeWGConf(conf string) bool {
	lower := strings.ToLower(conf)
	return strings.Contains(lower, "[interface]") && strings.Contains(lower, "[peer]")
}

// PatchWgConfEndpoint sets [Peer] Endpoint to 127.0.0.1:port (local wdtt-client listen).
func PatchWgConfEndpoint(conf string, localPort int) string {
	if localPort <= 0 || localPort > 65535 {
		localPort = 9000
	}
	host := "127.0.0.1"
	endpoint := host + ":" + strconv.Itoa(localPort)

	lines := strings.Split(conf, "\n")
	inPeer := false
	replaced := false
	out := make([]string, 0, len(lines)+3)
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			inPeer = strings.EqualFold(t, "[peer]")
			out = append(out, line)
			continue
		}
		if inPeer && strings.HasPrefix(strings.ToLower(t), "endpoint") {
			replaced = true
			out = append(out, "Endpoint = "+endpoint)
			continue
		}
		out = append(out, line)
	}
	if !replaced && strings.TrimSpace(conf) != "" {
		out = append(out, "", "[Peer]", "Endpoint = "+endpoint)
	}
	return strings.Join(out, "\n")
}

// ExtractInterfaceAddress returns [Interface] Address from a WireGuard config snippet.
func ExtractInterfaceAddress(conf string) string {
	lines := strings.Split(conf, "\n")
	inIface := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			inIface = strings.EqualFold(t, "[interface]")
			continue
		}
		if !inIface {
			continue
		}
		if strings.HasPrefix(strings.ToLower(t), "address") {
			parts := strings.SplitN(t, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// NormalizeWGAddress compares interface addresses case-insensitively (host part).
func NormalizeWGAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	host := addr
	if strings.Contains(addr, "/") {
		if h, _, err := net.ParseCIDR(addr); err == nil && h != nil {
			host = h.String()
		} else if i := strings.Index(addr, "/"); i > 0 {
			host = addr[:i]
		}
	}
	return strings.ToLower(strings.TrimSpace(host))
}

// MatchingAWGTunnel finds an existing tunnel with the same WG peer key or interface IP.
func MatchingAWGTunnel(tunnels []storage.AWGTunnel, wgConf string) *storage.AWGTunnel {
	key := strings.TrimSpace(ExtractPeerPublicKey(wgConf))
	addr := NormalizeWGAddress(ExtractInterfaceAddress(wgConf))
	for i := range tunnels {
		tun := &tunnels[i]
		if key != "" && strings.TrimSpace(tun.Peer.PublicKey) == key {
			return tun
		}
		if addr != "" && NormalizeWGAddress(tun.Interface.Address) == addr {
			return tun
		}
	}
	return nil
}

// ExtractPeerPublicKey returns [Peer] PublicKey from a WireGuard config snippet.
func ExtractPeerPublicKey(conf string) string {
	lines := strings.Split(conf, "\n")
	inPeer := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			inPeer = strings.EqualFold(t, "[peer]")
			continue
		}
		if !inPeer {
			continue
		}
		if strings.HasPrefix(strings.ToLower(t), "publickey") {
			parts := strings.SplitN(t, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func ListenPortFromAddr(listen string) int {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return 9000
	}
	if port, err := listenPort(listen); err == nil {
		return port
	}
	return 9000
}
