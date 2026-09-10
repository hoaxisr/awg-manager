package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ValidateKeepalive — только формат. Её накладывают на слитую запись, поэтому
// значение, сохранённое прошлой версией, обязано её проходить: иначе такой
// туннель перестанет правиться целиком.
func TestValidateKeepalive(t *testing.T) {
	for _, ok := range []storage.Keepalive{"", "0", "25", "22-30", "65535", "0-80"} {
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

// ValidateKeepaliveSubmitted — то же плюс запрет нулевой нижней границы: 0
// означает «keepalive выключен», диапазон — «случайное значение из отрезка»,
// вместе они противоречат друг другу. Её накладывают только на присланное
// значение, поэтому запирать сохранённые туннели ей нечем.
func TestValidateKeepaliveSubmitted(t *testing.T) {
	for _, ok := range []storage.Keepalive{"", "0", "25", "22-30", "65535"} {
		if err := ValidateKeepaliveSubmitted(ok); err != nil {
			t.Fatalf("%q должно приниматься: %v", ok, err)
		}
	}
	for _, bad := range []storage.Keepalive{"abc", "22-", "-5", "30-22", "70000", "0-80", "0-0", "00-80", " 0 - 80 "} {
		if err := ValidateKeepaliveSubmitted(bad); err == nil {
			t.Fatalf("%q должно отклоняться", bad)
		}
	}

	// Отказ обязан объяснять причину — иначе пользователь правит вслепую.
	err := ValidateKeepaliveSubmitted("0-80")
	if err == nil || !strings.Contains(err.Error(), "выключ") {
		t.Fatalf("сообщение не объясняет, что 0 означает выключенный keepalive: %v", err)
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
