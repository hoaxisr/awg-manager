package command

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Транспортная ошибка Post (таймаут/обрыв) больше не минует save и инвалидацию: RCI мог
// применить payload, не успев ответить, — применённое обязано попасть в startup-config,
// а кэши — сброситься. Раньше ранний return оставлял save невзведённым.
func TestPostMutationChecked_TransportErrorStillSavesAndInvalidates(t *testing.T) {
	p := &fakePoster{}
	p.SetError(errors.New("rci: i/o timeout"))
	// Часовые debounce/maxWait: таймер newSaveFor выстрелил бы уже ПОСЛЕ конца
	// теста, в fakePoster со взведённой ошибкой, и цепочка ретраев жила бы в фоне
	// остального прогона пакета.
	sc := NewSaveCoordinator(p, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	invalidated := 0
	err := postMutationChecked(context.Background(), p, sc, map[string]any{"x": 1}, "test op", func() { invalidated++ })
	if err == nil || err.Error() != "test op: rci: i/o timeout" {
		t.Fatalf("err = %v", err)
	}
	if st := sc.Status(); st.State != SaveStatePending {
		t.Fatalf("save не взведён на транспортной ошибке: %+v", st)
	}
	if invalidated != 1 {
		t.Fatalf("инвалидаторов вызвано %d, ждали 1", invalidated)
	}
}
