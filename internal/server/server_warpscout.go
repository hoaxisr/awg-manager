package server

import (
	"context"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/backup"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/warpscout"
)

func kmodVersionAtLeast(ver string, wantMaj, wantMin int) bool {
	s := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ver)), "v"))
	if s == "" {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return false
	}
	maj, err1 := strconv.Atoi(parts[0])
	// Вторую компоненту добирает префикс цифр на случай суффиксов вида "1-rc1".
	minPart := parts[1]
	if i := strings.IndexFunc(minPart, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		minPart = minPart[:i]
	}
	if minPart == "" {
		return false
	}
	min, err2 := strconv.Atoi(minPart)
	if err1 != nil || err2 != nil {
		return false
	}
	return maj > wantMaj || (maj == wantMaj && min >= wantMin)
}

// buildWarpScout собирает сервис «WARP Сканер» и его хендлер. Сторожей не
// запускает (это делает Start): registerRoutes зовут и тесты гардов, где
// фоновые горутины не нужны. Любая зависимость может быть nil — адаптер
// отвечает ошибкой на вызове, а не паникой на сборке.
func (s *Server) buildWarpScout(h *routeHandlers) *api.WarpScoutHandler {
	if s.settings == nil {
		return nil
	}
	log := logging.NewScopedLogger(s.loggingService, logging.GroupTunnel, logging.SubWarpScout)
	store, err := warpscout.NewStore(s.settings.DataDir(), warpscout.NewLocalCipher(s.settings.DataDir()))
	if err != nil {
		log.Warn("init", "warpscout", "Хранилище недоступно: "+err.Error())
		return nil
	}
	// Восстановление из резервной копии заменяет warpscout.json на диске и
	// перезапускает демон; до перезапуска состояние из памяти писать нельзя —
	// оно легло бы поверх восстановленного файла. Тот же маркер гейтит
	// shutdown-хук прокси-рантайма (cmd/awg-manager/wiring_server.go).
	// Маркер, уже стоявший при старте, — след прошлого процесса: этот читает
	// восстановленный файл сам и писать вправе (бут снимет маркер позже).
	dataDir := s.settings.DataDir()
	bornAfterRestore := backup.HasPostRestoreMarker(dataDir)
	store.Frozen = func() bool {
		return backup.Restoring() || (!bornAfterRestore && backup.HasPostRestoreMarker(dataDir))
	}
	supportsKernelAWG31 := func() bool {
		if s.kmodLoader == nil {
			return false
		}
		ver := s.kmodLoader.LoadedVersion()
		if ver == "" {
			ver = s.kmodLoader.OnDiskVersion()
		}
		return kmodVersionAtLeast(ver, 3, 1)
	}
	ops := &api.WarpTunnelOps{
		Svc:                 s.tunnelService,
		Store:               s.tunnels,
		Settings:            s.settings,
		System:              s.systemTunnelService,
		Commands:            s.ndmsCommands,
		Queries:             s.ndmsQueries,
		Tunnels:             h.tunnelsHandler,
		SupportsASC3:        ndmsinfo.SupportsWireguardASC3,
		SupportsKernelAWG31: supportsKernelAWG31,
	}
	deps := warpscout.Deps{
		Store:            store,
		Runner:           &warpscout.BinaryRunner{},
		Tunnels:          ops,
		Prober:           &warpscout.HTTPTraceProber{},
		HTTP:             api.WarpHTTPClientFactory,
		HTTPDirectBypass: api.WarpSplitTLSClientFactory,
		HTTPProxyClient:  api.WarpProxyHTTPClient,
		HTTPFallbacks: func(ctx context.Context) []warpscout.FallbackClient {
			return api.WarpFallbackClients(ctx, s.deviceProxySvc, ops, s.downloadSvc)
		},
		Log: log,
		WAN: func(context.Context) (string, bool) {
			if s.tunnelService == nil {
				return "", true
			}
			m := s.tunnelService.WANModel()
			if m == nil {
				return "", true
			}
			return m.PreferredUp()
		},
		Routes: func(ctx context.Context) []warpscout.RouteRef {
			out := []warpscout.RouteRef{}
			seen := map[string]bool{}
			if s.tunnelService != nil {
				if m := s.tunnelService.WANModel(); m != nil {
					for _, w := range m.ForUI() {
						if w.Name == "" || seen[w.Name] {
							continue
						}
						seen[w.Name] = true
						label := w.Label
						if label == "" {
							label = w.ID
						}
						if label == "" {
							label = w.Name
						}
						out = append(out, warpscout.RouteRef{
							Name:  w.Name,
							Label: label,
							Kind:  "wan",
							Up:    w.Up,
						})
					}
				}
			}
			if list, err := ops.List(ctx); err == nil {
				for _, t := range list {
					if t.Protected || t.Iface == "" || seen[t.Iface] {
						continue
					}
					if t.IsWarp {
						// Внешний WARP (верхнеуровневый) доступен как маршрут сканера для WARP-in-WARP.
						if t.ViaIface != "" {
							continue
						}
						seen[t.Iface] = true
						out = append(out, warpscout.RouteRef{
							Name:       t.Iface,
							Label:      t.Name,
							Kind:       "warp",
							Up:         t.Running,
							TunnelMode: t.Mode,
							TunnelID:   t.ID,
						})
						continue
					}
					seen[t.Iface] = true
					out = append(out, warpscout.RouteRef{
						Name:  t.Iface,
						Label: t.Name,
						Kind:  "tunnel",
						Up:    t.Running,
					})
				}
			}
			return out
		},
		ObfuscationSources:  ops.ObfuscationSources,
		UsedIPv4:            ops.UsedIPv4,
		SupportsASC3:        ndmsinfo.SupportsWireguardASC3,
		SupportsKernelAWG31: supportsKernelAWG31,
		OnWatchChanged: func() {
			if h.tunnelsHandler != nil {
				h.tunnelsHandler.PublishTunnelList(context.Background())
			}
		},
	}
	if s.bus != nil {
		deps.Bus = s.bus
	}
	s.warpScout = warpscout.New(deps)
	return api.NewWarpScoutHandler(s.warpScout, s.loggingService)
}
