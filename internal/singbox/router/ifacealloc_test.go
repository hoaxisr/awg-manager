package router

import (
	"errors"
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
