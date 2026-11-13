package warpscout

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	mu          sync.Mutex
	refs        map[string]*TunnelRef
	swaps       []string
	renames     []string
	created     []string
	createdOpts []CreateOptions
	getErr      error
	onSwap      func(r *TunnelRef)
	onCreate    func(r *TunnelRef)
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

func (f *fakeTunnels) Create(_ context.Context, m TargetMode, name, conf string, opts CreateOptions) (*TunnelRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "new" + string(rune('0'+len(f.created)))
	f.created = append(f.created, conf)
	f.createdOpts = append(f.createdOpts, opts)
	r := &TunnelRef{Mode: m, ID: id, Name: name, Iface: "nwg9", IsWarp: true, Running: true}
	f.refs[key(m, id)] = r
	if f.onCreate != nil {
		f.onCreate(r)
	}
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

func (f *fakeTunnels) Rename(_ context.Context, m TargetMode, id, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.refs[key(m, id)]
	if !ok {
		return errors.New("нет")
	}
	r.Name = name
	f.renames = append(f.renames, name)
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
	cfg := s.d.Store.Config()
	cfg.Profiles = []Profile{p}
	cfg.ActiveProfileID = p.ID
	if err := s.d.Store.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st := &watcherState{}
	s.checkOnce(context.Background(), p, st)

	if len(tun.swaps) != 1 || tun.swaps[0] != "188.114.96.9:2408" {
		t.Fatalf("ожидалась подмена на самый быстрый узел, swaps=%v", tun.swaps)
	}
	if len(tun.renames) != 1 || tun.renames[0] != "WARP HEL" {
		t.Fatalf("туннель должен переименоваться под новый colo HEL, renames=%v", tun.renames)
	}
	call := r.calls[0]
	if len(call.ExcludeEndpoints) != 1 || call.ExcludeEndpoints[0] != "162.159.192.1:2408" {
		t.Fatalf("скан должен исключать текущий endpoint: %v", call.ExcludeEndpoints)
	}
	if call.Ifname != "eth3" || call.MaxHits != swapScanMaxHits || call.Reserved != [3]int{0, 0, 0} {
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
		if d := time.Until(st.nextSwap); d < 50*time.Second {
			t.Fatalf("после провала проверки ожидалась пауза ≥1 мин, получили %s", d)
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
		if d := time.Until(st.nextSwap); d < 50*time.Second {
			t.Fatalf("после ошибки Get ожидалась пауза ≥1 мин, получили %s", d)
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

// Сканер находит узлы, а контрольная проверка после подмены стабильно не
// проходит: без паузы автопилот сканировал бы и переписывал endpoint на
// каждом круге. Пауза растёт по backoffSteps и сбрасывается успешной проверкой.
func TestWatchdog_PostSwapVerifyFailsBacksOff(t *testing.T) {
	s, r, tun, pr := newTestService(t)
	r.hits = []Hit{{Endpoint: "188.114.96.9:2408", PingMs: 35, Colo: "HEL", Loc: "FI"}}

	p := testProfile()
	p.Filters.MaxRTTMs = 50
	p.FailThreshold = 1
	st := &watcherState{}

	// И до, и после подмены реальная задержка в туннеле 500 мс > 50 мс.
	pr.def = TraceResult{Colo: "HEL", Loc: "FI", Warp: "on", LatencyMs: 500}
	s.checkOnce(context.Background(), p, st)
	if len(tun.swaps) != 1 {
		t.Fatalf("ожидалась подмена, swaps=%v", tun.swaps)
	}
	h := s.d.Store.History()
	if len(h) != 1 || h[0].OK || !strings.Contains(h[0].Error, "500") {
		t.Fatalf("превышение RTT после подмены должно провалить проверку: %+v", h)
	}
	if d := time.Until(st.nextSwap); d < 50*time.Second || d > 70*time.Second {
		t.Fatalf("после первого провала ожидалась пауза 1 мин, получили %s", d)
	}

	// Пока пауза не истекла — ни скана, ни подмены.
	s.checkOnce(context.Background(), p, st)
	if len(r.calls) != 1 || len(tun.swaps) != 1 {
		t.Fatalf("скан во время паузы: calls=%d swaps=%v", len(r.calls), tun.swaps)
	}

	// Пауза истекла, проверка снова не проходит — следующая ступень.
	st.nextSwap = time.Time{}
	s.checkOnce(context.Background(), p, st)
	if len(tun.swaps) != 2 {
		t.Fatalf("после паузы ожидалась вторая подмена, swaps=%v", tun.swaps)
	}
	if d := time.Until(st.nextSwap); d < 110*time.Second {
		t.Fatalf("пауза должна расти (2 мин), получили %s", d)
	}

	// Успешная проверка сбрасывает ступень.
	pr.def = TraceResult{Colo: "HEL", Loc: "FI", Warp: "on", LatencyMs: 30}
	s.checkOnce(context.Background(), p, st)
	if st.backoffIx != 0 {
		t.Fatalf("успешная проверка должна сбросить backoff, backoffIx=%d", st.backoffIx)
	}
}

// Отказ самой подмены — та же растущая пауза.
func TestWatchdog_SwapErrorBacksOff(t *testing.T) {
	s, r, tun, pr := newTestService(t)
	// Узел, который сканер вернул не адресом, до туннеля не доходит.
	r.hits = []Hit{{Endpoint: "engage.example:2408\nJc = 1", PingMs: 35}}
	pr.err = errors.New("timeout")
	p := testProfile()
	p.FailThreshold = 1
	st := &watcherState{}
	s.checkOnce(context.Background(), p, st)
	first := time.Until(st.nextSwap)
	st.nextSwap = time.Time{}
	s.checkOnce(context.Background(), p, st)
	if len(tun.swaps) != 0 {
		t.Fatalf("некорректный endpoint дошёл до подмены: %v", tun.swaps)
	}
	if second := time.Until(st.nextSwap); first < 50*time.Second || second < 110*time.Second {
		t.Fatalf("пауза после отказа подмены должна расти: %s, затем %s", first, second)
	}
}

// Проверки без подмены не пишут warpscout.json: счётчики живут в памяти и
// уходят на диск на остановке — и только если есть что писать.
func TestStats_ChecksDoNotWriteFile(t *testing.T) {
	s, _, _, _ := newTestService(t)
	p := testProfile()
	path := s.d.Store.path
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	st := &watcherState{}
	for i := 0; i < 20; i++ {
		s.checkOnce(context.Background(), p, st)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("проверки без подмены записали файл (err=%v)", err)
	}
	s.Stop()
	if got := s.d.Store.Stats()[p.ID].ChecksTotal; got != 20 {
		t.Fatalf("остановка должна сохранить накопленное: ChecksTotal=%d", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("остановка с несохранёнными изменениями не записала файл: %v", err)
	}
	// Повторная остановка без изменений файл не трогает.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("остановка без изменений записала файл (err=%v)", err)
	}
}

// Восстановление из резервной копии заменило файл: ни сторож, ни остановка
// не имеют права лечь поверх него состоянием из памяти.
func TestStore_FrozenRefusesWrites(t *testing.T) {
	s, _, _, _ := newTestService(t)
	p := testProfile()
	s.checkOnce(context.Background(), p, &watcherState{})

	restored := []byte(`{"schemaVersion":1,"config":{"profileMode":"multi","profiles":[]}}`)
	if err := os.WriteFile(s.d.Store.path, restored, 0o600); err != nil {
		t.Fatal(err)
	}
	s.d.Store.Frozen = func() bool { return true }

	if err := s.d.Store.SetConfig(DefaultConfig()); !errors.Is(err, ErrFrozen) {
		t.Fatalf("запись при восстановлении должна отклоняться, err=%v", err)
	}
	_ = s.d.Store.AppendHistory(SwapEvent{ProfileID: p.ID})
	_ = s.ResetStats("")
	s.Stop()
	got, err := os.ReadFile(s.d.Store.path)
	if err != nil || string(got) != string(restored) {
		t.Fatalf("восстановленный файл затёрт: %s (%v)", got, err)
	}
}

// Регистрация и привязка лицензии не идут одновременно: иначе SetLicense
// записал бы свою устаревшую копию учётки поверх перевыпущенного ключа, и
// локальный ключ разошёлся бы с ключом у Cloudflare.
func TestAccountOps_Serialized(t *testing.T) {
	var (
		mu      sync.Mutex
		lastKey string
	)
	putSeen := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPatch:
			var p map[string]string
			_ = json.Unmarshal(body, &p)
			mu.Lock()
			lastKey = p["key"]
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"a1","warp_enabled":true}`))
		case http.MethodPut:
			close(putSeen)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{"account_type":"limited","warp_plus":true,"quota":1}`))
		}
	}))
	defer srv.Close()
	origBase := cfAPIBase
	cfAPIBase = srv.URL
	t.Cleanup(func() { cfAPIBase = origBase })

	s, _, _, _ := newTestService(t)
	log := &captureLog{}
	s.d.Log = log
	s.d.HTTP = func(string) (*http.Client, error) { return srv.Client(), nil }

	licDone := make(chan error, 1)
	go func() {
		_, err := s.SetLicense(context.Background(), "lic")
		licDone <- err
	}()
	<-putSeen
	regDone := make(chan error, 1)
	go func() {
		_, err := s.Register(context.Background())
		regDone <- err
	}()
	// Привязка лицензии висит в сети. Без замка регистрация успевает пройти
	// целиком; с замком она ждёт — отпускаем привязку по таймауту.
	var regErr error
	select {
	case regErr = <-regDone:
		close(release)
	case <-time.After(200 * time.Millisecond):
		close(release)
		regErr = <-regDone
	}
	if err := <-licDone; err != nil || regErr != nil {
		t.Fatalf("SetLicense: %v, Register: %v", err, regErr)
	}
	acct := s.d.Store.Account()
	mu.Lock()
	defer mu.Unlock()
	if acct.PublicKey != lastKey {
		t.Fatalf("локальный ключ %q разошёлся с ключом у Cloudflare %q", acct.PublicKey, lastKey)
	}
	if pub, err := PublicKeyOf(acct.PrivateKey); err != nil || pub != lastKey {
		t.Fatalf("приватный ключ не соответствует ключу у Cloudflare: %q (%v)", pub, err)
	}
	if acct.License != "lic" {
		t.Fatalf("лицензия потеряна: %+v", acct)
	}
	for _, m := range log.msgs {
		if strings.Contains(m, acct.ID) {
			t.Fatalf("идентификатор учётной записи попал в журнал: %q", m)
		}
	}
}

