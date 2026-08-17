package wdtt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSanitizePasswordsDevices_RemovesGatewayIP(t *testing.T) {
	devices := map[string]any{
		"good": map[string]any{"ip": "10.66.0.2"},
		"bad":  map[string]any{"ip": DefaultWdttServerGatewayAddr},
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
	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwordsJSONPath(dir), data, 0600); err != nil {
		t.Fatal(err)
	}
	doc, sanitized, err := preparePasswordsJSONForServer(dir, "main", "", "", []ServerClient{
		{Password: "client1", Comment: "Иван"},
	})
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
		if deviceIPFromPasswordsEntry(entry) != DefaultWdttServerGatewayAddr {
			t.Fatalf("unexpected ip: %#v", entry)
		}
	}
	if len(reserveGatewayIPInDevices(out)) != 1 {
		t.Fatal("duplicate reservation")
	}
}

func TestSyncPasswordsJSON_DropsGatewayDevice(t *testing.T) {
	dir := t.TempDir()
	existing := passwordsJSON{
		MainPassword: "main",
		// Владелец у "bad" обязателен: без него устройство снимает прополка
		// сирот, и тест перестаёт сторожить сам sanitize.
		Passwords: map[string]passwordsJSONUser{
			"client1": {DeviceIDs: []string{"bad"}},
		},
		Devices: map[string]any{
			"bad": map[string]any{"ip": DefaultWdttServerGatewayAddr},
		},
	}
	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "passwords.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	sanitized, err := syncPasswordsJSON(dir, "main", "", "", []ServerClient{{Password: "client1"}})
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
	if deviceIPFromPasswordsEntry(out.Devices["__awgm_gateway_reserved__"]) != DefaultWdttServerGatewayAddr {
		t.Fatalf("reservation missing: %#v", out.Devices)
	}
}

