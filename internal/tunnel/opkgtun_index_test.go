package tunnel

import "testing"

func TestOpkgTunIndexOf(t *testing.T) {
	tests := []struct {
		name     string
		tunnelID string
		wantIdx  int
		wantOK   bool
	}{
		{"kernel-диапазон", "awg12", 12, true},
		{"legacy-нумерация", "awg0", 0, true},
		{"nativewg-диапазон по ID", "awg25", 25, true},
		{"OS4 без NDMS-имени", "awgm5", 0, false},
		{"OS4 нулевой", "awgm0", 0, false},
		{"клиентский ID с цифрой", "home4", 4, true},
		{"клиентский ID без цифр", "myvpn", 0, true},
		// Фолбэк extractTunnelNum даёт "0" на любом ID без цифр — отсев
		// raw-клиентов и nativewg возможен только по backend, не по имени.
		{"raw-клиент wdtt", "wdttraw-home", 0, true},
		{"пустой ID", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, ok := OpkgTunIndexOf(tt.tunnelID)
			if ok != tt.wantOK {
				t.Fatalf("OpkgTunIndexOf(%q) ok = %v, want %v", tt.tunnelID, ok, tt.wantOK)
			}
			if ok && idx != tt.wantIdx {
				t.Errorf("OpkgTunIndexOf(%q) = %d, want %d", tt.tunnelID, idx, tt.wantIdx)
			}
		})
	}
}

// Предикат обязан совпадать с NewNames: имя строит она, и расхождение означало
// бы, что аллокатор считает свободным номер, который занят живым интерфейсом.
func TestOpkgTunIndexOfMatchesNewNames(t *testing.T) {
	ids := []string{"awg0", "awg10", "awg16", "awg25", "awgm3", "home4", "myvpn", "wdttraw-x"}

	for _, id := range ids {
		idx, ok := OpkgTunIndexOf(id)
		ndmsName := NewNames(id).NDMSName

		if ok != (ndmsName != "") {
			t.Errorf("%q: ok = %v, а NDMSName = %q", id, ok, ndmsName)
			continue
		}
		if ok && ndmsName != "OpkgTun"+itoa(idx) {
			t.Errorf("%q: индекс %d не сходится с именем %q", id, idx, ndmsName)
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
