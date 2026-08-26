package instancestore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

type seedEnv struct {
	st    *Store
	deps  SeedDeps
	lives map[string][]string
	asked []string
}

func newSeedEnv(t *testing.T) *seedEnv {
	t.Helper()
	dir := t.TempDir()
	e := &seedEnv{st: New(dir), lives: map[string][]string{}}
	e.deps = SeedDeps{
		WdttPath:     filepath.Join(dir, "wdtt.json"),
		FreeturnPath: filepath.Join(dir, "freeturn.json"),
		RuntimeDir:   filepath.Join(dir, "run"),
		GOARCH:       "arm64",
		LivePermits: func(_ context.Context, iface string) ([]string, error) {
			e.asked = append(e.asked, iface)
			return e.lives[iface], nil
		},
		AllocIndex: func(_ string, pinned int, havePin bool) (int, error) {
			if havePin {
				return pinned, nil
			}
			return 30, nil
		},
	}
	return e
}

// byKey — записи посева по глобальному адресу (роль:id).
func byKey(res SeedResult) map[string]Record {
	m := map[string]Record{}
	for _, r := range res.State.Records {
		m[r.Key()] = r
	}
	return m
}

// samePermits — сравнение permit'ов ПО ЗНАЧЕНИЮ: Order — указатель, и `==`
// сравнивал бы адреса, то есть проходил бы всегда мимо позиции.
func samePermits(a, b []roles.PolicyPermit) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
		switch {
		case a[i].Order == nil && b[i].Order == nil:
		case a[i].Order == nil || b[i].Order == nil:
			return false
		case *a[i].Order != *b[i].Order:
			return false
		}
	}
	return true
}

const oldWdttJSON = `{
  "clients": [{"id":"default","name":"Клиент","config":{
    "enabled":true,"listen":"127.0.0.1:9000","peer":"9.9.9.9:1",
    "password":"pw","vkHashes":"h","workers":24,"connMode":"raw",
    "peerWg":"1.1.1.1:56000","peerRaw":"2.2.2.2:56003","sub":"https://wsub",
    "ndmsIface":"OpkgTun18","rawIface":"opkgtun18",
    "rawClientIp":"10.70.0.5","rawClientMTU":1300,
    "policyPermits":[{"name":"Policy1","order":1},{"name":"Policy2","order":2}]}}],
  "servers": [{"id":"default","name":"Сервер","config":{
    "enabled":false,"listen":"0.0.0.0:56002","password":"spw",
    "relayMode":"raw","natMode":"full","debug":true,
    "linkPeer":"77.1.2.3:56002","linkVkHashes":"lvh","statsLog":"disk",
    "clients":[{"password":"u1","comment":"Петя","expiresAt":42,"auto":false},
               {"password":"u2","comment":"","auto":true}],
    "ndmsIface":"OpkgTun20","wgIface":"opkgtun20",
    "rawNdmsIface":"OpkgTun21","rawIface":"opkgtun21"}}]
}`

const oldFreeturnJSON = `{
  "version": 2,
  "clients": [{"id":"default","name":"FT","config":{
    "enabled":true,"listen":"127.0.0.1:9001","peer":"3.3.3.3:56000",
    "provider":"vk","streams":10,"transport":"tcp","mode":"udp",
    "obfProfile":"none","sub":"https://sub.example"}}],
  "servers": [{"id":"default","name":"FTS","config":{
    "enabled":false,"listen":"0.0.0.0:56000","mode":"udp","obfProfile":"none"}}]
}`

// v1 — до 2026-07-21: singular client/server, version отсутствует (B6).
const oldFreeturnV1JSON = `{
  "client": {"enabled":true,"listen":"127.0.0.1:9002","peer":"4.4.4.4:56000",
    "provider":"vk","streams":10,"transport":"tcp","mode":"udp","obfProfile":"none"},
  "server": {"enabled":false,"listen":"0.0.0.0:56010","mode":"udp","obfProfile":"none"}
}`

func TestSeedMapsAllFourRoles(t *testing.T) {
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, oldWdttJSON)
	writeFile(t, e.deps.FreeturnPath, oldFreeturnJSON)
	e.lives["OpkgTun18"] = []string{"Policy2", "Policy3"}

	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SeededNow || !res.State.Seeded {
		t.Fatal("посев обязан пометить состояние")
	}
	if len(res.State.Records) != 4 {
		t.Fatalf("записей %d, ждали 4", len(res.State.Records))
	}
	rec := byKey(res)
	c, err := rec["wdtt-client:default"].WdttClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != "raw" || c.Peer != "2.2.2.2:56003" {
		t.Fatalf("режим/peer из слота: %+v", c)
	}
	if c.NdmsIface != "OpkgTun18" || c.RawIface != "opkgtun18" {
		t.Fatalf("пины не сохранились: %+v", c)
	}
	// Намерение членства = live ∪ cache, без дублей, порядок кэша первичен.
	// Кэш несёт СТАРЫЙ order (приоритет кандидатуры, Г-1 №2); live-довесок —
	// позиция не закреплена (nil, в хвост).
	want := []roles.PolicyPermit{{Name: "Policy1", Order: orderPtr(1)},
		{Name: "Policy2", Order: orderPtr(2)}, {Name: "Policy3"}}
	if !samePermits(c.Policies, want) {
		t.Fatalf("policies = %+v, ждали %+v", c.Policies, want)
	}
	if rec["wdtt-client:default"].Sub != "https://wsub" {
		t.Fatal("sub wdtt-клиента — в метаданные записи")
	}
	// RawClientIP/RawClientMTU — кэш факта, в новый мир НЕ едут (§9):
	// в roles-конфиге для них нет полей — проверка компилятором.
	srv := rec["wdtt-server:default"]
	sc, err := srv.WdttServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if sc.RelayMode != "raw" || sc.OpenFirewall != true || !sc.Debug {
		t.Fatalf("сервер: %+v (openFirewall nil → true; Debug — тумблер пользователя, Г-1)", sc)
	}
	// Г-1 №4: пустой configDir фиксируется СТАРОЙ формой пути.
	if want := filepath.Join(filepath.Dir(e.deps.WdttPath), "wdtt", "server", "default"); sc.ConfigDir != want {
		t.Fatalf("configDir = %q, ждали %q", sc.ConfigDir, want)
	}
	// Г-1 №1: оба слота peer пережили посев.
	cli := rec["wdtt-client:default"]
	if cli.PeerWg != "1.1.1.1:56000" || cli.PeerRaw != "2.2.2.2:56003" {
		t.Fatalf("слоты peer: wg=%q raw=%q", cli.PeerWg, cli.PeerRaw)
	}
	ft, err := rec["freeturn-client:default"].FreeTurnClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if ft.Peer != "3.3.3.3:56000" || ft.Sub != "https://sub.example" {
		t.Fatalf("freeturn: %+v (Sub — поле конфига, B4)", ft)
	}
	if srv.Enabled {
		t.Fatal("enabled сервера обязан быть false")
	}
}

