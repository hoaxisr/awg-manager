// Package wdttlink — продуктовая логика ссылок WDTT поверх нового рантайма:
// разбор wdtt:// / qwdtt:// / подписок, сборка ссылки абоненту, связанный
// WireGuard-туннель.
//
// Пакет — КОПИЯ работающей логики из умирающего internal/wdtt (link.go,
// wgconf.go, используемые части names.go и ports.go): оригиналы живут до
// сноса старых пакетов, потому что их зовут изнутри самого internal/wdtt и из
// старого HTTP-слоя. Расхождения между копией и оригиналом до сноса не
// исправляются здесь молча — правится копия, оригинал умирает целиком.
package wdttlink

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// lookupIP — seam для резолвера (подменяется в тестах).
var lookupIP = net.LookupIP

const (
	SchemeWdtt  = "wdtt://"
	SchemeQwdtt = "qwdtt://"
)

// DecodeImport parses wdtt://, qwdtt://, HTTPS subscription URL, .qwdtt JSON/URI,
// or base64-encoded profile JSON.
func DecodeImport(raw string) (ImportPayload, error) {
	res, err := DecodeLink(raw)
	if err != nil {
		return ImportPayload{}, err
	}
	if res.Profile == nil {
		return ImportPayload{}, fmt.Errorf("пустой профиль")
	}
	return *res.Profile, nil
}

// DecodeLink parses any supported import form and returns a profile plus optional
// multi-profile subscription preview.
func DecodeLink(raw string) (LinkDecodeResult, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "\ufeff")
	if raw == "" {
		return LinkDecodeResult{}, fmt.Errorf("пустая ссылка")
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if isVKCallJoinURL(raw) {
			return LinkDecodeResult{}, fmt.Errorf("это ссылка VK Calls — укажите полный профиль (.qwdtt / JSON) или wdtt:// / qwdtt://")
		}
		return fetchSubscriptionLink(raw)
	}
	if strings.HasPrefix(raw, SchemeQwdtt) {
		p, err := parseQwdttURI(raw)
		if err != nil {
			return LinkDecodeResult{}, err
		}
		return LinkDecodeResult{Profile: &p}, nil
	}
	if strings.HasPrefix(raw, SchemeWdtt) {
		p, err := parseWdttURI(raw)
		if err != nil {
			return LinkDecodeResult{}, err
		}
		return LinkDecodeResult{Profile: &p}, nil
	}
	if doc, err := parseSubscriptionDocument([]byte(raw), ""); err == nil && len(doc.Profiles) > 0 {
		return linkDecodeFromSubscription(doc), nil
	}
	if p, err := parseConfigJSON([]byte(raw)); err == nil {
		pp := p
		return LinkDecodeResult{Profile: &pp}, nil
	}
	if dec, err := base64.StdEncoding.DecodeString(raw); err == nil {
		if doc, err := parseSubscriptionDocument(dec, ""); err == nil && len(doc.Profiles) > 0 {
			return linkDecodeFromSubscription(doc), nil
		}
		if p, err := parseConfigJSON(dec); err == nil {
			pp := p
			return LinkDecodeResult{Profile: &pp}, nil
		}
	}
	if dec, err := base64.RawStdEncoding.DecodeString(raw); err == nil {
		if doc, err := parseSubscriptionDocument(dec, ""); err == nil && len(doc.Profiles) > 0 {
			return linkDecodeFromSubscription(doc), nil
		}
		if p, err := parseConfigJSON(dec); err == nil {
			pp := p
			return LinkDecodeResult{Profile: &pp}, nil
		}
	}
	return LinkDecodeResult{}, fmt.Errorf("неизвестный формат: ожидается wdtt://, qwdtt://, .qwdtt (JSON), HTTPS-подписка или JSON")
}

func isVKCallJoinURL(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	return strings.Contains(lower, "vk.com/call/join/") || strings.Contains(lower, "vk.ru/call/join/")
}

// EncodeLink builds a wdtt:// share link in the colon format:
// host:dtls:wg:clientListenPort:password:hashes
// clientListenPort — локальный UDP-порт wdtt-client/qWDTT (127.0.0.1:9000).
// Раньше здесь был 0; qWDTT трактует это как listen :1 → permission denied.
func EncodeLink(peer string, wgPort int, password string, vkHashes []string, name string) (string, error) {
	return EncodeLinkWithClientPort(peer, wgPort, password, vkHashes, name, 0)
}

