package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/ndmsres"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// Локальные узкие интерфейсы — срезы NDMS-клиентов по потребителю; фейк в
// тестах тривиален, *query.InterfaceStore и прочие удовлетворяют как есть.
type ifaceLister interface {
	List(ctx context.Context) ([]ndms.Interface, error)
}
type rcLines interface {
	Lines(ctx context.Context) ([]string, error)
}
type policyLister interface {
	List(ctx context.Context) ([]ndms.Policy, error)
}
type defaultRouteSetter interface {
	SetDefaultRoute(ctx context.Context, name string) error
}
type systemNameResolver interface {
	ResolveSystemName(ctx context.Context, ndmsName string) string
}

// proxyNDMSCommands — ndmsres.Commands поверх InterfaceCommands (методы
// совпадают по имени и сигнатуре — сверено) плюс кандидатура default route из
// RouteCommands. Кандидатура пишется БЕЗ nwg-гарда старого мира: запись —
// кандидатура, не захват (доказано железом, стендовый гейт волны; Р7).
type proxyNDMSCommands struct {
	*ndmscommand.InterfaceCommands
	// routes — узкий интерфейс, а не *ndmscommand.RouteCommands: у
	// RemoveDefaultRoute та же сигнатура, и подмена одного на другое собралась
	// бы молча. Фейк в тесте ловит и метод, и аргумент.
	routes defaultRouteSetter
}

func (c proxyNDMSCommands) EnsureDefaultRouteCandidacy(ctx context.Context, name string) error {
	return c.routes.SetDefaultRoute(ctx, name)
}

var _ ndmsres.Commands = proxyNDMSCommands{}

// proxyNDMSQuery — ndmsres.Query. Факты интерфейса — из списка (отсутствие в
// списке = подтверждённое «нет», без неоднозначной ошибки Get); ip global /
// ACL / кандидатура — из running-config.
type proxyNDMSQuery struct {
	ifaces ifaceLister
	rc     rcLines
}

func (q proxyNDMSQuery) Iface(ctx context.Context, name string) (ndmsres.IfaceFacts, bool, error) {
	list, err := q.ifaces.List(ctx)
	if err != nil {
		return ndmsres.IfaceFacts{}, false, err
	}
	for _, it := range list {
		if it.ID == name {
			return ndmsres.IfaceFacts{
				Description:   it.Description,
				SecurityLevel: it.SecurityLevel,
				Address:       it.Address,
				Mask:          it.Mask,
				MTU:           it.MTU,
				AdminUp:       it.ConfLayer == "running",
			}, true, nil
		}
	}
	return ndmsres.IfaceFacts{}, false, nil
}

func (q proxyNDMSQuery) HasIPGlobal(ctx context.Context, name string) (bool, error) {
	return q.rcHasInterfaceLine(ctx, name, func(l string) bool {
		return strings.HasPrefix(l, "ip global")
	})
}

func (q proxyNDMSQuery) HasPermitAllACL(ctx context.Context, name string) (bool, error) {
	want := "ip access-group _WEBADMIN_" + name + " in"
	return q.rcHasInterfaceLine(ctx, name, func(l string) bool { return l == want })
}

func (q proxyNDMSQuery) HasDefaultRoute(ctx context.Context, name string) (bool, error) {
	lines, err := q.rc.Lines(ctx)
	if err != nil {
		return false, err
	}
	for _, raw := range lines {
		l := strings.TrimSpace(raw)
		// Обе печатные формы записи кандидатуры; фактическую сверить по
		// стендовому running-config — стендовая сверка задачи.
		if l == "ip route default "+name || l == "ip route default interface "+name {
			return true, nil
		}
	}
	return false, nil
}

