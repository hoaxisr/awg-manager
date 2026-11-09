package judge

import (
	"net/netip"
	"testing"
)

func stalledFlow(j *Judge, f Flow, host string, ack0, finAck uint32, start, age int64) (Nomination, bool) {
	j.Hint(f.Client, host, []netip.Addr{f.Server}, at(start))
	j.Hello(f, 7, ack0, host, at(start))
	j.Answered(f, at(start+1))
	return j.Close(f, finAck, at(start+age))
}

func stalledAt(j *Judge, host string, port uint16, start int64) (Nomination, bool) {
	f := flowTo(port, "203.0.114.7")
	return stalledFlow(j, f, host, 1000, 1000+16384, start, 11)
}

func synTimes(j *Judge, f Flow, times ...int64) (Nomination, bool) {
	var out Nomination
	emitted := false
	for _, now := range times {
		got, ok := j.SYN(f, 999, at(now))
		if ok {
			out, emitted = got, true
		}
	}
	return out, emitted
}

func unansweredOn(j *Judge, host string, port uint16, server string, start int64) (Nomination, bool) {
	f := flowTo(port, server)
	j.Hint(f.Client, host, []netip.Addr{f.Server}, at(start))
	return synTimes(j, f, start+1, start+3)
}

func TestCloseBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		received uint32
		age      int64
		want     bool
	}{
		{name: "below byte window", received: StallMinBytes - 1, age: 10},
		{name: "minimum bytes and age", received: StallMinBytes, age: 10, want: true},
		{name: "middle of window", received: 16384, age: 11, want: true},
		{name: "maximum bytes and age", received: StallMaxBytes, age: 10, want: true},
		{name: "above byte window", received: StallMaxBytes + 1, age: 10},
		{name: "too young", received: 16384, age: 9},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := New()
			if out, ok := stalledAt(j, "freeze.test", 50001, 100); ok {
				t.Fatalf("first note nominated: %+v", out)
			}
			f := flowTo(50002, "203.0.114.7")
			out, ok := stalledFlow(j, f, "freeze.test", 1000, 1000+tc.received, 150, tc.age)
			if ok != tc.want {
				t.Fatalf("emitted=%v, want %v (%+v)", ok, tc.want, out)
			}
			if ok && (out.Kind != NominationStall || out.Host != "freeze.test" || out.Received != tc.received || out.Domain != "") {
				t.Fatalf("unexpected nomination: %+v", out)
			}
		})
	}
}

func TestCloseInstantThresholdUsesFirstProvenStall(t *testing.T) {
	j := NewWithParams(Params{StallNotesThreshold: 1})
	out, ok := stalledAt(j, "freeze.test", 50001, 100)
	if !ok || out.Kind != NominationStall || out.Host != "freeze.test" {
		t.Fatalf("first proven stall was not nominated: %+v, %v", out, ok)
	}
}

