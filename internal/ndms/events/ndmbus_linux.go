//go:build linux

package events

import (
	"errors"
	"net"
	"time"
)

// Start запускает горутину клиента и сразу возвращается: сокета может не
// быть (ndm ещё грузится) — это не ошибка, клиент подключается с backoff, а
// до подключения потребитель считает шину отключённой (его дефолт).
func (r *BusReader) Start() error {
	r.stop = make(chan struct{})
	r.done = make(chan struct{})
	go r.loop()
	return nil
}

// Stop закрывает соединение (Read возвращает ошибку) и дожидается горутины.
func (r *BusReader) Stop() {
	if r.done == nil {
		return
	}
	close(r.stop)
	r.mu.Lock()
	if r.conn != nil {
		r.conn.Close()
	}
	r.mu.Unlock()
	<-r.done
	r.done = nil
}

func (r *BusReader) loop() {
	defer close(r.done)
	backoff := r.backoffMin
	for {
		conn, err := net.Dial("unix", r.path)
		if err != nil {
			if !r.sleep(backoff) {
				return
			}
			backoff = min(backoff*2, r.backoffMax)
			continue
		}
		r.mu.Lock()
		select {
		case <-r.stop:
			r.mu.Unlock()
			conn.Close()
			return
		default:
		}
		r.conn = conn
		r.mu.Unlock()
		backoff = r.backoffMin

		r.onState(true)
		p := newBusParser(conn)
		for {
			ev, err := p.next()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					r.log.Warnf("ndm bus: %v — переподключение", err)
				}
				break
			}
			r.onEvent(ev)
		}
		conn.Close()
		r.mu.Lock()
		r.conn = nil
		r.mu.Unlock()
		select {
		case <-r.stop: // своя остановка — не обрыв шины: без onState(false)
			return
		default:
		}
		r.onState(false)
		if !r.sleep(backoff) {
			return
		}
	}
}

// sleep — пауза backoff; false — пришёл Stop.
func (r *BusReader) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-r.stop:
		return false
	case <-t.C:
		return true
	}
}
