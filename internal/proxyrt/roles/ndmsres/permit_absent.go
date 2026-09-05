package ndmsres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// PermitAbsent — ресурс permit_absent: на перечисленных NDMS-интерфейсах НЕТ
// permit-all ACL `_WEBADMIN_<iface>`.
//
// Стенд 2026-09-02 (5.01, OpkgTun10): привязанный список — permit-исключения,
// срабатывающие ДО security-level; permit-all даёт безусловный ACCEPT всему
// входящему с интерфейса и обнуляет выбор LAN-сегментов и isolate-private.
// Старые версии ставили такой список на WG-половину сервера всегда; интерфейс
// при выключении не удаляется, auto-delete не срабатывает — остаток живёт до
// удаления инстанса. Этот ресурс — миграция: желаемое «permit-all нет»
// доводится каждым проходом. Галка, поставленная руками в веб-морде,
// снимется при ближайшем проходе, вызванном нашей записью в NDMS или
// событием — не немедленно: кэш running-config (TTL 60 мин,
// internal/ndms/query/runningconfig.go:13) инвалидируется только нашими
// записями, а RecheckAfter() у ресурса равен 0 — собственного будильника нет.
// Там, где permit-all — часть замысла (policy_exit при ExposeToPolicies),
// роль этот интерфейс сюда не включает.
//
// Отсутствующий интерфейс — не шаг и не отказ (create-on-reference).
//
// Exists здесь означает «желаемое достигнуто — permit-all нет», а не «объект
// существует», как у соседних ресурсов.
type PermitAbsent struct {
	id    proxyrt.ResourceID
	cmds  Commands
	q     Query
	names []string
}

func NewPermitAbsent(id proxyrt.ResourceID, cmds Commands, q Query) *PermitAbsent {
	return &PermitAbsent{id: id, cmds: cmds, q: q}
}

// SetDesired — интерфейсы, на которых permit-all быть не должно. Пустые имена
// пропускаются.
func (r *PermitAbsent) SetDesired(names []string) { r.names = names }

func (r *PermitAbsent) ID() proxyrt.ResourceID { return r.id }

func (r *PermitAbsent) Observe(ctx context.Context) (proxyrt.Observation, error) {
	var present []string
	for _, name := range r.names {
		if name == "" {
			continue
		}
		_, ok, err := r.q.Iface(ctx, name)
		if err != nil {
			return proxyrt.Observation{}, err
		}
		if !ok {
			continue
		}
		has, err := r.q.HasPermitAllACL(ctx, name)
		if err != nil {
			return proxyrt.Observation{}, err
		}
		if has {
			present = append(present, name)
		}
	}
	return proxyrt.Observation{Known: true, Exists: len(present) == 0,
		Detail: detailPermit(present),
		Attrs:  map[string]string{"present": strings.Join(present, ",")}}, nil
}

func detailPermit(present []string) string {
	if len(present) == 0 {
		return "permit-all нет"
	}
	return "permit-all стоит: " + strings.Join(present, ", ")
}

func (r *PermitAbsent) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if obs.Exists {
		return nil
	}
	var steps []proxyrt.Step
	for _, name := range strings.Split(obs.Attrs["present"], ",") {
		if name == "" {
			continue
		}
		steps = append(steps, proxyrt.Step{Resource: r.id, Op: "remove-acl",
			Args:   map[string]string{"name": name},
			Reason: "permit-all ACL обнуляет выбор LAN-сегментов"})
	}
	return steps
}

func (r *PermitAbsent) Apply(ctx context.Context, s proxyrt.Step) error {
	if s.Op != "remove-acl" {
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
	name := s.Args["name"]
	if err := r.cmds.RemovePermitAllACL(ctx, name); err != nil {
		// Снятие best-effort по замыслу команды (acl.go: auto-delete мог уже
		// каскадировать список после unbind). Если привязки больше нет —
		// цель достигнута, отказ второй половины команды не считается.
		if has, qerr := r.q.HasPermitAllACL(ctx, name); qerr == nil && !has {
			return nil
		}
		return fmt.Errorf("снять permit-all с %s: %w", name, err)
	}
	return nil
}

func (r *PermitAbsent) RecheckAfter() time.Duration { return 0 }
