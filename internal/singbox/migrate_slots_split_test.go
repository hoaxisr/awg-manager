// internal/singbox/migrate_slots_split_test.go
package singbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Активный режим — fakeip: его правила становятся общими, режимная часть
// уезжает в 20-fakeip.json.
func TestMigrateSlotsSplitTakesActiveFakeIP(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "21-fakeip.json", fakeipFixtureWithRules("fakeip-rule"))
	writeSlotFixture(t, dir, "disabled/20-router.json", routerFixtureWithRules("router-rule"))

	changed, err := MigrateSlotsSplit(dir, "fakeip-tun")
	if err != nil {
		t.Fatalf("миграция: %v", err)
	}
	if !changed {
		t.Fatal("ожидалось changed=true")
	}

	shared := readSlot(t, dir, "21-routing.json")
	if !hasRule(shared, "fakeip-rule") {
		t.Error("правила активного режима не переехали в общий слот")
	}
	if hasRule(shared, "router-rule") {
		t.Error("правила НЕактивного режима не должны попадать в общий слот")
	}
	assertHijackNotIn(t, shared)           // системные правила остаются режимными
	assertExists(t, dir, "20-fakeip.json") // режимная часть
	assertBackupExists(t, dir, "20-router.json")
	assertAbsent(t, dir, "21-fakeip.json")
	assertAbsent(t, dir, "disabled/20-router.json")
}

// policy-tun: источник тот же 20-router.json, но режимный файл — policytun.
func TestMigrateSlotsSplitPolicyTun(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "20-router.json", routerFixtureWithRules("a"))

	if _, err := MigrateSlotsSplit(dir, "policy-tun"); err != nil {
		t.Fatalf("миграция: %v", err)
	}
	assertExists(t, dir, "20-policytun.json")
	assertAbsent(t, dir, "20-tproxy.json")
	// Разобранный исходный слот тоже уходит в резерв: после отката версии это
	// единственная копия правил в форме, понятной старому бинарю.
	assertBackupExists(t, dir, "20-router.json")
	assertAbsent(t, dir, "20-router.json")
}

// Черновик staging не теряется: он мигрирует в pending/ общего слота.
func TestMigrateSlotsSplitKeepsPendingDraft(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "20-router.json", routerFixtureWithRules("applied"))
	writeSlotFixture(t, dir, "pending/20-router.json", routerFixtureWithRules("drafted"))

	if _, err := MigrateSlotsSplit(dir, "tproxy"); err != nil {
		t.Fatalf("миграция: %v", err)
	}
	draft := readSlot(t, dir, "pending/21-routing.json")
	if !hasRule(draft, "drafted") {
		t.Error("несохранённый черновик потерян")
	}
}

// Идемпотентность: повторный прогон ничего не меняет.
func TestMigrateSlotsSplitIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "20-router.json", routerFixtureWithRules("a"))

	if _, err := MigrateSlotsSplit(dir, "tproxy"); err != nil {
		t.Fatalf("первый прогон: %v", err)
	}
	before := readSlotBytes(t, dir, "21-routing.json")

	changed, err := MigrateSlotsSplit(dir, "tproxy")
	if err != nil {
		t.Fatalf("второй прогон: %v", err)
	}
	if changed {
		t.Error("второй прогон не должен ничего менять")
	}
	if string(readSlotBytes(t, dir, "21-routing.json")) != string(before) {
		t.Error("повторная миграция изменила общий слот")
	}
}

