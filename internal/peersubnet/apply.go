package peersubnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// Router — узкий срез RCI, который нужен исполнителю. Адаптер живёт в
// ndms/command (PeerRouter); фейк — в тестах этого пакета.
type Router interface {
	AddAllowIP(ctx context.Context, iface, pubkey string, n *net.IPNet) error
	RemoveAllowIP(ctx context.Context, iface, pubkey string, n *net.IPNet) error
	// NetworkRouteOwner: есть ли запись на (n, iface) и наша ли (comment целиком).
	NetworkRouteOwner(ctx context.Context, n *net.IPNet, iface, comment string) (exists, own bool, err error)
	AddNetworkRoute(ctx context.Context, n *net.IPNet, iface, comment string) error
	// RemoveOwnNetworkRoute снимает только запись с меткой comment; removed —
	// была ли мутация (откату нужно знать, что возвращать).
	RemoveOwnNetworkRoute(ctx context.Context, n *net.IPNet, iface, comment string) (removed bool, err error)
}

// RollbackError — шаг Apply отказал, и откат сделанного тоже не завершился.
// Unwrap отдаёт причину (её показывают пользователю); Rollback вызывающий пишет
// в журнал приложения. Хранилище не тронуто, а роутер остаётся расходящимся с
// ним: сверки нет, Apply работает по разнице хранилища, поэтому следующее
// сохранение само расхождение не увидит. Лечится вручную (или повторным
// сохранением той же сети либо её удалением и возвратом).
type RollbackError struct{ Cause, Rollback error }

func (e *RollbackError) Error() string {
	return fmt.Sprintf("%v (откат не завершён: %v)", e.Cause, e.Rollback)
}

func (e *RollbackError) Unwrap() error { return e.Cause }

// rollbackTimeout — бюджет отката. Откат идёт на ctx, отвязанном от отмены
// вызывающего: самая вероятная причина сбоя посреди Apply — отключение
// клиента/таймаут запроса, и на том же ctx откат гарантированно не прошёл бы.
const rollbackTimeout = 30 * time.Second

func parseAll(subnets []string) ([]*net.IPNet, error) {
	out := make([]*net.IPNet, 0, len(subnets))
	for _, s := range subnets {
		n, err := parseV4Subnet(s)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// Apply — шаги 3–4 спеки: allow-ips (добавить, снять) → маршруты (добавить
// там, где записи нет; снять свои). Отказ любого шага откатывает сделанное в
// обратном порядке. Какие маршруты наши, не запоминается: владение сверяется
// по метке в момент снятия. Существующая запись на (N, I) — чужая: поверх не
// встаём и своей не считаем (стенд: повтор переписал бы комментарий).
func Apply(ctx context.Context, r Router, iface, pubkey string, added, removed []string) error {
	comment := RouteComment(pubkey)
	addNets, err := parseAll(added)
	if err != nil {
		return err
	}
	rmNets, err := parseAll(removed)
	if err != nil {
		return err
	}
	var allowAdded, allowRemoved, routesAdded, routesRemoved []*net.IPNet
	rollback := func(cause error) error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		var errs []error
		for i := len(routesAdded) - 1; i >= 0; i-- {
			if _, e := r.RemoveOwnNetworkRoute(ctx, routesAdded[i], iface, comment); e != nil {
				errs = append(errs, e)
			}
		}
		for i := len(routesRemoved) - 1; i >= 0; i-- {
			if e := r.AddNetworkRoute(ctx, routesRemoved[i], iface, comment); e != nil {
				errs = append(errs, e)
			}
		}
		for i := len(allowRemoved) - 1; i >= 0; i-- {
			if e := r.AddAllowIP(ctx, iface, pubkey, allowRemoved[i]); e != nil {
				errs = append(errs, e)
			}
		}
		for i := len(allowAdded) - 1; i >= 0; i-- {
			if e := r.RemoveAllowIP(ctx, iface, pubkey, allowAdded[i]); e != nil {
				errs = append(errs, e)
			}
		}
		if len(errs) > 0 {
			return &RollbackError{Cause: cause, Rollback: errors.Join(errs...)}
		}
		return cause
	}
	for _, n := range addNets {
		if err := r.AddAllowIP(ctx, iface, pubkey, n); err != nil {
			return rollback(fmt.Errorf("allow-ips %s: %w", n, err))
		}
		allowAdded = append(allowAdded, n)
	}
	for _, n := range rmNets {
		if err := r.RemoveAllowIP(ctx, iface, pubkey, n); err != nil {
			return rollback(fmt.Errorf("allow-ips %s: %w", n, err))
		}
		allowRemoved = append(allowRemoved, n)
	}
	for _, n := range addNets {
		exists, _, err := r.NetworkRouteOwner(ctx, n, iface, comment)
		if err != nil {
			return rollback(fmt.Errorf("route %s: %w", n, err))
		}
		if exists {
			continue
		}
		if err := r.AddNetworkRoute(ctx, n, iface, comment); err != nil {
			return rollback(fmt.Errorf("route %s: %w", n, err))
		}
		routesAdded = append(routesAdded, n)
	}
	for _, n := range rmNets {
		wasOurs, err := r.RemoveOwnNetworkRoute(ctx, n, iface, comment)
		if err != nil {
			return rollback(fmt.Errorf("route %s: %w", n, err))
		}
		if wasOurs {
			routesRemoved = append(routesRemoved, n)
		}
	}
	return nil
}

// RemoveRoutes снимает маршруты сетей пира, подписанные его меткой (правила
// 3–4). Первый отказ — отказ целиком (fail-closed, 11.B/11.6: маршрут-сирота
// без записи никто уже не снимет). allow-ips не трогаются: вызывающий снимает
// пира целиком.
func RemoveRoutes(ctx context.Context, r Router, iface, pubkey string, subnets []string) error {
	comment := RouteComment(pubkey)
	nets, err := parseAll(subnets)
	if err != nil {
		return err
	}
	for _, n := range nets {
		if _, err := r.RemoveOwnNetworkRoute(ctx, n, iface, comment); err != nil {
			return fmt.Errorf("remove route %s: %w", n, err)
		}
	}
	return nil
}
