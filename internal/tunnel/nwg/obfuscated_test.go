package nwg

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/obfuscator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

type fakeObfRunner struct {
	mu      sync.Mutex
	started map[string]*storage.Obfuscator
	alive   map[string]bool
}

func newFakeObfRunner() *fakeObfRunner {
	return &fakeObfRunner{started: map[string]*storage.Obfuscator{}, alive: map[string]bool{}}
}

func (f *fakeObfRunner) Start(_ context.Context, id string, o *storage.Obfuscator) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *o
	f.started[id] = &cp
	f.alive[id] = true
	return nil
}

func (f *fakeObfRunner) Stop(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.alive, id)
	return nil
}

func (f *fakeObfRunner) Alive(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[id]
}

// captureNDMS: GET /show/ip/route → default via ISP0; POST — копит тела и отвечает
// пустым объектом (batch — массивом той же длины). Ходит через transport.Client,
// который реализует и query.Getter, и command.Poster — поэтому и RCI-батч, и
// RouteCommands приходят на этот же сервер.
//
// Запрос состояния интерфейса (POST {"show":{"interface":…}}) в posts не
// попадает — там только команды; ответ задаётся ifaceResp, по умолчанию
// «интерфейса нет».
type captureNDMS struct {
	srv       *httptest.Server
	mu        sync.Mutex
	posts     []string
	failBatch bool   // RCI-батч (массив команд) отвечает 500
	ifaceResp string // тело ответа на show interface
}

func newCaptureNDMS(t *testing.T) *captureNDMS {
	t.Helper()
	c := &captureNDMS{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/show/ip/route") {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"destination": "0.0.0.0/0", "interface": "ISP0"}})
			return
		}
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), `"show"`) {
			c.mu.Lock()
			resp := c.ifaceResp
			c.mu.Unlock()
			if resp == "" {
				resp = `{"show":{"interface":{}}}`
			}
			_, _ = w.Write([]byte(resp))
			return
		}
		c.mu.Lock()
		c.posts = append(c.posts, string(b))
		failBatch := c.failBatch
		c.mu.Unlock()
		if strings.HasPrefix(strings.TrimSpace(string(b)), "[") {
			if failBatch {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			var arr []any
			_ = json.Unmarshal(b, &arr)
			_ = json.NewEncoder(w).Encode(make([]map[string]any, len(arr)))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *captureNDMS) joined() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.posts, "\n")
}

// firstPostWith — индекс первого тела с подстрокой, -1 если такого нет.
// Нужен для порядка «сначала батч, потом host-route».
func (c *captureNDMS) firstPostWith(sub string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, p := range c.posts {
		if strings.Contains(p, sub) {
			return i
		}
	}
	return -1
}

// obfIfaceOnRelay — ответ RCI для интерфейса на нашем релее: conf=running, peer
// смотрит в 127.0.0.1:39000 (LocalPort из obfStored), online — по аргументу.
func obfIfaceOnRelay(online bool) string {
	return `{"show":{"interface":{"id":"Wireguard3","link":"up",
		"summary":{"layer":{"conf":"running"}},
		"wireguard":{"status":"up","peer":[{"online":` + strconv.FormatBool(online) + `,"via":"ISP1",
			"remote-endpoint-address":"127.0.0.1","remote-port":39000}]}}}}`
}