// Докат после краха: новая раскладка уже записана, старый файл ещё лежит рядом.
// Оба в активном каталоге = дубли тегов в merged-конфиге = FATAL sing-box.
// Миграция обязана довести дело до конца, а не отчитаться «уже готово», и при
// этом уцелеть должно содержимое ИМЕННО прежнего файла: он новее (после отката
// версии его писал работавший бинарь), а недописанный общий слот — результат
// оборванного прогона.
func TestMigrateSlotsSplitFinishesInterrupted(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "00-base.json", baseFixture())
	writeSlotFixture(t, dir, "21-routing.json", routingSlotFixture("stale"))
	writeSlotFixture(t, dir, "20-tproxy.json", tproxyFixture())
	writeSlotFixture(t, dir, "20-router.json", routerFixtureWithRules("leftover")) // остаток

	changed, err := MigrateSlotsSplit(dir, "tproxy")
	if err != nil {
		t.Fatalf("докат: %v", err)
	}
	if !changed {
		t.Fatal("незавершённая миграция обязана быть доведена до конца")
	}
	assertAbsent(t, dir, "20-router.json")

	shared := readSlot(t, dir, "21-routing.json")
	if !hasRule(shared, "leftover") {
		t.Errorf("докат не перенёс содержимое прежнего файла:\n%s", readSlotBytes(t, dir, "21-routing.json"))
	}
	if hasRule(shared, "stale") {
		t.Errorf("в общем слоте осталось содержимое оборванного прогона:\n%s", readSlotBytes(t, dir, "21-routing.json"))
	}
	// Затёртый общий слот не выброшен, а сохранён.
	assertBackupExists(t, dir, "21-routing.json")
	singboxCheckDir(t, dir)
}

// Чистая установка: мигрировать нечего.
func TestMigrateSlotsSplitFreshInstall(t *testing.T) {
	dir := t.TempDir()
	changed, err := MigrateSlotsSplit(dir, "tproxy")
	if err != nil {
		t.Fatalf("миграция на чистой установке: %v", err)
	}
	if changed {
		t.Error("на чистой установке менять нечего")
	}
}

// Результат миграции обязан грузиться настоящим sing-box: юнит-ассерты видят
// перемещение полей, но не схему — а цена ошибки здесь пропавший у
// пользователя интернет, а не кривая вёрстка.
func TestMigrateSlotsSplitResultLoadsInSingbox(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		legacy   string
		fixture  string
		modeFile string
	}{
		{"tproxy", "tproxy", "20-router.json", routerFixtureWithRules("user"), "20-tproxy.json"},
		{"policy-tun", "policy-tun", "20-router.json", routerFixtureWithRules("user"), "20-policytun.json"},
		{"fakeip-tun", "fakeip-tun", "21-fakeip.json", fakeipFixtureWithRules("user"), "20-fakeip.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSlotFixture(t, dir, "00-base.json", baseFixture())
			writeSlotFixture(t, dir, tc.legacy, tc.fixture)

			if _, err := MigrateSlotsSplit(dir, tc.mode); err != nil {
				t.Fatalf("миграция: %v", err)
			}
			assertExists(t, dir, tc.modeFile)
			assertExists(t, dir, "21-routing.json")
			singboxCheckDir(t, dir)
		})
	}
}

// Режим в настройках переключили, но движок в нём ни разу не поднимали: файла
// активного режима на диске нет вовсе. Правила обязаны переехать из
// единственного файла, который есть, — иначе они пропадут из интерфейса.
func TestMigrateSlotsSplitFallsBackToOtherLegacySlot(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "20-router.json", routerFixtureWithRules("router-rule"))

	if _, err := MigrateSlotsSplit(dir, "fakeip-tun"); err != nil {
		t.Fatalf("миграция: %v", err)
	}
	shared := readSlot(t, dir, "21-routing.json")
	if !hasRule(shared, "router-rule") {
		t.Errorf("правила единственного слота прежней раскладки потеряны:\n%s", readSlotBytes(t, dir, "21-routing.json"))
	}
	// Режимный слот НЕ пишется: инбаунды чужого режима (tproxy-in/redirect-in)
	// в файле fakeip — это не захват трафика, а мусор. Его соберёт генератор.
	assertAbsent(t, dir, "20-fakeip.json")
	// А DNS пользователя обязан уцелеть в общем слоте: из режимного он бы
	// исчез при первой же перегенерации.
	if got := dnsServerTags(shared); len(got) != 1 || got[0] != "user-dns" {
		t.Errorf("DNS-серверы пользователя потеряны: %v\n%s", got, readSlotBytes(t, dir, "21-routing.json"))
	}
}

