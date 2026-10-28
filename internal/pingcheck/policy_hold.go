package pingcheck

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// policyPermits — политики NDMS, нужные для ухода с больного туннеля (F743).
// Список — всегда свежий, не из кэша: HR Neo и веб-интерфейс роутера правят
// политики мимо нас, а кэш живёт час (стенд 27.10: снятие молча не нашло
// туннель в политике, созданной после старта демона).
type policyPermits interface {
	FreshPolicies(ctx context.Context) ([]ndms.Policy, error)
	PermitInterface(ctx context.Context, name, iface string, order int) error
	DenyInterface(ctx context.Context, name, iface string) error
}

// policyHoldTimeout — потолок RCI-похода на снятие/возврат вне s.ctx
// (StopMonitoring и Stop зовут возврат, когда s.ctx уже может быть отменён).
const policyHoldTimeout = 15 * time.Second

// SetPolicies подключает политики NDMS. Без них монитор только лечит
// интерфейс, политику с туннеля не уводит.
func (s *Service) SetPolicies(p policyPermits) { s.policies = p }

// releasePolicies снимает permit туннеля во всех политиках, где он разрешён,
// и запоминает позиции для возврата. Политика NDMS сразу переходит на
// следующий интерфейс, а сам туннель остаётся поднятым — проверки через него
// продолжаются, и permit вернётся на первой успешной (restorePolicies).
// Прежний способ — держать link down — не давал проверять, а toggle
// поднимал интерфейс сразу, и политика с туннеля не уходила (F743, стенд 27.10).
func (s *Service) releasePolicies(tunnelID string) {
	if s.policies == nil {
		return
	}
	iface := tunnel.NewNames(tunnelID).NDMSName

	ctx, cancel := context.WithTimeout(context.Background(), policyHoldTimeout)
	defer cancel()
	policies, err := s.policies.FreshPolicies(ctx)
	if err != nil {
		s.logWarn(tunnelID, "policy release: "+err.Error())
		return
	}
	// Снимаем по живому списку, а не по записи: непустая запись не значит
	// «снято» — возврат мог пройти частично, а deny прошлого порога упасть.
	var live []storage.PolicyHold
	for _, p := range policies {
		for _, pi := range p.Interfaces {
			if pi.Name == iface && !pi.Denied {
				live = append(live, storage.PolicyHold{Policy: p.Name, Order: pi.Order})
			}
		}
	}
	if len(live) == 0 {
		return
	}

	// Запись раньше deny: упади демон между ними — вернём permit, который не
	// снимали (идемпотентно), а не потеряем снятый. Сохранение конфигурации
	// роутера идёт с каждой правкой политики, так что без этой записи снятый
	// permit пережил бы и перезагрузку.
	if err := s.tunnels.Update(tunnelID, func(t *storage.AWGTunnel) error {
		t.PolicyHolds = mergeHolds(t.PolicyHolds, live)
		return nil
	}); err != nil {
		s.logWarn(tunnelID, "policy release: "+err.Error())
		return
	}
	denied := false
	for _, h := range live {
		if err := s.policies.DenyInterface(ctx, h.Policy, iface); err != nil {
			s.logWarn(tunnelID, fmt.Sprintf("policy release %s: %v", h.Policy, err))
			continue
		}
		denied = true
		s.logInfo(tunnelID, fmt.Sprintf("Policy %s: %s removed until connectivity returns", h.Policy, iface))
	}
	if denied {
		s.publishPolicies()
	}
}

// mergeHolds — снятые сейчас политики с их нынешней позицией плюс прежние
// записи о политиках, где туннель уже не разрешён (ещё не вернулся).
func mergeHolds(held, live []storage.PolicyHold) []storage.PolicyHold {
	out := append([]storage.PolicyHold(nil), live...)
	for _, h := range held {
		if !slices.ContainsFunc(live, func(l storage.PolicyHold) bool { return l.Policy == h.Policy }) {
			out = append(out, h)
		}
	}
	return out
}

// restorePolicies возвращает снятые permit'ы на запомненные позиции.
// Не вернувшиеся остаются в записи до следующей попытки.
func (s *Service) restorePolicies(tunnelID string) {
	if s.policies == nil {
		return
	}
	stored, err := s.tunnels.Get(tunnelID)
	if err != nil || len(stored.PolicyHolds) == 0 {
		return
	}
	iface := tunnel.NewNames(tunnelID).NDMSName

	ctx, cancel := context.WithTimeout(context.Background(), policyHoldTimeout)
	defer cancel()
	policies, err := s.policies.FreshPolicies(ctx)
	if err != nil {
		s.logWarn(tunnelID, "policy restore: "+err.Error())
		return
	}
	current := make(map[string]ndms.Policy, len(policies))
	for _, p := range policies {
		current[p.Name] = p
	}

	var left []storage.PolicyHold
	for _, h := range stored.PolicyHolds {
		p, ok := current[h.Policy]
		if !ok {
			continue // политику удалили — возвращать некуда
		}
		if permitted(p, iface) {
			continue // вернули руками
		}
		// Пока permit был снят, список могли сократить: позиция за концом.
		order := min(h.Order, len(p.Interfaces))
		if err := s.policies.PermitInterface(ctx, h.Policy, iface, order); err != nil {
			s.logWarn(tunnelID, fmt.Sprintf("policy restore %s: %v", h.Policy, err))
			left = append(left, h)
			continue
		}
		s.logInfo(tunnelID, fmt.Sprintf("Policy %s: %s restored at position %d", h.Policy, iface, order))
	}

	if len(left) == len(stored.PolicyHolds) {
		return // ни один не вернулся — запись прежняя
	}
	if err := s.tunnels.Update(tunnelID, func(t *storage.AWGTunnel) error {
		t.PolicyHolds = left
		return nil
	}); err != nil {
		s.logWarn(tunnelID, "policy restore: "+err.Error())
	}
	s.publishPolicies()
}

// restoreAllPolicies возвращает permit'ы всех туннелей — при глобальном
// выключении мониторинга: монитора, который вернул бы их сам, больше не будет.
func (s *Service) restoreAllPolicies() {
	if s.policies == nil {
		return
	}
	tunnels, err := s.tunnels.List()
	if err != nil {
		return
	}
	for _, t := range tunnels {
		if len(t.PolicyHolds) > 0 {
			s.restorePolicies(t.ID)
		}
	}
}

// holdNames — имена политик удержаний для статуса (F748).
func holdNames(holds []storage.PolicyHold) []string {
	var out []string
	for _, h := range holds {
		out = append(out, h.Policy)
	}
	return out
}

// permitted — интерфейс в политике разрешён (не снят и не запрещён).
func permitted(p ndms.Policy, iface string) bool {
	for _, pi := range p.Interfaces {
		if pi.Name == iface && !pi.Denied {
			return true
		}
	}
	return false
}

func (s *Service) publishPolicies() {
	if s.bus != nil {
		s.bus.PublishInvalidated(events.ResourceRoutingAccessPolicies, "pingcheck")
	}
}
