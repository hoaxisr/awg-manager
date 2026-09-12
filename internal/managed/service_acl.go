package managed

import (
	"context"
	"fmt"
)

// ForeignAccessGroups — списки, привязанные к интерфейсу строками
// `ip access-group … in`, кроме нашего сегментного AWGM_<iface>. Порядок —
// порядок привязки (= порядок джампов _NDM_ACL_IN): список, привязанный раньше
// нашего, срабатывает раньше. Источник — кэш running-config.
func (s *Service) ForeignAccessGroups(ctx context.Context, iface string) ([]string, error) {
	if s.queries == nil || s.queries.RunningConfig == nil {
		return nil, fmt.Errorf("running-config store not wired")
	}
	names, err := s.queries.RunningConfig.InterfaceAccessGroups(ctx, iface)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, n := range names {
		if n != "AWGM_"+iface {
			out = append(out, n)
		}
	}
	return out, nil
}
