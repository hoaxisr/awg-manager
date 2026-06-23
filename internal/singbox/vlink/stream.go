package vlink

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// StreamBuilder is the intermediate result of parsing transport+TLS query
// parameters from any scheme that uses URL-form query (vless/trojan/ss
// when plugin involves transport). Each scheme maps these into its
// protocol-specific outbound JSON via MergeIntoOutbound.
type StreamBuilder struct {
	Network             string // "tcp" | "ws" | "grpc" | "http" | "httpupgrade" | "xhttp"
	TLS                 *outboundTLS
	Path                string
	Host                string // ws Host header / http hosts / xhttp host
	EarlyData           int
	EarlyDataHeaderName string
	ServiceName         string
	Mode                string // xhttp mode: auto | packet-up | stream-up | stream-one
	XPaddingBytes       string // xhttp x_padding_bytes (mandatory, non-zero); defaulted in MergeIntoOutbound
}

// outboundTLS is the parsed TLS / Reality block ready to be emitted as
// sing-box "tls" outbound field.
type outboundTLS struct {
	Enabled         bool          `json:"enabled"`
	ServerName      string        `json:"server_name,omitempty"`
	ALPN            []string      `json:"alpn,omitempty"`
	Insecure        bool          `json:"insecure,omitempty"`
	UTLSFingerprint string        // emitted under tls.utls.fingerprint
	Reality         *outboundRTLS // emitted under tls.reality
}

// outboundRTLS is the Reality sub-block.
type outboundRTLS struct {
	PublicKey string
	ShortID   string
}

