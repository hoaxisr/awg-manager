package main

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/proxyapp/install"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Тесты полноты диспатчей по роли в проводке. Перечисление идёт по
// instancestore.AllKinds: у всех трёх хелперов ниже есть ответ по умолчанию,
// который новой роли достаётся МОЛЧА — ни отказа, ни жалобы.
//
// Про карты notCaller ниже честно: для серверных ролей эти два теста не
// утверждают НИЧЕГО — ни вызова, ни проверки. Допущение «серверные роли сюда
// не ходят» сверено по вызывающим 2026-08-30 и живёт только в комментарии:
// появится вызов из серверной ветки — тест промолчит.

// TestKinds_ProxyLinkedFieldClassified — поле связи AWG-туннеля.
// Хелпер устроен как `if kind == KindFreeTurnClient { … }; return LinkedWdtt`,
// то есть ответ по умолчанию — не «не знаю», а ЧУЖОЕ поле (LinkedWdtt к тому
// же нулевое значение iota). Серверные роли сюда не ходят: вызывающие —
// proxyLinkedCleaners и клиентские ветви фабрики.
func TestKinds_ProxyLinkedFieldClassified(t *testing.T) {
	want := map[instancestore.Kind]api.LinkedField{
		instancestore.KindWdttClient:     api.LinkedWdtt,
		instancestore.KindFreeTurnClient: api.LinkedFreeTurn,
	}
	notCaller := map[instancestore.Kind]bool{
		instancestore.KindWdttServer:     true,
		instancestore.KindFreeTurnServer: true,
	}
	for _, k := range instancestore.AllKinds {
		exp, isClient := want[k]
		if !isClient {
			if !notCaller[k] {
				t.Errorf("роль %s не классифицирована: есть ли у неё поле связи с туннелем? см. proxyLinkedField", k)
			}
			continue
		}
		if got := proxyLinkedField(k); got != exp {
			t.Errorf("%s: поле связи %v, ждали %v", k, got, exp)
		}
	}
}

// TestKinds_ProxySubsystemClassified — подсистема бинарей роли. Пустая строка
// означает, что инстанс не удержит свои бинари от удаления (wiring_proxyrt.go:858).
func TestKinds_ProxySubsystemClassified(t *testing.T) {
	want := map[instancestore.Kind]install.Subsystem{
		instancestore.KindWdttClient:     install.SubsystemWdtt,
		instancestore.KindWdttServer:     install.SubsystemWdtt,
		instancestore.KindFreeTurnClient: install.SubsystemFreeTurn,
		instancestore.KindFreeTurnServer: install.SubsystemFreeTurn,
	}
	for _, k := range instancestore.AllKinds {
		exp, ok := want[k]
		if !ok {
			t.Errorf("роль %s не классифицирована: чьи бинари она держит? см. proxySubsystemOf", k)
			continue
		}
		got := proxySubsystemOf(k)
		if got == "" {
			t.Errorf("%s: proxySubsystemOf вернул пусто — бинари роли не будут удержаны от удаления", k)
			continue
		}
		if got != exp {
			t.Errorf("%s: подсистема %q, ждали %q", k, got, exp)
		}
	}
}

// TestKinds_LinkedToSelfClassified — «этот туннель связан со мной». false по
// умолчанию означает, что порт СВОЕГО же туннеля сочтётся занятым чужим и
// инстанс уйдёт в Blocked. Как и у поля связи, вызывающие — только клиентские
// ветви фабрики (newProxyOccupancy).
func TestKinds_LinkedToSelfClassified(t *testing.T) {
	const self = "me"
	linkOf := map[instancestore.Kind]storage.AWGTunnel{
		instancestore.KindWdttClient:     {WdttClientID: self},
		instancestore.KindFreeTurnClient: {FreeTurnClientID: self},
	}
	notCaller := map[instancestore.Kind]bool{
		instancestore.KindWdttServer:     true,
		instancestore.KindFreeTurnServer: true,
	}
	for _, k := range instancestore.AllKinds {
		tun, isClient := linkOf[k]
		if !isClient {
			if !notCaller[k] {
				t.Errorf("роль %s не классифицирована: каким полем туннель ссылается на неё? см. linkedToSelf", k)
			}
			continue
		}
		o := proxyOccupancy{selfKind: k, selfID: self}
		if !o.linkedToSelf(tun) {
			t.Errorf("%s: свой туннель не опознан — его порт сочтётся занятым чужим, инстанс уйдёт в Blocked", k)
		}
		// Чужой инстанс той же роли своим быть не должен.
		other := proxyOccupancy{selfKind: k, selfID: "other"}
		if other.linkedToSelf(tun) {
			t.Errorf("%s: чужой туннель опознан как свой", k)
		}
	}
}