func EncodeLinkWithClientPort(peer string, wgPort int, password string, vkHashes []string, name string, clientListenPort int) (string, error) {
	peer = normalizePeer(strings.TrimSpace(peer))
	password = strings.TrimSpace(password)
	if peer == "" {
		return "", fmt.Errorf("peer не задан")
	}
	if password == "" {
		return "", fmt.Errorf("password не задан")
	}
	if wgPort <= 0 {
		wgPort = defaultServerWgPort
	}
	if clientListenPort <= 0 {
		clientListenPort = defaultClientListenPort
	}
	host, dtlsPort, err := splitPeerHostPort(peer)
	if err != nil {
		return "", err
	}
	payload := fmt.Sprintf("%s:%d:%d:%d:%s", host, dtlsPort, wgPort, clientListenPort, password)
	if hashes := strings.Join(splitHashes(strings.Join(vkHashes, ",")), ","); hashes != "" {
		payload += ":" + hashes
	}
	link := SchemeWdtt + payload
	if n := strings.TrimSpace(name); n != "" {
		link += "#" + n
	}
	return link, nil
}

// EncodeQwdttLink builds qwdtt:// for qWDTT/Android — явный port=9000 в query.
func EncodeQwdttLink(peer, password string, vkHashes []string, name string, clientListenPort, workers int, connMode string) (string, error) {
	peer = normalizePeer(strings.TrimSpace(peer))
	password = strings.TrimSpace(password)
	if peer == "" {
		return "", fmt.Errorf("peer не задан")
	}
	if password == "" {
		return "", fmt.Errorf("password не задан")
	}
	if clientListenPort <= 0 {
		clientListenPort = defaultClientListenPort
	}
	if workers <= 0 {
		workers = 18
	}
	q := url.Values{}
	q.Set("peer", peer)
	q.Set("pass", password)
	if len(vkHashes) > 0 {
		q.Set("hashes", strings.Join(splitHashes(strings.Join(vkHashes, ",")), ","))
	}
	q.Set("port", strconv.Itoa(clientListenPort))
	q.Set("workers", strconv.Itoa(workers))
	if mode := normalizeConnMode(connMode); mode == ConnModeRaw {
		q.Set("mode", mode)
	}
	if n := strings.TrimSpace(name); n != "" {
		q.Set("name", n)
	}
	return SchemeQwdtt + "config?" + q.Encode(), nil
}

func splitPeerHostPort(peer string) (host string, port int, err error) {
	peer = strings.TrimSpace(peer)
	if !strings.Contains(peer, ":") {
		return peer, 56000, nil
	}
	h, portStr, splitErr := net.SplitHostPort(peer)
	if splitErr != nil {
		// fallback for bare host:port without brackets
		parts := strings.Split(peer, ":")
		if len(parts) < 2 {
			return "", 0, fmt.Errorf("некорректный peer %q", peer)
		}
		h = strings.Join(parts[:len(parts)-1], ":")
		portStr = parts[len(parts)-1]
	}
	p, convErr := strconv.Atoi(portStr)
	if convErr != nil || p <= 0 {
		return "", 0, fmt.Errorf("некорректный порт в peer %q", peer)
	}
	return h, p, nil
}

func parseWdttURI(link string) (ImportPayload, error) {
	link = strings.TrimSpace(link)
	if !strings.HasPrefix(link, SchemeWdtt) {
		return ImportPayload{}, fmt.Errorf("ожидается wdtt://")
	}
	payload := strings.TrimPrefix(link, SchemeWdtt)
	name := "Server"
	if hash := strings.Index(payload, "#"); hash >= 0 {
		if n := strings.TrimSpace(payload[hash+1:]); n != "" {
			name = n
		}
		payload = payload[:hash]
	}
	if looksColonWdtt(payload) {
		return colonWdttToPayload(payload, name)
	}
	if p, err := jsonWdttToPayload(payload); err == nil {
		p.Name = pickName(p.Name, name)
		return p, nil
	}
	return colonWdttToPayload(payload, name)
}

func looksColonWdtt(payload string) bool {
	parts := strings.Split(payload, ":")
	return len(parts) >= 5 && !strings.Contains(payload, "=") && isDigits(parts[1])
}

func colonWdttToPayload(payload, name string) (ImportPayload, error) {
	parts := strings.Split(payload, ":")
	if len(parts) < 5 {
		return ImportPayload{}, fmt.Errorf("некорректная wdtt:// ссылка (нужно минимум 5 полей через двоеточие)")
	}
	var hashes []string
	if len(parts) > 5 && parts[5] != "" {
		hashes = splitHashes(parts[5])
	}
	listen := ""
	if len(parts) > 3 && isDigits(parts[3]) {
		if p, err := strconv.Atoi(parts[3]); err == nil && p > 0 {
			listen = fmt.Sprintf("127.0.0.1:%d", p)
		}
	}
	return ImportPayload{
		Name:     name,
		Peer:     parts[0] + ":" + parts[1],
		Password: parts[4],
		VKHashes: hashes,
		Workers:  16,
		Listen:   listen,
	}, nil
}

