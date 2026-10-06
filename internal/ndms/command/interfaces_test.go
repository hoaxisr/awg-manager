package command

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/netdev"
)

func testQueries() *query.Queries {
	return query.NewQueries(query.Deps{
		Getter: query.NewFakeGetter(),
		Logger: query.NopLogger(),
		IsOS5:  func() bool { return true },
	})
}

// newTestInterfaceCommands — команды уходят в fakePoster (форма payload и
// ответы роутера), список интерфейсов читается у FakeNDMS: в нём уже есть
// имена, которые тесты создают, — так подтверждение после создания находит
// запись, как на роутере после принятой команды.
func newTestInterfaceCommands(_ *testing.T) (*InterfaceCommands, *fakePoster, *SaveCoordinator, *query.Queries) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	sc.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun0"}, ndms.Interface{ID: "OpkgTun10"})
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewInterfaceCommands(poster, sc, q), poster, sc, q
}

// confirmed — доказательство существования name для тестов формы команд:
// берётся у отдельного FakeNDMS, где name есть.
func confirmed(t *testing.T, name string) query.Confirmed {
	t.Helper()
	q := query.NewQueries(query.Deps{Getter: query.NewFakeNDMS(ndms.Interface{ID: name}), Logger: query.NopLogger()})
	c, _, ok, err := q.Interfaces.Confirm(context.Background(), name)
	if err != nil || !ok {
		t.Fatalf("confirm %s: ok=%v err=%v", name, ok, err)
	}
	return c
}