// TestSyncPasswordsJSON_CreatesOwnerOnlyFile сторожит права СОЗДАВАЕМОГО файла:
// в нём лежат пароли абонентов. Каталог обязан быть пустым — os.WriteFile права
// уже существующего файла не меняет.
func TestSyncPasswordsJSON_CreatesOwnerOnlyFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := syncPasswordsJSON(dir, "main", "", "", []ServerClient{{Password: "client1"}}); err != nil {
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
// Собственный резерв (gatewayReserveDeviceID) кладётся при каждой записи файла и
// снимается при следующей — без исключения для него бит истинен всегда, то есть
// не несёт информации, и журналировать его нельзя (ровно этим он был выключен в
// задаче 2). Мутация «считать и свой резерв» роняет второй прогон.
func TestSyncPasswordsJSON_SanitizedIgnoresOwnGatewayReservation(t *testing.T) {
	dir := t.TempDir()
	clients := []ServerClient{{Password: "client1"}}

	first, err := syncPasswordsJSON(dir, "main", "", "", clients)
	if err != nil {
		t.Fatal(err)
	}
	if first {
		t.Fatal("первая запись: sanitized = true на пустом файле")
	}
	// Со второй записи в файле лежит наш резерв — и только он.
	for i := 2; i <= 3; i++ {
		got, err := syncPasswordsJSON(dir, "main", "", "", clients)
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
	doc.Devices["dev-abonenta"] = map[string]any{"ip": DefaultWdttServerGatewayAddr}
	writePasswordsFixture(t, dir, doc)
	got, err := syncPasswordsJSON(dir, "main", "", "", clients)
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
	if err := os.WriteFile(passwordsJSONPath(dir), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// TestUsableServerClients_FollowsReasonClassifier сторожит связь предиката и
// классификатора: отбор обязан идти ТОЛЬКО через ServerClientUnusableReason.
// Мутация «добавить условие прямо в UsableServerClients, мимо классификатора»
// роняет тест — иначе потребитель причины (тексты отказов в internal/api)
// назвал бы для такого абонента чужую причину, и ни один тест бы не упал.
func TestUsableServerClients_FollowsReasonClassifier(t *testing.T) {
	now := time.Unix(1700000000, 0)
	const main = "adminpass"
	clients := []ServerClient{
		{Password: "abonent1"},
		{Password: "botpass", ExpiresAt: now.Add(time.Hour).Unix()},
		{Password: "  spaced  "},
		{Password: "   "},
		{Password: main},
		{Password: "stale", ExpiresAt: now.Add(-time.Hour).Unix()},
	}

	inUsable := map[string]bool{}
	for _, c := range UsableServerClients(clients, main, now) {
		inUsable[c.Password] = true
	}
	for _, c := range clients {
		want := ServerClientUnusableReason(c, main, now) == ServerClientUsable
		if got := inUsable[strings.TrimSpace(c.Password)]; got != want {
			t.Fatalf("абонент %q: предикат = %v, классификатор = %v (%q) — отбор идёт мимо классификатора",
				c.Password, got, want, ServerClientUnusableReason(c, main, now))
		}
	}
}

func TestServerClientUnusableReason_NamesEachCondition(t *testing.T) {
	now := time.Unix(1700000000, 0)
	cases := []struct {
		name   string
		client ServerClient
		want   ServerClientReason
	}{
		{"рабочий", ServerClient{Password: "abonent1"}, ServerClientUsable},
		{"пустой пароль", ServerClient{Password: "   "}, ServerClientEmptyPassword},
		{"главный пароль", ServerClient{Password: " adminpass "}, ServerClientMainPassword},
		{"просрочен", ServerClient{Password: "stale", ExpiresAt: now.Add(-time.Second).Unix()}, ServerClientExpired},
		{"бессрочный", ServerClient{Password: "forever", ExpiresAt: 0}, ServerClientUsable},
	}
	for _, tc := range cases {
		if got := ServerClientUnusableReason(tc.client, "adminpass", now); got != tc.want {
			t.Fatalf("%s: причина = %q, ожидалась %q", tc.name, got, tc.want)
		}
	}
}

func TestUsableServerClients_SkipsEmptyMainAndExpired(t *testing.T) {
	now := time.Unix(1700000000, 0)
	got := UsableServerClients([]ServerClient{
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

func TestPreparePasswordsJSON_SkipsExpiredClient(t *testing.T) {
	dir := t.TempDir()
	doc, _, err := preparePasswordsJSONForServer(dir, "  main  ", "", "", []ServerClient{
		{Password: "dead", ExpiresAt: time.Now().Add(-time.Hour).Unix()},
		{Password: "alive", ExpiresAt: time.Now().Add(time.Hour).Unix()},
	})
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

func TestPreparePasswordsJSON_KeepsLiveFieldsOfExistingClient(t *testing.T) {
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
			// Абонент бота: имени в нашем конфиге нет, и затирать его нечем.
			"client2": {Label: "имя из бота"},
		},
	})
	doc, _, err := preparePasswordsJSONForServer(dir, "main", "", "", []ServerClient{
		{Password: "client1", Comment: "Иван"},
		{Password: "client2"},
	})
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
		t.Fatalf("label = %q, пустое имя в конфиге затёрло имя из файла", label)
	}
}

func TestPreparePasswordsJSON_WritesLabelNotComment(t *testing.T) {
	dir := t.TempDir()
	doc, _, err := preparePasswordsJSONForServer(dir, "main", "", "", []ServerClient{
		{Password: "client1", Comment: "Иван"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Смотрим на СЕРИАЛИЗОВАННЫЙ вид: подмену ключа сравнение полей не поймает.
	// Сериализуем только записи абонентов — у резерва шлюза в devices свой
	// ключ "comment", к контракту PasswordEntry отношения не имеющий.
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
			"d1":                        map[string]any{"ip": "10.66.0.2"},
			"d2":                        map[string]any{"ip": "10.66.0.4"},
			"d3":                        map[string]any{"ip": "10.66.0.5"},
			"__awgm_gateway_reserved__": map[string]any{"ip": DefaultWdttServerGatewayAddr},
		},
	})
	doc, _, err := preparePasswordsJSONForServer(dir, "main", "", "", []ServerClient{
		{Password: "client1"},
	})
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
	if deviceIPFromPasswordsEntry(doc.Devices["__awgm_gateway_reserved__"]) != DefaultWdttServerGatewayAddr {
		t.Fatalf("резерв шлюза снят прополкой: %#v", doc.Devices)
	}
}

func TestPreparePasswordsJSON_RemembersExpiryOverEmptyFile(t *testing.T) {
	dir := t.TempDir()
	expires := time.Now().Add(time.Hour).Unix()
	// Записи в файле нет: её удалил янитор сервера.
	doc, _, err := preparePasswordsJSONForServer(dir, "main", "", "", []ServerClient{
		{Password: "client1", ExpiresAt: expires},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Passwords["client1"].ExpiresAt; got != expires {
		t.Fatalf("expires_at = %d, ожидался запомненный срок %d", got, expires)
	}
}