// C1: сбой ПОСЛЕ первой записи не имеет права оставить в активном каталоге обе
// раскладки сразу — это дублирующиеся теги инбаундов и sing-box, который не
// поднимается вообще. Битый черновик — самый дешёвый способ этот сбой вызвать.
func TestMigrateSlotsSplitBrokenDraftDoesNotStrandBothLayouts(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "00-base.json", baseFixture())
	writeSlotFixture(t, dir, "20-router.json", routerFixtureWithRules("applied"))
	writeSlotFixture(t, dir, "pending/20-router.json", "{ это не json")

	var log []string
	changed, err := MigrateSlotsSplitWithLog(dir, "tproxy", func(m string) { log = append(log, m) })
	if err != nil {
		t.Fatalf("битый черновик не должен ронять миграцию применённого конфига: %v", err)
	}
	if !changed {
		t.Error("ожидалось changed=true")
	}
	assertAbsent(t, dir, "20-router.json")
	assertAbsent(t, dir, "pending/20-router.json")
	assertExists(t, dir, "21-routing.json")
	assertExists(t, dir, "20-tproxy.json")
	if !logContains(log, "отброшен") {
		t.Errorf("отброшенный черновик обязан попасть в журнал: %v", log)
	}
	singboxCheckDir(t, dir)
}

// C1, главный случай: запись оборвалась ПОСЛЕ того, как общий слот уже лёг на
// диск. Уборка обязана состояться всё равно — иначе рядом остаются обе
// раскладки, и sing-box не поднимется («duplicate inbound tag»), причём
// повторный прогон воспроизведёт то же самое. Каталог вместо файла режимного
// слота ломает ровно вторую запись.
func TestMigrateSlotsSplitCleansUpAfterPartialWrite(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "00-base.json", baseFixture())
	writeSlotFixture(t, dir, "20-router.json", routerFixtureWithRules("applied"))
	if err := os.MkdirAll(filepath.Join(dir, "20-tproxy.json"), 0755); err != nil {
		t.Fatal(err)
	}

	changed, err := MigrateSlotsSplit(dir, "tproxy")
	if err == nil {
		t.Fatal("ожидалась ошибка записи режимного слота")
	}
	if !changed {
		t.Error("общий слот записан — миграция обязана отчитаться об изменении")
	}
	assertExists(t, dir, "21-routing.json")
	assertAbsent(t, dir, "20-router.json")
	assertBackupExists(t, dir, "20-router.json")
	// Остаток каталога-подделки убираем — sing-box читает только *.json-файлы,
	// а каталог с таким именем ему не помеха; проверяем сам merged-конфиг.
	if err := os.Remove(filepath.Join(dir, "20-tproxy.json")); err != nil {
		t.Fatal(err)
	}
	singboxCheckDir(t, dir)
}

// Обратная сторона C1: если записать не удалось НИЧЕГО, прежнюю раскладку
// трогать нельзя — на ней движок продолжает работать до следующей загрузки.
// Каталог вместо файла общего слота даёт ошибку записи, не тронув ничего.
func TestMigrateSlotsSplitKeepsLegacyWhenNothingWritten(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "20-router.json", routerFixtureWithRules("applied"))
	if err := os.MkdirAll(filepath.Join(dir, "21-routing.json"), 0755); err != nil {
		t.Fatal(err)
	}

	changed, err := MigrateSlotsSplit(dir, "tproxy")
	if err == nil {
		t.Fatal("ожидалась ошибка записи")
	}
	if changed {
		t.Error("changed=true при полном отсутствии записи")
	}
	assertExists(t, dir, "20-router.json")
	assertAbsent(t, dir, "disabled/20-router.json"+legacyBackupSuffix)
}

