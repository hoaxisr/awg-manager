package singbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

// VerifyUplinkRoute validates a dedicated inbound across active fragments.
func VerifyUplinkRoute(dir, tag string) (int, error) { return verifyUplinkRoute(dir, tag) }

// verifyUplinkRoute examines active fragments in the same filename order as
// sing-box -C. A port inferred from a tag alone is insufficient: an earlier
// routing rule could otherwise send the health check directly or via a group.
func verifyUplinkRoute(dir, tag string) (int, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var rules []map[string]any
	port := 0
	inTag := tag + "-in"
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			return 0, err
		}
		var doc struct {
			Inbounds []map[string]any `json:"inbounds"`
			Route    struct {
				Rules []map[string]any `json:"rules"`
			} `json:"route"`
		}
		if err = json.Unmarshal(b, &doc); err != nil {
			return 0, err
		}
		for _, ib := range doc.Inbounds {
			if ib["tag"] != inTag {
				continue
			}
			if port != 0 {
				return 0, fmt.Errorf("%w: duplicate inbound", uplink.ErrUnsupported)
			}
			if (ib["type"] != "mixed" && ib["type"] != "socks") || ib["listen"] != "127.0.0.1" || ib["users"] != nil {
				return 0, fmt.Errorf("%w: dedicated unauthenticated loopback SOCKS inbound required", uplink.ErrUnsupported)
			}
			n, ok := ib["listen_port"].(float64)
			if !ok || n != float64(int(n)) {
				return 0, fmt.Errorf("invalid SOCKS port")
			}
			port = int(n)
		}
		rules = append(rules, doc.Route.Rules...)
	}
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("%w: dedicated SOCKS inbound required", uplink.ErrUnsupported)
	}
	for _, rule := range rules {
		// Flat UDP-only/other-port rules cannot match our TCP/443 probes.
		// Logical or inverted rules need a full evaluator and remain rejected.
		if rule["type"] == nil && rule["invert"] != true {
			if rule["network"] == "udp" {
				continue
			}
			if ports, ok := rule["port"].([]any); ok && len(ports) > 0 && rule["port_range"] == nil {
				matches := false
				valid := true
				for _, p := range ports {
					port, ok := p.(float64)
					if !ok || port < 1 || port > 65535 || port != float64(int(port)) {
						valid = false
					}
					if p == float64(443) {
						matches = true
					}
				}
				if valid && !matches {
					continue
				}
			}
		}
		matches := false
		switch in := rule["inbound"].(type) {
		case string:
			matches = in == inTag
		case []any:
			for _, v := range in {
				if v == inTag {
					matches = true
				}
			}
		default:
			return 0, fmt.Errorf("%w: an earlier routing rule may bypass the tunnel probe", uplink.ErrUnsupported)
		}
		if !matches {
			continue
		}
		if action, _ := rule["action"].(string); action == "reject" && rule["outbound"] == nil {
			continue
		} // denial cannot bypass the proxy
		if rule["action"] == "sniff" {
			for key := range rule {
				if key != "inbound" && key != "action" && key != "timeout" && key != "sniffer" {
					return 0, fmt.Errorf("%w: unsupported sniff option", uplink.ErrUnsupported)
				}
			}
			continue
		}
		if rule["outbound"] != tag || (rule["action"] != nil && rule["action"] != "route") {
			return 0, fmt.Errorf("%w: inbound routes to another outbound", uplink.ErrUnsupported)
		}
		for key := range rule {
			if key != "inbound" && key != "outbound" && key != "action" {
				return 0, fmt.Errorf("%w: conditional inbound routing", uplink.ErrUnsupported)
			}
		}
		return port, nil
	}
	return 0, fmt.Errorf("%w: no unconditional inbound route", uplink.ErrUnsupported)
}
