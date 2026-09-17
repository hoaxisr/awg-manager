package vlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

func parseHysteria2(input string) (*ParsedOutbound, error) {
	u, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: parse: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("hysteria2: missing host")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("hysteria2: missing or invalid port")
	}

	password := u.User.Username()
	if pw, ok := u.User.Password(); ok && pw != "" {
		password = password + ":" + pw
	}
	if password == "" {
		return nil, errors.New("hysteria2: missing password")
	}

	q := u.Query()
	out := map[string]any{
		"type":        "hysteria2",
		"server":      host,
		"server_port": port,
		"password":    password,
	}

	// Port hopping
	if mport := q.Get("mport"); mport != "" {
		ports := parseMport(mport)
		if len(ports) > 0 {
			anyPorts := make([]any, len(ports))
			for i, p := range ports {
				anyPorts[i] = p
			}
			out["server_ports"] = anyPorts
		}
	}
	// Само значение hop_interval разбирается ниже, вместе с прочими
	// длительностями; здесь — только умолчание под mport.
	if _, hasMport := out["server_ports"]; hasMport && q.Get("hop_interval") == "" {
		out["hop_interval"] = "10s"
	}

	// TLS собирает общий слой — тот же, что у clash-пути. Здесь остаётся
	// только hysteria-специфика, которой общий слой не знает.
	stream, err := BuildStreamFromQuery(hysteria2StreamQuery(q), host)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: %w", err)
	}
	stream.MergeIntoOutbound(out)
	// pinSHA256 намеренно игнорируется: hysteria пинит hex-отпечаток всего
	// сертификата, sing-box certificate_public_key_sha256 — base64 sha256
	// от SPKI публичного ключа. Эквивалента нет, а сырое значение валит
	// decode всего конфига (issue #350).
	if boolish(q.Get("ech")) {
		if tls, ok := out["tls"].(map[string]any); ok {
			tls["ech"] = map[string]any{"enabled": true}
		}
	}

	// Obfs
	if obfsType := q.Get("obfs"); obfsType != "" {
		obfs := map[string]any{
			"type":     obfsType,
			"password": q.Get("obfs-password"),
		}
		// Размеры пакетов есть только у варианта gecko, и совпадать с
		// серверными они обязаны — без них обфускация не сходится.
		if strings.EqualFold(obfsType, "gecko") {
			if n, err := strconv.Atoi(q.Get("obfs-min-packet-size")); err == nil && n > 0 {
				obfs["min_packet_size"] = n
			}
			if n, err := strconv.Atoi(q.Get("obfs-max-packet-size")); err == nil && n > 0 {
				obfs["max_packet_size"] = n
			}
		}
		out["obfs"] = obfs
	}

	// Brutal congestion. Пропускная способность у аутбаунда hysteria2
	// выражается ПЛОСКИМИ up_mbps/down_mbps (sing-box option/hysteria2.go);
	// объекта "brutal" схема движка не знает, а лишний ключ роняет разбор
	// ВСЕЙ конфигурации, а не одного аутбаунда (F360).
	// congestion здесь описательный: brutal движок включает по ненулевому
	// up_mbps (sing-quic: actualTx > 0 → BrutalSender), а down_mbps — это
	// объявленная серверу скорость приёма, она работает при любом алгоритме.
	// Поэтому значения читаются независимо от congestion.
	if up, err := strconv.Atoi(q.Get("brutal_up")); err == nil && up > 0 {
		out["up_mbps"] = up
	}
	if down, err := strconv.Atoi(q.Get("brutal_down")); err == nil && down > 0 {
		out["down_mbps"] = down
	}

	// Настройки QUIC: имена параметров совпадают с ключами аутбаунда — так
	// ссылка читается обратно без потерь (F363).
	for _, key := range []string{"hop_interval", "hop_interval_max", "idle_timeout", "keep_alive_period"} {
		v := q.Get(key)
		if v == "" {
			continue
		}
		// Движок разбирает эти поля как Duration, и негодное значение роняет
		// разбор ВСЕЙ конфигурации, а не одного аутбаунда: пускать его внутрь
		// нельзя (тот же класс, что ключ brutal, F360).
		if !isDurationSpec(v) {
			return nil, fmt.Errorf("hysteria2: %s %q is not a duration like \"30s\"", key, v)
		}
		out[key] = v
	}
	if v := q.Get("bbr_profile"); v != "" {
		// Закрытый перечень движка: чужое значение валит создание аутбаунда.
		switch strings.ToLower(v) {
		case "standard", "conservative", "aggressive":
			out["bbr_profile"] = strings.ToLower(v)
		default:
			return nil, fmt.Errorf("hysteria2: bbr_profile %q is unknown", v)
		}
	}
	for _, key := range []string{"stream_receive_window", "connection_receive_window", "max_concurrent_streams"} {
		if n, err := strconv.Atoi(q.Get(key)); err == nil && n > 0 {
			out[key] = n
		}
	}
	for _, key := range []string{"disable_path_mtu_discovery", "disable_chrome_parrot", "brutal_debug"} {
		if boolish(q.Get(key)) {
			out[key] = true
		}
	}

	tag := u.Fragment
	if tag == "" {
		tag = fmt.Sprintf("hy2-%s-%d", host, port)
	}
	out["tag"] = tag

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "hysteria2",
		Server:   host,
		Port:     uint16(port),
		Outbound: raw,
		Label:    u.Fragment,
	}, nil
}

// durationSpec — грамматика длительности движка: пары «число + единица», как
// в стандартной библиотеке, плюс сутки, которые sing добавляет в свою копию
// парсера (my_time). Ровно это выражение объявляет и схема sing-box, поэтому
// составные вроде "1d12h" законны.
var durationSpec = regexp.MustCompile(`^[-+]?(((\d+(\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h|d))+|0)$`)

// isDurationSpec отсеивает значения, на которых движок отвергнет ВСЮ
// конфигурацию: голое число, мусор, оборванную пару.
func isDurationSpec(v string) bool {
	return durationSpec.MatchString(v)
}

// parseMport accepts "20000-30000" or "20000,21000-22000,30000-31000"
// and returns sing-box port spec strings ("20000:30000" for ranges,
// single port as "20000:20000" — same convention as the spec example).
// hysteria2StreamQuery отбирает из ссылки то, что понимает общий слой. Список
// закрытый намеренно: fp= сюда не попадает — uTLS поверх QUIC неприменим, а
// общий слой добавил бы блок utls.
func hysteria2StreamQuery(q url.Values) url.Values {
	v := url.Values{}
	v.Set("security", "tls")
	v.Set("sni", q.Get("sni"))
	// hysteria2 без ALPN h3 сервер обычно не принимает; движок дефолт не ставит.
	v.Set("alpn", firstNonEmpty(q.Get("alpn"), "h3"))
	if boolish(q.Get("insecure")) {
		v.Set("insecure", "1")
	}
	if b := q.Get("bind_interface"); b != "" {
		v.Set("bind_interface", b)
	}
	return v
}

func parseMport(mport string) []string {
	parts := strings.Split(mport, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, "-") {
			rng := strings.SplitN(p, "-", 2)
			out = append(out, rng[0]+":"+rng[1])
		} else {
			out = append(out, p+":"+p)
		}
	}
	return out
}
