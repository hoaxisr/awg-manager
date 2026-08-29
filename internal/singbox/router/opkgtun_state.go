package router

import (
	"context"
	"slices"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// opkgTunOwned возвращает единую запись владения, когда она принадлежит mode.
// Provisioned НЕ проверяется намеренно: hold policy-tun
// ({Provisioned:false, Index}) — валидное владение; нужен ли provisioned-гейт —
// решает вызывающий по своей семантике.
func opkgTunOwned(settings *storage.Settings, mode string) (*storage.OpkgTunState, bool) {
	if settings == nil {
		return nil, false
	}
	st := settings.OpkgTun
	if st == nil || st.Mode != mode {
		return nil, false
	}
	return st, true
}

// natSegmentsOf — nil-safe чтение policy-payload записи (в т.ч. чужого Mode:
// артефакт миграции v34 хранит NAT-записи на fakeip-записи).
func natSegmentsOf(st *storage.OpkgTunState) []storage.PolicyTunNATSegment {
	if st == nil || st.PolicyTun == nil {
		return nil
	}
	return st.PolicyTun.NATSegments
}

// setPolicyPayload кладёт NAT-записи в ЛОКАЛЬНУЮ копию записи владения (пустой
// список снимает payload). Персист — отдельным вызовом SetOpkgTunNATSegments.
func setPolicyPayload(st *storage.OpkgTunState, segs []storage.PolicyTunNATSegment) {
	if st == nil {
		return
	}
	if len(segs) == 0 {
		st.PolicyTun = nil
		return
	}
	st.PolicyTun = &storage.OpkgTunPolicyData{NATSegments: segs}
}

// ownsOpkgTun сообщает, несёт ли живой NDMS-интерфейс наше описание.
// Скана нет или он упал — false: «не знаем» ≠ «наш», а Create по чужому живому
// интерфейсу переписал бы его настройки (fail-closed, reuse-путь policy-tun).
func (s *ServiceImpl) ownsOpkgTun(ctx context.Context, ndmsName, description string) bool {
	if s.deps.OpkgTunScan == nil {
		return false
	}
	ids, err := s.deps.OpkgTunScan(ctx, description)
	if err != nil {
		return false
	}
	return slices.Contains(ids, ndmsName)
}

// provenForeignOpkgTun — «доказанно чужой»: скан по нашему описанию УСПЕШЕН и
// имени в нём нет. Отличие от !ownsOpkgTun принципиально: там «не знаем ≠ наш»
// (fail-closed для reuse), здесь «не знаем ≠ чужой» — недоступный скан не
// должен ронять идемпотентность в вечный re-provision.
func (s *ServiceImpl) provenForeignOpkgTun(ctx context.Context, ndmsName, description string) bool {
	if s.deps.OpkgTunScan == nil {
		return false
	}
	ids, err := s.deps.OpkgTunScan(ctx, description)
	if err != nil {
		return false
	}
	return !slices.Contains(ids, ndmsName)
}

// needsReprovision — общий предикат обоих reconcile: провижининга нет, наш
// интерфейс исчез, или на нашем номере доказанно ЧУЖОЙ живой интерфейс (иначе
// drift-heal чинил бы чужой интерфейс).
//
// Ошибка пробы live не равна «интерфейс исчез»: иначе каждый сбойный тик уходил
// бы в полный re-provision. Недоступный скан владения — тоже не повод: тут
// работает provenForeignOpkgTun с его «не знаем ≠ чужой», и направление
// fail-closed здесь ПРОТИВОПОЛОЖНО reuse-switch в enablePolicyTun. Подмена на
// !ownsOpkgTun тихо гасит drift-heal: гард enableLocked схлопнется тем же
// provenForeign, и тик не сделает вообще ничего.
func (s *ServiceImpl) needsReprovision(ctx context.Context, st *storage.OpkgTunState, live map[int]bool, probeErr error, desc string) bool {
	if st == nil || !st.Provisioned {
		return true
	}
	if probeErr != nil {
		return false
	}
	if !live[st.Index] {
		return true
	}
	return s.provenForeignOpkgTun(ctx, tunNDMSName(st.Index), desc)
}

// skipForeignTeardown отвечает, надо ли ПРОПУСТИТЬ снос интерфейса, на который
// указывает запись владения: индекс мог занять посторонний OpkgTun после смерти
// нашего, и снос по имени убил бы чужое. Зеркало provenForeignOpkgTun-гарда на
// присвоении: тот запрещает БРАТЬ чужой интерфейс, этот — УДАЛЯТЬ его.
// Семантика та же — «недоступный скан ≠ чужой»: без скана и на его ошибке
// сносим как раньше, иначе обвязки без скана перестали бы убирать собственные
// сироты. Пропуск логируется.
func (s *ServiceImpl) skipForeignTeardown(ctx context.Context, ndmsName, description, scope string) bool {
	if !s.provenForeignOpkgTun(ctx, ndmsName, description) {
		return false
	}
	s.appLog.Warn(scope, ndmsName, "на этом номере нет нашего OpkgTun — снос пропущен")
	return true
}

// releaseForeignOpkgTun освобождает запись владения ЧУЖОГО режима перед её
// перезаписью (handover в enable) или снятием (персист-реап): для policy-tun
// сперва восстановить записанный NAT сегментов (best-effort, Warn — как в
// реапе), затем teardownOpkgTun. Возвращает ошибку teardown; провал оставляет
// persist-less сироту, которую добивает description-скан — профиль потерь
// идентичен прежнему реаповому пути.
//
// removed отвечает, был ли интерфейс действительно снесён: на пропуске чужого
// ошибки нет, но и сноса не было — без этого флага вызывающий печатал бы
// «removed» вхолостую.
func (s *ServiceImpl) releaseForeignOpkgTun(ctx context.Context, st *storage.OpkgTunState, scope string) (removed bool, err error) {
	ndmsName := tunNDMSName(st.Index)
	if segs := natSegmentsOf(st); len(segs) > 0 {
		if err := s.restorePolicyTunNAT(ctx, segs); err != nil {
			s.appLog.Warn(scope, ndmsName, "restore segment NAT: "+err.Error())
		}
	}
	desc := fakeIPTunDescription
	if st.Mode == storage.OpkgTunModePolicyTun {
		desc = policyTunDescription
	}
	if s.skipForeignTeardown(ctx, ndmsName, desc, scope) {
		return false, nil
	}
	if err := s.teardownOpkgTun(ctx, ndmsName, scope); err != nil {
		return false, err
	}
	return true, nil
}
