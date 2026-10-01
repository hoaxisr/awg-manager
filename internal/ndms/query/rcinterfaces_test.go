package query

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// rcFixture — два сервера в FakeNDMS с rc (пиры, адрес, ASC 3.x) и
// тёплая карта интерфейсов, как в проде.
func rcFixture(t *testing.T) (*FakeNDMS, *Queries) {
	t.Helper()
	f := NewFakeNDMS(
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard", State: "up", Link: "up"},
		ndms.Interface{ID: "Wireguard2", Type: "Wireguard", State: "up", Link: "up"},
		ndms.Interface{ID: "Wireguard3", Type: "Wireguard", State: "up", Link: "up"})
	f.SetDetail("Wireguard1", json.RawMessage(`{"wireguard":{"public-key":"PUB1="}}`))
	f.SetRC("Wireguard1", json.RawMessage(`{"ip":{"address":{"address":"10.1.0.1","mask":"255.255.255.0"},"mtu":"1420"},
		"wireguard":{"listen-port":{"port":51821},
			"peer":[{"key":"P1=","comment":"alice","preshared-key":"PSK=","allow-ips":[{"address":"10.1.0.2","mask":"255.255.255.255"}]}],
			"asc":{"jc":4,"jmin":10,"jmax":50,"s1":1,"s2":2,"h1":"1","h2":"2","h3":"3","h4":"4","s3":12,"s4":13,"i1":"<b 0x01>","header-protection-key":"HPK"}}}`))
	f.SetRC("Wireguard2", json.RawMessage(`{"wireguard":{"peer":[{"key":"P2=","allow-ips":[{"address":"10.2.0.2","mask":"255.255.255.255"}]}]}}`))
	f.SetRC("Wireguard3", json.RawMessage(`{"wireguard":{"peer":[{"key":"P3="}]}}`))
	q := NewQueries(Deps{Getter: f, Logger: NopLogger()})
	if _, err := q.Interfaces.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f, q
}

// Конфигурация всех серверов и ASC — одно чтение дерева rc, по имени ни разу.
func TestRCInterfaces_OneReadServesAll(t *testing.T) {
	ctx := context.Background()
	f, q := rcFixture(t)

	c1, err := q.WGServers.GetConfig(ctx, "Wireguard1")
	if err != nil {
		t.Fatal(err)
	}
	if c1.PublicKey != "PUB1=" || c1.Address != "10.1.0.1" || c1.MTU != 1420 || c1.ListenPort != 51821 ||
		len(c1.Peers) != 1 || c1.Peers[0].PresharedKey != "PSK=" || c1.Peers[0].AllowedIPs[0] != "10.1.0.2/32" {
		t.Fatalf("GetConfig Wireguard1 = %+v", c1)
	}
	if c2, err := q.WGServers.GetConfig(ctx, "Wireguard2"); err != nil || len(c2.Peers) != 1 || c2.Peers[0].PublicKey != "P2=" {
		t.Fatalf("GetConfig Wireguard2 = %+v, %v", c2, err)
	}
	raw, err := q.WGServers.GetASCParams(ctx, "Wireguard1", true)
	if err != nil {
		t.Fatal(err)
	}
	var asc struct {
		Jc int    `json:"jc"`
		S3 int    `json:"s3"`
		I1 string `json:"i1"`
	}
	if err := json.Unmarshal(raw, &asc); err != nil || asc.Jc != 4 || asc.S3 != 12 || asc.I1 != "<b 0x01>" {
		t.Fatalf("GetASCParams = %s (%v)", raw, err)
	}
	if got := f.RCListCalls(); got != 1 {
		t.Fatalf("чтений дерева rc = %d, want 1", got)
	}
	if f.E != 0 {
		t.Fatalf("E=%d, want 0", f.E)
	}
}

// Наша команда (Invalidate) сбрасывает дерево; хук слоя (InvalidateRuntime) — нет.
func TestRCInterfaces_TTLAndInvalidate(t *testing.T) {
	ctx := context.Background()
	f, q := rcFixture(t)
	if _, err := q.WGServers.GetConfig(ctx, "Wireguard1"); err != nil {
		t.Fatal(err)
	}
	q.WGServers.InvalidateRuntime()
	if _, err := q.WGServers.GetConfig(ctx, "Wireguard1"); err != nil {
		t.Fatal(err)
	}
	if got := f.RCListCalls(); got != 1 {
		t.Fatalf("после InvalidateRuntime чтений дерева = %d, want 1", got)
	}
	q.WGServers.Invalidate("Wireguard1")
	if _, err := q.WGServers.GetConfig(ctx, "Wireguard1"); err != nil {
		t.Fatal(err)
	}
	if got := f.RCListCalls(); got != 2 {
		t.Fatalf("после Invalidate чтений дерева = %d, want 2", got)
	}
}