func newObfOperator(t *testing.T, n *captureNDMS, fr *fakeObfRunner) *OperatorNativeWG {
	t.Helper()
	tr := transport.NewWithURL(n.srv.URL, transport.NewSemaphore(2))
	q := query.NewQueries(query.Deps{Getter: tr, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	// Save обязателен: mutation.go зовёт save.Request() без nil-гарда.
	sc := command.NewSaveCoordinator(tr, startNopPublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)
	cmds := command.NewCommands(command.Deps{Poster: tr, Save: sc, Queries: q, IsOS5: func() bool { return true }})
	op := &OperatorNativeWG{
		queries:      q,
		commands:     cmds,
		transport:    tr,
		kmod:         NewKmodManager(nil),
		appLog:       logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps),
		supportsASC:  func() bool { return true },
		supportsASC3: func() bool { return true },
		resolveFn:    func(string) (string, int, error) { return "203.0.113.5", 51824, nil },
	}
	t.Cleanup(op.Close)
	op.SetObfuscator(fr)
	return op
}

func obfStored() *storage.AWGTunnel {
	return &storage.AWGTunnel{
		ID: "awg20", Name: "phobos", Backend: "nativewg", NWGIndex: 3, ISPInterface: "ISP0",
		Interface: storage.AWGInterface{Address: "10.25.0.4/32", MTU: 1420, PrivateKey: "k"},
		Peer:      storage.AWGPeer{PublicKey: "PUB", Endpoint: "127.0.0.1:39000", AllowedIPs: []string{"0.0.0.0/0"}},
		Obfuscator: &storage.Obfuscator{
			Flavor: storage.ObfuscatorFlavorPhobos, Target: "vpn.example.com:51824",
			Key: "k", Masking: "STUN", MaxDummy: 4, LocalPort: 39000,
		},
	}
}

func withObfDirs(t *testing.T) {
	t.Helper()
	oc, orun := obfuscator.ConfDir, obfuscator.RunDir
	obfuscator.ConfDir, obfuscator.RunDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() { obfuscator.ConfDir, obfuscator.RunDir = oc, orun })
}

func TestStartObfuscated_RunnerRouteEndpointUp(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	if err := op.Start(context.Background(), obfStored()); err != nil {
		t.Fatal(err)
	}
	if !fr.Alive("awg20") {
		t.Fatal("runner not started")
	}
	posts := n.joined()
	for _, want := range []string{`"host":"203.0.113.5"`, `"interface":"ISP0"`, `"comment":"awgm-obfuscator awg20"`, `127.0.0.1:39000`, `"PUB"`} {
		if !strings.Contains(posts, want) {
			t.Errorf("missing %q in RCI posts:\n%s", want, posts)
		}
	}
	if op.GetTrackedEndpointIP("awg20") != "203.0.113.5" {
		t.Fatal("target IP must be tracked for ResolvedEndpointIP persist")
	}
	// WAN для host-route читается после подъёма интерфейса — значит и сам
	// маршрут ставится после батча.
	batch, route := n.firstPostWith(`"up":true`), n.firstPostWith(`"host":"203.0.113.5"`)
	if batch < 0 || route < 0 || batch > route {
		t.Fatalf("host-route обязан идти после батча (batch=%d route=%d):\n%s", batch, route, posts)
	}
}

// Start прилетает на каждый WAN-up и на рестарт демона: если интерфейс уже
// поднят на наш релей, батч по нему — churn. Маршрут ставится всё равно.
func TestStartObfuscated_AlreadyUpOnRelay_SkipsBatch(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	n.ifaceResp = obfIfaceOnRelay(true)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)

	if err := op.Start(context.Background(), obfStored()); err != nil {
		t.Fatal(err)
	}
	if !fr.Alive("awg20") {
		t.Fatal("релей обязан быть запущен и на уже поднятом интерфейсе")
	}
	posts := n.joined()
	for _, forbidden := range []string{`"up":true`, `"connect"`, `127.0.0.1:39000`} {
		if strings.Contains(posts, forbidden) {
			t.Fatalf("батч по живому интерфейсу: %q в постах:\n%s", forbidden, posts)
		}
	}
	if !strings.Contains(posts, `"host":"203.0.113.5"`) {
		t.Fatalf("host-route обязан стоять и без батча:\n%s", posts)
	}
}

// Залипший интерфейс (conf=running, порт релея тот же, но пир offline — KN-1910)
// обязан получить батч: иначе он останется мёртвым навсегда.
func TestStartObfuscated_RunningButPeerOffline_SendsBatch(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	n.ifaceResp = obfIfaceOnRelay(false)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)

	if err := op.Start(context.Background(), obfStored()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(n.joined(), `"up":true`) {
		t.Fatalf("залипший интерфейс обязан получить батч:\n%s", n.joined())
	}
}

