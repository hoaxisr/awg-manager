package awgoutbounds

import "testing"

func TestManagedTag(t *testing.T) {
	got := ManagedTag("abc-123")
	want := "awg-abc-123"
	if got != want {
		t.Errorf("ManagedTag(%q) = %q, want %q", "abc-123", got, want)
	}
}

func TestSystemTag(t *testing.T) {
	got := SystemTag("Wireguard0")
	want := "awg-sys-Wireguard0"
	if got != want {
		t.Errorf("SystemTag(%q) = %q, want %q", "Wireguard0", got, want)
	}
}

func TestIsAWGTag(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"awg-foo", true},
		{"awg-sys-Wireguard0", true},
		{"awg-", true},
		{"direct", false},
		{"selector", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsAWGTag(c.in); got != c.want {
			t.Errorf("IsAWGTag(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIsSystemTag(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"awg-sys-Wireguard0", true},
		{"awg-sys-", true},
		{"awg-foo", false},
		{"awg-", false},
		{"sys-Wireguard0", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsSystemTag(c.in); got != c.want {
			t.Errorf("IsSystemTag(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
