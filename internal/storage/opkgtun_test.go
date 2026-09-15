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
