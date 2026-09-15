package vlink

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMapClashShadowsocks_HappyPath(t *testing.T) {
	in := map[string]any{
		"name":     "SS-1",
		"type":     "ss",
		"server":   "ss.example.com",
		"port":     8388,
		"cipher":   "aes-128-gcm",
		"password": "ss-pass",
	}
	got, err := mapClashShadowsocks(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Protocol != "shadowsocks" {
		t.Errorf("Protocol=%q want shadowsocks", got.Protocol)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ob["method"] != "aes-128-gcm" {
		t.Errorf("method=%v", ob["method"])
	}
	if ob["password"] != "ss-pass" {
		t.Errorf("password=%v", ob["password"])
	}
}

func TestMapClashShadowsocks_MissingCipher(t *testing.T) {
	_, err := mapClashShadowsocks(map[string]any{
		"server":   "h",
		"port":     8388,
		"password": "p",
	})
	if err == nil || !strings.Contains(err.Error(), "cipher") {
		t.Errorf("want cipher error, got %v", err)
	}
}

func TestMapClashShadowsocks_MissingPassword(t *testing.T) {
	_, err := mapClashShadowsocks(map[string]any{
		"server": "h",
		"port":   8388,
		"cipher": "aes-128-gcm",
	})
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("want password error, got %v", err)
	}
}

func TestMapClashShadowsocks_MissingPort(t *testing.T) {
	_, err := mapClashShadowsocks(map[string]any{
		"server":   "h",
		"cipher":   "aes-128-gcm",
		"password": "p",
	})
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Errorf("want port error, got %v", err)
	}
}

func TestMapClashShadowsocks_CipherAuto(t *testing.T) {
	_, err := mapClashShadowsocks(map[string]any{
		"server":   "h",
		"port":     8388,
		"cipher":   "auto",
		"password": "p",
	})
	if err == nil || !strings.Contains(err.Error(), "auto") {
		t.Errorf("want 'auto' rejection, got %v", err)
	}
}

func TestMapClashShadowsocks_PluginObfsLocal(t *testing.T) {
	in := map[string]any{
		"server":   "h",
		"port":     8388,
		"cipher":   "aes-128-gcm",
		"password": "p",
		"plugin":   "obfs",
		"plugin-opts": map[string]any{
			"mode": "http",
			"host": "cloudfront.net",
		},
	}
	got, err := mapClashShadowsocks(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ob["plugin"] != "obfs-local" {
		t.Errorf("plugin=%v want obfs-local", ob["plugin"])
	}
	opts := asString(ob["plugin_opts"])
	if !strings.Contains(opts, "obfs=http") || !strings.Contains(opts, "obfs-host=cloudfront.net") {
		t.Errorf("plugin_opts=%q must contain obfs=http and obfs-host=cloudfront.net", opts)
	}
}

func TestMapClashShadowsocks_PluginV2rayPlugin(t *testing.T) {
	in := map[string]any{
		"server":   "h",
		"port":     8388,
		"cipher":   "aes-128-gcm",
		"password": "p",
		"plugin":   "v2ray-plugin",
		"plugin-opts": map[string]any{
			"mode": "websocket",
			"host": "cdn.example.com",
			"path": "/v2",
			"tls":  true,
		},
	}
	got, err := mapClashShadowsocks(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ob["plugin"] != "v2ray-plugin" {
		t.Errorf("plugin=%v want v2ray-plugin", ob["plugin"])
	}
	opts := asString(ob["plugin_opts"])
	for _, want := range []string{"mode=websocket", "host=cdn.example.com", "path=/v2", "tls"} {
		if !strings.Contains(opts, want) {
			t.Errorf("plugin_opts=%q must contain %q", opts, want)
		}
	}
}

// Раньше неизвестное имя плагина пропускалось насквозь. В sing-box
// зарегистрированы ровно два плагина (transport/sip003), остальное валит
// создание аутбаунда, а значит и `sing-box check`, а значит и применение ВСЕЙ
// конфигурации: одна запись чужой подписки (xray-plugin, shadow-tls, kcptun,
// cloak) блокировала бы все остальные. Ничто между разбором и применением
// имя плагина не проверяет — значит проверять здесь.
func TestMapClashShadowsocks_PluginUnknown_Rejected(t *testing.T) {
	for _, plugin := range []string{"unknown-plugin", "xray-plugin", "shadow-tls"} {
		in := map[string]any{
			"server":   "h",
			"port":     8388,
			"cipher":   "aes-128-gcm",
			"password": "p",
			"plugin":   plugin,
		}
		if _, err := mapClashShadowsocks(in); err == nil {
			t.Errorf("%s: принят", plugin)
		}
	}
}

// Два известных плагина по-прежнему проходят.
func TestMapClashShadowsocks_PluginKnown_Accepted(t *testing.T) {
	in := map[string]any{
		"server":   "h",
		"port":     8388,
		"cipher":   "aes-128-gcm",
		"password": "p",
		"plugin":   "v2ray-plugin",
	}
	got, err := mapClashShadowsocks(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ob["plugin"] != "v2ray-plugin" {
		t.Errorf("plugin=%v want v2ray-plugin", ob["plugin"])
	}
}
