package instancestore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(t.TempDir())
}

func rawClient(id, name string) Record {
	return Record{ID: id, Kind: KindWdttClient, Name: name, Enabled: true,
		WdttClient: &roles.WdttClientConfig{
			Mode: "raw", Listen: "127.0.0.1:9000", Peer: "1.2.3.4:56000",
			Password: "pw", VKHashes: "h", Workers: 9,
			NdmsIface: "OpkgTun18", RawIface: "opkgtun18",
		}}
}

func ftClient(id string) Record {
	return Record{ID: id, Kind: KindFreeTurnClient, Name: "FT", Enabled: false,
		FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001",
			Peer: "5.6.7.8:56000", Sub: "https://sub.example"}}
}

func wdttServer(id string) Record {
	return Record{ID: id, Kind: KindWdttServer, Name: "Сервер", Enabled: false,
		Users:    []ServerUser{{Password: "u1", Comment: "Петя", ExpiresAt: 42}},
		LinkPeer: "1.2.3.4:56002", LinkVKHashes: "vh", StatsLog: "disk",
		WdttServer: &roles.WdttServerConfig{
			Listen: "0.0.0.0:56002", Password: "pw", NatMode: "none", RelayMode: "wg",
			NdmsIface: "OpkgTun20", WgIface: "opkgtun20",
			RawNdmsIface: "OpkgTun21", RawIface: "opkgtun21",
		}}
}

func TestRoundTripPreservesRecords(t *testing.T) {
	s := newStore(t)
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, rawClient("de", "Германия"), ftClient("ft1"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Новый экземпляр — честное чтение с диска, не кэш.
	st, err := New(s.dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Records) != 2 {
		t.Fatalf("записей %d", len(st.Records))
	}
	c, err := st.Records[0].WdttClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Германия" || c.NdmsIface != "OpkgTun18" || c.Peer != "1.2.3.4:56000" {
		t.Fatalf("конфиг не доехал: %+v", c)
	}
	if e, ok := st.Records[0].RawExiter().RawExit(); !ok || e.NDMSName != "OpkgTun18" {
		t.Fatalf("RawExit: %+v %v", e, ok)
	}
}

