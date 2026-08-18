// Package linkres — ресурсы связей инстанса с остальной системой: локальный
// порт, endpoint'ы связанных AWG-туннелей, правила «VPN для устройств» и
// публикация в реестре выходов.
package linkres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// Occupancy — занятость локальных портов ВНЕ этого инстанса: другие инстансы
// обеих подсистем + локальные endpoint'ы AWG-туннелей (сегодняшний
// OccupiedLocalListenPorts, freeturn/listen_ports.go:10-13). Адаптер — план 5.
type Occupancy interface {
	OccupiedLocalListenPorts(ctx context.Context) (map[int]bool, error)
}

// ListenPort — сверка пина локального порта. НЕ перевыделяет: единственный
// писатель конфига — handler плана 5; конфликт — приговор с причиной.
type ListenPort struct {
	id     proxyrt.ResourceID
	occ    Occupancy
	listen string
}

func NewListenPort(id proxyrt.ResourceID, occ Occupancy) *ListenPort {
	return &ListenPort{id: id, occ: occ}
}

func (l *ListenPort) SetDesired(listen string) { l.listen = listen }

func (l *ListenPort) ID() proxyrt.ResourceID { return l.id }

func (l *ListenPort) Observe(ctx context.Context) (proxyrt.Observation, error) {
	taken, err := l.occ.OccupiedLocalListenPorts(ctx)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	port, perr := localPort(l.listen)
	attrs := map[string]string{}
	switch {
	case perr != nil:
		attrs["bad"] = perr.Error()
	case port < roles.ListenPortMin || port > roles.ListenPortMax:
		attrs["bad"] = fmt.Sprintf("порт %d вне пула %d..%d", port, roles.ListenPortMin, roles.ListenPortMax)
	case taken[port]:
		attrs["bad"] = fmt.Sprintf("порт %d занят другим инстансом или туннелем", port)
	}
	return proxyrt.Observation{Known: true, Exists: attrs["bad"] == "", Attrs: attrs}, nil
}

func localPort(addr string) (int, error) {
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, err
	}
	if host != "127.0.0.1" {
		return 0, fmt.Errorf("listen %q не локальный", addr)
	}
	return strconv.Atoi(p)
}

func (l *ListenPort) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if bad := obs.Attrs["bad"]; bad != "" {
		return []proxyrt.Step{{Resource: l.id, Op: "fail", Reason: bad}}
	}
	return nil
}

func (l *ListenPort) Apply(_ context.Context, s proxyrt.Step) error {
	if s.Op == "fail" {
		return errors.New(s.Reason)
	}
	return fmt.Errorf("неизвестный шаг %q", s.Op)
}

func (l *ListenPort) RecheckAfter() time.Duration { return 0 }
