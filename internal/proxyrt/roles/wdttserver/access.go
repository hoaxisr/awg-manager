package wdttserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// AccessApplier — NDMS-настройки доступа сервера: режим NAT, hotspot policy,
// LAN-ACL.
//
// Разрешающего ACL здесь больше нет. Стенд 2026-09-02 (5.01, OpkgTun10):
// привязанный список — это permit-исключения, срабатывающие ДО security-level,
// а permit-all даёт безусловный ACCEPT всему входящему с интерфейса. То есть
// он обнулял и выбор LAN-сегментов, и isolate-private. Инвариант: доступ
// абонента в LAN определяется выбранными сегментами и ничем больше. Там, где
// разрешение — часть замысла (публичный выход, ExposeToPolicies), его ставит
// ресурс policy_exit. Снятие остатка `_WEBADMIN_<iface>` прошлых версий —
// ресурс `permit_absent` (`ndmsres.PermitAbsent`).
type AccessApplier interface {
	ApplyNATModeToInterface(ctx context.Context, iface, mode string, prevWANs []string) ([]string, error)
	ApplyPolicyToInterface(ctx context.Context, iface, policy string) error
	ApplyLANSegmentsToInterface(ctx context.Context, iface, addr, mask string, segments []string) error
	// ForeignAccessGroups — чужие списки, привязанные к интерфейсу строками
	// `ip access-group … in`: наблюдение для показа пользователю, ничего не
	// меняет.
	ForeignAccessGroups(ctx context.Context, iface string) ([]string, error)
}

// NDMSAccess — ресурс-защёлка ndms_access. Честно: наблюдения фактического
// NAT-режима/политики NDMS сегодня нет («состояние с роутера не сверяется» —
// разведка wdtt0-ndms-recon.md §6), поэтому ресурс уведомительный: применяет
// набор при смене отпечатка желаемого. Возвращённый ApplyNATModeToInterface
// WAN уходит в Detail — подхват в конфиг (NatStaticWAN) решает план 5 (В4).
type NDMSAccess struct {
	id     proxyrt.ResourceID
	access AccessApplier
	iface  string
	// rawIface — NDMS-имя ВТОРОЙ половины сервера. Политика применяется к
	// обеим: сервер один, и абонент не должен маршрутизироваться по-разному в
	// зависимости от того, каким портом подключился. Прежде вторая половина
	// метилась своей парой mangle-правил, хотя NDMS на привязку политики
	// ставит ровно такую же (стенд 2026-09-02: MARK + CONNMARK --save-mark на
	// `-i opkgtunN`) — одна реализация вместо двух.
	//
	// LAN-ACL применяется к ОБЕИМ половинам, каждой со своей peer-сетью:
	// выбор сегментов — свойство сервера, а не порта, которым подключился
	// абонент. Прежде список стоял только на первой половине, и raw-абонент
	// был ограничен одним security-level.
	//
	// Режим NAT остаётся на первой половине: raw-абонентам SNAT делает своя
	// группа MASQUERADE по peer-сети (netres.MasqGroups, role.go), а не
	// NDMS-режим интерфейса.
	rawIface string
	mode     string
	prevWANs []string
	policy   string
	addr     string
	mask     string
	// rawAddr, rawMask — шлюз и маска raw-половины: адрес её peer-сети, от
	// которого строится ACL второй половины.
	rawAddr string
	rawMask string
	lan     []string
	// active=false (disabled) — «не трогать»: старый код звал
	// applyServerAccess только на старте; доводка NAT/policy/LAN по
	// интерфейсу выключенного сервера — тот же класс create-on-reference
	// (I-3 ревью-2, симметрия с M9/IngressRefs).
	active  bool
	applied string // отпечаток применённого желаемого
	detail  string
}

func NewNDMSAccess(id proxyrt.ResourceID, access AccessApplier) *NDMSAccess {
	return &NDMSAccess{id: id, access: access}
}

func (a *NDMSAccess) SetDesired(iface, rawIface, mode string, prevWANs []string, policy, addr, mask, rawAddr, rawMask string, lan []string, active bool) {
	a.iface, a.rawIface, a.mode, a.prevWANs, a.policy = iface, rawIface, mode, prevWANs, policy
	a.addr, a.mask, a.rawAddr, a.rawMask, a.lan, a.active = addr, mask, rawAddr, rawMask, lan, active
}

func (a *NDMSAccess) fingerprint() string {
	return strings.Join(append([]string{a.iface, a.rawIface, a.mode, a.policy,
		a.addr, a.mask, a.rawAddr, a.rawMask}, a.lan...), "|")
}

func (a *NDMSAccess) ID() proxyrt.ResourceID { return a.id }