func TestSeedKeepsPolicyOrderZeroFromOldConfig(t *testing.T) {
	// ЛОВУШКА МИГРАЦИИ №1. Старая форма — OpkgPolicyPermit{Order int
	// json:"order"} БЕЗ omitempty (wdtt/types.go:68-71): `"order": 0` лежит на
	// диске у каждого, кто поднял прокси-выход первым в политике, выше
	// провайдера. Ноль обязан доехать как закреплённая позиция (&0), а не как
	// «не закреплено» (nil): иначе выход после апгрейда уезжает в хвост и
	// перестаёт быть кандидатом в маршрут по умолчанию — молча.
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, `{"clients":[{"id":"z","name":"Z","config":{
	  "enabled":true,"listen":"127.0.0.1:9000","peer":"1.1.1.1:1","password":"pw",
	  "vkHashes":"h","connMode":"raw","peerRaw":"1.1.1.1:2",
	  "ndmsIface":"OpkgTun18","rawIface":"opkgtun18",
	  "policyPermits":[{"name":"Верхняя","order":0},{"name":"Вторая","order":1}]}}]}`)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	c, err := byKey(res)["wdtt-client:z"].WdttClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := []roles.PolicyPermit{{Name: "Верхняя", Order: orderPtr(0)},
		{Name: "Вторая", Order: orderPtr(1)}}
	if !samePermits(c.Policies, want) {
		t.Fatalf("policies = %+v, ждали %+v (order 0 — ВЕРХ политики, не «не задано»)", c.Policies, want)
	}
}

func TestSeedOpenFirewallAbsentOrNullMeansOn(t *testing.T) {
	// ЛОВУШКА МИГРАЦИИ №2. В старом мире OpenFirewall — *bool с семантикой
	// «nil = true» у ОБЕИХ серверных ролей (wdtt/types.go, freeturn/types.go).
	// В новом конфиге поле обычный bool: отсутствующий или null-ключ обязан
	// читаться как true, иначе у всех, кто тумблер не трогал, порт закроется
	// молча — «сервер перестал принимать абонентов после обновления».
	cases := []struct {
		name string
		key  string // хвост JSON-объекта, "" — ключа нет вовсе
		want bool
	}{
		{"ключа нет", "", true},
		{"null", `,"openFirewall":null`, true},
		{"явное true", `,"openFirewall":true`, true},
		{"явное false", `,"openFirewall":false`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newSeedEnv(t)
			writeFile(t, e.deps.WdttPath, `{"servers":[{"id":"s","name":"S","config":{
			  "listen":"0.0.0.0:56002","password":"spw",
			  "ndmsIface":"OpkgTun20","wgIface":"opkgtun20",
			  "rawNdmsIface":"OpkgTun21","rawIface":"opkgtun21"`+tc.key+`}}]}`)
			writeFile(t, e.deps.FreeturnPath, `{"version":2,"servers":[{"id":"s","name":"FS","config":{
			  "listen":"0.0.0.0:56000","mode":"udp"`+tc.key+`}}]}`)
			res, err := Seed(context.Background(), e.st, e.deps)
			if err != nil {
				t.Fatal(err)
			}
			rec := byKey(res)
			ws, err := rec["wdtt-server:s"].WdttServerConfig()
			if err != nil {
				t.Fatal(err)
			}
			if ws.OpenFirewall != tc.want {
				t.Fatalf("wdtt-server openFirewall = %v, ждали %v", ws.OpenFirewall, tc.want)
			}
			fs, err := rec["freeturn-server:s"].FreeTurnServerConfig()
			if err != nil {
				t.Fatal(err)
			}
			if fs.OpenFirewall != tc.want {
				t.Fatalf("freeturn-server openFirewall = %v, ждали %v", fs.OpenFirewall, tc.want)
			}
		})
	}
}

func TestSeedMigratesServerUsersAndLinkMeta(t *testing.T) {
	// B5 (потеря пользовательских данных) + замечание 4 ревью А.
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, oldWdttJSON)
	e.lives["OpkgTun18"] = nil
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	var srv Record
	for _, r := range res.State.Records {
		if r.Kind == KindWdttServer {
			srv = r
		}
	}
	if len(srv.Users) != 2 {
		t.Fatalf("абоненты не посеяны: %+v", srv.Users)
	}
	if srv.Users[0].Password != "u1" || srv.Users[0].ExpiresAt != 42 || srv.Users[0].Comment != "Петя" {
		t.Fatalf("поля абонента: %+v (ExpiresAt терять нельзя — воскресит отозванный доступ)", srv.Users[0])
	}
	if !srv.Users[1].Auto {
		t.Fatal("флаг auto обязан пережить посев (вычислить его нечем)")
	}
	if srv.LinkPeer != "77.1.2.3:56002" || srv.LinkVKHashes != "lvh" || srv.StatsLog != "disk" {
		t.Fatalf("link-метаданные: %+v", srv)
	}
}

func TestSeedReadsFreeturnV1(t *testing.T) {
	// B6: v1-файл без version; молчаливый ноль инстансов — потеря класса
	// требования 1, только в форме файла, а не в пофайловом continue.
	e := newSeedEnv(t)
	writeFile(t, e.deps.FreeturnPath, oldFreeturnV1JSON)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	rec := byKey(res)
	fc, err := rec["freeturn-client:default"].FreeTurnClientConfig()
	if err != nil || fc.Peer != "4.4.4.4:56000" || fc.Listen != "127.0.0.1:9002" {
		t.Fatalf("v1-клиент: %+v %v", fc, err)
	}
	fs, err := rec["freeturn-server:default"].FreeTurnServerConfig()
	if err != nil || fs.Listen != "0.0.0.0:56010" {
		t.Fatalf("v1-сервер: %+v %v", fs, err)
	}
}

func TestSeedFreeturnV1EmptySeedsNothing(t *testing.T) {
	// G1: осознанное расхождение со старым мигратором (migrate.go:21-23,37-39).
	// Тот достраивал дефолтного клиента с 127.0.0.1:9000 из воздуха; после
	// слияния подсистем эта пустышка дралась за порт с клиентом wdtt, у
	// которого дефолт тот же. Переносить из пустого файла нечего.
	e := newSeedEnv(t)
	writeFile(t, e.deps.FreeturnPath, `{}`)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.State.Records) != 0 {
		t.Fatalf("пустой v1-файл не должен рождать инстансы: %+v", res.State.Records)
	}
}

func TestSeedFreeturnV2EmptyListsSeedNothing(t *testing.T) {
	// Осознанное расхождение со старым мигратором: тот достраивал дефолтные
	// инстансы и при version>=2 с пустыми списками (migrate.go:6 — условие
	// требует ОБА списка непустыми). Новый мир разрешает ноль инстансов, и
	// подкладывать пользователю выключенный клиент из воздуха незачем.
	e := newSeedEnv(t)
	writeFile(t, e.deps.FreeturnPath, `{"version":2,"clients":[],"servers":[]}`)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.State.Records) != 0 {
		t.Fatalf("v2 с пустыми списками не должен рождать инстансы: %+v", res.State.Records)
	}
}

// Файл с разбираемым префиксом и полем НЕВЕРНОГО ТИПА: encoding/json успевает
// заполнить clients и только потом возвращает ошибку. Синтаксический мусор для
// этого не годится — checkValid отвергает документ целиком, не тронув приёмник.
const typeMismatchWdttJSON = `{
  "clients": [{"id":"half","name":"Половина","config":{
    "enabled":true,"listen":"127.0.0.1:9000","peer":"9.9.9.9:1",
    "password":"pw","vkHashes":"h"}}],
  "servers": 5
}`

