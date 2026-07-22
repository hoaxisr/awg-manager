package mobile

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/stats"
)

type sessionTraffic struct {
	stats  *stats.Stats
	txRate atomic.Int64
	rxRate atomic.Int64
}

func (s *sessionTraffic) rateMeter(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var prevTx, prevRx uint64
	for {
		select {
		case <-ctx.Done():
			s.txRate.Store(0)
			s.rxRate.Store(0)
			return
		case <-t.C:
			tx, rx := s.stats.Counters()
			s.txRate.Store(clampToInt64(tx - prevTx))
			s.rxRate.Store(clampToInt64(rx - prevRx))
			prevTx, prevRx = tx, rx
		}
	}
}
