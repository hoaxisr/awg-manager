package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/procres"
)

// proxyRuntimePaths — файлы рантайма инстанса в dir: сокет, журнал, pid и
// каталог состояния. Одно место на фабрику (proxyFactory) и уборку
// (proxyRemoveRuntime): разъехавшиеся имена оставили бы уборку глотать
// IsNotExist и тихо вернули бы утечку.
type proxyRuntimePaths struct{ sock, log, pid, state string }

func proxyRuntimePathsFor(dir string, kind instancestore.Kind, id string) (proxyRuntimePaths, error) {
	impl, role, ok := proxyImplRole(kind)
	if !ok {
		return proxyRuntimePaths{}, fmt.Errorf("неизвестная роль %s", kind)
	}
	var p proxyRuntimePaths
	var err error
	if p.sock, err = control.SocketPath(dir, impl, role, id); err != nil {
		return p, err
	}
	if p.log, err = control.LogPath(dir, impl, role, id); err != nil {
		return p, err
	}
	if p.pid, err = control.PidPath(dir, impl, role, id); err != nil {
		return p, err
	}
	p.state, err = control.StatePath(dir, impl, role, id)
	return p, err
}

// proxyRemoveRuntime — manager.Deps.RemoveRuntime (F146). Ребёнка, который
// пережил teardown (удаление в окне старта, когда Observe ещё не знает
// процесса, или таймаут остановки), гасит по pid-файлу: без этого сирота
// живёт до ребута, а снятые pid и сокет лишают следующего инстанса с тем же ID
// и усыновления, и возможности его убить. binaryOf — бинарь роли для проверки,
// что pid наш (Runner.AlivePID).
func proxyRemoveRuntime(dir string, binaryOf func(instancestore.Kind) string) func(rec instancestore.Record) error {
	return func(rec instancestore.Record) error {
		p, err := proxyRuntimePathsFor(dir, rec.Kind, rec.ID)
		if err != nil {
			return err
		}
		var errs []error
		r := procres.NewRunner(binaryOf(rec.Kind), p.pid, nil)
		if pid, alive := r.AlivePID(); alive {
			if err := r.Stop(context.Background(), pid); err != nil {
				errs = append(errs, fmt.Errorf("процесс %d: %w", pid, err))
			}
		}
		for _, f := range []string{p.sock, p.log, p.pid} {
			if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
		}
		if err := os.RemoveAll(p.state); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
}