type captureLog struct {
	mu   sync.Mutex
	msgs []string
}

func (l *captureLog) Info(_, _, msg string) { l.add(msg) }
func (l *captureLog) Warn(_, _, msg string) { l.add(msg) }
func (l *captureLog) add(msg string) {
	l.mu.Lock()
	l.msgs = append(l.msgs, msg)
	l.mu.Unlock()
}

func TestValidEndpoint(t *testing.T) {
	for _, ep := range []string{"1.1.1.1:2408", "162.159.192.1:1", "[2606:4700:d0::a29f:c001]:65535"} {
		if err := validEndpoint(ep); err != nil {
			t.Fatalf("%q должен проходить: %v", ep, err)
		}
	}
	for _, ep := range []string{
		"", "1.1.1.1", "1.1.1.1:", ":2408", "1.1.1.1:0", "1.1.1.1:65536", "1.1.1.1:+5", "1.1.1.1:2408\nJc = 1",
		"1.1.1.1\n:2408", " 1.1.1.1:2408", "engage.cloudflareclient.com:2408", "[fe80::1%eth0]:2408",
		"[fe80::1%a\nb]:2408", "1.1.1.1:2408\x00", "2606:4700::1:2408",
	} {
		if err := validEndpoint(ep); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%q должен отвергаться, err=%v", ep, err)
		}
	}
}

