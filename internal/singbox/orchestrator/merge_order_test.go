package orchestrator

import (
	"sort"
	"testing"
)

// Порядок слияния config.d — контракт маршрутизации, а не деталь. Массивы
// конкатенируются в лексикографическом порядке имён файлов, скаляры берутся из
// первого файла. Отсюда: режимный слот обязан стоять раньше общего (иначе
// hijack-dns окажется после пользовательских правил), но позже перезаписи DNS,
// qos и selective — ровно там, где сегодня лежат 20-router.json и 21-fakeip.json.
func TestKnownSlotsMergeOrder(t *testing.T) {
	names := map[Slot]string{}
	var files []string
	for _, m := range KnownSlots() {
		names[m.Slot] = m.Filename
		files = append(files, m.Filename)
	}

	if !sort.StringsAreSorted(files) {
		t.Fatalf("KnownSlots должен быть перечислен в лексикографическом порядке имён: %v", files)
	}

	mustBefore := func(a, b Slot) {
		t.Helper()
		fa, ok := names[a]
		if !ok {
			t.Fatalf("слот %s не зарегистрирован", a)
		}
		fb, ok := names[b]
		if !ok {
			t.Fatalf("слот %s не зарегистрирован", b)
		}
		if !(fa < fb) {
			t.Errorf("%s (%s) обязан сливаться раньше %s (%s)", a, fa, b, fb)
		}
	}

	modes := []Slot{SlotTProxy, SlotPolicyTun, SlotFakeIP}

	for _, m := range modes {
		// Режимный — раньше общего: hijack-dns обязан обгонять правила пользователя.
		mustBefore(m, SlotRouting)
		// ...и позже тех, чей порядок 5D0 не меняет.
		mustBefore(SlotDNSRewrites, m)
		mustBefore(SlotQoSRoutes, m)
		mustBefore(SlotSelectiveRoutes, m)
	}

	// Общий слот — перед потребителями, которые дописывают свои правила.
	mustBefore(SlotRouting, SlotDeviceProxy)
	mustBefore(SlotRouting, SlotSubscriptions)
	mustBefore(SlotRouting, SlotUser)
}
