package mihomonative

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// CompileSocks parses a SOCKS5/SOCKS share URI for native Mihomo execution.
func CompileSocks(raw string, preference, routingEngine EnginePreference) (*ProxyNode, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || (!strings.EqualFold(u.Scheme, "socks5") && !strings.EqualFold(u.Scheme, "socks")) {
		return nil, fmt.Errorf("mihomo socks: invalid URI scheme %q", u.Scheme)
	}

	if u.Hostname() == "" {
		return nil, fmt.Errorf("mihomo socks: server host is required")
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("mihomo socks: valid port is required")
	}

	compatibility := CheckSocks()
	selected, reason := SelectEngine(preference, compatibility, routingEngine)
	if selected == "" {
		return nil, fmt.Errorf("mihomo socks: %s", reason)
	}

	name, _ := url.PathUnescape(u.Fragment)
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("%s:%d", u.Hostname(), port)
	}

	proxy := map[string]interface{}{
		"name":         name,
		"type":         "socks5",
		"server":       u.Hostname(),
		"port":         port,
		"udp":          true,
		"routing-mark": 666,
	}

	if u.User != nil {
		if username := u.User.Username(); username != "" {
			proxy["username"] = username
		}
		if password, hasPassword := u.User.Password(); hasPassword {
			proxy["password"] = password
		}
	}

	return &ProxyNode{
		Name:             name,
		Protocol:         "socks5",
		Transport:        "tcp",
		EnginePreference: preference,
		SelectedEngine:   selected,
		RawURI:           raw,
		NativeConfig:     proxy,
		Compatibility:    compatibility,
		Enabled:          true,
	}, nil
}
