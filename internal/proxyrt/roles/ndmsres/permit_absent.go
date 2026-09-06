package ndmsres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// PermitAbsent — ресурс «половина не экспонирована»: на интерфейсах `permit`
// не должно быть permit-all `_WEBADMIN_<iface>`, на интерфейсах `global` —
// `ip global`. permit-all обнуляет выбор LAN-сегментов (стенд 2026-09-02:
// привязанный список — permit-исключения, срабатывающие ДО security-level, и
// даёт безусловный ACCEPT всему входящему с интерфейса, обнуляя и
// isolate-private); `ip global` делает половину выходом в политиках/HydraRoute.
// Оба остатка законно ставит policy_exit — но только пока WG-половина
// экспонирована: снят тумблер — policy_exit уходит из ведомости (он аддитивен,
// обратных шагов у него нет), и снимать остатки здесь. Обратная команда
// `interface X no ip global` есть (стенд 5.01, 2026-09-06); уровень `private`
// возвращает Iface.
// Списки РАЗНЫЕ намеренно: permit-all снимается и у выключенного инстанса с
// тумблером (при включении его вернёт policy_exit), а `ip global` — только
// по снятому тумблеру: стенд 2026-09-06 — permit интерфейса в `ip policy`
// переживает `no ip global`, но повторный `ip global` сбрасывает его в deny,
// и цикл выкл/вкл сервера стирал бы раскладку пользователя по политикам.
// Снятие тумблера раскладку теряет по замыслу — пользователь сам вывел
// половину из политик.
// Старые версии ставили permit-all на WG-половину всегда; интерфейс при
// выключении не удаляется, auto-delete не срабатывает — остаток живёт до
// удаления инстанса. Ручная галка веб-морды снимется при ближайшем проходе —
// не немедленно: собственного будильника нет (RecheckAfter() == 0), кэш
// running-config живёт до 60 мин (internal/ndms/query/runningconfig.go:13),
// сбрасывают его наши записи (postMutationChecked → RunningConfig.InvalidateAll,
// internal/ndms/command/*) и хук ndm `iflayerchanged layer=conf`
// (internal/ndms/events/dispatcher.go:197-203); на ручную привязку
// `ip access-group` ndm этого события НЕ даёт (стенд 2026-09-06): галка живёт
// до нашей ближайшей записи, рестарта или TTL кэша.
// Свой InvalidateAll ресурсу не даём — проход периодический, GET на каждый
// проход не нужен; у встроенного сервера strip идёт по действию пользователя
// и кэш сбрасывает (internal/managed/service_acl.go).
//
// Отсутствующий интерфейс — не шаг и не отказ (create-on-reference).
//
// Exists здесь означает «желаемое достигнуто — permit-all нет», а не «объект
// существует», как у соседних ресурсов.
type PermitAbsent struct {
	id     proxyrt.ResourceID
	cmds   Commands
	q      Query
	permit []string
	global []string
}

func NewPermitAbsent(id proxyrt.ResourceID, cmds Commands, q Query) *PermitAbsent {
	return &PermitAbsent{id: id, cmds: cmds, q: q}
}

// SetDesired — интерфейсы без permit-all (`permit`) и без `ip global`
// (`global`); пустые имена пропускаются.
func (r *PermitAbsent) SetDesired(permit, global []string) {
	r.permit, r.global = permit, global
}

func (r *PermitAbsent) ID() proxyrt.ResourceID { return r.id }

// present — имена из list, на которых has(name) истинно; несуществующие
// интерфейсы пропускаются (create-on-reference).
func (r *PermitAbsent) present(ctx context.Context, list []string,
	has func(context.Context, string) (bool, error)) ([]string, error) {
	var out []string
	for _, name := range list {
		if name == "" {
			continue
		}
		_, ok, err := r.q.Iface(ctx, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		h, err := has(ctx, name)
		if err != nil {
			return nil, err
		}
		if h {
			out = append(out, name)
		}
	}
	return out, nil
}

func (r *PermitAbsent) Observe(ctx context.Context) (proxyrt.Observation, error) {
	acl, err := r.present(ctx, r.permit, r.q.HasPermitAllACL)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	global, err := r.present(ctx, r.global, r.q.HasIPGlobal)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	return proxyrt.Observation{Known: true, Exists: len(acl) == 0 && len(global) == 0,
		Detail: detailPermit(acl, global),
		Attrs: map[string]string{"present": strings.Join(acl, ","),
			"global": strings.Join(global, ",")}}, nil
}

func detailPermit(acl, global []string) string {
	var parts []string
	if len(acl) > 0 {
		parts = append(parts, "permit-all стоит: "+strings.Join(acl, ", "))
	}
	if len(global) > 0 {
		parts = append(parts, "ip global стоит: "+strings.Join(global, ", "))
	}
	if len(parts) == 0 {
		return "permit-all и ip global нет"
	}
	return strings.Join(parts, "; ")
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
	for _, name := range strings.Split(obs.Attrs["global"], ",") {
		if name == "" {
			continue
		}
		steps = append(steps, proxyrt.Step{Resource: r.id, Op: "clear-ip-global",
			Args:   map[string]string{"name": name},
			Reason: "половина не экспонирована — ip global делает её выходом в политиках"})
	}
	return steps
}

func (r *PermitAbsent) Apply(ctx context.Context, s proxyrt.Step) error {
	name := s.Args["name"]
	switch s.Op {
	case "remove-acl":
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
	case "clear-ip-global":
		return r.cmds.ClearIPGlobal(ctx, name)
	default:
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
}

func (r *PermitAbsent) RecheckAfter() time.Duration { return 0 }
