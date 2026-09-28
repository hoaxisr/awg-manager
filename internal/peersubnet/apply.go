package peersubnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// ErrPeerNotFound — пира с ключом на интерфейсе нет (свежее чтение). Отдельная
// ошибка, а не пустой список: allow-ips на отсутствующий ключ NDMS принимает,
// СОЗДАВАЯ пира (стенд 5.02.A.11), — сверка на ушедшем пире родила бы призрака.
var ErrPeerNotFound = errors.New("пир не найден на интерфейсе")

// Router — узкий срез RCI, который нужен исполнителю. Адаптер живёт в
// ndms/command (PeerRouter); фейк — в тестах этого пакета.
type Router interface {
	// PeerAllowIPs — allow-ips пира на iface, прочитанные сейчас, мимо кэша.
	// Отказ чтения — ошибка: по устаревшему снимку сверка сняла бы не то.
	// Пира нет — ErrPeerNotFound.
	PeerAllowIPs(ctx context.Context, iface, pubkey string) ([]*net.IPNet, error)
	// InterfaceRoutes — статические маршруты на iface с комментариями,
	// прочитанные сейчас (/show/rc/ip/route, 11.A/11.2).
	InterfaceRoutes(ctx context.Context, iface string) ([]Route, error)
	AddAllowIP(ctx context.Context, iface, pubkey string, n *net.IPNet) error
	RemoveAllowIP(ctx context.Context, iface, pubkey string, n *net.IPNet) error
	// NetworkRouteOwner: есть ли запись на (n, iface) — свежее чтение прямо
	// перед добавлением, чтобы не переписать comment записи, поставленной
	// после снимка (стенд: повторный ip route заменяет comment).
	NetworkRouteOwner(ctx context.Context, n *net.IPNet, iface, comment string) (exists, own bool, err error)
	AddNetworkRoute(ctx context.Context, n *net.IPNet, iface, comment string) error
	// RemoveOwnNetworkRoute снимает только запись с меткой comment; removed —
	// была ли мутация (откату нужно знать, что возвращать).
	RemoveOwnNetworkRoute(ctx context.Context, n *net.IPNet, iface, comment string) (removed bool, err error)
}

// Route — статическая запись маршрута на интерфейсе.
type Route struct {
	Net     *net.IPNet
	Comment string
}

// RollbackError — шаг Reconcile отказал, и откат сделанного тоже не завершился.
// Unwrap отдаёт причину (её показывают пользователю); Rollback вызывающий пишет
// в журнал приложения. Хранилище не тронуто; роутер расходится с ним до
// следующего сохранения: оно сверяется с роутером и расхождение снимет.
type RollbackError struct{ Cause, Rollback error }

func (e *RollbackError) Error() string {
	return fmt.Sprintf("%v (откат не завершён: %v)", e.Cause, e.Rollback)
}

func (e *RollbackError) Unwrap() error { return e.Cause }

// RollbackTimeout — бюджет отката на роутере, общий для правок пира (здесь,
// managed и api). Откат идёт на ctx, отвязанном от отмены вызывающего: самая
// вероятная причина сбоя посреди правки — отключение клиента/таймаут
// запроса, и на том же ctx откат гарантированно не прошёл бы.
const RollbackTimeout = 30 * time.Second

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

// isSubnetScope — сеть из области «сетей за клиентом»: IPv4 не /0 (форма
// RemoteSubnets, parseV4Subnet). Остальные allow-ips пира (IPv6, 0.0.0.0/0,
// поставленные мимо панели) сверка не видит и не трогает.
func isSubnetScope(n *net.IPNet) bool {
	ones, bits := n.Mask.Size()
	return n.IP.To4() != nil && bits == 32 && ones > 0
}

func isHostOf(n *net.IPNet, hosts []net.IP) bool {
	if ones, bits := n.Mask.Size(); ones != bits {
		return false
	}
	for _, h := range hosts {
		if h != nil && h.Equal(n.IP) {
			return true
		}
	}
	return false
}