func TestSeedSkipsUnparsableOldFile(t *testing.T) {
	// Битый старый файл никто не починит: отказ посева на нём не поднимал бы
	// прокси-подсистему НИКОГДА (амендмент D). Поэтому файл пропускается с
	// причиной, а посев идёт дальше по второму источнику.
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, typeMismatchWdttJSON)
	writeFile(t, e.deps.FreeturnPath, oldFreeturnJSON)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatalf("битый wdtt.json обязан быть пропуском, а не отказом посева: %v", err)
	}
	recs := byKey(res)
	if _, ok := recs["freeturn-client:default"]; !ok {
		t.Fatalf("инстансы читаемого источника обязаны перенестись: %v", recs)
	}
	for k := range recs {
		if strings.HasPrefix(k, "wdtt") {
			t.Fatalf("половина разобранного файла не имеет права уехать в записи: %s", k)
		}
	}
	if len(res.State.SkippedSources) != 1 ||
		res.State.SkippedSources[0].File != "wdtt.json" ||
		res.State.SkippedSources[0].Reason == "" {
		t.Fatalf("пропуск с причиной обязан лечь в состояние: %+v", res.State.SkippedSources)
	}
	if len(res.State.SeededFrom) != 1 || res.State.SeededFrom[0] != "freeturn.json" {
		t.Fatalf("пропущенный файл не источник: %v", res.State.SeededFrom)
	}
}

func TestSeedSkipIsRecordedOnDiskAndNeverRetried(t *testing.T) {
	// Оба файла биты: переносить нечего, но отметка посева обязана лечь — иначе
	// следующий боот пошёл бы по второму кругу, и так бесконечно. Список
	// пропусков лежит на диске: без него сообщение исчезло бы после первого же
	// перезапуска, и пользователь не узнал бы, что его инстансы потеряны.
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, typeMismatchWdttJSON)
	writeFile(t, e.deps.FreeturnPath, "{нет")
	first, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.State.SkippedSources) != 2 {
		t.Fatalf("пропущены оба источника: %+v", first.State.SkippedSources)
	}
	if len(first.State.SeededFrom) != 1 || first.State.SeededFrom[0] != "clean-install" {
		t.Fatalf("отметка посева обязана лечь и когда переносить нечего: %v", first.State.SeededFrom)
	}

	// Второй боот: Store ничего не кэширует, повторный Seed читает тот же файл
	// с диска — это и есть перезапуск.
	second, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if second.SeededNow {
		t.Fatal("посев обязан быть однократным: битый файл ретраить нечем")
	}
	if len(second.State.SkippedSources) != 2 ||
		second.State.SkippedSources[0].File != "wdtt.json" ||
		second.State.SkippedSources[1].File != "freeturn.json" {
		t.Fatalf("пропуски обязаны пережить перезапуск: %+v", second.State.SkippedSources)
	}
}

func TestSeedFailsClosedOnUnreadableOldFile(t *testing.T) {
	// Третий исход чтения (форма «файл есть, но прочитать нельзя»): между
	// «файла нет» и «файл разобран» лежит отказ ввода-вывода. Трактовка его
	// как чистой установки списала бы все инстансы пользователя, а флаг посева
	// закрыл бы дорогу второй попытке.
	e := newSeedEnv(t)
	if err := os.Mkdir(e.deps.WdttPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Seed(context.Background(), e.st, e.deps); err == nil {
		t.Fatal("нечитаемый wdtt.json обязан быть ошибкой посева, а не чистой установкой")
	}
	if _, statErr := os.Stat(e.st.path); !os.IsNotExist(statErr) {
		t.Fatal("при отказе чтения store-файл не должен появляться")
	}
}

func TestSeedFailsClosedOnCorruptStore(t *testing.T) {
	// Битый proxy-instances.json — не чистый лист: посев на нём затёр бы
	// живые записи копией старых файлов.
	e := newSeedEnv(t)
	writeFile(t, e.st.path, "{нет")
	writeFile(t, e.deps.WdttPath, oldWdttJSON)
	if _, err := Seed(context.Background(), e.st, e.deps); err == nil {
		t.Fatal("битый store обязан валить посев")
	}
}

func TestSeedFailsClosedWhenLivePermitsUnavailable(t *testing.T) {
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, oldWdttJSON)
	boom := errors.New("rci down")
	e.deps.LivePermits = func(context.Context, string) ([]string, error) { return nil, boom }
	if _, err := Seed(context.Background(), e.st, e.deps); !errors.Is(err, boom) {
		t.Fatalf("недоступное наблюдение откладывает посев (флаг только по успешному наблюдению, §9): %v", err)
	}
	if _, statErr := os.Stat(e.st.path); !os.IsNotExist(statErr) {
		t.Fatal("флаг/записи не должны ложиться без наблюдения")
	}
}

func TestSeedFailsClosedWhenAllocIndexFails(t *testing.T) {
	// Исчерпанный пул индексов — не повод посеять raw-клиента без пина:
	// запись без пина отвергнет валидация store, а посев с половиной
	// инстансов оставил бы пользователя с непредсказуемой конфигурацией.
	e := newSeedEnv(t)
	boom := errors.New("пул исчерпан")
	e.deps.AllocIndex = func(string, int, bool) (int, error) { return 0, boom }
	writeFile(t, e.deps.WdttPath, oldWdttJSON)
	if _, err := Seed(context.Background(), e.st, e.deps); !errors.Is(err, boom) {
		t.Fatalf("отказ аллокатора обязан валить посев: %v", err)
	}
	if _, statErr := os.Stat(e.st.path); !os.IsNotExist(statErr) {
		t.Fatal("при отказе аллокатора store-файл не должен появляться")
	}
}

func TestSeedRepinsOutOfRangeIndexOnMips(t *testing.T) {
	e := newSeedEnv(t)
	e.deps.GOARCH = "mipsle"
	e.deps.AllocIndex = func(owner string, pinned int, havePin bool) (int, error) {
		if havePin {
			t.Fatalf("перепин обязан идти БЕЗ старого пина (вне диапазона), пришло %d", pinned)
		}
		return 3, nil
	}
	writeFile(t, e.deps.WdttPath, oldWdttJSON)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	var c Record
	for _, r := range res.State.Records {
		if r.Kind == KindWdttClient {
			c = r
		}
	}
	cfg, _ := c.WdttClientConfig()
	if cfg.NdmsIface != "OpkgTun3" || cfg.RawIface != "opkgtun3" {
		t.Fatalf("перепин на mips: %+v", cfg)
	}
	for _, name := range e.asked {
		if name == "OpkgTun18" {
			t.Fatal("live-permits по недостижимому имени спрашивать нельзя")
		}
	}
}

func TestSeedKeepsOpkgTun0PinOnMips(t *testing.T) {
	// Щ13: ноль — законный индекс на mips (диапазон 0..15); сентинел «пина
	// нет» не имеет права совпадать с ним.
	e := newSeedEnv(t)
	e.deps.GOARCH = "mipsle"
	json0 := `{"clients":[{"id":"z","name":"Z","config":{
	  "enabled":true,"listen":"127.0.0.1:9000","peer":"1.1.1.1:1","password":"pw",
	  "vkHashes":"h","connMode":"raw","peerRaw":"1.1.1.1:2",
	  "ndmsIface":"OpkgTun0","rawIface":"opkgtun0"}}]}`
	writeFile(t, e.deps.WdttPath, json0)
	sawPin := false
	e.deps.AllocIndex = func(_ string, pinned int, havePin bool) (int, error) {
		if havePin && pinned == 0 {
			sawPin = true
		}
		if havePin {
			return pinned, nil
		}
		return 5, nil
	}
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if !sawPin {
		t.Fatal("пин OpkgTun0 обязан прийти как (0, havePin=true)")
	}
	cfg, _ := res.State.Records[0].WdttClientConfig()
	if cfg.NdmsIface != "OpkgTun0" {
		t.Fatalf("OpkgTun0 перепинован: %+v (потеря permit'ов класса B2)", cfg)
	}
	found := false
	for _, name := range e.asked {
		if name == "OpkgTun0" {
			found = true
		}
	}
	if !found {
		t.Fatal("live-permits обязаны спрашиваться по сохранённому пину OpkgTun0")
	}
}

