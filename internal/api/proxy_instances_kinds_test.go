package api

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// Тесты полноты диспатчей по роли. Оба хелпера ниже имеют ветку «прочее», и
// её молчаливый ответ дороже отказа: у proxySecretsOf это секреты, ушедшие в
// выдачу открытым текстом, у proxyNeedsOpkgTun — гейт прошивки, пройденный
// зря. Перечисление идёт по instancestore.AllKinds, поэтому пятая роль
// уронит тест, а не поведение.

// TestKinds_ProxySecretsClassified — proxyConfigView маршалит ВЕСЬ конфиг
// роли и вычёркивает только поля из proxySecretsOf: пустой список означает,
// что наружу уйдёт всё, включая пароль.
//
// Утечка не автоматическая: proxyConfigView идёт через proxyConfigPtr, у
// которого своя ветка «прочее» с nil, и совсем неизвестная роль отдаёт пустую
// карту. Дыра открывается ровно в том порядке, в каком роль и заводят —
// proxyConfigPtr прописан (иначе конфиг непоправим), proxySecretsOf забыт.
func TestKinds_ProxySecretsClassified(t *testing.T) {
	want := map[instancestore.Kind][]string{
		instancestore.KindWdttClient:     {"password"},
		instancestore.KindWdttServer:     nil,
		instancestore.KindFreeTurnClient: {"obfKey"},
		instancestore.KindFreeTurnServer: {"obfKey"},
	}
	// Второй половиной контракта владеет proxyPruneBlankSecrets: поле,
	// которого нет в proxySecretFields, на входе не получит семантику
	// «пусто = не менять» и затрётся пустой строкой. Сверяем В ОБЕ стороны —
	// лишнее поле в общем списке получает эту семантику, никем не будучи
	// объявлено секретом.
	known := map[string]bool{}
	for _, f := range proxySecretFields {
		known[f] = true
	}
	claimed := map[string]bool{}
	for _, k := range instancestore.AllKinds {
		exp, ok := want[k]
		if !ok {
			t.Errorf("роль %s не классифицирована: какие её поля секретны? см. proxySecretsOf", k)
			continue
		}
		// Сравнение множествами: порядок полей ни на что не влияет —
		// ни proxyConfigView, ни proxyPruneBlankSecrets его не читают.
		got := map[string]bool{}
		for _, f := range proxySecretsOf(k) {
			got[f] = true
			claimed[f] = true
			if !known[f] {
				t.Errorf("%s: секрет %q отсутствует в proxySecretFields — пустое значение затрёт его вместо «не менять»", k, f)
			}
		}
		if len(got) != len(exp) {
			t.Errorf("%s: секреты %v, ждали %v", k, got, exp)
			continue
		}
		for _, f := range exp {
			if !got[f] {
				t.Errorf("%s: поле %q не объявлено секретом — уйдёт в выдачу как есть", k, f)
			}
		}
	}
	for _, f := range proxySecretFields {
		if !claimed[f] {
			t.Errorf("поле %q числится секретом в proxySecretFields, но ни одна роль его таковым не объявляет — оно получает «пусто = не менять», не маскируясь в выдаче", f)
		}
	}
}

// TestKinds_ProxyNeedsOpkgTunClassified — гейт «прошивка не поддерживает
// OpkgTun». Ветка «прочее» отдаёт false, то есть новая роль пройдёт гейт на
// прошивке, где интерфейс выделить нечем.
func TestKinds_ProxyNeedsOpkgTunClassified(t *testing.T) {
	// Запись каждой роли в том виде, в котором она ТРЕБУЕТ интерфейс: у
	// wdtt-клиента это raw-режим, у freeturn — не требует ни в каком.
	recs := map[instancestore.Kind]instancestore.Record{
		instancestore.KindWdttClient: {Kind: instancestore.KindWdttClient,
			WdttClient: &roles.WdttClientConfig{Mode: "raw"}},
		instancestore.KindWdttServer: {Kind: instancestore.KindWdttServer,
			WdttServer: &roles.WdttServerConfig{}},
		instancestore.KindFreeTurnClient: {Kind: instancestore.KindFreeTurnClient,
			FreeTurnClient: &roles.FreeTurnClientConfig{}},
		instancestore.KindFreeTurnServer: {Kind: instancestore.KindFreeTurnServer,
			FreeTurnServer: &roles.FreeTurnServerConfig{}},
	}
	want := map[instancestore.Kind]bool{
		instancestore.KindWdttClient:     true,
		instancestore.KindWdttServer:     true,
		instancestore.KindFreeTurnClient: false,
		instancestore.KindFreeTurnServer: false,
	}
	for _, k := range instancestore.AllKinds {
		exp, ok := want[k]
		if !ok {
			t.Errorf("роль %s не классифицирована: нужен ли ей интерфейс OpkgTun? см. proxyNeedsOpkgTun", k)
			continue
		}
		rec, ok := recs[k]
		if !ok {
			t.Errorf("роль %s не имеет фикстуры в этом тесте", k)
			continue
		}
		if got := proxyNeedsOpkgTun(rec); got != exp {
			t.Errorf("%s: proxyNeedsOpkgTun=%v, ждали %v", k, got, exp)
		}
	}
}
