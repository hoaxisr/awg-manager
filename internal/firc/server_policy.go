package firc

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

const devicePolicyPrefix = "policy:"

// RouterPolicy — политика доступа NDMS.
type RouterPolicy struct {
	Name        string
	Description string
}

// WDTTServerPolicy — политика WDTT-сервера и NDMS-имена его интерфейсов.
type WDTTServerPolicy struct {
	Policy     string
	Interfaces []string
}

// RouterPolicies — политики роутера и их состав: по ним решается, можно ли
// снять с группы исключение политики включённого сервера.
type RouterPolicies struct {
	Policies   []RouterPolicy    // в порядке /show/rc/ip/policy, как их читает fircd
	Interfaces map[string]string // NDMS-интерфейс → политика (`ip hotspot`)
	Hosts      map[string]string // MAC → политика (`ip hotspot`)
	WDTT       []WDTTServerPolicy
}

// SetRouterPoliciesProvider задаёт источник политик роутера; false — данных нет.
func (op *Operator) SetRouterPoliciesProvider(fn func(ctx context.Context) (RouterPolicies, bool)) {
	op.mu.Lock()
	defer op.mu.Unlock()
	op.routerPoliciesProvider = fn
}

// routerPoliciesFor спрашивает роутер, только когда есть что решать: в исключениях
// групп названа политика и включён хоть один сервер.
func (op *Operator) routerPoliciesFor(ctx context.Context, groups []Group, sr ServerRoutingConfig) *RouterPolicies {
	if op == nil || len(normalizeServerRouting(sr).EnabledServers) == 0 {
		return nil
	}
	if !slices.ContainsFunc(groups, func(g Group) bool {
		return slices.ContainsFunc(g.Devices.Deny, func(e string) bool { return strings.HasPrefix(e, devicePolicyPrefix) })
	}) {
		return nil
	}
	op.mu.Lock()
	fn := op.routerPoliciesProvider
	op.mu.Unlock()
	if fn == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rp, ok := fn(ctx)
	if !ok {
		return nil
	}
	return &rp
}

// policyName — внутреннее имя политики, как internal_name в fircd: первая с таким
// описанием (без описания им служит имя), иначе сам текст.
func (rp *RouterPolicies) policyName(text string) string {
	for _, p := range rp.Policies {
		desc := p.Description
		if desc == "" {
			desc = p.Name
		}
		if desc == text {
			return p.Name
		}
	}
	return text
}

// othersIn — кто, кроме перечисленных интерфейсов, состоит в политике.
func (rp *RouterPolicies) othersIn(policy string, serverIfaces []string) []string {
	var out []string
	for iface, p := range rp.Interfaces {
		if p == policy && !slices.Contains(serverIfaces, iface) {
			out = append(out, iface)
		}
	}
	for mac, p := range rp.Hosts {
		if p == policy {
			out = append(out, mac)
		}
	}
	slices.Sort(out)
	return out
}

// serverDenyRules — какие записи Devices.Deny мешают клиентам включённых серверов.
type serverDenyRules struct {
	subnets  map[string]bool
	router   *RouterPolicies
	policies map[string][]string // политика сервера → кто ещё в ней состоит
}

func newServerDenyRules(sr ServerRoutingConfig, subnets []string, wgServers []DynamicWGServer, rp *RouterPolicies) serverDenyRules {
	r := serverDenyRules{subnets: map[string]bool{}, router: rp, policies: map[string][]string{}}
	for _, s := range subnets {
		r.subnets[strings.ToLower(strings.TrimSpace(s))] = true
	}
	if rp == nil {
		return r
	}
	var names, ifaces []string
	for _, srv := range matchEnabledWGServers(sr, wgServers) {
		names = append(names, srv.Policy)
		ifaces = append(ifaces, srv.NDMSName)
	}
	if sr.IsServerEnabled("wdtt_server") {
		for _, srv := range rp.WDTT {
			names = append(names, srv.Policy)
			ifaces = append(ifaces, srv.Interfaces...)
		}
	}
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" && name != "none" {
			r.policies[name] = rp.othersIn(name, ifaces)
		}
	}
	return r
}

// verdict: снять запись, либо оставить и назвать, кого она ещё исключает.
//
// Запрет в fircd сильнее разрешения и исключений не имеет (firc_devsel_allows), а
// policy:X охватывает всех её клиентов. Вырезать из неё подсеть сервера нельзя,
// поэтому политика с другими участниками остаётся: снятие открыло бы группу и им.
func (r serverDenyRules) verdict(entry string) (strip bool, policy string, others []string) {
	entry = strings.TrimSpace(entry)
	if r.subnets[strings.ToLower(entry)] {
		return true, "", nil
	}
	text, ok := strings.CutPrefix(entry, devicePolicyPrefix)
	if !ok || r.router == nil {
		return false, "", nil
	}
	policy = r.router.policyName(text)
	others, server := r.policies[policy]
	if !server {
		return false, "", nil
	}
	return len(others) == 0, policy, others
}

// warnKeptServerDenies пишет в журнал об исключениях, оставленных вопреки включённому
// серверу; повторно — только при смене состава.
func (op *Operator) warnKeptServerDenies(groups []Group, rules serverDenyRules) {
	var lines []string
	for _, g := range groups {
		for _, entry := range g.Devices.Deny {
			if strip, policy, others := rules.verdict(entry); !strip && policy != "" {
				lines = append(lines, fmt.Sprintf("Группа «%s»: исключение политики %s оставлено — кроме VPN-сервера в ней состоят %s. Клиенты сервера из этой политики в группу не попадают",
					g.Name, policy, strings.Join(others, ", ")))
			}
		}
	}
	op.mu.Lock()
	same := slices.Equal(op.lastKeptDenies, lines)
	op.lastKeptDenies = lines
	op.mu.Unlock()
	if same {
		return
	}
	for _, line := range lines {
		op.appLog.Warn("sync-groups", "firc", line)
	}
}
