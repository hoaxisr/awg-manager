package judge

import (
	"fmt"
	"net/netip"
	"testing"
	"time"
)

func at(sec int64) time.Time { return time.Unix(sec, 0) }

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func testFlow(port uint16) Flow {
	return Flow{
		Client:     mustAddr("192.168.1.10"),
		Server:     mustAddr("203.0.113.7"),
		ClientPort: port,
		ServerPort: 443,
	}
}

func flowTo(port uint16, server string) Flow {
	f := testFlow(port)
	f.Server = mustAddr(server)
	return f
}

func bareHelloTimes(j *Judge, f Flow, sni string, times int, start int64) (Failure, bool) {
	var out Failure
	emitted := false
	for i := 0; i < times; i++ {
		got, ok := j.Hello(f, 1000, 0, sni, at(start+int64(i)))
		if ok {
			out, emitted = got, true
		}
	}
	return out, emitted
}

func helloTimes(j *Judge, f Flow, sni string, times int, start int64) (Failure, bool) {
	if sni != "" {
		j.Hint(f.Client, sni, []netip.Addr{f.Server}, at(start))
	}
	return bareHelloTimes(j, f, sni, times, start)
}

func failureAt(j *Judge, f Flow, host string, now int64) (Failure, bool) {
	return helloTimes(j, f, host, 4, now)
}

func mustFailureAt(t *testing.T, j *Judge, f Flow, host string, now int64) Failure {
	t.Helper()
	out, ok := failureAt(j, f, host, now)
	if !ok {
		t.Fatalf("failure at %d did not emit", now)
	}
	return out
}

func TestHelloTransitions(t *testing.T) {
	t.Run("original and three retransmissions", func(t *testing.T) {
		j := New()
		f := testFlow(50000)
		j.Hint(f.Client, "blocked.example", []netip.Addr{f.Server}, at(100))
		for i, want := range []bool{false, false, false, true, false} {
			out, ok := j.Hello(f, 1000, 0, "blocked.example", at(100+int64(i)))
			if ok != want {
				t.Fatalf("send %d: emitted=%v, want %v", i+1, ok, want)
			}
			if want {
				if out.Host != "blocked.example" || out.Why != WhyRetrans || out.Fails != 1 || out.Listed || out.Bypass {
					t.Fatalf("unexpected failure: %+v", out)
				}
			}
		}
	})

	t.Run("new sequence starts a new handshake", func(t *testing.T) {
		j := New()
		f := testFlow(50000)
		j.Hint(f.Client, "x.example", []netip.Addr{f.Server}, at(100))
		for i := 0; i < 2; i++ {
			if _, ok := j.Hello(f, 1000, 0, "x.example", at(100+int64(i))); ok {
				t.Fatal("old handshake emitted early")
			}
		}
		for i := 0; i < 4; i++ {
			out, ok := j.Hello(f, 2000, 0, "x.example", at(102+int64(i)))
			if ok != (i == 3) {
				t.Fatalf("new send %d: emitted=%v", i+1, ok)
			}
			if ok && out.Bypass {
				t.Fatal("matching hint became bypass")
			}
		}
	})

	t.Run("custom thresholds", func(t *testing.T) {
		j := NewWithParams(Params{FailThreshold: 1, RetransThreshold: 1})
		out, ok := helloTimes(j, testFlow(50000), "fast.example", 2, 100)
		if !ok || !out.Listed || out.Fails != 1 {
			t.Fatalf("custom threshold failure: %+v, %v", out, ok)
		}
	})

	t.Run("flow lifetime boundary", func(t *testing.T) {
		for _, tc := range []struct {
			delay int64
			sent  uint8
		}{{300, 2}, {301, 1}} {
			t.Run(fmt.Sprint(tc.delay), func(t *testing.T) {
				j := New()
				f := testFlow(50000)
				j.Hello(f, 1000, 0, "x.example", at(100))
				j.Hello(f, 1000, 0, "x.example", at(100+tc.delay))
				if got := j.flows[0].sent; got != tc.sent {
					t.Fatalf("sent=%d, want %d", got, tc.sent)
				}
			})
		}
	})

	t.Run("retransmission counter saturates", func(t *testing.T) {
		j := NewWithParams(Params{RetransThreshold: 255})
		f := testFlow(50000)
		j.Hint(f.Client, "x.example", []netip.Addr{f.Server}, at(0))
		j.Hello(f, 1, 0, "x.example", at(1))
		for i := 0; i < 300; i++ {
			if _, ok := j.Hello(f, 1, 0, "x.example", at(2)); ok {
				t.Fatal("255 retransmissions cannot be represented by the saturating counter")
			}
		}
		if j.flows[0].sent != 255 {
			t.Fatalf("sent=%d, want 255", j.flows[0].sent)
		}
	})
}

