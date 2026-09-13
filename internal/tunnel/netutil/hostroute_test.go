package netutil

import "testing"

func TestSkipHostRoute(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true}, // связанный wdtt/freeturn, обфусцированный релей
		{"127.0.0.5", true}, // вся /8, а не один адрес
		{"::1", true},       // v6-петля
		{"0.0.0.0", true},   // фильтрующий DNS на заблокированный домен
		{"::", true},        // он же в v6
		{"169.254.10.1", true},
		{"fe80::1", true},
		{"ff02::1", true},
		{"203.0.113.5", false},
		{"10.8.0.1", false},     // цепочка туннелей: приватный адрес маршрут получает
		{"192.168.0.10", false}, // сервер в LAN — тоже
		{"2001:db8::1", false},
		{"vpn.example.com", false}, // не адрес: пусть отказывает ip, причина видна
		{"", false},
	} {
		t.Run(tc.ip, func(t *testing.T) {
			if got := SkipHostRoute(tc.ip); got != tc.want {
				t.Errorf("SkipHostRoute(%q) = %v, ждали %v", tc.ip, got, tc.want)
			}
		})
	}
}
