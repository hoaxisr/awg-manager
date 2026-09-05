package router

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// holdProbeProc считает обращения к процессу. Нужен, чтобы поймать чужой
// debounce-reload, прилетевший ПОСРЕДИ транзакции.
type holdProbeProc struct {
	mu    sync.Mutex
	calls []string
}

func (p *holdProbeProc) record(s string) {
	p.mu.Lock()
	p.calls = append(p.calls, s)
	p.mu.Unlock()
}

// countKind считает обращения одного вида: «движок тронули» после выхода из
// транзакции обязано быть именно reload, а не побочный Stop самого Disable.
func (p *holdProbeProc) countKind(kind string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if c == kind {
			n++
		}
	}
	return n
}

func (p *holdProbeProc) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *holdProbeProc) IsRunning() (bool, int) { return true, 4242 }
func (p *holdProbeProc) Start() error           { p.record("start"); return nil }
func (p *holdProbeProc) Stop() error            { p.record("stop"); return nil }
func (p *holdProbeProc) Reload() error          { p.record("reload"); return nil }

// Disable — транзакция: он паркует слот, чистит netfilter и примиряет базу.
// Пока она идёт, чужой продюсер (подписки, device-proxy) не должен дёрнуть
// движок своим debounce-reload'ом: при живом tun каждый такой reload это полный
// Stop+Start, то есть разрыв соединений посреди чужой транзакции.
//
// Тест поведенческий: чужая запись слота делается ИЗ СЕРЕДИНЫ Disable (через
// netfilter-хук) и переживает целое окно debounce. Без hold'а движок был бы
// тронут прямо там; с hold'ом — ровно один раз, уже на выходе.
func TestDisable_HoldsForeignReloadsUntilDone(t *testing.T) {
	dir := t.TempDir()
	proc := &holdProbeProc{}
	orch := orchestrator.NewWithAppliedPath(dir, proc, filepath.Join(t.TempDir(), "singbox-applied.json"))
	for _, m := range []orchestrator.SlotMeta{
		{Slot: orchestrator.SlotRouter, Filename: "20-router.json"},
		{Slot: orchestrator.SlotSubscriptions, Filename: "30-subscriptions.json"},
	} {
		if err := orch.Register(m); err != nil {
			t.Fatalf("Register %v: %v", m.Slot, err)
		}
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if err := orch.SetEnabled(orchestrator.SlotSubscriptions, true); err != nil {
		t.Fatalf("SetEnabled subscriptions: %v", err)
	}

	midTransaction := 0
	foreignWritten := false
	ipt := &IPTables{
		runIPTablesOut: func(_ context.Context, _ ...string) (string, error) { return "", nil },
		runIPTables: func(_ context.Context, _ ...string) error {
			if !foreignWritten {
				foreignWritten = true
				// Чужой продюсер записал свой слот посреди нашей транзакции.
				if err := orch.Save(orchestrator.SlotSubscriptions,
					[]byte(`{"outbounds":[{"type":"direct","tag":"sub-probe"}]}`)); err != nil {
					t.Errorf("чужой Save: %v", err)
				}
				// Переживаем окно debounce с запасом: без hold'а reload
				// выстрелил бы ровно здесь.
				time.Sleep(400 * time.Millisecond)
				midTransaction = proc.count()
			}
			return nil
		},
		runIP:    func(_ context.Context, _ ...string) error { return nil },
		runIPOut: func(_ context.Context, _ ...string) (string, error) { return "", nil },
	}

	svc := newTestService(t, Deps{
		Settings: newTestSettingsStore(t, storage.SingboxRouterSettings{
			Enabled:    true,
			PolicyName: "Policy0",
		}),
		Policies:       &fakeAccessPolicyProvider{mark: "0xffffaaa"},
		IPTables:       ipt,
		Singbox:        newTestSingbox(t),
		WANIPCollector: &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
		Orch:           orch,
	})

	if err := svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if !foreignWritten {
		t.Fatal("чужая запись не состоялась — тест ничего не проверил")
	}
	if midTransaction != 0 {
		t.Errorf("посреди транзакции движок трогать нельзя, обращений %d (%v)", midTransaction, proc.calls)
	}

	// RT6: hold обязан быть ОТПУЩЕН, и отложенная чужая запись — доехать.
	// Проверялась только первая половина контракта, поэтому мутация «взять
	// hold и не вернуть» (`s.deps.Orch.HoldReloads()` без вызова функции
	// снятия) проходила зелёной. Её цена — застрявший счётчик holds: глохнет
	// debounce-путь (правка чужого продюсера лишь взводит pendingReload), и
	// до перезапуска демона hold не отпустить. Прямой ReloadNow идёт мимо
	// счётчика и работает — глухота именно debounce, а не «любого reload».
	deadline := time.Now().Add(3 * time.Second)
	for proc.countKind("reload") == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if proc.countKind("reload") == 0 {
		t.Fatalf("отложенный reload не состоялся после транзакции — hold не отпущен (%v)", proc.calls)
	}
}
