package exitreg

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/wdttclient"
)

func rawCfg(name, ndms, kernel string) roles.WdttClientConfig {
	return roles.WdttClientConfig{Mode: "raw", Name: name, Listen: "127.0.0.1:9000",
		Peer: "1.2.3.4:56000", Password: "p", VKHashes: "h",
		NdmsIface: ndms, RawIface: kernel}
}

func TestDeclaredExitsOnlyRawClients(t *testing.T) {
	got := DeclaredExits([]InstanceConfig{
		// Enabled задан ЯВНО: нулевое значение поля тоже осмысленно
		// (выключенный инстанс), и умолчание здесь читалось бы как «забыли».
		{ID: "de", Cfg: rawCfg("Германия", "OpkgTun18", "opkgtun18"), Enabled: true},
		{ID: "nl", Cfg: roles.WdttClientConfig{Mode: "wg", Name: "Голландия", Peer: "1.1.1.1:1"}, Enabled: true},
		{ID: "srv", Cfg: roles.WdttServerConfig{Listen: "0.0.0.0:56000", NdmsIface: "OpkgTun20"}, Enabled: true},
		{ID: "ft", Cfg: roles.FreeTurnClientConfig{}, Enabled: true},
	})
	if len(got) != 1 {
		t.Fatalf("выход объявляет только raw-клиент, получено %d: %+v", len(got), got)
	}
	d := got[0]
	if d.ID != wdttclient.RawTunnelID("de") {
		t.Fatalf("ExitID обязан совпадать с id зеркальной записи: %q", d.ID)
	}
	if d.InstanceID != "de" || d.NDMSName != "OpkgTun18" || d.KernelIface != "opkgtun18" {
		t.Fatalf("объявление собрано неверно: %+v", d)
	}
	if d.Name != "Германия" || d.Peer != "1.2.3.4:56000" {
		t.Fatalf("имя или peer не доехали: %+v", d)
	}
	if !d.Enabled {
		t.Fatalf("Enabled обязан доехать из InstanceConfig: %+v", d)
	}
}

func TestDeclaredExitsCarriesIntentNotFact(t *testing.T) {
	// Пара к предыдущему тесту: вместе они запирают Enabled с обеих сторон.
	// Захардкоженное true убивает этот тест, захардкоженное false — тот.
	got := DeclaredExits([]InstanceConfig{
		{ID: "de", Cfg: rawCfg("Германия", "OpkgTun18", "opkgtun18"), Enabled: false},
	})
	if len(got) != 1 || got[0].Enabled {
		t.Fatalf("выключенный инстанс объявляет выход, но с Enabled=false: %+v", got)
	}
}

func TestDeclaredExitsAcceptsPointerConfig(t *testing.T) {
	// Указатель на конфиг — обычная форма у писателя. Утверждение о
	// метод-сете (*T включает методы T), а не о нашей ветке: ветки больше нет.
	cfg := rawCfg("Германия", "OpkgTun18", "opkgtun18")
	got := DeclaredExits([]InstanceConfig{{ID: "de", Cfg: &cfg, Enabled: true}})
	if len(got) != 1 || got[0].ID != wdttclient.RawTunnelID("de") {
		t.Fatalf("указатель на конфиг обязан давать то же объявление: %+v", got)
	}
}

func TestDeclaredExitsSkipsNilConfig(t *testing.T) {
	// nil в интерфейсном поле — дефект писателя, но ронять ВЕСЬ список
	// (а с ним и зеркальные записи остальных) из-за него нельзя.
	got := DeclaredExits([]InstanceConfig{
		{ID: "x", Cfg: nil, Enabled: true},
		{ID: "de", Cfg: rawCfg("Германия", "OpkgTun18", "opkgtun18"), Enabled: true},
	})
	if len(got) != 1 || got[0].InstanceID != "de" {
		t.Fatalf("nil-конфиг обязан быть пропущен, остальные — объявлены: %+v", got)
	}
}

func TestMirrorNameParity(t *testing.T) {
	// Форма имени зеркальной записи — та же, что у старого кода
	// (wdtt.TunnelNameFromClient, names.go:5-21): суффикс " wdtt", дефолт
	// "WDTT", усечение по рунам до 60.
	cases := map[string]string{
		"":            "WDTT wdtt",
		"  Германия ": "Германия wdtt", // обрамляющие пробелы старый код срезал

		"Германия":    "Германия wdtt",
		"Берлин wdtt": "Берлин wdtt",
		"БЕРЛИН WDTT": "БЕРЛИН WDTT",
	}
	for in, want := range cases {
		if got := MirrorName(in); got != want {
			t.Fatalf("MirrorName(%q) = %q, want %q", in, got, want)
		}
	}
	long := make([]rune, 80)
	for i := range long {
		long[i] = 'я'
	}
	if r := []rune(MirrorName(string(long))); len(r) != 60 {
		t.Fatalf("усечение по рунам: got %d рун", len(r))
	}
}