func TestCloseWrapWindowAndDeduplication(t *testing.T) {
	t.Run("ack wraps modulo 2^32", func(t *testing.T) {
		j := New()
		f1 := flowTo(50001, "203.0.114.7")
		f2 := flowTo(50002, "203.0.114.7")
		stalledFlow(j, f1, "freeze.test", 0xfffff000, 0x00003000, 100, 11)
		out, ok := stalledFlow(j, f2, "freeze.test", 0xfffff000, 0x00003000, 150, 11)
		if !ok || out.Received != 0x4000 {
			t.Fatalf("wrapped close: %+v, %v", out, ok)
		}
	})

	for _, tc := range []struct {
		gap  int64
		want bool
	}{{120, true}, {121, false}} {
		t.Run(timeWindowName(tc.gap), func(t *testing.T) {
			j := New()
			stalledFlow(j, flowTo(50001, "203.0.114.7"), "freeze.test", 1000, 17384, 100, 10)
			out, ok := stalledFlow(j, flowTo(50002, "203.0.114.7"), "freeze.test", 1000, 17384, 100+tc.gap, 10)
			if ok != tc.want {
				t.Fatalf("gap %d: emitted=%v, want %v (%+v)", tc.gap, ok, tc.want, out)
			}
		})
	}

	t.Run("FIN is examined once and nomination clears notes", func(t *testing.T) {
		j := New()
		f := flowTo(50001, "203.0.114.7")
		stalledFlow(j, f, "freeze.test", 1000, 17384, 100, 11)
		if out, ok := j.Close(f, 17384, at(112)); ok {
			t.Fatalf("duplicate FIN nominated: %+v", out)
		}
		out, ok := stalledAt(j, "freeze.test", 50002, 150)
		if !ok || out.Host != "freeze.test" {
			t.Fatalf("two distinct flows did not nominate: %+v, %v", out, ok)
		}
		if out, ok := stalledAt(j, "freeze.test", 50003, 170); ok {
			t.Fatalf("notes were not cleared: %+v", out)
		}
	})

	t.Run("bypass and SYN closes do not note", func(t *testing.T) {
		j := New()
		bypass := testFlow(50001)
		j.Hello(bypass, 7, 1000, "freeze.test", at(100))
		if out, ok := j.Close(bypass, 17384, at(111)); ok {
			t.Fatalf("bypass close nominated: %+v", out)
		}
		syn := testFlow(50002)
		j.Hint(syn.Client, "freeze.test", []netip.Addr{syn.Server}, at(100))
		j.SYN(syn, 999, at(100))
		if out, ok := j.Close(syn, 17384, at(120)); ok {
			t.Fatalf("SYN close nominated: %+v", out)
		}
		if got := len(j.Addresses(at(120))); got != 0 {
			t.Fatalf("closes created %d address rows", got)
		}
	})
}

func timeWindowName(gap int64) string {
	if gap == 120 {
		return "notes exactly 120 seconds apart pair"
	}
	return "notes 121 seconds apart do not pair"
}

func TestSYNTransitions(t *testing.T) {
	t.Run("two second retransmission threshold", func(t *testing.T) {
		for _, tc := range []struct {
			delay int64
			want  bool
		}{{1, false}, {2, true}} {
			j := NewWithParams(Params{FailThreshold: 1})
			f := flowTo(50001, "203.0.114.1")
			j.Hint(f.Client, "only.example", []netip.Addr{f.Server}, at(100))
			j.SYN(f, 999, at(101))
			out, ok := j.SYN(f, 999, at(101+tc.delay))
			if ok != tc.want {
				t.Errorf("delay %d: emitted=%v, want %v", tc.delay, ok, tc.want)
			}
			if ok && (out.Kind != NominationNoConnect || out.Host != "only.example" || out.Bypass) {
				t.Errorf("delay %d: unexpected nomination %+v", tc.delay, out)
			}
		}
	})

	t.Run("one no-connect per flow and host restart", func(t *testing.T) {
		j := NewWithParams(Params{FailThreshold: 2})
		f := flowTo(50001, "203.0.114.1")
		j.Hint(f.Client, "web.telegram.org", []netip.Addr{f.Server}, at(100))
		if out, ok := synTimes(j, f, 100, 102, 104, 106); ok {
			t.Fatalf("same SYN flow counted more than once: %+v", out)
		}
		out, ok := unansweredOn(j, "web.telegram.org", 50002, "203.0.114.1", 110)
		if !ok || out.Host != "web.telegram.org" {
			t.Fatalf("second flow did not nominate: %+v, %v", out, ok)
		}
		if out, ok := unansweredOn(j, "web.telegram.org", 50003, "203.0.114.1", 120); ok {
			t.Fatalf("nominated host did not restart: %+v", out)
		}
	})

	t.Run("default three no-connect threshold", func(t *testing.T) {
		j := New()
		for i := 0; i < 2; i++ {
			if out, ok := unansweredOn(j, "www.instagram.com", uint16(50001+i), "203.0.114.1", 100+int64(i)*10); ok {
				t.Fatalf("early nomination %d: %+v", i, out)
			}
		}
		out, ok := unansweredOn(j, "www.instagram.com", 50003, "203.0.114.1", 120)
		if !ok || out.Host != "www.instagram.com" || out.Domain != "" || out.Received != 0 {
			t.Fatalf("third no-connect: %+v, %v", out, ok)
		}
	})

	t.Run("HELLO ends SYN state", func(t *testing.T) {
		j := NewWithParams(Params{FailThreshold: 1})
		connected := flowTo(50001, "203.0.114.1")
		j.Hint(connected.Client, "x.example", []netip.Addr{connected.Server}, at(90))
		j.SYN(connected, 999, at(100))
		j.SYN(connected, 999, at(101))
		j.Hello(connected, 1000, 0, "x.example", at(102))
		if out, ok := j.SYN(connected, 999, at(103)); ok {
			t.Fatalf("late SYN after HELLO nominated: %+v", out)
		}
		silent := flowTo(50002, "203.0.114.1")
		j.Hint(silent.Client, "x.example", []netip.Addr{silent.Server}, at(90))
		if out, ok := synTimes(j, silent, 100, 101, 103); !ok {
			t.Fatal("unanswered SYN flow did not nominate")
		} else if out.Host != "x.example" {
			t.Fatalf("wrong host: %+v", out)
		}
	})

	t.Run("late SYN keeps connected HELLO", func(t *testing.T) {
		j := New()
		f := testFlow(50000)
		j.Hint(f.Client, "late.example", []netip.Addr{f.Server}, at(100))
		j.Hello(f, 1000, 0, "late.example", at(100))
		j.SYN(f, 999, at(101))
		var out Failure
		var ok bool
		for i := int64(102); i <= 104; i++ {
			out, ok = j.Hello(f, 1000, 0, "late.example", at(i))
		}
		if !ok || out.Host != "late.example" {
			t.Fatalf("late SYN disrupted HELLO: %+v, %v", out, ok)
		}
	})
}

