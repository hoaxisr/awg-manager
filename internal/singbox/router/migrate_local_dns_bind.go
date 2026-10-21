package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// MigrateLocalDNSBind долечивает уже сохранённые DNS-серверы маршрутизатора на
// собственном адресе роутера: им ставится тот же адрес отправителя
// (localDNSSelfBind, F702). Новые серверы получают его при сохранении; без
// этого прохода сохранённый до обновления сервер остаётся мёртвым, пока его не
// пересохранят руками.
//
// Только слот маршрутизатора (активный, disabled/, pending/): fakeip
// автопривязку не включает, ему бинд не нужен. Правка по обобщённой карте —
// неописанные нашими структурами поля переживают проход. Идемпотентна.
func MigrateLocalDNSBind(configDir string, localAddrs func() ([]net.Addr, error)) (bool, error) {
	name := routerSlotFilename()
	addrs, err := localAddrs()
	if err != nil {
		return false, fmt.Errorf("адреса интерфейсов роутера: %w", err)
	}
	changed := false
	var failures []error
	for _, p := range []string{
		filepath.Join(configDir, name),
		filepath.Join(configDir, "disabled", name),
		filepath.Join(configDir, "pending", name),
	} {
		fileChanged, err := migrateLocalDNSBindFile(p, addrs)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		changed = changed || fileChanged
	}
	return changed, errors.Join(failures...)
}

func routerSlotFilename() string {
	for _, meta := range orchestrator.KnownSlots() {
		if meta.Slot == orchestrator.SlotRouter {
			return meta.Filename
		}
	}
	return ""
}

func migrateLocalDNSBindFile(path string, addrs []net.Addr) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return false, fmt.Errorf("%s: parse: %w", path, err)
	}
	dns, _ := root["dns"].(map[string]any)
	servers, _ := dns["servers"].([]any)
	changed := false
	for _, v := range servers {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		srv := DNSServer{}
		srv.Tag, _ = m["tag"].(string)
		srv.Type, _ = m["type"].(string)
		srv.Server, _ = m["server"].(string)
		srv.Detour, _ = m["detour"].(string)
		srv.Inet4BindAddress, _ = m["inet4_bind_address"].(string)
		if bind, ok := localDNSSelfBind(srv, addrs); ok {
			m["inet4_bind_address"] = bind
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	next, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, fmt.Errorf("%s: marshal: %w", path, err)
	}
	if err := storage.AtomicWrite(path, next); err != nil {
		return false, err
	}
	return true, nil
}