// Отказ дерева после успешного чтения (TTL истёк) — прежнее дерево + Warn.
func TestRCInterfaces_StaleOnError(t *testing.T) {
	ctx := context.Background()
	f, q := rcFixture(t)
	log := &dedupCaptureLogger{}
	s := NewWGServerStoreWithTTL(f, log, q.Interfaces, time.Minute, time.Minute, time.Millisecond)
	if _, err := s.GetConfig(ctx, "Wireguard1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	f.FailRCList(errors.New("rci down"))
	c, err := s.GetConfig(ctx, "Wireguard1")
	if err != nil || len(c.Peers) != 1 || c.Peers[0].PublicKey != "P1=" {
		t.Fatalf("GetConfig при отказе дерева = %+v, %v; want прежнее", c, err)
	}
	log.mu.Lock()
	warned := strings.Contains(strings.Join(log.msgs, "\n"), "serving stale cache")
	log.mu.Unlock()
	if f.RCListCalls() != 2 || !warned {
		t.Fatalf("RCListCalls=%d, warn=%v", f.RCListCalls(), warned)
	}
}

// Проверка перед записью — свежее дерево на каждый вызов; снятый — ErrGone без E.
func TestPeersRC_Fresh(t *testing.T) {
	ctx := context.Background()
	f, q := rcFixture(t)
	c1, _, ok, err := q.Interfaces.Confirm(ctx, "Wireguard1")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	for i := 1; i <= 2; i++ {
		peers, err := q.WGServers.PeersRC(ctx, c1)
		if err != nil || len(peers) != 1 || peers[0].PublicKey != "P1=" {
			t.Fatalf("PeersRC = %+v, %v", peers, err)
		}
		if got := f.RCListCalls(); got != i {
			t.Fatalf("вызов %d: чтений дерева %d", i, got)
		}
	}
	c2, _, _, _ := q.Interfaces.Confirm(ctx, "Wireguard2")
	f.Remove("Wireguard2")
	if _, err := q.WGServers.PeersRC(ctx, c2); !errors.Is(err, ErrGone) {
		t.Fatalf("PeersRC снятого = %v, want ErrGone", err)
	}
	if f.E != 0 {
		t.Fatalf("E=%d, want 0", f.E)
	}
}

// Пиры нескольких серверов — одно свежее дерево на всех.
func TestPeersRCEach_OneRead(t *testing.T) {
	ctx := context.Background()
	f, q := rcFixture(t)
	cs, err := q.Interfaces.ConfirmEach(ctx, []string{"Wireguard1", "Wireguard2", "Wireguard3"})
	if err != nil || len(cs) != 3 {
		t.Fatal(cs, err)
	}
	before := f.RCListCalls()
	got, err := q.WGServers.PeersRCEach(ctx, cs)
	if err != nil {
		t.Fatal(err)
	}
	if n := f.RCListCalls() - before; n != 1 {
		t.Fatalf("чтений дерева %d, want 1", n)
	}
	if len(got) != 3 || got["Wireguard2"][0].PublicKey != "P2=" || got["Wireguard3"][0].PublicKey != "P3=" {
		t.Fatalf("PeersRCEach = %+v", got)
	}
	f.Remove("Wireguard3")
	if _, err := q.WGServers.PeersRCEach(ctx, cs); !errors.Is(err, ErrGone) {
		t.Fatalf("PeersRCEach со снятым = %v, want ErrGone", err)
	}
	if f.E != 0 {
		t.Fatalf("E=%d", f.E)
	}
}

// ASC 3.x перед записью — свежее дерево на каждый вызов.
func TestASC3Fields_Fresh(t *testing.T) {
	ctx := context.Background()
	f, q := rcFixture(t)
	for i := 1; i <= 2; i++ {
		got, err := q.WGServers.ASC3Fields(ctx, "Wireguard1")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || string(got["header-protection-key"]) != `"HPK"` {
			t.Fatalf("ASC3Fields = %v", got)
		}
		if n := f.RCListCalls(); n != i {
			t.Fatalf("вызов %d: чтений дерева %d", i, n)
		}
	}
	f.Remove("Wireguard1")
	if _, err := q.WGServers.ASC3Fields(ctx, "Wireguard1"); !errors.Is(err, ErrGone) {
		t.Fatalf("ASC3Fields снятого = %v, want ErrGone", err)
	}
	if f.E != 0 {
		t.Fatalf("E=%d", f.E)
	}
}

// F581: сверка после записи — свежее дерево на каждый вызов, даже при тёплом
// кэше (иначе сверка видела бы дерево до записи).
func TestASCParamsFresh_Fresh(t *testing.T) {
	ctx := context.Background()
	f, q := rcFixture(t)
	if _, err := q.WGServers.GetASCParams(ctx, "Wireguard1", true); err != nil {
		t.Fatal(err)
	}
	warm := f.RCListCalls()
	for i := 1; i <= 2; i++ {
		raw, err := q.WGServers.ASCParamsFresh(ctx, "Wireguard1", true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"jc":4`) || !strings.Contains(string(raw), `"s3":12`) {
			t.Fatalf("ASCParamsFresh = %s", raw)
		}
		if n := f.RCListCalls() - warm; n != i {
			t.Fatalf("вызов %d: чтений дерева %d", i, n)
		}
	}
	f.Remove("Wireguard1")
	if _, err := q.WGServers.ASCParamsFresh(ctx, "Wireguard1", true); !errors.Is(err, ErrGone) {
		t.Fatalf("ASCParamsFresh снятого = %v, want ErrGone", err)
	}
	if f.E != 0 {
		t.Fatalf("E=%d", f.E)
	}
}

// R35: ifcreated потерян — сервер уже в свежем списке, а дерево rc в кэше
// старше его создания. Конфигурация и ASC не пустые/не ErrGone: ОДНО свежее
// чтение дерева на вызов. Нет и в свежем дереве — прежнее поведение.
func TestRCInterfaces_MissedCreateRefetchesOnce(t *testing.T) {
	ctx := context.Background()
	f, q := rcFixture(t)
	if _, err := q.WGServers.GetConfig(ctx, "Wireguard1"); err != nil { // дерево в кэше
		t.Fatal(err)
	}
	f.Add(ndms.Interface{ID: "Wireguard5", Type: "Wireguard", State: "up"})
	f.SetRC("Wireguard5", json.RawMessage(`{"wireguard":{"peer":[{"key":"P5=","allow-ips":[{"address":"10.5.0.2","mask":"255.255.255.255"}]}],"asc":{"jc":7}}}`))
	f.DrainHooks() // ifcreated потерян: дерево rc не сброшено
	if _, err := q.Interfaces.Snapshot(ctx, SnapshotLive); err != nil {
		t.Fatal(err)
	}

	before := f.RCListCalls()
	cfg, err := q.WGServers.GetConfig(ctx, "Wireguard5")
	if err != nil || len(cfg.Peers) != 1 || cfg.Peers[0].PublicKey != "P5=" {
		t.Fatalf("GetConfig = %+v, %v; want пир P5=", cfg, err)
	}
	if n := f.RCListCalls() - before; n != 1 {
		t.Fatalf("GetConfig: чтений дерева %d, want 1", n)
	}
	raw, err := q.WGServers.GetASCParams(ctx, "Wireguard5", false)
	if err != nil || !strings.Contains(string(raw), `"jc":7`) {
		t.Fatalf("GetASCParams = %s, %v", raw, err)
	}
	if n := f.RCListCalls() - before; n != 1 {
		t.Fatalf("GetASCParams после освежения: чтений дерева %d, want 1", n)
	}

	// В снимке есть, в свежем дереве нет (снят без хука) — одно чтение и
	// прежнее поведение: пустая конфигурация, ASC — ErrGone.
	f.Remove("Wireguard5")
	q.WGServers.InvalidateAll()
	if _, err := q.WGServers.GetConfig(ctx, "Wireguard1"); err != nil { // дерево без Wireguard5 в кэше
		t.Fatal(err)
	}
	before = f.RCListCalls()
	if cfg, err := q.WGServers.GetConfig(ctx, "Wireguard5"); err != nil || len(cfg.Peers) != 0 {
		t.Fatalf("GetConfig снятого = %+v, %v", cfg, err)
	}
	if _, err := q.WGServers.GetASCParams(ctx, "Wireguard5", false); !errors.Is(err, ErrGone) {
		t.Fatalf("GetASCParams снятого = %v, want ErrGone", err)
	}
	if n := f.RCListCalls() - before; n != 2 {
		t.Fatalf("чтений дерева %d, want 2 (по одному на вызов)", n)
	}
	if f.E != 0 {
		t.Fatalf("E=%d", f.E)
	}
}

// R35 для списка серверов: новый сервер без ifcreated (пересбор по хуку слоя)
// получает allow-ips из одного свежего дерева, а не «сетей нет».
func TestRCInterfaces_MissedCreateListRefetchesOnce(t *testing.T) {
	ctx := context.Background()
	f, q := rcFixture(t)
	if _, err := q.WGServers.List(ctx); err != nil {
		t.Fatal(err)
	}
	f.Add(ndms.Interface{ID: "Wireguard6", Type: "Wireguard", State: "up"})
	f.SetDetail("Wireguard6", json.RawMessage(`{"wireguard":{"peer":[{"public-key":"P6="}]}}`))
	f.SetRC("Wireguard6", json.RawMessage(`{"wireguard":{"peer":[{"key":"P6=","allow-ips":[{"address":"10.6.0.2","mask":"255.255.255.255"}]}]}}`))
	f.DrainHooks()
	if _, err := q.Interfaces.Snapshot(ctx, SnapshotLive); err != nil {
		t.Fatal(err)
	}
	q.WGServers.InvalidateRuntime() // iflayerchanged: дерево rc не сброшено
	before := f.RCListCalls()
	srvs, err := q.WGServers.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range srvs {
		if s.ID == "Wireguard6" && len(s.Peers) == 1 {
			got = s.Peers[0].AllowedIPs
		}
	}
	if len(got) != 1 || got[0] != "10.6.0.2/32" {
		t.Fatalf("allow-ips Wireguard6 = %v", got)
	}
	if n := f.RCListCalls() - before; n != 1 {
		t.Fatalf("чтений дерева %d, want 1", n)
	}
}
