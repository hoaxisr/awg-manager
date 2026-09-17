package vlink

import (
	"encoding/json"
	"fmt"
	"math"
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

// XrayHysteriaStream — streamSettings.hysteriaSettings: версия, пароль и
// таймаут простоя UDP-сессии. Блок masquerade оттуда сюда не переносится: он
// серверный (Xray собирает из него ответ маскировки на inbound) и на поведение
// клиента не влияет.
type XrayHysteriaStream struct {
	Version        int    `json:"version"`
	Auth           string `json:"auth"`
	UDPIdleTimeout int    `json:"udpIdleTimeout"`
}

// XrayFinalMask — streamSettings.finalmask: udp-маски (salamander, udphop и
// прочие) и настройки QUIC.
type XrayFinalMask struct {
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

// XrayUDPHop — маска udphop. mode перечисляет, ЧТО меняется при прыжке;
// удалённый порт берётся только при intervalRemote/perConnRemote
// (udphop/conn.go). interval — Int32Range в секундах, remotePorts — PortList.
type XrayUDPHop struct {
	Mode        string          `json:"mode"`
	Interval    json.RawMessage `json:"interval"`
	RemotePorts json.RawMessage `json:"remotePorts"`
	RemoteIPs   []string        `json:"remoteIPs"`
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
	Remarks   string         `json:"remarks"`
	Outbounds []XrayOutbound `json:"outbounds"`
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
			var outbounds []XrayOutbound
			if err := json.Unmarshal(rawOutbounds, &outbounds); err == nil && len(outbounds) > 0 {
				for _, ob := range outbounds {
					proto := strings.ToLower(ob.Protocol)
					if isXrayProtocol(proto) {
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
				proto := strings.ToLower(ob.Protocol)
				if isXrayProtocol(proto) {
					return true
				}
			}
		}
	}

	// 3. Bare array of Xray outbounds
	var arr []XrayOutbound
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		for _, ob := range arr {
			proto := strings.ToLower(ob.Protocol)
			if isXrayProtocol(proto) && len(ob.Settings) > 0 {
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
				var realOutbounds []XrayOutbound
				for _, ob := range item.Outbounds {
					proto := strings.ToLower(ob.Protocol)
					if proto != "" && proto != "freedom" && proto != "blackhole" && proto != "dns" && proto != "loopback" {
						realOutbounds = append(realOutbounds, ob)
					}
				}

				for idx, ob := range realOutbounds {
					proto := strings.ToLower(ob.Protocol)
					thisIdx := nodeIdx
					nodeIdx++
					if proto == "vmess" {
						res.SkippedVmess++
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
	var outbounds []XrayOutbound
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

	for idx, ob := range outbounds {
		proto := strings.ToLower(ob.Protocol)
		if proto == "" || proto == "freedom" || proto == "blackhole" || proto == "dns" || proto == "loopback" {
			continue
		}
		if proto == "vmess" {
			res.SkippedVmess++
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
	// Своего таймаута UDP-сессии у аутбаунда sing-box нет. Умолчание Xray
	// (60 с) терять нечего, а заданное вручную значение — потеря поведения.
	if hy != nil && hy.UDPIdleTimeout != 0 && hy.UDPIdleTimeout != 60 {
		return nil, fmt.Errorf("hysteria: udpIdleTimeout has no sing-box equivalent")
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

	if err := applyXrayHysteriaMasks(ob.StreamSettings, out); err != nil {
		return nil, err
	}

	stream, err := BuildStreamFromQuery(xrayHysteriaStreamQuery(ob.StreamSettings, settings.Address), settings.Address)
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
		Server:   settings.Address,
		Port:     settings.Port,
		Outbound: raw,
		Label:    tag,
	}, nil
}

// applyXrayHysteriaMasks переносит в аутбаунд слой масок Xray. Маски
// навешиваются на ВЕСЬ udp-сокет аутбаунда (transport_internet.go: весь
// FinalMask.Udp уходит в config.Udpmasks независимо от транспорта), поэтому
// маска, которой в sing-box нет, — это не «чужая настройка», а другой формат
// на проводе: узел с ней импортировался бы зелёным и молча не работал.
func applyXrayHysteriaMasks(stream *XrayStream, out map[string]any) error {
	if stream == nil || len(stream.FinalMask) == 0 {
		return nil
	}
	var final XrayFinalMask
	if err := json.Unmarshal(stream.FinalMask, &final); err != nil {
		return fmt.Errorf("hysteria: invalid finalmask")
	}
	for _, mask := range final.UDP {
		switch strings.ToLower(mask.Type) {
		case "salamander":
			if err := applyXraySalamander(mask.Settings, out); err != nil {
				return err
			}
		case "udphop":
			if err := applyXrayUDPHop(mask.Settings, out); err != nil {
				return err
			}
		default:
			return fmt.Errorf("hysteria: udp mask %q has no sing-box equivalent", mask.Type)
		}
	}
	return applyXrayQuicParams(final.QuicParams, out)
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
		}
	}
	if !remote {
		return nil
	}
	ports := parseMport(xrayPortListString(h.RemotePorts))
	if len(ports) == 0 {
		return nil
	}
	anyPorts := make([]any, len(ports))
	for i, p := range ports {
		anyPorts[i] = p
	}
	out["server_ports"] = anyPorts

	// Xray выбирает задержку случайно в диапазоне [min, max]; sing-box
	// описывает то же парой hop_interval/hop_interval_max.
	from, to, ok := parseXrayInt32Range(h.Interval)
	if !ok || from <= 0 {
		from, to = 10, 0
	}
	// Нижнюю границу держат обе стороны, но sing-box — на дозвоне
	// (sing-quic: hop interval must be at least 5 seconds).
	if from < 5 {
		return fmt.Errorf("hysteria: udphop interval %ds is below the 5s minimum", from)
	}
	out["hop_interval"] = strconv.Itoa(from) + "s"
	if to > from {
		out["hop_interval_max"] = strconv.Itoa(to) + "s"
	}
	return nil
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
		{"initStreamReceiveWindow", q.InitStreamReceiveWindow > 0},
		{"initConnectionReceiveWindow", q.InitConnectionReceiveWindow > 0},
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
	case "", "brutal", "force-brutal":
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
		// Значения brutal при bbr не работают и у самого Xray (dialer.go):
		// перенести их значило бы включить в sing-box другой алгоритм.
	case "reno":
		return fmt.Errorf("hysteria: quicParams congestion reno has no sing-box equivalent")
	default:
		return fmt.Errorf("hysteria: quicParams congestion %q is unknown", q.Congestion)
	}

	if q.BbrProfile != "" {
		out["bbr_profile"] = strings.ToLower(q.BbrProfile)
	}
	if q.MaxIdleTimeout > 0 {
		out["idle_timeout"] = strconv.FormatInt(q.MaxIdleTimeout, 10) + "s"
	}
	if q.KeepAlivePeriod > 0 {
		out["keep_alive_period"] = strconv.FormatInt(q.KeepAlivePeriod, 10) + "s"
	}
	if q.MaxStreamReceiveWindow > 0 {
		out["stream_receive_window"] = q.MaxStreamReceiveWindow
	}
	if q.MaxConnectionReceiveWindow > 0 {
		out["connection_receive_window"] = q.MaxConnectionReceiveWindow
	}
	if q.MaxIncomingStreams > 0 {
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
	mbps := int(math.Round(bytesPerSec / 125000))
	if mbps == 0 {
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
		if v, err := strconv.Atoi(strings.TrimSpace(hi)); err == nil {
			to = v
		}
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
	v.Set("sni", defaultHost)
	// hysteria2 без ALPN h3 сервер обычно не принимает; движок дефолт не ставит.
	v.Set("alpn", "h3")
	if stream == nil {
		return v
	}
	if ts := stream.TLSSettings; ts != nil {
		v.Set("sni", firstNonEmpty(ts.ServerName, defaultHost))
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