func TestInputValidation(t *testing.T) {
	s, r, tun, _ := newTestService(t)
	ctx := context.Background()

	// I1..I5 ложатся строками в .conf — перевод строки отвергается.
	for _, bad := range []string{"<b 0x01>\nPostUp = reboot", "<b 0x01>\r", "<b 0x01>\x00"} {
		cfg := Config{ProfileMode: ProfileModeMulti, AWG: AWGParams{SignatureProfile: "dns", I1: "<b 0x01>", I3: bad}}
		if _, err := s.SaveConfig(ctx, cfg); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("I3=%q должен отвергаться, err=%v", bad, err)
		}
	}

	// Endpoint создаваемого туннеля и подмены.
	for _, action := range []string{"create", "swap"} {
		_, err := s.Apply(ctx, ApplyRequest{Action: action, Endpoint: "1.1.1.1:2408\nJc = 1", Mode: TargetNative, TargetID: "awg10"})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s: endpoint с переводом строки должен отвергаться, err=%v", action, err)
		}
	}
	if len(tun.created) != 0 || len(tun.swaps) != 0 {
		t.Fatalf("некорректный endpoint дошёл до туннеля: created=%v swaps=%v", tun.created, tun.swaps)
	}

	// Список endpoint'ов ручного скана: строгий разбор и потолок длины.
	if err := s.StartScan(ScanRequest{Endpoints: []string{"1.1.1.1:2408", "host:1"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("имя хоста в endpoints должно отвергаться, err=%v", err)
	}
	many := make([]string, maxScanCandidates+1)
	for i := range many {
		many[i] = "1.1.1.1:2408"
	}
	if err := s.StartScan(ScanRequest{Endpoints: many}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("слишком длинный список endpoints должен отвергаться, err=%v", err)
	}
	if len(r.calls) != 0 {
		t.Fatal("скан запущен с некорректным входом")
	}

	// Интервалы: значение ~1e11 с переполняло time.Duration.
	p := normalizeProfile(Profile{CheckIntervalSec: 100_000_000_000, CooldownSec: 100_000_000_000})
	if p.CheckIntervalSec != 86400 || p.CooldownSec != 86400 {
		t.Fatalf("потолок интервалов: check=%d cooldown=%d", p.CheckIntervalSec, p.CooldownSec)
	}
	ports := make([]int, 1000)
	for i := range ports {
		ports[i] = i + 1
	}
	if f := normalizeFilters(Filters{Ports: ports}); len(f.Ports) != maxFilterPorts {
		t.Fatalf("потолок списка портов: %d", len(f.Ports))
	}
}

// hangRunner — сканер, который не завершается сам.
type hangRunner struct{}

func (hangRunner) Available() (string, bool) { return "/fake", true }
func (hangRunner) Run(ctx context.Context, _ ScanParams, _ func(EngineEvent)) error {
	<-ctx.Done()
	return nil
}

// Зависший процесс сканера снимается по дедлайну и освобождает движок.
func TestManualScan_DeadlineFreesEngine(t *testing.T) {
	s, _, _, _ := newTestService(t)
	s.d.Runner = hangRunner{}
	origD, origG := scanDeadline, scanKillGrace
	scanDeadline, scanKillGrace = 20*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { scanDeadline, scanKillGrace = origD, origG })

	if err := s.StartScan(ScanRequest{Filters: Filters{Protocol: ProtocolWG}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.ScanSnapshot().Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	sc := s.ScanSnapshot()
	if sc.Running || sc.Error == "" {
		t.Fatalf("зависший скан должен завершиться ошибкой по дедлайну: %+v", sc)
	}
	if !s.engineMu.TryLock() {
		t.Fatal("движок остался занят после дедлайна")
	}
	s.engineMu.Unlock()
}

// Строка stdout длиннее 64 КБ не обрывает разбор событий; строка длиннее
// потолка — ошибка, а не молчаливо пустой скан.
func TestBinaryRunner_LongLines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("нужен sh")
	}
	dir := t.TempDir()
	script := func(name string, n int) string {
		p := filepath.Join(dir, name)
		body := fmt.Sprintf("#!/bin/sh\ncat >/dev/null\nhead -c %d /dev/zero | tr '\\0' 'a'\necho\necho '{\"type\":\"hit\",\"endpoint\":\"1.1.1.1:2408\"}'\n", n)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var hits int
	count := func(e EngineEvent) {
		if e.Type == "hit" {
			hits++
		}
	}
	r := &BinaryRunner{Path: script("long.sh", 100_000)}
	if err := r.Run(context.Background(), ScanParams{}, count); err != nil || hits != 1 {
		t.Fatalf("событие после длинной строки потеряно: hits=%d err=%v", hits, err)
	}
	hits = 0
	r = &BinaryRunner{Path: script("huge.sh", maxEngineLine+1)}
	if err := r.Run(context.Background(), ScanParams{}, count); err == nil {
		t.Fatalf("строка длиннее потолка должна давать ошибку (hits=%d)", hits)
	}
}

// Путь к бинарнику задаётся только полем: окружение в проде не читается.
func TestBinaryRunner_IgnoresEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "evil")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWGM_WARPSCOUT_BIN", p)
	if got, _ := (&BinaryRunner{}).Available(); got == p {
		t.Fatal("путь к сканеру подменён переменной окружения")
	}
}

