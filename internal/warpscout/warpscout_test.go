package warpscout

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

// ── фейки ────────────────────────────────────────────────────────

type plainCipher struct{}

func (plainCipher) Encrypt(s string) (string, error) { return "enc:" + s, nil }
func (plainCipher) Decrypt(s string) (string, error) {
	if !strings.HasPrefix(s, "enc:") {
		return "", errors.New("bad")
	}
	return strings.TrimPrefix(s, "enc:"), nil
}

type fakeRunner struct {
	mu     sync.Mutex
	hits   []Hit
	calls  []ScanParams
	runErr error
}

func (r *fakeRunner) Available() (string, bool) { return "/fake", true }
func (r *fakeRunner) Run(ctx context.Context, p ScanParams, on func(EngineEvent)) error {
	r.mu.Lock()
	r.calls = append(r.calls, p)
	hits := append([]Hit(nil), r.hits...)
	r.mu.Unlock()
	on(EngineEvent{Type: "start", Total: 10})
	for _, h := range hits {
		on(EngineEvent{Type: "hit", Endpoint: h.Endpoint, PingMs: h.PingMs, Colo: h.Colo, Loc: h.Loc, Warp: "on"})
	}
	on(EngineEvent{Type: "done", Scanned: 10, Hits: len(hits)})
	return r.runErr
}

type fakeTunnels struct {
	mu      sync.Mutex
	refs    map[string]*TunnelRef
	swaps   []string
	created []string
	getErr  error
	onSwap  func(r *TunnelRef)
}

func key(m TargetMode, id string) string { return string(m) + "/" + id }

func (f *fakeTunnels) List(context.Context) ([]TunnelRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []TunnelRef
	for _, r := range f.refs {
		out = append(out, *r)
	}
	return out, nil
}

func (f *fakeTunnels) Get(_ context.Context, m TargetMode, id string) (*TunnelRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	r, ok := f.refs[key(m, id)]
	if !ok {
		return nil, errors.New("нет")
	}
	c := *r
	return &c, nil
}

func (f *fakeTunnels) Create(_ context.Context, m TargetMode, name, conf string) (*TunnelRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "new" + string(rune('0'+len(f.created)))
	f.created = append(f.created, conf)
	r := &TunnelRef{Mode: m, ID: id, Name: name, Iface: "nwg9", IsWarp: true, Running: true}
	f.refs[key(m, id)] = r
	c := *r
	return &c, nil
}

func (f *fakeTunnels) SwapEndpoint(_ context.Context, m TargetMode, id, ep string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.refs[key(m, id)]
	if !ok {
		return errors.New("нет")
	}
	r.Endpoint = ep
	f.swaps = append(f.swaps, ep)
	if f.onSwap != nil {
		f.onSwap(r)
	}
	return nil
}

type fakeProber struct {
	mu    sync.Mutex
	queue []TraceResult
	def   TraceResult
	err   error
}

func (p *fakeProber) Trace(context.Context, string) (TraceResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return TraceResult{}, p.err
	}
	if len(p.queue) > 0 {
		t := p.queue[0]
		p.queue = p.queue[1:]
		return t, nil
	}
	return p.def, nil
}

func newTestService(t *testing.T) (*Service, *fakeRunner, *fakeTunnels, *fakeProber) {
	t.Helper()
	colorecheckDelay, verifyAfterSwap, verifyRetryDelay = 0, 0, time.Millisecond
	st, err := NewStore(t.TempDir(), plainCipher{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccount(&Account{ID: "a1", Token: "tok", PrivateKey: "cHJpdg==", PublicKey: "pub",
		PeerPublicKey: WarpPeerPublicKey, AddressV4: "172.16.0.2", ClientID: "AQID"}); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	tun := &fakeTunnels{refs: map[string]*TunnelRef{
		key(TargetNative, "awg10"): {Mode: TargetNative, ID: "awg10", Name: "WARP", Iface: "nwg1",
			Endpoint: "162.159.192.1:2408", IsWarp: true, Running: true},
		key(TargetSystem, "Wireguard2"): {Mode: TargetSystem, ID: "Wireguard2", Name: "AWGM", Protected: true,
			ProtectReason: "сервер"},
		key(TargetKernel, "awg0"): {Mode: TargetKernel, ID: "awg0", Name: "home", IsWarp: false, Running: true},
	}}
	pr := &fakeProber{def: TraceResult{Colo: "HEL", Loc: "FI", Warp: "on"}}
	s := New(Deps{Store: st, Runner: r, Tunnels: tun, Prober: pr,
		WAN: func(context.Context) (string, bool) { return "eth3", true }})
	t.Cleanup(s.Stop)
	return s, r, tun, pr
}

func testProfile() Profile {
	return normalizeProfile(Profile{ID: "p1", Name: "WARP", Enabled: true, TargetMode: TargetNative,
		TargetID: "awg10", Filters: DefaultFilters()})
}

// ── тесты ────────────────────────────────────────────────────────

func TestStore_AccountEncryptedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewStore(dir, plainCipher{})
	if err := st.SetAccount(&Account{ID: "x", Token: "secret-token", PrivateKey: "secret-pk"}); err != nil {
		t.Fatal(err)
	}
	st2, err := NewStore(dir, plainCipher{})
	if err != nil {
		t.Fatal(err)
	}
	a := st2.Account()
	if a == nil || a.Token != "secret-token" || a.PrivateKey != "secret-pk" {
		t.Fatalf("учётка не восстановилась: %+v", a)
	}
	if st2.state.Account.TokenCipher != "enc:secret-token" {
		t.Fatal("токен лежит на диске в открытом виде")
	}
}

func TestNormalizeConfig_SingleModeKeepsOnlyActive(t *testing.T) {
	c := normalizeConfig(Config{ProfileMode: ProfileModeSingle, ActiveProfileID: "b", Profiles: []Profile{
		{ID: "a", Enabled: true}, {ID: "b", Enabled: true}, {ID: "c", Enabled: true}}})
	for _, p := range c.Profiles {
		if p.Enabled != (p.ID == "b") {
			t.Fatalf("в single-режиме включён лишний профиль: %+v", c.Profiles)
		}
	}
	m := normalizeConfig(Config{ProfileMode: ProfileModeMulti, Profiles: []Profile{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}}})
	if !m.Profiles[0].Enabled || !m.Profiles[1].Enabled {
		t.Fatal("multi-режим не должен выключать профили")
	}
}

