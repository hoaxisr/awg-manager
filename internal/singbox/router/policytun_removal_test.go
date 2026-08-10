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
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun})
	if err := store.SetPolicyTunState(&storage.PolicyTunState{Index: 0}); err != nil {
		t.Fatalf("SetPolicyTunState: %v", err)
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
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{RoutingMode: statePolicyTun})
	if err := store.SetPolicyTunState(&storage.PolicyTunState{
		Index:       1,
		NATSegments: []storage.PolicyTunNATSegment{{Name: "Home", PriorMode: "dynamic"}},
	}); err != nil {
		t.Fatalf("SetPolicyTunState: %v", err)
	}
	log := &callLog{}
	opkg := &recordingOpkgTunProvisioner{}
	state := &fakeNATState{}

	if err := ReleasePolicyTunForRemoval(context.Background(), Deps{
		Settings:   store,
		OpkgTun:    opkg,
		SegmentNAT: &recSegmentNAT{log: log, state: state},
		NATState:   state,
	}); err != nil {
		t.Fatalf("ReleasePolicyTunForRemoval: %v", err)
	}

	if !log.has("SetSegmentNAT:Home") {
		t.Errorf("сегменту обязан вернуться динамический NAT: %v", log.calls)
	}
	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun1" {
		t.Errorf("deleted = %v, want [OpkgTun1]", opkg.deleted)
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