func TestSeedUnparsablePinAsksFreshIndex(t *testing.T) {
	// Формы имени интерфейса на диске: ключа нет; пусто; чужое имя (legacy
	// wdtt0); префикс без числа; отрицательное; нечисловой хвост. Ни одна не
	// имеет права стать пином — иначе OpkgTun-имя собирается из мусора.
	for _, name := range []string{"", "   ", "wdtt0", "OpkgTun", "OpkgTun-1", "OpkgTunX", "opkgtun18"} {
		t.Run("iface="+name, func(t *testing.T) {
			e := newSeedEnv(t)
			gotHavePin := true
			e.deps.AllocIndex = func(_ string, _ int, havePin bool) (int, error) {
				gotHavePin = havePin
				return 30, nil
			}
			writeFile(t, e.deps.WdttPath, `{"clients":[{"id":"z","name":"Z","config":{
			  "enabled":true,"listen":"127.0.0.1:9000","peer":"1.1.1.1:1","password":"pw",
			  "vkHashes":"h","connMode":"raw","peerRaw":"1.1.1.1:2",
			  "ndmsIface":"`+name+`"}}]}`)
			res, err := Seed(context.Background(), e.st, e.deps)
			if err != nil {
				t.Fatal(err)
			}
			if gotHavePin {
				t.Fatalf("имя %q — не пин, аллокатор обязан получить havePin=false", name)
			}
			cfg, _ := res.State.Records[0].WdttClientConfig()
			if cfg.NdmsIface != "OpkgTun30" || cfg.RawIface != "opkgtun30" {
				t.Fatalf("новый пин не проставлен: %+v", cfg)
			}
			if len(e.asked) != 0 {
				t.Fatalf("у перепиненного интерфейса живых permit'ов не бывает, спросили %v", e.asked)
			}
		})
	}
}

func TestSeedTrimsWhitespaceForms(t *testing.T) {
	// Форма «значение с пробелами»: руками правленный файл и старые записи.
	// Непотримленный режим «  raw  » не равен "raw" — клиент уехал бы в wg
	// без пина; непотримленное имя OpkgTun не распознаётся как пин.
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, `{"clients":[{"id":"z","name":" Z ","config":{
	  "enabled":true,"listen":"127.0.0.1:9000","peer":"","password":"pw",
	  "vkHashes":"h","connMode":"  raw  ","peerRaw":"  2.2.2.2:56003  ",
	  "ndmsIface":"  OpkgTun18  ","rawIface":"opkgtun18"}}]}`)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := res.State.Records[0].WdttClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != "raw" {
		t.Fatalf("режим не потримлен: %q", cfg.Mode)
	}
	if cfg.NdmsIface != "OpkgTun18" || cfg.RawIface != "opkgtun18" {
		t.Fatalf("пробельный пин не распознан: %+v", cfg)
	}
	if cfg.Peer != "2.2.2.2:56003" {
		t.Fatalf("peer из слота не потримлен: %q", cfg.Peer)
	}
}

func TestSeedFallsBackToCommonPeerWhenSlotEmpty(t *testing.T) {
	// Форма «слот пуст»: конфиги, созданные до появления слотов, несут только
	// общий peer. Потеря адреса означала бы неподнимающийся клиент.
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, `{"clients":[{"id":"z","name":"Z","config":{
	  "enabled":true,"listen":"127.0.0.1:9000","peer":" 9.9.9.9:1 ","password":"pw",
	  "vkHashes":"h","connMode":"raw","ndmsIface":"OpkgTun18","rawIface":"opkgtun18"}}]}`)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := res.State.Records[0].WdttClientConfig()
	if cfg.Peer != "9.9.9.9:1" {
		t.Fatalf("peer = %q, ждали общий адрес", cfg.Peer)
	}
}

func TestSeedWgClientGetsNoPinAndNoLivePermits(t *testing.T) {
	// Форма «режим не задан» (старый дефолт ConnModeWG) и режим wg: NDMS-имени
	// у такого клиента не было никогда — выделять ему индекс значит занять
	// чужой слот пула и спросить permit'ы по несуществующему интерфейсу.
	for _, mode := range []string{"", "wg"} {
		t.Run("connMode="+mode, func(t *testing.T) {
			e := newSeedEnv(t)
			e.deps.AllocIndex = func(string, int, bool) (int, error) {
				t.Fatal("wg-клиенту индекс не выделяется")
				return 0, nil
			}
			writeFile(t, e.deps.WdttPath, `{"clients":[{"id":"z","name":"Z","config":{
			  "enabled":true,"listen":"127.0.0.1:9000","peer":"9.9.9.9:1","password":"pw",
			  "vkHashes":"h","connMode":"`+mode+`","peerWg":"1.1.1.1:56000"}}]}`)
			res, err := Seed(context.Background(), e.st, e.deps)
			if err != nil {
				t.Fatal(err)
			}
			cfg, _ := res.State.Records[0].WdttClientConfig()
			if cfg.Mode != "wg" {
				t.Fatalf("режим = %q, ждали дефолт wg", cfg.Mode)
			}
			if cfg.NdmsIface != "" || cfg.RawIface != "" || len(cfg.Policies) != 0 {
				t.Fatalf("wg-клиент получил raw-атрибуты: %+v", cfg)
			}
			if len(e.asked) != 0 {
				t.Fatalf("live-permits спрошены у wg-клиента: %v", e.asked)
			}
		})
	}
}

func TestSeedDropsEmptyAndDuplicatePermitNames(t *testing.T) {
	// Формы списка политик: пустое имя (мусор ручной правки) и дубль в кэше
	// либо между кэшем и наблюдением. Дубль permit'а в одной политике —
	// вторая запись в NDMS, пустое имя — команда без аргумента.
	e := newSeedEnv(t)
	e.lives["OpkgTun18"] = []string{"Policy1", "", "Policy9", "Policy9"}
	writeFile(t, e.deps.WdttPath, `{"clients":[{"id":"z","name":"Z","config":{
	  "enabled":true,"listen":"127.0.0.1:9000","peer":"1.1.1.1:1","password":"pw",
	  "vkHashes":"h","connMode":"raw","peerRaw":"1.1.1.1:2",
	  "ndmsIface":"OpkgTun18","rawIface":"opkgtun18",
	  "policyPermits":[{"name":"Policy1","order":2},{"name":"","order":3},
	                   {"name":"Policy1","order":7}]}}]}`)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := res.State.Records[0].WdttClientConfig()
	want := []roles.PolicyPermit{{Name: "Policy1", Order: orderPtr(2)}, {Name: "Policy9"}}
	if !samePermits(cfg.Policies, want) {
		t.Fatalf("policies = %+v, ждали %+v", cfg.Policies, want)
	}
}

