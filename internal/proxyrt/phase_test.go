package proxyrt

import "testing"

func TestDerivePhase(t *testing.T) {
	ok := ResourceState{ID: "a", Status: StatusOK}
	unknown := ResourceState{ID: "b", Status: StatusUnknown}
	failed := ResourceState{ID: "c", Status: StatusFailed}
	blocked := ResourceState{ID: "d", Status: StatusBlocked}

	cases := []struct {
		name      string
		intent    Intent
		res       []ResourceState
		planEmpty bool
		stop      StopReason
		want      Phase
	}{
		{"выключен — disabled независимо от ресурсов",
			IntentDisabled, []ResourceState{failed}, true, StopNone, PhaseDisabled},
		{"всё ок и план пуст — достигнуто",
			IntentEnabled, []ResourceState{ok, ok}, true, StopNone, PhaseSettled},
		{"есть unknown — ждём триггер, а не достигнуто",
			IntentEnabled, []ResourceState{ok, unknown}, true, StopNone, PhaseWaiting},
		{"есть failed — стабильный объяснимый отказ",
			IntentEnabled, []ResourceState{ok, failed}, true, StopNone, PhaseFailed},
		{"failed важнее unknown",
			IntentEnabled, []ResourceState{unknown, failed}, true, StopNone, PhaseFailed},
		{"failed важнее blocked: причина информативнее следствия",
			IntentEnabled, []ResourceState{failed, blocked}, true, StopNone, PhaseFailed},
		{"применили, эффекта ждём — это waiting, не затык",
			IntentEnabled, []ResourceState{ok}, false, StopAwaiting, PhaseWaiting},
		{"исчерпан потолок проходов — stuck",
			IntentEnabled, []ResourceState{ok}, false, StopCeiling, PhaseStuck},
		{"отмена контекста — не затык",
			IntentEnabled, []ResourceState{ok}, false, StopCanceled, PhaseWaiting},
		{"план не пуст, стопор не сработал — ещё сходимся",
			IntentEnabled, []ResourceState{ok}, false, StopNone, PhaseWaiting},
		{"отказ ресурса информативнее исчерпанного потолка",
			IntentEnabled, []ResourceState{failed}, false, StopCeiling, PhaseFailed},
		{"выключен — disabled перекрывает даже потолок",
			IntentDisabled, []ResourceState{ok}, false, StopCeiling, PhaseDisabled},
		{"blocked без failed — не достигнуто, а ожидание",
			IntentEnabled, []ResourceState{ok, blocked}, true, StopNone, PhaseWaiting},
		{"инстанс без ресурсов и без плана — достигнуто",
			IntentEnabled, nil, true, StopNone, PhaseSettled},
		{"ненаблюдаемый ресурс при исчерпанном потолке — затык, а не ожидание",
			IntentEnabled, []ResourceState{ok, unknown}, false, StopCeiling, PhaseStuck},
		// Отмена до первого прохода: шагов ещё нет, значит план пуст, и клаузу
		// StopCanceled не подпирает !planEmpty. Без неё отменённый прогон
		// объявляется достигнутым.
		{"отмена при пустом плане — не «достигнуто»",
			IntentEnabled, []ResourceState{ok}, true, StopCanceled, PhaseWaiting},
		// Удаление: ресурсы сошлись к пустому желаемому, но уборка ещё не
		// случилась. Без своей ветки это докладывалось бы как settled — «всё
		// как заказано» про инстанс, которого не должно существовать.
		{"удаляется — не «достигнуто», а то же «работать не должно»",
			IntentDeleted, []ResourceState{ok, ok}, true, StopNone, PhaseDisabled},
		{"удаляется — намерение перекрывает даже отказ",
			IntentDeleted, []ResourceState{failed}, true, StopNone, PhaseDisabled},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DerivePhase(c.intent, c.res, c.planEmpty, c.stop)
			if got != c.want {
				t.Fatalf("DerivePhase = %q, want %q", got, c.want)
			}
		})
	}
}