func jsonWdttToPayload(payload string) (ImportPayload, error) {
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return ImportPayload{}, err
		}
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ImportPayload{}, err
	}
	return mapJSONProfile(raw)
}

func parseQwdttURI(link string) (ImportPayload, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return ImportPayload{}, fmt.Errorf("qwdtt://: %w", err)
	}
	q := u.Query()
	peer := strings.TrimSpace(q.Get("peer"))
	pass := firstQuery(q, "pass", "password")
	hashes := splitHashes(firstQuery(q, "hashes", "vk", "vkHashes", "hash"))
	name := firstQuery(q, "name")
	if peer == "" || pass == "" {
		return ImportPayload{}, fmt.Errorf("qwdtt://: нужны peer и pass/password")
	}
	peer = normalizePeer(peer)
	workers := 18
	if w := strings.TrimSpace(q.Get("workers")); w != "" {
		if n, err := strconv.Atoi(w); err == nil && n > 0 {
			workers = n
		}
	} else if w := strings.TrimSpace(q.Get("workersPerHash")); w != "" {
		if n, err := strconv.Atoi(w); err == nil && n > 0 {
			workers = n
		}
	}
	listen := ""
	if port := strings.TrimSpace(firstQuery(q, "port", "listenPort")); port != "" {
		if _, err := strconv.Atoi(port); err == nil {
			listen = "127.0.0.1:" + port
		}
	}
	return ImportPayload{
		Name:     name,
		Peer:     peer,
		Password: pass,
		VKHashes: hashes,
		Workers:  workers,
		Listen:   listen,
		DeviceID: firstQuery(q, "deviceId", "device-id", "did"),
		SubURL:   normalizeSubURL(firstQuery(q, "sub", "subUrl", "sub_url")),
		ConnMode: firstQuery(q, "mode", "connMode", "relayMode"),
	}, nil
}

func parseConfigJSON(data []byte) (ImportPayload, error) {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return ImportPayload{}, fmt.Errorf("пустой JSON")
	}
	if strings.HasPrefix(string(data), SchemeWdtt) || strings.HasPrefix(string(data), SchemeQwdtt) {
		return DecodeImport(string(data))
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ImportPayload{}, err
	}
	if profiles, ok := raw["profiles"].([]interface{}); ok && len(profiles) > 0 {
		if doc, err := parseSubscriptionDocument(data, ""); err == nil {
			return doc.Profiles[0], nil
		}
	}
	return mapJSONProfile(raw)
}

func mapJSONProfile(raw map[string]interface{}) (ImportPayload, error) {
	peer := strings.TrimSpace(firstStr(raw, "peer", "server", "host"))
	pass := strings.TrimSpace(firstStr(raw, "pass", "password", "pwd"))
	name := strings.TrimSpace(firstStr(raw, "name", "ps", "remark", "title"))
	hashes := splitHashes(firstStr(raw, "hashes", "vkHashes", "hash", "vk"))
	if peer == "" {
		ip := strings.TrimSpace(firstStr(raw, "ip", "add"))
		dtls := intFrom(raw, "dtls", "dtls_port", "server_port")
		if ip != "" && dtls > 0 {
			peer = ip + ":" + strconv.Itoa(dtls)
		}
	}
	if peer == "" || pass == "" {
		return ImportPayload{}, fmt.Errorf("неполный профиль: нужны peer и password")
	}
	workers := intFrom(raw, "workers", "workersPerHash", "n")
	if workers <= 0 {
		workers = 18
	}
	listen := ""
	if port := intFrom(raw, "port", "listenPort"); port > 0 {
		listen = fmt.Sprintf("127.0.0.1:%d", port)
	}
	return ImportPayload{
		Name:     name,
		Peer:     normalizePeer(peer),
		Password: pass,
		VKHashes: hashes,
		Workers:  workers,
		Listen:   listen,
		DeviceID: firstStr(raw, "deviceId", "device_id", "did"),
		SubURL:   normalizeSubURL(firstStr(raw, "sub", "subUrl", "sub_url")),
		WG:       firstStr(raw, "wg", "conf", "config"),
		ConnMode: firstStr(raw, "mode", "connMode", "relayMode"),
	}, nil
}

