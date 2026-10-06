package router

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// F607 на настоящем командном слое: ACL-примитивы ходят в модель NDMS, где
// running-config — блоки списков, а повтор стоящего permit даёт E duplicate
// (стенд X2: идемпотентной формы permit нет). Список интерфейсов (Confirm) —
// от FakeNDMS. Модель считает чтения running-config (≈89 тиков ndm каждое) и
// E-дубли.
type aclModel struct {
	*query.FakeNDMS

	mu     sync.Mutex
	blocks map[string][]string // заголовок списка → правила в порядке постановки
	reads  int
	dupes  int
	parses []string
}

func newACLModel(ifaces ...string) *aclModel {
	list := make([]ndms.Interface, 0, len(ifaces))
	for _, n := range ifaces {
		list = append(list, ndms.Interface{ID: n})
	}
	return &aclModel{FakeNDMS: query.NewFakeNDMS(list...), blocks: map[string][]string{}}
}

func (m *aclModel) seed(header string, rules ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blocks[header] = append(m.blocks[header], rules...)
}

func (m *aclModel) rc() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	var lines []string
	for h, rules := range m.blocks {
		lines = append(lines, h)
		for _, r := range rules {
			lines = append(lines, "    "+r)
		}
		lines = append(lines, "!")
	}
	b, _ := json.Marshal(map[string]any{"message": lines})
	return b
}

func (m *aclModel) GetRaw(ctx context.Context, path string) ([]byte, error) {
	if path == "/show/running-config" {
		return m.rc(), nil
	}
	return m.FakeNDMS.GetRaw(ctx, path)
}

func (m *aclModel) Get(ctx context.Context, path string, dst any) error {
	if path == "/show/running-config" {
		return json.Unmarshal(m.rc(), dst)
	}
	return m.FakeNDMS.Get(ctx, path, dst)
}

// Post: permit ставит правило в блок (повтор — E duplicate, как NDMS); bind и
// auto-delete идемпотентны без E (X2) и в модели не нужны.
func (m *aclModel) Post(_ context.Context, payload any) (json.RawMessage, error) {
	p, _ := payload.(map[string]any)
	cmd, _ := p["parse"].(string)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parses = append(m.parses, cmd)
	for _, kw := range []string{" permit ip ", " permit ipv6 "} {
		i := strings.Index(cmd, kw)
		if i < 0 || strings.HasPrefix(cmd, "no ") {
			continue
		}
		header, rule := cmd[:i], cmd[i+1:]
		for _, r := range m.blocks[header] {
			if r == rule {
				m.dupes++
				return json.RawMessage(`[{"parse":{"status":[{"status":"error","ident":"Network::Acl","message":"a duplicate was found for the rule being set."}]}}]`), nil
			}
		}
		m.blocks[header] = append(m.blocks[header], rule)
	}
	return json.RawMessage(`{}`), nil
}

func (m *aclModel) permits() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.parses {
		if !strings.HasPrefix(c, "no ") && (strings.Contains(c, " permit ip ") || strings.Contains(c, " permit ipv6 ")) {
			n++
		}
	}
	return n
}

func (m *aclModel) readCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reads
}

// realACLOpkg — стаб обвязки, у которого постановка permit идёт в настоящий
// командный слой поверх aclModel (подтверждение имени — как confirmingOpkgTun).
type realACLOpkg struct {
	OpkgTunProvisioner
	cmds   *command.InterfaceCommands
	ifaces *query.InterfaceStore
}

func withRealACL(t *testing.T, base OpkgTunProvisioner, m *aclModel) *realACLOpkg {
	t.Helper()
	q := query.NewQueries(query.Deps{Getter: m, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	cmds := command.NewCommands(command.Deps{
		Poster:  m,
		Queries: q,
		Save:    command.NewSaveCoordinator(m, nil, time.Hour, time.Hour, 0, q.RunningConfig),
		IsOS5:   func() bool { return true },
	})
	return &realACLOpkg{OpkgTunProvisioner: base, cmds: cmds.Interfaces, ifaces: q.Interfaces}
}

func (o *realACLOpkg) set(ctx context.Context, name string, do func(query.Confirmed) error) error {
	c, _, ok, err := o.ifaces.Confirm(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return do(c)
}

func (o *realACLOpkg) SetPermitAllACL(ctx context.Context, name string) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.SetPermitAllACL(ctx, c) })
}

func (o *realACLOpkg) SetPermitAllACLv6(ctx context.Context, name string) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.SetPermitAllACLv6(ctx, c) })
}

func (o *realACLOpkg) SetPermitAllACLs(ctx context.Context, name string, withV6 bool) error {
	return o.set(ctx, name, func(c query.Confirmed) error { return o.cmds.SetPermitAllACLs(ctx, c, withV6) })
}

const (
	f607HeaderV4 = "access-list _WEBADMIN_OpkgTun0"
	f607HeaderV6 = "ipv6 access-list _WEBADMIN_OpkgTun0"
)

