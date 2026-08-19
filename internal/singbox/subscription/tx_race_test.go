package subscription

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// goid возвращает id текущей горутины (парсинг runtime.Stack — стандартный
// тест-трюк). Нужен txRaceMutator'у, чтобы атрибутировать staged-мутации и
// коммиты конкретной операции без прокидывания меток через Service.
func goid() int {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	// "goroutine 123 [running]:" → 123
	fields := bytes.Fields(buf)
	id, _ := strconv.Atoi(string(fields[1]))
	return id
}

// txRaceMutator — потокобезопасный фейк ConfigMutator, проверяющий
// атомарность батча: все мутации, попавшие в один Reload, должны принадлежать
// ОДНОЙ горутине (одной операции), и во время «медленного» Reload не должны
// появляться чужие мутации. Без Service.txMu пересборка групп из UpdateGroup
// вклинивалась бы в батч идущего refresh — тест фиксирует это как conflict.
type txRaceMutator struct {
	mu          sync.Mutex
	batch       []int // goid каждой staged-мутации с прошлого коммита
	conflicts   []string
	reloadDelay time.Duration
}

func (m *txRaceMutator) stage() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batch = append(m.batch, goid())
}

func (m *txRaceMutator) AllocListenPort() (uint16, error)                    { return 11001, nil }
func (m *txRaceMutator) AllocProxyIndex(_ context.Context) (int, error)      { return 1, nil }
func (m *txRaceMutator) AddOutbound(string, []byte) error                    { m.stage(); return nil }
func (m *txRaceMutator) UpdateOutbound(string, []byte) error                 { m.stage(); return nil }
func (m *txRaceMutator) RemoveOutbound(string) error                         { m.stage(); return nil }
func (m *txRaceMutator) AddInbound(string, []byte) error                     { m.stage(); return nil }
func (m *txRaceMutator) RemoveInbound(string) error                          { m.stage(); return nil }
func (m *txRaceMutator) AddRouteRule([]byte) error                           { m.stage(); return nil }
func (m *txRaceMutator) RemoveRouteRule(string, string) error                { m.stage(); return nil }
func (m *txRaceMutator) EnsureProxy(context.Context, int, int, string) error { return nil }
func (m *txRaceMutator) RemoveProxy(context.Context, int) error              { return nil }
func (m *txRaceMutator) SelectClashProxy(string, string) error               { return nil }
func (m *txRaceMutator) GetClashSelectorActive(string) (string, error)       { return "", nil }
func (m *txRaceMutator) DeclaredOutboundTags() []string                      { return nil }
func (m *txRaceMutator) SubscriptionOutbounds() []map[string]any             { return nil }

func (m *txRaceMutator) Rollback() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batch = nil
}

// Reload — «медленный» коммит: снимает снапшот батча, спит, затем проверяет,
// что (а) за время сна батч не пополнился чужими мутациями и (б) весь батч
// принадлежит горутине, выполняющей коммит.
func (m *txRaceMutator) Reload(_ context.Context) error {
	self := goid()
	m.mu.Lock()
	snapLen := len(m.batch)
	m.mu.Unlock()

	time.Sleep(m.reloadDelay)

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.batch) != snapLen {
		m.conflicts = append(m.conflicts,
			fmt.Sprintf("batch grew from %d to %d during Reload (goroutine %d)", snapLen, len(m.batch), self))
	}
	for _, gid := range m.batch {
		if gid != self {
			m.conflicts = append(m.conflicts,
				fmt.Sprintf("Reload by goroutine %d commits mutation staged by goroutine %d", self, gid))
		}
	}
	m.batch = nil
	return nil
}

// TestService_TxSerialization_RefreshVsGroupUpdate гоняет параллельно refresh
// подписки и UpdateGroup: батч общего мутатора не должен смешивать staged-
// мутации двух операций (issue: планировщик обновляет подписки в отдельных
// горутинах под per-sub локами, Group-CRUD держит только groupMu — без txMu
// Reload одной операции коммитил полуфабрикат другой). Запускать с -race.
func TestService_TxSerialization_RefreshVsGroupUpdate(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	if err != nil {
		t.Fatal(err)
	}
	mut := &txRaceMutator{reloadDelay: 2 * time.Millisecond}
	svc := NewService(store, mut)
	gs, err := NewGroupStore(filepath.Join(t.TempDir(), "groups.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc.SetGroupStore(gs)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890a1@a.example:443?security=tls&sni=a#A\n" +
			"vless://3a3b1c2e-9999-4321-aaaa-1234567890a2@b.example:443?security=tls&sni=b#B\n"))
	}))
	defer srv.Close()

	sub, err := svc.Create(context.Background(), CreateInput{Label: "x", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	grp, err := svc.CreateGroup(context.Background(), GroupCreateInput{
		Label:              "g",
		UseSubscriptionIDs: []string{sub.ID},
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	const iterations = 15
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if _, err := svc.Refresh(context.Background(), sub.ID); err != nil {
				t.Errorf("Refresh: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			label := fmt.Sprintf("g-%d", i)
			if _, err := svc.UpdateGroup(context.Background(), grp.ID, GroupUpdatePatch{Label: &label}); err != nil {
				t.Errorf("UpdateGroup: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	mut.mu.Lock()
	defer mut.mu.Unlock()
	if len(mut.conflicts) > 0 {
		t.Fatalf("interleaved batch commits detected (%d):\n%s", len(mut.conflicts), mut.conflicts[0])
	}
}