func fetchSubscriptionLink(rawURL string) (LinkDecodeResult, error) {
	rawURL = strings.TrimSpace(rawURL)
	if err := validateSubURL(rawURL); err != nil {
		return LinkDecodeResult{}, err
	}
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("слишком много редиректов при загрузке подписки")
			}
			return validateSubURL(req.URL.String())
		},
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return LinkDecodeResult{}, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "WDTT/1.0 awg-manager")
	resp, err := client.Do(req)
	if err != nil {
		return LinkDecodeResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return LinkDecodeResult{}, fmt.Errorf("подписка HTTP %d", resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return LinkDecodeResult{}, err
	}
	subURL := rawURL // keep query — subscription tokens are needed for RefreshSubscription
	if doc, err := parseSubscriptionDocument(bodyBytes, subURL); err == nil {
		return linkDecodeFromSubscription(doc), nil
	}
	link := decodeSubBody(string(bodyBytes))
	if strings.HasPrefix(link, SchemeWdtt) || strings.HasPrefix(link, SchemeQwdtt) {
		p, err := parseWdttURI(link)
		if err != nil {
			if p2, err2 := parseQwdttURI(link); err2 == nil {
				p = p2
			} else {
				return LinkDecodeResult{}, err
			}
		}
		if p.SubURL == "" {
			p.SubURL = subURL
		}
		pp := p
		return LinkDecodeResult{Profile: &pp}, nil
	}
	p, err := parseConfigJSON(bodyBytes)
	if err != nil {
		return LinkDecodeResult{}, err
	}
	if p.SubURL == "" {
		p.SubURL = subURL
	}
	pp := p
	return LinkDecodeResult{Profile: &pp}, nil
}

func decodeSubBody(body string) string {
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, SchemeWdtt) || strings.HasPrefix(body, SchemeQwdtt) {
		return body
	}
	if dec, err := base64.StdEncoding.DecodeString(body); err == nil {
		s := strings.TrimSpace(string(dec))
		if strings.HasPrefix(s, SchemeWdtt) || strings.HasPrefix(s, SchemeQwdtt) {
			return s
		}
	}
	return body
}

func validateSubURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("некорректный URL подписки: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL подписки должен быть http(s)")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL подписки без хоста")
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("URL подписки указывает на внутренний адрес")
	}
	ips, err := lookupIP(host)
	if err != nil {
		return fmt.Errorf("не удалось разрешить хост подписки: %w", err)
	}
	for _, ip := range ips {
		// Приватные диапазоны (LAN) намеренно НЕ блокируем — сервер подписки в LAN легитимен.
		// Блок только loopback/link-local/unspecified: закрывает RCI localhost:79 и метадату.
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("URL подписки указывает на внутренний адрес")
		}
	}
	// DNS-rebinding закрыт dial-time IP-пином (blockInternalDial в Transport клиента):
	// фактический IP каждого connect проверяется повторно, резолв здесь — ранний отказ + defense-in-depth.
	return nil
}

// blockInternalDial проверяет фактически подключаемый IP в момент dial (после резолва,
// перед connect), закрывая DNS-rebinding: резолв в validateSubURL мог отличаться от dial-резолва.
func blockInternalDial(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()) {
		return fmt.Errorf("подписка резолвится во внутренний адрес")
	}
	return nil
}

func normalizePeer(peer string) string {
	peer = strings.TrimSpace(peer)
	if peer == "" {
		return peer
	}
	if strings.Contains(peer, ":") {
		return peer
	}
	return peer + ":56000"
}

// PeersEqual compares WDTT peer addresses with default port normalization.
func PeersEqual(a, b string) bool {
	return normalizePeer(a) == normalizePeer(b)
}

func normalizeSubURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return ""
	}
	return raw // keep query — subscription tokens are needed for RefreshSubscription
}

func splitHashes(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t'
	}) {
		h := normalizeVKJoinHash(part)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// normalizeVKJoinHash accepts a bare VK call hash or a full
// https://vk.com/call/join/… URL (as found in .qwdtt exports) and returns the hash.
func normalizeVKJoinHash(input string) string {
	s := strings.Trim(strings.TrimSpace(input), "<>\"'")
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	if idx := strings.Index(lower, "/call/join/"); idx >= 0 {
		s = s[idx+len("/call/join/"):]
	} else if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return ""
	}
	if idx := strings.IndexAny(s, "?#/"); idx != -1 {
		s = s[:idx]
	}
	return strings.Trim(strings.TrimSpace(s), "/")
}

func firstQuery(q url.Values, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

func firstStr(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			s := strings.TrimSpace(fmt.Sprint(v))
			if s != "" {
				return s
			}
		}
	}
	return ""
}

func intFrom(m map[string]interface{}, keys ...string) int {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch n := v.(type) {
		case float64:
			return int(n)
		case json.Number:
			i, _ := n.Int64()
			return int(i)
		case string:
			if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
				return i
			}
		}
	}
	return 0
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func pickName(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return b
}
