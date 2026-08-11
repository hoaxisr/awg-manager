package nwg

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// procStub is an in-memory /proc/awg_proxy/* stand-in. AddTunnel and
// RestoreTunnel write through procWriteFn / procReadFn, so tests swap in
// this stub and assert what was written, what came back in /proc/list,
// and how many del/add ops fired.
type procStub struct {
	mu       sync.Mutex
	listBody string
	writes   []procWrite
	// version overrides what /proc/awg_proxy/version reports (default
	// "1.1.10"). IPv6-endpoint tests raise it past kmodVersionIPv6.
	version string
	// failAdd lets tests force /proc/awg_proxy/add to return a specific
	// errno (e.g. EEXIST) on the next attempt and clears itself after.
	failAddOnce error
}

type procWrite struct {
	path string
	body string
}

func newProcStub() *procStub { return &procStub{} }

func (p *procStub) write(path string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if path == "/proc/awg_proxy/add" && p.failAddOnce != nil {
		err := p.failAddOnce
		p.failAddOnce = nil
		return err
	}
	p.writes = append(p.writes, procWrite{path: path, body: string(data)})

	// Simulate the kernel side-effects on /proc/awg_proxy/list so
	// subsequent reads (readListenPortLocked) see the slot the way real
	// kmod publishes it.
	switch path {
	case "/proc/awg_proxy/add":
		// Format the kmod uses: "IP:PORT listen=127.0.0.1:LPORT rx=... tx=..."
		head := strings.SplitN(string(data), " ", 2)[0] // "IP:PORT"
		listenPort := 49494 + len(p.writes)             // deterministic, distinct
		p.listBody += fmt.Sprintf("%s listen=127.0.0.1:%d rx=0 tx=0 rx_pkt=0 tx_pkt=0\n", head, listenPort)
	case "/proc/awg_proxy/del":
		ipPort := strings.TrimSpace(string(data)) // "IP:PORT"
		var keep []string
		for _, ln := range strings.Split(p.listBody, "\n") {
			if ln == "" {
				continue
			}
			if !strings.HasPrefix(ln, ipPort+" ") {
				keep = append(keep, ln)
			}
		}
		if len(keep) == 0 {
			p.listBody = ""
		} else {
			p.listBody = strings.Join(keep, "\n") + "\n"
		}
	}
	return nil
}

func (p *procStub) read(path string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch path {
	case "/proc/awg_proxy/list":
		return []byte(p.listBody), nil
	case "/proc/awg_proxy/version":
		if p.version != "" {
			return []byte(p.version + "\n"), nil
		}
		return []byte("1.1.10\n"), nil
	default:
		return nil, fmt.Errorf("unexpected read: %s", path)
	}
}

func (p *procStub) setListSlot(ip string, port, listenPort int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listBody = fmt.Sprintf("%s:%d listen=127.0.0.1:%d rx=0 tx=0 rx_pkt=0 tx_pkt=0\n", ip, port, listenPort)
}

func (p *procStub) countWritesTo(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, w := range p.writes {
		if w.path == path {
			n++
		}
	}
	return n
}

// newKmodManagerForTest wires up a KmodManager with the procStub swapped
// in and the in-memory tunnels map cleared. After-call assertions can
// inspect both km.tunnels and stub.writes.
func newKmodManagerForTest() (*KmodManager, *procStub) {
	stub := newProcStub()
	km := NewKmodManager(nil)
	km.procWriteFn = stub.write
	km.procReadFn = stub.read
	return km, stub
}

func defaultCfg() KmodConfig {
	return KmodConfig{
		EndpointIP:   "203.0.113.10",
		EndpointPort: 5060,
		H1:           "148", H2: "92", H3: "64", H4: "64",
		S1: 0, S2: 0, S3: 0, S4: 0,
		Jc: 0, Jmin: 0, Jmax: 0,
	}
}

// --- AddTunnel: never adopts ----------------------------------------------