func TestFailureWindowAndListing(t *testing.T) {
	tests := []struct {
		name       string
		times      []int64
		wantFails  uint32
		wantListed bool
	}{
		{name: "three within window", times: []int64{100, 110, 120}, wantFails: 3, wantListed: true},
		{name: "exactly sixty seconds stays live", times: []int64{100, 160, 220}, wantFails: 3, wantListed: true},
		{name: "sixty one seconds restarts", times: []int64{100, 150, 220}, wantFails: 1, wantListed: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := New()
			var out Failure
			for i, now := range tc.times {
				f := testFlow(uint16(50000 + i))
				out = mustFailureAt(t, j, f, "slow.example", now)
			}
			if out.Fails != tc.wantFails || out.Listed != tc.wantListed {
				t.Fatalf("got fails=%d listed=%v, want %d/%v", out.Fails, out.Listed, tc.wantFails, tc.wantListed)
			}
			next := mustFailureAt(t, j, testFlow(50010), "slow.example", tc.times[len(tc.times)-1]+1)
			if tc.wantListed && (next.Fails != 1 || next.Listed) {
				t.Fatalf("listed host did not restart: %+v", next)
			}
		})
	}
}

func TestResetAndAnsweredTransitions(t *testing.T) {
	t.Run("reset is one verdict", func(t *testing.T) {
		j := New()
		f := testFlow(50000)
		helloTimes(j, f, "reset.example", 1, 100)
		out, ok := j.Reset(f, at(101))
		if !ok || out.Why != WhyReset || out.Host != "reset.example" {
			t.Fatalf("reset: %+v, %v", out, ok)
		}
		if _, ok := j.Reset(f, at(102)); ok {
			t.Fatal("second reset emitted")
		}
		if _, ok := j.Hello(f, 1000, 0, "reset.example", at(103)); ok {
			t.Fatal("judged flow emitted on retransmission")
		}
	})

	t.Run("unknown and SYN replies are ignored", func(t *testing.T) {
		j := New()
		f := testFlow(50000)
		if _, ok := j.Reset(f, at(100)); ok {
			t.Fatal("unknown reset emitted")
		}
		j.Hint(f.Client, "x.example", []netip.Addr{f.Server}, at(100))
		j.SYN(f, 999, at(100))
		if _, ok := j.Reset(f, at(102)); ok {
			t.Fatal("SYN reset emitted")
		}
		j.Answered(f, at(102))
		if j.flows[0].judged {
			t.Fatal("SYN reply judged the flow")
		}
	})

	t.Run("answered blocks later reset", func(t *testing.T) {
		j := New()
		f := testFlow(50000)
		helloTimes(j, f, "good.example", 1, 100)
		if _, ok := j.Reply(f, false, at(101)); ok {
			t.Fatal("answered reply emitted a failure")
		}
		if !j.flows[0].judged {
			t.Fatal("answered flow was not judged")
		}
		if _, ok := j.Reset(f, at(102)); ok {
			t.Fatal("reset after answer emitted")
		}
	})

	t.Run("good unknown address preserves failures", func(t *testing.T) {
		j := New()
		bad1 := flowTo(50001, "203.0.113.41")
		good := flowTo(50002, "203.0.113.215")
		bad2 := flowTo(50003, "203.0.113.41")
		bad3 := flowTo(50004, "203.0.113.41")
		mustFailureAt(t, j, bad1, "www.linkedin.com", 100)
		helloTimes(j, good, "www.linkedin.com", 1, 110)
		j.Answered(good, at(111))
		if out := mustFailureAt(t, j, bad2, "www.linkedin.com", 112); out.Fails != 2 {
			t.Fatalf("good unrelated address reset failures: %+v", out)
		}
		if out := mustFailureAt(t, j, bad3, "www.linkedin.com", 120); !out.Listed {
			t.Fatalf("third failure did not list: %+v", out)
		}
	})

	t.Run("answer from known bad address restarts host", func(t *testing.T) {
		j := New()
		onA := flowTo(50001, "203.0.113.41")
		onB := flowTo(50002, "203.0.113.215")
		bAgain := flowTo(50003, "203.0.113.215")
		later := flowTo(50004, "203.0.113.41")
		mustFailureAt(t, j, onA, "x.example", 100)
		mustFailureAt(t, j, onB, "x.example", 105)
		helloTimes(j, bAgain, "x.example", 1, 110)
		j.Answered(bAgain, at(111))
		if out := mustFailureAt(t, j, later, "x.example", 112); out.Fails != 1 || out.Listed {
			t.Fatalf("known good answer did not restart: %+v", out)
		}
	})
}

