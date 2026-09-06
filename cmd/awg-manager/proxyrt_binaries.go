package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/install"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/manager"
)

// binaryInstaller — срез install.Service, нужный воротам бута.
type binaryInstaller interface {
	Stale(records []instancestore.Record) []install.Subsystem
	Install(ctx context.Context, subsystem string) error
}

// proxyEnsureBinaries — ворота бута прокси-рантайма (F98, спека
// docs/superpowers/specs/2026-09-05-proxy-binaries-autoinstall-design.md):
// подсистемы с включёнными записями, чьи бинари не совпали с пином сборки,
// докачиваются с зеркала ДО добивания старого поколения. Отказ загрузки —
// ErrBinariesPending: старые процессы держат канал, повтор — proxyBinariesRetry.
// Частичный успех тоже откладывает бут: добивание и legacyCleanup идут одним
// списком, а скачанное не пропадает — на следующем проходе Stale его не назовёт.
func proxyEnsureBinaries(svc binaryInstaller, journal manager.Journal) func(context.Context, []instancestore.Record, func(string)) error {
	return func(ctx context.Context, recs []instancestore.Record, progress func(string)) error {
		for _, name := range svc.Stale(recs) {
			journal.Info("boot", "proxy", fmt.Sprintf("бинари %s не соответствуют пину сборки, загрузка с зеркала", name))
			progress(fmt.Sprintf("Бинари %s не соответствуют этой версии awg-manager, идёт загрузка с зеркала…", name))
			if err := svc.Install(ctx, string(name)); err != nil {
				return fmt.Errorf("Бинари %s не соответствуют этой версии awg-manager, загрузка не удалась: %w. Работающие процессы прежней версии не тронуты, повтор с нарастающим интервалом (до 15 мин) [%w]",
					name, err, manager.ErrBinariesPending)
			}
		}
		return nil
	}
}

// proxyBinariesRetryDelays — backoff повтора бута после ErrBinariesPending;
// последний интервал повторяется.
var proxyBinariesRetryDelays = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute}

// proxyWait — ожидание с отменой; false = контекст закрыт.
func proxyWait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// armBinariesRetry — вооружает цикл повтора, когда Boot вернул
// ErrBinariesPending, один раз на процесс: после Booted Boot больше не зовётся
// (proxyNudge гейтит по SeedInfo), а до того цикл уже идёт. Чужие ошибки бута
// (посев по RCI) ждут WAN-up-нуджа, как и раньше.
func armBinariesRetry(once *sync.Once, err error, start func()) bool {
	if !errors.Is(err, manager.ErrBinariesPending) {
		return false
	}
	armed := false
	once.Do(func() { armed = true; start() })
	return armed
}

// proxyBinariesRetry — повтор бута, пока рантайм не поднят. Отсчёт каждого
// интервала — от возврата предыдущей попытки. Booted снаружи (WAN-up-нудж,
// ручная установка из UI) завершает цикл без лишнего нуджа.
func proxyBinariesRetry(ctx context.Context, rt proxyRuntime, delays []time.Duration,
	wait func(context.Context, time.Duration) bool, nudge func(reason string)) {
	for i := 0; ; i++ {
		if !wait(ctx, delays[min(i, len(delays)-1)]) {
			return
		}
		if rt.SeedInfo().Booted {
			return
		}
		nudge(fmt.Sprintf("binaries-retry-%d", i+1))
		if rt.SeedInfo().Booted {
			return
		}
	}
}
