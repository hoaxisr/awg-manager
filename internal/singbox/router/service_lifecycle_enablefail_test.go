package router

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// RT51: ВТОРОЙ сайт IPTables.Install — путь Enable. Тот же инвариант F20, что
// у reconcileInstalled (см. service_lifecycle_installfail_test.go), плюс
// парковка слота: отказ обязан
//   - вернуть причину вызывающему;
//   - пометить снимок netfilter неизвестным (иначе следующий тик решит, что
//     перехват на месте, и не переустановит его);
//   - НЕ коммитить appliedSpec желаемым;
//   - запарковать слот 20, чтобы sing-box не слушал осиротевший TPROXY-порт;
//   - сообщить об этом device-proxy (OnRoutingSlotsChanged), иначе ссылки на
//     композиты останутся по устаревшей видимости слота.
//
// До этого теста ветка отказа не прогонялась ни разу (нулевое покрытие).
func TestEnable_InstallFailureParksSlotAndDropsSnapshot(t *testing.T) {
	boom := errors.New("iptables-restore: отказ")
	svc, dir := newQoSSlotTestService(t, "vpn")
	ensureDisabledDir(t, dir)
	orch := svc.deps.Orch
	if err := orch.SetEnabledSilent(orchestrator.SlotRouter, false); err != nil {
		t.Fatalf("park router slot: %v", err)
	}

	var notified int
	var parkedAtLastNotify bool
	svc.deps.OnRoutingSlotsChanged = func() {
		notified++
		parkedAtLastNotify = !routerSlotEnabled(orch)
	}
	svc.deps.Settings = newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode: "tproxy", DeviceMode: "all", WANAutoDetect: true,
	})
	svc.deps.Singbox = &fakeSingbox{dir: dir, isRunningFn: func() (bool, int) { return true, 1234 }}
	stubListeningProbe(t, func() bool { return true })
	svc.deps.Policies = &fakeAccessPolicyProvider{}
	svc.deps.IPTables = newStubIPTables(func(context.Context, string) error { return boom })
	svc.deps.WANIPCollector = &fakeWANIPCollector{}
	svc.deps.NetfilterPreflight = func(context.Context) error { return nil }
	svc.deps.XtDscpProbe = func(context.Context) bool { return true }

	// Снимок от прежнего успешного применения: отказ не имеет права подменить
	// его своим желаемым.
	prev := &RestoreInputSpec{PolicyMark: "0xffffaaa"}
	svc.appliedSpec = prev
	svc.netfilterStateKnown = true

	err := svc.Enable(context.Background())
	if err == nil {
		t.Fatal("отказ установки правил обязан дойти до вызывающего")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("причина отказа подменена: %v", err)
	}
	if svc.netfilterStateKnown {
		t.Error("после отказа состояние netfilter НЕИЗВЕСТНО: known обязан быть false")
	}
	if svc.appliedSpec != prev {
		t.Errorf("снимок закоммичен вопреки отказу: %+v", svc.appliedSpec)
	}
	if routerSlotEnabled(orch) {
		t.Error("слот 20 не запаркован: sing-box остался слушать осиротевший TPROXY-порт")
	}
	// Хук по пути Enable дёргается и до отказа (промоут слота), поэтому
	// ассерт «вызван хоть раз» ничего не доказывает: проверяется, что
	// ПОСЛЕДНЕЕ уведомление пришло уже с запаркованным слотом — иначе
	// device-proxy останется с видимостью живого слота 20.
	if notified == 0 {
		t.Error("о парковке слота не сообщено device-proxy (OnRoutingSlotsChanged)")
	}
	if !parkedAtLastNotify {
		t.Error("последнее уведомление пришло ДО парковки слота")
	}
}