func (a *NDMSAccess) Observe(ctx context.Context) (proxyrt.Observation, error) {
	if !a.active {
		return proxyrt.Observation{Known: true, Exists: true, Detail: "выключен — не доводится"}, nil
	}
	// Чужие привязки ACL обеих половин — наблюдение для показа: они
	// срабатывают ДО security-level и способны обнулить выбор сегментов, а
	// ставит их не панель. На готовность ресурса не влияют, ошибка чтения —
	// без ключа, а не отказ наблюдения.
	var public map[string]string
	var foreign []string
	for _, iface := range []string{a.iface, a.rawIface} {
		if iface == "" {
			continue
		}
		names, err := a.access.ForeignAccessGroups(ctx, iface)
		if err != nil {
			continue
		}
		for _, n := range names {
			if n != "_WEBADMIN_"+iface { // этот снимает permit_absent — не «чужой»
				foreign = append(foreign, iface+":"+n)
			}
		}
	}
	if len(foreign) > 0 {
		public = map[string]string{"foreign-acl": strings.Join(foreign, ",")}
	}
	return proxyrt.Observation{Known: true, Exists: a.applied == a.fingerprint(),
		Detail: a.detail, Public: public}, nil
}

func (a *NDMSAccess) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if obs.Exists {
		return nil
	}
	return []proxyrt.Step{{Resource: a.id, Op: "apply",
		Reason: "настройки доступа NDMS не применены к этому желаемому"}}
}

func (a *NDMSAccess) Apply(ctx context.Context, s proxyrt.Step) error {
	if s.Op != "apply" {
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
	wans, err := a.access.ApplyNATModeToInterface(ctx, a.iface, a.mode, a.prevWANs)
	if err != nil {
		return fmt.Errorf("NDMS NAT %s: %w", a.mode, err)
	}
	// Политика — на ОБЕ половины: одна принадлежность на один сервер.
	for _, iface := range []string{a.iface, a.rawIface} {
		if iface == "" {
			continue
		}
		if err := a.access.ApplyPolicyToInterface(ctx, iface, a.policy); err != nil {
			return fmt.Errorf("policy %s на %s: %w", a.policy, iface, err)
		}
	}
	// LAN-ACL — на ОБЕ половины, каждой со своей peer-сетью: выбранные
	// сегменты и есть весь доступ абонента в LAN, независимо от того, каким
	// портом он подключился.
	if err := a.access.ApplyLANSegmentsToInterface(ctx, a.iface, a.addr, a.mask, a.lan); err != nil {
		return fmt.Errorf("LAN ACL: %w", err)
	}
	if a.rawIface != "" {
		if err := a.access.ApplyLANSegmentsToInterface(ctx, a.rawIface, a.rawAddr, a.rawMask, a.lan); err != nil {
			return fmt.Errorf("LAN ACL (raw): %w", err)
		}
	}
	a.applied = a.fingerprint()
	a.detail = "применено; WAN=" + strings.Join(wans, ",")
	return nil
}

func (a *NDMSAccess) RecheckAfter() time.Duration { return 0 }

// IngressEnsurer — срез wdtt.IngressRefEnsurer (server_access_raw.go:19-21):
// пара kernel-интерфейсов сервера в ingress-refs sing-box.
type IngressEnsurer interface {
	EnsureWdttServerIngressRefs(ctx context.Context, wgKernelIface, rawKernelIface string) error
}

// IngressRefs — ресурс-защёлка ingress_refs (та же природа, что NDMSAccess).
// active=false (disabled) — «не трогать»: старый код звал ensure только на
// старте, refs выключенного сервера не доводятся и не снимаются (снятие —
// только при delete, вне ресурсной модели) (M9 ревью).
type IngressRefs struct {
	id      proxyrt.ResourceID
	ing     IngressEnsurer
	wg, raw string
	active  bool
	applied string
}

func NewIngressRefs(id proxyrt.ResourceID, ing IngressEnsurer) *IngressRefs {
	return &IngressRefs{id: id, ing: ing}
}

func (i *IngressRefs) SetDesired(wg, raw string, active bool) {
	i.wg, i.raw, i.active = wg, raw, active
}

func (i *IngressRefs) ID() proxyrt.ResourceID { return i.id }

func (i *IngressRefs) Observe(context.Context) (proxyrt.Observation, error) {
	if !i.active {
		return proxyrt.Observation{Known: true, Exists: true, Detail: "выключен — не доводится"}, nil
	}
	return proxyrt.Observation{Known: true, Exists: i.applied == i.wg+"|"+i.raw}, nil
}

func (i *IngressRefs) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if obs.Exists {
		return nil
	}
	return []proxyrt.Step{{Resource: i.id, Op: "ensure", Reason: "ingress-refs не доведены"}}
}

func (i *IngressRefs) Apply(ctx context.Context, s proxyrt.Step) error {
	if s.Op != "ensure" {
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
	if err := i.ing.EnsureWdttServerIngressRefs(ctx, i.wg, i.raw); err != nil {
		return err
	}
	i.applied = i.wg + "|" + i.raw
	return nil
}

func (i *IngressRefs) RecheckAfter() time.Duration { return 0 }
