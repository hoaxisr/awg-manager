package roles

import "testing"

// Границу задаёт прошивка: mips отвергает OpkgTun17 («index 17 is too large»,
// стенд 5.1.3), arm держит 17..49 (проверено владельцем). Пул mips делится с
// fakeip и AWG, поэтому там shared.
func TestOpkgIndexRangeByArch(t *testing.T) {
	cases := []struct {
		arch   string
		min    int
		max    int
		shared bool
	}{
		{"mipsle", 0, 15, true},
		{"mips", 0, 15, true},
		{"arm64", 17, 49, false},
		{"amd64", 17, 49, false},
	}
	for _, c := range cases {
		min, max, shared := OpkgIndexRange(c.arch)
		if min != c.min || max != c.max || shared != c.shared {
			t.Fatalf("OpkgIndexRange(%q) = (%d,%d,%v), ожидали (%d,%d,%v)",
				c.arch, min, max, shared, c.min, c.max, c.shared)
		}
	}
	// Граница на точном значении: 16 на mips уже вне пула, 17 на arm — внутри.
	if _, max, _ := OpkgIndexRange("mipsle"); max >= 16 {
		t.Fatalf("mips: верхняя граница %d, прошивка отвергает уже 17 (и 16 занят AWG)", max)
	}
	if min, _, _ := OpkgIndexRange("arm64"); min != 17 {
		t.Fatalf("arm: нижняя граница %d, ожидали 17 (ниже — fakeip и AWG)", min)
	}
}
