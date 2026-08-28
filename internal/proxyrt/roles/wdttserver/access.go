package wdttserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// AccessApplier — срез wdtt.AccessManager (access.go:13-21): NDMS NAT-режим,
// hotspot policy, LAN-ACL, firewall permit. Прод-реализация существует.
type AccessApplier interface {
	ApplyNATModeToInterface(ctx context.Context, iface, mode string, prevWANs []string) ([]string, error)
	ApplyPolicyToInterface(ctx context.Context, iface, policy string) error
	ApplyLANSegmentsToInterface(ctx context.Context, iface, addr, mask string, segments []string) error
	EnsureInterfaceFirewallPermit(ctx context.Context, iface string) error
}

// NDMSAccess — ресурс-защёлка ndms_access. Честно: наблюдения фактического
// NAT-режима/политики NDMS сегодня нет («состояние с роутера не сверяется» —
// разведка wdtt0-ndms-recon.md §6), поэтому ресурс уведомительный: применяет
// набор при смене отпечатка желаемого. Возвращённый ApplyNATModeToInterface
// WAN уходит в Detail — подхват в конфиг (NatStaticWAN) решает план 5 (В4).
type NDMSAccess struct {
	id       proxyrt.ResourceID
	access   AccessApplier
	iface    string
	mode     string
	prevWANs []string
	policy   string
	addr     string
	mask     string
	lan      []string
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

func (a *NDMSAccess) SetDesired(iface, mode string, prevWANs []string, policy, addr, mask string, lan []string, active bool) {
	a.iface, a.mode, a.prevWANs, a.policy, a.addr, a.mask, a.lan, a.active =
		iface, mode, prevWANs, policy, addr, mask, lan, active
}

func (a *NDMSAccess) fingerprint() string {
	return strings.Join(append([]string{a.iface, a.mode, a.policy, a.addr, a.mask}, a.lan...), "|")
}

func (a *NDMSAccess) ID() proxyrt.ResourceID { return a.id }

func (a *NDMSAccess) Observe(context.Context) (proxyrt.Observation, error) {
	if !a.active {
		return proxyrt.Observation{Known: true, Exists: true, Detail: "выключен — не доводится"}, nil
	}
	return proxyrt.Observation{Known: true, Exists: a.applied == a.fingerprint(),
		Detail: a.detail}, nil
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
	if err := a.access.ApplyPolicyToInterface(ctx, a.iface, a.policy); err != nil {
		return fmt.Errorf("policy %s: %w", a.policy, err)
	}
	if err := a.access.ApplyLANSegmentsToInterface(ctx, a.iface, a.addr, a.mask, a.lan); err != nil {
		return fmt.Errorf("LAN ACL: %w", err)
	}
	if a.mode != "none" {
		if err := a.access.EnsureInterfaceFirewallPermit(ctx, a.iface); err != nil {
			return fmt.Errorf("firewall permit: %w", err)
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
