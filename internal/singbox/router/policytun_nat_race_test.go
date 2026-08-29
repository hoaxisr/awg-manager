package router

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// seedSourcePreserveCopy — как setSourcePreserve, но правит КОПИЮ настроек:
// Load отдаёт живой кэш, и правка по месту сама была бы гонкой с читателями,
// маскируя гонку продакшн-кода.
func seedSourcePreserveCopy(t *testing.T, h *policyTunEnableHarness, segs []string) {
	t.Helper()
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cp := *all
	cp.SingboxRouter.PolicyTunSourcePreserve = true
	cp.SingboxRouter.PolicyTunNATSegments = segs
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = cp; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// Красный до фикса под -race: reconcilePolicyTun берёт объект состояния прямо
// из живого кэша (`st := settings.OpkgTun` после Load) и дописывает в него
// записи NAT-сегментов без лока, а Snapshot маршалит тот же кэш под RLock —
// лок с одной стороны гонку не закрывает.
func TestPolicyTunReconcile_NoSharedNATStateMutation(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	state := &fakeNATState{nat: []query.NATEntry{{Interface: "seg0"}}}
	h.svc.deps.NATState = state
	h.svc.deps.SegmentNAT = &recSegmentNAT{log: h.log, state: state}
	h.svc.deps.DefaultGateway = &fakeGateway{name: "PPPoE0"}
	seedSourcePreserveCopy(t, h, []string{"seg0"})
	provisionPolicyTunForDisable(t, h)

	stop := make(chan struct{})
	var readers sync.WaitGroup
	// Несколько читателей: окно записи узкое, одиночный читатель попадает в
	// него не каждый прогон.
	for r := 0; r < 8; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = h.store.Snapshot()
			}
		}()
	}
	// Каждый тик добавляет в желаемый список ещё один сегмент на маскараде:
	// reconcile применяет к нему source-preserve и дописывает запись в
	// состояние — ровно та запись, что гонится с маршалом.
	var segs []string
	for i := 0; i < 30; i++ {
		segs = append(segs, fmt.Sprintf("seg%d", i))
		state.nat = nil
		for _, seg := range segs {
			state.nat = append(state.nat, query.NATEntry{Interface: seg})
		}
		state.static = nil
		seedSourcePreserveCopy(t, h, segs)
		if err := h.svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile #%d: %v", i, err)
		}
	}
	close(stop)
	readers.Wait()
}

// Красный до фикса под -race: в одном тике есть И отозванный сегмент, И новый.
// restoreRevokedPolicyTunNAT публикует полученный объект состояния в кэш
// (SetOpkgTunState кладёт САМ указатель), а reconcilePolicyTunNAT следом
// дописывает в него записи без лока — запись идёт уже в объект кэша, который
// параллельно маршалит Snapshot. Копии `st` у вызывающего для этого мало.
func TestPolicyTunReconcile_NoSharedNATStateMutation_RevokedAndPending(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	state := &fakeNATState{nat: []query.NATEntry{{Interface: "seg0"}}}
	h.svc.deps.NATState = state
	h.svc.deps.SegmentNAT = &recSegmentNAT{log: h.log, state: state}
	h.svc.deps.DefaultGateway = &fakeGateway{name: "PPPoE0"}
	seedSourcePreserveCopy(t, h, []string{"seg0"})
	provisionPolicyTunForDisable(t, h)

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for r := 0; r < 8; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = h.store.Snapshot()
			}
		}()
	}
	// Каждый тик ЗАМЕНЯЕТ сегмент — как одно сохранение настроек, в котором
	// пользователь поменял A на B: прежний становится отозванным (restore
	// успешен и публикует состояние в кэш), новый — pending (apply пишет
	// записи в тот же объект).
	for i := 1; i <= 30; i++ {
		seg := fmt.Sprintf("seg%d", i)
		state.nat = []query.NATEntry{{Interface: seg}}
		state.static = nil
		seedSourcePreserveCopy(t, h, []string{seg})
		if err := h.svc.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile #%d: %v", i, err)
		}
	}
	close(stop)
	readers.Wait()
}
