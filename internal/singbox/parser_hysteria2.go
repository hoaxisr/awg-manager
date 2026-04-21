package singbox

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

func parseHysteria2(raw string) (*ParsedOutbound, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse hysteria2 url: %w", err)
	}
	if u.Scheme != "hysteria2" && u.Scheme != "hy2" {
		return nil, fmt.Errorf("not a hysteria2 link: scheme=%s", u.Scheme)
	}
	// Hysteria2 treats the entire userinfo as one opaque auth string. For
	// URIs with "user:pass@host" syntax, official clients join the two
	// halves back with a colon rather than keeping only username or only
	// password. url.User.Username() alone would silently drop "pass".
	password := u.User.Username()
	if pw, ok := u.User.Password(); ok && pw != "" {
		password = password + ":" + pw
	}
	if password == "" {
		return nil, fmt.Errorf("missing password")
	}
	host := u.Hostname()
	portStr := u.Port()
	if host == "" || portStr == "" {
		return nil, fmt.Errorf("missing host or port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("bad port: %w", err)
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("port out of range: %d", port)
	}
	q := u.Query()
	// pinSHA256 (SHA-256 of the DER cert, hex) cannot be translated to
	// sing-box certificate_public_key_sha256 (SHA-256 of the public key,
	// base64) without the server certificate — different hashes, different
	// bytes. Silently drop instead of emitting a wrong pin that would
	// fail every handshake. With insecure=1 (the common companion param)
	// the tunnel still connects.
	tag := u.Fragment
	if tag == "" {
		tag = fmt.Sprintf("hysteria2-%s-%d", host, port)
	}

	ob := map[string]any{
		"type":        "hysteria2",
		"tag":         tag,
		"server":      host,
		"server_port": port,
		"password":    password,
		"hop_interval": "10s",
	}
	if obfsType := q.Get("obfs"); obfsType != "" {
		ob["obfs"] = map[string]any{
			"type":     obfsType,
			"password": q.Get("obfs-password"),
		}
	}
	tls := map[string]any{
		"enabled": true,
		"alpn":    []string{"h3"},
	}
	if sni := q.Get("sni"); sni != "" {
		tls["server_name"] = sni
	}
	if ins := q.Get("insecure"); ins == "1" || ins == "true" {
		tls["insecure"] = true
	}
	ob["tls"] = tls

	b, err := json.Marshal(ob)
	if err != nil {
		return nil, fmt.Errorf("marshal hysteria2 outbound: %w", err)
	}
	return &ParsedOutbound{
		Tag: tag, Protocol: "hysteria2",
		Server: host, Port: port, Outbound: b,
	}, nil
}
