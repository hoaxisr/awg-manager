package uplink

import (
	"context"
	"sort"
	"sync"
)

// Порядок захвата (обратного пути нет нигде):
//
//	gate (разделяемо/исключительно) → замки ref по возрастанию → замки
//	адаптера и оркестратора (WithTunnelLock) → probeSlot → stateMu, mu.
//
// stateMu и mu — листовые: под ними нет I/O и не берётся ничего другого.
// Под исключительным gate (смена движка, переименование цели, пауза бэкапа)
// нельзя звать то, что берёт gate разделяемо: такт, Put, страж туннеля
// политики — замок нереентерабелен.

// opGate — разделяемо-исключительный замок с ctx. Ждущий исключительного
// закрывает вход новым разделяемым, поэтому поток проб его не заморит.
type opGate struct {
	mu      sync.Mutex
	shared  int
	excl    bool
	queued  int
	changed chan struct{}
}

// await вызывается и возвращается под g.mu.
func (g *opGate) await(ctx context.Context, ready func() bool) error {
	for !ready() {
		if g.changed == nil {
			g.changed = make(chan struct{})
		}
		ch := g.changed
		g.mu.Unlock()
		select {
		case <-ch:
			g.mu.Lock()
		case <-ctx.Done():
			g.mu.Lock()
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func (g *opGate) wake() {
	if g.changed != nil {
		close(g.changed)
		g.changed = nil
	}
}

func (g *opGate) lockShared(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.await(ctx, func() bool { return !g.excl && g.queued == 0 }); err != nil {
		return err
	}
	g.shared++
	return nil
}

func (g *opGate) unlockShared() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.shared--
	if g.shared == 0 {
		g.wake()
	}
}

func (g *opGate) lockExclusive(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.queued++
	err := g.await(ctx, func() bool { return !g.excl && g.shared == 0 })
	g.queued--
	if err != nil {
		// Снятая очередь могла открыть вход разделяемым.
		g.wake()
		return err
	}
	g.excl = true
	return nil
}

func (g *opGate) unlockExclusive() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.excl = false
	g.wake()
}

// refLock — семафор одного ref. Запись не удаляется: ref'ов столько,
// сколько их когда-либо было у политик. sync.Map — чтобы быстрый путь
// стража не брал никаких мьютексов контроллера.
func (c *Controller) refLock(ref string) chan struct{} {
	if ch, ok := c.refs.Load(ref); ok {
		return ch.(chan struct{})
	}
	ch, _ := c.refs.LoadOrStore(ref, make(chan struct{}, 1))
	return ch.(chan struct{})
}

// lockRefs берёт gate разделяемо и замки ref по возрастанию; ожидание
// прерывается ctx. Освобождение идемпотентно.
func (c *Controller) lockRefs(ctx context.Context, refs ...string) (func(), error) {
	sorted := append([]string(nil), refs...)
	sort.Strings(sorted)
	if err := c.gate.lockShared(ctx); err != nil {
		return nil, err
	}
	var held []chan struct{}
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			<-held[i]
		}
		c.gate.unlockShared()
	}
	for i, ref := range sorted {
		if i > 0 && ref == sorted[i-1] {
			continue
		}
		ch := c.refLock(ref)
		select {
		case ch <- struct{}{}:
			held = append(held, ch)
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
	var once sync.Once
	return func() { once.Do(release) }, nil
}

// acquireProbe держит «одну пробу за раз» на маленьком роутере. Ждёт по
// родительскому ctx: время ожидания не съедает таймаут самой пробы.
func (c *Controller) acquireProbe(ctx context.Context) (func(), error) {
	select {
	case c.probeSlot <- struct{}{}:
		return func() { <-c.probeSlot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