func TestCPSToRaw(t *testing.T) {
	raw, err := CPSToRaw("<b 0xc0ffee><r 4><rd 3><t>")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 3+4+3+4 || hex.EncodeToString(raw[:3]) != "c0ffee" {
		t.Fatalf("неверная развёртка: %x", raw)
	}
	for _, c := range raw[7:10] {
		if c < '0' || c > '9' {
			t.Fatalf("<rd> дал не цифру: %q", c)
		}
	}
	if _, err := CPSToRaw("<zz 1>"); err == nil {
		t.Fatal("неизвестный тег должен отвергаться")
	}
}

func TestBuildConf_WarpSafeAWG(t *testing.T) {
	a := &Account{PrivateKey: "PK", AddressV4: "172.16.0.2", PeerPublicKey: WarpPeerPublicKey}
	conf := BuildConf(a, AWGParams{Jc: 4, Jmin: 40, Jmax: 70, I1: "<b 0x01>"}, ProtocolAWG, "1.2.3.4:2408")
	for _, want := range []string{"Address = 172.16.0.2/32", "Jc = 4", "S1 = 0", "S2 = 0", "H1 = 1", "H4 = 4", "I1 = <b 0x01>", "Endpoint = 1.2.3.4:2408", "MTU = 1280"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("в конфиге нет %q:\n%s", want, conf)
		}
	}
	wg := BuildConf(a, AWGParams{Jc: 4}, ProtocolWG, "1.2.3.4:2408")
	if strings.Contains(wg, "Jc") || strings.Contains(wg, "H1") {
		t.Fatal("WG-конфиг не должен нести параметры AWG")
	}
}

func TestParseTrace(t *testing.T) {
	tr := ParseTrace("fl=1\nh=cp.cloudflare.com\nip=1.2.3.4\ncolo=dme\nloc=RU\nwarp=on\n")
	if tr.Colo != "DME" || tr.Loc != "RU" || tr.Warp != "on" || tr.IP != "1.2.3.4" {
		t.Fatalf("разбор trace: %+v", tr)
	}
}

func TestWatchdog_DMEPolymorphTriggersSwap(t *testing.T) {
	s, r, tun, pr := newTestService(t)
	r.hits = []Hit{{Endpoint: "188.114.97.5:4500", PingMs: 80, Colo: "WAW", Loc: "PL"},
		{Endpoint: "188.114.96.9:2408", PingMs: 40, Colo: "HEL", Loc: "FI"}}
	// Два подряд DME (проверка + подтверждение), затем HEL после подмены.
	pr.queue = []TraceResult{{Colo: "DME", Loc: "RU", Warp: "on"}, {Colo: "DME", Loc: "RU", Warp: "on"}}
	p := testProfile()
	st := &watcherState{}
	s.checkOnce(context.Background(), p, st)

	if len(tun.swaps) != 1 || tun.swaps[0] != "188.114.96.9:2408" {
		t.Fatalf("ожидалась подмена на самый быстрый узел, swaps=%v", tun.swaps)
	}
	call := r.calls[0]
	if len(call.ExcludeEndpoints) != 1 || call.ExcludeEndpoints[0] != "162.159.192.1:2408" {
		t.Fatalf("скан должен исключать текущий endpoint: %v", call.ExcludeEndpoints)
	}
	if call.Ifname != "eth3" || call.MaxHits != swapScanMaxHits || call.Reserved != [3]int{1, 2, 3} {
		t.Fatalf("параметры скана: ifname=%q maxHits=%d reserved=%v", call.Ifname, call.MaxHits, call.Reserved)
	}
	h := s.d.Store.History()
	if len(h) != 1 || !h[0].OK || h[0].NewEndpoint != "188.114.96.9:2408" {
		t.Fatalf("журнал автоподмен: %+v", h)
	}
	if !st.nextSwap.After(time.Now()) {
		t.Fatal("после подмены должен действовать cooldown")
	}
	// Повторный DME во время cooldown не запускает новый скан.
	pr.queue = []TraceResult{{Colo: "DME"}, {Colo: "DME"}}
	s.checkOnce(context.Background(), p, st)
	if len(r.calls) != 1 {
		t.Fatal("скан во время cooldown")
	}
}

