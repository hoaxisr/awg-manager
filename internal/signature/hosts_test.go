package signature

import (
	mrand "math/rand"
	"net"
	"testing"
)

func TestHostPool_NoIPLiterals(t *testing.T) {
	for _, h := range hostPool {
		if net.ParseIP(h) != nil {
			t.Fatalf("host pool must not contain IP literals: %q", h)
		}
	}
}

func TestPickHost_FromPoolAndWeightedToHead(t *testing.T) {
	r := mrand.New(mrand.NewSource(1))
	seen := map[string]int{}
	for i := 0; i < 5000; i++ {
		h := pickHost(r)
		if !hostPoolHas(h) {
			t.Fatalf("host %q not in pool", h)
		}
		seen[h]++
	}
	if seen[hostPool[0]] <= seen[hostPool[len(hostPool)-1]] {
		t.Fatalf("head %q (%d) must be drawn more often than tail %q (%d)",
			hostPool[0], seen[hostPool[0]], hostPool[len(hostPool)-1], seen[hostPool[len(hostPool)-1]])
	}
}
