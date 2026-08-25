package ops

import "testing"

// NDMS принимает маску точечной формой, а хранится она длиной префикса — той
// самой, что ввёл пользователь. Ноль означает «не задана»: адрес интерфейса с
// нулевым префиксом бессмыслен, а прежнее поведение было именно /32.
func TestMaskFromPrefix(t *testing.T) {
	tests := []struct {
		prefix int
		want   string
	}{
		{24, "255.255.255.0"},
		{32, "255.255.255.255"},
		{16, "255.255.0.0"},
		{30, "255.255.255.252"},
		{0, "255.255.255.255"},
		{-1, "255.255.255.255"},
		{33, "255.255.255.255"},
	}

	for _, tt := range tests {
		if got := maskFromPrefix(tt.prefix); got != tt.want {
			t.Errorf("maskFromPrefix(%d) = %q, want %q", tt.prefix, got, tt.want)
		}
	}
}

func TestAddressWithPrefix(t *testing.T) {
	tests := []struct {
		addr   string
		prefix int
		want   string
	}{
		{"10.55.0.2", 24, "10.55.0.2/24"},
		{"10.8.0.2", 0, "10.8.0.2/32"},
		{"", 24, ""},
	}

	for _, tt := range tests {
		if got := addressWithPrefix(tt.addr, tt.prefix); got != tt.want {
			t.Errorf("addressWithPrefix(%q, %d) = %q, want %q", tt.addr, tt.prefix, got, tt.want)
		}
	}
}
