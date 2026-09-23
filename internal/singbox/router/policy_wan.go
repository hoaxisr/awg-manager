package router

import (
	"context"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Выходы целевой политики зависят от режима захвата (F440):
//
//   - policy-tun: весь трафик членов идёт в OpkgTun, прямой выход решает сам
//     sing-box. Разрешённый в политике WAN — второй выход: пока туннель лежит,
//     NDMS уводит трафик мимо sing-box. Без WAN таблица политики пуста и
//     срабатывает соседнее правило `fwmark … blackhole` — kill switch.
//   - tproxy: sing-box перехватывает только TCP/UDP и не всё из них (bypass по
//     портам, подсетям, geoip); остальное NDMS маршрутизирует по таблице
//     политики, и без WAN оно уходит в тот же blackhole.
//
// Проверено на стенде (5.01.C.3.0-1): пакет с меткой политики без permit даёт
// blackhole, а не основную таблицу; метку NDMS выдаёт и без permit.

// policyPermits — интерфейсы из `permit global <iface>` в блоке
// `ip policy <policyName>`, в порядке конфига. Строки `no permit …` в список не
// входят: позиций в NDMS они не занимают (order на политике из одних `no permit`
// принимается только 0).
func policyPermits(lines []string, policyName string) []string {
	var out []string
	inPolicy := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if line == trimmed { // строка без отступа — заголовок блока или его конец
			f := strings.Fields(trimmed)
			inPolicy = len(f) == 3 && f[0] == "ip" && f[1] == "policy" && f[2] == policyName
			continue
		}
		if !inPolicy {
			continue
		}
		f := strings.Fields(trimmed)
		if len(f) >= 3 && f[0] == "permit" && f[1] == "global" {
			out = append(out, f[2])
		}
	}
	return out
}

// wanInterfaces — WAN-интерфейсы роутера; ok=false, если спросить не у кого или
// запрос не удался: «не знаем» не значит «WAN нет», решать по пустому списку
// нельзя.
func (s *ServiceImpl) wanInterfaces(ctx context.Context) ([]WANInterfaceInfo, bool) {
	if s.deps.WANInterfaces == nil {
		return nil, false
	}
	wans, err := s.deps.WANInterfaces.ListWAN(ctx)
	if err != nil {
		s.appLog.Warn("policy-wan", "", "список WAN: "+err.Error())
		return nil, false
	}
	return wans, true
}

// denyPolicyWAN снимает WAN из выходов целевой политики (policy-tun). Прочие
// выходы (например, VPN-туннели пользователя) не трогаются. Запись — только
// при расхождении с running-config: повторный вызов на тике ничего не шлёт.
func (s *ServiceImpl) denyPolicyWAN(ctx context.Context, sr storage.SingboxRouterSettings, lines []string) {
	if sr.PolicyName == "" || s.deps.Policies == nil {
		return
	}
	permits := policyPermits(lines, sr.PolicyName)
	if len(permits) == 0 {
		return
	}
	wans, ok := s.wanInterfaces(ctx)
	if !ok {
		return
	}
	isWAN := make(map[string]bool, len(wans))
	for _, w := range wans {
		isWAN[w.ID] = true
	}
	for _, iface := range permits {
		if !isWAN[iface] {
			continue
		}
		if err := s.deps.Policies.DenyInterface(ctx, sr.PolicyName, iface); err != nil {
			s.appLog.Warn("policy-tun", iface, "снять WAN "+iface+" из политики "+sr.PolicyName+": "+err.Error())
			continue
		}
		s.appLog.Info("policy-tun", iface, "WAN "+iface+" снят из политики "+sr.PolicyName+
			": в policy-tun он был бы обходом туннеля")
	}
}

// ensurePolicyWAN разрешает WAN выходом целевой политики (tproxy), если там нет
// ни одного: берётся поднятый WAN с наибольшим приоритетом NDMS и ставится в
// конец списка — order = число текущих permit, чужую расстановку не двигаем.
func (s *ServiceImpl) ensurePolicyWAN(ctx context.Context, sr storage.SingboxRouterSettings, lines []string) {
	// Без running-config «permit нет» неотличимо от «не прочитали» — повторный
	// permit уже стоящего WAN переставил бы список выходов.
	if sr.PolicyName == "" || s.deps.Policies == nil || lines == nil {
		return
	}
	wans, ok := s.wanInterfaces(ctx)
	if !ok {
		return
	}
	permits := policyPermits(lines, sr.PolicyName)
	var best *WANInterfaceInfo
	for i := range wans {
		w := &wans[i]
		for _, p := range permits {
			if p == w.ID {
				return
			}
		}
		if w.Up && w.ID != "" && (best == nil || w.Priority > best.Priority) {
			best = w
		}
	}
	if best == nil {
		s.appLog.Warn("tproxy", sr.PolicyName, "в политике нет WAN, а поднятого WAN нет — трафик мимо sing-box у членов политики не пройдёт")
		return
	}
	if err := s.deps.Policies.PermitInterface(ctx, sr.PolicyName, best.ID, len(permits)); err != nil {
		s.appLog.Warn("tproxy", best.ID, "разрешить WAN "+best.ID+" в политике "+sr.PolicyName+": "+err.Error())
		return
	}
	s.appLog.Info("tproxy", best.ID, "WAN "+best.ID+" разрешён выходом политики "+sr.PolicyName+
		": через него идёт трафик мимо sing-box (исключения, не TCP/UDP)")
}

// runningConfigLines — running-config для решений о выходах политики; nil,
// если прочитать не удалось (решения тогда пропускаются до следующего тика).
func (s *ServiceImpl) runningConfigLines(ctx context.Context, scope string) []string {
	if s.deps.RunningConfig == nil {
		return nil
	}
	lines, err := s.deps.RunningConfig.Lines(ctx)
	if err != nil {
		s.appLog.Warn(scope, "", "running-config: "+err.Error())
		return nil
	}
	return lines
}
