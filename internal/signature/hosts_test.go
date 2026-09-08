package signature

import (
	mrand "math/rand"
	"net"
	"strings"
	"testing"
)

func TestHostPool_NoIPLiterals(t *testing.T) {
	for _, h := range hostPool {
		if net.ParseIP(h) != nil {
			t.Fatalf("host pool must not contain IP literals: %q", h)
		}
	}
}

// Пул — кандидаты в SNI/QNAME веб-трафика. Хосты VoIP-инфраструктуры в h3
// ClientHello или в A-запросе выдают инструмент, а не браузер.
func TestHostPool_NoVoIPInfrastructure(t *testing.T) {
	for _, h := range hostPool {
		for _, p := range []string{"stun.", "turn.", "sip.", "pbx.", "sips.", "voip."} {
			if strings.HasPrefix(h, p) {
				t.Fatalf("host pool must not contain VoIP infrastructure hosts: %q", h)
			}
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
