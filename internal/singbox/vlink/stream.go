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
	Mode                string         // xhttp mode: auto | packet-up | stream-up | stream-one
	HTTPMethod          string         // метод запроса транспорта http; пусто — умолчание sing-box
	XPaddingBytes       string         // xhttp x_padding_bytes (mandatory, non-zero); defaulted in MergeIntoOutbound
	XHTTPExtra          map[string]any // xhttp fields from ?extra=, already in sing-box snake_case (#797)
	BindInterface       string         // egress kernel interface for dial (#709)
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
//
// Эта функция — единственный сборщик транспорта и TLS в пакете. Форматы, у
// которых своего query нет (Clash YAML, Xray JSON, Amnezia), приводят свои
// поля к нему же — clashFieldsToValues и xrayStreamToValues, — и потому
// получают ровно те же возможности. Тест эквивалентности следит, чтобы это
// оставалось правдой.
//
// Набор понимаемых параметров шире, чем у share-ссылок: часть введена как
// общий язык между форматами и в самих ссылках почти не встречается.
//
//	type, security, sni, alpn, fp/fingerprint, insecure, pbk, sid — как в ссылке
//	path, host, serviceName, mode                                 — как в ссылке
//	headerType      — обфускация заголовком у type=tcp. Понимается только
//	                  http (и только без TLS) — это транспорт http sing-box;
//	                  none отсутствует, всё прочее — отказ.
//	method          — метод запроса транспорта http. У обфускации по
//	                  умолчанию GET, у h2 — умолчание sing-box.
//	ed, eh          — ранние данные ws: размер и имя заголовка. Форма "?ed=N"
//	                  внутри path эквивалентна ed=N с заголовком
//	                  Sec-WebSocket-Protocol; пустой eh означает ранние данные
//	                  прямо в пути. При обоих формах побеждает ed=.
//	extra           — объект xhttp-настроек Xray (xmux и прочее), см. #797
//	bind_interface  — исходящий интерфейс (#709)
//
// Добавляя поле в StreamBuilder, заводите ему параметр здесь: иначе форматы,
// приходящие через этот вход, снова начнут расходиться в возможностях.
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
	case "h2", "http":
		s.Network = "http"
	case "httpupgrade":
		s.Network = "httpupgrade"
	case "tcp", "raw":
		// raw — нынешнее имя tcp у Xray (infra/conf: case "raw", "tcp").
		s.Network = "tcp"
		// Обфускация заголовком у tcp-транспорта Xray/v2ray: клиент один раз
		// пишет HTTP/1.1-запрос перед полезной нагрузкой и снимает один ответ.
		// Это ровно транспорт http sing-box без TLS (transport/v2rayhttp:
		// dialHTTP → HTTPConn.writeRequest), поэтому отображаем в него. С TLS
		// тот же транспорт становится HTTP/2 — другой протокол, а не та же
		// обфускация поверх TLS, поэтому такую ссылку отвергаем, а не собираем
		// молча заведомо неработающий аутбаунд.
		switch ht := strings.ToLower(strings.TrimSpace(q.Get("headerType"))); ht {
		case "", "none":
		case "http":
			if sec := strings.ToLower(q.Get("security")); sec != "" && sec != "none" {
				return nil, fmt.Errorf("vlink: tcp headerType=http under %s: header obfuscation inside TLS has no sing-box equivalent", sec)
			}
			s.Network = "http"
			// У tcp в v2ray/Xray есть ровно два заголовка, none и http, и для
			// http клиенты шлют GET (mihomo transport/vmess/http.go:67, Xray
			// RequestConfig). sing-box без указания шлёт PUT
			// (transport/v2rayhttp/client.go), поэтому метод задаём явно:
			// сервер его обычно не проверяет, но обфускация на то и обфускация.
			s.HTTPMethod = "GET"
		default:
			// Остальные заголовки (srtp, utp, wechat-video, dtls, wireguard) —
			// это mKCP, на tcp они не существуют. Молча собрать голый tcp
			// значит выдать заведомо неработающий аутбаунд — как в #904.
			return nil, fmt.Errorf("vlink: unsupported tcp headerType %q", ht)
		}
	case "xhttp", "splithttp":
		s.Network = "xhttp"
	default:
		return nil, fmt.Errorf("vlink: unsupported transport %q", netRaw)
	}
	if strings.EqualFold(q.Get("mode"), "gun") {
		s.Network = "grpc"
	}
	// Явный метод (его несут Clash и Xray, ссылка — нет) перебивает умолчание.
	if m := strings.ToUpper(strings.TrimSpace(q.Get("method"))); m != "" && s.Network == "http" {
		s.HTTPMethod = m
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
			if s.EarlyData > 0 {
				s.EarlyDataHeaderName = "Sec-WebSocket-Protocol"
			}
		}
		s.Path = rawPath
	}
	if n, err := strconv.Atoi(q.Get("ed")); err == nil && n > 0 {
		s.EarlyData = n
		s.EarlyDataHeaderName = q.Get("eh")
	}
	s.Host = q.Get("host")
	if s.Host == "" {
		s.Host = defaultHost
	}
	s.ServiceName = q.Get("serviceName")
	s.Mode = q.Get("mode")
	if s.Network == "xhttp" {
		// The option layer refuses anything else and takes the whole config
		// down with it, so the bad link is rejected on its own instead.
		switch s.Mode {
		case "", "auto", "packet-up", "stream-up", "stream-one":
		default:
			return nil, fmt.Errorf("vlink: unsupported xhttp mode %q", s.Mode)
		}
		s.XHTTPExtra = parseXHTTPExtra(q.Get("extra"))
	}

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
			UTLSFingerprint: firstNonEmpty(q.Get("fp"), q.Get("fingerprint"), "chrome"),
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

	// Только каноническое имя поля sing-box: алиасы (bindInterface, bind)
	// ничем не подтверждены и угадывают чужой формат.
	if b := strings.TrimSpace(q.Get("bind_interface")); b != "" {
		s.BindInterface = b
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
			if s.EarlyDataHeaderName != "" {
				transport["early_data_header_name"] = s.EarlyDataHeaderName
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
			if s.HTTPMethod != "" {
				transport["method"] = s.HTTPMethod
			}
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
			for k, v := range s.XHTTPExtra {
				transport[k] = v
			}
			if s.XPaddingBytes != "" {
				transport["x_padding_bytes"] = s.XPaddingBytes
			} else if transport["x_padding_bytes"] == nil {
				transport["x_padding_bytes"] = "100-1000"
			}
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

	if s.BindInterface != "" {
		out["bind_interface"] = s.BindInterface
	}
}
