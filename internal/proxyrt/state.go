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
	// pubMu задаёт порядок публикаций и берётся ПЕРЕД mu. Отдельным локом, а не
	// расширением mu: держать лок состояния на время колбэка подписчиков —
	// приглашение к взаимоблокировке, если подписчик позовёт Get.
	pubMu sync.Mutex
	mu    sync.RWMutex
	m     map[string]InstanceState
	pub   Publisher
	now   func() time.Time
}

func NewStateStore(pub Publisher, now func() time.Time) *StateStore {
	if now == nil {
		now = time.Now
	}
	return &StateStore{m: make(map[string]InstanceState), pub: pub, now: now}
}

// Update кладёт новое состояние и публикует его, если оно изменилось.
//
// Намерение берётся из результата прогона, отдельным аргументом не приходит:
// пара «намерение + фаза» обязана быть той же, по которой цикл считал. Читать
// намерение заново после прогона значит публиковать пару, которой не было:
// {disabled, settled} либо {enabled, disabled}.
//
// Переданные слайсы хранилище кладёт к себе как есть, копии на входе не делает:
// на mipsel копия каждого прогона не окупается. Поэтому требование к
// вызывающему: ни шаги и состояния ресурсов, ни КАРТЫ Step.Args после возврата
// не менять. Роль, переиспользующая одну карту аргументов между проходами,
// испортит хранимое состояние, оно совпадёт со следующим прогоном, и публикация
// подавится молча.
//
// Порядок публикаций строгий: события уходят на шину в том же порядке, в каком
// состояния легли в хранилище. Это держится на pubMu, который берётся первым и
// не отпускается до конца вызова. Без него два одновременных Update по одному
// инстансу — воркер и ручка API, снимающая инстанс, — могли бы опубликовать
// новое состояние раньше старого, и фронт застрял бы на протухшем до следующего
// события.
func (s *StateStore) Update(id string, res Result, phase Phase) InstanceState {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()

	st := InstanceState{
		ID:        id,
		Intent:    res.Intent,
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
	// Возврат — такая же выдача наружу, как Get и List: правка на месте не должна
	// доезжать до хранилища. Порча хранимого состояния хуже гонки — она совпадёт
	// со следующим настоящим прогоном, sameState подавит публикацию, и потеря
	// станет липкой.
	return clone(st)
}

func (s *StateStore) Get(id string) (InstanceState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.m[id]
	if !ok {
		return InstanceState{}, false
	}
	return clone(st), true
}

func (s *StateStore) List() []InstanceState {
	s.mu.RLock()
	out := make([]InstanceState, 0, len(s.m))
	for _, st := range s.m {
		out = append(out, clone(st))
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// clone отвязывает выдаваемое состояние от хранимого. Мьютекс защищает карту, а
// не содержимое слайсов: правка на месте — сортировка ресурсов в ручке API —
// портила бы хранилище и давала гонку данных, которую -race внутри пакета не
// видит.
//
// Args копируется тоже: копия одного слайса оставила бы карту общей, то есть
// защиту, которой нет.
func clone(st InstanceState) InstanceState {
	if st.Resources != nil {
		st.Resources = append([]ResourceState(nil), st.Resources...)
	}
	if st.LastPlan != nil {
		steps := append([]Step(nil), st.LastPlan...)
		for i := range steps {
			if steps[i].Args == nil {
				continue
			}
			args := make(map[string]string, len(steps[i].Args))
			for k, v := range steps[i].Args {
				args[k] = v
			}
			steps[i].Args = args
		}
		st.LastPlan = steps
	}
	return st
}

// sameState сравнивает всё, кроме отметки времени: она меняется каждый прогон
// и сама по себе поводом для публикации не является. Step содержит карту и
// потому несравним через ==, поэтому идём по полям.
//
// Причина шага сравнивается отдельно от StepKey: ключ намеренно кодирует только
// Resource/Op/Args — этого хватает на вопрос «этот шаг уже применяли», но здесь
// вопрос другой, «изменилось ли то, что видит пользователь», а причина ему
// показывается.
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
		if a.LastPlan[i].Reason != b.LastPlan[i].Reason {
			return false
		}
		if StepKey(a.LastPlan[i]) != StepKey(b.LastPlan[i]) {
			return false
		}
	}
	return true
}
