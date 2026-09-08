package nwg

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// eventLog — единая лента событий теста: RCI-батчи и вызовы wg, в порядке
// исполнения. Даёт проверять ОТНОСИТЕЛЬНЫЙ порядок (wg set строго после
// батча с interface up) — мутация «wg set до батча» должна валить тест.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(e string) {
	l.mu.Lock()
	l.events = append(l.events, e)
	l.mu.Unlock()
}

func (l *eventLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// recordingPoster фиксирует command-層 RCI-вызовы (SetASCParams и т.п.) —
// fail-fast обязан случиться раньше ЛЮБОГО RCI, включая commands-путь.
type recordingPoster struct {
	mu    sync.Mutex
	posts int
}

func (r *recordingPoster) Post(context.Context, any) (json.RawMessage, error) {
	r.mu.Lock()
	r.posts++
	r.mu.Unlock()
	return json.RawMessage(`{}`), nil
}

func (r *recordingPoster) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.posts
}

type startNopPublisher struct{}

func (startNopPublisher) Publish(string, any) {}

func newStartTestCommands(poster *recordingPoster) *command.Commands {
	q := query.NewQueries(query.Deps{
		Getter: query.NewFakeGetter(),
		Logger: query.NopLogger(),
		IsOS5:  func() bool { return true },
	})
	sc := command.NewSaveCoordinator(poster, startNopPublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)
	return command.NewCommands(command.Deps{Poster: poster, Save: sc, Queries: q, IsOS5: func() bool { return true }})
}

// stubWGTool подменяет lookup/run wg; каждый вызов пишется в лог событий.
func stubWGTool(t *testing.T, log *eventLog, path string, run func(ctx context.Context, binary string, args ...string) error) *[][]string {
	t.Helper()
	origLookup, origRun, origDelay := wgToolLookup, wgToolRun, wgSetRetryDelay
	var mu sync.Mutex
	var calls [][]string
	wgToolLookup = func() string { return path }
	wgSetRetryDelay = 0
	wgToolRun = func(ctx context.Context, binary string, args ...string) error {
		mu.Lock()
		calls = append(calls, append([]string{binary}, args...))
		mu.Unlock()
		if log != nil {
			log.add("wg " + strings.Join(args, " "))
		}
		if run != nil {
			return run(ctx, binary, args...)
		}
		return nil
	}
	t.Cleanup(func() { wgToolLookup, wgToolRun, wgSetRetryDelay = origLookup, origRun, origDelay })
	return &calls
}

// rciBatchServer отвечает на батчи, сохраняет последнее тело запроса и
// пишет каждый батч в лог событий.
type rciBatchServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	lastBody string
}

func (s *rciBatchServer) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastBody
}

func newRCIBatchServer(t *testing.T, log *eventLog) *rciBatchServer {
	t.Helper()
	s := &rciBatchServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.lastBody = string(body)
		s.mu.Unlock()
		log.add("rci-batch")
		_, _ = w.Write([]byte(`[{}, {}, {}]`))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func newStartTestOperator(t *testing.T, srvURL string, poster *recordingPoster, resolvedIP string, port int) *OperatorNativeWG {
	t.Helper()
	o := &OperatorNativeWG{
		transport:   transport.NewWithURL(srvURL, transport.NewSemaphore(2)),
		commands:    newStartTestCommands(poster),
		appLog:      logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps),
		supportsASC: func() bool { return true },
		resolveFn: func(endpoint string) (string, int, error) {
			return resolvedIP, port, nil
		},
	}
	t.Cleanup(o.Close)
	return o
}

func startTestTunnel(endpoint string) *storage.AWGTunnel {
	return &storage.AWGTunnel{
		ID:       "awg20",
		Name:     "t-v6",
		NWGIndex: 3,
		Interface: storage.AWGInterface{
			PrivateKey:     "priv",
			Address:        "10.0.0.2/32",
			AWGObfuscation: storage.AWGObfuscation{Jc: 4, Jmin: 40, Jmax: 70},
		},
		Peer: storage.AWGPeer{
			PublicKey: "PUBKEY",
			Endpoint:  endpoint,
		},
	}
}

// decodeBatchCommands разбирает lastBody батча в список JSON-команд.
func decodeBatchCommands(t *testing.T, body string) []string {
	t.Helper()
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(body), &arr); err != nil {
		t.Fatalf("batch body is not a JSON array: %v\n%s", err, body)
	}
	out := make([]string, len(arr))
	for i, raw := range arr {
		out[i] = string(raw)
	}
	return out
}

