package transport

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSemaphore_LimitsConcurrency(t *testing.T) {
	sem := NewSemaphore(2)
	ctx := context.Background()

	var inFlight, peak int32
	release := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sem.Acquire(ctx); err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			cur := atomic.AddInt32(&inFlight, 1)
			for {
				p := atomic.LoadInt32(&peak)
				if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
					break
				}
			}
			<-release
			atomic.AddInt32(&inFlight, -1)
			sem.Release()
		}()
	}

	// Let goroutines attempt to grab slots.
	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(&peak); got != 2 {
		t.Errorf("peak in-flight: want 2, got %d", got)
	}
	close(release)
	wg.Wait()
}

func TestSemaphore_AcquireRespectsContextCancel(t *testing.T) {
	sem := NewSemaphore(1)
	ctx := context.Background()
	if err := sem.Acquire(ctx); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	// Second acquire should block; cancel its ctx to unblock with ctx err.
	ctx2, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := sem.Acquire(ctx2)
	if err == nil {
		t.Fatalf("Acquire under cancelled ctx: want error, got nil")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("Acquire ctx err: want DeadlineExceeded, got %v", err)
	}
	sem.Release()
}

// withBackstop подменяет страховку на время теста: с прод-значением 60 с
// проверить её нечем.
func withBackstop(t *testing.T, d time.Duration) {
	t.Helper()
	prev := semAcquireBackstop
	semAcquireBackstop = d
	t.Cleanup(func() { semAcquireBackstop = prev })
}

// Ожидание слота без собственного дедлайна ОБЯЗАНО кончиться: boot-пути и
// фоновые циклы зовут транспорт с context.Background(), и без страховки они
// встают в очередь за подвисшим запросом навсегда. Мутация «звать голый
// Acquire» держала тест зелёным — ждать минуту он не станет, — поэтому
// константа стала переменной.
func TestSemaphore_BackstopBoundsDeadlinelessWait(t *testing.T) {
	withBackstop(t, 30*time.Millisecond)
	s := NewSemaphore(1)
	if err := s.Acquire(context.Background()); err != nil { // занимаем единственный слот
		t.Fatalf("seed acquire: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.acquireWithBackstop(context.Background()) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ждали DeadlineExceeded от страховки, получили %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ожидание слота без дедлайна не кончилось: страховки нет")
	}
}

// Свой дедлайн вызывающего уважается КАК ЕСТЬ — страховка его не подменяет.
// Иначе запрос с осознанно длинным ожиданием обрывался бы на шестидесятой
// секунде, а тест на этом ничего бы не заметил: обе формы отдают
// DeadlineExceeded, различает их только момент.
func TestSemaphore_BackstopDoesNotShortenOwnDeadline(t *testing.T) {
	withBackstop(t, 20*time.Millisecond)
	s := NewSemaphore(1)
	if err := s.Acquire(context.Background()); err != nil {
		t.Fatalf("seed acquire: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := s.acquireWithBackstop(ctx)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ждали DeadlineExceeded, получили %v", err)
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("ожидание оборвано через %v — страховка подменила дедлайн вызывающего", elapsed)
	}
}
