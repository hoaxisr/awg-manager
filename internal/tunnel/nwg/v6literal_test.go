package nwg

import "testing"

// F240: предикат легко «унифицировать» с соседом или с поиском двоеточия —
// обе подмены выглядят безобидно и обе меняют поведение ровно на том случае,
// ради которого предикаты и разведены.
//
// isV6Literal            — «уже отрезолвленный адрес является v6-литералом»
// EndpointHostIsIPv6     — «сырая endpoint-строка несёт v6» (To4 запрещён)
// strings.Contains(":")  — «в строке есть двоеточие» (host:port тоже)
func TestIsV6Literal_Boundaries(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"2001:db8::1", true},
		{"fe80::1", true},
		{"::1", true},
		{"1.2.3.4", false},
		{"::ffff:1.2.3.4", false}, // IPv4-mapped: адрес v4, EndpointHostIsIPv6 сказал бы true
		{"vpn.example.com", false},
		{"vpn.example.com:51820", false}, // Contains(":") сказал бы true
		{"", false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := isV6Literal(tc.in); got != tc.want {
				t.Errorf("isV6Literal(%q) = %v, ждали %v", tc.in, got, tc.want)
			}
		})
	}
}

// Соседний предикат намеренно другой — если оба начнут отвечать одинаково,
// значит кто-то их «схлопнул», и заглушка endpoint'а поедет не туда.
func TestIsV6Literal_DiffersFromEndpointPredicate(t *testing.T) {
	const mapped = "::ffff:1.2.3.4"
	if isV6Literal(mapped) == EndpointHostIsIPv6(mapped) {
		t.Fatalf("предикаты совпали на IPv4-mapped — разведены они именно ради этого случая")
	}
}

// F230: у обфусцированного туннеля Peer.Endpoint — 127.0.0.1:<порт> релея, и
// Create/SyncPeer резолвят именно его. Оркестратор сохраняет
// GetTrackedEndpointIP в ResolvedEndpointIP, поэтому петля затирала бы там
// адрес target'а релея — единственное, по чему потом снимается host-route.
func TestTrackEndpointIP_SkipsLoopback(t *testing.T) {
	o := &OperatorNativeWG{}

	o.trackEndpointIP("awg20", "127.0.0.1")
	if got := o.GetTrackedEndpointIP("awg20"); got != "" {
		t.Errorf("петля попала в трекер: %q", got)
	}

	o.trackEndpointIP("awg20", "203.0.113.5")
	if got := o.GetTrackedEndpointIP("awg20"); got != "203.0.113.5" {
		t.Errorf("обычный адрес обязан трекаться, got %q", got)
	}

	// ::1 — та же петля другой формой.
	o.trackEndpointIP("awg21", "::1")
	if got := o.GetTrackedEndpointIP("awg21"); got != "" {
		t.Errorf("v6-петля попала в трекер: %q", got)
	}
}