func TestSeedLegacyKernelIfacesFallBackToWdtt0(t *testing.T) {
	// Вход одноразовой уборки непомеченных правил: у сервера, жившего до
	// NDMS-имён, kernel-интерфейсы назывались wdtt0/wdttraw0. Пустая ведомость
	// оставила бы их правила навсегда.
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, `{"servers":[{"id":"s","name":"S","config":{
	  "listen":"0.0.0.0:56002","password":"spw"}}]}`)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.LegacyKernelIfaces) != 2 ||
		res.LegacyKernelIfaces[0] != "wdtt0" || res.LegacyKernelIfaces[1] != "wdttraw0" {
		t.Fatalf("legacy-имена: %v", res.LegacyKernelIfaces)
	}

	e2 := newSeedEnv(t)
	writeFile(t, e2.deps.WdttPath, oldWdttJSON)
	e2.lives["OpkgTun18"] = nil
	res2, err := Seed(context.Background(), e2.st, e2.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.LegacyKernelIfaces) != 2 ||
		res2.LegacyKernelIfaces[0] != "opkgtun20" || res2.LegacyKernelIfaces[1] != "opkgtun21" {
		t.Fatalf("прежние kernel-имена: %v", res2.LegacyKernelIfaces)
	}
}

func TestSeedRecordsSourceFiles(t *testing.T) {
	// SeededFrom — и флаг, и след происхождения. Пустой список означает
	// «посева не было»: Store выводит Seeded именно из него, и молчаливо
	// пустой SeededFrom пустил бы посев по второму кругу.
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, oldWdttJSON)
	writeFile(t, e.deps.FreeturnPath, oldFreeturnJSON)
	e.lives["OpkgTun18"] = nil
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.State.SeededFrom) != 2 ||
		res.State.SeededFrom[0] != "wdtt.json" || res.State.SeededFrom[1] != "freeturn.json" {
		t.Fatalf("seededFrom = %v", res.State.SeededFrom)
	}

	clean := newSeedEnv(t)
	cres, err := Seed(context.Background(), clean.st, clean.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(cres.State.SeededFrom) != 1 || cres.State.SeededFrom[0] != "clean-install" {
		t.Fatalf("чистая установка: %v", cres.State.SeededFrom)
	}
}

func TestSeedIsIdempotentAndSkipsOldFilesWhenSeeded(t *testing.T) {
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, oldWdttJSON)
	writeFile(t, e.deps.FreeturnPath, oldFreeturnJSON)
	e.lives["OpkgTun18"] = []string{"Policy2"}
	first, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	// Старые файлы «испортились» после посева — второй вызов их не читает.
	writeFile(t, e.deps.WdttPath, "{нет")
	second, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if second.SeededNow {
		t.Fatal("повторный посев обязан быть no-op")
	}
	if len(second.State.Records) != len(first.State.Records) {
		t.Fatalf("состояние уплыло: %d != %d", len(second.State.Records), len(first.State.Records))
	}
	if len(second.OldGenProcs) != 0 {
		t.Fatal("pid-файлов нет — добивать нечего")
	}
}

// Отметка «уборка не доведена» и список прежних kernel-имён ложатся на диск
// ТОЙ ЖЕ транзакцией, что и записи: транзиентный отказ уборочных шагов
// (занятая блокировка iptables, недоступная команда) иначе навсегда съедал бы
// единственный шанс — следующий боот видит SeededNow=false. Пересобрать список
// на повторе неоткуда: старые конфиги к тому времени могут быть удалены.
func TestSeedMarksCleanupPendingAndPersistsLegacyIfaces(t *testing.T) {
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, `{"servers":[{"id":"s","name":"S","config":{
	  "listen":"0.0.0.0:56002","password":"spw"}}]}`)
	if err := os.MkdirAll(e.deps.RuntimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "wdtt-server-s.pid"), "321")
	first, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if !first.CleanupPending {
		t.Fatal("посев обязан поднять отметку уборки")
	}
	st, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !st.CleanupPending {
		t.Fatal("отметка уборки не легла на диск")
	}
	if !reflect.DeepEqual(st.LegacyKernelIfaces, []string{"wdtt0", "wdttraw0"}) {
		t.Fatalf("прежние kernel-имена не сохранены: %v", st.LegacyKernelIfaces)
	}
	if !reflect.DeepEqual(st.OldGenProcs, []OldGenProc{{PID: 321}}) {
		t.Fatalf("процессы старого поколения не сохранены: %+v", st.OldGenProcs)
	}

	// Повторный боот: посева нет, а уборка обязана быть повторена — с ТЕМИ ЖЕ
	// списками. Пересбор pid'ов с диска запрещён (B3): pid-файлы старого мира
	// никто не удаляет, лежат они на флеше и переживают перезагрузку, а номер
	// из протухшего файла система могла отдать процессу НОВОГО поколения.
	// Проверка имени бинаря тут не спасает — бинари у обоих миров одни и те же.
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "wdtt-server-other.pid"), "654")
	second, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if second.SeededNow || !second.CleanupPending {
		t.Fatalf("повтор: SeededNow=%v CleanupPending=%v", second.SeededNow, second.CleanupPending)
	}
	if !reflect.DeepEqual(second.LegacyKernelIfaces, []string{"wdtt0", "wdttraw0"}) {
		t.Fatalf("список имён не доехал до повтора: %v", second.LegacyKernelIfaces)
	}
	if !reflect.DeepEqual(second.OldGenProcs, []OldGenProc{{PID: 321}}) {
		t.Fatalf("список процессов пересобран с диска: %+v", second.OldGenProcs)
	}
}

// Снятие отметки — ОТДЕЛЬНАЯ транзакция, после успешного прохода шагов: в
// одной с посевом она обесценивала бы саму отметку.
func TestClearCleanupPendingStopsRepeats(t *testing.T) {
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, `{"servers":[{"id":"s","name":"S","config":{
	  "listen":"0.0.0.0:56002","password":"spw"}}]}`)
	// pid-файл обязан существовать ДО посева: иначе список процессов пуст уже
	// на входе, и страж «снятие отметки чистит и его» ничего не проверяет.
	if err := os.MkdirAll(e.deps.RuntimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "wdtt-server-s.pid"), "321")
	seeded, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded.OldGenProcs) != 1 {
		t.Fatalf("посев не собрал процессы, страж снятия пуст: %+v", seeded.OldGenProcs)
	}
	if err := ClearCleanupPending(e.st); err != nil {
		t.Fatal(err)
	}
	st, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.CleanupPending || len(st.LegacyKernelIfaces) != 0 || len(st.OldGenProcs) != 0 {
		t.Fatalf("отметка не снята: pending=%v ifaces=%v procs=%+v",
			st.CleanupPending, st.LegacyKernelIfaces, st.OldGenProcs)
	}
	if len(st.Records) != 1 {
		t.Fatalf("снятие отметки тронуло записи: %+v", st.Records)
	}

	next, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if next.CleanupPending || len(next.OldGenProcs) != 0 || len(next.LegacyKernelIfaces) != 0 {
		t.Fatalf("доведённая уборка повторяется: %+v", next)
	}
}

func TestSeedCleanInstall(t *testing.T) {
	e := newSeedEnv(t)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if !res.State.Seeded || len(res.State.Records) != 0 {
		t.Fatalf("чистая установка: %+v", res.State)
	}
}

