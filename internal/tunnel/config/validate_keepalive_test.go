package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestValidateKeepalive(t *testing.T) {
	for _, ok := range []storage.Keepalive{"", "0", "25", "22-30", "0-80", "65535"} {
		if err := ValidateKeepalive(ok); err != nil {
			t.Fatalf("%q должно приниматься: %v", ok, err)
		}
	}
	for _, bad := range []storage.Keepalive{"abc", "22-", "-5", "30-22", "70000", "1-70000"} {
		if err := ValidateKeepalive(bad); err == nil {
			t.Fatalf("%q должно отклоняться", bad)
		}
	}
}

// NativeWG отдаёт keepalive в NDMS числом, диапазонов прошивка не знает.
func TestValidateKeepaliveForBackend(t *testing.T) {
	if err := ValidateKeepaliveForBackend("22-30", "kernel"); err != nil {
		t.Fatalf("kernel должен принимать диапазон: %v", err)
	}
	if err := ValidateKeepaliveForBackend("25", "nativewg"); err != nil {
		t.Fatalf("nativewg должен принимать одиночное значение: %v", err)
	}
	err := ValidateKeepaliveForBackend("22-30", "nativewg")
	if err == nil {
		t.Fatal("nativewg не должен принимать диапазон")
	}
	if !strings.Contains(err.Error(), "kernel") {
		t.Fatalf("ошибка должна подсказывать про kernel-режим, получили %q", err)
	}
}

// Одиночное значение остаётся в tunnels.json числом: файл, записанный новой
// версией, продолжают читать предыдущие.
func TestKeepaliveJSONShape(t *testing.T) {
	for _, tc := range []struct {
		value storage.Keepalive
		want  string
	}{
		{"25", "25"},
		{"", "0"},
		{"0", "0"},
		{"22-30", `"22-30"`},
	} {
		got, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatalf("%q: %v", tc.value, err)
		}
		if string(got) != tc.want {
			t.Fatalf("%q сериализовалось как %s, ждали %s", tc.value, got, tc.want)
		}
	}

	// Читаем обе формы: число из старых файлов и строку из новых.
	var fromNumber, fromString storage.Keepalive
	if err := json.Unmarshal([]byte(`25`), &fromNumber); err != nil || fromNumber != "25" {
		t.Fatalf("число: %v %q", err, fromNumber)
	}
	if err := json.Unmarshal([]byte(`"22-30"`), &fromString); err != nil || fromString != "22-30" {
		t.Fatalf("строка: %v %q", err, fromString)
	}
}

func TestParseGenerateKeepaliveRange(t *testing.T) {
	conf := `[Interface]
PrivateKey = GF+8XleAkOGCOSOX0yRC5duoeI0fY12VSTtZ1sJ3uXk=
Address = 10.0.0.2/32

[Peer]
PublicKey = t/y0HtImYhv3EIA6gWX4pvKdHVSBFVbSfX1ldOMlXl4=
AllowedIPs = 0.0.0.0/0
Endpoint = 192.0.2.1:51820
PersistentKeepalive = 22-30
`
	tunnel, err := Parse(conf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tunnel.Peer.PersistentKeepalive != "22-30" {
		t.Fatalf("диапазон потерялся при разборе: %q", tunnel.Peer.PersistentKeepalive)
	}
	if !strings.Contains(Generate(tunnel), "PersistentKeepalive = 22-30") {
		t.Fatalf("диапазон не попал в .conf:\n%s", Generate(tunnel))
	}
}
