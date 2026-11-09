package autolist

import (
	"context"
	"testing"
	"time"
)

// TestOpenTarpitTriggersProbeOnly — открытое соединение никогда не создаёт
// FIN-ноту. Медленная доставка даёт отдельный сигнал только для активной пробы.
func TestOpenTarpitTriggersProbeOnly(t *testing.T) {
	f := newFakeAWGM(t)
	obs := NewObserver(f.srv.URL, "")
	now := time.Now()
	obs.now = func() time.Time { return now }

	autolistConn(f, "T1", "tarped.example", 10000, 60*time.Second)
	if out := obs.PollConnections(context.Background()); len(out) != 0 {
		t.Fatalf("первый срез: %v", out)
	}
	now = now.Add(15 * time.Second)
	f.resetConns()
	autolistConn(f, "T1", "tarped.example", 10000, 60*time.Second)
	out := obs.PollConnections(context.Background())
	if len(out) != 1 || !out[0].ProbeOnly || out[0].Why != WhyFreeze {
		t.Fatalf("открытый tarpit должен дать probe-only сигнал: %+v", out)
	}
	obs.AckOpen(out[0].ConnectionID)
	if len(obs.stall) != 0 {
		t.Fatalf("открытое соединение не должно создавать FIN-ноты: %+v", obs.stall)
	}
	now = now.Add(15 * time.Second)
	f.resetConns()
	autolistConn(f, "T1", "tarped.example", 10000, 75*time.Second)
	if out := obs.PollConnections(context.Background()); len(out) != 0 {
		t.Fatalf("то же connection ID повторно запустило проверку: %+v", out)
	}
	now = now.Add(15 * time.Second)
	f.resetConns()
	autolistConn(f, "T2", "tarped.example", 10000, 90*time.Second)
	if out := obs.PollConnections(context.Background()); len(out) != 0 {
		t.Fatalf("новый ID не должен проверяться до второго среза: %+v", out)
	}
	now = now.Add(15 * time.Second)
	f.resetConns()
	autolistConn(f, "T2", "tarped.example", 10000, 90*time.Second)
	if out := obs.PollConnections(context.Background()); len(out) != 1 {
		t.Fatalf("новое connection ID не получило новую проверку: %+v", out)
	}
}

func TestOpenHealthyRateDoesNotTriggerProbe(t *testing.T) {
	f := newFakeAWGM(t)
	obs := NewObserver(f.srv.URL, "")
	now := time.Now()
	obs.now = func() time.Time { return now }

	autolistConn(f, "L1", "live.example", 10000, 60*time.Second)
	_ = obs.PollConnections(context.Background())
	now = now.Add(15 * time.Second)
	f.resetConns()
	autolistConn(f, "L1", "live.example", 20000, 60*time.Second)
	if out := obs.PollConnections(context.Background()); len(out) != 0 {
		t.Fatalf("нормальный темп передачи не должен запускать пробу: %+v", out)
	}
}
