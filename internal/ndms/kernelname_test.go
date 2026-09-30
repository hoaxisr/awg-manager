package ndms

import "testing"

// Таблица снята на стенде 5.01.C.6 (30.09.2026). Всё, чего там нет, — ok=false.
func TestKernelName(t *testing.T) {
	for _, tc := range []struct {
		id, want string
		ok       bool
	}{
		{"Wireguard0", "nwg0", true},
		{"Wireguard13", "nwg13", true},
		{"OpkgTun11", "opkgtun11", true},
		{"Proxy0", "t2s0", true},
		{"PPPoE0", "ppp0", true},
		{"Bridge0", "br0", true},
		{"PPPoE1", "", false},
		{"Bridge1", "", false},
		{"Bridge10", "", false},
		{"GigabitEthernet1", "", false},
		{"WifiMaster0/AccessPoint0", "", false},
		{"UsbQmi0", "", false},
		{"L2TP0", "", false},
		{"Wireguard", "", false},
		{"OpkgTun+5", "", false},
		{"OpkgTun-5", "", false},
		{"Wireguard01", "", false},
		{"wireguard0", "", false},
		{"", "", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			got, ok := KernelName(tc.id)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("KernelName(%q) = %q, %v; want %q, %v", tc.id, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// Расширение таблицы — только с новым стенд-фактом: тест держит ровно пять
// классов, и новый префикс обязан прийти вместе с правкой этого списка.
func TestKernelName_ExactlyFiveClasses(t *testing.T) {
	want := map[string]string{
		"Wireguard": "nwg",
		"OpkgTun":   "opkgtun",
		"Proxy":     "t2s",
		"PPPoE":     "ppp",
		"Bridge":    "br",
	}
	zeroOnly := map[string]bool{"PPPoE": true, "Bridge": true} // стенд: только номер 0
	if len(kernelNamePrefixes) != len(want) {
		t.Fatalf("классов %d, ждали %d", len(kernelNamePrefixes), len(want))
	}
	for _, p := range kernelNamePrefixes {
		if want[p.ndms] != p.kernel || zeroOnly[p.ndms] != p.zeroOnly {
			t.Fatalf("класс %q → %q не из стенд-таблицы", p.ndms, p.kernel)
		}
	}
}
