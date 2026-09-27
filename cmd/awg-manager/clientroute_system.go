package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/clientroute"
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
}

func (s systemClientRoutes) enabledSystemIDs() map[string]bool {
	list, err := s.routes.List()
	if err != nil {
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
		_ = s.routes.Reconcile(ctx, running) // ошибки пишет сам Reconcile
	}
}

// reapply — по хуку ipv4 running интерфейса ndmsID.
func (s systemClientRoutes) reapply(ctx context.Context, ndmsID string) {
	id := "system:" + ndmsID
	if !s.enabledSystemIDs()[id] {
		return
	}
	if k, ok := s.kernel(ctx, id); ok {
		_ = s.routes.OnTunnelStart(ctx, id, k)
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
	return systemClientRoutes{routes: a.clientRouteService, kernel: a.kernelIfPresent}
}

func (a *app) reconcileSystemClientRoutes() {
	ctx, cancel := context.WithTimeout(a.shutdownCtx, 30*time.Second)
	defer cancel()
	a.systemClientRoutes().reconcileAll(ctx)
}
