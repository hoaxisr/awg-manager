package vlink

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// XrayOutbound represents a single outbound in an Xray/V2Ray JSON config.
type XrayOutbound struct {
	Tag            string          `json:"tag"`
	Protocol       string          `json:"protocol"`
	Settings       json.RawMessage `json:"settings"`
	StreamSettings *XrayStream     `json:"streamSettings"`
}

type XrayStream struct {
	Network         string             `json:"network"`
	Security        string             `json:"security"`
	TLSSettings     *XrayTLSConfig     `json:"tlsSettings"`
	RealitySettings *XrayRealityConfig `json:"realitySettings"`
	TCPSettings     *XrayTCPConfig     `json:"tcpSettings"`
	// Современный Xray зовёт ту же сеть "raw" и кладёт блок в rawSettings,
	// причём он ПЕРЕБИВАЕТ tcpSettings (infra/conf/transport_internet.go:119).
	RAWSettings         *XrayTCPConfig  `json:"rawSettings"`
	WSSettings          *XrayWSConfig   `json:"wsSettings"`
	GRPCSettings        *XrayGRPCConfig `json:"grpcSettings"`
	HTTPSettings        *XrayHTTPConfig `json:"httpSettings"`
	HTTPUpgradeSettings *XrayWSConfig   `json:"httpupgradeSettings"`
	// xhttp несёт настройки плоско (xmux, xPaddingBytes, ...) и/или внутри
	// "extra" — ключи в обеих формах те же, что в share-ссылке, поэтому объект
	// уезжает в разбор целиком.
	XHTTPSettings     json.RawMessage `json:"xhttpSettings"`
	SplitHTTPSettings json.RawMessage `json:"splithttpSettings"`
	// network "hysteria" транспортом не является: блок несёт версию и пароль
	// протокола, а не настройки транспорта (issue #916).
	//
	// Оба блока ниже — сырые: json.Unmarshal в типизированный массив работает
	// по принципу «всё или ничего», и неожиданная форма ОДНОГО узла унесла бы
	// разбор всей подписки без единой ошибки. Разбираются они там, где нужны,
	// и отказ остаётся при своём узле. Для finalmask это особенно важно: Xray
	// кладёт его в streamSettings любого транспорта, не только hysteria.
	HysteriaSettings json.RawMessage `json:"hysteriaSettings"`
	FinalMask        json.RawMessage `json:"finalmask"`
	Sockopt          map[string]any  `json:"sockopt"`
}

type XrayTLSConfig struct {
	ServerName    string   `json:"serverName"`
	AllowInsecure bool     `json:"allowInsecure"`
	ALPN          []string `json:"alpn"`
	Fingerprint   string   `json:"fingerprint"`
}

type XrayRealityConfig struct {
	ServerName  string `json:"serverName"`
	PublicKey   string `json:"publicKey"`
	ShortID     string `json:"shortId"`
	Fingerprint string `json:"fingerprint"`
}

// XrayTCPConfig несёт только обфускацию заголовком: остальное в tcpSettings
// (acceptProxyProtocol) к транспорту не относится.
type XrayTCPConfig struct {
	Header *XrayTCPHeader `json:"header"`
}

type XrayTCPHeader struct {
	Type    string          `json:"type"`
	Request *XrayTCPRequest `json:"request"`
}

// XrayTCPRequest — заголовок запроса. path у Xray список, значения headers —
// тоже списки, но встречается и форма со строкой, поэтому разбираются как any
// общими хелперами.
type XrayTCPRequest struct {
	Method  string         `json:"method"`
	Path    []string       `json:"path"`
	Headers map[string]any `json:"headers"`
}

