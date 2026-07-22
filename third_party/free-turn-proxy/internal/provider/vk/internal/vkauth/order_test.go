package vkauth

import (
	"slices"
	"testing"
)

func TestOrderAddrs(t *testing.T) {
	t.Parallel()
	addrs := []string{"a", "b", "c"}

	cases := []struct {
		streamID int
		want     []string
	}{
		{0, []string{"a", "b", "c"}},
		{1, []string{"b", "c", "a"}},
		{2, []string{"c", "a", "b"}},
		{3, []string{"a", "b", "c"}}, // wrap
	}
	for _, tc := range cases {
		got := orderAddrs(addrs, tc.streamID)
		if !slices.Equal(got, tc.want) {
			t.Errorf("orderAddrs(streamID=%d) = %v, want %v", tc.streamID, got, tc.want)
		}
	}

	// Не мутирует вход.
	if !slices.Equal(addrs, []string{"a", "b", "c"}) {
		t.Errorf("orderAddrs mutated input: %v", addrs)
	}

	// Один адрес - копия из одного элемента.
	if got := orderAddrs([]string{"x"}, 5); !slices.Equal(got, []string{"x"}) {
		t.Errorf("single addr: got %v", got)
	}
}
