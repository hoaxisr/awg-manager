package judge

import (
	"fmt"
	"net/netip"
	"testing"
)

func hintPair(j *Judge, f Flow, one, two string, now int64) {
	j.Hint(f.Client, one, []netip.Addr{f.Server}, at(now))
	j.Hint(f.Client, two, []netip.Addr{f.Server}, at(now))
}

func unansweredPair(j *Judge, one, two string, port uint16, now int64) (Nomination, bool) {
	f := flowTo(port, "203.0.114.1")
	hintPair(j, f, one, two, now)
	return synTimes(j, f, now+1, now+3)
}

func TestDomainFailureAggregation(t *testing.T) {
	t.Run("distinct hosts reach domain threshold and restart", func(t *testing.T) {
		j := New()
		hosts := []string{"rr1---sn-a.googlevideo.com", "rr2---sn-b.googlevideo.com", "rr3---sn-c.googlevideo.com"}
		var out Failure
		for i, host := range hosts {
			out = mustFailureAt(t, j, flowTo(uint16(50001+i), fmt.Sprintf("203.0.114.%d", i+1)), host, 100+int64(i)*10)
			if i < 2 && out.Listed {
				t.Fatalf("early domain listing at %d: %+v", i, out)
			}
		}
		if !out.Listed || out.Domain != "googlevideo.com" || out.Host != hosts[0] || out.Fails != 3 {
			t.Fatalf("domain aggregate: %+v", out)
		}
		next := mustFailureAt(t, j, flowTo(50004, "203.0.114.4"), "rr4---sn-d.googlevideo.com", 130)
		if next.Listed || next.Fails != 1 {
			t.Fatalf("domain did not restart: %+v", next)
		}
	})

	t.Run("representative is host with most failures of triggering kind", func(t *testing.T) {
		j := NewWithParams(Params{FailThreshold: 4})
		mustFailureAt(t, j, flowTo(50001, "203.0.114.1"), "a.googlevideo.com", 100)
		mustFailureAt(t, j, flowTo(50002, "203.0.114.2"), "b.googlevideo.com", 105)
		mustFailureAt(t, j, flowTo(50003, "203.0.114.2"), "b.googlevideo.com", 110)
		out := mustFailureAt(t, j, flowTo(50004, "203.0.114.3"), "c.googlevideo.com", 115)
		if !out.Listed || out.Domain != "googlevideo.com" || out.Host != "b.googlevideo.com" {
			t.Fatalf("representative: %+v", out)
		}
	})

	t.Run("host threshold takes priority and resets domain failures", func(t *testing.T) {
		j := New()
		for i := 0; i < 3; i++ {
			mustFailureAt(t, j, flowTo(uint16(50001+i), "203.0.114.1"), "www.linkedin.com", 100+int64(i)*10)
		}
		out := mustFailureAt(t, j, flowTo(50004, "203.0.114.2"), "static.linkedin.com", 130)
		if out.Listed || out.Fails != 1 || out.Domain != "" {
			t.Fatalf("host listing did not reset domain kind: %+v", out)
		}
	})

	t.Run("domain listing restarts tracked hosts and marker", func(t *testing.T) {
		j := New()
		mustFailureAt(t, j, flowTo(50001, "203.0.114.1"), "www.example.com", 100)
		mustFailureAt(t, j, flowTo(50002, "203.0.114.1"), "www.example.com", 105)
		out := mustFailureAt(t, j, flowTo(50003, "203.0.114.2"), "cdn.example.com", 110)
		if !out.Listed || out.Host != "www.example.com" || out.Domain != "example.com" {
			t.Fatalf("domain listing: %+v", out)
		}
		next := mustFailureAt(t, j, flowTo(50004, "203.0.114.1"), "www.example.com", 115)
		if next.Listed || next.Fails != 1 {
			t.Fatalf("tracked host survived domain reset: %+v", next)
		}
	})

	t.Run("stale host and domain restart independently", func(t *testing.T) {
		j := New()
		mustFailureAt(t, j, flowTo(50001, "203.0.114.1"), "www.example.com", 100)
		mustFailureAt(t, j, flowTo(50002, "203.0.114.1"), "www.example.com", 102)
		out := mustFailureAt(t, j, flowTo(50003, "203.0.114.1"), "www.example.com", 200)
		if out.Listed || out.Fails != 1 {
			t.Fatalf("stale host did not restart: %+v", out)
		}
		out = mustFailureAt(t, j, flowTo(50004, "203.0.114.2"), "cdn.example.com", 205)
		if out.Listed {
			t.Fatalf("stale domain did not restart: %+v", out)
		}
		out = mustFailureAt(t, j, flowTo(50005, "203.0.114.2"), "cdn.example.com", 210)
		if !out.Listed || out.Host != "cdn.example.com" {
			t.Fatalf("cdn host should list independently: %+v", out)
		}
	})
}