func TestSeedPreservesExistingRecords(t *testing.T) {
	e := newSeedEnv(t)
	if _, err := e.st.Replace(func(st *State) error {
		st.Records = append(st.Records, Record{ID: "default", Kind: KindWdttClient,
			Name: "Уже есть", WdttClient: &roles.WdttClientConfig{Mode: "wg",
				Listen: "127.0.0.1:9000", Peer: "p:1", Password: "pw", VKHashes: "h"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, e.deps.WdttPath, oldWdttJSON)
	e.lives["OpkgTun18"] = nil
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.State.Records {
		if r.Key() == "wdtt-client:default" && r.Name != "Уже есть" {
			t.Fatal("существующая запись главнее посева")
		}
	}
}

func TestSeedCollectsOldGenerationPIDsOnly(t *testing.T) {
	e := newSeedEnv(t)
	if err := os.MkdirAll(e.deps.RuntimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "wdtt-client.pid"), "123")
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "freeturn-server-default.pid"), "456")
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "wdtt-broken.pid"), "мусор")
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "other.pid"), "789") // чужой префикс
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]bool{}
	for _, p := range res.OldGenProcs {
		got[p.PID] = true
	}
	if !got[123] || !got[456] || len(res.OldGenProcs) != 2 {
		t.Fatalf("процессы = %+v (чужие префиксы и мусор — мимо)", res.OldGenProcs)
	}
}

// Отпечаток снимается при посеве: номер + время старта из /proc (поле 22).
// Голый номер идентичностью не является — система его переиспользует, а
// сверка по имени бинаря не спасает, когда у старого и нового поколения один
// и тот же бинарь. У мёртвого номера отпечатка нет, и это законно: добивать
// там нечего.
func TestSeedTakesStartTimeFingerprint(t *testing.T) {
	e := newSeedEnv(t)
	if err := os.MkdirAll(e.deps.RuntimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "wdtt-live.pid"), strconv.Itoa(os.Getpid()))
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "wdtt-dead.pid"), "999999999")
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	want, ok := childproc.StartTime(os.Getpid())
	if !ok {
		t.Skip("/proc недоступен")
	}
	var seen int
	for _, p := range res.OldGenProcs {
		switch p.PID {
		case os.Getpid():
			seen++
			if p.StartTime != want {
				t.Fatalf("отпечаток живого процесса = %d, ждали %d", p.StartTime, want)
			}
		case 999999999:
			seen++
			if p.StartTime != 0 {
				t.Fatalf("у мёртвого номера взялся отпечаток %d", p.StartTime)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("процессы = %+v", res.OldGenProcs)
	}
}

func TestSeedSkipsNonPositiveAndUnreadablePIDs(t *testing.T) {
	// Формы содержимого pid-файла: ноль, отрицательное, пустой файл, каталог
	// вместо файла, не-.pid имя. Ноль и минус в kill(2) означают ГРУППУ
	// процессов — добивание по такому «pid» убило бы посторонних.
	e := newSeedEnv(t)
	if err := os.MkdirAll(e.deps.RuntimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "wdtt-zero.pid"), "0")
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "wdtt-neg.pid"), "-5")
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "wdtt-empty.pid"), "")
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "wdtt-client.log"), "777")
	if err := os.Mkdir(filepath.Join(e.deps.RuntimeDir, "wdtt-dir.pid"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.deps.RuntimeDir, "wdtt-ok.pid"), " 321\n")
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.OldGenProcs) != 1 || res.OldGenProcs[0].PID != 321 {
		t.Fatalf("процессы = %+v (ноль, минус, пустое и каталог — мимо)", res.OldGenProcs)
	}
}

func TestSeedMissingRuntimeDirIsNotAnError(t *testing.T) {
	// Форма «каталога нет»: чистая установка либо /tmp после ребута. Сбор
	// pid'ов — best-effort, ронять из-за него посев нечем.
	e := newSeedEnv(t)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.OldGenProcs != nil {
		t.Fatalf("процессы = %+v", res.OldGenProcs)
	}
}

func TestSeedSkipsLivePermitsWhenAllocatorMovedPin(t *testing.T) {
	// Пин лежит в диапазоне архитектуры, но занят ДРУГИМ владельцем, и
	// аллокатор выдаёт иной индекс. Спрашивать live-permits тут нельзя ни по
	// старому имени (оно больше не наше), ни по новому (интерфейса с ним на
	// роутере ещё не существует): RCI ответил бы ошибкой, посев валился бы на
	// каждом старте, и выйти из этого пользователь не может. Намерение
	// членства остаётся кэш-only — permit'ы восстановит уже применение.
	e := newSeedEnv(t)
	e.lives["OpkgTun18"] = []string{"ЧужаяПолитика"}
	e.lives["OpkgTun31"] = []string{"НесуществующийИнтерфейс"}
	e.deps.AllocIndex = func(_ string, pinned int, havePin bool) (int, error) {
		if !havePin || pinned != 18 {
			t.Fatalf("ждали заявку на сохранение пина 18, пришло (%d, havePin=%v)", pinned, havePin)
		}
		return 31, nil // пин занят другим владельцем — аллокатор подвинул
	}
	writeFile(t, e.deps.WdttPath, `{"clients":[{"id":"z","name":"Z","config":{
	  "enabled":true,"listen":"127.0.0.1:9000","peer":"1.1.1.1:1","password":"pw",
	  "vkHashes":"h","connMode":"raw","peerRaw":"1.1.1.1:2",
	  "ndmsIface":"OpkgTun18","rawIface":"opkgtun18",
	  "policyPermits":[{"name":"ИзКэша","order":0}]}}]}`)
	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.asked) != 0 {
		t.Fatalf("live-permits спрошены после сдвига пина: %v", e.asked)
	}
	cfg, _ := res.State.Records[0].WdttClientConfig()
	if cfg.NdmsIface != "OpkgTun31" || cfg.RawIface != "opkgtun31" {
		t.Fatalf("сдвинутый пин не применён: %+v", cfg)
	}
	want := []roles.PolicyPermit{{Name: "ИзКэша", Order: orderPtr(0)}}
	if !samePermits(cfg.Policies, want) {
		t.Fatalf("намерение членства обязано остаться кэш-only: %+v", cfg.Policies)
	}
}

// Полная фикстура: КАЖДОЕ поле старых форматов заполнено уникальным
// различимым значением. Проверка — сравнение записей ЦЕЛИКОМ, а не по
// интересным полям (B4: посев — последний ручной маппер полутора сотен полей
// в волне, и `Sub` уже терялся ровно так).
//
// Поля, которые не переносятся намеренно (rawClientIp, rawClientMTU и debug
// клиента, ingressEnabled сервера), здесь тоже заданы — но охраняет их не эта
// фикстура, а КОМПИЛЯТОР: приёмника нет ни в DTO старого формата, ни в конфиге
// роли, поэтому попытка их перенести просто не соберётся. Ключи стоят в
// фикстуре как документация реальной формы файла; если приёмник когда-нибудь
// появится, непреднамеренный перенос поймает сравнение записей.
const fullWdttJSON = `{
  "clients": [{"id":"cli-1","name":"Клиент раз","config":{
    "enabled":true,
    "listen":"127.0.0.1:9011",
    "peer":"common.example:9999",
    "password":"pw-client",
    "vkHashes":"vkh-client",
    "workers":27,
    "obfs":"video",
    "fingerprint":"firefox",
    "deviceId":"dev-client",
    "captchaMode":"wv",
    "vkAuthMode":"vkauth-client",
    "sub":"https://sub.wdtt.example/one",
    "connMode":"raw",
    "peerWg":"wg.example:56000",
    "peerRaw":"raw.example:56003",
    "ndmsIface":"OpkgTun18",
    "rawIface":"opkgtun18",
    "rawClientIp":"10.70.0.9",
    "rawClientMTU":1300,
    "debug":true,
    "policyPermits":[{"name":"CacheTop","order":0},{"name":"CacheSecond","order":4}]}},
   {"id":"cli-2","name":"Клиент два","config":{
    "enabled":false,
    "listen":"127.0.0.1:9014",
    "peer":"common2.example:9999",
    "password":"pw-client-2",
    "vkHashes":"vkh-client-2",
    "workers":9,
    "obfs":"audio",
    "fingerprint":"chrome",
    "deviceId":"dev-client-2",
    "captchaMode":"rjs",
    "vkAuthMode":"vkauth-client-2",
    "sub":"https://sub.wdtt.example/two",
    "connMode":"wg",
    "peerWg":"wg2.example:56000",
    "peerRaw":"raw2.example:56003"}}],
  "servers": [{"id":"srv-1","name":"Сервер раз","config":{
    "enabled":true,
    "listen":"0.0.0.0:56002",
    "wgPort":56001,
    "configDir":"/opt/etc/awgm/wdtt-srv-1",
    "password":"pw-server",
    "adminId":"admin-77",
    "botToken":"bot-token-77",
    "natIface":"ISP",
    "natMode":"internet-only",
    "natStaticWan":"ISP2",
    "policy":"PolicyForServer",
    "lanSegments":["Home","Guest"],
    "openFirewall":false,
    "relayMode":"raw",
    "rawListen":"0.0.0.0:56003",
    "directListen":"0.0.0.0:56004",
    "ndmsIface":"OpkgTun20",
    "wgIface":"opkgtun20",
    "rawNdmsIface":"OpkgTun21",
    "rawIface":"opkgtun21",
    "exposeToPolicies":true,
    "debug":true,
    "ingressEnabled":true,
    "clients":[{"password":"user-pw-1","comment":"Абонент один","vkHash":"vkhash-1",
                "expiresAt":1700000001,"auto":true}],
    "linkPeer":"link.example:56002",
    "linkVkHashes":"link-vkh",
    "statsLog":"disk"}}]
}`