// ISPInterface пуст: WAN берётся из peer.via, прочитанного после батча.
func TestStartObfuscated_HostRouteViaFreshPeerVia(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	// conf не running → батч уходит, а peer.via читается для маршрута.
	n.ifaceResp = `{"show":{"interface":{"id":"Wireguard3","link":"up",
		"wireguard":{"status":"up","peer":[{"online":true,"via":"PPPoE0"}]}}}}`
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	st.ISPInterface = ""

	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(n.joined(), `"interface":"PPPoE0"`) {
		t.Fatalf("host-route обязан идти через свежий peer.via:\n%s", n.joined())
	}
}

// ISPInterface пуст и peer.via в RCI нет — WAN берётся у дефолтного шлюза.
func TestStartObfuscated_HostRouteViaDefaultGateway(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	st.ISPInterface = ""
	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(n.joined(), `"interface":"ISP0"`) {
		t.Fatalf("host route must go via default gateway ISP0:\n%s", n.joined())
	}
}

// Туннель, ставший обфусцированным на ходу, мог оставить запись endpoint-стража:
// в режиме viaNDMS она переписала бы loopback-endpoint реальным адресом сервера.
func TestStartObfuscated_DropsEndpointGuardEntry(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	op.guardRegister(st.ID, guardEntry{
		iface: "nwg3", pubkey: "PUB", endpoint: "203.0.113.5:51824",
		spec: "vpn.example.com:51824", name: "Wireguard3", viaNDMS: true,
	})

	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if op.guardHas(st.ID) {
		t.Fatal("запись стража должна быть снята на обфусцированном пути")
	}
}

// WAN совпал с собственным интерфейсом туннеля — маршрут был бы петлёй.
// Start не валится, но причина обязана доехать до пользователя через Details.
func TestStartObfuscated_RouteLoopRefused_ShowsInDetails(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	st.ISPInterface = "Wireguard3" // == NewNWGNames(st.NWGIndex).NDMSName

	if err := op.Start(context.Background(), st); err != nil {
		t.Fatalf("Start не должен валиться из-за маршрута: %v", err)
	}
	if strings.Contains(n.joined(), `"route"`) {
		t.Fatalf("маршрут через сам туннель ставить нельзя:\n%s", n.joined())
	}

	info := tunnel.StateInfo{State: tunnel.StateRunning}
	op.overlayObfuscatorState(st, &info)
	if info.State != tunnel.StateRunning {
		t.Fatalf("состояние менять не надо: %+v", info)
	}
	if !strings.HasPrefix(info.Details, obfRouteDetailsPrefix) || !strings.Contains(info.Details, "Wireguard3") {
		t.Fatalf("Details не объясняет отсутствие маршрута: %q", info.Details)
	}
}

// Успешный маршрут стирает прежнюю причину: перезапуск после почившего WAN
// не должен вечно показывать старую жалобу.
func TestStartObfuscated_SuccessfulRouteClearsDetails(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	st.ISPInterface = "Wireguard3"
	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}

	st.ISPInterface = "ISP0"
	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	info := tunnel.StateInfo{State: tunnel.StateRunning}
	op.overlayObfuscatorState(st, &info)
	if info.Details != "" {
		t.Fatalf("причина должна быть снята: %q", info.Details)
	}
}

// После рестарта демона trackedIP пуст, а в записи лежит прежний IP target'а:
// маршрут под ним обязан быть снят, иначе останется сиротой навсегда.
func TestStartObfuscated_RemovesStaleTargetRoute(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	st.ResolvedEndpointIP = "198.51.100.1"

	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(n.joined(), `"host":"198.51.100.1","no":true`) {
		t.Fatalf("прежний host-route не снят:\n%s", n.joined())
	}
	if !strings.Contains(n.joined(), `"host":"203.0.113.5"`) {
		t.Fatalf("новый host-route не поставлен:\n%s", n.joined())
	}
}

