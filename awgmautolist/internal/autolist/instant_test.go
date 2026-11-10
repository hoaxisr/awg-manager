package autolist

import (
	"context"
	"testing"
)

func TestInstantModeUsesFirstLogFailure(t *testing.T) {
	t.Run("default keeps three-failure threshold", func(t *testing.T) {
		awgm := newFakeAWGM(t)
		eng := NewEngine(awgm.srv.URL, "")
		awgm.pushDialLog("instant.example", "443")
		eng.tick(context.Background())
		if rows := eng.table.List(); len(rows) != 0 {
			t.Fatalf("default mode nominated after one failure: %+v", rows)
		}
	})

	t.Run("instant nominates and queues immediately", func(t *testing.T) {
		awgm := newFakeAWGM(t)
		eng := NewEngine(awgm.srv.URL, "")
		if err := eng.SetConfig(Config{Instant: true}); err != nil {
			t.Fatal(err)
		}
		awgm.pushDialLog("instant.example", "443")
		eng.tick(context.Background())
		rows := eng.table.List()
		if len(rows) != 1 || rows[0].Domain != "instant.example" {
			t.Fatalf("instant candidate = %+v", rows)
		}
		if got := eng.Snapshot()["probesQueued"]; got != 1 {
			t.Fatalf("first instant probe was not queued: %v", got)
		}
	})
}