const fullFreeturnJSON = `{
  "version": 2,
  "clients": [{"id":"ftc-1","name":"FT клиент","config":{
    "enabled":true,
    "listen":"127.0.0.1:9012",
    "peer":"ft.example:56000",
    "provider":"vk",
    "links":"https://vk.example/a,https://vk.example/b",
    "streams":12,
    "transport":"udp",
    "mode":"tcp",
    "bond":true,
    "turnHost":"turn.example",
    "turnPort":3478,
    "obfProfile":"rtpopus2",
    "obfKey":"ft-obf-key-client",
    "streamsPerCred":6,
    "platform":"mobile",
    "dnsMode":"doh",
    "dnsServers":"1.1.1.1:53,8.8.8.8",
    "clientId":"ft-client-id",
    "sub":"https://sub.ft.example/one",
    "debug":true}}],
  "servers": [{"id":"fts-1","name":"FT сервер","config":{
    "enabled":true,
    "listen":"0.0.0.0:56005",
    "connect":"127.0.0.1:9013",
    "mode":"tcp",
    "obfProfile":"rtpopus3",
    "obfKey":"ft-obf-key-server",
    "clientsFile":"/opt/etc/awgm/ft-clients.json",
    "debug":true,
    "openFirewall":false}}]
}`

// assertEveryFieldCarried — канарейка на ПОЛНОТУ фикстуры и маппинга: каждое
// экспортированное поле типа обязано быть ненулевым хотя бы в одном из
// переданных значений. Ловит и потерю существующего поля, и появление нового —
// того, которого в перечне сравнения ещё нет: сравнение записей знает только
// про поля, выписанные руками, а это — про ВСЕ.
//
// Союз, а не поштучная проверка, потому что у Record поштучная невозможна: три
// из четырёх указателей на конфиг ОБЯЗАНЫ быть nil в каждой записи. Для типов,
// где союз не нужен, передаётся один элемент — семантика та же.
// Исключения называются поимённо, с причиной.
func assertEveryFieldCarried(t *testing.T, what string, items []any, allowedZero ...string) {
	t.Helper()
	if len(items) == 0 {
		t.Fatalf("%s: канарейке нечего проверять", what)
	}
	allowed := map[string]bool{}
	for _, name := range allowedZero {
		allowed[name] = true
	}
	rt := reflect.TypeOf(items[0])
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() || allowed[f.Name] {
			continue
		}
		covered := false
		for _, it := range items {
			if !reflect.ValueOf(it).Field(i).IsZero() {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s: поле %s нулевое во всех проверяемых значениях — либо посев его не переносит, либо фикстура его не задаёт", what, f.Name)
		}
	}
}

