package router

import (
	"context"

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

// releaseForeignOpkgTun освобождает запись владения ЧУЖОГО режима перед её
// перезаписью (handover в enable) или снятием (персист-реап): для policy-tun
// сперва восстановить записанный NAT сегментов (best-effort, Warn — как в
// реапе), затем teardownOpkgTun. Возвращает ошибку teardown; провал оставляет
// persist-less сироту, которую добивает description-скан — профиль потерь
// идентичен прежнему реаповому пути.
func (s *ServiceImpl) releaseForeignOpkgTun(ctx context.Context, st *storage.OpkgTunState, scope string) error {
	ndmsName := fakeIPNDMSName(st.Index)
	if segs := natSegmentsOf(st); len(segs) > 0 {
		if err := s.restorePolicyTunNAT(ctx, segs); err != nil {
			s.appLog.Warn(scope, ndmsName, "restore segment NAT: "+err.Error())
		}
	}
	return s.teardownOpkgTun(ctx, ndmsName, scope)
}
