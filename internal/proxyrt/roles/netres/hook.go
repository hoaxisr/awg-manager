package netres

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// HookScript — netfilter.d-скрипт из ТЕХ ЖЕ групп правил (четвёртый рендер
// Rule). Диспетчер по $table — как wdttNetfilterHookScript.
func HookScript(groups []Group) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# AWG Manager: правила прокси-ролей, переживающие перезапись таблиц NDM.\n")
	b.WriteString("[ \"$type\" = \"ip6tables\" ] && exit 0\n")
	b.WriteString("IPTABLES=/opt/sbin/iptables\n")
	b.WriteString("[ -x \"$IPTABLES\" ] || IPTABLES=iptables\n")
	b.WriteString("run() { \"$IPTABLES\" -w \"$@\" 2>/dev/null || \"$IPTABLES\" \"$@\" 2>/dev/null; }\n")
	b.WriteString("has_if() { /opt/sbin/ip link show \"$1\" >/dev/null 2>&1; }\n")
	byTable := map[string][]Group{}
	for _, g := range groups {
		tables := map[string]bool{}
		for _, r := range g.Rules {
			tables[r.table()] = true
		}
		for t := range tables {
			byTable[t] = append(byTable[t], g)
		}
	}
	b.WriteString("case \"$table\" in\n")
	for _, table := range []string{"filter", "nat", "mangle"} {
		b.WriteString(table + ")\n")
		for _, g := range byTable[table] {
			var rules []Rule
			for _, r := range g.Rules {
				if r.table() == table {
					rules = append(rules, r)
				}
			}
			if len(rules) == 0 {
				continue
			}
			if g.Guard != "" {
				fmt.Fprintf(&b, "if has_if %q; then\n", g.Guard)
			}
			if g.AllOrNone {
				// Пара ставится только когда отсутствуют ОБА правила:
				// довставка половины инвертирует порядок (F3). Частичное
				// состояние чинит Go-reconcile за ruleRecheck.
				var checks []string
				for _, r := range rules {
					checks = append(checks, "! run "+hookQuoteIfaces(strings.Join(r.CheckArgs(), " ")))
				}
				fmt.Fprintf(&b, "  if %s; then\n", strings.Join(checks, " && "))
				for i := len(rules) - 1; i >= 0; i-- {
					fmt.Fprintf(&b, "    run %s\n", hookQuoteIfaces(strings.Join(rules[i].InsertArgs(), " ")))
				}
				b.WriteString("  fi\n")
			} else {
				for _, r := range rules {
					fmt.Fprintf(&b, "  %s\n", r.HookLine())
				}
			}
			if g.Guard != "" {
				b.WriteString("fi\n")
			}
		}
		b.WriteString(";;\n")
	}
	b.WriteString("esac\nexit 0\n")
	return b.String()
}

// сортировка групп по guard — детерминизм содержимого файла.
func sortedGroups(groups []Group) []Group {
	out := append([]Group{}, groups...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Guard < out[j].Guard })
	return out
}

// Hook — ресурс netfilter_hook: файл хука соответствует декларации.
// Группы — тем же провайдером, что у RuleSet: хук это четвёртый рендер ТЕХ ЖЕ
// правил, и желаемое у них общее.
type Hook struct {
	id       proxyrt.ResourceID
	path     string
	runOne   func(ctx context.Context, path, table string) error // прод: sh c table=… type=iptables
	provider GroupProvider
	groups   []Group // результат последнего наблюдения
}

func NewHook(id proxyrt.ResourceID, path string, runOne func(ctx context.Context, path, table string) error) *Hook {
	return &Hook{id: id, path: path, runOne: runOne, provider: StaticGroups(nil)}
}

func (h *Hook) SetDesired(provider GroupProvider) {
	if provider == nil {
		provider = StaticGroups(nil)
	}
	h.provider = provider
}

func (h *Hook) ID() proxyrt.ResourceID { return h.id }

func (h *Hook) Observe(ctx context.Context) (proxyrt.Observation, error) {
	groups, err := h.provider(ctx)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	h.groups = sortedGroups(groups)
	data, rerr := os.ReadFile(h.path)
	if os.IsNotExist(rerr) {
		return proxyrt.Observation{Known: true, Exists: false}, nil
	}
	if rerr != nil {
		return proxyrt.Observation{}, rerr
	}
	same := len(h.groups) > 0 && string(data) == HookScript(h.groups)
	return proxyrt.Observation{Known: true, Exists: true,
		Attrs: map[string]string{"current": boolAttr(same)}}, nil
}

func boolAttr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func (h *Hook) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if len(h.groups) == 0 {
		if obs.Exists {
			return []proxyrt.Step{{Resource: h.id, Op: "remove", Reason: "правил нет — хук не нужен"}}
		}
		return nil
	}
	if obs.Exists && obs.Attrs["current"] == "true" {
		return nil
	}
	return []proxyrt.Step{{Resource: h.id, Op: "write", Reason: "хук отсутствует или устарел"}}
}

func (h *Hook) Apply(ctx context.Context, s proxyrt.Step) error {
	switch s.Op {
	case "remove":
		return os.Remove(h.path)
	case "write":
		if err := storage.AtomicWritePerm(h.path, []byte(HookScript(h.groups)), 0o755); err != nil {
			return err
		}
		// Прогон по трём таблицам — правила встают сразу, не дожидаясь
		// перезаписи таблиц движком ndm (паритет ensureWdttNetfilterHook).
		for _, table := range []string{"filter", "nat", "mangle"} {
			if err := h.runOne(ctx, h.path, table); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
}

func (h *Hook) RecheckAfter() time.Duration { return 0 }
