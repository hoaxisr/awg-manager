package lock

import (
	"sync"
	"sync/atomic"
	"testing"
)

// F295: os.WriteFile создаёт pid-файл пустым и лишь потом пишет pid, а
// cleanStale сносит каталог с неразборным pid сразу — ждущий, попавший в это
// окно, снимал живой лок и брал его. Двое держателей одновременно = провал.
func TestTryLock_NoTwoHoldersWhilePidIsWritten(t *testing.T) {
	dir := t.TempDir()
	var holders, overlaps atomic.Int32
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := NewWithDir("tunnels", dir)
			for got := 0; got < 2000; {
				if l.TryLock() != nil {
					continue
				}
				got++
				if holders.Add(1) > 1 {
					overlaps.Add(1)
				}
				holders.Add(-1)
				l.Unlock()
			}
		}()
	}
	wg.Wait()
	if n := overlaps.Load(); n != 0 {
		t.Fatalf("лок одновременно держали двое: %d раз", n)
	}
}
