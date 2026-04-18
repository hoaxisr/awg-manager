package cache

import "sync"

// SingleFlight coalesces concurrent callers asking for the same key: one
// goroutine executes fn, the rest wait on its result. The entry is removed
// once fn returns — a later Do triggers a fresh execution.
type SingleFlight[K comparable, V any] struct {
	mu       sync.Mutex
	inFlight map[K]*sfCall[V]
}

type sfCall[V any] struct {
	wg    sync.WaitGroup
	value V
	err   error
}

// NewSingleFlight creates an empty coalescer.
func NewSingleFlight[K comparable, V any]() *SingleFlight[K, V] {
	return &SingleFlight[K, V]{inFlight: make(map[K]*sfCall[V])}
}

// Do executes fn for k. Concurrent callers for the same k share the result.
func (s *SingleFlight[K, V]) Do(k K, fn func() (V, error)) (V, error) {
	s.mu.Lock()
	if call, ok := s.inFlight[k]; ok {
		s.mu.Unlock()
		call.wg.Wait()
		return call.value, call.err
	}
	call := &sfCall[V]{}
	call.wg.Add(1)
	s.inFlight[k] = call
	s.mu.Unlock()

	call.value, call.err = fn()
	call.wg.Done()

	s.mu.Lock()
	delete(s.inFlight, k)
	s.mu.Unlock()

	return call.value, call.err
}
