package singbox

import (
	"fmt"
	"testing"
)

// allocator honoring reserved as a skip-set: returns lowest slot not reserved.
func skipSetAlloc(reserved map[int]bool) func() (int, error) {
	return func() (int, error) {
		for i := 0; i < maxProxySlots; i++ {
			if !reserved[i] {
				return i, nil
			}
		}
		return 0, fmt.Errorf("full")
	}
}

func TestAllocBindableSlot_SkipsOccupiedPort(t *testing.T) {
	orig := portBindable
	defer func() { portBindable = orig }()
	// slot 0 (firstPort) is held externally; everything else free.
	portBindable = func(port int) bool { return port != firstPort }

	reserved := map[int]bool{}
	idx, port, err := allocBindableSlot(reserved, skipSetAlloc(reserved))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx != 1 || port != firstPort+1 {
		t.Fatalf("got idx=%d port=%d, want idx=1 port=%d", idx, port, firstPort+1)
	}
}

func TestAllocBindableSlot_FirstPortFreeUsesIt(t *testing.T) {
	orig := portBindable
	defer func() { portBindable = orig }()
	portBindable = func(int) bool { return true }

	reserved := map[int]bool{}
	idx, port, err := allocBindableSlot(reserved, skipSetAlloc(reserved))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx != 0 || port != firstPort {
		t.Fatalf("got idx=%d port=%d, want idx=0 port=%d", idx, port, firstPort)
	}
}

func TestAllocBindableSlot_AllOccupied(t *testing.T) {
	orig := portBindable
	defer func() { portBindable = orig }()
	portBindable = func(int) bool { return false }

	reserved := map[int]bool{}
	if _, _, err := allocBindableSlot(reserved, skipSetAlloc(reserved)); err == nil {
		t.Fatal("expected error when every probed port is occupied")
	}
}
