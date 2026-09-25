package router

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Удаление пакета обязано снимать интерфейс по ПЕРСИСТУ, не глядя на
// Provisioned: после выключения режима персист хранит именно {false, Index}, и
// гейт по Provisioned оставил бы OpkgTun жить после `opkg remove`.
//
// Index 0 проверяется отдельно осознанно: поле объявлено `omitempty`, то есть
// нулевой индекс в JSON отсутствует вовсе — именно поэтому снятие делается в
// Go, а не парсингом файла шеллом (на прошивке нет jq).
func TestReleasePolicyTunForRemoval_RemovesHeldInterface(t *testing.T) {
	stubLinkAbsent(t)
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun})
	if err := store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 0}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	opkg := &recordingOpkgTunProvisioner{}
	log := &callLog{}
	routes := &recDefaultRoute{log: log}

	if err := ReleasePolicyTunForRemoval(context.Background(), Deps{
		Settings:     store,
		OpkgTun:      opkg,
		DefaultRoute: routes,
	}); err != nil {
		t.Fatalf("ReleasePolicyTunForRemoval: %v", err)
	}

	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun0" {
		t.Errorf("deleted = %v, want [OpkgTun0]", opkg.deleted)
	}
	// Дефолт снимается тоже: переживший интерфейс маршрут остался бы в конфиге
	// на несуществующем имени, а fakeip позже может пересоздать этот номер —
	// и чужой дефолт ожил бы на его интерфейсе.
	if !log.has("RemoveDefaultRoute:OpkgTun0") || !log.has("RemoveIPv6DefaultRoute:OpkgTun0") {
		t.Errorf("дефолт обязан сниматься (v4+v6): %v", log.calls)
	}
}

// Записанный NAT сегментов восстанавливается ДО снятия интерфейса — иначе
// `opkg remove` при включённом source-preserve оставил бы сегменты на
// static-NAT навсегда.
func TestReleasePolicyTunForRemoval_RestoresSegmentNATFirst(t *testing.T) {
	stubLinkAbsent(t)
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun})
	if err := store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 1, PolicyTun: &storage.OpkgTunPolicyData{NATSegments: []storage.PolicyTunNATSegment{{Name: "Home", PriorMode: "dynamic"}}}}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	// ОДИН регистратор на все шаги: с раздельными журналами утверждение о
	// порядке недоказуемо — можно проверить только факт вызовов.
	log := &callLog{}
	state := &fakeNATState{}

	if err := ReleasePolicyTunForRemoval(context.Background(), Deps{
		Settings:     store,
		OpkgTun:      &recOpkgTun{log: log},
		DefaultRoute: &recDefaultRoute{log: log},
		SegmentNAT:   &recSegmentNAT{log: log, state: state},
		NATState:     state,
	}); err != nil {
		t.Fatalf("ReleasePolicyTunForRemoval: %v", err)
	}

	if !log.has("SetSegmentNAT:Home") {
		t.Errorf("сегменту обязан вернуться динамический NAT: %v", log.calls)
	}
	// Возврат NAT — ПЕРВЫМ: после сноса интерфейса дефолт уедет на WAN, и
	// восстанавливать сегменты будет уже поздно (записи уходят вместе с
	// пакетом). Дефолт снимается до сноса по той же причине.
	mustOrderCalls(t, log, "SetSegmentNAT:Home", "RemoveDefaultRoute:OpkgTun1")
	mustOrderCalls(t, log, "RemoveDefaultRoute:OpkgTun1", "Delete:OpkgTun1")
	if !log.has("Delete:OpkgTun1") {
		t.Errorf("интерфейс обязан сноситься: %v", log.calls)
	}
}

// Персиста нет — снимать нечего, NDMS не трогаем.
func TestReleasePolicyTunForRemoval_NoopWithoutPersist(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun})
	opkg := &recordingOpkgTunProvisioner{}
	log := &callLog{}

	if err := ReleasePolicyTunForRemoval(context.Background(), Deps{
		Settings:     store,
		OpkgTun:      opkg,
		DefaultRoute: &recDefaultRoute{log: log},
	}); err != nil {
		t.Fatalf("ReleasePolicyTunForRemoval: %v", err)
	}

	if len(opkg.deleted) != 0 || len(log.calls) != 0 {
		t.Errorf("без персиста NDMS не трогаем: deleted=%v calls=%v", opkg.deleted, log.calls)
	}
}

// F450: fakeip OpkgTun переживал `opkg remove` — cleanup снимал только
// policy-tun. Снос идёт по записи владения fakeip и щадит чужой интерфейс на
// нашем индексе так же, как у policy-tun.
func TestReleaseFakeIPTunForRemoval_DeletesOwnSparesForeign(t *testing.T) {
	stubLinkAbsent(t)
	for _, tc := range foreignTeardownCases(fakeIPTunDescription, "OpkgTun2") {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: stateFakeIPTun})
			if err := store.SetOpkgTunState(&storage.OpkgTunState{
				Mode: storage.OpkgTunModeFakeIP, Index: 2, Provisioned: true,
			}); err != nil {
				t.Fatalf("SetOpkgTunState: %v", err)
			}
			opkg := &recordingOpkgTunProvisioner{}
			if err := ReleaseFakeIPTunForRemoval(context.Background(), Deps{
				Settings:    store,
				OpkgTun:     opkg,
				OpkgTunScan: tc.scan,
			}); err != nil {
				t.Fatalf("ReleaseFakeIPTunForRemoval: %v", err)
			}
			if got := len(opkg.deleted) == 1 && opkg.deleted[0] == "OpkgTun2"; got != tc.wantDel {
				t.Errorf("deleted = %v, want снос = %v", opkg.deleted, tc.wantDel)
			}
		})
	}
}

// Запись владения policy-tun — не наша: fakeip-снятие её не трогает (иначе
// удаление пакета снесло бы интерфейс дважды разными путями).
func TestReleaseFakeIPTunForRemoval_IgnoresPolicyTunRecord(t *testing.T) {
	stubLinkAbsent(t)
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun})
	if err := store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 1}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	opkg := &recordingOpkgTunProvisioner{}
	if err := ReleaseFakeIPTunForRemoval(context.Background(), Deps{Settings: store, OpkgTun: opkg}); err != nil {
		t.Fatalf("ReleaseFakeIPTunForRemoval: %v", err)
	}
	if len(opkg.deleted) != 0 {
		t.Errorf("снесён чужой режим: %v", opkg.deleted)
	}
}