func TestAnsweredResetsDomainFailures(t *testing.T) {
	t.Run("ordinary host answer", func(t *testing.T) {
		j := New()
		mustFailureAt(t, j, flowTo(50001, "203.0.114.1"), "rr1.googlevideo.com", 100)
		mustFailureAt(t, j, flowTo(50002, "203.0.114.2"), "rr2.googlevideo.com", 110)
		good := flowTo(50003, "203.0.114.9")
		helloTimes(j, good, "www.googlevideo.com", 1, 115)
		j.Answered(good, at(116))
		out := mustFailureAt(t, j, flowTo(50004, "203.0.114.3"), "rr3.googlevideo.com", 120)
		if out.Listed || out.Fails != 1 {
			t.Fatalf("answer did not reset domain tally: %+v", out)
		}
	})

	t.Run("domain-named hostless answer", func(t *testing.T) {
		j := New()
		for i := 0; i < 2; i++ {
			f := flowTo(uint16(50001+i), "203.0.114.1")
			hintPair(j, f, "www.instagram.com", "i.instagram.com", 100)
			mustFailureAt(t, j, f, "", 100+int64(i)*10)
		}
		good := flowTo(50003, "203.0.114.1")
		hintPair(j, good, "www.instagram.com", "i.instagram.com", 120)
		bareHelloTimes(j, good, "", 1, 120)
		j.Answered(good, at(121))
		f := flowTo(50004, "203.0.114.1")
		hintPair(j, f, "www.instagram.com", "i.instagram.com", 125)
		out := mustFailureAt(t, j, f, "", 125)
		if out.Listed || out.Fails != 1 {
			t.Fatalf("domain-named answer did not reset tally: %+v", out)
		}
	})
}

func TestStallAggregation(t *testing.T) {
	t.Run("two hosts nominate domain and reset notes", func(t *testing.T) {
		j := New()
		if out, ok := stalledAt(j, "a.freeze.test", 50001, 100); ok {
			t.Fatalf("first stall nominated: %+v", out)
		}
		out, ok := stalledAt(j, "b.freeze.test", 50002, 150)
		if !ok || out.Kind != NominationStall || out.Domain != "freeze.test" || out.Host != "a.freeze.test" {
			t.Fatalf("domain stall: %+v, %v", out, ok)
		}
		if out, ok := stalledAt(j, "c.freeze.test", 50003, 170); ok {
			t.Fatalf("domain notes were not reset: %+v", out)
		}
	})

	t.Run("same host nominates itself and clears domain notes", func(t *testing.T) {
		j := New()
		stalledAt(j, "a.freeze.test", 50001, 100)
		out, ok := stalledAt(j, "a.freeze.test", 50002, 150)
		if !ok || out.Host != "a.freeze.test" || out.Domain != "" {
			t.Fatalf("host stall: %+v, %v", out, ok)
		}
		if out, ok := stalledAt(j, "b.freeze.test", 50003, 170); ok {
			t.Fatalf("domain notes survived host nomination: %+v", out)
		}
	})
}

