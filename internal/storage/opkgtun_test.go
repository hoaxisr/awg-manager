package storage

import "testing"

func TestAWGTunnelOpkgTunIndex(t *testing.T) {
	tests := []struct {
		name    string
		tunnel  AWGTunnel
		wantIdx int
		wantOK  bool
	}{
		{"kernel", AWGTunnel{ID: "awg12", Backend: "kernel"}, 12, true},
		{"пустой backend — legacy kernel", AWGTunnel{ID: "awg13"}, 13, true},
		{"nativewg в своём диапазоне", AWGTunnel{ID: "awg25", Backend: "nativewg"}, 0, false},
		{"legacy nativewg в kernel-диапазоне", AWGTunnel{ID: "awg12", Backend: "nativewg"}, 0, false},
		{"raw-клиент wdtt", AWGTunnel{ID: "wdttraw-home", Backend: "wdtt-raw"}, 0, false},
		{"OS4-запись", AWGTunnel{ID: "awgm5", Backend: "kernel"}, 0, false},
		{"клиентский ID", AWGTunnel{ID: "home4", Backend: "kernel"}, 4, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, ok := tt.tunnel.OpkgTunIndex()
			if ok != tt.wantOK {
				t.Fatalf("OpkgTunIndex() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && idx != tt.wantIdx {
				t.Errorf("OpkgTunIndex() = %d, want %d", idx, tt.wantIdx)
			}
		})
	}
}

func TestOpkgTunIndicesOf(t *testing.T) {
	tunnels := []AWGTunnel{
		{ID: "awg10", Backend: "kernel"},
		{ID: "awg13"},                             // legacy kernel
		{ID: "awg25", Backend: "nativewg"},        // номер не занимает
		{ID: "wdttraw-home", Backend: "wdtt-raw"}, // номер живёт в RawNdmsIface
		{ID: "awgm5", Backend: "kernel"},          // OS4, NDMS-имени нет
	}

	got := OpkgTunIndicesOf(tunnels)

	want := map[int]bool{10: true, 13: true}
	if len(got) != len(want) {
		t.Fatalf("OpkgTunIndicesOf() = %v, want %v", got, want)
	}
	for idx := range want {
		if !got[idx] {
			t.Errorf("номер %d должен быть занят, got %v", idx, got)
		}
	}
}

// Пустая выдача при пустом входе — отдельный случай: аллокатор обязан отличать
// «занятых нет» от «не смогли посмотреть», и второе выражается ошибкой у
// поставщика, а не пустой картой здесь.
func TestOpkgTunIndicesOfEmpty(t *testing.T) {
	if got := OpkgTunIndicesOf(nil); len(got) != 0 {
		t.Errorf("OpkgTunIndicesOf(nil) = %v, want пустую карту", got)
	}
}
