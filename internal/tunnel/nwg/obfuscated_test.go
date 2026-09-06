package nwg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
type captureNDMS struct {
	srv       *httptest.Server
	mu        sync.Mutex
	posts     []string
	failBatch bool // RCI-батч (массив команд) отвечает 500
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
	for _, want := range []string{`"host":"203.0.113.5"`, `"interface":"ISP0"`, `127.0.0.1:39000`, `"PUB"`} {
		if !strings.Contains(posts, want) {
			t.Errorf("missing %q in RCI posts:\n%s", want, posts)
		}
	}
	if op.GetTrackedEndpointIP("awg20") != "203.0.113.5" {
		t.Fatal("target IP must be tracked for ResolvedEndpointIP persist")
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
	if !strings.Contains(n.joined(), `"host":"203.0.113.5"`) || !strings.Contains(n.joined(), `"no":true`) {
		t.Fatalf("host route must be removed on rollback:\n%s", n.joined())
	}
}

func TestSyncKmodSlot_NoopForObfuscated(t *testing.T) {
	op := &OperatorNativeWG{appLog: logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps)}
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

func TestStartPlainWG_WithoutObfuscator_StillRejected(t *testing.T) {
	op := &OperatorNativeWG{appLog: logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps)}
	st := obfStored()
	st.Obfuscator = nil
	if err := op.Start(context.Background(), st); err != tunnel.ErrNotObfuscated {
		t.Fatalf("got %v", err)
	}
}