func TestNoConnectDomainAggregation(t *testing.T) {
	t.Run("representative uses no-connect counts", func(t *testing.T) {
		j := NewWithParams(Params{FailThreshold: 4})
		unansweredOn(j, "www.instagram.com", 50001, "203.0.114.1", 100)
		unansweredOn(j, "i.instagram.com", 50002, "203.0.114.2", 110)
		unansweredOn(j, "i.instagram.com", 50003, "203.0.114.2", 115)
		out, ok := unansweredOn(j, "scontent.instagram.com", 50004, "203.0.114.3", 120)
		if !ok || out.Domain != "instagram.com" || out.Host != "i.instagram.com" || out.Kind != NominationNoConnect {
			t.Fatalf("domain no-connect: %+v, %v", out, ok)
		}
	})

	t.Run("hostless names of one domain count and reset", func(t *testing.T) {
		j := New()
		for i := 0; i < 2; i++ {
			if out, ok := unansweredPair(j, "www.instagram.com", "i.instagram.com", uint16(50001+i), 100+int64(i)*10); ok {
				t.Fatalf("early hostless nomination: %+v", out)
			}
		}
		out, ok := unansweredPair(j, "www.instagram.com", "i.instagram.com", 50003, 120)
		if !ok || out.Bypass || out.Host != "instagram.com" || out.Domain != "instagram.com" {
			t.Fatalf("hostless domain nomination: %+v, %v", out, ok)
		}
		if out, ok := unansweredPair(j, "www.instagram.com", "i.instagram.com", 50004, 130); ok {
			t.Fatalf("hostless domain did not restart: %+v", out)
		}
	})

	t.Run("hostless count outranks a single host", func(t *testing.T) {
		j := New()
		unansweredOn(j, "graph.instagram.com", 50001, "203.0.114.2", 100)
		unansweredPair(j, "www.instagram.com", "i.instagram.com", 50002, 105)
		out, ok := unansweredPair(j, "www.instagram.com", "i.instagram.com", 50003, 110)
		if !ok || out.Host != "instagram.com" || out.Domain != "instagram.com" {
			t.Fatalf("hostless marker: %+v, %v", out, ok)
		}
		unansweredOn(j, "graph.instagram.com", 50004, "203.0.114.2", 115)
		unansweredOn(j, "graph.instagram.com", 50005, "203.0.114.2", 120)
		out, ok = unansweredPair(j, "www.instagram.com", "i.instagram.com", 50006, 125)
		if !ok || out.Host != "graph.instagram.com" {
			t.Fatalf("marker after hostless reset: %+v, %v", out, ok)
		}
	})
}

func TestCounterResetIsolation(t *testing.T) {
	t.Run("host no-connect nomination keeps domain failures", func(t *testing.T) {
		j := New()
		mustFailureAt(t, j, flowTo(50001, "203.0.114.1"), "www.example.com", 100)
		mustFailureAt(t, j, flowTo(50002, "203.0.114.2"), "api.example.com", 105)
		unansweredOn(j, "cdn.example.com", 50003, "203.0.114.3", 110)
		unansweredOn(j, "cdn.example.com", 50004, "203.0.114.3", 115)
		nom, ok := unansweredOn(j, "cdn.example.com", 50005, "203.0.114.3", 120)
		if !ok || nom.Host != "cdn.example.com" || nom.Domain != "" {
			t.Fatalf("host no-connect: %+v, %v", nom, ok)
		}
		out := mustFailureAt(t, j, flowTo(50006, "203.0.114.4"), "img.example.com", 125)
		if !out.Listed || out.Domain != "example.com" || out.Fails != 3 {
			t.Fatalf("domain failures were reset by no-connect: %+v", out)
		}
	})

	t.Run("host failure listing keeps domain no-connect", func(t *testing.T) {
		j := New()
		unansweredOn(j, "www.example.com", 50001, "203.0.114.1", 100)
		unansweredOn(j, "api.example.com", 50002, "203.0.114.2", 105)
		mustFailureAt(t, j, flowTo(50003, "203.0.114.3"), "cdn.example.com", 110)
		mustFailureAt(t, j, flowTo(50004, "203.0.114.3"), "cdn.example.com", 112)
		out := mustFailureAt(t, j, flowTo(50005, "203.0.114.3"), "cdn.example.com", 114)
		if !out.Listed || out.Domain != "" {
			t.Fatalf("host failure: %+v", out)
		}
		nom, ok := unansweredOn(j, "img.example.com", 50006, "203.0.114.4", 120)
		if !ok || nom.Domain != "example.com" {
			t.Fatalf("domain no-connect was reset by failure: %+v, %v", nom, ok)
		}
	})

	t.Run("domain nomination keeps failure tallies", func(t *testing.T) {
		j := New()
		mustFailureAt(t, j, flowTo(50001, "203.0.114.1"), "www.example.com", 100)
		mustFailureAt(t, j, flowTo(50002, "203.0.114.1"), "www.example.com", 102)
		unansweredOn(j, "a.example.com", 50003, "203.0.114.2", 104)
		unansweredOn(j, "b.example.com", 50004, "203.0.114.3", 106)
		nom, ok := unansweredOn(j, "c.example.com", 50005, "203.0.114.4", 108)
		if !ok || nom.Domain != "example.com" {
			t.Fatalf("domain no-connect: %+v, %v", nom, ok)
		}
		out := mustFailureAt(t, j, flowTo(50006, "203.0.114.1"), "www.example.com", 110)
		if !out.Listed || out.Host != "www.example.com" || out.Domain != "" || out.Fails != 3 {
			t.Fatalf("domain no-connect reset failures: %+v", out)
		}
	})

	t.Run("host stall keeps domain no-connect", func(t *testing.T) {
		j := New()
		unansweredOn(j, "x.freeze.test", 50001, "203.0.114.1", 100)
		unansweredOn(j, "y.freeze.test", 50002, "203.0.114.2", 105)
		stalledAt(j, "a.freeze.test", 50011, 110)
		nom, ok := stalledAt(j, "a.freeze.test", 50012, 115)
		if !ok || nom.Domain != "" {
			t.Fatalf("host stall: %+v, %v", nom, ok)
		}
		nom, ok = unansweredOn(j, "z.freeze.test", 50003, "203.0.114.3", 130)
		if !ok || nom.Domain != "freeze.test" || nom.Kind != NominationNoConnect {
			t.Fatalf("stall reset no-connect: %+v, %v", nom, ok)
		}
	})
}