func TestFlowNaming(t *testing.T) {
	tests := []struct {
		name          string
		hintClient    string
		hintName      string
		sni           string
		hintAge       int64
		wantHost      string
		wantBypass    bool
		wantEmitted   bool
		wantAmbiguous uint32
	}{
		{name: "SNI with matching hint", hintName: "a.test", sni: "a.test", wantHost: "a.test", wantEmitted: true},
		{name: "SNI without hint is bypass", sni: "a.test", wantHost: "a.test", wantBypass: true, wantEmitted: true},
		{name: "SNI with another hint is bypass", hintName: "b.test", sni: "a.test", wantHost: "a.test", wantBypass: true, wantEmitted: true},
		{name: "sniless takes only hint", hintName: "a.test", wantHost: "a.test", wantEmitted: true},
		{name: "sniless without hint is bypass", wantBypass: true, wantEmitted: true},
		{name: "other client hint is not used", hintClient: "192.168.1.11", hintName: "a.test", wantBypass: true, wantEmitted: true},
		{name: "hint valid at TTL", hintName: "a.test", hintAge: 600, wantHost: "a.test", wantEmitted: true},
		{name: "stale hint is bypass", hintName: "a.test", hintAge: 601, wantBypass: true, wantEmitted: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := New()
			f := testFlow(50000)
			if tc.hintName != "" {
				client := f.Client
				if tc.hintClient != "" {
					client = mustAddr(tc.hintClient)
				}
				j.Hint(client, tc.hintName, []netip.Addr{f.Server}, at(90))
			}
			out, ok := bareHelloTimes(j, f, tc.sni, 4, 90+tc.hintAge)
			if ok != tc.wantEmitted {
				t.Fatalf("emitted=%v, want %v (%+v)", ok, tc.wantEmitted, out)
			}
			if ok && (out.Host != tc.wantHost || out.Bypass != tc.wantBypass) {
				t.Fatalf("got %+v, want host=%q bypass=%v", out, tc.wantHost, tc.wantBypass)
			}
		})
	}
}