func TestStartObfuscated_BatchFailureRollsBack(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	n.failBatch = true
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	if err := op.Start(context.Background(), obfStored()); err == nil {
		t.Fatal("expected error")
	}
	if fr.Alive("awg20") {
		t.Fatal("relay must be stopped after failed batch")
	}
	// Маршрут ставится после батча — снимать на откате нечего.
	if strings.Contains(n.joined(), `"host":"203.0.113.5"`) {
		t.Fatalf("host-route не должен ни ставиться, ни сниматься при отказе батча:\n%s", n.joined())
	}
}

// На бутe DNS может быть ещё не готов: отказ резолва не имеет права оставить
// интерфейс поднятым без релея — до RCI-команд дело не доходит вовсе.
func TestStartObfuscated_ResolveFailure_LeavesNDMSUntouched(t *testing.T) {
	withObfDirs(t)
	stubResolveGap(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	op.resolveFn = func(string) (string, int, error) { return "", 0, errors.New("no DNS") }

	if err := op.Start(context.Background(), obfStored()); err == nil {
		t.Fatal("Start обязан упасть на отказе резолва")
	}
	if fr.Alive("awg20") {
		t.Fatal("релей обязан быть погашен")
	}
	if n.joined() != "" {
		t.Fatalf("NDMS-команд быть не должно:\n%s", n.joined())
	}
}

func TestSyncKmodSlot_NoopForObfuscated(t *testing.T) {
	op := &OperatorNativeWG{appLog: logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps)}
	t.Cleanup(op.Close)
	if err := op.SyncKmodSlot(context.Background(), obfStored()); err != nil {
		t.Fatal(err)
	}
	if err := op.RestoreKmodTunnel(context.Background(), obfStored()); err != nil {
		t.Fatal(err)
	}
}

func TestStopObfuscated_StopsRunnerAndRemovesRoute(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	st.ResolvedEndpointIP = "203.0.113.5"
	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if err := op.Stop(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if fr.Alive("awg20") {
		t.Fatal("runner still alive")
	}
	if !strings.Contains(n.joined(), `"no":true,"host":"203.0.113.5"`) && !strings.Contains(n.joined(), `"host":"203.0.113.5","no":true`) {
		t.Fatalf("host route not removed:\n%s", n.joined())
	}
}

// У двух туннелей target может резолвиться в один IP: host-route до него общий,
// и Stop одного не имеет права обрубить второй.
func TestStopObfuscated_SharedHostRouteKeptForNeighbour(t *testing.T) {
	for _, tc := range []struct {
		name        string
		held        bool
		wantRemoved bool
	}{
		{"сосед держит маршрут", true, false},
		{"соседа нет", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withObfDirs(t)
			n := newCaptureNDMS(t)
			fr := newFakeObfRunner()
			op := newObfOperator(t, n, fr)
			var gotID, gotIP string
			op.SetObfuscatorRouteSharing(func(excludeID, ip string) bool {
				gotID, gotIP = excludeID, ip
				return tc.held
			})
			st := obfStored()
			st.ResolvedEndpointIP = "203.0.113.5"

			if err := op.Stop(context.Background(), st); err != nil {
				t.Fatal(err)
			}
			if gotID != st.ID || gotIP != "203.0.113.5" {
				t.Fatalf("шов вызван с (%q, %q)", gotID, gotIP)
			}
			removed := n.firstPostWith(`"host":"203.0.113.5"`) >= 0
			if removed != tc.wantRemoved {
				t.Fatalf("removed = %v, want %v:\n%s", removed, tc.wantRemoved, n.joined())
			}
		})
	}
}