type XrayWSConfig struct {
	Host    string            `json:"host"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

type XrayGRPCConfig struct {
	ServiceName string `json:"serviceName"`
}

type XrayHTTPConfig struct {
	Method string   `json:"method"`
	Path   string   `json:"path"`
	Host   []string `json:"host"`
}

// VlessSettings represents settings block for VLESS protocol in Xray.
type VlessSettings struct {
	Vnext []struct {
		Address string `json:"address"`
		Port    uint16 `json:"port"`
		Users   []struct {
			ID         string `json:"id"`
			Encryption string `json:"encryption"`
			Flow       string `json:"flow"`
		} `json:"users"`
	} `json:"vnext"`
}

// TrojanSettings represents settings block for Trojan protocol in Xray.
type TrojanSettings struct {
	Servers []struct {
		Address  string `json:"address"`
		Port     uint16 `json:"port"`
		Password string `json:"password"`
	} `json:"servers"`
}

// XrayHysteriaSettings — блок settings протокола "hysteria": адрес и порт
// лежат плоско, версия дублируется в streamSettings.hysteriaSettings.
type XrayHysteriaSettings struct {
	Address string `json:"address"`
	Port    uint16 `json:"port"`
	Version int    `json:"version"`
}

// XrayHysteriaStream — streamSettings.hysteriaSettings: версия и пароль.
// Остальные поля блока серверные и на поведение клиента не влияют, поэтому
// сюда не переносятся и отказа не вызывают: masquerade Xray читает только в
// inbound (hysteria/hub.go), udpIdleTimeout — там же, клиентский диалер
// собирает менеджер UDP-сессий без него.
type XrayHysteriaStream struct {
	Version int    `json:"version"`
	Auth    string `json:"auth"`
}

// XrayFinalMask — streamSettings.finalmask: udp-маски (salamander, udphop и
// прочие) и настройки QUIC.
type XrayFinalMask struct {
	TCP        []XrayMask      `json:"tcp"`
	UDP        []XrayMask      `json:"udp"`
	QuicParams *XrayQuicParams `json:"quicParams"`
}

// XrayQuicParams — finalmask.quicParams (Xray: QuicParamsConfig). Полоса
// приходит строкой с единицей ("100 mbps"), таймауты — в секундах.
type XrayQuicParams struct {
	Congestion                    string `json:"congestion"`
	Debug                         bool   `json:"debug"`
	BbrProfile                    string `json:"bbrProfile"`
	BrutalUp                      string `json:"brutalUp"`
	BrutalDown                    string `json:"brutalDown"`
	BrutalDisableLossCompensation bool   `json:"brutalDisableLossCompensation"`
	InitStreamReceiveWindow       uint64 `json:"initStreamReceiveWindow"`
	MaxStreamReceiveWindow        uint64 `json:"maxStreamReceiveWindow"`
	InitConnectionReceiveWindow   uint64 `json:"initConnectionReceiveWindow"`
	MaxConnectionReceiveWindow    uint64 `json:"maxConnectionReceiveWindow"`
	MaxIdleTimeout                int64  `json:"maxIdleTimeout"`
	KeepAlivePeriod               int64  `json:"keepAlivePeriod"`
	DisablePathMTUDiscovery       bool   `json:"disablePathMTUDiscovery"`
	DisableChromeParrot           bool   `json:"disableChromeParrot"`
	DisableGSO                    bool   `json:"disableGSO"`
	MaxIncomingStreams            int64  `json:"maxIncomingStreams"`
	DisableStatelessReset         bool   `json:"disableStatelessReset"`
}

// XrayMask — элемент списка масок: тип и его собственный блок настроек.
type XrayMask struct {
	Type     string          `json:"type"`
	Settings json.RawMessage `json:"settings"`
}

// XraySalamander — маска salamander. packetSize — Int32Range: при непустом
// верхнем значении Xray строит вариант gecko (Salamander.Build).
type XraySalamander struct {
	Password   string          `json:"password"`
	PacketSize json.RawMessage `json:"packetSize"`
}

// XrayRealm — маска realm: реле, через которое узел и доступен. Адрес входа
// лежит в url, а не в settings.
type XrayRealm struct {
	URL         string                `json:"url"`
	StunServers []string              `json:"stunServers"`
	TLSConfig   json.RawMessage       `json:"tlsConfig"`
	IPMode      string                `json:"ipMode"`
	PortMapping *XrayRealmPortMapping `json:"portMapping"`
}

// XrayRealmPortMapping — проброс порта у реле. timeout и lifetime у Xray в
// секундах (realm/client.go умножает их на time.Second).
type XrayRealmPortMapping struct {
	Enabled  bool  `json:"enabled"`
	Timeout  int64 `json:"timeout"`
	Lifetime int64 `json:"lifetime"`
}

// XrayUDPHop — маска udphop. mode перечисляет, ЧТО меняется при прыжке;
// удалённый порт берётся только при intervalRemote/perConnRemote
// (udphop/conn.go). interval — Int32Range в секундах, remotePorts — PortList.
type XrayUDPHop struct {
	Mode        string          `json:"mode"`
	Interval    json.RawMessage `json:"interval"`
	RemotePorts json.RawMessage `json:"remotePorts"`
	RemoteIPs   []string        `json:"remoteIPs"`
	Sockopt     json.RawMessage `json:"sockopt"`
}

// ShadowsocksSettings represents settings block for Shadowsocks in Xray.
type ShadowsocksSettings struct {
	Servers []struct {
		Address  string `json:"address"`
		Port     uint16 `json:"port"`
		Method   string `json:"method"`
		Password string `json:"password"`
	} `json:"servers"`
}

type XrayConfigItem struct {
	Remarks string `json:"remarks"`
	// Сырой список: json.Unmarshal в массив структур — всё или ничего, и
	// неожиданная форма ОДНОГО поля у ОДНОГО узла унесла бы всю подписку
	// молча (ноль узлов, ноль причин). Каждый узел разбирается отдельно.
	Outbounds []json.RawMessage `json:"outbounds"`
}

// decodeXrayOutbound разбирает один аутбаунд. Ошибка остаётся при своём узле.
func decodeXrayOutbound(raw json.RawMessage) (XrayOutbound, error) {
	var ob XrayOutbound
	if err := json.Unmarshal(raw, &ob); err != nil {
		return ob, fmt.Errorf("xray: outbound is malformed")
	}
	return ob, nil
}

// isXrayRealOutbound отсеивает служебные аутбаунды Xray: они есть почти в
// каждой подписке и узлами не являются.
func isXrayRealOutbound(proto string) bool {
	switch proto {
	case "", "freedom", "blackhole", "dns", "loopback":
		return false
	}
	return true
}

// xrayRawProtocol достаёт только имя протокола. Структура из одного поля
// переживает любую форму соседних: json игнорирует то, чего в ней нет.
func xrayRawProtocol(raw json.RawMessage) string {
	var head struct {
		Protocol string `json:"protocol"`
	}
	_ = json.Unmarshal(raw, &head)
	return strings.ToLower(head.Protocol)
}

// IsXrayJSON tests whether body is a valid JSON document structured as Xray/V2Ray configuration.
func IsXrayJSON(body []byte) bool {
	trimmed := trimLeadingSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return false
	}

	// 1. Single config object {"outbounds": [...]}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err == nil {
		if rawOutbounds, ok := root["outbounds"]; ok {
			var outbounds []json.RawMessage
			if err := json.Unmarshal(rawOutbounds, &outbounds); err == nil && len(outbounds) > 0 {
				for _, ob := range outbounds {
					if isXrayProtocol(xrayRawProtocol(ob)) {
						return true
					}
				}
			}
		}
	}

	// 2. Array of config objects [{"remarks": "...", "outbounds": [...]}, ...]
	var configArr []XrayConfigItem
	if err := json.Unmarshal(body, &configArr); err == nil && len(configArr) > 0 {
		for _, item := range configArr {
			for _, ob := range item.Outbounds {
				if isXrayProtocol(xrayRawProtocol(ob)) {
					return true
				}
			}
		}
	}

	// 3. Bare array of Xray outbounds
	var arr []json.RawMessage
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		for _, raw := range arr {
			var head struct {
				Protocol string          `json:"protocol"`
				Settings json.RawMessage `json:"settings"`
			}
			if json.Unmarshal(raw, &head) != nil {
				continue
			}
			if isXrayProtocol(strings.ToLower(head.Protocol)) && len(head.Settings) > 0 {
				return true
			}
		}
	}

	return false
}

// isXrayProtocol — протоколы, по которым тело опознаётся как Xray-конфиг.
// Список обязан совпадать с тем, что умеет convertXrayOutbound: узел, который
// разбирается, но не опознаётся, до разбора просто не доходит — подписка из
// одних только таких узлов пропадает целиком (issue #916).
func isXrayProtocol(proto string) bool {
	switch proto {
	case "vless", "trojan", "shadowsocks", "hysteria", "vmess":
		return true
	}
	return false
}

// ParseXrayBody extracts outbounds from an Xray/V2Ray JSON config and translates them to sing-box outbounds.
func ParseXrayBody(body []byte) BatchResult {
	var res BatchResult

	// Try 1: Array of config objects with remarks [{"remarks": "...", "outbounds": [...]}]
	var configArr []XrayConfigItem
	if err := json.Unmarshal(body, &configArr); err == nil && len(configArr) > 0 {
		hasAny := false
		for _, item := range configArr {
			if len(item.Outbounds) > 0 {
				hasAny = true
				break
			}
		}
		if hasAny {
			// Сквозной номер узла по всему телу: индекс внутри элемента у
			// подписок Happ/Remnawave всегда 0 — все отказы схлопывались на
			// фронте в одну строку «Строка 0» (F359).
			nodeIdx := 0
			for _, item := range configArr {
				remarks := strings.TrimSpace(item.Remarks)
				var realOutbounds []json.RawMessage
				for _, raw := range item.Outbounds {
					if isXrayRealOutbound(xrayRawProtocol(raw)) {
						realOutbounds = append(realOutbounds, raw)
					}
				}

				for idx, raw := range realOutbounds {
					proto := xrayRawProtocol(raw)
					thisIdx := nodeIdx
					nodeIdx++
					if proto == "vmess" {
						res.SkippedVmess++
						continue
					}

					ob, err := decodeXrayOutbound(raw)
					if err != nil {
						res.Errors = append(res.Errors, ParseError{LineIdx: thisIdx, Scheme: proto, Message: err.Error()})
						continue
					}

					tag := ob.Tag
					if remarks != "" {
						if len(realOutbounds) > 1 {
							tag = fmt.Sprintf("%s #%d", remarks, idx+1)
						} else {
							tag = remarks
						}
					}
					ob.Tag = tag

					parsed, err := convertXrayOutbound(ob)
					if err != nil {
						res.Errors = append(res.Errors, ParseError{
							LineIdx: thisIdx,
							Scheme:  proto,
							Message: err.Error(),
						})
						continue
					}
					if parsed != nil {
						res.Outbounds = append(res.Outbounds, *parsed)
					}
				}
			}
			return res
		}
	}

	// Try 2: Single config object or bare array
	var outbounds []json.RawMessage
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err == nil {
		if rawOutbounds, ok := root["outbounds"]; ok {
			_ = json.Unmarshal(rawOutbounds, &outbounds)
		}
	}
	if len(outbounds) == 0 {
		_ = json.Unmarshal(body, &outbounds)
	}

	if len(outbounds) == 0 {
		return res
	}

	for idx, raw := range outbounds {
		proto := xrayRawProtocol(raw)
		if !isXrayRealOutbound(proto) {
			continue
		}
		if proto == "vmess" {
			res.SkippedVmess++
			continue
		}

		ob, err := decodeXrayOutbound(raw)
		if err != nil {
			res.Errors = append(res.Errors, ParseError{LineIdx: idx, Scheme: proto, Message: err.Error()})
			continue
		}

		parsed, err := convertXrayOutbound(ob)
		if err != nil {
			res.Errors = append(res.Errors, ParseError{
				LineIdx: idx,
				Scheme:  proto,
				Message: err.Error(),
			})
			continue
		}
		if parsed != nil {
			res.Outbounds = append(res.Outbounds, *parsed)
		}
	}

	return res
}

func convertXrayOutbound(ob XrayOutbound) (*ParsedOutbound, error) {
	proto := strings.ToLower(ob.Protocol)
	tag := ob.Tag
	if tag == "" {
		tag = fmt.Sprintf("%s-node", proto)
	}

	sbOutbound := map[string]any{
		"tag": tag,
	}

	var server string
	var port uint16
	var vlessFlow string

	switch proto {
	case "vless":
		var settings VlessSettings
		if err := json.Unmarshal(ob.Settings, &settings); err != nil || len(settings.Vnext) == 0 || len(settings.Vnext[0].Users) == 0 {
			return nil, fmt.Errorf("invalid vless settings")
		}
		vn := settings.Vnext[0]
		user := vn.Users[0]

		server = vn.Address
		port = vn.Port

		if user.ID == "" {
			return nil, fmt.Errorf("vless: missing uuid")
		}
		if err := checkVlessEncryption(user.Encryption); err != nil {
			return nil, err
		}
		sbOutbound["type"] = "vless"
		sbOutbound["server"] = server
		sbOutbound["server_port"] = int(port)
		sbOutbound["uuid"] = user.ID
		// Само присваивание ниже, после сборки транспорта: checkVlessFlow
		// судит по паре (flow, транспорт+TLS), а stream здесь ещё не собран.
		vlessFlow = normalizeFlow(user.Flow)

	case "trojan":
		var settings TrojanSettings
		if err := json.Unmarshal(ob.Settings, &settings); err != nil || len(settings.Servers) == 0 {
			return nil, fmt.Errorf("invalid trojan settings")
		}
		srv := settings.Servers[0]
		server = srv.Address
		port = srv.Port

		if srv.Password == "" {
			return nil, fmt.Errorf("trojan: missing password")
		}
		sbOutbound["type"] = "trojan"
		sbOutbound["server"] = server
		sbOutbound["server_port"] = int(port)
		sbOutbound["password"] = srv.Password

	case "shadowsocks":
		var settings ShadowsocksSettings
		if err := json.Unmarshal(ob.Settings, &settings); err != nil || len(settings.Servers) == 0 {
			return nil, fmt.Errorf("invalid shadowsocks settings")
		}
		srv := settings.Servers[0]
		server = srv.Address
		port = srv.Port

		if srv.Method == "" || srv.Password == "" {
			return nil, fmt.Errorf("shadowsocks: missing method or password")
		}
		sbOutbound["type"] = "shadowsocks"
		sbOutbound["server"] = server
		sbOutbound["server_port"] = int(port)
		sbOutbound["method"] = srv.Method
		sbOutbound["password"] = srv.Password

	case "hysteria":
		// Свой разбор целиком: транспорта у hysteria2 нет, поэтому общий
		// слой streamSettings для него неприменим.
		return convertXrayHysteria(ob, tag)

	default:
		return nil, fmt.Errorf("unsupported protocol: %s", proto)
	}

	if server == "" {
		return nil, fmt.Errorf("%s: missing server", proto)
	}
	if port == 0 {
		return nil, fmt.Errorf("%s: missing or invalid port", proto)
	}

	// Маски finalmask меняют формат на проводе, и Xray навешивает их на любой
	// транспорт (transport_internet.go: весь FinalMask.Tcp/Udp уходит в
	// config.Tcpmasks/Udpmasks). Своего такого слоя у sing-box нет, так что
	// узел с маской подключился бы вхолостую — отказ честнее молчания. Путь
	// hysteria сюда не попадает: у него свой разбор udp-масок.
	if err := rejectXrayForeignMasks(ob.StreamSettings); err != nil {
		return nil, err
	}

	// Транспорт и TLS собирает общий слой — тот же, что у share-ссылок и
	// Clash. Своей реализации здесь больше нет.
	var stream *StreamBuilder
	if ob.StreamSettings != nil {
		var err error
		stream, err = BuildStreamFromQuery(xrayStreamToValues(ob.StreamSettings, server), server)
		if err != nil {
			return nil, fmt.Errorf("xray: %w", err)
		}
		stream.MergeIntoOutbound(sbOutbound)
	}
	if vlessFlow != "" {
		// Xray-вход раньше ставил flow напрямую и проходил мимо проверки,
		// которую ссылки и Clash уже получили: подписка в формате Xray несла
		// чужой flow дальше и роняла применение всей конфигурации.
		if err := checkVlessFlow(vlessFlow, stream); err != nil {
			return nil, err
		}
		sbOutbound["flow"] = vlessFlow
	}

	rawJSON, err := json.Marshal(sbOutbound)
	if err != nil {
		return nil, err
	}

	return &ParsedOutbound{
		Tag:      tag,
		Protocol: proto,
		Server:   server,
		Port:     port,
		Outbound: rawJSON,
		Label:    tag,
	}, nil
}

// convertXrayHysteria собирает hysteria2 из блока Xray (issue #916). Панели
// отдают его как protocol "hysteria" с версией 2 — отдельного "hysteria2" в
// формате Xray нет. v1 — другой протокол, в проекте он не поддержан нигде,
// поэтому отвергается явно, а не молчаливо собирается как v2.
func convertXrayHysteria(ob XrayOutbound, tag string) (*ParsedOutbound, error) {
	var settings XrayHysteriaSettings
	if err := json.Unmarshal(ob.Settings, &settings); err != nil {
		return nil, fmt.Errorf("invalid hysteria settings")
	}

	var hy *XrayHysteriaStream
	if ob.StreamSettings != nil && len(ob.StreamSettings.HysteriaSettings) > 0 {
		hy = &XrayHysteriaStream{}
		if err := json.Unmarshal(ob.StreamSettings.HysteriaSettings, hy); err != nil {
			return nil, fmt.Errorf("hysteria: invalid hysteriaSettings")
		}
	}

	// Версия названа дважды, и Xray требует 2 в обоих местах: hysteria.go
	// проверяет settings, transport_method.go — hysteriaSettings. Разнобой
	// означает конфигурацию, которую сам Xray не соберёт.
	version := settings.Version
	if hy != nil && hy.Version != 0 {
		if version != 0 && version != hy.Version {
			return nil, fmt.Errorf("hysteria: version mismatch: settings %d, hysteriaSettings %d", version, hy.Version)
		}
		version = hy.Version
	}
	if version != 2 {
		return nil, fmt.Errorf("hysteria: unsupported version %d (only 2 is supported)", version)
	}
	if settings.Address == "" {
		return nil, fmt.Errorf("hysteria: missing server")
	}
	if settings.Port == 0 {
		return nil, fmt.Errorf("hysteria: missing or invalid port")
	}
	if hy == nil || hy.Auth == "" {
		return nil, fmt.Errorf("hysteria: missing password")
	}

	out := map[string]any{
		"type":        "hysteria2",
		"server":      settings.Address,
		"server_port": int(settings.Port),
		"password":    hy.Auth,
		"tag":         tag,
	}

	// hysteria2 работает только поверх TLS (QUIC), и TLS ставится безусловно:
	// узел, объявивший иное, описан неверно — собирать из него TLS-аутбаунд
	// молча нельзя.
	if ob.StreamSettings != nil {
		switch strings.ToLower(ob.StreamSettings.Security) {
		case "", "tls", "true":
		default:
			return nil, fmt.Errorf("hysteria: security %q is not usable, hysteria2 is always over TLS", ob.StreamSettings.Security)
		}
	}

	endpoint, err := applyXrayHysteriaMasks(ob.StreamSettings, out)
	if err != nil {
		return nil, err
	}
	server, port := settings.Address, settings.Port
	if endpoint != nil {
		// Точка входа задана реле: в карточке узла показываем её, иначе адрес
		// узла остался бы от блока settings, которого в аутбаунде уже нет.
		server, port = endpoint.Server, endpoint.Port
	}

	stream, err := BuildStreamFromQuery(xrayHysteriaStreamQuery(ob.StreamSettings, server), server)
	if err != nil {
		return nil, fmt.Errorf("hysteria: %w", err)
	}
	stream.MergeIntoOutbound(out)

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "hysteria2",
		Server:   server,
		Port:     port,
		Outbound: raw,
		Label:    tag,
	}, nil
}

// applyXrayHysteriaMasks переносит в аутбаунд слой масок Xray. Маски
// навешиваются на ВЕСЬ udp-сокет аутбаунда (transport_internet.go: весь
// FinalMask.Udp уходит в config.Udpmasks независимо от транспорта), поэтому
// маска, которой в sing-box нет, — это не «чужая настройка», а другой формат
// на проводе: узел с ней импортировался бы зелёным и молча не работал.
func applyXrayHysteriaMasks(stream *XrayStream, out map[string]any) (*xrayEndpoint, error) {
	if stream == nil || len(stream.FinalMask) == 0 {
		return nil, nil
	}
	var final XrayFinalMask
	if err := json.Unmarshal(stream.FinalMask, &final); err != nil {
		return nil, fmt.Errorf("hysteria: invalid finalmask")
	}
	var endpoint *xrayEndpoint
	seen := map[string]bool{}
	for _, mask := range final.UDP {
		typ := strings.ToLower(mask.Type)
		// Xray применяет маски по очереди, sing-box умеет одну каждого рода:
		// вторая молча затёрла бы первую.
		if seen[typ] {
			return nil, fmt.Errorf("hysteria: udp mask %q is repeated, sing-box takes only one", mask.Type)
		}
		seen[typ] = true
		switch typ {
		case "salamander":
			if err := applyXraySalamander(mask.Settings, out); err != nil {
				return nil, err
			}
		case "udphop":
			if err := applyXrayUDPHop(mask.Settings, out); err != nil {
				return nil, err
			}
		case "realm":
			ep, err := applyXrayRealm(mask.Settings, out)
			if err != nil {
				return nil, err
			}
			endpoint = ep
		default:
			return nil, fmt.Errorf("hysteria: udp mask %q has no sing-box equivalent", mask.Type)
		}
	}
	// Порядок масок в списке произвольный, поэтому несовместимость проверяем
	// после цикла: sing-box отвергает realm рядом с server_ports.
	if endpoint != nil {
		if _, hop := out["server_ports"]; hop {
			return nil, fmt.Errorf("hysteria: udp mask realm cannot be combined with udphop: sing-box takes the address from realm alone")
		}
	}
	if err := applyXrayQuicParams(final.QuicParams, out); err != nil {
		return nil, err
	}
	return endpoint, nil
}

// rejectXrayForeignMasks отвергает узел, если на его транспорт навешаны маски:
// выразить их нечем. Пустые списки и одни только quicParams формат на проводе
// не меняют, поэтому проходят.
func rejectXrayForeignMasks(stream *XrayStream) error {
	if stream == nil || len(stream.FinalMask) == 0 {
		return nil
	}
	var final XrayFinalMask
	if err := json.Unmarshal(stream.FinalMask, &final); err != nil {
		return fmt.Errorf("xray: invalid finalmask")
	}
	if n := len(final.TCP) + len(final.UDP); n > 0 {
		return fmt.Errorf("xray: finalmask has %d mask(s) with no sing-box equivalent", n)
	}
	return nil
}

// xrayEndpoint — точка входа, заданная не полем settings, а маской.
type xrayEndpoint struct {
	Server string
	Port   uint16
}

// applyXrayRealm переносит маску realm. Адрес входа у такого аутбаунда живёт
// внутри realm.server_url, а server/server_port sing-box рядом с ним прямо
// запрещает (protocol/hysteria2/outbound.go), поэтому они снимаются.
func applyXrayRealm(raw json.RawMessage, out map[string]any) (*xrayEndpoint, error) {
	var r XrayRealm
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("hysteria: invalid realm settings")
	}
	// TLS реле у sing-box настраивается внутри realm.http_client, и одного
	// поля под конфигурацию Xray там нет.
	if len(r.TLSConfig) > 0 && string(r.TLSConfig) != "null" {
		return nil, fmt.Errorf("hysteria: realm tlsConfig has no sing-box equivalent")
	}

	u, err := url.Parse(r.URL)
	if err != nil {
		return nil, fmt.Errorf("hysteria: realm url is malformed")
	}
	var scheme, defaultPort string
	switch u.Scheme {
	case "realm":
		scheme, defaultPort = "https", "443"
	case "realm+http":
		scheme, defaultPort = "http", "80"
	default:
		return nil, fmt.Errorf("hysteria: realm url scheme %q is not realm or realm+http", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("hysteria: realm url has no host")
	}
	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	portN, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portN == 0 {
		return nil, fmt.Errorf("hysteria: realm url port %q is not valid", port)
	}
	token, err := url.PathUnescape(u.User.String())
	if err != nil || token == "" {
		return nil, fmt.Errorf("hysteria: realm url has no token")
	}
	id, err := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), "/"))
	if err != nil || id == "" {
		return nil, fmt.Errorf("hysteria: realm url has no id")
	}
	if len(r.StunServers) == 0 {
		return nil, fmt.Errorf("hysteria: realm stunServers is empty")
	}
	for _, srv := range r.StunServers {
		if _, _, err := net.SplitHostPort(srv); err != nil {
			return nil, fmt.Errorf("hysteria: realm stunServers %q is not host:port", srv)
		}
	}

	realm := map[string]any{
		"server_url":   scheme + "://" + net.JoinHostPort(host, port),
		"token":        token,
		"realm_id":     id,
		"stun_servers": r.StunServers,
	}
	// dual — умолчание обеих сторон (Xray: Family_Dual, sing-box: 0).
	switch strings.ToLower(r.IPMode) {
	case "v4":
		realm["ip_version"] = 4
	case "v6":
		realm["ip_version"] = 6
	}
	if pm := r.PortMapping; pm != nil && pm.Enabled {
		mapping := map[string]any{"enabled": true}
		if pm.Timeout > 0 {
			mapping["timeout"] = strconv.FormatInt(pm.Timeout, 10) + "s"
		}
		if pm.Lifetime > 0 {
			mapping["lifetime"] = strconv.FormatInt(pm.Lifetime, 10) + "s"
		}
		realm["port_mapping"] = mapping
	}
	out["realm"] = realm
	delete(out, "server")
	delete(out, "server_port")
	return &xrayEndpoint{Server: host, Port: uint16(portN)}, nil
}

func applyXraySalamander(raw json.RawMessage, out map[string]any) error {
	var s XraySalamander
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf("hysteria: invalid salamander obfs settings")
	}
	if s.Password == "" {
		// Пустой пароль обфускации — заведомо неработающая пара с сервером,
		// тот же отказ даёт разбор Clash.
		return fmt.Errorf("hysteria: obfs requires password")
	}
	obfs := map[string]any{"type": "salamander", "password": s.Password}
	// packetSize у Xray включает вариант gecko, и размеры обязаны совпадать с
	// серверными: молча отбросить их значит собрать нерабочий аутбаунд.
	if from, to, ok := parseXrayInt32Range(s.PacketSize); ok && to > 0 {
		// Границы — Xray (Salamander.Build) и sing-box проверяют их одинаково,
		// но sing-box только на дозвоне: без проверки здесь узел выглядел бы
		// исправным и отказывал на каждом соединении.
		if from <= 0 || to > 2048 {
			return fmt.Errorf("hysteria: invalid gecko packet size range %d-%d (want 1..2048)", from, to)
		}
		obfs["type"] = "gecko"
		obfs["min_packet_size"] = from
		obfs["max_packet_size"] = to
	}
	out["obfs"] = obfs
	return nil
}

func applyXrayUDPHop(raw json.RawMessage, out map[string]any) error {
	var h XrayUDPHop
	if err := json.Unmarshal(raw, &h); err != nil {
		return fmt.Errorf("hysteria: invalid udphop settings")
	}
	// Прыжки по адресам sing-box не умеет: он дозванивается на один адрес.
	if len(h.RemoteIPs) > 0 {
		return fmt.Errorf("hysteria: udphop remoteIPs has no sing-box equivalent")
	}
	// Прыжковый сокет Xray настраивает отдельно; своего диалера под прыжки у
	// sing-box нет.
	if len(h.Sockopt) > 0 && string(h.Sockopt) != "null" {
		return fmt.Errorf("hysteria: udphop sockopt has no sing-box equivalent")
	}
	// mode перечисляет, ЧТО меняется при прыжке (udphop/conn.go):
	// intervalRemote — удалённый порт по таймеру, ровно server_ports sing-box;
	// perConnRemote — порт выбирается ОДИН раз на соединение, таймера нет;
	// intervalLocal — пересоздаётся локальный сокет, адресат не меняется.
	// Последние два в sing-box не выражаются, а отличие видно на проводе.
	remote := false
	for _, mode := range strings.Split(h.Mode, ",") {
		switch strings.ToLower(strings.TrimSpace(mode)) {
		case "intervalremote":
			remote = true
		case "perconnremote":
			return fmt.Errorf("hysteria: udphop mode perConnRemote has no sing-box equivalent")
		case "intervallocal":
			return fmt.Errorf("hysteria: udphop mode intervalLocal has no sing-box equivalent")
		default:
			// Xray на неизвестном режиме тоже отказывает (UDPHop.Build):
			// молча забыть маску значит потерять прыжки без следа.
			return fmt.Errorf("hysteria: udphop mode %q is unknown", h.Mode)
		}
	}
	if !remote {
		return nil
	}
	ports, err := xrayHopPorts(h.RemotePorts)
	if err != nil {
		return err
	}
	if len(ports) == 0 {
		// У Xray это законно: прыжок получается вырожденным (новый адрес
		// равен старому), конфигурация рабочая — прыжков просто нет.
		return nil
	}
	anyPorts := make([]any, len(ports))
	for i, p := range ports {
		anyPorts[i] = p
	}
	out["server_ports"] = anyPorts

	// Xray выбирает задержку случайно в диапазоне [min, max]; sing-box
	// описывает то же парой hop_interval/hop_interval_max. Своего умолчания
	// тут нет и быть не может: конфигурацию без interval Xray не принимает
	// (обе границы обязаны быть >= 5, udphop/conn.go).
	from, to, ok := parseXrayInt32Range(h.Interval)
	if !ok {
		return fmt.Errorf("hysteria: udphop interval is missing")
	}
	if from < 5 {
		return fmt.Errorf("hysteria: udphop interval %ds is below the 5s minimum", from)
	}
	out["hop_interval"] = strconv.Itoa(from) + "s"
	if to > from {
		out["hop_interval_max"] = strconv.Itoa(to) + "s"
	}
	return nil
}

// xrayHopPorts проверяет список портов ДО того, как он уедет в конфиг:
// sing-box разбирает server_ports только на дозвоне, и негодное значение
// роняет не узел, а весь движок. Xray свой PortList проверяет при разборе.
func xrayHopPorts(raw json.RawMessage) ([]string, error) {
	spec := xrayPortListString(raw)
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	ports := parseMport(spec)
	for _, p := range ports {
		lo, hi, _ := strings.Cut(p, ":")
		from, err1 := strconv.Atoi(lo)
		to, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || from < 1 || to > 65535 || from > to {
			return nil, fmt.Errorf("hysteria: udphop remotePorts %q is not a valid port range", spec)
		}
	}
	return ports, nil
}

// applyXrayQuicParams переносит настройки QUIC. Всё, чему в аутбаунде sing-box
// нет соответствия, — отказ с названием поля: у этих ключей есть эффект на
// проводе, и молчаливая потеря дала бы конфигурацию, которая ведёт себя иначе,
// чем её описал сервер.
func applyXrayQuicParams(q *XrayQuicParams, out map[string]any) error {
	if q == nil {
		return nil
	}
	for _, unsupported := range []struct {
		name string
		set  bool
	}{
		{"brutalDisableLossCompensation", q.BrutalDisableLossCompensation},
		{"disableGSO", q.DisableGSO},
		{"disableStatelessReset", q.DisableStatelessReset},
	} {
		if unsupported.set {
			return fmt.Errorf("hysteria: quicParams %s has no sing-box equivalent", unsupported.name)
		}
	}

	// Управление перегрузкой: sing-box включает brutal ровно по ненулевым
	// up_mbps/down_mbps, иначе работает BBR. Отдельного reno у него нет, а
	// Xray под reno оставляет умолчание quic-go — другой алгоритм.
	switch strings.ToLower(q.Congestion) {
	case "", "brutal":
		// Полоса у Xray в байтах в секунду (Bandwidth.Bps), у sing-box — в
		// Mbps по 125000 байт (constant/speed.go): переносим байты, а не
		// подпись, иначе изменилась бы заявленная скорость.
		up, err := xrayBandwidthMbps(q.BrutalUp)
		if err != nil {
			return fmt.Errorf("hysteria: quicParams brutalUp: %w", err)
		}
		down, err := xrayBandwidthMbps(q.BrutalDown)
		if err != nil {
			return fmt.Errorf("hysteria: quicParams brutalDown: %w", err)
		}
		if up > 0 {
			out["up_mbps"] = up
		}
		if down > 0 {
			out["down_mbps"] = down
		}
	case "bbr":
		// brutalUp при bbr не работает и у самого Xray (dialer.go выбирает
		// UseBBR), а в sing-box ненулевой up_mbps ВКЛЮЧИЛ бы brutal — то есть
		// другой алгоритм. А вот brutalDown уезжает на провод независимо от
		// алгоритма: Xray кладёт его в заголовок CCRX запроса авторизации до
		// выбора congestion, и сервер по нему настраивает свою отдачу. В
		// sing-box это down_mbps, и brutal он не включает.
		down, err := xrayBandwidthMbps(q.BrutalDown)
		if err != nil {
			return fmt.Errorf("hysteria: quicParams brutalDown: %w", err)
		}
		if down > 0 {
			out["down_mbps"] = down
		}
	case "force-brutal":
		// force-brutal шлёт заявленную полосу безусловно, обычный brutal
		// берёт минимум с объявленной сервером (hysteria/dialer.go).
		// sing-box всегда берёт минимум — «force» выразить нечем.
		return fmt.Errorf("hysteria: quicParams congestion force-brutal has no sing-box equivalent")
	case "reno":
		return fmt.Errorf("hysteria: quicParams congestion reno has no sing-box equivalent")
	default:
		return fmt.Errorf("hysteria: quicParams congestion %q is unknown", q.Congestion)
	}

	// Профиль BBR — закрытый перечень: чужое значение движок не примет и
	// откажется поднимать конфигурацию целиком, а Xray его пропускает молча.
	if q.BbrProfile != "" {
		switch strings.ToLower(q.BbrProfile) {
		case "standard", "conservative", "aggressive":
			out["bbr_profile"] = strings.ToLower(q.BbrProfile)
		default:
			return fmt.Errorf("hysteria: quicParams bbrProfile %q is unknown", q.BbrProfile)
		}
	}
	// Границы ниже проверяет сам Xray: пропустить внутрь то, что источник бы
	// не принял, значит собрать заведомо неверный аутбаунд.
	if q.MaxIdleTimeout != 0 {
		if q.MaxIdleTimeout < 4 || q.MaxIdleTimeout > 120 {
			return fmt.Errorf("hysteria: quicParams maxIdleTimeout %d is out of the 4..120 range", q.MaxIdleTimeout)
		}
		out["idle_timeout"] = strconv.FormatInt(q.MaxIdleTimeout, 10) + "s"
	}
	if q.KeepAlivePeriod != 0 {
		if q.KeepAlivePeriod < 2 || q.KeepAlivePeriod > 60 {
			return fmt.Errorf("hysteria: quicParams keepAlivePeriod %d is out of the 2..60 range", q.KeepAlivePeriod)
		}
		out["keep_alive_period"] = strconv.FormatInt(q.KeepAlivePeriod, 10) + "s"
	}
	// Окно приёма у sing-box одно на «стартовое» и «предельное» (sing-quic
	// ApplyQUICOptions ставит оба), у Xray их два. Точно выражается только
	// случай, когда они равны.
	stream, err := xrayReceiveWindow("StreamReceiveWindow", q.InitStreamReceiveWindow, q.MaxStreamReceiveWindow)
	if err != nil {
		return err
	}
	if stream > 0 {
		out["stream_receive_window"] = stream
	}
	conn, err := xrayReceiveWindow("ConnectionReceiveWindow", q.InitConnectionReceiveWindow, q.MaxConnectionReceiveWindow)
	if err != nil {
		return err
	}
	if conn > 0 {
		out["connection_receive_window"] = conn
	}
	if q.MaxIncomingStreams != 0 {
		if q.MaxIncomingStreams < 8 {
			return fmt.Errorf("hysteria: quicParams maxIncomingStreams %d is below the minimum of 8", q.MaxIncomingStreams)
		}
		out["max_concurrent_streams"] = q.MaxIncomingStreams
	}
	if q.DisablePathMTUDiscovery {
		out["disable_path_mtu_discovery"] = true
	}
	if q.DisableChromeParrot {
		out["disable_chrome_parrot"] = true
	}
	if q.Debug {
		out["brutal_debug"] = true
	}
	return nil
}

// xrayReceiveWindow сводит пару Xray (стартовое и предельное окно) к
// единственному значению sing-box. Разные значения выразить нечем: sing-box
// одним ключом ставит оба.
func xrayReceiveWindow(name string, init, max uint64) (uint64, error) {
	if init == 0 && max == 0 {
		return 0, nil
	}
	if init != max {
		return 0, fmt.Errorf("hysteria: quicParams init%s and max%s differ, sing-box has a single window", name, name)
	}
	// Нижнюю границу держит Xray.
	if max < 16384 {
		return 0, fmt.Errorf("hysteria: quicParams max%s %d is below the minimum of 16384", name, max)
	}
	return max, nil
}

// xrayBandwidthMbps повторяет Bandwidth.Bps Xray (единицы 1024-кратные, потом
// деление на 8) и переводит байты в секунду в Mbps sing-box (125000 байт).
func xrayBandwidthMbps(raw string) (int, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return 0, nil
	}
	idx := len(s)
	for i, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			idx = i
			break
		}
	}
	val, err := strconv.ParseFloat(s[:idx], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid bandwidth %q", raw)
	}
	var mul float64
	switch strings.TrimSpace(s[idx:]) {
	case "", "b", "bps":
		mul = 1
	case "k", "kb", "kbps":
		mul = 1024
	case "m", "mb", "mbps":
		mul = 1024 * 1024
	case "g", "gb", "gbps":
		mul = 1024 * 1024 * 1024
	case "t", "tb", "tbps":
		mul = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unsupported bandwidth unit in %q", raw)
	}
	bytesPerSec := val * mul / 8
	if bytesPerSec <= 0 {
		return 0, nil
	}
	// Нижняя граница — Xray: меньше 65536 байт в секунду он не принимает, и
	// такое значение у него означает не «медленно», а «конфиг неверен».
	if bytesPerSec < 65536 {
		return 0, fmt.Errorf("%q is below the minimum of 65536 bytes per second", raw)
	}
	// Вниз, а не к ближайшему: brutal шлёт заявленный темп без обратной связи,
	// и завышение — это лишний поток в канал. Ниже 1 Mbps не опускаемся —
	// нулём выключился бы сам brutal.
	mbps := int(math.Floor(bytesPerSec / 125000))
	if mbps < 1 {
		mbps = 1
	}
	return mbps, nil
}

// parseXrayInt32Range разбирает Int32Range Xray: строка "10-30" или число.
func parseXrayInt32Range(raw json.RawMessage) (from, to int, ok bool) {
	if len(raw) == 0 {
		return 0, 0, false
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, n, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, 0, false
	}
	lo, hi, isRange := strings.Cut(s, "-")
	from, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil {
		return 0, 0, false
	}
	to = from
	if isRange {
		// Оборванный диапазон Xray отвергает (ParseRangeString): прочитать
		// "10-" как одиночное значение значило бы потерять верхнюю границу.
		v, err := strconv.Atoi(strings.TrimSpace(hi))
		if err != nil {
			return 0, 0, false
		}
		to = v
	}
	if from > to {
		from, to = to, from
	}
	return from, to, true
}

// xrayPortListString приводит PortList Xray к строке "a-b,c": он приходит и
// строкой, и числом. Грамматика совпадает с mport ссылки hy2://, поэтому
// дальше работает parseMport.
func xrayPortListString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return strconv.Itoa(n)
	}
	return ""
}

// xrayHysteriaStreamQuery отбирает из streamSettings только то, что у hysteria2
// есть. Список закрытый — тот же, что у разбора hy2://: транспорт не участвует
// (network "hysteria" транспортом sing-box не является), fp тоже — uTLS поверх
// QUIC неприменим, а общий слой добавил бы блок utls.
func xrayHysteriaStreamQuery(stream *XrayStream, defaultHost string) url.Values {
	v := url.Values{}
	v.Set("security", "tls")
	// h3 движок подставил бы и сам при пустом наборе (sing-quic hysteria2:
	// SetNextProtos на пустых NextProtos), но ставим явно — так значение видно
	// в карточке и в экспорте ссылки, а не только внутри процесса.
	v.Set("alpn", "h3")
	if stream == nil {
		return v
	}
	if ts := stream.TLSSettings; ts != nil {
		// Пустое имя общий слой подставит сам из адреса (BuildStreamFromQuery).
		v.Set("sni", ts.ServerName)
		if ts.AllowInsecure {
			v.Set("insecure", "1")
		}
		if len(ts.ALPN) > 0 {
			v.Set("alpn", strings.Join(ts.ALPN, ","))
		}
	}
	if iface, _ := stream.Sockopt["interface"].(string); iface != "" {
		v.Set("bind_interface", iface)
	}
	return v
}

// xrayStreamToValues переводит streamSettings Xray-конфига в тот же набор
// query-параметров, что несёт share-ссылка, чтобы дальше работал общий слой
// (BuildStreamFromQuery + MergeIntoOutbound). Без этого Xray-путь пришлось бы
// держать второй реализацией транспорта и TLS, а она уже разъехалась с общей:
// теряла xhttp и httpupgrade целиком, early data и bind_interface.
func xrayStreamToValues(stream *XrayStream, defaultHost string) url.Values {
	v := url.Values{}
	if stream == nil {
		return v
	}

	network := strings.ToLower(stream.Network)
	switch network {
	case "splithttp":
		network = "xhttp"
	case "raw":
		network = "tcp"
	}
	if network != "" {
		v.Set("type", network)
	}

	switch network {
	case "tcp":
		// Тип заголовка уезжает как есть: что с ним делать — знает
		// BuildStreamFromQuery (она же отвергает несуществующие на tcp).
		// Фильтровать здесь значило бы вернуть Xray-входу своё решение о
		// транспорте и своё молчание на непонятом значении.
		tcp := stream.TCPSettings
		if stream.RAWSettings != nil {
			tcp = stream.RAWSettings
		}
		if t := tcp; t != nil && t.Header != nil && t.Header.Type != "" {
			v.Set("headerType", t.Header.Type)
			if req := t.Header.Request; req != nil {
				if req.Method != "" {
					v.Set("method", req.Method)
				}
				if len(req.Path) > 0 {
					v.Set("path", req.Path[0])
				}
				// Значение заголовка у Xray — список, но встречается и строка;
				// asStringSlice принимает обе формы.
				hosts := asStringSlice(req.Headers["Host"])
				if len(hosts) == 0 {
					hosts = asStringSlice(req.Headers["host"])
				}
				if len(hosts) > 0 {
					v.Set("host", hosts[0])
				}
			}
		}
	case "ws":
		if ws := stream.WSSettings; ws != nil {
			v.Set("path", ws.Path)
			v.Set("host", firstNonEmpty(ws.Host, ws.Headers["Host"], ws.Headers["host"]))
		}
	case "httpupgrade":
		if hu := stream.HTTPUpgradeSettings; hu != nil {
			v.Set("path", hu.Path)
			v.Set("host", firstNonEmpty(hu.Host, hu.Headers["Host"], hu.Headers["host"]))
		}
	case "grpc":
		if g := stream.GRPCSettings; g != nil {
			v.Set("serviceName", g.ServiceName)
		}
	case "http", "h2":
		if h := stream.HTTPSettings; h != nil {
			if h.Method != "" {
				v.Set("method", h.Method)
			}
			v.Set("path", h.Path)
			if len(h.Host) > 0 {
				v.Set("host", h.Host[0])
			}
		}
	case "xhttp":
		raw := stream.XHTTPSettings
		if len(raw) == 0 {
			raw = stream.SplitHTTPSettings
		}
		setXHTTPValues(v, raw)
	}

	switch strings.ToLower(stream.Security) {
	case "reality":
		if rs := stream.RealitySettings; rs != nil {
			v.Set("security", "reality")
			v.Set("sni", rs.ServerName)
			v.Set("pbk", rs.PublicKey)
			v.Set("sid", rs.ShortID)
			v.Set("fp", rs.Fingerprint)
		}
	case "tls", "true":
		v.Set("security", "tls")
		if ts := stream.TLSSettings; ts != nil {
			v.Set("sni", firstNonEmpty(ts.ServerName, defaultHost))
			v.Set("fp", ts.Fingerprint)
			if ts.AllowInsecure {
				v.Set("insecure", "1")
			}
			if len(ts.ALPN) > 0 {
				v.Set("alpn", strings.Join(ts.ALPN, ","))
			}
		}
	}

	if iface, _ := stream.Sockopt["interface"].(string); iface != "" {
		v.Set("bind_interface", iface)
	}
	return v
}

// setXHTTPValues раскладывает объект xhttpSettings: path/host/mode идут
// отдельными параметрами, остальное — тем же путём, что "extra" у ссылки.
// Вложенный "extra" перекрывает плоские поля, как и в самом Xray.
func setXHTTPValues(v url.Values, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var settings map[string]any
	if json.Unmarshal(raw, &settings) != nil {
		return
	}
	if extra, ok := settings["extra"].(map[string]any); ok {
		for k, val := range extra {
			settings[k] = val
		}
	}
	delete(settings, "extra")

	for _, key := range []string{"path", "host", "mode"} {
		if s, ok := settings[key].(string); ok && s != "" {
			v.Set(key, s)
		}
		delete(settings, key)
	}
	if encoded, err := json.Marshal(settings); err == nil {
		v.Set("extra", string(encoded))
	}
}
