package managed

import (
	"context"

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
// Per-peer best-effort. The flag is set only when at least one removal
// succeeded (or there was nothing to do): if every attempt failed, NDMS is
// likely unreachable, so we leave the flag unset and retry on the next boot.
// Legacy peers all carry 0.0.0.0/0, so a reachable NDMS removes it cleanly;
// fresh installs default the flag to true and never enter this path.
//
// Called from the daemon boot path after the NDMS interface cache is ready.
func (s *Service) MigratePeerAllowIPs(ctx context.Context) {
	if s.settings.IsManagedPeerAllowIPsMigrated() {
		return
	}
	attempted, failures := 0, 0
	for _, sv := range s.settings.GetManagedServers() {
		for _, peer := range sv.Peers {
			if peer.PublicKey == "" {
				continue
			}
			attempted++
			if err := s.rciRemovePeerDefaultRoute(ctx, sv.InterfaceName, peer.PublicKey); err != nil {
				failures++
				if s.log != nil {
					s.log.Warn("migrate-peer-allow-ips: remove failed",
						"interface", sv.InterfaceName, "peer", peer.PublicKey, "error", err)
				}
			}
		}
	}
	if attempted > 0 && failures == attempted {
		// NDMS unreachable / transient — do not mark done; retry next boot.
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