func TestDeleteObfuscated_RemovesConf(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	if err := obfuscator.WriteConf(st.ID, st.Obfuscator); err != nil {
		t.Fatal(err)
	}
	if err := op.Delete(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(obfuscator.ConfPath(st.ID)); !os.IsNotExist(err) {
		t.Fatalf("conf must be removed, stat err = %v", err)
	}
}

func TestGetState_ObfuscatorDown_IsBrokenWithDetails(t *testing.T) {
	// Полный GetState требует RCI show interface; проверяем чистый оверлей.
	fr := newFakeObfRunner()
	op := &OperatorNativeWG{appLog: logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps)}
	t.Cleanup(op.Close)
	op.SetObfuscator(fr)
	info := tunnel.StateInfo{State: tunnel.StateRunning}
	op.overlayObfuscatorState(obfStored(), &info)
	if info.State != tunnel.StateBroken || info.Details != obfuscator.DetailsNotRunning {
		t.Fatalf("%+v", info)
	}
	info = tunnel.StateInfo{State: tunnel.StateStopped}
	op.overlayObfuscatorState(obfStored(), &info)
	if info.State != tunnel.StateStopped || info.Details != "" {
		t.Fatalf("stopped must not be overlaid: %+v", info)
	}
}

func TestObfSlotPredicate_LiveRelayCountsAsSlot(t *testing.T) {
	fr := newFakeObfRunner()
	op := &OperatorNativeWG{appLog: logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps)}
	t.Cleanup(op.Close)
	op.SetObfuscator(fr)
	st := obfStored()
	pred := op.obfSlotPredicate(st)
	if pred(39000) {
		t.Fatal("релея нет — слота нет")
	}
	_ = fr.Start(context.Background(), st.ID, st.Obfuscator)
	if !pred(39000) {
		t.Fatal("живой релей на своём порту = слот")
	}
	if pred(39001) {
		t.Fatal("чужой порт слотом быть не может")
	}
}

func TestSyncObfuscator_MovesHostRouteToNewTarget(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	st.ResolvedEndpointIP = "198.51.100.1" // маршрут стоит под прежним адресом target

	targetIP, err := op.SyncObfuscator(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if targetIP != "203.0.113.5" {
		t.Fatalf("targetIP = %q", targetIP)
	}
	if !fr.Alive(st.ID) {
		t.Fatal("релей должен быть перезапущен")
	}
	posts := n.joined()
	if !strings.Contains(posts, `"host":"198.51.100.1","no":true`) {
		t.Fatalf("старый host-route не снят:\n%s", posts)
	}
	if !strings.Contains(posts, `"host":"203.0.113.5"`) {
		t.Fatalf("новый host-route не поставлен:\n%s", posts)
	}
}

// Петля могла осесть в ЗАПИСИ туннеля под прежними версиями (трекер её не
// принимает с F230). Маршрута под этим адресом никогда не было: Start не имеет
// права слать в NDMS no-route на собственный loopback.
//
// Состояние готовим именно записью: через trackEndpointIP оно больше не
// создаётся, и тест, оставленный на прежней подготовке, проходил бы при любом
// содержимом obfRouteIP — то есть сторожил бы пустоту.
func TestStartObfuscated_LoopbackTrackedIPIsNotRemoved(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	st.ResolvedEndpointIP = "127.0.0.1" // наследство прежней версии в записи

	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	posts := n.joined()
	if strings.Contains(posts, `"host":"127.0.0.1"`) {
		t.Fatalf("маршрут на loopback не трогаем:\n%s", posts)
	}
	// И не снимаем ничего вовсе: отброшенный prevIP не должен превращаться
	// в no-route (в том числе с пустым host).
	if rm := n.routeRemovals(); len(rm) > 0 {
		t.Fatalf("снятие на пустом prevIP: %v", rm)
	}
	if !strings.Contains(posts, `"host":"203.0.113.5"`) {
		t.Fatalf("новый host-route не поставлен:\n%s", posts)
	}
}

// Не только петля: в записи туннеля мог осесть и «неуказанный» 0.0.0.0 —
// фильтрующий DNS отдаёт его на заблокированный домен. Маршрута под ним не
// было, снимать нечего.
func TestStartObfuscated_UnroutableStoredIPIsNotRemoved(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	fr := newFakeObfRunner()
	op := newObfOperator(t, n, fr)
	st := obfStored()
	st.ResolvedEndpointIP = "0.0.0.0"

	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	posts := n.joined()
	if strings.Contains(posts, `"host":"0.0.0.0"`) {
		t.Fatalf("маршрут на «неуказанный» адрес не трогаем:\n%s", posts)
	}
	if !strings.Contains(posts, `"host":"203.0.113.5"`) {
		t.Fatalf("новый host-route не поставлен:\n%s", posts)
	}
}

func TestStartPlainWG_WithoutObfuscator_StillRejected(t *testing.T) {
	op := &OperatorNativeWG{appLog: logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps)}
	t.Cleanup(op.Close)
	st := obfStored()
	st.Obfuscator = nil
	if err := op.Start(context.Background(), st); err != tunnel.ErrNotObfuscated {
		t.Fatalf("got %v", err)
	}
}

