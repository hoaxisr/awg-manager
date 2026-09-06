package managed

import (
	"context"
	"fmt"
	"slices"
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

// stripForeignPermitAll снимает permit-all `_WEBADMIN_<iface>` веб-морды, если
// он привязан: такой список — безусловный ACCEPT ДО security-level и обнуляет
// выбор сегментов (стенд 2026-09-02/05). Паритет с ресурсом permit_absent у
// wdtt. Только при наличии — без записи и RCI на чистом интерфейсе.
func (s *Service) stripForeignPermitAll(ctx context.Context, iface string) error {
	names, err := s.ForeignAccessGroups(ctx, iface)
	if err != nil {
		return err
	}
	if !slices.Contains(names, "_WEBADMIN_"+iface) {
		return nil
	}
	if err := s.commands.Interfaces.RemovePermitAllACL(ctx, iface); err != nil {
		return err
	}
	s.appLog.Info("lan-acl", iface, "снят permit-all _WEBADMIN_"+iface+": он открывал клиентам весь LAN мимо выбора сегментов")
	return nil
}

// SweepForeignPermitAll — бут-проход по всем managed-серверам: снять остаток
// permit-all `_WEBADMIN_<iface>`, если он есть. Чтение из кэша running-config
// (прогрет на старте), запись и RCI — только при наличии остатка: на чистом
// роутере проход ничего не пишет (износ флеша — память проекта).
func (s *Service) SweepForeignPermitAll(ctx context.Context) {
	if s.commands == nil || s.commands.Interfaces == nil {
		return
	}
	for _, srv := range s.List() {
		if err := s.stripForeignPermitAll(ctx, srv.InterfaceName); err != nil {
			s.appLog.Warn("lan-acl", srv.InterfaceName, "бут-проверка permit-all: "+err.Error())
		}
	}
}