func TestFreeturnSubRoundTrip(t *testing.T) {
	// B4 ред. 1: Sub терялся в ручном конвертере. Теперь это поле конфига с
	// тегом; roles.FreeTurnClientArgs эмитит -sub (args.go:125).
	s := newStore(t)
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, ftClient("ft1"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := New(s.dir).Load()
	c, err := st.Records[0].FreeTurnClientConfig()
	if err != nil || c.Sub != "https://sub.example" {
		t.Fatalf("Sub не доехал: %+v %v", c, err)
	}
}

func TestServerUsersRoundTrip(t *testing.T) {
	// B5 + замечание 4: абоненты и link-метаданные — данные пользователя.
	s := newStore(t)
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, wdttServer("default"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := New(s.dir).Load()
	r := st.Records[0]
	if len(r.Users) != 1 || r.Users[0].ExpiresAt != 42 || r.Users[0].Comment != "Петя" {
		t.Fatalf("Users не доехали: %+v", r.Users)
	}
	if r.LinkPeer != "1.2.3.4:56002" || r.LinkVKHashes != "vh" || r.StatsLog != "disk" {
		t.Fatalf("link-метаданные не доехали: %+v", r)
	}
}

func TestLoadFailsClosedOnCorruptFile(t *testing.T) {
	s := newStore(t)
	if err := os.WriteFile(s.path, []byte("{нет"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("битый файл обязан быть ошибкой Load: пустое состояние = снос зеркальных записей ведомостью")
	}
}

func TestLoadValidatesFailClosed(t *testing.T) {
	// Валидация и на чтении: рукописная запись без конфига не должна дожить
	// до геттеров (у них поэтому нет канала «может не быть»).
	s := newStore(t)
	if err := os.WriteFile(s.path,
		[]byte(`{"version":1,"seededFrom":["x"],"instances":[{"id":"a","kind":"wdtt-client","name":"n","enabled":true}]}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("запись без конфига обязана валить Load")
	}
}

func TestLoadMissingFileIsEmptyState(t *testing.T) {
	s := newStore(t)
	st, err := s.Load()
	if err != nil || st.Seeded || len(st.Records) != 0 {
		t.Fatalf("чистая установка: %+v, %v", st, err)
	}
}

func TestReplaceRejectsSecondWdttServer(t *testing.T) {
	s := newStore(t)
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, wdttServer("s1"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, wdttServer("s2"))
		return nil
	}); err == nil {
		t.Fatal("второй wdtt-server обязан отклоняться: правила AWGM_WDTT не несут инстансного дискриминатора")
	}
}

func TestReplaceRejectsDuplicateKeyAllowsCrossRoleID(t *testing.T) {
	s := newStore(t)
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, rawClient("x", "A"), rawClient("x", "B"))
		return nil
	}); err == nil {
		t.Fatal("дубликат (роль, id) обязан отклоняться")
	}
	// Одинаковый id в РАЗНЫХ ролях законен: старые подсистемы держали
	// раздельные пространства, посев обязан прожевать «default» у всех.
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, rawClient("default", "A"), ftClient("default"))
		return nil
	}); err != nil {
		t.Fatalf("id, совпадающий между ролями, обязан приниматься: %v", err)
	}
}

func TestReplaceRejectsKindMismatch(t *testing.T) {
	s := newStore(t)
	r := rawClient("y", "C")
	r.Kind = KindFreeTurnClient // конфиг wdtt при чужом Kind
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r)
		return nil
	}); err == nil {
		t.Fatal("несоответствие Kind ↔ конфиг обязано отклоняться")
	}
}

func TestReplaceRejectsRawClientWithoutPins(t *testing.T) {
	s := newStore(t)
	r := rawClient("z", "D")
	r.WdttClient.NdmsIface, r.WdttClient.RawIface = "", ""
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r)
		return nil
	}); err == nil {
		t.Fatal("raw-клиент без пинов ронял бы SetDeclared всех: запись обязана отклоняться (ErrMissingPins)")
	}
}

func TestNormalizationOnWrite(t *testing.T) {
	s := newStore(t)
	r := rawClient("n", "  Имя ")
	r.WdttClient.Peer = "  1.2.3.4:56000 "
	r.WdttClient.Workers = 24 // старый дефолт: клиент молча сделал бы 18
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := New(s.dir).Load()
	got, _ := st.Records[0].WdttClientConfig()
	if got.Workers != 18 {
		t.Fatalf("workers = %d, ждали 18 (кратно 9 вниз — так сделает и сам клиент)", got.Workers)
	}
	if got.Peer != "1.2.3.4:56000" || strings.TrimSpace(st.Records[0].Name) != st.Records[0].Name {
		t.Fatalf("нормализация не сработала: %+v / %q", got, st.Records[0].Name)
	}
}

func TestWorkersFloorIsNine(t *testing.T) {
	s := newStore(t)
	r := Record{ID: "w", Kind: KindWdttClient, Name: "W",
		WdttClient: &roles.WdttClientConfig{Mode: "wg", Listen: "127.0.0.1:9000",
			Peer: "p:1", Password: "pw", VKHashes: "h", Workers: 4}}
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := New(s.dir).Load()
	got, _ := st.Records[0].WdttClientConfig()
	if got.Workers != 9 {
		t.Fatalf("workers = %d, ждали 9 (минимум клиента)", got.Workers)
	}
}

func TestReplaceCheckedHookSeesNormalizedAndCancels(t *testing.T) {
	// З1: хук beforeWrite видит НОРМАЛИЗОВАННОЕ состояние (manager объявляет
	// из него реестру), а его ошибка отменяет запись целиком.
	s := newStore(t)
	var seenPeer string
	_, err := s.ReplaceChecked(func(st *State) error {
		r := rawClient("h", "H")
		r.WdttClient.Peer = "  1.2.3.4:56000  "
		st.Records = append(st.Records, r)
		return nil
	}, func(st State) error {
		c, _ := st.Records[0].WdttClientConfig()
		seenPeer = c.Peer
		return os.ErrInvalid // хук передумал
	})
	if err == nil {
		t.Fatal("ошибка хука обязана отменять запись")
	}
	if seenPeer != "1.2.3.4:56000" {
		t.Fatalf("хук обязан видеть нормализованное: %q", seenPeer)
	}
	if st, _ := New(s.dir).Load(); len(st.Records) != 0 {
		t.Fatal("отменённая хуком запись доехала до диска")
	}
}

func TestReplaceIsAtomicOnMutateError(t *testing.T) {
	s := newStore(t)
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, rawClient("keep", "K"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(func(st *State) error {
		st.Records = nil
		return os.ErrInvalid // мутатор передумал
	}); err == nil {
		t.Fatal("ошибка мутатора обязана отменять запись")
	}
	st, _ := New(s.dir).Load()
	if len(st.Records) != 1 {
		t.Fatalf("отменённая мутация доехала до диска: %d", len(st.Records))
	}
}

func TestRawExiterCoversAllKinds(t *testing.T) {
	// Требование 16: ведомость по интерфейсу для ЛЮБОЙ роли; отсутствие
	// выхода выражает сам RawExit()==false, а не выпадение из ведомости.
	recs := []Record{rawClient("a", "A"), ftClient("b"), wdttServer("c"),
		{ID: "d", Kind: KindFreeTurnServer, Name: "S",
			FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:56000"}}}
	for _, r := range recs {
		ex := r.RawExiter()
		if ex == nil {
			t.Fatalf("%s: RawExiter обязан отдаваться для любой роли", r.Key())
		}
		_, has := ex.RawExit()
		if has != (r.Kind == KindWdttClient) {
			t.Fatalf("%s: выход объявляет только raw-клиент, has=%v", r.Key(), has)
		}
	}
}

func TestPeerSlotsInvariant(t *testing.T) {
	// Г-1 №1: слот неактивного режима переживает запись; Peer зеркалится в
	// активный слот; пустой Peer восстанавливается из слота.
	s := newStore(t)
	r := rawClient("p", "P") // Mode raw, Peer 1.2.3.4:56000
	r.PeerWg = "9.9.9.9:56000"
	if _, err := s.Replace(func(st *State) error { st.Records = append(st.Records, r); return nil }); err != nil {
		t.Fatal(err)
	}
	st, _ := New(s.dir).Load()
	got := st.Records[0]
	if got.PeerRaw != "1.2.3.4:56000" || got.PeerWg != "9.9.9.9:56000" {
		t.Fatalf("слоты: raw=%q wg=%q", got.PeerRaw, got.PeerWg)
	}
	// Переключение режима без Peer: адрес восстанавливается из слота wg.
	if _, err := s.Replace(func(st *State) error {
		st.Records[0].WdttClient.Mode = "wg"
		st.Records[0].WdttClient.Peer = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ = New(s.dir).Load()
	c, _ := st.Records[0].WdttClientConfig()
	if c.Peer != "9.9.9.9:56000" {
		t.Fatalf("восстановление из слота: %q", c.Peer)
	}
}

func TestPolicyOrderRoundTrip(t *testing.T) {
	// Три состояния позиции обязаны быть различимы на диске: закреплённая
	// ненулевая, закреплённый НОЛЬ (верх политики — NDMS нумерует с нуля) и
	// незакреплённая. Слияние нуля с «не закреплено» уводило бы выход,
	// поднятый выше провайдера, в хвост политики молча.
	s := newStore(t)
	r := rawClient("o", "O")
	r.WdttClient.Policies = []roles.PolicyPermit{
		{Name: "P1", Order: orderPtr(1)},
		{Name: "P2", Order: orderPtr(0)},
		{Name: "P3"},
	}
	if _, err := s.Replace(func(st *State) error { st.Records = append(st.Records, r); return nil }); err != nil {
		t.Fatal(err)
	}
	st, _ := New(s.dir).Load()
	c, _ := st.Records[0].WdttClientConfig()
	if len(c.Policies) != 3 {
		t.Fatalf("permit'ов %d: %+v", len(c.Policies), c.Policies)
	}
	if c.Policies[0].Order == nil || *c.Policies[0].Order != 1 {
		t.Fatalf("позиция 1 не доехала: %+v", c.Policies[0])
	}
	if c.Policies[1].Order == nil || *c.Policies[1].Order != 0 {
		t.Fatalf("позиция 0 (верх политики) не доехала: %+v", c.Policies[1])
	}
	if c.Policies[2].Order != nil {
		t.Fatalf("незакреплённая позиция обязана остаться nil: %+v", c.Policies[2])
	}
}

func orderPtr(v int) *int { return &v }

func TestRecordWireFormatCanary(t *testing.T) {
	// Формат proxy-instances.json менять только с миграцией. Канарейка на
	// конфигах ролей живёт в roles/config_test.go; ЭТИ ключи — оболочка
	// записи и продуктовые данные, за ними до фикс-раунда не следило ничто:
	// переименование linkVkHashes, peerRaw или expiresAt обнуляло бы на
	// апгрейде параметры ссылки, адрес неактивного режима и срок абонента
	// (отозванный доступ воскресал бы бессрочным).
	cases := []struct {
		name string
		v    any
		want []string
	}{
		{"record", Record{ID: "i", Kind: KindWdttClient, Name: "n", Enabled: true,
			CreatedAt: "t", Sub: "s", PeerWg: "w", PeerRaw: "r",
			Users: []ServerUser{{Password: "p"}}, LinkPeer: "lp", LinkVKHashes: "lv",
			StatsLog:       "disk",
			WdttClient:     &roles.WdttClientConfig{},
			WdttServer:     &roles.WdttServerConfig{},
			FreeTurnClient: &roles.FreeTurnClientConfig{},
			FreeTurnServer: &roles.FreeTurnServerConfig{}},
			[]string{"id", "kind", "name", "enabled", "createdAt", "sub",
				"peerWg", "peerRaw", "users", "linkPeer", "linkVkHashes",
				"statsLog", "wdttClient", "wdttServer", "freeturnClient",
				"freeturnServer"}},
		{"server-user", ServerUser{Password: "p", Comment: "c", VkHash: "v",
			ExpiresAt: 1, Auto: true},
			[]string{"password", "comment", "vkHash", "expiresAt", "auto"}},
		{"file", fileFormat{Version: 1, SeededFrom: []string{"x"}, Instances: []Record{}},
			[]string{"version", "seededFrom", "instances"}},
	}
	for _, c := range cases {
		data, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		for _, k := range c.want {
			if _, ok := m[k]; !ok {
				t.Fatalf("%s: ключ %q пропал — формат store менять только с миграцией", c.name, k)
			}
		}
		if len(m) != len(c.want) {
			t.Fatalf("%s: ключей %d (%v), ждали %d (%v) — состав формата менять только с миграцией",
				c.name, len(m), m, len(c.want), c.want)
		}
	}
}

func TestStoreFileNameIsPinned(t *testing.T) {
	// Дрейф имени файла = «чистая установка» на апгрейде: посев проходит
	// второй раз и дублирует инстансы пользователя (тот же класс, что потеря
	// SeededFrom).
	dir := t.TempDir()
	if got := New(dir).path; got != filepath.Join(dir, "proxy-instances.json") {
		t.Fatalf("имя файла store сменилось: %q — это миграция, а не правка", got)
	}
}

func TestLoadNormalizesBeforeValidating(t *testing.T) {
	// Загрузка обязана нормализовать, а не только проверять: иначе
	// ненормализованный mode («  raw  ») уводит запись МИМО проверки пинов —
	// а докстрока Record обещает, что запись без пинов до геттеров не доживёт.
	s := newStore(t)
	if err := os.WriteFile(s.path, []byte(`{"version":1,"instances":[{"id":"a",`+
		`"kind":"wdtt-client","name":"n","enabled":true,`+
		`"wdttClient":{"connMode":"  raw  ","listen":"127.0.0.1:9000",`+
		`"peer":"1.2.3.4:56000","password":"pw","vkHashes":"h","workers":9}}]}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrMissingPins) {
		t.Fatalf("raw-клиент с ненормализованным mode и без пинов обязан валить Load: %v", err)
	}
}

func TestLoadNormalizesRecords(t *testing.T) {
	// Та же нормализация на чтении в положительной форме: рукописный файл
	// доезжает до геттеров уже приведённым.
	s := newStore(t)
	if err := os.WriteFile(s.path, []byte(`{"version":1,"instances":[{"id":"  a  ",`+
		`"kind":"wdtt-client","name":"n","enabled":true,`+
		`"wdttClient":{"listen":"  127.0.0.1:9000  ","peer":"  1.2.3.4:56000  ",`+
		`"password":"pw","vkHashes":"h","workers":24}}]}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Records[0].ID != "a" {
		t.Fatalf("id не нормализован на чтении: %q", st.Records[0].ID)
	}
	c, _ := st.Records[0].WdttClientConfig()
	if c.Mode != "wg" || c.Peer != "1.2.3.4:56000" || c.Listen != "127.0.0.1:9000" || c.Workers != 18 {
		t.Fatalf("конфиг не нормализован на чтении: %+v", c)
	}
}

func TestPinsWithWhitespaceRejected(t *testing.T) {
	// Пробельный пин — не пин: имя OpkgTun с пробелом не совпадёт ни с одним
	// реальным интерфейсом, а проверка на пустоту его пропускала.
	s := newStore(t)
	r := rawClient("z", "D")
	r.WdttClient.NdmsIface, r.WdttClient.RawIface = "  ", "  "
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r)
		return nil
	}); !errors.Is(err, ErrMissingPins) {
		t.Fatalf("пробельные пины клиента обязаны валиться ErrMissingPins: %v", err)
	}
	srv := wdttServer("s")
	srv.WdttServer.RawNdmsIface = "   "
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, srv)
		return nil
	}); !errors.Is(err, ErrMissingPins) {
		t.Fatalf("пробельный пин сервера обязан валиться ErrMissingPins: %v", err)
	}
}

func TestPinsAreTrimmedOnWrite(t *testing.T) {
	s := newStore(t)
	r := rawClient("t", "T")
	r.WdttClient.NdmsIface, r.WdttClient.RawIface = "  OpkgTun18  ", "  opkgtun18  "
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := New(s.dir).Load()
	c, _ := st.Records[0].WdttClientConfig()
	if c.NdmsIface != "OpkgTun18" || c.RawIface != "opkgtun18" {
		t.Fatalf("пины не нормализованы: %+v", c)
	}
	srv := wdttServer("s")
	srv.WdttServer.NdmsIface = "  OpkgTun20  "
	srv.WdttServer.WgIface = "  opkgtun20  "
	srv.WdttServer.RawNdmsIface = "  OpkgTun21  "
	srv.WdttServer.RawIface = "  opkgtun21  "
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, srv)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ = New(s.dir).Load()
	sc, _ := st.Records[1].WdttServerConfig()
	if sc.NdmsIface != "OpkgTun20" || sc.WgIface != "opkgtun20" ||
		sc.RawNdmsIface != "OpkgTun21" || sc.RawIface != "opkgtun21" {
		t.Fatalf("пины сервера не нормализованы: %+v", sc)
	}
}

func TestNDMSNamedCoversAllKinds(t *testing.T) {
	// Ведомость NDMS-имён уборщика собирается ПО ЭТОМУ вызову: роль, для
	// которой он вернёт nil, отдала бы свой живой интерфейс уборке.
	recs := []Record{rawClient("a", "A"), ftClient("b"), wdttServer("c"),
		{ID: "d", Kind: KindFreeTurnServer, Name: "S",
			FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:56000"}}}
	want := map[Kind][]string{
		KindWdttClient:     {"OpkgTun18"},
		KindWdttServer:     {"OpkgTun20", "OpkgTun21"},
		KindFreeTurnClient: nil,
		KindFreeTurnServer: nil,
	}
	for _, r := range recs {
		n := r.NDMSNamed()
		if n == nil {
			t.Fatalf("%s: NDMSNamed обязан отдаваться для любой роли", r.Key())
		}
		got := n.NDMSNames()
		if len(got) != len(want[r.Kind]) {
			t.Fatalf("%s: имена %v, ждали %v", r.Key(), got, want[r.Kind])
		}
		for i := range got {
			if got[i] != want[r.Kind][i] {
				t.Fatalf("%s: имена %v, ждали %v", r.Key(), got, want[r.Kind])
			}
		}
	}
}

func TestGettersRejectForeignKind(t *testing.T) {
	// Геттер отдаёт конфиг только своей роли: молчаливый нулевой конфиг
	// вместо ошибки увёл бы роль в argv без единого параметра.
	r := rawClient("a", "A")
	if _, err := r.WdttServerConfig(); err == nil {
		t.Fatal("WdttServerConfig на клиенте обязан быть ошибкой")
	}
	if _, err := r.FreeTurnClientConfig(); err == nil {
		t.Fatal("FreeTurnClientConfig на wdtt-клиенте обязан быть ошибкой")
	}
	if _, err := r.FreeTurnServerConfig(); err == nil {
		t.Fatal("FreeTurnServerConfig на wdtt-клиенте обязан быть ошибкой")
	}
	if _, err := ftClient("b").WdttClientConfig(); err == nil {
		t.Fatal("WdttClientConfig на freeturn-клиенте обязан быть ошибкой")
	}
}

func TestLoadFailsClosedOnUnreadableFile(t *testing.T) {
	// Третий исход чтения: файл ЕСТЬ, но прочитать нельзя. Пустое состояние
	// здесь равно «инстансов нет» — ведомость снесла бы зеркальные записи.
	s := newStore(t)
	if err := os.Mkdir(s.path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("нечитаемый файл обязан быть ошибкой Load, а не пустым состоянием")
	}
}

func TestReplaceFailsClosedOnCorruptFile(t *testing.T) {
	// Битый файл не должен превращаться в чистый лист под запись: иначе
	// первая же мутация затирает все инстансы пользователя.
	s := newStore(t)
	if err := os.WriteFile(s.path, []byte("{нет"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(func(st *State) error { return nil }); err == nil {
		t.Fatal("Replace поверх битого файла обязан отказать, а не переписать его пустым")
	}
	data, err := os.ReadFile(s.path)
	if err != nil || string(data) != "{нет" {
		t.Fatalf("битый файл затёрт: %q, %v", data, err)
	}
}

func TestWriteErrorIsReported(t *testing.T) {
	// Ошибка записи обязана доехать до вызывающего: проглоченная, она даёт
	// «сохранено» на потерянной настройке.
	if os.Geteuid() == 0 {
		t.Skip("под root права на каталог не запрещают запись")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := New(dir).Replace(func(st *State) error {
		st.Records = append(st.Records, rawClient("a", "A"))
		return nil
	}); err == nil {
		t.Fatal("отказ записи обязан быть ошибкой Replace")
	}
}

func TestSeededFromRoundTrip(t *testing.T) {
	// Гейт посева читается из этого поля: потерянный SeededFrom заставил бы
	// посев пройти второй раз и продублировать инстансы.
	s := newStore(t)
	st, err := s.Replace(func(st *State) error {
		st.SeededFrom = []string{"wdtt.json"}
		st.Records = append(st.Records, rawClient("a", "A"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Seeded {
		t.Fatal("Replace обязан вернуть Seeded по непустому SeededFrom")
	}
	got, err := New(s.dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Seeded || len(got.SeededFrom) != 1 || got.SeededFrom[0] != "wdtt.json" {
		t.Fatalf("SeededFrom не доехал: %+v", got)
	}
}

func TestNormalizationTrimsEveryField(t *testing.T) {
	s := newStore(t)
	r := rawClient("  id  ", "N")
	r.Sub = "  https://s  "
	r.WdttClient.Listen = "  127.0.0.1:9000  "
	r.PeerRaw = "  1.2.3.4:56000  "
	r.PeerWg = "  9.9.9.9:56000  "
	ft := ftClient("ft")
	ft.FreeTurnClient.Listen = "  127.0.0.1:9001  "
	ft.FreeTurnClient.Peer = "  5.6.7.8:56000  "
	ft.FreeTurnClient.Sub = "  https://sub.example  "
	fs := Record{ID: "fs", Kind: KindFreeTurnServer, Name: "S",
		FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "  0.0.0.0:56000  "}}
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r, ft, fs)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := New(s.dir).Load()
	if st.Records[0].ID != "id" || st.Records[0].Sub != "https://s" {
		t.Fatalf("запись: id=%q sub=%q", st.Records[0].ID, st.Records[0].Sub)
	}
	c, _ := st.Records[0].WdttClientConfig()
	if c.Listen != "127.0.0.1:9000" || st.Records[0].PeerWg != "9.9.9.9:56000" ||
		st.Records[0].PeerRaw != "1.2.3.4:56000" {
		t.Fatalf("wdtt-клиент: %+v / %+v", c, st.Records[0])
	}
	fc, _ := st.Records[1].FreeTurnClientConfig()
	if fc.Listen != "127.0.0.1:9001" || fc.Peer != "5.6.7.8:56000" || fc.Sub != "https://sub.example" {
		t.Fatalf("freeturn-клиент: %+v", fc)
	}
	fsc, _ := st.Records[2].FreeTurnServerConfig()
	if fsc.Listen != "0.0.0.0:56000" {
		t.Fatalf("freeturn-сервер: %+v", fsc)
	}
}

func TestModeNormalization(t *testing.T) {
	// Пустой Mode — дефолт старого мира wg (иначе roles.Validate отвергнет
	// конфиг). Пробелы вокруг raw увели бы запись мимо проверки пинов.
	s := newStore(t)
	empty := rawClient("e", "E")
	empty.WdttClient.Mode = ""
	spaced := rawClient("s", "S")
	spaced.WdttClient.Mode = "  raw  "
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, empty, spaced)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := New(s.dir).Load()
	if c, _ := st.Records[0].WdttClientConfig(); c.Mode != "wg" {
		t.Fatalf("пустой mode = %q, ждали wg", c.Mode)
	}
	if c, _ := st.Records[1].WdttClientConfig(); c.Mode != "raw" {
		t.Fatalf("mode с пробелами = %q, ждали raw", c.Mode)
	}
}

func TestWorkersZeroStaysZero(t *testing.T) {
	// Ноль — «не задано»: его решает DefaultWorkers по архитектуре (27 на
	// arm64). Подмена нулю на девять молча срезала бы полосу на arm64.
	s := newStore(t)
	r := rawClient("z", "Z")
	r.WdttClient.Workers = 0
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := New(s.dir).Load()
	if c, _ := st.Records[0].WdttClientConfig(); c.Workers != 0 {
		t.Fatalf("workers = %d, ждали 0 (не задано)", c.Workers)
	}
}

func TestServerDefaultsRelayAndNatMode(t *testing.T) {
	// roles.WdttServerConfig.Validate отвергает пустые relayMode/natMode:
	// дефолты обязан проставить store, иначе старая запись без этих полей
	// уедет в вечный failed.
	s := newStore(t)
	r := wdttServer("d")
	r.WdttServer.RelayMode, r.WdttServer.NatMode = "", ""
	r.WdttServer.Listen = "  0.0.0.0:56002  "
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := New(s.dir).Load()
	c, _ := st.Records[0].WdttServerConfig()
	if c.RelayMode != "wg" || c.NatMode != "none" || c.Listen != "0.0.0.0:56002" {
		t.Fatalf("дефолты сервера: %+v", c)
	}
}

func TestReplaceRejectsServerWithoutAnyPin(t *testing.T) {
	// У сервера ЧЕТЫРЕ пина, и потеря любого одного ломает свою половину:
	// проверяются все четыре по отдельности.
	for _, drop := range []string{"NdmsIface", "WgIface", "RawNdmsIface", "RawIface"} {
		s := newStore(t)
		r := wdttServer("s")
		switch drop {
		case "NdmsIface":
			r.WdttServer.NdmsIface = ""
		case "WgIface":
			r.WdttServer.WgIface = ""
		case "RawNdmsIface":
			r.WdttServer.RawNdmsIface = ""
		case "RawIface":
			r.WdttServer.RawIface = ""
		}
		if _, err := s.Replace(func(st *State) error {
			st.Records = append(st.Records, r)
			return nil
		}); !errors.Is(err, ErrMissingPins) {
			t.Fatalf("сервер без %s обязан валиться ErrMissingPins: %v", drop, err)
		}
	}
}

func TestReplaceRejectsRawClientWithEitherPinMissing(t *testing.T) {
	for _, drop := range []string{"NdmsIface", "RawIface"} {
		s := newStore(t)
		r := rawClient("z", "D")
		if drop == "NdmsIface" {
			r.WdttClient.NdmsIface = ""
		} else {
			r.WdttClient.RawIface = ""
		}
		if _, err := s.Replace(func(st *State) error {
			st.Records = append(st.Records, r)
			return nil
		}); !errors.Is(err, ErrMissingPins) {
			t.Fatalf("raw-клиент без %s обязан валиться ErrMissingPins: %v", drop, err)
		}
	}
}

func TestReplaceRejectsEmptyID(t *testing.T) {
	// Пустой id даёт Key вида "wdtt-client:" — общий для всех безымянных:
	// вторая такая запись затёрла бы первую в карте инстансов.
	s := newStore(t)
	r := rawClient("   ", "A")
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r)
		return nil
	}); err == nil {
		t.Fatal("запись без id обязана отклоняться")
	}
}

func TestReplaceRejectsTwoConfigsInOneRecord(t *testing.T) {
	// Второй конфиг — второй набор пинов и портов, который никто не
	// применит: роль читает ровно один.
	s := newStore(t)
	r := rawClient("t", "T")
	r.FreeTurnClient = &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001"}
	if _, err := s.Replace(func(st *State) error {
		st.Records = append(st.Records, r)
		return nil
	}); err == nil {
		t.Fatal("два конфига в одной записи обязаны отклоняться")
	}
}
