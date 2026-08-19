package listenfirewall

import "testing"

func TestWANListenPort(t *testing.T) {
	tests := []struct {
		addr   string
		want   int
		wantOK bool
	}{
		{"0.0.0.0:56002", 56002, true},
		{":56000", 56000, true},
		{"127.0.0.1:56000", 0, false},
		{"192.168.1.1:56000", 0, false},
		{"bad", 0, false},
	}
	for _, tc := range tests {
		port, ok := WANListenPort(tc.addr)
		if port != tc.want || ok != tc.wantOK {
			t.Fatalf("WANListenPort(%q) = (%d, %v), want (%d, %v)", tc.addr, port, ok, tc.want, tc.wantOK)
		}
	}
}

func TestOpenEnabled(t *testing.T) {
	on := true
	off := false
	if !OpenEnabled(nil) {
		t.Fatal("nil should default to true")
	}
	if !OpenEnabled(&on) {
		t.Fatal("true pointer")
	}
	if OpenEnabled(&off) {
		t.Fatal("false pointer")
	}
}

func TestMergePortSpecs(t *testing.T) {
	got := MergePortSpecs([]PortSpec{
		{Port: 56000, Proto: "udp"},
		{Port: 56000, Proto: "udp"},
		{Port: 56002, Proto: "udp"},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 unique specs, got %d", len(got))
	}
}

// Холостой тик (ни один прокси-сервер не работает) восстанавливать нечего:
// правила сами не появляются, а сверка стоит вызовов iptables. Снос делается
// один раз на переход в простой, дальше тик пропускается целиком — и снова
// оживает, как только появился хоть один порт.
func TestSkipIdleTick(t *testing.T) {
	ports := []PortSpec{{Port: 500, Proto: "udp"}}
	tests := []struct {
		name      string
		desired   []PortSpec
		idleSwept bool
		want      bool
	}{
		{"первый холостой тик сносит", nil, false, false},
		{"второй холостой тик пропускается", nil, true, true},
		{"есть порты — сверка обязательна", ports, true, false},
		{"есть порты, простоя не было", ports, false, false},
	}
	for _, tt := range tests {
		if got := skipIdleTick(tt.desired, tt.idleSwept); got != tt.want {
			t.Errorf("%s: skipIdleTick = %v, want %v", tt.name, got, tt.want)
		}
	}
}