func TestAmbiguousAndDomainNaming(t *testing.T) {
	t.Run("two unrelated names are ambiguous", func(t *testing.T) {
		j := New()
		f := testFlow(50000)
		j.Hint(f.Client, "a.example", []netip.Addr{f.Server}, at(90))
		j.Hint(f.Client, "b.test", []netip.Addr{f.Server}, at(91))
		if out, ok := bareHelloTimes(j, f, "", 4, 100); ok {
			t.Fatalf("ambiguous flow emitted: %+v", out)
		}
		if got := j.AmbiguousDay(at(104)); got != 1 {
			t.Fatalf("ambiguous=%d, want 1", got)
		}
		if got := j.FailuresDay(at(104)); got != 0 {
			t.Fatalf("failures=%d, want 0", got)
		}
		if got := len(j.Addresses(at(104))); got != 0 {
			t.Fatalf("ambiguous flow made %d address rows", got)
		}
	})

	t.Run("explicit SNI selects among several hints", func(t *testing.T) {
		j := New()
		f := testFlow(50000)
		j.Hint(f.Client, "a.test", []netip.Addr{f.Server}, at(90))
		j.Hint(f.Client, "b.test", []netip.Addr{f.Server}, at(91))
		out, ok := bareHelloTimes(j, f, "a.test", 4, 100)
		if !ok || out.Bypass || out.Host != "a.test" {
			t.Fatalf("SNI selection: %+v, %v", out, ok)
		}
	})

	t.Run("names in one domain aggregate without a host", func(t *testing.T) {
		j := New()
		var out Failure
		for i := 0; i < 3; i++ {
			f := testFlow(uint16(50000 + i))
			j.Hint(f.Client, "www.instagram.com", []netip.Addr{f.Server}, at(90))
			j.Hint(f.Client, "i.instagram.com", []netip.Addr{f.Server}, at(90))
			out = mustFailureAt(t, j, f, "", 100+int64(i)*10)
		}
		if !out.Listed || out.Host != "instagram.com" || out.Domain != "instagram.com" || out.Fails != 3 {
			t.Fatalf("domain-named failure: %+v", out)
		}
		if j.AmbiguousDay(at(130)) != 0 {
			t.Fatal("same-domain hints counted as ambiguous")
		}
	})

	t.Run("shared hosting stays ambiguous", func(t *testing.T) {
		j := NewWithParams(Params{FailThreshold: 1})
		f := testFlow(50000)
		j.Hint(f.Client, "p.alice.github.io", []netip.Addr{f.Server}, at(90))
		j.Hint(f.Client, "q.alice.github.io", []netip.Addr{f.Server}, at(90))
		j.SYN(f, 999, at(100))
		if out, ok := j.SYN(f, 999, at(102)); ok {
			t.Fatalf("shared suffix nominated: %+v", out)
		}
		if j.AmbiguousDay(at(102)) != 1 {
			t.Fatal("shared suffix was not counted ambiguous")
		}
	})
}

func TestDayCounters(t *testing.T) {
	j := New()
	const base = int64(3600 * 1000)
	for i := 0; i < 3; i++ {
		mustFailureAt(t, j, testFlow(uint16(50000+i)), "day.test", base+int64(i))
	}
	ambiguous := testFlow(51000)
	j.Hint(ambiguous.Client, "a.test", []netip.Addr{ambiguous.Server}, at(base))
	j.Hint(ambiguous.Client, "b.test", []netip.Addr{ambiguous.Server}, at(base))
	bareHelloTimes(j, ambiguous, "", 4, base)
	for _, tc := range []struct {
		offset          int64
		fail, ambiguous uint32
	}{{86399, 3, 1}, {86400, 0, 0}, {90000, 0, 0}} {
		if got := j.FailuresDay(at(base + tc.offset)); got != tc.fail {
			t.Errorf("offset %d: failures=%d, want %d", tc.offset, got, tc.fail)
		}
		if got := j.AmbiguousDay(at(base + tc.offset)); got != tc.ambiguous {
			t.Errorf("offset %d: ambiguous=%d, want %d", tc.offset, got, tc.ambiguous)
		}
	}
}