// F236: у v6-таргета host-route выражается формой ipv6 с prefix /128. Без
// флага V6 запрос уходил v4-формой, роутер отвечал «invalid destination host»,
// и маршрута не было вовсе — трафик релея уходил в сам туннель.
func TestAddObfHostRoute_V6TargetUsesIPv6Form(t *testing.T) {
	n := newCaptureNDMS(t)
	op := newObfOperator(t, n, &fakeObfRunner{})
	stored := obfStored()

	if err := op.addObfHostRoute(context.Background(), stored, "2001:db8::5", "ISP0"); err != nil {
		t.Fatalf("addObfHostRoute: %v", err)
	}

	if n.firstPostWith(`"prefix":"2001:db8::5/128"`) < 0 {
		t.Fatalf("v6 host-route ушёл не той формой: %v", n.posts)
	}
	if n.firstPostWith(`"host":"2001:db8::5"`) >= 0 {
		t.Fatalf("v4-форма с v6-адресом отвергается роутером: %v", n.posts)
	}
	// Без WAN маршрут бессмыслен (трафик релея уйдёт в сам туннель), без
	// метки владения его не отличить от чужого при уборке.
	if n.firstPostWith(`"interface":"ISP0"`) < 0 {
		t.Fatalf("v6 host-route без WAN: %v", n.posts)
	}
	if n.firstPostWith(`"comment":"awgm-obfuscator awg20"`) < 0 {
		t.Fatalf("v6 host-route без метки владения: %v", n.posts)
	}
}

// v4-таргет как был.
func TestAddObfHostRoute_V4TargetUsesIPv4Form(t *testing.T) {
	n := newCaptureNDMS(t)
	op := newObfOperator(t, n, &fakeObfRunner{})
	stored := obfStored()

	if err := op.addObfHostRoute(context.Background(), stored, "203.0.113.5", "ISP0"); err != nil {
		t.Fatalf("addObfHostRoute: %v", err)
	}

	if n.firstPostWith(`"host":"203.0.113.5"`) < 0 {
		t.Fatalf("v4 host-route ушёл не той формой: %v", n.posts)
	}
}

// routeRemovals — посты, снимающие маршрут (снятие адреса интерфейса под эту
// проверку не подпадает: там нет ключа "route").
func (c *captureNDMS) routeRemovals() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, p := range c.posts {
		if strings.Contains(p, `"route"`) && strings.Contains(p, `"no":true`) {
			out = append(out, p)
		}
	}
	return out
}

// reset забывает накопленные посты: нужен, когда проверяется ВТОРОЙ Start,
// а первый только готовит состояние.
func (c *captureNDMS) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.posts = nil
}

