package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/clientroute"
	"github.com/hoaxisr/awg-manager/internal/logging"
)

type clientRouteLister interface {
	List() ([]clientroute.ClientRoute, error)
	OnTunnelStart(ctx context.Context, tunnelID, kernelIface string) error
	Reconcile(ctx context.Context, running map[string]string) error
}

// systemClientRoutes — клиентские маршруты на system:-выходах (F497).
// Управляемые туннели переприменяет оркестратор; system:-выходы — никто:
// после ребута правил нет, после down/up интерфейса ядро снимает маршрут.
type systemClientRoutes struct {
	routes clientRouteLister
	kernel func(ctx context.Context, tunnelID string) (string, bool)
	log    *logging.ScopedLogger // nil-safe (logging.ScopedLogger)
}

func (s systemClientRoutes) enabledSystemIDs() map[string]bool {
	list, err := s.routes.List()
	if err != nil {
		s.log.Warn("list", "", fmt.Sprintf("client routes list failed: %v", err))
		return nil
	}
	ids := map[string]bool{}
	for _, r := range list {
		if r.Enabled && strings.HasPrefix(r.TunnelID, "system:") {
			ids[r.TunnelID] = true
		}
	}
	return ids
}

// reconcileAll — при старте демона.
func (s systemClientRoutes) reconcileAll(ctx context.Context) {
	running := map[string]string{}
	for id := range s.enabledSystemIDs() {
		if k, ok := s.kernel(ctx, id); ok {
			running[id] = k
		}
	}
	if len(running) > 0 {
		if err := s.routes.Reconcile(ctx, running); err != nil {
			// Reconcile сегодня всегда возвращает nil; проверка на случай, если контракт изменится.
			s.log.Warn("reconcile", "", fmt.Sprintf("reconcile system client routes failed: %v", err))
		}
	}
}

// reapply — по хуку ipv4 running интерфейса ndmsID.
func (s systemClientRoutes) reapply(ctx context.Context, ndmsID string) {
	id := "system:" + ndmsID
	if !s.enabledSystemIDs()[id] {
		return
	}
	if k, ok := s.kernel(ctx, id); ok {
		if err := s.routes.OnTunnelStart(ctx, id, k); err != nil {
			s.log.Warn("reapply", id, fmt.Sprintf("re-apply client routes on %s failed: %v", k, err))
		}
	}
}

// kernelIfPresent — имя ядра system:-выхода, только если устройство есть:
// GetKernelIface отвечает running=true по одному имени из NDMS, не проверяя
// устройство, и Reconcile на старте писал бы ошибку в журнал на каждом буте.
func (a *app) kernelIfPresent(ctx context.Context, id string) (string, bool) {
	k, ok := a.catalog.GetKernelIface(ctx, id)
	if !ok {
		return "", false
	}
	if _, err := os.Stat("/sys/class/net/" + k); err != nil {
		return "", false
	}
	return k, true
}

func (a *app) systemClientRoutes() systemClientRoutes {
	return systemClientRoutes{
		routes: a.clientRouteService,
		kernel: a.kernelIfPresent,
		log:    logging.NewScopedLogger(a.loggingService, logging.GroupRouting, logging.SubClientRoute),
	}
}

func (a *app) reconcileSystemClientRoutes() {
	ctx, cancel := context.WithTimeout(a.shutdownCtx, 30*time.Second)
	defer cancel()
	a.systemClientRoutes().reconcileAll(ctx)
}
