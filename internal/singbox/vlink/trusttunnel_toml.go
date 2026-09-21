package vlink

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// Два TOML-формата TrustTunnel (стенд 21.09): экспорт endpoint — плоский, ключ
// client_random_prefix; конфиг CLI-клиента — секция [endpoint], ключ client_random.
// Поля одни и те же, поэтому одна структура и два места, откуда её взять.
type ttTOMLEndpoint struct {
	Hostname         string   `toml:"hostname"`
	Addresses        []string `toml:"addresses"`
	CustomSNI        string   `toml:"custom_sni"`
	Username         string   `toml:"username"`
	Password         string   `toml:"password"`
	SkipVerification bool     `toml:"skip_verification"`
	Certificate      string   `toml:"certificate"`
	UpstreamProtocol string   `toml:"upstream_protocol"`
	AntiDPI          bool     `toml:"anti_dpi"`
	Name             string   `toml:"name"`
}

type ttTOMLDoc struct {
	ttTOMLEndpoint
	Endpoint *ttTOMLEndpoint `toml:"endpoint"`
}

func (d ttTOMLDoc) endpoint() ttTOMLEndpoint {
	if d.Endpoint != nil {
		return *d.Endpoint
	}
	return d.ttTOMLEndpoint
}

func (e ttTOMLEndpoint) complete() bool {
	return e.Hostname != "" && len(e.Addresses) > 0 && e.Username != "" && e.Password != ""
}

func decodeTTTOML(body []byte) (ttTOMLDoc, error) {
	var doc ttTOMLDoc
	_, err := toml.Decode(string(stripUTF8BOM(body)), &doc)
	return doc, err
}

// IsTrustTunnelTOML: тело целиком — TOML с полным набором обязательных полей
// endpoint (в корне или в [endpoint]). JSON/YAML/ссылки отсекаются самим TOML-разбором
// либо отсутствием полей.
func IsTrustTunnelTOML(body []byte) bool {
	trimmed := trimLeadingSpace(stripUTF8BOM(body))
	if len(trimmed) == 0 || trimmed[0] == '{' {
		return false // JSON (sing-box/mieru/Xray) — не наш формат; '[' не отсекаем: [endpoint]
	}
	doc, err := decodeTTTOML(trimmed)
	return err == nil && doc.endpoint().complete()
}

func ParseTrustTunnelTOML(body []byte) BatchResult {
	fail := func(msg string) BatchResult {
		return BatchResult{Errors: []ParseError{{LineIdx: 0, Scheme: "trusttunnel-toml", Message: msg, Node: true}}}
	}
	doc, err := decodeTTTOML(body)
	if err != nil {
		return fail("не удалось разобрать TOML TrustTunnel: " + err.Error())
	}
	e := doc.endpoint()
	if !e.complete() {
		return fail("TOML TrustTunnel: нужны hostname, addresses, username, password")
	}
	parsed, err := ttEndpointToOutbounds(ttEndpoint{
		Hostname: e.Hostname, Addresses: e.Addresses, CustomSNI: e.CustomSNI,
		Username: e.Username, Password: e.Password, SkipVerification: e.SkipVerification,
		Certificate: e.Certificate, UpstreamHTTP3: e.UpstreamProtocol == "http3",
		AntiDPI: e.AntiDPI, Name: e.Name,
	}, "")
	if err != nil {
		return fail(fmt.Sprintf("TOML TrustTunnel: %s", err))
	}
	return BatchResult{Outbounds: parsed}
}