func TestAddTunnel_RefusesAdoptEvenWhenSlotExists(t *testing.T) {
	km, stub := newKmodManagerForTest()
	cfg := defaultCfg()

	// A live slot for this endpoint is sitting in /proc/list; AddTunnel
	// must IGNORE it (because the caller is starting a fresh tunnel and
	// the slot's stored keys may belong to a deleted predecessor).
	stub.setListSlot(cfg.EndpointIP, cfg.EndpointPort, 49494)

	res, err := km.AddTunnel("tunnel-fresh", cfg)
	if err != nil {
		t.Fatalf("AddTunnel: %v", err)
	}
	if res.Adopted {
		t.Fatalf("AddTunnel must never report Adopted=true; got %#v", res)
	}
	if stub.countWritesTo("/proc/awg_proxy/add") != 1 {
		t.Fatalf("AddTunnel must write /proc/awg_proxy/add exactly once; got %d", stub.countWritesTo("/proc/awg_proxy/add"))
	}
}

func TestAddTunnel_DeleteThenCreateSameEndpoint_UsesNewKeys(t *testing.T) {
	// Critical #234 / C1 regression test.
	// Scenario: T1 (keys K1) → Delete → T2 with same endpoint but new
	// keys K2. The orphan slot from T1 still sits in /proc/list. Pre-fix
	// AddTunnel adopted that slot — and silently kept routing T2 traffic
	// through K1, with no log line. T2 then never finished a handshake.
	km, stub := newKmodManagerForTest()
	cfg := defaultCfg()
	cfg.PubServerHex = strings.Repeat("aa", 32) // new keys
	cfg.PubClientHex = strings.Repeat("bb", 32)

	// Pretend the kernel still has T1's slot for the same endpoint.
	stub.setListSlot(cfg.EndpointIP, cfg.EndpointPort, 60728)

	res, err := km.AddTunnel("tunnel-T2", cfg)
	if err != nil {
		t.Fatalf("AddTunnel: %v", err)
	}
	if res.Adopted {
		t.Fatalf("AddTunnel adopted an orphan slot — that's the #234 silent-corruption path; cfg keys would be ignored")
	}

	// The add line must carry the NEW keys (PUB_SERVER/PUB_CLIENT) so
	// the kernel uses them, not K1.
	var addBody string
	for _, w := range stub.writes {
		if w.path == "/proc/awg_proxy/add" {
			addBody = w.body
		}
	}
	if !strings.Contains(addBody, "PUB_SERVER="+cfg.PubServerHex) {
		t.Fatalf("/proc/awg_proxy/add line missing new PUB_SERVER; body=%q", addBody)
	}
	if !strings.Contains(addBody, "PUB_CLIENT="+cfg.PubClientHex) {
		t.Fatalf("/proc/awg_proxy/add line missing new PUB_CLIENT; body=%q", addBody)
	}
}

func TestAddTunnel_EEXISTFallbackDelThenAdd(t *testing.T) {
	km, stub := newKmodManagerForTest()
	cfg := defaultCfg()

	// First add attempt fails with EEXIST — kmod has a stale slot for
	// this endpoint. The fallback path must del and retry add.
	stub.failAddOnce = &errnoPathErr{op: "write", path: "/proc/awg_proxy/add", err: syscall.EEXIST}

	if _, err := km.AddTunnel("tunnel-eexist", cfg); err != nil {
		t.Fatalf("AddTunnel: %v", err)
	}

	if got := stub.countWritesTo("/proc/awg_proxy/del"); got != 1 {
		t.Fatalf("EEXIST fallback must write /proc/awg_proxy/del once; got %d", got)
	}
	if got := stub.countWritesTo("/proc/awg_proxy/add"); got != 1 {
		t.Fatalf("EEXIST fallback must succeed with one retry add (initial failed); got %d successful add writes", got)
	}
}

// --- RestoreTunnel: adopts when slot matches ------------------------------

