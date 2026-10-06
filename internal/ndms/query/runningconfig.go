package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

const runningConfigTTL = 60 * time.Minute

type RunningConfigStore struct {
	*cache.ListStore[[]string]
	getter Getter
}

func NewRunningConfigStore(g Getter, log Logger) *RunningConfigStore {
	return NewRunningConfigStoreWithTTL(g, log, runningConfigTTL)
}

func NewRunningConfigStoreWithTTL(g Getter, log Logger, ttl time.Duration) *RunningConfigStore {
	s := &RunningConfigStore{getter: g}
	s.ListStore = cache.NewListStore(ttl, log, "running-config", s.fetch)
	return s
}

// Lines returns the cached /show/running-config message lines. Thin
// alias over the promoted ListStore.List — callers use "lines" in the
// running-config domain rather than the generic "list".
func (s *RunningConfigStore) Lines(ctx context.Context) ([]string, error) {
	return s.ListStore.List(ctx)
}

// GlobalEgressInterfaces возвращает NDMS-имена интерфейсов, в блоке которых
// есть `ip global` (кандидаты-выходы глобального роутинга), в порядке
// появления в running-config. Формат блочный: заголовок `interface <Name>`
// без отступа, тело с отступом; одного признака на блок достаточно.
func (s *RunningConfigStore) GlobalEgressInterfaces(ctx context.Context) ([]string, error) {
	lines, err := s.Lines(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	current := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if line == trimmed { // без отступа — заголовок блока или его конец
			f := strings.Fields(trimmed)
			current = ""
			if len(f) == 2 && f[0] == "interface" {
				current = f[1]
			}
			continue
		}
		if current == "" {
			continue
		}
		if trimmed == "ip global" || strings.HasPrefix(trimmed, "ip global ") {
			out = append(out, current)
			current = ""
		}
	}
	return out, nil
}

// aclFamily — пространство списков NDMS: у IPv6 своё (`ipv6 access-list`,
// `ipv6 access-group`), имена с v4 не пересекаются. Тип неэкспортирован, значения —
// только ACLv4/ACLv6: опечатка семейства у вызывающего не компилируется.
type aclFamily uint8

const (
	ACLv4 aclFamily = iota + 1
	ACLv6
)

// keyword — первое слово строки привязки: `ip access-group` / `ipv6 access-group`.
func (f aclFamily) keyword() string {
	if f == ACLv6 {
		return "ipv6"
	}
	return "ip"
}

// InterfaceAccessGroupsOf — имена списков, привязанных к интерфейсу строками
// `<ip|ipv6> access-group <name> in` внутри блока `interface <iface>`, в порядке
// появления — это порядок привязки и порядок джампов в _NDM_ACL_IN (стенд
// 5.01, 2026-09-05). Семейство выбирает пространство: v6-привязка печатается
// `ipv6 access-group <name> in` (стенд 5.01.C.6, Task 59 П5). Форма
// `no ip access-group …` не совпадает по построению: сравнение идёт с начала
// строки после TrimSpace. Разбор по готовым строкам — для вызывающих без стора
// (адаптеры cmd).
func InterfaceAccessGroupsOf(lines []string, iface string, family aclFamily) []string {
	out := []string{}
	in := false
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if raw == trimmed { // без отступа — заголовок блока или его конец
			in = trimmed == "interface "+iface
			continue
		}
		if !in {
			continue
		}
		f := strings.Fields(trimmed)
		if len(f) == 4 && f[0] == family.keyword() && f[1] == "access-group" && f[3] == "in" {
			out = append(out, f[2])
		}
	}
	return out
}

// ACLRulesOf — правила списка доступа из running-config: строки `permit …`
// и `deny …` его блока. header — полная строка заголовка: `access-list <name>`
// или `ipv6 access-list <name>` (у NDMS под IPv6 своё пространство списков, и
// имена в них не пересекаются). Флаги блока (`auto-delete`) правилами не
// считаются: снять их отдельной командой нельзя, и на «список чей-то ещё» они
// не указывают.
//
// Нужен снятию permit-all: список `_WEBADMIN_<iface>` — это ЕЩЁ И место, куда
// веб-морда роутера кладёт правила межсетевого экрана интерфейса, поэтому
// сносить его целиком можно, только если кроме нашего правила в нём ничего
// нет (F314, issue #879).
func ACLRulesOf(lines []string, header string) []string {
	out := []string{}
	in := false
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if raw == trimmed { // без отступа — заголовок блока или его конец
			in = trimmed == header
			continue
		}
		if in && (strings.HasPrefix(trimmed, "permit ") || strings.HasPrefix(trimmed, "deny ")) {
			out = append(out, trimmed)
		}
	}
	return out
}

// HasBlock — есть ли в running-config блок с заголовком header (строка без
// отступа, целиком), например `ipv6 access-list <name>`.
func HasBlock(lines []string, header string) bool {
	for _, raw := range lines {
		if raw == header {
			return true
		}
	}
	return false
}

// HasBlockLine — есть ли в теле блока header строка line (после TrimSpace,
// целиком). Флаг списка `auto-delete` NDMS печатает именно так — строкой тела
// блока `[ipv6 ]access-list <name>` (стенд 5.01.C.6, Task 59 П5).
func HasBlockLine(lines []string, header, line string) bool {
	in := false
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if raw == trimmed { // без отступа — заголовок блока или его конец
			in = trimmed == header
			continue
		}
		if in && trimmed == line {
			return true
		}
	}
	return false
}

// InterfaceAccessGroups — то же по кэшированному running-config.
func (s *RunningConfigStore) InterfaceAccessGroups(ctx context.Context, iface string) ([]string, error) {
	lines, err := s.Lines(ctx)
	if err != nil {
		return nil, err
	}
	return InterfaceAccessGroupsOf(lines, iface, ACLv4), nil
}

type rcResp struct {
	Message []string `json:"message"`
}

func (s *RunningConfigStore) fetch(ctx context.Context) ([]string, error) {
	raw, err := s.getter.GetRaw(ctx, "/show/running-config")
	if err != nil {
		return nil, fmt.Errorf("fetch running-config: %w", err)
	}
	var resp rcResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse running-config: %w", err)
	}
	return resp.Message, nil
}