func TestSeedCarriesEveryFieldOfEveryRole(t *testing.T) {
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, fullWdttJSON)
	writeFile(t, e.deps.FreeturnPath, fullFreeturnJSON)
	e.lives["OpkgTun18"] = []string{"LiveExtra"}

	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}

	// Адресация по Key(), а не по индексу: перестановка блоков в посеве —
	// не изменение поведения, и ронять тест ею незачем.
	want := map[string]Record{
		"wdtt-client:cli-1": {
			ID: "cli-1", Kind: KindWdttClient, Name: "Клиент раз", Enabled: true,
			Sub:    "https://sub.wdtt.example/one",
			PeerWg: "wg.example:56000", PeerRaw: "raw.example:56003",
			WdttClient: &roles.WdttClientConfig{
				Mode:   "raw",
				Listen: "127.0.0.1:9011",
				// Слот активного режима главнее общего peer (common.example
				// в фикстуре стоит именно как ловушка на этот выбор).
				Peer:        "raw.example:56003",
				Password:    "pw-client",
				VKHashes:    "vkh-client",
				Workers:     27,
				Obfs:        "video",
				Fingerprint: "firefox",
				DeviceID:    "dev-client",
				CaptchaMode: "wv",
				VKAuthMode:  "vkauth-client",
				NdmsIface:   "OpkgTun18",
				RawIface:    "opkgtun18",
				Policies: []roles.PolicyPermit{
					{Name: "CacheTop", Order: orderPtr(0)},
					{Name: "CacheSecond", Order: orderPtr(4)},
					{Name: "LiveExtra"},
				},
			},
		},
		// Клиент в режиме wg с ЗАПОЛНЕННЫМ неактивным raw-слотом. Без него
		// PeerRaw не охранялся: normalizeRecord восстанавливает АКТИВНЫЙ слот
		// из Peer, поэтому у raw-клиента потеря PeerRaw чинилась сама собой.
		// Цена реальна — пользователь с wg-клиентом и сохранённым raw-адресом
		// потерял бы адрес соседнего режима (Г-1 №1: фронт восстанавливает
		// его при переключении wg↔raw).
		"wdtt-client:cli-2": {
			ID: "cli-2", Kind: KindWdttClient, Name: "Клиент два", Enabled: false,
			Sub:    "https://sub.wdtt.example/two",
			PeerWg: "wg2.example:56000", PeerRaw: "raw2.example:56003",
			WdttClient: &roles.WdttClientConfig{
				Mode:        "wg",
				Listen:      "127.0.0.1:9014",
				Peer:        "wg2.example:56000",
				Password:    "pw-client-2",
				VKHashes:    "vkh-client-2",
				Workers:     9,
				Obfs:        "audio",
				Fingerprint: "chrome",
				DeviceID:    "dev-client-2",
				CaptchaMode: "rjs",
				VKAuthMode:  "vkauth-client-2",
				// NdmsIface/RawIface/Policies у wg-клиента не существует.
			},
		},
		"wdtt-server:srv-1": {
			ID: "srv-1", Kind: KindWdttServer, Name: "Сервер раз", Enabled: true,
			Users: []ServerUser{{Password: "user-pw-1", Comment: "Абонент один",
				VkHash: "vkhash-1", ExpiresAt: 1700000001, Auto: true}},
			LinkPeer: "link.example:56002", LinkVKHashes: "link-vkh", StatsLog: "disk",
			WdttServer: &roles.WdttServerConfig{
				Listen:           "0.0.0.0:56002",
				WgPort:           56001,
				ConfigDir:        "/opt/etc/awgm/wdtt-srv-1",
				Password:         "pw-server",
				AdminID:          "admin-77",
				BotToken:         "bot-token-77",
				NatIface:         "ISP",
				WgIface:          "opkgtun20",
				RawIface:         "opkgtun21",
				NdmsIface:        "OpkgTun20",
				RawNdmsIface:     "OpkgTun21",
				RawListen:        "0.0.0.0:56003",
				DirectListen:     "0.0.0.0:56004",
				RelayMode:        "raw",
				NatMode:          "internet-only",
				NatStaticWAN:     "ISP2",
				Policy:           "PolicyForServer",
				LanSegments:      []string{"Home", "Guest"},
				Debug:            true,
				ExposeToPolicies: true,
				OpenFirewall:     false,
			},
		},
		"freeturn-client:ftc-1": {
			ID: "ftc-1", Kind: KindFreeTurnClient, Name: "FT клиент", Enabled: true,
			FreeTurnClient: &roles.FreeTurnClientConfig{
				Listen:         "127.0.0.1:9012",
				Peer:           "ft.example:56000",
				Provider:       "vk",
				Links:          "https://vk.example/a,https://vk.example/b",
				Streams:        12,
				Transport:      "udp",
				Mode:           "tcp",
				Bond:           true,
				TurnHost:       "turn.example",
				TurnPort:       3478,
				ObfProfile:     "rtpopus2",
				ObfKey:         "ft-obf-key-client",
				StreamsPerCred: 6,
				Platform:       "mobile",
				DNSMode:        "doh",
				DNSServers:     "1.1.1.1:53,8.8.8.8",
				ClientID:       "ft-client-id",
				Sub:            "https://sub.ft.example/one",
				Debug:          true,
			},
		},
		"freeturn-server:fts-1": {
			ID: "fts-1", Kind: KindFreeTurnServer, Name: "FT сервер", Enabled: true,
			FreeTurnServer: &roles.FreeTurnServerConfig{
				Listen:       "0.0.0.0:56005",
				Connect:      "127.0.0.1:9013",
				Mode:         "tcp",
				ObfProfile:   "rtpopus3",
				ObfKey:       "ft-obf-key-server",
				ClientsFile:  "/opt/etc/awgm/ft-clients.json",
				Debug:        true,
				OpenFirewall: false,
			},
		},
	}

	got := byKey(res)
	if len(got) != len(want) {
		t.Fatalf("записей %d, ждали %d", len(got), len(want))
	}
	for key, w := range want {
		g, ok := got[key]
		if !ok {
			t.Errorf("записи %s нет вовсе", key)
			continue
		}
		if !reflect.DeepEqual(g, w) {
			gj, _ := json.MarshalIndent(g, "", "  ")
			wj, _ := json.MarshalIndent(w, "", "  ")
			t.Errorf("запись %s разошлась с ожиданием.\nполучено:\n%s\nждали:\n%s", key, gj, wj)
		}
	}

	rawCli := got["wdtt-client:cli-1"]
	wgCli := got["wdtt-client:cli-2"]
	srv := got["wdtt-server:srv-1"]

	// Канарейка полноты: нулевое поле означает либо потерю в маппинге, либо
	// новое поле, которого фикстура ещё не знает. Сравнение выше знает только
	// про поля, выписанные руками; эта проверка — про все поля типа.
	//
	// Name — единственное поле конфига с json:"-": его впрыскивает геттер из
	// Record.Name, в самой записи оно пустое (Р3, один писатель имени).
	// Союз двух клиентов: NdmsIface/RawIface/Policies есть только у raw.
	assertEveryFieldCarried(t, "WdttClientConfig",
		[]any{*rawCli.WdttClient, *wgCli.WdttClient}, "Name")
	if c, _ := rawCli.WdttClientConfig(); c.Name != "Клиент раз" {
		t.Errorf("геттер не впрыснул имя: %q", c.Name)
	}
	// OpenFirewall у обеих серверных ролей задан явным false — чтобы значение
	// отличалось от дефолта «ключа нет → true» и потеря маппинга была видна.
	// Форма nil→true закрыта TestSeedOpenFirewallAbsentOrNullMeansOn.
	assertEveryFieldCarried(t, "WdttServerConfig", []any{*srv.WdttServer}, "OpenFirewall")
	assertEveryFieldCarried(t, "ServerUser", []any{srv.Users[0]})
	assertEveryFieldCarried(t, "FreeTurnClientConfig",
		[]any{*got["freeturn-client:ftc-1"].FreeTurnClient})
	assertEveryFieldCarried(t, "FreeTurnServerConfig",
		[]any{*got["freeturn-server:fts-1"].FreeTurnServer}, "OpenFirewall")

	// Record и PolicyPermit — те же права. Именно в Record живут Sub,
	// PeerWg/PeerRaw, Users и link-метаданные: класс, который уже терялся
	// молча. Без союза здесь нельзя — три из четырёх указателей на конфиг
	// обязаны быть nil в каждой записи.
	//
	// CreatedAt — единственное исключение: посев дату не проставляет, потому
	// что в старом мире её не было (ClientInstance/ServerInstance — только
	// ID, Name, Config), взять её неоткуда, а выдумывать «дату создания»
	// временем апгрейда значило бы соврать.
	all := make([]any, 0, len(got))
	for _, r := range got {
		all = append(all, r)
	}
	assertEveryFieldCarried(t, "Record", all, "CreatedAt")

	permits := make([]any, 0, len(rawCli.WdttClient.Policies))
	for _, p := range rawCli.WdttClient.Policies {
		permits = append(permits, p)
	}
	assertEveryFieldCarried(t, "PolicyPermit", permits)
}

// TestSeedServerWithoutNatModeGetsFull — РЕГРЕСС МИГРАЦИИ. У пользователя,
// который режим NAT никогда не трогал, поле старого конфига пустое, а старый
// мир трактовал пустое как full (wdtt.normalizeNatMode, access.go:30-37).
// Дефолт "none" на посеве снял бы NAT с РАБОТАВШЕЙ раздачи после обновления:
// абоненты подключаются, интернета нет.
//
// Проверка отдельная от нормализации на записи (store_test.go) сознательно:
// посев переносит NatMode как есть (seed.go:412), и ломается этот путь
// независимо — своим кодом и своими фикстурами.
func TestSeedServerWithoutNatModeGetsFull(t *testing.T) {
	e := newSeedEnv(t)
	writeFile(t, e.deps.WdttPath, `{"servers":[{"id":"default","name":"Раздача","config":{
	  "listen":"0.0.0.0:56002","wgPort":56001,"password":"pw",
	  "wgIface":"opkgtun20","ndmsIface":"OpkgTun20",
	  "rawIface":"opkgtun21","rawNdmsIface":"OpkgTun21"}}]}`)

	res, err := Seed(context.Background(), e.st, e.deps)
	if err != nil {
		t.Fatal(err)
	}
	srv, ok := byKey(res)["wdtt-server:default"]
	if !ok {
		t.Fatalf("сервер не засеян: %+v", res.State.Records)
	}
	cfg, err := srv.WdttServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NatMode != "full" {
		t.Fatalf("natMode после посева = %q, ждали full (паритет со старым миром)", cfg.NatMode)
	}
}