func TestSharedSuffixAndLostDomain(t *testing.T) {
	t.Run("multi-tenant domains never aggregate", func(t *testing.T) {
		j := New()
		for i, host := range []string{"p.alice.github.io", "q.alice.github.io", "r.alice.github.io"} {
			out := mustFailureAt(t, j, flowTo(uint16(50001+i), "203.0.114.1"), host, 100+int64(i)*10)
			if out.Listed || out.Domain != "" {
				t.Fatalf("shared suffix listed: %+v", out)
			}
		}
		for i, host := range []string{"a.cdn.akamaized.net", "b.cdn.akamaized.net", "c.cdn.akamaized.net"} {
			if out, ok := unansweredOn(j, host, uint16(50004+i), "203.0.114.2", 100+int64(i)*10); ok {
				t.Fatalf("shared suffix no-connect nominated: %+v", out)
			}
		}
	})

	t.Run("domain-named flow that becomes many is ambiguous", func(t *testing.T) {
		j := New()
		f := flowTo(50001, "203.0.114.1")
		hintPair(j, f, "www.instagram.com", "i.instagram.com", 100)
		bareHelloTimes(j, f, "", 1, 100)
		j.Hint(f.Client, "web.telegram.org", []netip.Addr{f.Server}, at(101))
		if out, ok := bareHelloTimes(j, f, "", 3, 102); ok {
			t.Fatalf("lost domain emitted: %+v", out)
		}
		if j.AmbiguousDay(at(104)) != 1 || j.FailuresDay(at(104)) != 0 {
			t.Fatal("lost domain accounting mismatch")
		}
	})
}

func TestRegDomain(t *testing.T) {
	tests := []struct {
		host   string
		want   string
		ok     bool
		shared bool
	}{
		{host: "example.com", want: "example.com", ok: true},
		{host: "www.example.com", want: "example.com", ok: true},
		{host: "a.b.example.com.", want: "example.com", ok: true},
		{host: "x.co.uk", want: "x.co.uk", ok: true},
		{host: "a.x.co.uk", want: "x.co.uk", ok: true},
		{host: "co.uk", ok: false},
		{host: "localhost", ok: false},
		{host: "192.0.2.1", ok: false},
		{host: "2001:db8::1", ok: false},
		{host: ".example.com", ok: false},
		{host: "a..example.com", ok: false},
		{host: "x.github.io", want: "x.github.io", ok: true, shared: true},
		{host: "p.alice.github.io", want: "alice.github.io", ok: true, shared: true},
		{host: "core.windows.net", ok: false},
		{host: "x.core.windows.net", want: "x.core.windows.net", ok: true, shared: true},
		{host: "a.x.core.windows.net", want: "x.core.windows.net", ok: true, shared: true},
		{host: "www.msk.ru", want: "www.msk.ru", ok: true},
	}
	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			got, ok := regDomain(tc.host)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("regDomain=%q/%v, want %q/%v", got, ok, tc.want, tc.ok)
			}
			if ok && regDomainShared(got) != tc.shared {
				t.Fatalf("shared=%v, want %v", regDomainShared(got), tc.shared)
			}
		})
	}
}