func TestSYNBypassAndAmbiguous(t *testing.T) {
	t.Run("no hint is bypass with address row", func(t *testing.T) {
		j := NewWithParams(Params{FailThreshold: 1})
		f := flowTo(50001, "203.0.114.1")
		out, ok := synTimes(j, f, 101, 103)
		if !ok || !out.Bypass || out.Kind != NominationNoConnect || out.Server != f.Server || out.Client != f.Client {
			t.Fatalf("SYN bypass: %+v, %v", out, ok)
		}
		rows := j.Addresses(at(103))
		if len(rows) != 1 || rows[0].Why != WhyNoConnect || rows[0].Name != "" || rows[0].Fails != 1 {
			t.Fatalf("SYN bypass rows: %+v", rows)
		}
	})

	t.Run("unrelated names are ambiguous not bypass", func(t *testing.T) {
		j := NewWithParams(Params{FailThreshold: 1})
		f := flowTo(50001, "203.0.114.1")
		j.Hint(f.Client, "one.example", []netip.Addr{f.Server}, at(90))
		j.Hint(f.Client, "two.example", []netip.Addr{f.Server}, at(91))
		if out, ok := synTimes(j, f, 101, 103); ok {
			t.Fatalf("ambiguous SYN emitted: %+v", out)
		}
		if j.AmbiguousDay(at(103)) != 1 || len(j.Addresses(at(103))) != 0 {
			t.Fatal("ambiguous SYN accounting mismatch")
		}
	})
}

func TestNoConnectAndFailuresAreIndependent(t *testing.T) {
	j := New()
	unansweredOn(j, "x.example", 50001, "203.0.114.1", 100)
	unansweredOn(j, "x.example", 50002, "203.0.114.1", 110)
	out := mustFailureAt(t, j, flowTo(50003, "203.0.114.1"), "x.example", 115)
	if out.Listed || out.Fails != 1 {
		t.Fatalf("no-connect contaminated failures: %+v", out)
	}
	nom, ok := unansweredOn(j, "x.example", 50004, "203.0.114.1", 120)
	if !ok || nom.Host != "x.example" || nom.Domain != "" {
		t.Fatalf("third independent no-connect did not nominate host: %+v, %v", nom, ok)
	}
}