// C2: возврат резервной копии под исходным именем (к нему подталкивает
// инструкция по откату) не имеет права подменить уже мигрированный общий слот
// правилами неактивного режима.
func TestMigrateSlotsSplitKeepsMigratedSharedSlot(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "00-base.json", baseFixture())
	writeSlotFixture(t, dir, "21-routing.json", routingSlotFixture("current"))
	writeSlotFixture(t, dir, "20-tproxy.json", tproxyFixture())
	// Пользователь вручную вернул резерв прежней раскладки под исходным именем.
	writeSlotFixture(t, dir, "21-fakeip.json", fakeipFixtureWithRules("restored"))

	before := readSlotBytes(t, dir, "21-routing.json")
	changed, err := MigrateSlotsSplit(dir, "tproxy")
	if err != nil {
		t.Fatalf("миграция: %v", err)
	}
	if !changed {
		t.Error("остаток прежней раскладки обязан уехать в резерв")
	}
	if !bytes.Equal(readSlotBytes(t, dir, "21-routing.json"), before) {
		t.Errorf("общий слот подменён данными неактивного режима:\n%s", readSlotBytes(t, dir, "21-routing.json"))
	}
	assertAbsent(t, dir, "21-fakeip.json")
	assertBackupExists(t, dir, "21-fakeip.json")
	assertAbsent(t, dir, "disabled/21-routing.json"+legacyBackupSuffix)
	singboxCheckDir(t, dir)
}

// I1: на установке в режиме fakeip черновик принадлежит ДРУГОМУ набору правил
// (SaveDraft всегда звался со слотом 20-router.json) — он отбрасывается, но не
// молча: пользователь видел баннер «есть несохранённые изменения».
func TestMigrateSlotsSplitDiscardsForeignDraft(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "21-fakeip.json", fakeipFixtureWithRules("fakeip-rule"))
	writeSlotFixture(t, dir, "pending/20-router.json", routerFixtureWithRules("drafted"))

	var log []string
	if _, err := MigrateSlotsSplitWithLog(dir, "fakeip-tun", func(m string) { log = append(log, m) }); err != nil {
		t.Fatalf("миграция: %v", err)
	}
	assertAbsent(t, dir, "pending/21-routing.json")
	assertAbsent(t, dir, "pending/20-router.json")
	assertBackupExists(t, dir, "20-router.json")
	if !logContains(log, "отброшен") {
		t.Errorf("об отброшенном черновике не написано в журнал: %v", log)
	}
}

// Запасной вариант с fakeip-источником: режимного слота не будет, значит
// серверы fakeip/real не объявит никто. Движковый DNS обязан быть выброшен
// (иначе висячие ссылки и FATAL), а пользовательский — уцелеть.
func TestMigrateSlotsSplitFallbackDropsEngineDNS(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "00-base.json", baseFixture())
	writeSlotFixture(t, dir, "21-fakeip.json", fakeipFixtureWithRules("fakeip-rule"))

	if _, err := MigrateSlotsSplit(dir, "tproxy"); err != nil {
		t.Fatalf("миграция: %v", err)
	}
	shared := readSlot(t, dir, "21-routing.json")
	raw := readSlotBytes(t, dir, "21-routing.json")
	for _, tag := range dnsServerTags(shared) {
		if tag == "real" || tag == "fakeip" {
			t.Errorf("движковый DNS-сервер %q остался без объявляющего его режима:\n%s", tag, raw)
		}
	}
	if got := dnsServerTags(shared); len(got) != 1 || got[0] != "user-dns" {
		t.Errorf("пользовательский DNS-сервер потерян: %v\n%s", got, raw)
	}
	if dnsFinal(shared) != "" {
		t.Errorf("dns.final ссылается на движковый сервер: %q", dnsFinal(shared))
	}
	assertAbsent(t, dir, "20-tproxy.json")
	singboxCheckDir(t, dir)
}

