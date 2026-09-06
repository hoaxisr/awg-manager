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

// InterfaceAccessGroupsOf — имена списков, привязанных к интерфейсу строками
// `ip access-group <name> in` внутри блока `interface <iface>`, в порядке
// появления — это порядок привязки и порядок джампов в _NDM_ACL_IN (стенд
// 5.01, 2026-09-05). Форма `no ip access-group …` не совпадает по построению:
// сравнение идёт с начала строки после TrimSpace. Разбор по готовым строкам —
// для вызывающих без стора (адаптеры cmd).
func InterfaceAccessGroupsOf(lines []string, iface string) []string {
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
		if len(f) == 4 && f[0] == "ip" && f[1] == "access-group" && f[3] == "in" {
			out = append(out, f[2])
		}
	}
	return out
}

// InterfaceAccessGroups — то же по кэшированному running-config.
func (s *RunningConfigStore) InterfaceAccessGroups(ctx context.Context, iface string) ([]string, error) {
	lines, err := s.Lines(ctx)
	if err != nil {
		return nil, err
	}
	return InterfaceAccessGroupsOf(lines, iface), nil
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
