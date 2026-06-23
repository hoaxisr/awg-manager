package router

import (
	"errors"
	"reflect"
	"testing"
)

func TestAllocateFakeIPIndex(t *testing.T) {
	tests := []struct {
		name string
		live map[int]bool
		want int
	}{
		{"empty", map[int]bool{}, 0},
		{"nil", nil, 0},
		{"first taken", map[int]bool{0: true}, 1},
		{"gap at 2", map[int]bool{0: true, 1: true, 3: true}, 2},
		{"top free", map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true, 8: true}, 9},
		{"out-of-band ignored", map[int]bool{10: true, 16: true, 100: true}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := allocateFakeIPIndex(tt.live)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAllocateFakeIPIndexExhausted(t *testing.T) {
	live := make(map[int]bool)
	for i := 0; i <= maxFakeIPIndex; i++ {
		live[i] = true
	}
	_, err := allocateFakeIPIndex(live)
	if !errors.Is(err, ErrFakeIPIndexExhausted) {
		t.Fatalf("want ErrFakeIPIndexExhausted, got %v", err)
	}
}

func TestUnionOpkgTunIndices(t *testing.T) {
	sysNums := []int{0, 2}
	ndmsNames := []string{
		"opkgtun1",   // -> 1 (наш диапазон)
		"opkgtun100", // -> 100 (managed на OS5, заякорен)
		"awg3",       // -> 3 (over-count, но матчится anchored)
		"awgm4",      // -> 4 (over-count, anchored)
		"nwg2",       // не матчится -> игнор
		"br0",        // не матчится -> игнор
		"Wireguard0", // не матчится -> игнор
		"opkgtun",    // нет цифры -> не матчится
	}
	got := UnionOpkgTunIndices(sysNums, ndmsNames)
	want := map[int]bool{0: true, 2: true, 1: true, 100: true, 3: true, 4: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