// BuildStreamFromQuery parses transport+security parameters from a URL
// query and returns a normalized StreamBuilder. defaultHost is used as
// the WS Host header / HTTP hosts when the query doesn't specify host=.
func BuildStreamFromQuery(q url.Values, defaultHost string) (*StreamBuilder, error) {
	s := &StreamBuilder{}

	// Network normalization: type= with mode=gun override
	netRaw := strings.ToLower(q.Get("type"))
	if netRaw == "" {
		netRaw = "tcp"
	}
	switch netRaw {
	case "ws", "websocket", "w":
		s.Network = "ws"
	case "grpc":
		s.Network = "grpc"
	case "h2":
		s.Network = "http"
	case "http":
		s.Network = "http"
	case "httpupgrade":
		s.Network = "httpupgrade"
	case "tcp":
		s.Network = "tcp"
	case "xhttp", "splithttp":
		s.Network = "xhttp"
	default:
		return nil, fmt.Errorf("vlink: unsupported transport %q", netRaw)
	}
	if strings.EqualFold(q.Get("mode"), "gun") {
		s.Network = "grpc"
	}

	// Path / Host / WS early data
	rawPath := q.Get("path")
	if rawPath != "" {
		// Extract ?ed=N from path — web4core pattern.
		if idx := strings.Index(rawPath, "?ed="); idx >= 0 {
			rest := rawPath[idx+4:]
			if amp := strings.Index(rest, "&"); amp >= 0 {
				if n, err := strconv.Atoi(rest[:amp]); err == nil {
					s.EarlyData = n
				}
			} else {
				if n, err := strconv.Atoi(rest); err == nil {
					s.EarlyData = n
				}
			}
			rawPath = rawPath[:idx]
		}
		s.Path = rawPath
	}
	s.Host = q.Get("host")
	if s.Host == "" {
		s.Host = defaultHost
	}
	s.ServiceName = q.Get("serviceName")
	s.Mode = q.Get("mode")

	// TLS / Reality
	sec := strings.ToLower(q.Get("security"))
	switch sec {
	case "tls":
		s.TLS = &outboundTLS{
			Enabled:         true,
			ServerName:      q.Get("sni"),
			ALPN:            splitCSV(q.Get("alpn")),
			UTLSFingerprint: firstNonEmpty(q.Get("fp"), q.Get("fingerprint")),
			Insecure:        boolish(q.Get("insecure")),
		}
		if s.TLS.ServerName == "" {
			s.TLS.ServerName = defaultHost
		}
	case "reality":
		sid := q.Get("sid")
		if comma := strings.Index(sid, ","); comma >= 0 {
			sid = sid[:comma]
		}
		if !isHex(sid) || len(sid) > 16 {
			return nil, fmt.Errorf("vlink: reality sid %q must be <=16 hex chars", sid)
		}
		s.TLS = &outboundTLS{
			Enabled:         true,
			ServerName:      q.Get("sni"),
			UTLSFingerprint: firstNonEmpty(q.Get("fp"), q.Get("fingerprint")),
			Reality: &outboundRTLS{
				PublicKey: q.Get("pbk"),
				ShortID:   sid,
			},
		}
		if s.TLS.ServerName == "" {
			s.TLS.ServerName = defaultHost
		}
	case "none", "":
		// no TLS
	default:
		return nil, fmt.Errorf("vlink: unknown security %q", sec)
	}

	return s, nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func boolish(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func isHex(s string) bool {
	if s == "" {
		return true // empty is allowed (no sid)
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// MergeIntoOutbound writes transport + TLS fields into a partially-built
// outbound JSON map. The protocol-specific parser populates protocol fields
// (uuid, password, server, etc.) and then calls this to add the shared
// transport+TLS shape.
func (s *StreamBuilder) MergeIntoOutbound(out map[string]any) {
	if s.Network != "tcp" {
		transport := map[string]any{}
		switch s.Network {
		case "ws":
			transport["type"] = "ws"
			if s.Path != "" {
				transport["path"] = s.Path
			}
			if s.Host != "" {
				transport["headers"] = map[string]any{"Host": s.Host}
			}
			if s.EarlyData > 0 {
				transport["max_early_data"] = s.EarlyData
			}
		case "grpc":
			transport["type"] = "grpc"
			if s.ServiceName != "" {
				transport["service_name"] = s.ServiceName
			} else if s.Path != "" {
				transport["service_name"] = s.Path
			}
		case "http":
			transport["type"] = "http"
			if s.Host != "" {
				transport["host"] = []string{s.Host}
			}
			if s.Path != "" {
				transport["path"] = s.Path
			}
		case "httpupgrade":
			// httpupgrade carries host as a top-level string (not headers.Host
			// like ws) and has no max_early_data.
			transport["type"] = "httpupgrade"
			if s.Host != "" {
				transport["host"] = s.Host
			}
			if s.Path != "" {
				transport["path"] = s.Path
			}
		case "xhttp":
			// host is a separate top-level field (the option layer rejects a
			// headers key named "host"). x_padding_bytes is mandatory and must
			// be non-zero, so always emit a default when unset.
			transport["type"] = "xhttp"
			if s.Path != "" {
				transport["path"] = s.Path
			}
			if s.Host != "" {
				transport["host"] = s.Host
			}
			if s.Mode != "" {
				transport["mode"] = s.Mode
			}
			pad := s.XPaddingBytes
			if pad == "" {
				pad = "100-1000"
			}
			transport["x_padding_bytes"] = pad
		}
		out["transport"] = transport
	}

	if s.TLS != nil {
		tls := map[string]any{
			"enabled":     s.TLS.Enabled,
			"server_name": s.TLS.ServerName,
		}
		if len(s.TLS.ALPN) > 0 {
			tls["alpn"] = s.TLS.ALPN
		}
		if s.TLS.Insecure {
			tls["insecure"] = true
		}
		if s.TLS.UTLSFingerprint != "" {
			tls["utls"] = map[string]any{
				"enabled":     true,
				"fingerprint": s.TLS.UTLSFingerprint,
			}
		}
		if s.TLS.Reality != nil {
			tls["reality"] = map[string]any{
				"enabled":    true,
				"public_key": s.TLS.Reality.PublicKey,
				"short_id":   s.TLS.Reality.ShortID,
			}
		}
		out["tls"] = tls
	}
}