// Пользовательский DNS-сервер с движковым тегом переименовывается, а ссылки на
// него переписываются. Без этого переключение в fakeip даёт duplicate-dns и
// вечную блокировку reload.
func TestMigrateSlotsSplitRenamesReservedDNSTag(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "20-router.json", routerFixtureWithReservedDNS())

	if _, err := MigrateSlotsSplit(dir, "tproxy"); err != nil {
		t.Fatalf("миграция: %v", err)
	}
	shared := readSlot(t, dir, "21-routing.json")
	raw := readSlotBytes(t, dir, "21-routing.json")
	for _, tag := range dnsServerTags(shared) {
		if tag == "real" || tag == "fakeip" {
			t.Errorf("движковый тег %q остался за пользовательским сервером:\n%s", tag, raw)
		}
	}
	if !bytes.Contains(raw, []byte(`"real-user"`)) {
		t.Errorf("сервер не переименован в свободный тег:\n%s", raw)
	}
	// Ссылки переписаны: висячий "real" в dns.final или в правиле — тот же
	// нерабочий конфиг, только с другой ошибкой.
	if got := dnsFinal(shared); got != "real-user" {
		t.Errorf("dns.final = %q, ожидался переименованный тег", got)
	}
	if bytes.Contains(raw, []byte(`"server": "real"`)) {
		t.Errorf("ссылка на старый тег не переписана:\n%s", raw)
	}
}

// Правило пользователя, стоящее ПОСЛЕ системного префикса, остаётся
// пользовательским, даже если по форме похоже на системное: режимный слот
// перегенерируется из настроек, и такое правило там бы просто исчезло.
func TestMigrateSlotsSplitKeepsUserPrivateRule(t *testing.T) {
	dir := t.TempDir()
	writeSlotFixture(t, dir, "20-router.json", routerFixtureWithUserPrivateRule())

	if _, err := MigrateSlotsSplit(dir, "tproxy"); err != nil {
		t.Fatalf("миграция: %v", err)
	}
	shared := readSlot(t, dir, "21-routing.json")
	if !hasRule(shared, "lan-out") {
		t.Errorf("пользовательское правило ip_is_private потеряно:\n%s", readSlotBytes(t, dir, "21-routing.json"))
	}
}

// --- фикстуры ---

// Минимальный, но валидный слот прежней раскладки для режимов tproxy/policy-tun:
// пара инбаундов перехвата, системный префикс правил, пользовательское правило,
// набор, композит, DNS и route.final.
func routerFixtureWithRules(tag string) string {
	return fmt.Sprintf(`{
  "inbounds": [
    {"type": "tproxy", "tag": "tproxy-in", "listen": "127.0.0.1", "listen_port": 51271, "network": "udp"},
    {"type": "redirect", "tag": "redirect-in", "listen": "127.0.0.1", "listen_port": 51272}
  ],
  "outbounds": [
    {"type": "selector", "tag": "user-proxy", "outbounds": ["direct"]}
  ],
  "dns": {
    "servers": [{"tag": "user-dns", "type": "udp", "server": "1.1.1.1"}],
    "rules": [{"action": "route", "server": "user-dns", "domain_suffix": ["%[1]s.example"]}],
    "final": "user-dns"
  },
  "route": {
    "rule_set": [
      {"tag": "%[1]s-set", "type": "remote", "format": "binary", "url": "https://example.org/%[1]s.srs", "update_interval": "24h"}
    ],
    "rules": [
      {"action": "sniff"},
      {"type": "logical", "mode": "or", "rules": [{"protocol": "dns"}, {"port": [53]}], "action": "hijack-dns"},
      {"ip_is_private": true, "outbound": "direct"},
      {"action": "route-options", "network": "udp", "udp_timeout": "5m"},
      {"action": "route", "domain_suffix": ["%[1]s.example"], "outbound": "user-proxy"}
    ],
    "final": "user-proxy",
    "auto_detect_interface": true
  }
}`, tag)
}

