package wdttlink

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// Перенос ports_test.go:8-34 старого мира на конфиг роли: тот же набор
// исходов, тот же ожидаемый порт.
func TestLinkListenPort(t *testing.T) {
	wg := roles.WdttServerConfig{Listen: "0.0.0.0:56002", WgPort: 56001, RelayMode: ConnModeWG}
	if p := LinkListenPort(wg); p != 56002 {
		t.Fatalf("wg port: got %d", p)
	}
	wgDirect := roles.WdttServerConfig{
		Listen:       "0.0.0.0:56010",
		DirectListen: "0.0.0.0:56002",
		WgPort:       56011,
		RelayMode:    ConnModeWG,
	}
	if p := LinkListenPort(wgDirect); p != 56002 {
		t.Fatalf("wg direct port: got %d", p)
	}
	raw := roles.WdttServerConfig{Listen: "0.0.0.0:56002", RelayMode: ConnModeRaw, RawListen: "0.0.0.0:56003"}
	if p := LinkListenPort(raw); p != 56003 {
		t.Fatalf("raw port: got %d", p)
	}
	rawAuto := roles.WdttServerConfig{Listen: "0.0.0.0:56002", RelayMode: ConnModeRaw}
	if p := LinkListenPort(rawAuto); p != 56003 {
		t.Fatalf("raw auto port: got %d", p)
	}
}

// Режим ссылки не обязан совпадать с RelayMode записи (§11).
func TestLinkListenPortForMode_OverridesRelayMode(t *testing.T) {
	cfg := roles.WdttServerConfig{Listen: "0.0.0.0:56002", DirectListen: "0.0.0.0:56004", RelayMode: ConnModeWG}
	if p := LinkListenPortForMode(cfg, ConnModeRaw); p != 56003 {
		t.Fatalf("raw over wg-сервера: got %d", p)
	}
	rawCfg := roles.WdttServerConfig{Listen: "0.0.0.0:56002", RelayMode: ConnModeRaw}
	if p := LinkListenPortForMode(rawCfg, ConnModeWG); p != 56002 {
		t.Fatalf("wg over raw-сервера: got %d", p)
	}
	// Пустой режим — не raw: normalizeConnMode отдаёт wg.
	if p := LinkListenPortForMode(rawCfg, ""); p != 56002 {
		t.Fatalf("пустой режим: got %d", p)
	}
}

func TestTunnelNameFromClient(t *testing.T) {
	if got := TunnelNameFromClient("Германия"); got != "Германия wdtt" {
		t.Fatalf("got %q", got)
	}
	if got := TunnelNameFromClient("Германия wdtt"); got != "Германия wdtt" {
		t.Fatalf("duplicate suffix: got %q", got)
	}
	if got := TunnelNameFromClient(""); got != "WDTT wdtt" {
		t.Fatalf("empty name: got %q", got)
	}
}