// Reconcile приводит сети за клиентом пира на роутере к desired — шаги 3–4
// спеки, но «было» читается с роутера, а не из хранилища (F509): расхождение
// после неудачного отката или правки мимо панели следующее сохранение снимает.
//
// Было: allow-ips пира в области сетей за клиентом кроме туннельных адресов
// tunnelHosts (при смене адреса — старый и новый: /32 туннеля сетью не
// бывает) и маршруты на iface с меткой пира целиком. Шаги: allow-ips
// (добавить недостающие, снять лишние) → маршруты (добавить там, где записи
// на (N, iface) нет; снять свои лишние). Существующая запись на (N, iface) —
// не наша: поверх не встаём (стенд: повтор переписал бы комментарий).
// Отказ чтения (и ErrPeerNotFound) — ошибка до единой мутации. Отказ шага откатывает сделанное
// ЭТИМ вызовом в обратном порядке: стоявшее до вызова остаётся.
func Reconcile(ctx context.Context, r Router, iface, pubkey string, tunnelHosts []net.IP, desired []string) error {
	comment := RouteComment(pubkey)
	want, err := parseAll(desired)
	if err != nil {
		return err
	}
	allow, err := r.PeerAllowIPs(ctx, iface, pubkey)
	if err != nil {
		return fmt.Errorf("read peer allow-ips: %w", err)
	}
	routes, err := r.InterfaceRoutes(ctx, iface)
	if err != nil {
		return fmt.Errorf("read routes: %w", err)
	}
	wantSet := make(map[string]bool, len(want))
	for _, n := range want {
		wantSet[n.String()] = true
	}
	haveAllow := map[string]bool{}
	var allowRm []*net.IPNet
	for _, n := range allow {
		if !isSubnetScope(n) || isHostOf(n, tunnelHosts) {
			continue
		}
		haveAllow[n.String()] = true
		if !wantSet[n.String()] {
			allowRm = append(allowRm, n)
		}
	}
	taken := map[string]bool{}
	var routeRm []*net.IPNet
	for _, rt := range routes {
		taken[rt.Net.String()] = true
		if rt.Comment == comment && isSubnetScope(rt.Net) && !isHostOf(rt.Net, tunnelHosts) && !wantSet[rt.Net.String()] {
			routeRm = append(routeRm, rt.Net)
		}
	}
	var allowAdd, routeAdd []*net.IPNet
	for _, n := range want {
		if !haveAllow[n.String()] {
			allowAdd = append(allowAdd, n)
		}
		if !taken[n.String()] {
			routeAdd = append(routeAdd, n)
		}
	}

	var allowAdded, allowRemoved, routesAdded, routesRemoved []*net.IPNet
	rollback := func(cause error) error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), RollbackTimeout)
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
	for _, n := range allowAdd {
		if err := r.AddAllowIP(ctx, iface, pubkey, n); err != nil {
			return rollback(fmt.Errorf("allow-ips %s: %w", n, err))
		}
		allowAdded = append(allowAdded, n)
	}
	for _, n := range allowRm {
		if err := r.RemoveAllowIP(ctx, iface, pubkey, n); err != nil {
			return rollback(fmt.Errorf("allow-ips %s: %w", n, err))
		}
		allowRemoved = append(allowRemoved, n)
	}
	for _, n := range routeAdd {
		// Снимок старше этой секунды: запись, появившуюся после него, не
		// трогаем и своей не считаем (правило 2).
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
	for _, n := range routeRm {
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

// RemoveRoutes снимает все маршруты на iface с меткой пира, найденные на
// роутере (правила 3–4), — не список из хранилища: сирота после RollbackError
// снимается тоже. Отказ чтения или первого снятия — отказ целиком (fail-closed,
// 11.B/11.6: маршрут-сирота без пира никто уже не снимет). allow-ips не
// трогаются: вызывающий снимает пира целиком.
func RemoveRoutes(ctx context.Context, r Router, iface, pubkey string) error {
	comment := RouteComment(pubkey)
	routes, err := r.InterfaceRoutes(ctx, iface)
	if err != nil {
		return fmt.Errorf("read routes: %w", err)
	}
	for _, rt := range routes {
		if rt.Comment != comment {
			continue
		}
		if _, err := r.RemoveOwnNetworkRoute(ctx, rt.Net, iface, comment); err != nil {
			return fmt.Errorf("remove route %s: %w", rt.Net, err)
		}
	}
	return nil
}