// Включение ставит permit обоих семейств по ОДНОМУ чтению running-config.
// Мутация: включение зовёт SetPermitAllACL и SetPermitAllACLv6 по отдельности
// (или SetPermitAllACLs читает на каждое семейство) → 2 чтения, красный.
func TestEnable_PermitACL_OneRunningConfigRead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (enable func() error, base OpkgTunProvisioner, set func(OpkgTunProvisioner))
	}{
		{"policy-tun", func(t *testing.T) (func() error, OpkgTunProvisioner, func(OpkgTunProvisioner)) {
			h := newPolicyTunEnableHarness(t, "")
			return func() error { return h.svc.Enable(context.Background()) }, h.svc.deps.OpkgTun,
				func(p OpkgTunProvisioner) { h.svc.deps.OpkgTun = p }
		}},
		{"fakeip", func(t *testing.T) (func() error, OpkgTunProvisioner, func(OpkgTunProvisioner)) {
			h := newFakeIPEnableHarness(t, "")
			return func() error { return h.svc.Enable(context.Background()) }, h.svc.deps.OpkgTun,
				func(p OpkgTunProvisioner) { h.svc.deps.OpkgTun = p }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enable, base, set := tc.setup(t)
			m := newACLModel("OpkgTun0")
			set(withRealACL(t, base, m))
			if err := enable(); err != nil {
				t.Fatalf("Enable: %v", err)
			}
			if n := m.readCount(); n != 1 {
				t.Errorf("чтений running-config на включение = %d, ждали 1", n)
			}
			if n := m.permits(); n != 2 {
				t.Errorf("permit на включение = %d, ждали 2 (v4 и v6): %v", n, m.parses)
			}
		})
	}
}

// fakeip-ассерт (F607, В2): после включения правила стоят — за любое число
// тиков ассерт делает не больше 2 чтений (по одному на семейство, one-shot) и
// ни одного permit, E duplicate — 0.
// Мутация: снять гейт чтения (permit всегда) → 2 permit и 2 E, красный;
// не взводить флаги ассерта → чтение и bind на каждом тике, красный.
func TestReconcileFakeIPTun_PermitACLAssert_NoResendBounded(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	m := newACLModel("OpkgTun0")
	h.svc.deps.OpkgTun = withRealACL(t, h.svc.deps.OpkgTun, m)
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	// Новый процесс поверх включённого режима: ассерт ещё не проходил.
	h.svc.fakeipACLAsserted, h.svc.fakeipACLv6Asserted = false, false
	reads0, permits0 := m.readCount(), m.permits()

	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	for i := 0; i < 3; i++ {
		if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
			t.Fatalf("reconcileFakeIPTun #%d: %v", i, err)
		}
	}
	if n := m.readCount() - reads0; n > 2 {
		t.Errorf("чтений running-config за 3 тика = %d, ждали ≤ 2", n)
	}
	if n := m.permits() - permits0; n != 0 {
		t.Errorf("ассерт повторил стоящий permit %d раз: %v", n, m.parses)
	}
	if m.dupes != 0 {
		t.Errorf("E duplicate = %d, ждали 0", m.dupes)
	}
	if !h.svc.fakeipACLAsserted || !h.svc.fakeipACLv6Asserted {
		t.Error("флаги ассерта не взведены после успеха")
	}
}

// В списке только ЧУЖОЕ правило (межсетевой экран веб-морды): наш permit
// уходит один раз (ставится рядом), следующий ассерт его не повторяет.
// Мутация: «правило стоит» = «блок есть» → наш permit не уходит, красный;
// предикат всегда false → ассерт шлёт дубль (E), красный.
func TestPermitACL_ForeignRuleOnly_PermitOnceNoDuplicate(t *testing.T) {
	h := newFakeIPEnableHarness(t, "")
	m := newACLModel("OpkgTun0")
	m.seed(f607HeaderV4, "permit tcp 10.77.0.2 255.255.255.255 0.0.0.0 0.0.0.0")
	m.seed(f607HeaderV6, "permit ipv6 2001:db8::/32 ::/0")
	h.svc.deps.OpkgTun = withRealACL(t, h.svc.deps.OpkgTun, m)
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if n := m.permits(); n != 2 {
		t.Fatalf("наш permit рядом с чужим: %d, ждали 2 (v4 и v6): %v", n, m.parses)
	}

	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{0: true}}
	h.svc.fakeipACLAsserted, h.svc.fakeipACLv6Asserted = false, false
	all, _ := h.store.Load()
	sr, _ := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err := h.svc.reconcileFakeIPTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcileFakeIPTun: %v", err)
	}
	if n := m.permits(); n != 2 {
		t.Errorf("ассерт повторил permit: всего %d, ждали 2: %v", n, m.parses)
	}
	if m.dupes != 0 {
		t.Errorf("E duplicate = %d, ждали 0", m.dupes)
	}
}