func TestRestoreTunnel_AdoptsExistingSlot(t *testing.T) {
	km, stub := newKmodManagerForTest()
	cfg := defaultCfg()
	stub.setListSlot(cfg.EndpointIP, cfg.EndpointPort, 56034)

	res, err := km.RestoreTunnel("tunnel-reconnect", cfg)
	if err != nil {
		t.Fatalf("RestoreTunnel: %v", err)
	}
	if !res.Adopted {
		t.Fatalf("RestoreTunnel must adopt an existing slot; got Adopted=false")
	}
	if res.ListenPort != 56034 {
		t.Fatalf("adopted listen port should be 56034 (from /proc/list); got %d", res.ListenPort)
	}
	if got := stub.countWritesTo("/proc/awg_proxy/add"); got != 0 {
		t.Fatalf("RestoreTunnel adopt path must NOT write /proc/awg_proxy/add; got %d writes", got)
	}
	if got := stub.countWritesTo("/proc/awg_proxy/del"); got != 0 {
		t.Fatalf("RestoreTunnel adopt path must NOT write /proc/awg_proxy/del; got %d writes", got)
	}
}

func TestRestoreTunnel_AddsIfNoMatchingSlot(t *testing.T) {
	km, stub := newKmodManagerForTest()
	cfg := defaultCfg()
	// /proc/list is empty — no slot to adopt; fall through to add.

	res, err := km.RestoreTunnel("tunnel-no-slot", cfg)
	if err != nil {
		t.Fatalf("RestoreTunnel: %v", err)
	}
	if res.Adopted {
		t.Fatalf("RestoreTunnel should fall back to add when no slot exists; got Adopted=true")
	}
	if got := stub.countWritesTo("/proc/awg_proxy/add"); got != 1 {
		t.Fatalf("RestoreTunnel fallback must write /proc/awg_proxy/add once; got %d", got)
	}
}

func TestRestoreTunnel_DoubleRestoreIsIdempotent(t *testing.T) {
	// I-4: a redundant RestoreTunnel (e.g. flapping reconnect) must NOT
	// fall through to addFreshLocked, which would del the live slot we
	// just adopted and assign a new listen port — leaving NDMS peer
	// endpoint stale until the next sync.
	km, stub := newKmodManagerForTest()
	cfg := defaultCfg()
	stub.setListSlot(cfg.EndpointIP, cfg.EndpointPort, 56034)

	first, err := km.RestoreTunnel("tunnel-A", cfg)
	if err != nil {
		t.Fatalf("first RestoreTunnel: %v", err)
	}
	if !first.Adopted || first.ListenPort != 56034 {
		t.Fatalf("first restore: want Adopted=true, port=56034; got %+v", first)
	}

	second, err := km.RestoreTunnel("tunnel-A", cfg)
	if err != nil {
		t.Fatalf("second RestoreTunnel: %v", err)
	}
	if second.ListenPort != first.ListenPort {
		t.Fatalf("double-restore changed listen port (%d → %d) — would orphan NDMS peer endpoint", first.ListenPort, second.ListenPort)
	}
	if got := stub.countWritesTo("/proc/awg_proxy/add"); got != 0 {
		t.Fatalf("double-restore must not write /proc/awg_proxy/add; got %d", got)
	}
	if got := stub.countWritesTo("/proc/awg_proxy/del"); got != 0 {
		t.Fatalf("double-restore must not write /proc/awg_proxy/del; got %d", got)
	}
}