// v6: батч несёт endpoint-ЗАГЛУШКУ (перетирает возможный старый v4 в конфиге
// NDMS), реальный [v6]:port уходит в ядро через wg set СТРОГО ПОСЛЕ батча,
// туннель регистрируется в endpoint-страже.
func TestStartNative_IPv6EndpointViaWGTool(t *testing.T) {
	log := &eventLog{}
	s := newRCIBatchServer(t, log)
	poster := &recordingPoster{}
	op := newStartTestOperator(t, s.srv.URL, poster, "2a02:6b8::feed:ff", 51820)
	calls := stubWGTool(t, log, "/opt/bin/wg", nil)

	stored := startTestTunnel("[2a02:6b8::feed:ff]:51820")
	if err := op.startNative(context.Background(), stored); err != nil {
		t.Fatalf("startNative: %v", err)
	}

	if strings.Contains(s.last(), "2a02:6b8") {
		t.Fatalf("v6 endpoint must not reach RCI:\n%s", s.last())
	}
	cmds := decodeBatchCommands(t, s.last())
	if len(cmds) != 3 {
		t.Fatalf("batch must carry exactly 3 commands (endpoint-placeholder, connect, up), got %d:\n%s", len(cmds), s.last())
	}
	if !strings.Contains(cmds[0], ndmsEndpointPlaceholder) {
		t.Fatalf("first batch command must set the placeholder endpoint, got %s", cmds[0])
	}

	if len(*calls) != 1 {
		t.Fatalf("wg tool calls = %d, want 1: %v", len(*calls), *calls)
	}
	got := strings.Join((*calls)[0], " ")
	want := "/opt/bin/wg set nwg3 peer PUBKEY endpoint [2a02:6b8::feed:ff]:51820"
	if got != want {
		t.Fatalf("wg call = %q, want %q", got, want)
	}

	// Порядок: wg set — после RCI-батча (up/down NDMS сбрасывает endpoint).
	events := log.list()
	lastBatch, wgSet := -1, -1
	for i, e := range events {
		if e == "rci-batch" {
			lastBatch = i
		}
		if strings.HasPrefix(e, "wg set") {
			wgSet = i
		}
	}
	if wgSet == -1 || wgSet < lastBatch {
		t.Fatalf("wg set must come after the RCI batch, events: %v", events)
	}

	entry, ok := op.guardGet(stored.ID)
	if !ok {
		t.Fatal("v6 tunnel must be registered in the endpoint guard")
	}
	// spec обязателен: без него DDNS-перерезолв в sweep молча мёртв для
	// туннелей, взятых под стражу при старте (главный путь наполнения).
	if entry.pubkey != "PUBKEY" || entry.endpoint != "[2a02:6b8::feed:ff]:51820" ||
		entry.spec != stored.Peer.Endpoint || entry.iface != "nwg3" {
		t.Fatalf("guard entry incomplete: %+v", entry)
	}
}

// v4: прежний путь — реальный endpoint в RCI-батче, wg не вызывается,
// в страже туннеля нет.
func TestStartNative_IPv4EndpointViaRCI(t *testing.T) {
	log := &eventLog{}
	s := newRCIBatchServer(t, log)
	poster := &recordingPoster{}
	op := newStartTestOperator(t, s.srv.URL, poster, "203.0.113.7", 51820)
	calls := stubWGTool(t, log, "/opt/bin/wg", nil)

	stored := startTestTunnel("203.0.113.7:51820")
	if err := op.startNative(context.Background(), stored); err != nil {
		t.Fatalf("startNative: %v", err)
	}
	cmds := decodeBatchCommands(t, s.last())
	if len(cmds) != 3 || !strings.Contains(cmds[0], "203.0.113.7:51820") {
		t.Fatalf("v4 endpoint must go through RCI as the first command:\n%s", s.last())
	}
	if len(*calls) != 0 {
		t.Fatalf("wg tool must not be called for v4: %v", *calls)
	}
	if op.guardHas(stored.ID) {
		t.Fatal("v4 tunnel must not be in the endpoint guard")
	}
}

// Нет бинаря wg — ошибка ДО каких-либо RCI-команд: и transport-батчей,
// и commands-пути (SetASCParams).
func TestStartNative_IPv6WithoutWGToolFailsFast(t *testing.T) {
	log := &eventLog{}
	s := newRCIBatchServer(t, log)
	poster := &recordingPoster{}
	op := newStartTestOperator(t, s.srv.URL, poster, "2a02:6b8::1", 51820)
	stubWGTool(t, log, "", nil)

	err := op.startNative(context.Background(), startTestTunnel("[2a02:6b8::1]:51820"))
	if err == nil || !strings.Contains(err.Error(), "wireguard-tools") {
		t.Fatalf("want wireguard-tools hint error, got %v", err)
	}
	if s.last() != "" {
		t.Fatalf("transport RCI must not be touched, got body:\n%s", s.last())
	}
	if poster.count() != 0 {
		t.Fatalf("commands RCI (SetASCParams etc.) must not be touched, got %d posts", poster.count())
	}
}

// Гонка с асинхронным поднятием интерфейса: первые попытки wg set падают
// (девайса ещё нет) — ретраи добивают, старт успешен.
func TestStartNative_WGSetRetries(t *testing.T) {
	log := &eventLog{}
	s := newRCIBatchServer(t, log)
	poster := &recordingPoster{}
	op := newStartTestOperator(t, s.srv.URL, poster, "2a02:6b8::1", 51820)
	attempt := 0
	calls := stubWGTool(t, log, "/opt/bin/wg", func(context.Context, string, ...string) error {
		attempt++
		if attempt < 3 {
			return errors.New("Unable to modify interface: No such device")
		}
		return nil
	})

	if err := op.startNative(context.Background(), startTestTunnel("[2a02:6b8::1]:51820")); err != nil {
		t.Fatalf("startNative must survive transient wg set failures: %v", err)
	}
	if len(*calls) != 3 {
		t.Fatalf("wg set attempts = %d, want 3", len(*calls))
	}
}

// Ошибка wg set после всех ретраев пробрасывается как ошибка старта.
func TestStartNative_WGToolFailurePropagates(t *testing.T) {
	log := &eventLog{}
	s := newRCIBatchServer(t, log)
	poster := &recordingPoster{}
	op := newStartTestOperator(t, s.srv.URL, poster, "2a02:6b8::1", 51820)
	stubWGTool(t, log, "/opt/bin/wg", func(context.Context, string, ...string) error {
		return context.DeadlineExceeded
	})

	err := op.startNative(context.Background(), startTestTunnel("[2a02:6b8::1]:51820"))
	if err == nil || !strings.Contains(err.Error(), "wg set") {
		t.Fatalf("want wg set error, got %v", err)
	}
}
