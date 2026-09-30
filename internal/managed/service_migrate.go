package managed

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// resolveFn is the interface-store-name-resolver indirection used by tests.
type resolveFn func(ctx context.Context, ndmsName string) string

// MigratePrivateKeys back-fills empty ManagedServer.PrivateKey entries by
// reading the kernel-side WireGuard private key via wg-tools. Idempotent
// (already-populated entries are skipped). Per-server best-effort: a
// failure on one entry (wg-tools missing, kernel name unresolvable,
// interface unreachable) logs a warning and continues with the rest.
//
// Called from the daemon boot path after NDMS interface cache is ready.
// Existing servers created before the PrivateKey field existed in storage
// get their key populated on the next boot; new servers from Service.Create
// arrive with the key already set and are no-ops here.
func (s *Service) MigratePrivateKeys(ctx context.Context) {
	s.migratePrivateKeysWith(ctx, s.resolveKernelName, s.wgRun)
}

// MigratePeerAllowIPs performs the one-time NDMS sweep that strips the legacy
// default 0.0.0.0/0 from every managed-server peer's allow-ips, leaving each
// peer with its /32 only. Older builds added 0.0.0.0/0 by default; new
// firmware rejects multiple peers sharing it ("subnet overlaps with the other
// peer"). Gated by a persisted flag so it runs once.
//
// Флаг встаёт, только если прочиталось всё: список интерфейсов и дерево rc
// (одно чтение на все серверы, F546). Сбой чтения = состояние неизвестно → флаг не ставится,
// проход повторится на следующей загрузке. Сервер, чьего интерфейса нет в
// прочитанном списке (удалён вне панели), — обработан: снимать не у кого.
//
// Снятия — best-effort по пиру: флаг не встаёт, только если отказали ВСЕ
// попытки снятия (NDMS, вероятно, недоступен). Если хоть одно прошло, флаг
// встаёт и пиры с отказавшим снятием больше не повторяются — как и до #713.
// Fresh installs default the flag to true and never enter this path.
//
// Called from the daemon boot path after the NDMS interface cache is ready.
func (s *Service) MigratePeerAllowIPs(ctx context.Context) {
	if s.settings.IsManagedPeerAllowIPsMigrated() {
		return
	}
	// Любая операция allow-ips на отсутствующем ключе NDMS создаёт пира (стенд
	// 5.02.A.11, 28.09): пира из записи, снятого в веб-морде, снятие 0/0
	// воскресило бы. Наличие — свежим чтением rc сервера, под блокировкой,
	// которую берёт и удаление пира.
	defer s.LockPeerSubnets()()
	var exists map[string]query.Confirmed
	var onRouter map[string]map[string]bool
	attempted, failures := 0, 0
	for _, sv := range s.settings.GetManagedServers() {
		if len(sv.Peers) == 0 {
			continue
		}
		if exists == nil {
			var err error
			if exists, err = s.routerServers(ctx); err != nil {
				if s.log != nil {
					s.log.Warn("migrate-peer-allow-ips: interfaces not read, retry next boot", "error", err)
				}
				return
			}
			if onRouter, err = s.routerPeerKeys(ctx, exists); err != nil {
				// Не знаем, есть ли пиры, — не шлём и не ставим флаг.
				if s.log != nil {
					s.log.Warn("migrate-peer-allow-ips: peers not read, retry next boot", "error", err)
				}
				return
			}
		}
		iface, ok := exists[sv.InterfaceName]
		if !ok {
			continue // интерфейс удалён вне панели — пиров на роутере нет
		}
		for _, peer := range sv.Peers {
			if peer.PublicKey == "" || !onRouter[sv.InterfaceName][peer.PublicKey] {
				continue // пира на роутере нет — снимать нечего
			}
			attempted++
			if err := s.rciRemovePeerDefaultRoute(ctx, iface, peer.PublicKey); err != nil {
				failures++
				if s.log != nil {
					s.log.Warn("migrate-peer-allow-ips: remove failed",
						"interface", sv.InterfaceName, "peer", peer.PublicKey, "error", err)
				}
			}
		}
	}
	if attempted > 0 && failures == attempted {
		// Состояние не прочитано или NDMS не принял ни одного снятия — повтор.
		return
	}
	if err := s.settings.SetManagedPeerAllowIPsMigrated(true); err != nil {
		if s.log != nil {
			s.log.Warn("migrate-peer-allow-ips: flag save failed", "error", err)
		}
		return
	}
	if s.log != nil {
		s.log.Info("migrate-peer-allow-ips: completed", "peers", attempted)
	}
}

// routerServers — интерфейсы managed-серверов по одному свежему списку
// роутера; в ответе только существующие.
func (s *Service) routerServers(ctx context.Context) (map[string]query.Confirmed, error) {
	if s.queries == nil || s.queries.Interfaces == nil {
		return nil, fmt.Errorf("interface store not wired")
	}
	var names []string
	for _, sv := range s.settings.GetManagedServers() {
		names = append(names, sv.InterfaceName)
	}
	return s.queries.Interfaces.ConfirmEach(ctx, names)
}

// routerPeerKeys — ключи пиров серверов по одному свежему дереву rc.
func (s *Service) routerPeerKeys(ctx context.Context, ifaces map[string]query.Confirmed) (map[string]map[string]bool, error) {
	if s.queries == nil || s.queries.WGServers == nil {
		return nil, fmt.Errorf("wireguard server store not wired")
	}
	byIface, err := s.queries.WGServers.PeersRCEach(ctx, ifaces)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]bool, len(byIface))
	for id, peers := range byIface {
		keys := make(map[string]bool, len(peers))
		for _, p := range peers {
			keys[p.PublicKey] = true
		}
		out[id] = keys
	}
	return out, nil
}

func (s *Service) migratePrivateKeysWith(ctx context.Context, resolve resolveFn, run wgRunner) {
	for _, sv := range s.settings.GetManagedServers() {
		if sv.PrivateKey != "" {
			continue
		}
		kernel := resolve(ctx, sv.InterfaceName)
		if kernel == "" {
			if s.log != nil {
				s.log.Warn("migrate-private-keys: cannot resolve kernel name",
					"interface", sv.InterfaceName)
			}
			continue
		}
		key, err := readKernelPrivateKeyWith(ctx, kernel, run)
		if err != nil {
			if s.log != nil {
				s.log.Warn("migrate-private-keys: read failed",
					"interface", sv.InterfaceName, "kernel", kernel, "error", err)
			}
			continue
		}
		if err := s.settings.UpdateManagedServer(sv.InterfaceName, func(target *storage.ManagedServer) error {
			target.PrivateKey = key
			return nil
		}); err != nil {
			if s.log != nil {
				s.log.Warn("migrate-private-keys: save failed",
					"interface", sv.InterfaceName, "error", err)
			}
			continue
		}
		if s.log != nil {
			s.log.Info("migrate-private-keys: populated", "interface", sv.InterfaceName)
		}
	}
}