// Смена WAN при неизменном адресе: запись NDMS ключуется парой (host, interface),
// и без снятия прежней их становится две — одна через мёртвый канал (стенд 5.01).
// Состояние готовится настоящим Start, а не выставленным полем: сверка идёт с
// тем, что оператор сам отправил в NDMS.
func TestStartObfuscated_WANChanged_ClearsRouteOnOldWAN(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	op := newObfOperator(t, n, newFakeObfRunner())
	st := obfStored() // ISPInterface = ISP0

	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	n.reset()

	// Тот же адрес (резолв даёт 203.0.113.5), но WAN сменился.
	st.ResolvedEndpointIP = "203.0.113.5"
	st.ISPInterface = "ISP1"
	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}

	posts := n.joined()
	// Снятие адресуется парой (host, interface) прежнего WAN: соседняя запись
	// на тот же адрес через другой канал не наша и остаться обязана.
	if !strings.Contains(posts, `"host":"203.0.113.5","interface":"ISP0","no":true`) {
		t.Fatalf("запись на прежнем WAN не снята точной формой:\n%s", posts)
	}
	if n.firstPostWith(`"interface":"ISP0","no":true`) > n.firstPostWith(`"interface":"ISP1"`) {
		t.Fatalf("снятие обязано идти до добавления:\n%s", posts)
	}
}

// Тот же WAN и тот же адрес — снимать нечего: лишний no-route оставил бы окно
// без маршрута на каждом WAN-up соседнего канала.
func TestStartObfuscated_SameWAN_KeepsRoute(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	op := newObfOperator(t, n, newFakeObfRunner())
	st := obfStored()

	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	n.reset()

	st.ResolvedEndpointIP = "203.0.113.5"
	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}

	if rm := n.routeRemovals(); len(rm) > 0 {
		t.Fatalf("маршрут не менялся, снятие лишнее: %v", rm)
	}
}

// Рестарт демона: реестр WAN пуст, а адрес в записи есть. Под каким WAN стоит
// запись — неизвестно, поэтому снимаем вслепую, заодно унося мусор прошлой
// жизни (RemoveHostRoute чистит все записи по адресу).
func TestStartObfuscated_WANUnknownAfterRestart_ClearsBlind(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	op := newObfOperator(t, n, newFakeObfRunner())
	st := obfStored()
	st.ResolvedEndpointIP = "203.0.113.5"

	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}

	if n.firstPostWith(`{"ip":{"route":{"host":"203.0.113.5","no":true}}}`) < 0 {
		t.Fatalf("при неизвестном WAN снимаем слепой формой по адресу:\n%s", n.joined())
	}
}

// Stop снимает маршрут — значит и память о его WAN обязана уйти, иначе
// следующий Start на том же WAN решит, что снимать нечего.
func TestStopObfuscated_ForgetsRoutedWAN(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	op := newObfOperator(t, n, newFakeObfRunner())
	st := obfStored()

	if err := op.Start(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	st.ResolvedEndpointIP = "203.0.113.5"
	op.stopObfuscated(context.Background(), st)

	if _, known := op.routedObfWAN(st.ID); known {
		t.Fatal("после Stop WAN маршрута обязан быть забыт")
	}
}

// Петля (дефолт через сам туннель) при живом прежнем маршруте: поставить новый
// нечем, значит и снимать старый нельзя — иначе трафик релея уйдёт в туннель,
// ради чего маршрут и существует.
func TestStartObfuscated_RouteLoopRefused_KeepsPreviousRoute(t *testing.T) {
	withObfDirs(t)
	n := newCaptureNDMS(t)
	op := newObfOperator(t, n, newFakeObfRunner())
	st := obfStored()
	st.ISPInterface = "Wireguard3"        // == NewNWGNames(st.NWGIndex).NDMSName
	st.ResolvedEndpointIP = "203.0.113.5" // адрес не менялся: снимать нечего и незачем

	if err := op.Start(context.Background(), st); err != nil {
		t.Fatalf("Start не должен валиться из-за маршрута: %v", err)
	}

	if rm := n.routeRemovals(); len(rm) > 0 {
		t.Fatalf("прежний маршрут снят, а новый поставить нечем: %v", rm)
	}
	if op.obfRouteErrFor(st.ID) == "" {
		t.Fatal("причина отсутствия маршрута обязана попасть в реестр")
	}
}
