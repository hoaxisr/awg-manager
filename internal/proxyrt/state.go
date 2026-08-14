package proxyrt

import (
	"sort"
	"sync"
	"time"
)

// EventInstanceState — тип события на шине для фронта. Форма имени та же, что
// у существующих событий проекта (tunnel:state и прочие).
const EventInstanceState = "proxy:instance-state"

// Publisher — узкий срез events.Bus, чтобы движок не тянул зависимость.
type Publisher interface {
	Publish(eventType string, data any)
}

// StateStore держит последнее известное состояние инстансов и публикует
// изменения. Фаза здесь не вычисляется — она приходит из цикла, второго
// источника правды не заводим.
type StateStore struct {
	mu  sync.RWMutex
	m   map[string]InstanceState
	pub Publisher
	now func() time.Time
}

func NewStateStore(pub Publisher, now func() time.Time) *StateStore {
	if now == nil {
		now = time.Now
	}
	return &StateStore{m: make(map[string]InstanceState), pub: pub, now: now}
}

// Update кладёт новое состояние и публикует его, если оно изменилось.
func (s *StateStore) Update(id string, intent Intent, res Result, phase Phase) InstanceState {
	st := InstanceState{
		ID:        id,
		Intent:    intent,
		Phase:     phase,
		Resources: res.States,
		LastPlan:  res.Steps,
		UpdatedAt: s.now(),
	}

	s.mu.Lock()
	prev, had := s.m[id]
	changed := !had || !sameState(prev, st)
	s.m[id] = st
	s.mu.Unlock()

	if changed && s.pub != nil {
		s.pub.Publish(EventInstanceState, st)
	}
	return st
}

func (s *StateStore) Get(id string) (InstanceState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.m[id]
	return st, ok
}

func (s *StateStore) List() []InstanceState {
	s.mu.RLock()
	out := make([]InstanceState, 0, len(s.m))
	for _, st := range s.m {
		out = append(out, st)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// sameState сравнивает всё, кроме отметки времени: она меняется каждый прогон
// и сама по себе поводом для публикации не является. Step содержит карту и
// потому несравним через ==, поэтому идём по полям.
func sameState(a, b InstanceState) bool {
	if a.Intent != b.Intent || a.Phase != b.Phase {
		return false
	}
	if len(a.Resources) != len(b.Resources) || len(a.LastPlan) != len(b.LastPlan) {
		return false
	}
	for i := range a.Resources {
		if a.Resources[i] != b.Resources[i] {
			return false
		}
	}
	for i := range a.LastPlan {
		if StepKey(a.LastPlan[i]) != StepKey(b.LastPlan[i]) {
			return false
		}
	}
	return true
}