// Отменённый сторож не воскрешает состояние удалённого профиля, а сохранение
// конфигурации не перезапускает сторожей, чей профиль не менялся.
func TestReconcileWatchers(t *testing.T) {
	s, _, _, _ := newTestService(t)
	ctx := context.Background()
	mk := func(id, target string) Profile {
		return Profile{ID: id, Name: id, Enabled: true, TargetMode: TargetNative, TargetID: target}
	}
	s.d.Tunnels.(*fakeTunnels).refs[key(TargetNative, "awg11")] = &TunnelRef{Mode: TargetNative, ID: "awg11", IsWarp: true, Running: true}
	cfg := Config{ProfileMode: ProfileModeMulti, AWG: AWGParams{SignatureProfile: "none"},
		Profiles: []Profile{mk("p1", "awg10"), mk("p2", "awg11")}}
	if _, err := s.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	w1, w2 := s.watchers["p1"], s.watchers["p2"]
	s.mu.Unlock()
	if w1 == nil || w2 == nil {
		t.Fatal("сторожа не запущены")
	}

	// p2 меняется, p1 — нет.
	saved := s.d.Store.Config()
	saved.Profiles[1].FailThreshold = 5
	if _, err := s.SaveConfig(ctx, saved); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	n1, n2 := s.watchers["p1"], s.watchers["p2"]
	s.mu.Unlock()
	if n1 != w1 {
		t.Fatal("сторож неизменённого профиля перезапущен — cooldown и backoff сброшены")
	}
	if n2 == w2 || n2 == nil || n2.p.FailThreshold != 5 {
		t.Fatal("сторож изменённого профиля не перезапущен")
	}

	// p2 удалён: его отменённый сторож ещё доигрывает проверку.
	wctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.watchers["p2"].cancel = cancel
	s.mu.Unlock()
	saved.Profiles = saved.Profiles[:1]
	if _, err := s.SaveConfig(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if wctx.Err() == nil {
		t.Fatal("сторож удалённого профиля не отменён")
	}
	s.setWatch(wctx, "p2", func(ws *WatchStatus) { ws.State = WatchOK })
	s.recordCheckSample(wctx, "p2", time.Now(), "1.1.1.1:2408", nil, true, "")
	s.recordSwapResult(wctx, "p2", time.Now(), true, true, "1.1.1.1:2408", nil)
	s.mu.Lock()
	_, hasWatch := s.watch["p2"]
	_, hasStats := s.stats["p2"]
	s.mu.Unlock()
	if hasWatch || hasStats {
		t.Fatalf("отменённый сторож воскресил состояние удалённого профиля: watch=%v stats=%v", hasWatch, hasStats)
	}
}

// Туннель создан, а автопилот включить не удалось: это успех — ошибка привела
// бы к повтору и второму туннелю.
func TestApply_AutopilotFailureIsNotAnError(t *testing.T) {
	s, _, tun, _ := newTestService(t)
	tun.onCreate = func(r *TunnelRef) { r.IsWarp = false } // SaveConfig откажет: не WARP
	res, err := s.Apply(context.Background(), ApplyRequest{Action: "create", Endpoint: "188.114.96.9:2408",
		Mode: TargetKernel, Name: "WARP-FI", EnableAutopilot: true})
	if err != nil || res == nil || res.Tunnel == nil {
		t.Fatalf("созданный туннель должен быть успехом: res=%+v err=%v", res, err)
	}
	if res.ProfileID != "" || len(s.d.Store.Config().Profiles) != 0 {
		t.Fatalf("автопилот не включён — profileId должен быть пуст: %+v", res)
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

// Узел без замера пинга (0) не должен становиться «лучшим»: по первому
// элементу автопилот выбирает, куда переключаться.
func TestSortHits_UnmeasuredPingLast(t *testing.T) {
	hits := []Hit{{Endpoint: "a", PingMs: 0, HandshakeMs: 5}, {Endpoint: "b", PingMs: 40}, {Endpoint: "c", PingMs: 12}}
	sortHits(hits)
	if hits[0].Endpoint != "c" || hits[1].Endpoint != "b" || hits[2].Endpoint != "a" {
		t.Fatalf("порядок %v, want c b a", hits)
	}
}

func TestNormalizeTunnelIPv4(t *testing.T) {
	valid := []struct {
		in   string
		want string
	}{
		{"172.16.0.5", "172.16.0.5"},
		{" 172.16.0.5/32 ", "172.16.0.5"},
		{"10.66.66.2/24", "10.66.66.2/24"},
	}
	for _, tc := range valid {
		got, err := NormalizeTunnelIPv4(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("NormalizeTunnelIPv4(%q) = (%q, %v), хотели (%q, nil)", tc.in, got, err, tc.want)
		}
	}
	invalid := []string{"", "   ", "2606:4700::1", "127.0.0.1", "0.0.0.0", "169.254.1.1", "255.255.255.255", "224.0.0.1", "172.16.0.2/0", "172.16.0.999"}
	for _, in := range invalid {
		if got, err := NormalizeTunnelIPv4(in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("NormalizeTunnelIPv4(%q) = (%q, %v), хотели ErrInvalidInput", in, got, err)
		}
	}
}

func TestSetAccountAddress_PreservedAcrossRotationAndCreateOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "dev-rot",
			"warp_enabled": true,
			"config": {"client_id": "BAUG", "peers": [{"public_key": "` + WarpPeerPublicKey + `"}], "interface": {"addresses": {"v4": "172.16.0.2"}}}
		}`))
	}))
	defer srv.Close()

	origBase := cfAPIBase
	cfAPIBase = srv.URL
	t.Cleanup(func() { cfAPIBase = origBase })

	s, _, tun, _ := newTestService(t)
	s.d.HTTP = func(string) (*http.Client, error) { return srv.Client(), nil }

	// 1. Пользователь задаёт кастомный IPv4 в учётке.
	acct, err := s.SetAccountAddress("172.16.0.9/32")
	if err != nil || acct.AddressV4 != "172.16.0.9" {
		t.Fatalf("SetAccountAddress: acct=%+v err=%v", acct, err)
	}
	if _, err := s.SetAccountAddress("bad-ip"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ожидался ErrInvalidInput, получили %v", err)
	}

	// 2. При перевыпуске ключей кастомный IPv4 сохраняется (CF возвращает 172.16.0.2).
	rotated, err := s.Register(context.Background())
	if err != nil {
		t.Fatalf("Register (rotate): %v", err)
	}
	if rotated.AddressV4 != "172.16.0.9" {
		t.Fatalf("при ротации должен сохраниться кастомный IPv4 172.16.0.9, получили %q", rotated.AddressV4)
	}

	// 3. Apply(create) с явным addressV4 кладёт его в .conf, не ломая сохранённый в учётке.
	_, err = s.Apply(context.Background(), ApplyRequest{
		Action:    "create",
		Endpoint:  "162.159.192.1:2408",
		Mode:      TargetKernel,
		Name:      "WARP Custom",
		AddressV4: "172.16.0.11",
		Protocol:  ProtocolAWG,
	})
	if err != nil {
		t.Fatalf("Apply create: %v", err)
	}
	if len(tun.created) != 1 || !strings.Contains(tun.created[0], "Address = 172.16.0.11/32\n") {
		t.Fatalf("в конфиге ожидался Address = 172.16.0.11/32, получили:\n%v", tun.created)
	}
	if got := s.d.Store.Account().AddressV4; got != "172.16.0.9" {
		t.Fatalf("локальный addressV4 аккаунта не должен меняться разовым create override: %q", got)
	}
}

func TestDeriveWarpTunnelName(t *testing.T) {
	cases := []struct {
		cur  string
		colo string
		want string
		ok   bool
	}{
		{"WARP", "hel", "WARP HEL", true},
		{"WARP TLL", "HEL", "WARP HEL", true},
		{"WARP-WAW", "HEL", "WARP-HEL", true},
		{"WARP (TLL)", "FRA", "WARP (FRA)", true},
		{"WARP TLL 2", "ARN", "WARP ARN 2", true},
		{"Мой WARP TLL", "HEL", "Мой WARP HEL", true},
		{"WARP HEL", "HEL", "WARP HEL", false},
		{"WARP AWG", "HEL", "", false},
		{"WARP VPN", "HEL", "", false},
		{"WARP Native", "HEL", "", false},
		{"Мой туннель", "HEL", "", false},
		{"WARP TLL", "BAD1", "", false},
	}
	for _, tc := range cases {
		got, ok := DeriveWarpTunnelName(tc.cur, tc.colo)
		if ok != tc.ok || (tc.ok && got != tc.want) {
			t.Fatalf("DeriveWarpTunnelName(%q, %q) = (%q, %v), want (%q, %v)",
				tc.cur, tc.colo, got, ok, tc.want, tc.ok)
		}
	}
}

func TestApply_SwapUpdatesTunnelCardName(t *testing.T) {
	s, _, tun, _ := newTestService(t)
	tun.mu.Lock()
	tun.refs[key(TargetNative, "awg10")].Name = "WARP TLL"
	tun.mu.Unlock()

	cfg := s.d.Store.Config()
	cfg.Profiles = []Profile{{
		ID:         "p-custom",
		Name:       "Профиль 1",
		Enabled:    true,
		TargetMode: TargetNative,
		TargetID:   "awg10",
		Filters:    DefaultFilters(),
	}}
	cfg.ActiveProfileID = "p-custom"
	if err := s.d.Store.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}

	res, err := s.Apply(context.Background(), ApplyRequest{
		Action:   "swap",
		Endpoint: "162.159.192.43:903",
		Colo:     "HEL",
		Mode:     TargetNative,
		TargetID: "awg10",
	})
	if err != nil {
		t.Fatalf("Apply swap: %v", err)
	}
	if res.Tunnel == nil || res.Tunnel.Name != "WARP HEL" {
		t.Fatalf("ожидалось переименование карточки в WARP HEL, получили %+v", res.Tunnel)
	}
	if got := s.d.Store.Config().Profiles[0].Name; got != "Профиль 1" {
		t.Fatalf("пользовательское имя профиля «Профиль 1» не должно меняться, получили %q", got)
	}
}

func TestStore_InnerAccountRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir, plainCipher{})
	if err != nil {
		t.Fatal(err)
	}
	inner := &Account{
		ID:            "inner-1",
		Token:         "tok-in",
		PrivateKey:    "priv-in",
		PublicKey:     "pub-in",
		PeerPublicKey: WarpPeerPublicKey,
		AddressV4:     "172.16.0.3",
	}
	if err := st.SetInnerAccount(inner); err != nil {
		t.Fatalf("SetInnerAccount: %v", err)
	}
	reloaded, err := NewStore(dir, plainCipher{})
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.InnerAccount()
	if got == nil || got.ID != "inner-1" || got.PrivateKey != "priv-in" || got.AddressV4 != "172.16.0.3" {
		t.Fatalf("перезагруженный innerAccount не совпал: %+v", got)
	}
	if err := reloaded.SetInnerAccount(nil); err != nil {
		t.Fatal(err)
	}
	if reloaded.InnerAccount() != nil {
		t.Fatalf("после удаления InnerAccount должен быть nil")
	}
}

func TestDeriveWarpTunnelName_WarpInWarp(t *testing.T) {
	if got, ok := DeriveWarpTunnelName("WARP-in-WARP", "fra"); !ok || got != "WARP-in-WARP FRA" {
		t.Fatalf("WARP-in-WARP -> %q, %v", got, ok)
	}
	if got, ok := DeriveWarpTunnelName("WARP-in-WARP HEL", "ams"); !ok || got != "WARP-in-WARP AMS" {
		t.Fatalf("WARP-in-WARP HEL -> %q, %v", got, ok)
	}
}

func TestSaveConfig_PreservesCustomSignature(t *testing.T) {
	s, _, _, _ := newTestService(t)
	cfg := s.d.Store.Config()
	cfg.AWG = AWGParams{
		SignatureProfile: SignatureCustom,
		Jc:               5,
		Jmin:             10,
		Jmax:             80,
		I1:               "<b 0xdeadbeef><r 8>",
		I2:               "<t>",
	}
	saved, err := s.SaveConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SaveConfig custom: %v", err)
	}
	if saved.AWG.SignatureProfile != SignatureCustom || saved.AWG.I1 != "<b 0xdeadbeef><r 8>" || saved.AWG.I2 != "<t>" {
		t.Fatalf("I1/I2 должны сохраниться без перезаписи: %+v", saved.AWG)
	}

	cfg.AWG.I1 = "<b invalidhex>"
	if _, err := s.SaveConfig(context.Background(), cfg); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ожидался ErrInvalidInput для битой CPS I1, получили %v", err)
	}
}

func TestStartScan_ViaWarpTunnel_UsesInnerAccountAndConcurrency1(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id": "dev-inner",
			"token": "tok-inner",
			"warp_enabled": true,
			"config": {"client_id": "AQID", "peers": [{"public_key": "` + WarpPeerPublicKey + `"}], "interface": {"addresses": {"v4": "172.16.0.2"}}}
		}`))
	}))
	defer srv.Close()
	origBase := cfAPIBase
	cfAPIBase = srv.URL
	t.Cleanup(func() { cfAPIBase = origBase })

	s, runner, tun, _ := newTestService(t)
	s.d.HTTP = func(string) (*http.Client, error) { return srv.Client(), nil }
	tun.mu.Lock()
	tun.refs[key(TargetNative, "awg10")] = &TunnelRef{
		Mode:           TargetNative,
		ID:             "awg10",
		Name:           "WARP HEL",
		Iface:          "nwg1",
		Endpoint:       "162.159.192.255:880",
		AddressV4:      "172.16.0.10",
		LocalPublicKey: "pub", // совпадает с основным аккаунтом — должен создаться InnerAccount
		PeerPublicKey:  WarpPeerPublicKey,
		IsWarp:         true,
		Running:        true,
	}
	tun.mu.Unlock()
	runner.hits = []Hit{{Endpoint: "162.159.193.1:2408", PingMs: 45, Colo: "FRA", Loc: "DE", Warp: "on"}}

	if err := s.StartScan(ScanRequest{Filters: Filters{Protocol: ProtocolWG}, Ifname: "nwg1", MaxHits: 5}); err != nil {
		t.Fatalf("StartScan: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !s.ScanSnapshot().Running && s.ScanSnapshot().Done > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := s.State()
	if st.Scan.Running || st.Scan.Error != "" {
		t.Fatalf("ожидался успешный скан через WARP, got %+v", st.Scan)
	}
	if !st.HasInnerAccount {
		t.Fatalf("ожидалось появление InnerAccount при скане через WARP")
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 {
		t.Fatalf("ожидался 1 запуск runner, получили %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.Concurrency != 1 {
		t.Fatalf("ожидалась Concurrency=1 внутри WARP, получили %d", call.Concurrency)
	}
	if call.PrivateKey == st.Account.ID || call.PrivateKey == "cHJpdg==" {
		t.Fatalf("внутренний скан не должен использовать ключ внешнего туннеля")
	}
	if len(call.ExcludeEndpoints) == 0 || call.ExcludeEndpoints[0] != "162.159.192.255" {
		t.Fatalf("в ExcludeEndpoints должен попасть IP внешнего WARP, получили %v", call.ExcludeEndpoints)
	}
	if call.TunnelIP == "172.16.0.10" {
		t.Fatalf("TunnelIP не должен пересекаться с внешним туннелем")
	}
}

func TestApplyCreate_WarpInWarp_MTU1220AndMultiAutopilot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id": "dev-inner-2",
			"token": "tok-inner-2",
			"warp_enabled": true,
			"config": {"client_id": "AQID", "peers": [{"public_key": "` + WarpPeerPublicKey + `"}], "interface": {"addresses": {"v4": "172.16.0.2"}}}
		}`))
	}))
	defer srv.Close()
	origBase := cfAPIBase
	cfAPIBase = srv.URL
	t.Cleanup(func() { cfAPIBase = origBase })

	s, _, tun, _ := newTestService(t)
	s.d.HTTP = func(string) (*http.Client, error) { return srv.Client(), nil }
	tun.mu.Lock()
	tun.refs[key(TargetNative, "awg50")] = &TunnelRef{
		Mode:           TargetNative,
		ID:             "awg50",
		Name:           "WARP HEL",
		Iface:          "nwg3",
		Endpoint:       "162.159.192.255:880",
		AddressV4:      "172.16.0.10",
		LocalPublicKey: "pub",
		PeerPublicKey:  WarpPeerPublicKey,
		IsWarp:         true,
		Running:        true,
	}
	tun.mu.Unlock()

	cfg := s.d.Store.Config()
	cfg.ProfileMode = ProfileModeSingle
	cfg.Profiles = []Profile{{
		ID: "p-outer", Name: "WARP HEL", Enabled: true, TargetMode: TargetNative, TargetID: "awg50", Filters: DefaultFilters(),
	}}
	cfg.ActiveProfileID = "p-outer"
	if err := s.d.Store.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}

	// Kernel AWG должен быть запрещён для WARP-in-WARP
	if _, err := s.Apply(context.Background(), ApplyRequest{
		Action: "create", Endpoint: "162.159.193.1:2408", Mode: TargetKernel, ViaMode: TargetNative, ViaID: "awg50",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("TargetKernel для WARP-in-WARP должен отвергаться ErrInvalidInput, got %v", err)
	}
	// Native WG с plain WireGuard тоже запрещён (Native WG требует параметры AWG)
	if _, err := s.Apply(context.Background(), ApplyRequest{
		Action: "create", Endpoint: "162.159.193.1:2408", Mode: TargetNative, Protocol: ProtocolWG, ViaMode: TargetNative, ViaID: "awg50",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("TargetNative + ProtocolWG должен отвергаться ErrInvalidInput, got %v", err)
	}

	res, err := s.Apply(context.Background(), ApplyRequest{
		Action:          "create",
		Endpoint:        "162.159.193.1:2408",
		Colo:            "FRA",
		Mode:            TargetNative,
		Name:            "WARP-in-WARP FRA",
		Protocol:        ProtocolAWG,
		ViaMode:         TargetNative,
		ViaID:           "awg50",
		EnableAutopilot: true,
	})
	if err != nil {
		t.Fatalf("Apply WARP-in-WARP create: %v", err)
	}
	if res.Tunnel == nil || len(tun.created) != 1 {
		t.Fatalf("ожидалось создание туннеля, got %+v", res)
	}
	conf := tun.created[0]
	if !strings.Contains(conf, "MTU = 1220\n") {
		t.Fatalf("для WARP-in-WARP ожидался MTU = 1220, конфиг:\n%s", conf)
	}
	if strings.Contains(conf, "PrivateKey = cHJpdg==\n") {
		t.Fatalf("WARP-in-WARP не должен переиспользовать приватный ключ внешнего туннеля")
	}
	if tun.createdOpts[0].ViaMode != TargetNative || tun.createdOpts[0].ViaID != "awg50" {
		t.Fatalf("ожидались CreateOptions{native, awg50}, got %+v", tun.createdOpts[0])
	}
	afterCfg := s.d.Store.Config()
	if afterCfg.ProfileMode != ProfileModeMulti {
		t.Fatalf("включение автопилота на WARP-in-WARP должно переключать в multi-режим, не выключая внешний профиль")
	}
	if len(afterCfg.Profiles) != 2 || !afterCfg.Profiles[0].Enabled || !afterCfg.Profiles[1].Enabled {
		t.Fatalf("оба профиля автопилота должны остаться активными: %+v", afterCfg.Profiles)
	}

	// Системный + ProtocolWG для WARP-in-WARP разрешён и генерирует plain WG с MTU 1220
	if _, err := s.Apply(context.Background(), ApplyRequest{
		Action:   "create",
		Endpoint: "162.159.193.2:2408",
		Mode:     TargetSystem,
		Protocol: ProtocolWG,
		ViaMode:  TargetNative,
		ViaID:    "awg50",
	}); err != nil {
		t.Fatalf("TargetSystem + ProtocolWG для WARP-in-WARP: %v", err)
	}
	sysConf := tun.created[1]
	if !strings.Contains(sysConf, "MTU = 1220\n") || strings.Contains(sysConf, "Jc =") {
		t.Fatalf("ожидался plain WG конфиг с MTU 1220 без Jc, got:\n%s", sysConf)
	}
}

func TestWatchdog_WarpInWarp_WaitsForOuterAndScansViaOuter(t *testing.T) {
	s, runner, tun, prober := newTestService(t)
	if err := s.d.Store.SetInnerAccount(&Account{
		ID: "in-1", Token: "t", PrivateKey: "aW5uZXItcHJpdi1rZXk=", PublicKey: "pub-inner",
		PeerPublicKey: WarpPeerPublicKey, AddressV4: "172.16.0.3",
	}); err != nil {
		t.Fatal(err)
	}
	tun.mu.Lock()
	tun.refs[key(TargetNative, "awg60")] = &TunnelRef{
		Mode: TargetNative, ID: "awg60", Name: "WARP-in-WARP FRA", Iface: "nwg4",
		Endpoint: "162.159.193.1:2408", AddressV4: "172.16.0.3",
		LocalPublicKey: "pub-inner", PeerPublicKey: WarpPeerPublicKey,
		IsWarp: true, Running: true,
		ViaMode: TargetNative, ViaTunnelID: "awg50", ViaIface: "nwg3",
		ViaLabel: "WARP HEL", ViaEndpoint: "162.159.192.255:880", ViaRunning: false,
	}
	tun.mu.Unlock()

	p := Profile{
		ID: "p-in", Name: "WARP-in-WARP FRA", Enabled: true,
		TargetMode: TargetNative, TargetID: "awg60",
		Filters: DefaultFilters(), FailThreshold: 1, CooldownSec: 10,
	}
	st := &watcherState{}
	s.checkOnce(context.Background(), p, st)
	s.mu.Lock()
	wsDirect := *s.watch["p-in"]
	s.mu.Unlock()
	if wsDirect.State != WatchTargetStopped || !strings.Contains(wsDirect.Message, "внешний туннель") {
		t.Fatalf("при выключенном внешнем WARP сторож должен ждать, got %+v", wsDirect)
	}

	// Включаем внешний туннель, эмулируем отказ внутреннего -> swap должен сканить через nwg3 и ключом InnerAccount.
	tun.mu.Lock()
	tun.refs[key(TargetNative, "awg60")].ViaRunning = true
	tun.mu.Unlock()
	prober.mu.Lock()
	prober.queue = []TraceResult{
		{Colo: "DME", Loc: "RU", Warp: "on"},
		{Colo: "DME", Loc: "RU", Warp: "on"},
		{Colo: "AMS", Loc: "NL", Warp: "on", LatencyMs: 42},
	}
	prober.mu.Unlock()
	runner.hits = []Hit{{Endpoint: "162.159.193.9:1002", PingMs: 42, Colo: "AMS", Loc: "NL", Warp: "on"}}

	s.checkOnce(context.Background(), p, st)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 {
		t.Fatalf("ожидался 1 запуск сканера при подмене, получили %d", len(runner.calls))
	}
	if runner.calls[0].Ifname != "nwg3" || runner.calls[0].Concurrency != 1 {
		t.Fatalf("ожидался скан через внешний интерфейс nwg3 (Concurrency=1), получили %+v", runner.calls[0])
	}
	if runner.calls[0].PrivateKey != "aW5uZXItcHJpdi1rZXk=" {
		t.Fatalf("ожидался ключ InnerAccount, получили %q", runner.calls[0].PrivateKey)
	}
}
