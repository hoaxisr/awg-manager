package router

import (
	"errors"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// routingSlotToggler — та часть оркестратора, которая нужна переключателю
// слотов. Интерфейс (а не *orchestrator.Orchestrator) держит applyRoutingSlots
// тестируемым без файловых побочек всего сервиса.
type routingSlotToggler interface {
	SetEnabled(slot orchestrator.Slot, enabled bool) error
}

// modeSlots — режимные (взаимоисключающие) слоты захвата трафика. Ровно один
// из них активен при включённом движке.
func modeSlots() []orchestrator.Slot {
	return []orchestrator.Slot{
		orchestrator.SlotTProxy,
		orchestrator.SlotPolicyTun,
		orchestrator.SlotFakeIP,
	}
}

// ModeSlot возвращает режимный слот для значения RoutingMode. Набор значений
// закрыт NormalizeSingboxRouterSettings (tproxy|fakeip-tun|policy-tun), пустое
// и любое неизвестное значение нормализуется в tproxy — так же, как это делает
// сама нормализация настроек.
func ModeSlot(mode string) orchestrator.Slot {
	switch mode {
	case stateFakeIPTun:
		return orchestrator.SlotFakeIP
	case statePolicyTun:
		return orchestrator.SlotPolicyTun
	default:
		return orchestrator.SlotTProxy
	}
}

// modeBySlot — обратное отображение для чтения «какой режим сейчас размечен в
// слотах» (см. currentRoutingSlots).
var modeBySlot = map[orchestrator.Slot]string{
	orchestrator.SlotTProxy:    stateTProxy,
	orchestrator.SlotPolicyTun: statePolicyTun,
	orchestrator.SlotFakeIP:    stateFakeIPTun,
}

// applyRoutingSlots — ЕДИНСТВЕННАЯ точка включения/выключения слотов движка
// маршрутизации. Включённое состояние = ровно один режимный слот (по mode)
// плюс общий 21-routing; выключенное = все четыре под disabled/.
//
// Два лишних режимных слота гасятся ВСЕГДА и ПЕРВЫМИ: одновременно активные
// 20-*.json дают в merged-конфиге два инбаунда и два hijack-dns, на чём
// sing-box либо падает при загрузке, либо молча ловит трафик не тем
// перехватчиком. Гашение до промоута гарантирует, что даже коалесцированный
// reload не увидит на диске двух режимов сразу.
//
// Порядок «сначала слот, потом запись конфига» — на вызывающей стороне:
// Orch.Save целится в active/ только когда слот уже промотирован, иначе запись
// уезжает в disabled/ (см. persistConfigDirect).
//
// Обход слотов — BEST-EFFORT С АГРЕГАЦИЕЙ: КАЖДЫЙ слот получает свою попытку
// независимо от чужих ошибок, все сбои склеиваются в один error. Выход по
// первой ошибке был опасен именно на выключении: сбой на постороннем (и без
// того выключенном) слоте оставлял бы режимный слот активным, а вызывающие
// пути выключения только логируют warning и идут сносить tun/OpkgTun — то есть
// в конфиге остался бы tun-инбаунд на удалённый интерфейс.
//
// Вызывающий обязан гарантировать orch != nil (типизированный nil в
// интерфейсе здесь не отлавливается — все точки вызова уже под проверкой
// deps.Orch != nil).
func applyRoutingSlots(orch routingSlotToggler, mode string, enabled bool) error {
	want := ModeSlot(mode)
	var errs []error
	set := func(slot orchestrator.Slot, on bool) {
		if err := orch.SetEnabled(slot, on); err != nil {
			verb := "включить"
			if !on {
				verb = "выключить"
			}
			errs = append(errs, fmt.Errorf("%s слот %s: %w", verb, slot, err))
		}
	}
	for _, slot := range modeSlots() {
		if enabled && slot == want {
			continue
		}
		set(slot, false)
	}
	if enabled {
		set(want, true)
		set(orchestrator.SlotRouting, true)
	} else {
		set(orchestrator.SlotRouting, false)
	}
	return errors.Join(errs...)
}

// ApplyRoutingSlots — экспортированная обёртка для boot-вайринга (cmd), где
// разметку слотов приводят к персистнутым настройкам до старта сервиса.
func ApplyRoutingSlots(orch *orchestrator.Orchestrator, mode string, enabled bool) error {
	return applyRoutingSlots(orch, mode, enabled)
}

// currentRoutingSlots читает разметку слотов ДО переключения: какой режим в
// ней размечен и включён ли движок вообще. Нужен путям enable, которые обязаны
// уметь откатиться в ПРЕЖНИЙ режим, а не в захардкоженный.
//
// Общий слот учитывается в enabled наравне с режимными: на апгрейде с прежней
// разметки (единственный слот маршрутизации без режимных) он — единственный
// признак включённого движка, и откат обязан его вернуть.
//
// Два включённых режимных слота — состояние, которого applyRoutingSlots не
// создаёт (лишние гасятся первыми), но оно возможно при ручной правке
// config.d. Победитель тогда определяется порядком KnownSlots, последний
// включённый выигрывает — молча и намеренно: выбирать не из чего, а сама
// разметка чинится следующим же applyRoutingSlots, который погасит остальные.
// Отдельного предупреждения нет и потому, что такое состояние громко видно
// само: sing-box не грузит конфиг с двумя инбаундами захвата.
func (s *ServiceImpl) currentRoutingSlots() (mode string, enabled bool) {
	mode = stateTProxy
	for _, st := range s.deps.Orch.Snapshot() {
		if !st.Enabled {
			continue
		}
		if m, ok := modeBySlot[st.Slot]; ok {
			mode, enabled = m, true
		}
		if st.Slot == orchestrator.SlotRouting {
			enabled = true
		}
	}
	return mode, enabled
}

// routingSlotsParked сообщает, что разметка слотов разъехалась с намерением
// «движок включён в режиме mode»: режимный либо общий слот припаркован.
// Признак Present намеренно НЕ проверяется — в отличие от routerSlotEnabled:
// слот, которому в этом режиме ещё нечего писать, файла не имеет, и требование
// Present гоняло бы drift-heal (а с ним debounced reload) каждый тик.
// Незарегистрированный слот читается как припаркованный.
func (s *ServiceImpl) routingSlotsParked(mode string) bool {
	for _, slot := range []orchestrator.Slot{ModeSlot(mode), orchestrator.SlotRouting} {
		if st, ok := s.slotSnapshot(slot); !ok || !st.Enabled {
			return true
		}
	}
	return false
}