func TestWatchdog_SingleFlapIsNotSwapped(t *testing.T) {
	s, r, tun, pr := newTestService(t)
	r.hits = []Hit{{Endpoint: "188.114.96.9:2408", PingMs: 40}}
	pr.queue = []TraceResult{{Colo: "DME", Warp: "on"}} // второй запрос вернёт HEL
	s.checkOnce(context.Background(), testProfile(), &watcherState{})
	if len(tun.swaps) != 0 || len(r.calls) != 0 {
		t.Fatal("единичный всплеск DME не должен вызывать подмену")
	}
}

func TestWatchdog_TimeoutNeedsThreshold(t *testing.T) {
	s, r, tun, pr := newTestService(t)
	r.hits = []Hit{{Endpoint: "188.114.96.9:2408", PingMs: 40}}
	pr.err = errors.New("timeout")
	p := testProfile() // FailThreshold = 2
	st := &watcherState{}
	s.checkOnce(context.Background(), p, st)
	if len(tun.swaps) != 0 {
		t.Fatal("одна ошибка не должна вызывать подмену")
	}
	pr.mu.Lock()
	pr.err = nil
	pr.mu.Unlock()
	pr.err = errors.New("timeout")
	s.checkOnce(context.Background(), p, st)
	if len(tun.swaps) != 1 {
		t.Fatalf("после порога ожидалась подмена, swaps=%v", tun.swaps)
	}
}

func TestWatchdog_WANDownNoAction(t *testing.T) {
	s, r, _, pr := newTestService(t)
	s.d.WAN = func(context.Context) (string, bool) { return "", false }
	pr.err = errors.New("timeout")
	st := &watcherState{fails: 5}
	s.checkOnce(context.Background(), testProfile(), st)
	if len(r.calls) != 0 || st.fails != 0 {
		t.Fatal("при упавшем WAN автопилот не должен сканировать")
	}
}

func TestWatchdog_NoHitsBackoff(t *testing.T) {
	s, r, tun, pr := newTestService(t)
	pr.queue = []TraceResult{{Colo: "DME"}, {Colo: "DME"}}
	st := &watcherState{}
	s.checkOnce(context.Background(), testProfile(), st)
	if len(r.calls) != 1 || len(tun.swaps) != 0 {
		t.Fatal("ожидался скан без подмены")
	}
	if d := time.Until(st.nextSwap); d < 50*time.Second {
		t.Fatalf("ожидался backoff ≥1 мин, получили %s", d)
	}
	ws := s.State().Watch
	_ = ws
}

func TestWatchdog_RefusesProtectedAndForeign(t *testing.T) {
	s, r, _, pr := newTestService(t)
	pr.err = errors.New("timeout")
	for _, p := range []Profile{
		{ID: "x", TargetMode: TargetSystem, TargetID: "Wireguard2", FailThreshold: 1, Filters: DefaultFilters()},
		{ID: "y", TargetMode: TargetKernel, TargetID: "awg0", FailThreshold: 1, Filters: DefaultFilters()},
	} {
		s.checkOnce(context.Background(), p, &watcherState{fails: 10})
	}
	if len(r.calls) != 0 {
		t.Fatal("автопилот тронул защищённый или не-WARP туннель")
	}
	states := map[string]string{}
	s.mu.Lock()
	for id, ws := range s.watch {
		states[id] = ws.State
	}
	s.mu.Unlock()
	if states["x"] != WatchProtected || states["y"] != WatchNotWarp {
		t.Fatalf("состояния: %v", states)
	}
}

func TestSaveConfig_Validation(t *testing.T) {
	s, _, _, _ := newTestService(t)
	ctx := context.Background()
	bad := []Config{
		{ProfileMode: ProfileModeMulti, Profiles: []Profile{{Name: "a", Enabled: true, TargetMode: TargetSystem, TargetID: "Wireguard2"}}},
		{ProfileMode: ProfileModeMulti, Profiles: []Profile{{Name: "a", Enabled: true, TargetMode: TargetKernel, TargetID: "awg0"}}},
		{ProfileMode: ProfileModeMulti, Profiles: []Profile{
			{Name: "a", Enabled: true, TargetMode: TargetNative, TargetID: "awg10"},
			{Name: "b", Enabled: true, TargetMode: TargetNative, TargetID: "awg10"}}},
		{ProfileMode: ProfileModeMulti, Profiles: []Profile{{Name: "a", Enabled: true, TargetMode: "bogus", TargetID: "x"}}},
	}
	for i, c := range bad {
		if _, err := s.SaveConfig(ctx, c); err == nil {
			t.Fatalf("конфиг %d должен быть отвергнут", i)
		}
	}
	good, err := s.SaveConfig(ctx, Config{ProfileMode: ProfileModeMulti, AWG: AWGParams{SignatureProfile: "none"},
		Profiles: []Profile{{Name: "a", Enabled: false, TargetMode: TargetNative, TargetID: "awg10"}}})
	if err != nil {
		t.Fatal(err)
	}
	if good.Profiles[0].ID == "" {
		t.Fatal("профилю не выдан id")
	}
}

