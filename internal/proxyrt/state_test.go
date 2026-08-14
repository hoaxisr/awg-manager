package proxyrt

import (
	"testing"
	"time"
)

type fakePublisher struct {
	events []string
	last   any
}

func (f *fakePublisher) Publish(eventType string, data any) {
	f.events = append(f.events, eventType)
	f.last = data
}

func fixedNow() time.Time { return time.Unix(1700000000, 0).UTC() }

func TestStateStoreUpdatePublishesOnChange(t *testing.T) {
	pub := &fakePublisher{}
	st := NewStateStore(pub, fixedNow)

	res := Result{
		Steps:  []Step{{Resource: "a", Op: "create", Reason: "нужно"}},
		States: []ResourceState{{ID: "a", Status: StatusOK}},
	}
	got := st.Update("inst1", IntentEnabled, res, PhaseSettled)

	if got.Phase != PhaseSettled || got.ID != "inst1" {
		t.Fatalf("состояние собрано неверно: %+v", got)
	}
	if len(got.LastPlan) != 1 {
		t.Fatal("последний план обязан быть публичным")
	}
	if !got.UpdatedAt.Equal(fixedNow()) {
		t.Fatalf("UpdatedAt = %v", got.UpdatedAt)
	}
	if len(pub.events) != 1 || pub.events[0] != EventInstanceState {
		t.Fatalf("публикации %v", pub.events)
	}
}

func TestStateStoreDoesNotRepublishIdenticalState(t *testing.T) {
	// Реконсиляция идёт по каждому событию; одинаковое состояние не должно
	// заливать шину и фронт.
	pub := &fakePublisher{}
	st := NewStateStore(pub, fixedNow)
	res := Result{States: []ResourceState{{ID: "a", Status: StatusOK}}}

	st.Update("inst1", IntentEnabled, res, PhaseSettled)
	st.Update("inst1", IntentEnabled, res, PhaseSettled)

	if len(pub.events) != 1 {
		t.Fatalf("публикаций %d, ожидали 1 — повтор не публикуется", len(pub.events))
	}
}

func TestStateStorePublishesWhenPhaseChanges(t *testing.T) {
	pub := &fakePublisher{}
	st := NewStateStore(pub, fixedNow)
	res := Result{States: []ResourceState{{ID: "a", Status: StatusOK}}}

	st.Update("inst1", IntentEnabled, res, PhaseWaiting)
	st.Update("inst1", IntentEnabled, res, PhaseSettled)

	if len(pub.events) != 2 {
		t.Fatalf("публикаций %d, ожидали 2: фаза изменилась", len(pub.events))
	}
}

func TestStateStoreGetAndList(t *testing.T) {
	st := NewStateStore(&fakePublisher{}, fixedNow)
	st.Update("b", IntentEnabled, Result{}, PhaseSettled)
	st.Update("a", IntentDisabled, Result{}, PhaseDisabled)

	if _, ok := st.Get("a"); !ok {
		t.Fatal("инстанс a не найден")
	}
	list := st.List()
	if len(list) != 2 || list[0].ID != "a" || list[1].ID != "b" {
		t.Fatalf("список не отсортирован по id: %+v", list)
	}
}
