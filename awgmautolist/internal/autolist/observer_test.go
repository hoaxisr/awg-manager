package autolist

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// autolistConn кладёт один direct TCP/443 срез в fake Clash API.
func autolistConn(f *fakeAWGM, id, host string, down int64, age time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns = append(f.conns, map[string]any{
		"id": id,
		"metadata": map[string]any{
			"network": "tcp", "sourceIP": "192.168.31.10",
			"destinationIP": "203.0.113.5", "destinationPort": "443", "host": host,
		},
		"upload":   20000,
		"download": down,
		"start":    time.Now().Add(-age).UTC().Format(time.RFC3339),
		"outbound": "direct",
		"chains":   []string{"direct"},
	})
}

// autolistClose проводит соединение через два среза: открыто -> исчезло (FIN).
func autolistClose(f *fakeAWGM, obs *Observer, id, host string, down int64) []Observation {
	f.resetConns()
	autolistConn(f, id, host, down, 30*time.Second)
	if out := obs.PollConnections(context.Background()); len(out) != 0 {
		return []Observation{{Why: Why(-1)}}
	}
	f.resetConns()
	return obs.PollConnections(context.Background())
}

func TestAutolistStallNeedsTwoClosedConnections(t *testing.T) {
	f := newFakeAWGM(t)
	obs := NewObserver(f.srv.URL, "")

	if out := autolistClose(f, obs, "F1", "frozen.example", 16384); len(out) != 0 {
		t.Fatalf("одна FIN-нота не должна номинировать: %v", out)
	}
	out := autolistClose(f, obs, "F2", "frozen.example", 16384)
	if len(out) != 1 || out[0].Why != WhyFreeze || out[0].Host.Name != "frozen.example" {
		t.Fatalf("две FIN-ноты должны дать freeze: %+v", out)
	}

	// autolist очищает ноты после номинации: следующее одно закрытие — ещё не огонь.
	if out := autolistClose(f, obs, "F3", "frozen.example", 16384); len(out) != 0 {
		t.Fatalf("счётчик не перезапущен после номинации: %v", out)
	}
}

func TestAutolistStallByteBoundaries(t *testing.T) {
	cases := []struct {
		name string
		down int64
		want bool
	}{
		{"below", StallMinUplink - 1, false},
		{"min-inclusive", StallMinUplink, true},
		{"max-inclusive", StallMaxBytes, true},
		{"above", StallMaxBytes + 1, false},
		{"empty", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAWGM(t)
			obs := NewObserver(f.srv.URL, "")
			_ = autolistClose(f, obs, "F1", "bytes.example", tc.down)
			out := autolistClose(f, obs, "F2", "bytes.example", tc.down)
			if (len(out) == 1) != tc.want {
				t.Fatalf("download=%d: out=%v, want nomination=%v", tc.down, out, tc.want)
			}
		})
	}
}

func TestAutolistStallRequiresTenSeconds(t *testing.T) {
	f := newFakeAWGM(t)
	obs := NewObserver(f.srv.URL, "")
	for i := 0; i < StallNotesThreshold; i++ {
		f.resetConns()
		autolistConn(f, fmt.Sprint(i), "short.example", 16384, (StallMinSecs-1)*time.Second)
		_ = obs.PollConnections(context.Background())
		f.resetConns()
		if out := obs.PollConnections(context.Background()); len(out) != 0 {
			t.Fatalf("соединение короче 10 секунд не должно считаться: %v", out)
		}
	}
}

func TestAutolistStallWindowSlidesFromLastNote(t *testing.T) {
	f := newFakeAWGM(t)
	obs := NewObserver(f.srv.URL, "")
	now := time.Now()
	obs.now = func() time.Time { return now }

	_ = autolistClose(f, obs, "F1", "window.example", 16384)
	now = now.Add((StallNoteWindow + 1) * time.Second)
	if out := autolistClose(f, obs, "F2", "window.example", 16384); len(out) != 0 {
		t.Fatalf("ноты с паузой больше 120 секунд не суммируются: %v", out)
	}
	now = now.Add(time.Second)
	if out := autolistClose(f, obs, "F3", "window.example", 16384); len(out) != 1 {
		t.Fatalf("две ноты внутри скользящих 120 секунд должны номинировать: %v", out)
	}
}

func TestAutolistStallsAggregateByRegisteredDomain(t *testing.T) {
	f := newFakeAWGM(t)
	obs := NewObserver(f.srv.URL, "")
	if out := autolistClose(f, obs, "F1", "a.freeze.test", 16384); len(out) != 0 {
		t.Fatalf("первая нота: %v", out)
	}
	out := autolistClose(f, obs, "F2", "b.freeze.test", 16384)
	if len(out) != 1 {
		t.Fatalf("ноты разных хостов одного домена должны суммироваться: %v", out)
	}
}

func TestAutolistDialSameTimestampBatch(t *testing.T) {
	f := newFakeAWGM(t)
	obs := NewObserver(f.srv.URL, "")
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	for _, host := range []string{"a.same.example", "b.same.example", "c.same.example"} {
		f.logsToGive = append(f.logsToGive, map[string]any{
			"timestamp": ts,
			"message":   fmt.Sprintf("connection: open connection to %s:443 using outbound/direct[direct]: dial tcp %s:443: i/o timeout", host, host),
		})
	}
	if out := obs.PollLogs(context.Background()); len(out) != 1 {
		t.Fatalf("одинаковый timestamp внутри пачки не должен съедать отказы: %v", out)
	}
}

func TestAutolistDialFailuresAggregateByDomainAndRestart(t *testing.T) {
	f := newFakeAWGM(t)
	obs := NewObserver(f.srv.URL, "")
	hosts := []string{"a.dead.example", "b.dead.example", "c.dead.example"}
	var out []Observation
	for i, host := range hosts {
		f.pushDialLog(host, "443")
		out = obs.PollLogs(context.Background())
		if i < 2 && len(out) != 0 {
			t.Fatalf("отказ %d пришёл раньше порога: %v", i+1, out)
		}
	}
	if len(out) != 1 || out[0].Why != WhyNoConnect {
		t.Fatalf("три no-connect домена должны номинировать: %+v", out)
	}

	// После номинации нужны три новых отказа, а не событие на каждый следующий лог.
	f.pushDialLog("d.dead.example", "443")
	if out := obs.PollLogs(context.Background()); len(out) != 0 {
		t.Fatalf("no-connect не перезапущен после номинации: %v", out)
	}
}

func TestObserverMemoryIsBounded(t *testing.T) {
	now := time.Now()
	tallies := map[string]*tally{}
	for i := 0; i < HostsCap+100; i++ {
		bump(tallies, fmt.Sprintf("h%d.example", i), 1000000, time.Hour, now)
	}
	if len(tallies) != HostsCap {
		t.Fatalf("tally capacity = %d, want %d", len(tallies), HostsCap)
	}
	obs := NewObserver("http://127.0.0.1:1", "")
	for i := 0; i < HostsCap+100; i++ {
		obs.addHint(fmt.Sprintf("203.0.%d.%d", i/250, i%250+1), fmt.Sprintf("h%d.example", i), now)
	}
	if len(obs.hints) != HostsCap {
		t.Fatalf("hint capacity = %d, want %d", len(obs.hints), HostsCap)
	}
}
