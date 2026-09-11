package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
)

// Дефолтного маршрута нет — бут обязан приехать в оркестратор с WANUp=false,
// иначе тот отработает как обычный бут и попытается стартовать туннели без WAN.
func TestBootEvent_CarriesProbeResult(t *testing.T) {
	if ev := bootEvent(ndmsquery.ErrNoDefaultRoute); ev.WANUp {
		t.Error("дефолтного маршрута нет — WANUp обязан быть false")
	}
	if ev := bootEvent(fmt.Errorf("проба: %w", ndmsquery.ErrNoDefaultRoute)); ev.WANUp {
		t.Error("обёрнутый ErrNoDefaultRoute обязан распознаваться")
	}
	if ev := bootEvent(nil); !ev.WANUp {
		t.Error("проба удалась — WANUp обязан быть true")
	}
	if ev := bootEvent(nil); ev.Type != orchestrator.EventBoot {
		t.Errorf("тип события = %v, ожидали EventBoot", ev.Type)
	}
}

// Транспортный отказ RCI — не показание об отсутствии WAN. Приняв его за
// «WAN нет», демон отложил бы бут до фронта WAN-up, которого при уже поднятом
// WAN никто не пришлёт: туннели простояли бы до ручного старта.
func TestBootEvent_TransportFailureIsNotWANDown(t *testing.T) {
	for _, err := range []error{
		errors.New("ndms transport: connection refused"),
		errors.New("context deadline exceeded"),
	} {
		if ev := bootEvent(err); !ev.WANUp {
			t.Errorf("транспортный отказ %v принят за отсутствие WAN", err)
		}
	}
}

// Хелпер проверяем отдельно от проводки, поэтому саму проводку держит этот
// страж: в прошлый раз вызов молча оказался не в той ветке, и поймал это
// только стенд.
func TestBootSequence_UsesBootEvent(t *testing.T) {
	src, err := os.ReadFile("boot.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "HandleEvent(a.shutdownCtx, bootEvent(err))") {
		t.Error("бут-последовательность больше не шлёт событие через bootEvent(err)")
	}
}