// newOracleInterfaceCommands — FakeNDMS и принимает команды, и отдаёт список:
// видно созданное, снятое, фантомы и E.
func newOracleInterfaceCommands(t *testing.T, ifaces ...ndms.Interface) (*InterfaceCommands, *query.FakeNDMS, *SaveCoordinator, *query.Queries) {
	t.Helper()
	f := query.NewFakeNDMS(ifaces...)
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(f, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	sc.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewInterfaceCommands(f, sc, q), f, sc, q
}

// oracleGetter — FakeNDMS плюс ответы на пути, которых у него нет
// (running-config, ping-check): чтение списка и команды идут в оракул.
type oracleGetter struct {
	*query.FakeNDMS
	raw map[string]string
}

func (g oracleGetter) GetRaw(ctx context.Context, path string) ([]byte, error) {
	if b, ok := g.raw[path]; ok {
		return []byte(b), nil
	}
	return g.FakeNDMS.GetRaw(ctx, path)
}

func (g oracleGetter) Get(ctx context.Context, path string, dst any) error {
	if b, ok := g.raw[path]; ok {
		return json.Unmarshal([]byte(b), dst)
	}
	return g.FakeNDMS.Get(ctx, path, dst)
}

// newOracleCommands — все группы команд на оракуле; raw — ответы по путям
// сверх модели FakeNDMS.
// newTestSave — координатор для автономных конструкторов команд в тестах
// (nil паникует, newMutator): debounce 0 — заказ стреляет сразу, в p.
func newTestSave(p Poster) *SaveCoordinator {
	return noBusSave(NewSaveCoordinator(p, nil, 0, 0, 0, nil))
}

func newOracleCommands(t *testing.T, raw map[string]string, ifaces ...ndms.Interface) (*Commands, *query.FakeNDMS, *query.Queries) {
	t.Helper()
	f := query.NewFakeNDMS(ifaces...)
	sc := NewSaveCoordinator(f, &fakePublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)
	sc.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)
	q := query.NewQueries(query.Deps{Getter: oracleGetter{FakeNDMS: f, raw: raw}, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewCommands(Deps{Poster: f, Save: sc, Queries: q, IsOS5: func() bool { return true }}), f, q
}

func TestCreateOpkgTun_ReturnsConfirmedAndFillsCache(t *testing.T) {
	cmds, f, _, q := newOracleInterfaceCommands(t)
	f.ExpectCreate("OpkgTun3")
	c, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun3", "t", freeFor(t, "opkgtun3"))
	if err != nil || c.Name() != "OpkgTun3" || f.Phantoms != 0 || len(f.Created) != 1 {
		t.Fatalf("c=%v err=%v phantoms=%d created=%v", c, err, f.Phantoms, f.Created)
	}
	lists := f.ListCalls()
	if rec, _ := q.Interfaces.Get(context.Background(), "OpkgTun3"); rec == nil {
		t.Fatal("Confirm after create must land the record in the cache")
	}
	// Настройки после Confirm метят карту грязной: Get берёт не больше одного
	// свежего списка (F546) и ни одного чтения по имени.
	if f.ListCalls()-lists > 1 || f.E != 0 || len(f.Posts) == 0 {
		t.Fatalf("Get after create: lists %d→%d, E=%d", lists, f.ListCalls(), f.E)
	}
}

// NDMS принял команду, а записи нет ни в списке, ни в хуках — ошибка с
// именем, не Confirmed; ни настроек, ни сноса (снос отсутствующего — E, F584).
func TestCreateOpkgTun_AbsentAfterCreate_Error(t *testing.T) {
	cmds, poster, _, q := newTestInterfaceCommands(t)
	q.Interfaces.SetCreatedBackoff() // один список, без пауз
	c, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun7", "t", freeFor(t, "opkgtun7"))
	if err == nil || !errors.Is(err, query.ErrNotSeen) || !strings.Contains(err.Error(), "OpkgTun7") || c != (query.Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if p := poster.Payloads(); len(p) != 1 || q.Interfaces.ClaimOwnConf("OpkgTun7", "running") {
		t.Fatalf("payloads: %v (или выдан кредит грани conf)", p)
	}
}

// Список не прочитался после создания — ошибка с именем (одним), не Confirmed.
func TestCreateOpkgTun_ConfirmListError(t *testing.T) {
	cmds, f, _, _ := newOracleInterfaceCommands(t)
	f.ExpectCreate("OpkgTun3")
	f.FailList(errors.New("rci down"))
	c, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun3", "t", freeFor(t, "opkgtun3"))
	if err == nil || !strings.Contains(err.Error(), "rci down") || strings.Count(err.Error(), "OpkgTun3") != 1 || c != (query.Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
}

// Настоящий отказ сноса (не «интерфейса нет») — ошибка наружу, запись в кэше остаётся.
func TestDeleteOpkgTun_RealError_KeepsRecord(t *testing.T) {
	cmds, poster, _, q := newTestInterfaceCommands(t)
	c, _, _, err := q.Interfaces.Confirm(context.Background(), "OpkgTun0")
	if err != nil {
		t.Fatal(err)
	}
	poster.SetError(errors.New("interface is busy"))
	if err := cmds.DeleteOpkgTun(context.Background(), c); err == nil {
		t.Fatal("ожидалась ошибка сноса")
	}
	if rec, _ := q.Interfaces.Get(context.Background(), "OpkgTun0"); rec == nil {
		t.Fatal("отказ сноса не должен забывать запись")
	}
}

func TestDeleteOpkgTun_ForgetsOnSuccess(t *testing.T) {
	cmds, f, _, q := newOracleInterfaceCommands(t, ndms.Interface{ID: "OpkgTun3", Type: "OpkgTun"})
	c, _, _, _ := q.Interfaces.Confirm(context.Background(), "OpkgTun3")
	lists := f.ListCalls()
	if err := cmds.DeleteOpkgTun(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if rec, _ := q.Interfaces.Get(context.Background(), "OpkgTun3"); rec != nil || f.Has("OpkgTun3") {
		t.Fatal("record must be forgotten locally and gone on the router")
	}
	if f.ListCalls() != lists || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("delete: lists %d→%d, E=%d, phantoms=%d", lists, f.ListCalls(), f.E, f.Phantoms)
	}
}

// П8 (стенд 06.10): действующий up:true/up:false даёт ровно одну грань conf,
// no-op — ни одной. Кредит — только за действующую команду, судит карта, и
// карта сразу принимает наш слой: up→up и down→down — no-op, кредита нет.
// Мутации: «кредит всегда» → no-op down даёт кредит, красный; «карту не
// трогать» → второй up снова действующий, второй кредит running, красный.
func TestInterfaceUpDown_CreditOnlyWhenEffective(t *testing.T) {
	ctx := context.Background()
	cmds, f, _, q := newOracleInterfaceCommands(t, ndms.Interface{ID: "OpkgTun3", ConfLayer: "disabled"})
	c, _, _, _ := q.Interfaces.Confirm(ctx, "OpkgTun3")
	claim := func(level string) bool { return q.Interfaces.ClaimOwnConf("OpkgTun3", level) }

	if err := cmds.InterfaceDown(ctx, c); err != nil || claim("disabled") {
		t.Fatalf("down на выключенной: err=%v или выдан кредит no-op", err)
	}
	for i := 0; i < 2; i++ {
		if err := cmds.InterfaceUp(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if !claim("running") || claim("running") {
		t.Fatal("up, up: ждали ровно один кредит running")
	}
	if err := cmds.InterfaceDown(ctx, c); err != nil || !claim("disabled") {
		t.Fatalf("down после up: err=%v, кредита disabled нет", err)
	}
	if f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("phantoms=%d E=%d", f.Phantoms, f.E)
	}
}

// Отказ POST: кредит назад, слой карты прежний — иначе no-op следующей
// команды прочитался бы действующим: down по-прежнему выключенной записи дал
// бы кредит, которого не погасит ни одна грань. Мутации: «не звать refused» →
// кредит running остался, красный; «слой не возвращать» → кредит disabled у
// no-op down, красный.
func TestInterfaceUp_Refused_CreditBack(t *testing.T) {
	ctx := context.Background()
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun0", ConfLayer: "disabled"})
	poster := &fakePoster{}
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	cmds := NewInterfaceCommands(poster, NewSaveCoordinator(poster, &fakePublisher{}, time.Hour, time.Hour, 0, nil), q)
	c, _, _, err := q.Interfaces.Confirm(ctx, "OpkgTun0")
	if err != nil {
		t.Fatal(err)
	}
	poster.SetError(errors.New("refused"))
	if err := cmds.InterfaceUp(ctx, c); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if q.Interfaces.ClaimOwnConf("OpkgTun0", "running") {
		t.Fatal("кредит отказанного up остался")
	}
	poster.SetError(nil)
	if err := cmds.InterfaceDown(ctx, c); err != nil {
		t.Fatal(err)
	}
	if q.Interfaces.ClaimOwnConf("OpkgTun0", "disabled") {
		t.Fatal("no-op down после отказанного up получил кредит: слой не возвращён")
	}
}

func TestInterfaceCommands_CreateOpkgTun(t *testing.T) {
	cmds, poster, sc, _ := newTestInterfaceCommands(t)
	if _, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun0", "test", freeFor(t, "opkgtun0")); err != nil {
		t.Fatalf("CreateOpkgTun: %v", err)
	}
	if len(poster.Payloads()) != 2 {
		t.Fatalf("payloads: want 2 (create, settings), got %d", len(poster.Payloads()))
	}
	p := poster.Payloads()[1].(map[string]any)
	iface := p["interface"].(map[string]any)["OpkgTun0"].(map[string]any)
	if iface["description"] != "test" {
		t.Errorf("description: %v", iface["description"])
	}
	sec := iface["security-level"].(map[string]any)
	if sec["public"] != true {
		t.Errorf("security-level.public: %v", sec["public"])
	}
	if sc.Status().State != SaveStatePending {
		t.Errorf("save state: want Pending, got %v", sc.Status().State)
	}
}

func TestCreateOpkgTunWithSecurityLevel_Private(t *testing.T) {
	cmds, poster, _, _ := newTestInterfaceCommands(t)
	if _, err := cmds.CreateOpkgTunWithSecurityLevel(context.Background(), "OpkgTun10", "fakeip-tun", "private", freeFor(t, "opkgtun10")); err != nil {
		t.Fatalf("create: %v", err)
	}
	iface := poster.Payloads()[1].(map[string]any)["interface"].(map[string]any)["OpkgTun10"].(map[string]any)
	sl := iface["security-level"].(map[string]any)
	if sl["private"] != true {
		t.Errorf("want security-level.private=true, got %#v", sl)
	}
	if _, hasPublic := sl["public"]; hasPublic {
		t.Errorf("public must not be set in private mode: %#v", sl)
	}
}

func TestInterfaceCommands_DeleteOpkgTun(t *testing.T) {
	cmds, poster, _, _ := newTestInterfaceCommands(t)
	if err := cmds.DeleteOpkgTun(context.Background(), confirmed(t, "OpkgTun0")); err != nil {
		t.Fatalf("DeleteOpkgTun: %v", err)
	}
	p := poster.Payloads()[0].(map[string]any)
	iface := p["interface"].(map[string]any)["OpkgTun0"].(map[string]any)
	if iface["no"] != true {
		t.Errorf("no: %v", iface["no"])
	}
}

func TestInterfaceCommands_SetAddress_TwoPosts(t *testing.T) {
	cmds, poster, _, _ := newTestInterfaceCommands(t)
	if err := cmds.SetAddress(context.Background(), confirmed(t, "OpkgTun0"), "10.0.0.2", "255.255.255.255"); err != nil {
		t.Fatalf("SetAddress: %v", err)
	}
	if len(poster.Payloads()) != 2 {
		t.Fatalf("payloads: want 2 (clear + set), got %d", len(poster.Payloads()))
	}
	p2 := poster.Payloads()[1].(map[string]any)
	addr := p2["interface"].(map[string]any)["OpkgTun0"].(map[string]any)["ip"].(map[string]any)["address"].(map[string]any)
	if addr["address"] != "10.0.0.2" || addr["mask"] != "255.255.255.255" {
		t.Errorf("address payload: %#v", addr)
	}
}

func TestInterfaceCommands_SetMTU(t *testing.T) {
	cmds, poster, _, _ := newTestInterfaceCommands(t)
	if err := cmds.SetMTU(context.Background(), confirmed(t, "OpkgTun0"), 1280); err != nil {
		t.Fatalf("SetMTU: %v", err)
	}
	p := poster.Payloads()[0].(map[string]any)
	ip := p["interface"].(map[string]any)["OpkgTun0"].(map[string]any)["ip"].(map[string]any)
	if ip["mtu"] != 1280 {
		t.Errorf("mtu: %v", ip["mtu"])
	}
	tcp := ip["tcp"].(map[string]any)["adjust-mss"].(map[string]any)
	if tcp["pmtu"] != true {
		t.Errorf("pmtu: %v", tcp["pmtu"])
	}
}

// Записи нет в карте — судить нечем, кредита нет (недодать безопаснее, чем
// повесить кредит). Мутация «кредит неизвестной записи» → красный.
func TestInterfaceUp_UnknownRecord_NoCredit(t *testing.T) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	sc.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)
	q := testQueries()
	cmds := NewInterfaceCommands(poster, sc, q)
	if err := cmds.InterfaceUp(context.Background(), confirmed(t, "OpkgTun0")); err != nil {
		t.Fatalf("InterfaceUp: %v", err)
	}
	if poster.Calls() != 1 || q.Interfaces.ClaimOwnConf("OpkgTun0", "running") {
		t.Errorf("calls=%d (want 1) или выдан кредит неизвестной записи", poster.Calls())
	}
}

func TestInterfaceCommands_SetDNS_MultipleServers(t *testing.T) {
	cmds, poster, _, _ := newTestInterfaceCommands(t)
	servers := []string{"1.1.1.1", "8.8.8.8"}
	if err := cmds.SetDNS(context.Background(), confirmed(t, "OpkgTun0"), servers); err != nil {
		t.Fatalf("SetDNS: %v", err)
	}
	if len(poster.Payloads()) != 2 {
		t.Fatalf("payloads: want 2 (one per server), got %d", len(poster.Payloads()))
	}
}

func TestInterfaceCommands_ClearDNS_IgnoresErrors(t *testing.T) {
	cmds, poster, _, _ := newTestInterfaceCommands(t)
	poster.SetError(errors.New("already absent"))
	if err := cmds.ClearDNS(context.Background(), confirmed(t, "OpkgTun0"), []string{"1.1.1.1"}); err != nil {
		t.Fatalf("ClearDNS best-effort: want no error, got %v", err)
	}
}

func TestInterfaceCommands_CreateOpkgTun_PosterError(t *testing.T) {
	cmds, poster, _, _ := newTestInterfaceCommands(t)
	poster.SetError(errors.New("ndms rejected"))
	_, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun0", "x", freeFor(t, "opkgtun0"))
	if err == nil {
		t.Fatalf("CreateOpkgTun on poster error: want error, got nil")
	}
}

// freeFor — доказательство «opkgtunN нет» с временного /sys/class/net:
// тесты команд проверяют форму и порядок POST, а не ядро роутера.
func freeFor(t *testing.T, kernel string) netdev.Free {
	t.Helper()
	old := netdev.SysClassNet
	netdev.SysClassNet = t.TempDir()
	defer func() { netdev.SysClassNet = old }()
	f, err := netdev.Absent(kernel)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// F569: создание — голое, настройки — отдельным POST по Confirmed.
func TestCreateOpkgTun_SplitPayload(t *testing.T) {
	cmds, f, _, _ := newOracleInterfaceCommands(t)
	f.ExpectCreate("OpkgTun3")
	if _, err := cmds.CreateOpkgTunWithSecurityLevel(context.Background(), "OpkgTun3", "t", "private", freeFor(t, "opkgtun3")); err != nil {
		t.Fatal(err)
	}
	if len(f.Posts) < 2 {
		t.Fatalf("posts: %v", f.Posts)
	}
	requireJSONEqual(t, json.RawMessage(f.Posts[0]), `{"interface":{"OpkgTun3":{}}}`)
	requireJSONEqual(t, json.RawMessage(f.Posts[1]), `{"interface":{"OpkgTun3":{"description":"t","security-level":{"private":true}}}}`)
	if f.E != 0 || f.C != 0 || f.Phantoms != 0 || len(f.Created) != 1 || f.Created[0] != "OpkgTun3" {
		t.Fatalf("E=%d C=%d Phantoms=%d Created=%v", f.E, f.C, f.Phantoms, f.Created)
	}
}

// Отказ создания (устройство живо) — ни одной команды по несозданной записи.
func TestCreateOpkgTun_CreateFails_NoSettings(t *testing.T) {
	cmds, f, _, _ := newOracleInterfaceCommands(t)
	f.ExpectCreate("OpkgTun3")
	f.SetNetdev("opkgtun3", true)
	c, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun3", "t", freeFor(t, "opkgtun3"))
	if err == nil || c != (query.Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if f.C != 1 || f.E != 0 || len(f.Posts) != 1 || f.Has("OpkgTun3") {
		t.Fatalf("C=%d E=%d posts=%v Has=%v", f.C, f.E, f.Posts, f.Has("OpkgTun3"))
	}
}

// Доказательство для чужого имени — отказ до POST.
func TestCreateOpkgTun_WrongProofName(t *testing.T) {
	cmds, f, _, _ := newOracleInterfaceCommands(t)
	f.ExpectCreate("OpkgTun3")
	_, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun3", "t", freeFor(t, "opkgtun4"))
	if err == nil || len(f.Posts) != 0 || f.ListCalls() != 0 {
		t.Fatalf("err=%v posts=%v lists=%d", err, f.Posts, f.ListCalls())
	}
}

// failSettingsPoster — оракул, у которого POST настроек (с description)
// применяется, но отвечает отказом: так NDMS отвергает ключ целиком.
type failSettingsPoster struct{ f *query.FakeNDMS }

func (p failSettingsPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	resp, err := p.f.Post(ctx, payload)
	if b, _ := json.Marshal(payload); err == nil && strings.Contains(string(b), `"description"`) {
		return json.RawMessage(`{"status":[{"status":"error","code":"1","message":"injected: settings rejected"}]}`), nil
	}
	return resp, err
}

// Создание прошло, настройки отвергнуты — запись без нашего описания стала бы
// «чужой» (ForeignRecordError у kernel, сирота у sing-box). Команда сносит её
// сама по тому же Confirmed; свой ifdestroyed узнаётся по кредиту (П21).
func TestCreateOpkgTun_SettingsFail_RollsBackRecord(t *testing.T) {
	f := query.NewFakeNDMS()
	f.ExpectCreate("OpkgTun3")
	sc := NewSaveCoordinator(f, &fakePublisher{}, time.Hour, time.Hour, 0, nil)
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	cmds := NewInterfaceCommands(failSettingsPoster{f}, sc, q)

	c, err := cmds.CreateOpkgTun(context.Background(), "OpkgTun3", "t", freeFor(t, "opkgtun3"))
	if err == nil || !strings.Contains(err.Error(), "injected: settings rejected") || c != (query.Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if f.Has("OpkgTun3") {
		t.Fatalf("запись без настроек осталась: %v", f.Posts)
	}
	var cmdPosts []string // без точечных чтений кэша
	for _, p := range f.Posts {
		if !strings.HasPrefix(p, `{"show"`) {
			cmdPosts = append(cmdPosts, p)
		}
	}
	if len(cmdPosts) != 3 || cmdPosts[0] != `{"interface":{"OpkgTun3":{}}}` || cmdPosts[2] != `{"interface":{"OpkgTun3":{"no":true}}}` {
		t.Fatalf("posts = %v, want [create, settings, no interface]", f.Posts)
	}
	if f.E != 0 || f.Phantoms != 0 || f.C != 0 {
		t.Fatalf("E=%d Phantoms=%d C=%d", f.E, f.Phantoms, f.C)
	}
	if !q.Interfaces.ClaimOwnDestroyed("OpkgTun3") || q.Interfaces.ClaimOwnConf("OpkgTun3", "running") {
		t.Fatal("снос не выдал кредит своего ifdestroyed (или выдан кредит грани conf)")
	}
}