func TestApply_CreateWithAutopilotSingleMode(t *testing.T) {
	s, _, tun, _ := newTestService(t)
	_, _ = s.SaveConfig(context.Background(), Config{ProfileMode: ProfileModeSingle, AWG: AWGParams{SignatureProfile: "dns", Jc: 4, Jmin: 40, Jmax: 70}})
	res, err := s.Apply(context.Background(), ApplyRequest{Action: "create", Endpoint: "188.114.96.9:2408",
		Mode: TargetKernel, Name: "WARP-FI", EnableAutopilot: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(tun.created) != 1 || !strings.Contains(tun.created[0], "Endpoint = 188.114.96.9:2408") || !strings.Contains(tun.created[0], "I1 = <") {
		t.Fatalf("конфиг нового туннеля: %v", tun.created)
	}
	cfg := s.d.Store.Config()
	if cfg.ActiveProfileID != res.ProfileID || len(cfg.Profiles) != 1 || !cfg.Profiles[0].Enabled {
		t.Fatalf("профиль автопилота: %+v", cfg)
	}
	if _, err := s.Apply(context.Background(), ApplyRequest{Action: "swap", Endpoint: "1.1.1.1:2408", Mode: TargetSystem, TargetID: "Wireguard2"}); !errors.Is(err, ErrProtected) {
		t.Fatalf("подмена на защищённом интерфейсе должна отвергаться, err=%v", err)
	}
}

func TestManualScan_CollectsSortedHits(t *testing.T) {
	s, r, _, _ := newTestService(t)
	r.hits = []Hit{{Endpoint: "a:1", PingMs: 90}, {Endpoint: "b:2", PingMs: 30}}
	if err := s.StartScan(ScanRequest{Filters: Filters{Protocol: ProtocolWG}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.State().Scan.Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	sc := s.State().Scan
	if sc.Running || len(sc.Hits) != 2 || sc.Hits[0].Endpoint != "b:2" {
		t.Fatalf("итог скана: %+v", sc)
	}
	if r.calls[0].AWG.Jc != 0 {
		t.Fatal("протокол wg не должен слать мусорные пакеты")
	}
}

func TestWatchdog_PostSwapTunnelStoppedOrGetError(t *testing.T) {
	t.Run("stopped_after_swap", func(t *testing.T) {
		s, r, tun, pr := newTestService(t)
		r.hits = []Hit{{Endpoint: "188.114.96.9:2408", PingMs: 40, Colo: "HEL", Loc: "FI"}}
		pr.queue = []TraceResult{{Colo: "DME", Loc: "RU", Warp: "on"}, {Colo: "DME", Loc: "RU", Warp: "on"}}
		tun.onSwap = func(ref *TunnelRef) { ref.Running = false }

		p := testProfile()
		st := &watcherState{}
		s.checkOnce(context.Background(), p, st)

		if len(tun.swaps) != 1 {
			t.Fatalf("ожидалась попытка подмены, swaps=%v", tun.swaps)
		}
		h := s.d.Store.History()
		if len(h) != 1 || h[0].OK || !strings.Contains(h[0].Error, "выключен") {
			t.Fatalf("выключенный после подмены туннель не должен считаться успехом: %+v", h)
		}
		if st.nextSwap.After(time.Now()) {
			t.Fatalf("при провале проверки не должен включаться cooldown: nextSwap=%v", st.nextSwap)
		}
		s.mu.Lock()
		ws := *s.watch[p.ID]
		s.mu.Unlock()
		if ws.State != WatchDegraded {
			t.Fatalf("ожидалось состояние %s, получили %s", WatchDegraded, ws.State)
		}
	})

	t.Run("get_error_after_swap", func(t *testing.T) {
		s, r, tun, pr := newTestService(t)
		r.hits = []Hit{{Endpoint: "188.114.96.9:2408", PingMs: 40, Colo: "HEL", Loc: "FI"}}
		pr.queue = []TraceResult{{Colo: "DME", Loc: "RU", Warp: "on"}, {Colo: "DME", Loc: "RU", Warp: "on"}}
		tun.onSwap = func(*TunnelRef) { tun.getErr = errors.New("ndms unavailable") }

		p := testProfile()
		st := &watcherState{}
		s.checkOnce(context.Background(), p, st)

		h := s.d.Store.History()
		if len(h) != 1 || h[0].OK || !strings.Contains(h[0].Error, "ndms unavailable") {
			t.Fatalf("ошибка Get после подмены не должна маскироваться под успех: %+v", h)
		}
		if st.nextSwap.After(time.Now()) {
			t.Fatalf("при ошибке Get после подмены не должен включаться cooldown: nextSwap=%v", st.nextSwap)
		}
		s.mu.Lock()
		ws := *s.watch[p.ID]
		s.mu.Unlock()
		if ws.State != WatchDegraded {
			t.Fatalf("ожидалось состояние %s, получили %s", WatchDegraded, ws.State)
		}
	})
}

func TestWatchdog_MaxRTTEnforced(t *testing.T) {
	s, r, tun, pr := newTestService(t)
	r.hits = []Hit{{Endpoint: "188.114.96.9:2408", PingMs: 35, Colo: "HEL", Loc: "FI"}}

	p := testProfile()
	p.Filters.MaxRTTMs = 50 // FailThreshold = 2 по умолчанию
	st := &watcherState{}

	// В пределах лимита (ровно 50 мс) — узел исправен.
	pr.queue = []TraceResult{{Colo: "HEL", Loc: "FI", Warp: "on", LatencyMs: 50}}
	s.checkOnce(context.Background(), p, st)
	if st.fails != 0 || len(tun.swaps) != 0 {
		t.Fatalf("50 мс при лимите 50 мс должен проходить: fails=%d swaps=%v", st.fails, tun.swaps)
	}

	// Первое превышение (500 мс > 50 мс) — деградация, но до порога без подмены.
	pr.queue = []TraceResult{{Colo: "HEL", Loc: "FI", Warp: "on", LatencyMs: 500}}
	s.checkOnce(context.Background(), p, st)
	if st.fails != 1 || len(tun.swaps) != 0 {
		t.Fatalf("первое превышение RTT должно увеличить счётчик сбоев без подмены: fails=%d swaps=%v", st.fails, tun.swaps)
	}

	// Второе превышение достигает порога → подмена + контрольный trace 35 мс → успех и cooldown.
	pr.queue = []TraceResult{
		{Colo: "HEL", Loc: "FI", Warp: "on", LatencyMs: 500},
		{Colo: "HEL", Loc: "FI", Warp: "on", LatencyMs: 35},
	}
	s.checkOnce(context.Background(), p, st)
	if len(tun.swaps) != 1 || tun.swaps[0] != "188.114.96.9:2408" {
		t.Fatalf("ожидалась подмена при устойчивом превышении RTT: swaps=%v", tun.swaps)
	}
	h := s.d.Store.History()
	if len(h) != 1 || !h[0].OK {
		t.Fatalf("подмена на быстрый узел должна быть успешной: %+v", h)
	}
	if !st.nextSwap.After(time.Now()) {
		t.Fatal("после успешной подмены должен действовать cooldown")
	}
}

func TestWatchdog_MaxRTTPostSwapVerifyFails(t *testing.T) {
	s, r, tun, pr := newTestService(t)
	r.hits = []Hit{{Endpoint: "188.114.96.9:2408", PingMs: 35, Colo: "HEL", Loc: "FI"}}

	p := testProfile()
	p.Filters.MaxRTTMs = 50
	p.FailThreshold = 1
	st := &watcherState{}

	// И до, и после подмены реальная задержка в туннеле 500 мс > 50 мс.
	pr.queue = []TraceResult{
		{Colo: "HEL", Loc: "FI", Warp: "on", LatencyMs: 500},
		{Colo: "HEL", Loc: "FI", Warp: "on", LatencyMs: 500},
	}
	s.checkOnce(context.Background(), p, st)
	if len(tun.swaps) != 1 {
		t.Fatalf("ожидалась подмена, swaps=%v", tun.swaps)
	}
	h := s.d.Store.History()
	if len(h) != 1 || h[0].OK || !strings.Contains(h[0].Error, "500") {
		t.Fatalf("превышение RTT после подмены должно провалить проверку: %+v", h)
	}
	if st.nextSwap.After(time.Now()) {
		t.Fatalf("после провала контрольного RTT не должен включаться cooldown: %v", st.nextSwap)
	}
}

type stubHTTPDoer struct {
	res *httpclient.Result
	err error
}

func (d stubHTTPDoer) Do(context.Context, httpclient.CallConfig) (*httpclient.Result, error) {
	return d.res, d.err
}

func TestHTTPTraceProber_LatencyMetrics(t *testing.T) {
	body := "colo=HEL\nloc=FI\nwarp=on\n"
	pr := &HTTPTraceProber{Client: stubHTTPDoer{res: &httpclient.Result{
		Body:    body,
		Metrics: httpclient.Metrics{HTTPCode: 200, TimeNameLookup: 0.010, TimeConnect: 0.085, TimeTotal: 0.160},
	}}}
	tr, err := pr.Trace(context.Background(), "nwg1")
	if err != nil || tr.LatencyMs != 75 {
		t.Fatalf("ожидалась задержка connect 75 мс, получили tr=%+v err=%v", tr, err)
	}

	// Fallback на TimeTotal, если TimeConnect не заполнен.
	pr2 := &HTTPTraceProber{Client: stubHTTPDoer{res: &httpclient.Result{
		Body:    body,
		Metrics: httpclient.Metrics{HTTPCode: 200, TimeTotal: 0.120},
	}}}
	tr2, err := pr2.Trace(context.Background(), "nwg1")
	if err != nil || tr2.LatencyMs != 120 {
		t.Fatalf("ожидался fallback на TimeTotal 120 мс, получили tr=%+v err=%v", tr2, err)
	}
}

type failRoundTripper struct{ err error }

func (f failRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestRegisterAccount_V0a4005FlowAndWarpPlusSemantics(t *testing.T) {
	var (
		mu          sync.Mutex
		patchWarped bool
		putLicense  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/reg":
			if r.Header.Get("CF-Client-Version") != apiClientVer {
				t.Errorf("неожиданный CF-Client-Version: %q", r.Header.Get("CF-Client-Version"))
			}
			// Реальный ответ v0a4005: warp_enabled=false, у free-учётки warp_plus=true при quota=0.
			_, _ = w.Write([]byte(`{
				"id": "dev-1",
				"token": "tok-1",
				"warp_enabled": false,
				"account": {"account_type": "free", "warp_plus": true, "quota": 0, "premium_data": 0},
				"config": {
					"client_id": "AQID",
					"peers": [{"public_key": "` + WarpPeerPublicKey + `"}],
					"interface": {"addresses": {"v4": "172.16.0.2", "v6": "2606:4700:110::2"}}
				}
			}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/reg/dev-1":
			if r.Header.Get("Authorization") != "Bearer tok-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var p map[string]any
			_ = json.Unmarshal(body, &p)
			if v, ok := p["warp_enabled"].(bool); ok && v {
				patchWarped = true
			}
			_, _ = w.Write([]byte(`{"id":"dev-1","warp_enabled":true}`))
		case r.Method == http.MethodPut && r.URL.Path == "/reg/dev-1/account":
			var p map[string]string
			_ = json.Unmarshal(body, &p)
			if p["license"] == "bad-key" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errors":[{"message":"invalid license"}]}`))
				return
			}
			putLicense = p["license"]
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/reg/dev-1/account":
			_, _ = w.Write([]byte(`{"account_type":"limited","warp_plus":true,"quota":10737418240}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	origBase := cfAPIBase
	cfAPIBase = srv.URL
	t.Cleanup(func() { cfAPIBase = origBase })

	acct, err := RegisterAccount(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}
	if !patchWarped {
		t.Fatal("ожидался PATCH /reg/{id} с warp_enabled=true")
	}
	if acct.WarpPlus {
		t.Fatal("бесплатная учётка v0a4005 (account_type=free, quota=0) не должна помечаться как WARP+")
	}
	if gotPub, err := PublicKeyOf(acct.PrivateKey); err != nil || gotPub != acct.PublicKey {
		t.Fatalf("несовпадение ключей: got=%q want=%q err=%v", gotPub, acct.PublicKey, err)
	}
	if err := ApplyLicense(context.Background(), srv.Client(), acct, "1111-2222-3333"); err != nil {
		t.Fatalf("ApplyLicense: %v", err)
	}
	if putLicense != "1111-2222-3333" || !acct.WarpPlus || acct.AccountType != "limited" {
		t.Fatalf("после привязки лицензии ожидался WARP+: %+v", acct)
	}
}

func TestService_RegisterFallbackAndKeyRotation(t *testing.T) {
	var (
		mu        sync.Mutex
		regCalls  int
		rotCalls  int
		rotStatus int
		licCalls  int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/reg":
			regCalls++
			id := "dev-fresh"
			if regCalls > 1 {
				id = "dev-fresh-2"
			}
			_, _ = w.Write([]byte(`{
				"id": "` + id + `",
				"token": "tok-fresh",
				"warp_enabled": true,
				"account": {"account_type": "free", "warp_plus": true, "quota": 0},
				"config": {"client_id": "AQID", "peers": [{"public_key": "` + WarpPeerPublicKey + `"}], "interface": {"addresses": {"v4": "172.16.0.2"}}}
			}`))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/reg/"):
			var p map[string]any
			_ = json.Unmarshal(body, &p)
			if _, hasKey := p["key"]; hasKey {
				rotCalls++
				if rotStatus != 0 {
					w.WriteHeader(rotStatus)
					_, _ = w.Write([]byte(`rotation failed`))
					return
				}
				_, _ = w.Write([]byte(`{
					"id": "dev-fresh",
					"warp_enabled": true,
					"config": {"client_id": "BAUG", "peers": [{"public_key": "` + WarpPeerPublicKey + `"}], "interface": {"addresses": {"v4": "172.16.0.2"}}}
				}`))
				return
			}
			_, _ = w.Write([]byte(`{"warp_enabled":true}`))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/account"):
			licCalls++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`invalid license`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	origBase := cfAPIBase
	cfAPIBase = srv.URL
	t.Cleanup(func() { cfAPIBase = origBase })

	st, err := NewStore(t.TempDir(), plainCipher{})
	if err != nil {
		t.Fatal(err)
	}
	blockedClient := &http.Client{Transport: failRoundTripper{err: errors.New("net/http: TLS handshake timeout")}}
	s := New(Deps{
		Store:  st,
		Runner: &fakeRunner{},
		WAN:    func(context.Context) (string, bool) { return "eth3", true },
		HTTP:   func(string) (*http.Client, error) { return blockedClient, nil },
		HTTPFallbacks: func(context.Context) []*http.Client {
			return []*http.Client{srv.Client(), srv.Client()}
		},
	})
	t.Cleanup(s.Stop)

	// 1. Прямой WAN заблокирован ТСПУ → регистрация проходит через резервный выход.
	a1, err := s.Register(context.Background())
	if err != nil {
		t.Fatalf("Register через fallback: %v", err)
	}
	if a1.ID != "dev-fresh" || regCalls != 1 {
		t.Fatalf("ожидалась первичная регистрация через fallback: acct=%+v regCalls=%d", a1, regCalls)
	}
	oldPriv := st.Account().PrivateKey

	// Помечаем учётку как WARP+, чтобы убедиться, что ротация ключа сохраняет лицензию.
	withPlus := st.Account()
	withPlus.License = "plus-key"
	withPlus.AccountType = "limited"
	withPlus.WarpPlus = true
	_ = st.SetAccount(withPlus)

	// 2. Перевыпуск: ротация ключа на месте через резервный выход сохраняет ID и WARP+.
	a2, err := s.Register(context.Background())
	if err != nil {
		t.Fatalf("повторный Register (ротация): %v", err)
	}
	saved := st.Account()
	if rotCalls != 1 || regCalls != 1 || a2.ID != "dev-fresh" || !a2.WarpPlus || saved.License != "plus-key" || saved.PrivateKey == oldPriv {
		t.Fatalf("ротация ключа должна сменить ключ и сохранить WARP+: rot=%d reg=%d saved=%+v", rotCalls, regCalls, saved)
	}

	// 3. Ошибка 400 (неверный ключ лицензии) не должна повторяться по всем резервным клиентам.
	if _, err := s.SetLicense(context.Background(), "bad-key"); err == nil {
		t.Fatal("неверная лицензия должна вернуть ошибку")
	}
	if licCalls != 1 {
		t.Fatalf("HTTP 400 не должен перебирать остальные fallback-клиенты: licCalls=%d", licCalls)
	}

	// 4. Ошибки 400/401/422 при ротации НЕ должны создавать новую регистрацию или затирать WARP+.
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusUnprocessableEntity} {
		mu.Lock()
		rotStatus = code
		mu.Unlock()
		if _, err := s.Register(context.Background()); err == nil {
			t.Fatalf("при HTTP %d на ротации ожидалась ошибка, а не новая регистрация", code)
		}
		if regCalls != 1 {
			t.Fatalf("HTTP %d на ротации не должен вызывать POST /reg (regCalls=%d)", code, regCalls)
		}
		afterErr := st.Account()
		if afterErr == nil || afterErr.ID != "dev-fresh" || afterErr.License != "plus-key" || !afterErr.WarpPlus {
			t.Fatalf("после HTTP %d сохранённый аккаунт WARP+ не должен измениться: %+v", code, afterErr)
		}
	}

	// 5. Если регистрация удалена у Cloudflare (PATCH /reg/{id} -> 404), создаётся новая.
	mu.Lock()
	rotStatus = http.StatusNotFound
	mu.Unlock()
	a3, err := s.Register(context.Background())
	if err != nil || a3.ID != "dev-fresh-2" {
		t.Fatalf("при 404 на ротации ожидалась новая регистрация: a3=%+v err=%v", a3, err)
	}
}

func TestScanInterfaceRouteSelection(t *testing.T) {
	st, err := NewStore(t.TempDir(), plainCipher{})
	if err != nil {
		t.Fatal(err)
	}
	priv, pub, _ := GenerateKeyPair()
	_ = st.SetAccount(&Account{
		ID: "acct-route", Token: "tok", PrivateKey: priv, PublicKey: pub,
		PeerPublicKey: WarpPeerPublicKey, AddressV4: "172.16.0.2",
	})
	runner := &fakeRunner{hits: []Hit{{Endpoint: "162.159.192.1:2408", PingMs: 25, Colo: "HEL", Loc: "FI"}}}
	s := New(Deps{
		Store:  st,
		Runner: runner,
		WAN:    func(context.Context) (string, bool) { return "eth3", true },
		Routes: func(context.Context) []RouteRef {
			return []RouteRef{
				{Name: "eth3", Label: "ISP", Kind: "wan", Up: true},
				{Name: "ppp0", Label: "Backup", Kind: "wan", Up: true},
			}
		},
	})
	t.Cleanup(s.Stop)

	snap := s.State()
	if snap.DefaultWAN != "eth3" || len(snap.Routes) != 2 {
		t.Fatalf("неожиданные маршруты в State: defaultWan=%q routes=%+v", snap.DefaultWAN, snap.Routes)
	}

	// Сохраняем маршрут сканера в Config -> ручной скан берёт его автоматически.
	cfg := st.Config()
	cfg.ScanInterface = "ppp0"
	if _, err := s.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.StartScan(ScanRequest{Filters: DefaultFilters(), MaxHits: 5}); err != nil {
		t.Fatal(err)
	}
	for s.State().Scan.Running {
		time.Sleep(10 * time.Millisecond)
	}
	runner.mu.Lock()
	gotIface := runner.calls[0].Ifname
	runner.mu.Unlock()
	if gotIface != "ppp0" {
		t.Fatalf("ожидался ifname=ppp0 из Config.ScanInterface, получили %q", gotIface)
	}
}

func TestAutopilotStats_CollectionPersistenceAndReset(t *testing.T) {
	s, r, _, pr := newTestService(t)
	p := testProfile()
	cfg := s.d.Store.Config()
	cfg.Profiles = []Profile{p}
	cfg.ActiveProfileID = p.ID
	if err := s.d.Store.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}

	st := &watcherState{}
	// 1. Два успешных чека с разной задержкой и колокейшном.
	pr.queue = []TraceResult{
		{Colo: "HEL", Loc: "FI", Warp: "on", LatencyMs: 30},
		{Colo: "ARN", Loc: "SE", Warp: "on", LatencyMs: 50},
	}
	s.checkOnce(context.Background(), p, st)
	s.checkOnce(context.Background(), p, st)

	// 2. Полиморфинг в DME -> автоподмена на новый узел.
	r.hits = []Hit{{Endpoint: "188.114.96.9:2408", PingMs: 38, Colo: "HEL", Loc: "FI"}}
	pr.queue = []TraceResult{
		{Colo: "DME", Loc: "RU", Warp: "on", LatencyMs: 12},
		{Colo: "DME", Loc: "RU", Warp: "on", LatencyMs: 12},
		{Colo: "HEL", Loc: "FI", Warp: "on", LatencyMs: 36},
	}
	s.checkOnce(context.Background(), p, st)

	snap := s.State()
	if len(snap.Stats) != 1 {
		t.Fatalf("ожидалась статистика для 1 профиля, получили %d", len(snap.Stats))
	}
	ps := snap.Stats[0]
	if ps.ChecksTotal != 3 || ps.ChecksOK != 2 || ps.ChecksFail != 1 {
		t.Fatalf("счётчики проверок: total=%d ok=%d fail=%d", ps.ChecksTotal, ps.ChecksOK, ps.ChecksFail)
	}
	if ps.SwapsOK != 1 || ps.SwapsFail != 0 {
		t.Fatalf("счётчики подмен: ok=%d fail=%d", ps.SwapsOK, ps.SwapsFail)
	}
	if ps.MinLatencyMs != 30 || ps.MaxLatencyMs != 50 || ps.AvgLatencyMs != 40 {
		t.Fatalf("статистика пинга: min=%d avg=%d max=%d", ps.MinLatencyMs, ps.AvgLatencyMs, ps.MaxLatencyMs)
	}
	if ps.Colos["HEL"] != 1 || ps.Colos["ARN"] != 1 {
		t.Fatalf("распределение колокейшнов: %+v", ps.Colos)
	}
	if len(ps.Samples) != 3 || !ps.Samples[2].Swapped {
		t.Fatalf("серия точек проверок: %+v", ps.Samples)
	}
	if ps.CurrentEndpoint != "188.114.96.9:2408" || ps.EndpointSince.IsZero() {
		t.Fatalf("текущий endpoint не обновился: ep=%q since=%v", ps.CurrentEndpoint, ps.EndpointSince)
	}

	// 3. Проверяем, что агрегаты сохранились на диск (без Samples).
	diskStats := s.d.Store.Stats()
	if diskStats[p.ID].ChecksTotal != 3 || diskStats[p.ID].SwapsOK != 1 || len(diskStats[p.ID].Samples) != 0 {
		t.Fatalf("сохранённая статистика на диске: %+v", diskStats[p.ID])
	}

	// 4. Сброс статистики обнуляет счётчики, но оставляет текущий endpoint.
	if err := s.ResetStats(p.ID); err != nil {
		t.Fatal(err)
	}
	after := s.State().Stats[0]
	if after.ChecksTotal != 0 || after.SwapsOK != 0 || len(after.Samples) != 0 {
		t.Fatalf("после ResetStats ожидались нули: %+v", after)
	}
	if after.CurrentEndpoint != "188.114.96.9:2408" {
		t.Fatalf("после ResetStats должен сохраниться текущий endpoint: %q", after.CurrentEndpoint)
	}
}

func TestLocalCipher_RoundTripAndWrongKey(t *testing.T) {
	dir1 := t.TempDir()
	c1 := NewLocalCipher(dir1)
	st1, err := NewStore(dir1, c1)
	if err != nil {
		t.Fatal(err)
	}
	priv, pub, _ := GenerateKeyPair()
	if err := st1.SetAccount(&Account{
		ID: "dev-cipher", Token: "tok-secret", PrivateKey: priv, PublicKey: pub,
		PeerPublicKey: WarpPeerPublicKey, AddressV4: "172.16.0.2",
	}); err != nil {
		t.Fatal(err)
	}

	// Повторная инициализация в той же директории использует сохранённый .warpscout-key.
	c1Reload := NewLocalCipher(dir1)
	st1Reload, err := NewStore(dir1, c1Reload)
	if err != nil {
		t.Fatal(err)
	}
	if got := st1Reload.Account(); got == nil || got.Token != "tok-secret" || got.PrivateKey != priv {
		t.Fatalf("после перезагрузки ожидался расшифрованный аккаунт, получили %+v", got)
	}

	// Другой ключ устройства (например, после переноса бэкапа на другой роутер) помечает аккаунт нечитаемым.
	cOther := NewLocalCipher(t.TempDir())
	stOther, err := NewStore(dir1, cOther)
	if err != nil {
		t.Fatal(err)
	}
	if stOther.Account() != nil || !stOther.AccountUnreadable() {
		t.Fatalf("при чужом ключе ожидался AccountUnreadable=true, got account=%+v unreadable=%v", stOther.Account(), stOther.AccountUnreadable())
	}
}
