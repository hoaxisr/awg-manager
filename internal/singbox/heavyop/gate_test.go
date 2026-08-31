package heavyop

import (
	"testing"
)

func TestGateTryLock(t *testing.T) {
	var g Gate
	if !g.TryLock() {
		t.Fatal("TryLock on free gate must succeed")
	}
	if g.TryLock() {
		t.Fatal("TryLock on held gate must fail")
	}
	g.Unlock()
	if !g.TryLock() {
		t.Fatal("TryLock after Unlock must succeed")
	}
	g.Unlock()
}
