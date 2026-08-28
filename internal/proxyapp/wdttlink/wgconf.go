package wdttlink

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

const wgConfigEventPrefix = "__WDTT_EVENT__|CONFIG|"

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
	var last string
	for _, line := range strings.Split(log, "\n") {
		if c := parseWGConfigEvent(line); c != "" {
			last = c
		}
	}
	return last
}

// parseWGConfigEvent extracts the WG config text from a single
// __WDTT_EVENT__|CONFIG|{json} line, or "" if the line is not such an event.
func parseWGConfigEvent(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, wgConfigEventPrefix) {
		return ""
	}
	var data struct {
		Config string `json:"config"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, wgConfigEventPrefix)), &data); err != nil {
		return ""
	}
	return strings.TrimSpace(data.Config)
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

// MatchingAWGTunnel finds an existing tunnel with the same WG peer public key.
// Matching only by peer key avoids adopting an unrelated user tunnel that
// happens to share a 10.x interface address.
func MatchingAWGTunnel(tunnels []storage.AWGTunnel, wgConf string) *storage.AWGTunnel {
	key := strings.TrimSpace(ExtractPeerPublicKey(wgConf))
	if key == "" {
		return nil
	}
	for i := range tunnels {
		if strings.TrimSpace(tunnels[i].Peer.PublicKey) == key {
			return &tunnels[i]
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
