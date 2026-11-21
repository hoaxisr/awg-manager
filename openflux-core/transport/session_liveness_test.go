package transport

import (
	"sync/atomic"
	"testing"
	"time"
)

func fastKeepalive(sessions ...*Session) {
	for _, s := range sessions {
		s.keepaliveInterval = 20 * time.Millisecond
		s.linkTimeout = 100 * time.Millisecond
	}
}

func blackhole(wires ...*startCountingWire) {
	for _, w := range wires {
		w.mu.Lock()
		w.drop = true
		w.mu.Unlock()
	}
}

func sentCount(w *startCountingWire) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.packets)
}

// settled is w's packet count once the handshake has gone quiet on it: an
// exit answers every initial hello, on every carrier, and the client
// confirms each answer, so a few hellos may still be on their way when
// startPair returns.
func settled(w *startCountingWire) int {
	n := sentCount(w)
	for quiet := 0; quiet < 10; quiet++ {
		time.Sleep(10 * time.Millisecond)
		if m := sentCount(w); m != n {
			n, quiet = m, 0
		}
	}
	return n
}

func startPair(t *testing.T, client, exit *Session) {
	t.Helper()
	if err := exit.Start(); err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both sides ready", func() bool { return client.IsConnected() && exit.IsConnected() })
}

// A carrier can stay attached to its document (IsConnected) while the
// peer's side of it is down; traffic must move to a carrier that still
// reaches the peer.
func TestSessionFailsOverWhenCarrierStopsReachingPeer(t *testing.T) {
	client, exit, cw, ew := linkedSessions(t, "direct", "yandex")
	fastKeepalive(client, exit)
	var got atomic.Int32
	exit.Receive(func([]byte) { got.Add(1) })
	startPair(t, client, exit)
	eventually(t, "keepalive support to be detected", func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.peerKeepalive
	})

	if got := client.ActiveTransport(); got != "direct" {
		t.Fatalf("active transport = %q, want direct", got)
	}
	blackhole(cw["direct"], ew["direct"])
	eventually(t, "data to arrive over the remaining carrier", func() bool {
		_ = client.Send(testIPv4(40, 6))
		return got.Load() > 0
	})
	if !client.IsConnected() {
		t.Fatal("session reported down while a carrier still reaches the peer")
	}
	eventually(t, "the active transport to follow the failover", func() bool {
		return client.ActiveTransport() == "yandex"
	})
}

func TestSessionPrefersHigherPriorityCarrier(t *testing.T) {
	client, exit, cw, _ := linkedSessions(t, "direct", "yandex")
	var got atomic.Int32
	exit.Receive(func([]byte) { got.Add(1) })
	startPair(t, client, exit)

	before := settled(cw["yandex"])
	for i := 0; i < 5; i++ {
		if err := client.Send(testIPv4(40, 6)); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "data delivery", func() bool { return got.Load() == 5 })
	if after := sentCount(cw["yandex"]); after != before {
		t.Fatalf("lower-priority carrier carried data while the preferred one was live (%d -> %d)", before, after)
	}
}

// Peers that predate keepalive never answer pings; their carriers must not
// age out of routing.
func TestSessionToleratesPeerWithoutKeepalive(t *testing.T) {
	client, exit, _, _ := linkedSessions(t, "direct")
	fastKeepalive(client, exit)
	exit.noPong = true
	var got atomic.Int32
	exit.Receive(func([]byte) { got.Add(1) })
	startPair(t, client, exit)

	time.Sleep(300 * time.Millisecond)
	if !client.IsConnected() {
		t.Fatal("carrier aged out against a peer without keepalive")
	}
	if err := client.Send(testIPv4(40, 6)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "data delivery", func() bool { return got.Load() == 1 })
}

// Carriers of equal top priority share the traffic, so the app must be told
// all of them, and not the lower-priority standby.
func TestSessionActiveTransportsNameTheWholeTopGroup(t *testing.T) {
	priorities := []int{100, 100, 50}
	client, exit, _, _ := linkedSessionsWith(t, func(i int) int { return priorities[i] }, "boards", "yandex", "direct")
	fastKeepalive(client, exit)
	startPair(t, client, exit)

	eventually(t, "both top-priority carriers to be active", func() bool {
		got := client.ActiveTransports()
		return len(got) == 2 && got[0] == "boards" && got[1] == "yandex"
	})
	if got := client.ActiveTransport(); got != "boards" {
		t.Fatalf("ActiveTransport = %q, want the first of the group", got)
	}

	_ = client.Stop()
	if got := client.ActiveTransports(); got != nil {
		t.Fatalf("ActiveTransports after Stop = %v, want none", got)
	}
}

// An exit whose client went silent on every carrier stops pinging it after
// exitPingIdle: on a room carrier every ping is messages to the service, and
// a host's sessions all send from one address.
func TestExitStopsPingingAGoneClient(t *testing.T) {
	client, exit, _, ew := linkedSessions(t, "direct", "yandex")
	fastKeepalive(client, exit)
	exit.exitPingIdle = 300 * time.Millisecond
	startPair(t, client, exit)
	_ = client.Stop() // the device goes away without a word
	time.Sleep(exit.exitPingIdle + 100*time.Millisecond)
	before := sentCount(ew["direct"]) + sentCount(ew["yandex"])
	time.Sleep(20 * exit.keepaliveInterval)
	if after := sentCount(ew["direct"]) + sentCount(ew["yandex"]); after != before {
		t.Fatalf("the exit kept pinging a client gone for %v (%d -> %d frames)", exit.exitPingIdle, before, after)
	}
}

// While the client is there the exit keeps pinging the carriers it is quiet on.
func TestExitPingsALiveClientsQuietCarrier(t *testing.T) {
	client, exit, _, ew := linkedSessions(t, "direct", "yandex")
	fastKeepalive(client, exit)
	exit.exitPingIdle = 300 * time.Millisecond
	startPair(t, client, exit)
	before := sentCount(ew["yandex"])
	time.Sleep(time.Second)
	if sentCount(ew["yandex"]) == before {
		t.Fatal("the exit stopped pinging the quiet carrier of a client that is still there")
	}
}