// rcHasInterfaceLine — строка внутри блока `interface <name>` running-config.
// Блок кончается на следующей строке без отступа. Формы с «no » не совпадают
// по построению (сравнение после TrimSpace с положительной формой) — ловушка
// «запрет печатается как no <форма>» закрыта.
func (q proxyNDMSQuery) rcHasInterfaceLine(ctx context.Context, name string, match func(string) bool) (bool, error) {
	lines, err := q.rc.Lines(ctx)
	if err != nil {
		return false, err
	}
	in := false
	for _, raw := range lines {
		indented := strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t")
		l := strings.TrimSpace(raw)
		if !indented {
			in = l == "interface "+name
			continue
		}
		if in && match(l) {
			return true, nil
		}
	}
	return false, nil
}

var _ ndmsres.Query = proxyNDMSQuery{}

// proxyKernelWAN — NDMS-имя WAN → kernel-имя (internet-only MASQUERADE).
func proxyKernelWAN(ifaces systemNameResolver) func(ctx context.Context, ndmsName string) (string, error) {
	return func(ctx context.Context, ndmsName string) (string, error) {
		sys := ifaces.ResolveSystemName(ctx, ndmsName)
		if strings.TrimSpace(sys) == "" {
			return "", fmt.Errorf("kernel-имя для %s неизвестно", ndmsName)
		}
		return sys, nil
	}
}

// proxyPolicyMark — fwmark политики (raw-половина сервера при policy != none).
func proxyPolicyMark(marks *query.PolicyMarkStore) func(ctx context.Context, policy string) (string, error) {
	return func(ctx context.Context, policy string) (string, error) {
		return marks.Get(ctx, policy)
	}
}

// opkgTunIndex — proxyrt.IndexOf: чистый разбор имени, без ввода-вывода
// (зовётся под локом аллокатора).
func opkgTunIndex(name string) (int, bool) {
	const p = "OpkgTun"
	if !strings.HasPrefix(name, p) {
		return 0, false
	}
	n, err := strconv.Atoi(name[len(p):])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// opkgTunSupported — поддерживает ли прошивка интерфейсы OpkgTun. Источник
// один, osdetect.Is5 (wiring_core.go:87): на 4.x запрос даёт «unsupported
// interface type». Пробы созданием тут нет намеренно (§4.2, запрет
// create-on-reference); на OS5 с исчерпанным пулом индексов остаётся честный
// отказ аллокатора.
func opkgTunSupported() bool { return osdetect.Is5() }

// proxySweepScanner — интерфейсы OpkgTun с нашими метками (по ПРЕФИКСУ
// описания — контракт proxyrt.Scanner). Label — честно прочитанное описание,
// не константа: уборщик перепроверяет метку сам (Sweeper.ours).
type proxySweepScanner struct {
	ifaces ifaceLister
}

func (s proxySweepScanner) Scan(ctx context.Context, labels []string) ([]proxyrt.OwnedResource, error) {
	list, err := s.ifaces.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []proxyrt.OwnedResource
	for _, it := range list {
		if _, ok := opkgTunIndex(it.ID); !ok {
			continue
		}
		for _, l := range labels {
			if strings.HasPrefix(it.Description, l) {
				out = append(out, proxyrt.OwnedResource{Label: it.Description, Name: it.ID})
				break
			}
		}
	}
	return out, nil
}

type proxySweepRemover struct {
	// cmds — интерфейс движка, а не конкретный proxyNDMSCommands: Label и Name
	// оба строковые, снос по описанию вместо имени собрался бы молча.
	cmds ndmsres.Commands
}

func (r proxySweepRemover) Remove(ctx context.Context, res proxyrt.OwnedResource) error {
	return r.cmds.DeleteOpkgTun(ctx, res.Name)
}

// livePermitsFor — LivePermits посева: политики, где ndmsIface разрешён СЕЙЧАС.
func livePermitsFor(policies policyLister) func(ctx context.Context, ndmsIface string) ([]string, error) {
	return func(ctx context.Context, ndmsIface string) ([]string, error) {
		list, err := policies.List(ctx)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, p := range list {
			for _, pi := range p.Interfaces {
				if pi.Name == ndmsIface && !pi.Denied {
					out = append(out, p.Name)
					break
				}
			}
		}
		return out, nil
	}
}
