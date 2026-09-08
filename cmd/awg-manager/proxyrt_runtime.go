package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// proxyRemoveRuntime — manager.Deps.RemoveRuntime: снимает из dir сокет, журнал,
// pid и каталог состояния удалённого инстанса (F146). Пути — те же, что строит
// фабрика (control.SocketPath и хвосты .pid/.state от базы сокета), поэтому
// расхождение имён было бы видно здесь же.
func proxyRemoveRuntime(dir string) func(rec instancestore.Record) error {
	return func(rec instancestore.Record) error {
		impl, role, ok := proxyImplRole(rec.Kind)
		if !ok {
			return fmt.Errorf("неизвестная роль %s", rec.Kind)
		}
		sock, err := control.SocketPath(dir, impl, role, rec.ID)
		if err != nil {
			return err
		}
		base := strings.TrimSuffix(sock, ".sock")
		var errs []error
		for _, p := range []string{sock, base + ".log", base + ".pid"} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
		}
		if err := os.RemoveAll(base + ".state"); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
}