func TestRestoreTunnel_AdoptDiscardsCallerCfg(t *testing.T) {
	// I-6: documents the trust boundary. adopt-by-endpoint uses the LIVE
	// slot's parameters from kernel; caller-supplied cfg (keys / obfuscation)
	// is silently dropped. If the caller's stored config diverged from the
	// in-kernel slot, that mismatch survives across daemon-restart. Service-
	// level mitigation lives in applyDiffNWG → SyncKmodSlot for the Update
	// path; adopt itself is intentionally "trust the kernel state".
	km, stub := newKmodManagerForTest()
	cfg := defaultCfg()
	cfg.PubServerHex = strings.Repeat("aa", 32) // brand-new keys
	cfg.PubClientHex = strings.Repeat("bb", 32)
	stub.setListSlot(cfg.EndpointIP, cfg.EndpointPort, 49494) // pre-existing slot (old keys)

	res, err := km.RestoreTunnel("tunnel-mismatch", cfg)
	if err != nil {
		t.Fatalf("RestoreTunnel: %v", err)
	}
	if !res.Adopted {
		t.Fatalf("adopt path must fire on matching endpoint regardless of cfg drift; got Adopted=false")
	}
	// Crucially: no /proc/add write — the caller-supplied PUB_SERVER / PUB_CLIENT
	// never reach the kernel. This is the residual silent-failure window the
	// service layer is expected to close before calling RestoreTunnel.
	if got := stub.countWritesTo("/proc/awg_proxy/add"); got != 0 {
		t.Fatalf("adopt must NOT push caller cfg via /proc/add; got %d writes (#234 invariant breach)", got)
	}
}

// Смена адреса не должна оставлять слот прежнего адреса в ядре (#702).
func TestSyncKmodSlot_RemovesPreviousSlotOnAddressChange(t *testing.T) {
	km, stub := newKmodManagerForTest()

	cfg := defaultCfg()
	cfg.EndpointIP, cfg.EndpointPort = "198.51.100.1", 51820
	if _, err := km.AddTunnel("awg10", cfg); err != nil {
		t.Fatalf("первый add: %v", err)
	}

	// Адрес сменился — вызывающая сторона обязана снять прежний слот.
	if err := km.RemoveTunnel("awg10"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	cfg.EndpointIP = "203.0.113.9"
	if _, err := km.AddTunnel("awg10", cfg); err != nil {
		t.Fatalf("второй add: %v", err)
	}

	if strings.Contains(stub.listBody, "198.51.100.1:51820") {
		t.Fatalf("слот прежнего адреса остался в ядре:\n%s", stub.listBody)
	}
}

// --- errnoPathErr wraps a syscall errno so errors.Is(err, syscall.EEXIST)
// works the same way it does in real *os.PathError from os.WriteFile. ----

type errnoPathErr struct {
	op   string
	path string
	err  syscall.Errno
}

func (e *errnoPathErr) Error() string { return fmt.Sprintf("%s %s: %v", e.op, e.path, e.err) }
func (e *errnoPathErr) Unwrap() error { return e.err }

// compile-time check that errors.Is(unwrap) chain works for our stub.
var _ = func() bool {
	e := &errnoPathErr{err: syscall.EEXIST}
	return errors.Is(e, syscall.EEXIST)
}()

// Все 16 слотов awg_proxy заняты: ядро отвечает -ENOSPC на /proc/add, и
// ошибка старта обязана нести понятное русское пояснение про предел kmod
// (срабатывает при старте туннеля с обфускацией, а не при создании).
func TestAddTunnel_SlotsExhausted_ENOSPC(t *testing.T) {
	km, stub := newKmodManagerForTest()
	stub.failAddOnce = &errnoPathErr{op: "write", path: "/proc/awg_proxy/add", err: syscall.ENOSPC}

	_, err := km.AddTunnel("awg20", defaultCfg())
	if err == nil {
		t.Fatal("AddTunnel with ENOSPC: want error, got nil")
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("error must preserve ENOSPC in the chain: %v", err)
	}
	if !strings.Contains(err.Error(), "достигнут предел awg_proxy: 16 туннелей с обфускацией") {
		t.Fatalf("error = %q, want kmod slot-limit hint", err.Error())
	}
	// ENOSPC — не EEXIST: fallback-del и повторный add не должны выполняться.
	if got := stub.countWritesTo("/proc/awg_proxy/del"); got != 0 {
		t.Fatalf("ENOSPC must not trigger EEXIST del-retry; got %d del writes", got)
	}
}