// Слот прежней раскладки для fakeip: tun-инбаунд, серверы fakeip/real,
// DNS-правило режима, cache_file и default_domain_resolver — плюс то же
// пользовательское содержимое, что и в роутерном слоте.
func fakeipFixtureWithRules(tag string) string {
	return fmt.Sprintf(`{
  "inbounds": [
    {"type": "tun", "tag": "tun-in", "interface_name": "opkgtun0", "address": ["172.18.0.1/30"],
     "mtu": 1500, "auto_route": false, "auto_redirect": false, "strict_route": false,
     "stack": "gvisor", "endpoint_independent_nat": false}
  ],
  "outbounds": [
    {"type": "selector", "tag": "user-proxy", "outbounds": ["direct"]}
  ],
  "dns": {
    "servers": [
      {"tag": "fakeip", "type": "fakeip", "inet4_range": "198.18.0.0/15"},
      {"tag": "real", "type": "udp", "server": "1.1.1.1"},
      {"tag": "user-dns", "type": "udp", "server": "9.9.9.9"}
    ],
    "rules": [{"action": "route", "server": "fakeip", "query_type": ["A", "AAAA"]}],
    "final": "real"
  },
  "route": {
    "rule_set": [
      {"tag": "%[1]s-set", "type": "remote", "format": "binary", "url": "https://example.org/%[1]s.srs", "update_interval": "24h"}
    ],
    "rules": [
      {"action": "hijack-dns", "protocol": "dns"},
      {"ip_is_private": true, "outbound": "direct"},
      {"action": "route", "domain_suffix": ["%[1]s.example"], "outbound": "user-proxy"}
    ],
    "final": "user-proxy",
    "default_domain_resolver": {"server": "real"},
    "auto_detect_interface": true
  },
  "experimental": {"cache_file": {"enabled": true, "store_fakeip": true, "path": "/tmp/awgm-fakeip-test.db"}}
}`, tag)
}

// Слот НОВОЙ раскладки — общий. Отличие от прежней раскладки принципиальное:
// ни инбаундов, ни системных правил (они в режимном файле).
func routingSlotFixture(tag string) string {
	return fmt.Sprintf(`{
  "outbounds": [
    {"type": "selector", "tag": "user-proxy", "outbounds": ["direct"]}
  ],
  "dns": {
    "servers": [{"tag": "user-dns", "type": "udp", "server": "1.1.1.1"}],
    "final": "user-dns"
  },
  "route": {
    "rules": [
      {"action": "route", "domain_suffix": ["%[1]s.example"], "outbound": "user-proxy"}
    ],
    "final": "user-proxy",
    "auto_detect_interface": true
  }
}`, tag)
}

// Слот НОВОЙ раскладки — режимный tproxy. Нужен, чтобы изобразить прогон
// миграции, прерванный между записью новых файлов и уборкой старых.
func tproxyFixture() string {
	return `{
  "inbounds": [
    {"type": "tproxy", "tag": "tproxy-in", "listen": "127.0.0.1", "listen_port": 51271, "network": "udp"},
    {"type": "redirect", "tag": "redirect-in", "listen": "127.0.0.1", "listen_port": 51272}
  ],
  "route": {
    "rules": [
      {"action": "sniff"},
      {"type": "logical", "mode": "or", "rules": [{"protocol": "dns"}, {"port": [53]}], "action": "hijack-dns"},
      {"ip_is_private": true, "outbound": "direct"}
    ]
  }
}`
}

// Установка, где пользователь успел завести DNS-сервер с движковым тегом до
// того, как тег стал зарезервированным.
func routerFixtureWithReservedDNS() string {
	return `{
  "outbounds": [
    {"type": "selector", "tag": "user-proxy", "outbounds": ["direct"], "server": "proxy.example.org",
     "domain_resolver": {"server": "real"}}
  ],
  "dns": {
    "servers": [{"tag": "real", "type": "udp", "server": "8.8.8.8"}],
    "rules": [{"action": "route", "server": "real", "domain_suffix": ["example.org"]}],
    "final": "real"
  },
  "route": {
    "rules": [{"action": "route", "domain_suffix": ["example.org"], "outbound": "user-proxy"}],
    "final": "user-proxy"
  }
}`
}

// Пользовательское правило про приватные адреса ПОСЛЕ системного префикса —
// его выход не "direct", и это данные пользователя.
func routerFixtureWithUserPrivateRule() string {
	return `{
  "outbounds": [
    {"type": "selector", "tag": "lan-out", "outbounds": ["direct"]}
  ],
  "route": {
    "rules": [
      {"action": "sniff"},
      {"ip_is_private": true, "outbound": "direct"},
      {"ip_is_private": true, "outbound": "lan-out"}
    ],
    "final": "direct"
  }
}`
}

