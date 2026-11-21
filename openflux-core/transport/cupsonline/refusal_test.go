package cupsonline

import (
	"testing"
	"time"
)

func TestRefusalPauseGrowsAndCalmsDown(t *testing.T) {
	var g refusalGate
	t0 := time.Unix(1000, 0)
	if d, began := g.refused(t0); !began || d != refusalPauseMin {
		t.Fatalf("first refusal: %v %v", d, began)
	}
	// Refusals of messages already on their way change nothing.
	if _, began := g.refused(t0.Add(time.Second)); began {
		t.Fatal("a refusal inside the pause began another")
	}
	if g.wait(t0.Add(refusalPauseMin/2)) <= 0 || g.wait(t0.Add(refusalPauseMin)) != 0 {
		t.Fatal("wait does not follow the pause")
	}
	// Refused again right after: twice as long, up to the ceiling.
	at := t0.Add(refusalPauseMin)
	want := refusalPauseMin
	for i := 0; i < 10; i++ {
		want = min(2*want, refusalPauseMax)
		d, began := g.refused(at)
		if !began || d != want {
			t.Fatalf("refusal %d after a pause: %v, want %v", i+2, d, want)
		}
		at = at.Add(d)
	}
	// A calm spell starts over.
	if d, _ := g.refused(at.Add(refusalCalm + time.Second)); d != refusalPauseMin {
		t.Fatalf("after a calm spell: %v", d)
	}
}

// A refusing server gets nothing from any room while the pause lasts, and
// traffic flows again once it takes messages.
func TestARefusedAddressGoesQuiet(t *testing.T) {
	refusals = refusalGate{}
	oldMin, oldMax := refusalPauseMin, refusalPauseMax
	refusalPauseMin, refusalPauseMax = 2*time.Second, 2*time.Second
	t.Cleanup(func() { refusals, refusalPauseMin, refusalPauseMax = refusalGate{}, oldMin, oldMax })

	f := newFakeCups(t)
	exit, client := startPair(t, fastConfig())
	deliver(t, client, exit, []byte("before"), 2*time.Second)

	f.locked(func() { f.refuse = true })
	for i := 0; i < 20; i++ {
		_ = client.Send([]byte("into a refusing server"))
		time.Sleep(10 * time.Millisecond)
	}
	waitFor(t, 2*time.Second, "the pause", func() bool { return refusals.wait(time.Now()) > 0 })
	// Inside the 2 s pause, with traffic still coming: at most what was
	// already on its way when the pause began.
	var during int
	f.locked(func() { during = f.refusedRPCs })
	for end := time.Now().Add(time.Second); time.Now().Before(end); {
		_ = client.Send([]byte("still sending"))
		time.Sleep(10 * time.Millisecond)
	}
	var after int
	f.locked(func() { after = f.refusedRPCs })
	if after-during > 3 {
		t.Fatalf("rooms kept sending during the pause: %d more messages", after-during)
	}

	f.locked(func() { f.refuse = false })
	deliver(t, client, exit, []byte("after the pause"), 6*time.Second)
	// What was queued during the pause still arrives; let it, so no room of
	// this test is still receiving when the next test starts.
	for quiet := time.NewTimer(500 * time.Millisecond); ; {
		select {
		case <-exit.got:
			quiet.Reset(500 * time.Millisecond)
			continue
		case <-quiet.C:
		}
		break
	}
}
