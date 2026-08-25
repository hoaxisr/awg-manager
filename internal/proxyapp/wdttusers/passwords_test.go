package wdttusers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

func TestSanitizePasswordsDevices_RemovesGatewayIP(t *testing.T) {
	devices := map[string]any{
		"good": map[string]any{"ip": "10.66.0.2"},
		"bad":  map[string]any{"ip": wdttServerGatewayAddr},
	}
	out, changed := sanitizePasswordsDevices(devices)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if len(out) != 1 {
		t.Fatalf("devices = %#v", out)
	}
	if _, ok := out["good"]; !ok {
		t.Fatalf("good device removed: %#v", out)
	}
}

func TestSanitizePasswordsDevices_RemovesRawGatewayIP(t *testing.T) {
	devices := map[string]any{
		"good": map[string]any{"ip": "10.66.0.2", "raw_ip": "10.70.0.2"},
		"bad":  map[string]any{"ip": "10.66.0.3", "raw_ip": rawServerGatewayAddr},
	}
	out, changed := sanitizePasswordsDevices(devices)
	if !changed {
		t.Fatal("устройство с raw-адресом шлюза не снято")
	}
	if _, ok := out["bad"]; ok {
		t.Fatalf("bad остался: %#v", out)
	}
}

func TestPreparePasswordsJSONForServer_PreservesDevices(t *testing.T) {
	dir := t.TempDir()
	existing := passwordsJSON{
		MainPassword: "main",
		// Владелец обязателен: устройство без ссылки из device_ids — сирота,
		// и прополка его снимает. Сохраняем устройства ЖИВЫХ абонентов.
		Passwords: map[string]passwordsJSONUser{
			"client1": {DeviceIDs: []string{"dev1"}},
		},
		Devices: map[string]any{
			"dev1": map[string]any{"ip": "10.66.0.3", "pub_key": "abc"},
		},
	}
	writePasswordsFixture(t, dir, existing)
	doc, sanitized, err := preparePasswordsJSONForServer(dir, "main", "", "", []instancestore.ServerUser{
		{Password: "client1", Comment: "Иван"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if sanitized {
		t.Fatal("sanitized should be false for valid device IP")
	}
	if len(doc.Devices) != 2 {
		t.Fatalf("devices not preserved/reserved: %#v", doc.Devices)
	}
	if _, ok := doc.Devices["dev1"]; !ok {
		t.Fatalf("dev1 missing: %#v", doc.Devices)
	}
	if _, ok := doc.Passwords["client1"]; !ok {
		t.Fatalf("client password missing: %#v", doc.Passwords)
	}
}

func TestReserveGatewayIPInDevices(t *testing.T) {
	out := reserveGatewayIPInDevices(map[string]any{})
	if len(out) != 1 {
		t.Fatalf("reservation missing: %#v", out)
	}
	for _, entry := range out {
		if deviceIPFromPasswordsEntry(entry) != wdttServerGatewayAddr {
			t.Fatalf("unexpected ip: %#v", entry)
		}
	}
	if len(reserveGatewayIPInDevices(out)) != 1 {
		t.Fatal("duplicate reservation")
	}
}

func TestSyncPasswordsJSON_DropsGatewayDevice(t *testing.T) {
	dir := t.TempDir()
	writePasswordsFixture(t, dir, passwordsJSON{
		MainPassword: "main",
		// Владелец у "bad" обязателен: без него устройство снимает прополка
		// сирот, и тест перестаёт сторожить сам sanitize.
		Passwords: map[string]passwordsJSONUser{
			"client1": {DeviceIDs: []string{"bad"}},
		},
		Devices: map[string]any{
			"bad": map[string]any{"ip": wdttServerGatewayAddr},
		},
	})
	sanitized, err := syncPasswordsJSON(dir, "main", "", "", []instancestore.ServerUser{{Password: "client1"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !sanitized {
		t.Fatal("sanitized = false, устройство с IP шлюза не отмечено вычищенным")
	}
	out, err := loadPasswordsJSON(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Devices) != 1 {
		t.Fatalf("expected gateway reservation only: %#v", out.Devices)
	}
	if deviceIPFromPasswordsEntry(out.Devices[gatewayReserveDeviceID]) != wdttServerGatewayAddr {
		t.Fatalf("reservation missing: %#v", out.Devices)
	}
}

// TestSyncPasswordsJSON_CreatesOwnerOnlyFile сторожит права СОЗДАВАЕМОГО файла:
// в нём лежат пароли абонентов. Каталог обязан быть пустым — os.WriteFile права
// уже существующего файла не меняет.
func TestSyncPasswordsJSON_CreatesOwnerOnlyFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	if _, err := syncPasswordsJSON(dir, "main", "", "", []instancestore.ServerUser{{Password: "client1"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(passwordsJSONPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("права passwords.json = %04o, ожидалось 0600", perm)
	}
}

// TestSyncPasswordsJSON_SanitizedIgnoresOwnGatewayReservation сторожит СМЫСЛ
// признака: «вычищено» обязано означать снятие устройства АБОНЕНТА с IP шлюза.
// Собственный резерв кладётся при каждой записи файла и снимается при
// следующей — без исключения для него бит истинен всегда, то есть не несёт
// информации.
func TestSyncPasswordsJSON_SanitizedIgnoresOwnGatewayReservation(t *testing.T) {
	dir := t.TempDir()
	users := []instancestore.ServerUser{{Password: "client1"}}

	first, err := syncPasswordsJSON(dir, "main", "", "", users, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first {
		t.Fatal("первая запись: sanitized = true на пустом файле")
	}
	for i := 2; i <= 3; i++ {
		got, err := syncPasswordsJSON(dir, "main", "", "", users, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if got {
			t.Fatalf("запись %d: sanitized = true при неизменном наборе — бит вырожден собственным резервом", i)
		}
	}

	// Контроль: настоящее устройство абонента на IP шлюза бит поднимает —
	// исключение сделано для одного идентификатора, а не для адреса.
	doc, err := loadPasswordsJSON(dir)
	if err != nil {
		t.Fatal(err)
	}
	doc.Devices["dev-abonenta"] = map[string]any{"ip": wdttServerGatewayAddr}
	writePasswordsFixture(t, dir, doc)
	got, err := syncPasswordsJSON(dir, "main", "", "", users, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("устройство абонента с IP шлюза снято молча: сигнал потерян")
	}
}

// writePasswordsFixture кладёт в dir passwords.json с заданным содержимым.
func writePasswordsFixture(t *testing.T, dir string, doc passwordsJSON) {
	t.Helper()
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwordsJSONPath(dir), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// TestUsableUsers_FollowsReasonClassifier сторожит связь предиката и
// классификатора: отбор обязан идти ТОЛЬКО через UnusableReason.
func TestUsableUsers_FollowsReasonClassifier(t *testing.T) {
	now := time.Unix(1700000000, 0)
	const main = "adminpass"
	users := []instancestore.ServerUser{
		{Password: "abonent1"},
		{Password: "botpass", ExpiresAt: now.Add(time.Hour).Unix()},
		{Password: "  spaced  "},
		{Password: "   "},
		{Password: main},
		{Password: "stale", ExpiresAt: now.Add(-time.Hour).Unix()},
	}

	inUsable := map[string]bool{}
	for _, u := range UsableUsers(users, main, now) {
		inUsable[u.Password] = true
	}
	for _, u := range users {
		want := UnusableReason(u, main, now) == wdttlink.ReasonUsable
		if got := inUsable[strings.TrimSpace(u.Password)]; got != want {
			t.Fatalf("абонент %q: предикат = %v, классификатор = %v (%q) — отбор идёт мимо классификатора",
				u.Password, got, want, UnusableReason(u, main, now))
		}
	}
}

func TestUnusableReason_NamesEachCondition(t *testing.T) {
	now := time.Unix(1700000000, 0)
	cases := []struct {
		name string
		user instancestore.ServerUser
		want wdttlink.UnusableReason
	}{
		{"рабочий", instancestore.ServerUser{Password: "abonent1"}, wdttlink.ReasonUsable},
		{"пустой пароль", instancestore.ServerUser{Password: "   "}, wdttlink.ReasonEmptyPassword},
		{"главный пароль", instancestore.ServerUser{Password: " adminpass "}, wdttlink.ReasonMainPassword},
		{"просрочен", instancestore.ServerUser{Password: "stale", ExpiresAt: now.Add(-time.Second).Unix()}, wdttlink.ReasonExpired},
		{"бессрочный", instancestore.ServerUser{Password: "forever", ExpiresAt: 0}, wdttlink.ReasonUsable},
	}
	for _, tc := range cases {
		if got := UnusableReason(tc.user, "adminpass", now); got != tc.want {
			t.Fatalf("%s: причина = %q, ожидалась %q", tc.name, got, tc.want)
		}
	}
	// Vetting обязана быть тем же предикатом, а не своей копией.
	var v Vetting
	if got := v.UnusableReason(instancestore.ServerUser{Password: " adminpass "}, "adminpass", now); got != wdttlink.ReasonMainPassword {
		t.Fatalf("Vetting.UnusableReason = %q", got)
	}
	if got := v.UsableUsers([]instancestore.ServerUser{{Password: " forever "}}, "adminpass", now); len(got) != 1 || got[0].Password != "forever" {
		t.Fatalf("Vetting.UsableUsers = %#v", got)
	}
}

func TestUsableUsers_SkipsEmptyMainAndExpired(t *testing.T) {
	now := time.Unix(1700000000, 0)
	got := UsableUsers([]instancestore.ServerUser{
		{Password: ""},
		{Password: "   "},
		{Password: "  main  "},
		{Password: "expired", ExpiresAt: now.Unix() - 1},
		{Password: "edge", ExpiresAt: now.Unix()},
		{Password: " forever "},
		{Password: " timed ", ExpiresAt: now.Unix() + 1},
	}, " main ", now)
	if len(got) != 2 {
		t.Fatalf("usable = %#v", got)
	}
	if got[0].Password != "forever" {
		t.Fatalf("первый абонент = %q, ожидался подрезанный forever", got[0].Password)
	}
	if got[1].Password != "timed" {
		t.Fatalf("второй абонент = %q, ожидался подрезанный timed", got[1].Password)
	}
}

func TestPreparePasswordsJSON_SkipsExpiredUser(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	doc, _, err := preparePasswordsJSONForServer(dir, "  main  ", "", "", []instancestore.ServerUser{
		{Password: "dead", ExpiresAt: now.Add(-time.Hour).Unix()},
		{Password: "alive", ExpiresAt: now.Add(time.Hour).Unix()},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if doc.MainPassword != "main" {
		t.Fatalf("main_password = %q, ожидался подрезанный", doc.MainPassword)
	}
	if _, ok := doc.Passwords["dead"]; ok {
		t.Fatalf("просроченный абонент попал в файл: %#v", doc.Passwords)
	}
	if _, ok := doc.Passwords["alive"]; !ok {
		t.Fatalf("живой абонент потерян: %#v", doc.Passwords)
	}
}

func TestPreparePasswordsJSON_KeepsLiveFieldsOfExistingUser(t *testing.T) {
	dir := t.TempDir()
	expires := time.Now().Add(24 * time.Hour).Unix()
	writePasswordsFixture(t, dir, passwordsJSON{
		MainPassword: "main",
		Passwords: map[string]passwordsJSONUser{
			"client1": {
				Label:         "старое имя",
				DeviceID:      "legacy",
				DeviceIDs:     []string{"d1"},
				MaxDevices:    3,
				ExpiresAt:     expires,
				DownBytes:     111,
				UpBytes:       222,
				VkHash:        "vk-from-server",
				Ports:         "1,2,3",
				IsDeactivated: true,
			},
			// Абонент бота: имени в нашей записи нет, и затирать его нечем.
			"client2": {Label: "имя из бота"},
		},
	})
	doc, _, err := preparePasswordsJSONForServer(dir, "main", "", "", []instancestore.ServerUser{
		{Password: "client1", Comment: "Иван"},
		{Password: "client2"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := doc.Passwords["client1"]
	want := passwordsJSONUser{
		Label:         "Иван",
		DeviceID:      "legacy",
		DeviceIDs:     []string{"d1"},
		MaxDevices:    3,
		ExpiresAt:     expires,
		DownBytes:     111,
		UpBytes:       222,
		VkHash:        "vk-from-server",
		Ports:         "1,2,3",
		IsDeactivated: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("живые поля не сохранены:\n получено %#v\n ожидалось %#v", got, want)
	}
	if label := doc.Passwords["client2"].Label; label != "имя из бота" {
		t.Fatalf("label = %q, пустое имя в записи затёрло имя из файла", label)
	}
}

func TestPreparePasswordsJSON_WritesLabelNotComment(t *testing.T) {
	dir := t.TempDir()
	doc, _, err := preparePasswordsJSONForServer(dir, "main", "", "", []instancestore.ServerUser{
		{Password: "client1", Comment: "Иван"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Смотрим на СЕРИАЛИЗОВАННЫЙ вид: подмену ключа сравнение полей не поймает.
	data, err := json.Marshal(doc.Passwords)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"label":"Иван"`) {
		t.Fatalf("имя абонента не в label: %s", data)
	}
	if strings.Contains(string(data), "comment") {
		t.Fatalf("в записи абонента остался ключ comment: %s", data)
	}
}

func TestPreparePasswordsJSON_DropsOrphanDevices(t *testing.T) {
	dir := t.TempDir()
	writePasswordsFixture(t, dir, passwordsJSON{
		MainPassword: "main",
		Passwords: map[string]passwordsJSONUser{
			"client1": {DeviceIDs: []string{"d1"}, DeviceID: "d3"},
		},
		Devices: map[string]any{
			"d1":                   map[string]any{"ip": "10.66.0.2"},
			"d2":                   map[string]any{"ip": "10.66.0.4"},
			"d3":                   map[string]any{"ip": "10.66.0.5"},
			gatewayReserveDeviceID: map[string]any{"ip": wdttServerGatewayAddr},
		},
	})
	doc, _, err := preparePasswordsJSONForServer(dir, "main", "", "", []instancestore.ServerUser{
		{Password: "client1"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Devices["d2"]; ok {
		t.Fatalf("ничьё устройство осталось: %#v", doc.Devices)
	}
	if _, ok := doc.Devices["d1"]; !ok {
		t.Fatalf("устройство живого абонента снято: %#v", doc.Devices)
	}
	if _, ok := doc.Devices["d3"]; !ok {
		t.Fatalf("устройство по legacy device_id снято: %#v", doc.Devices)
	}
	if deviceIPFromPasswordsEntry(doc.Devices[gatewayReserveDeviceID]) != wdttServerGatewayAddr {
		t.Fatalf("резерв шлюза снят прополкой: %#v", doc.Devices)
	}
}

func TestPreparePasswordsJSON_RemembersExpiryOverEmptyFile(t *testing.T) {
	dir := t.TempDir()
	expires := time.Now().Add(time.Hour).Unix()
	// Записи в файле нет: её удалил янитор сервера.
	doc, _, err := preparePasswordsJSONForServer(dir, "main", "", "", []instancestore.ServerUser{
		{Password: "client1", ExpiresAt: expires},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Passwords["client1"].ExpiresAt; got != expires {
		t.Fatalf("expires_at = %d, ожидался запомненный срок %d", got, expires)
	}
}