func baseFixture() string {
	return `{
  "log": {"level": "warn"},
  "outbounds": [{"type": "direct", "tag": "direct"}]
}`
}

// --- хелперы ---

func writeSlotFixture(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir для %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("запись %s: %v", rel, err)
	}
}

func readSlotBytes(t *testing.T, dir, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("чтение %s: %v", rel, err)
	}
	return data
}

func readSlot(t *testing.T, dir, rel string) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(readSlotBytes(t, dir, rel), &cfg); err != nil {
		t.Fatalf("разбор %s: %v", rel, err)
	}
	return cfg
}

// routeRules достаёт route.rules как список объектов; отсутствие секции — не
// ошибка (режимный слот без правил валиден).
func routeRules(cfg map[string]any) []any {
	route, ok := cfg["route"].(map[string]any)
	if !ok {
		return nil
	}
	rules, _ := route["rules"].([]any)
	return rules
}

// hasRule ищет правило, в котором встречается опознавательная строка фикстуры.
func hasRule(cfg map[string]any, token string) bool {
	for _, r := range routeRules(cfg) {
		raw, err := json.Marshal(r)
		if err != nil {
			continue
		}
		if bytes.Contains(raw, []byte(token)) {
			return true
		}
	}
	return false
}

func assertHijackNotIn(t *testing.T, cfg map[string]any) {
	t.Helper()
	for _, r := range routeRules(cfg) {
		obj, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if obj["action"] == "hijack-dns" {
			t.Errorf("системное правило hijack-dns попало в общий слот — после первого Reconcile их станет два")
		}
	}
}

func dnsServerTags(cfg map[string]any) []string {
	dns, ok := cfg["dns"].(map[string]any)
	if !ok {
		return nil
	}
	servers, _ := dns["servers"].([]any)
	tags := make([]string, 0, len(servers))
	for _, s := range servers {
		obj, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if tag, ok := obj["tag"].(string); ok {
			tags = append(tags, tag)
		}
	}
	return tags
}

func dnsFinal(cfg map[string]any) string {
	dns, ok := cfg["dns"].(map[string]any)
	if !ok {
		return ""
	}
	final, _ := dns["final"].(string)
	return final
}

func logContains(log []string, substr string) bool {
	for _, line := range log {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

func assertExists(t *testing.T, dir, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
		t.Errorf("ожидался файл %s: %v", rel, err)
	}
}

func assertAbsent(t *testing.T, dir, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err == nil {
		t.Errorf("файл %s обязан был исчезнуть", rel)
	}
}

// assertBackupExists проверяет резервную копию под ТОЧНЫМ именем
// disabled/<name>.pre-5d0 — не по маске: копия с числовым суффиксом
// (`.pre-5d0.2`) означает вторую копию того же слота, и путать их нельзя,
// инструкция по откату называет именно первую.
func assertBackupExists(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, "disabled", name+legacyBackupSuffix)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("резервная копия %s не найдена: %v", name, err)
	}
}

// singboxCheckDir прогоняет настоящий `sing-box check -C dir`, если бинарь
// доступен.
func singboxCheckDir(t *testing.T, dir string) {
	t.Helper()
	bin := locateSingboxBinaryForTest()
	if bin == "" {
		t.Log("sing-box не найден — настоящая проверка конфига пропущена")
		return
	}
	cmd := exec.Command(bin, "check", "-C", dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		merged, _ := os.ReadDir(dir)
		names := make([]string, 0, len(merged))
		for _, e := range merged {
			names = append(names, e.Name())
		}
		t.Fatalf("sing-box check: %v\nstderr: %s\nфайлы: %s", err, stderr.String(), strings.Join(names, ", "))
	}
}

func locateSingboxBinaryForTest() string {
	if p := os.Getenv("AWGM_SINGBOX_BIN"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("sing-box"); err == nil {
		return p
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return ""
	}
	matches, err := filepath.Glob(filepath.FromSlash(
		"../../dist/singbox-binaries/*/sing-box-*-linux-" + runtime.GOARCH + "*/sing-box"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}
