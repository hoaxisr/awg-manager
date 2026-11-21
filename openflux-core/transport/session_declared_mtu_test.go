package transport

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// smallWire is a carrier that declares it moves packets of 1200 bytes and no more, as a data channel does
// when a record has to fit one datagram.
type smallWire struct{ *startCountingWire }

func (w *smallWire) CarrierMTU() int { return 1200 }

func smallPair(t *testing.T) (client, exit *Session) {
	t.Helper()
	var err error
	if client, err = NewSession(testParams, false); err != nil {
		t.Fatal(err)
	}
	if exit, err = NewSession(testParams, true); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Session{client, exit} {
		s.restartMin, s.restartMax = 10*time.Millisecond, 20*time.Millisecond
		s.handshakeTimeout = 3 * time.Second
	}
	a, b := &startCountingWire{}, &startCountingWire{}
	a.peer, b.peer = &b.negotiationWire, &a.negotiationWire
	if err := client.AddTransport("max", &smallWire{a}, testSessionSecret, testSessionCtx, 100); err != nil {
		t.Fatal(err)
	}
	if err := exit.AddTransport("max", &smallWire{b}, testSessionSecret, testSessionCtx, 100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop(); _ = exit.Stop() })
	return client, exit
}

// A carrier that declares less than the usual floor of 1280 is believed: the stack runs the MTU it asked for.
func TestSessionCarrierMTUBelowTheUsualFloor(t *testing.T) {
	client, exit := smallPair(t)
	startPair(t, client, exit)
	eventually(t, "the declared packet size to be reported", func() bool { return client.CarrierMTU() == 1200 })
}

// TCP above the declared size is refused with the size the carrier does move (the stack answers with an ICMP
// "fragmentation needed", an exit cuts what receive offload merged); anything else goes as it always did.
func TestSessionRefusesTCPAboveADeclaredSmallLimit(t *testing.T) {
	client, exit := smallPair(t)
	var got atomic.Int32
	exit.Receive(func([]byte) { got.Add(1) })
	startPair(t, client, exit)
	eventually(t, "the carrier to be live", func() bool { return client.CarrierMTU() == 1200 })

	if err := client.Send(testIPv4(1200, 6)); err != nil {
		t.Fatalf("a TCP packet of exactly the declared size: %v", err)
	}
	eventually(t, "it to arrive", func() bool { return got.Load() == 1 })

	err := client.Send(testIPv4(1400, 6))
	var big *PacketTooLargeError
	if !errors.As(err, &big) || big.Limit != 1200 {
		t.Fatalf("TCP of 1400 bytes: err %v, want a refusal naming 1200", err)
	}
	if err := client.Send(testIPv4(1400, 17)); err != nil {
		t.Fatalf("UDP above the declared size must still go: %v", err)
	}
	eventually(t, "the UDP packet to arrive", func() bool { return got.Load() == 2 })
}
