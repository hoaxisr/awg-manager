package router

import (
	"context"
	"errors"
	"slices"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// opkgTunOwned возвращает единую запись владения, когда она принадлежит mode.
// Provisioned НЕ проверяется намеренно: hold policy-tun
// ({Provisioned:false, Index}) — валидное владение; нужен ли provisioned-гейт —
// решает вызывающий по своей семантике.
//
// Отдаётся КОПИЯ записи: Load/Get отдают ЖИВОЙ кэш стора, который параллельно
// маршалят читатели без нашего лока, и правка возвращённого объекта по месту
// была бы гонкой. Раньше копию снимал каждый вызывающий сам — и снимал только
// один из них (F24). Copy-on-write в SetOpkgTunState этого НЕ заменяет: он
// изолирует объект писателя, а здесь речь про сторону чтения.
func opkgTunOwned(settings *storage.Settings, mode string) (*storage.OpkgTunState, bool) {
	if settings == nil {
		return nil, false
	}
	st := settings.OpkgTun
	if st == nil || st.Mode != mode {
		return nil, false
	}
	cp := *st
	return &cp, true
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

// opkgTunOwnership — вердикт скана владения по NDMS-имени из записи.
type opkgTunOwnership uint8

const (
	// ownershipNoScan — deps.OpkgTunScan не подключён. Прод подключает скан
	// всегда (wiring_server.go, cleanup.go); без него живут только тесты и
	// обвязки без NDMS — каждый потребитель ведёт себя как до появления скана.
	ownershipNoScan opkgTunOwnership = iota
	// ownershipUnknown — скан подключён, но упал: «не знаем» ≠ «наш» и ≠ «чужой»
	// (F493). Снос по имени из записи откладывается, запись не снимается.
	ownershipUnknown
	ownershipOurs
	ownershipForeign
)

// opkgTunOwnership — один скан на описание; отличие ошибки скана от его
// отсутствия принципиально (см. константы).
//
// Описаний бывает несколько: у policy-tun оно настраивается, и на время
// переименования своим считается интерфейс и под применённым, и под ожидаемым
// из записи (policyTunOwnDescriptions). Имя нашлось хоть в одном скане — наш; иначе
// хоть один скан упал — «не знаем»; все успешны и пусты — чужой.
//
// Пустой список описаний — «не знаем», а не «чужой»: без единого скана
// «доказанно чужой» был бы не доказан, а вердикт ведёт к re-provision поверх
// живого интерфейса и к пропуску сноса. Штатно сюда всегда приходит хотя бы
// одно описание.
func (s *ServiceImpl) opkgTunOwnership(ctx context.Context, ndmsName string, descriptions ...string) opkgTunOwnership {
	if s.deps.OpkgTunScan == nil {
		return ownershipNoScan
	}
	if len(descriptions) == 0 {
		return ownershipUnknown
	}
	verdict := ownershipForeign
	for _, description := range descriptions {
		ids, err := s.deps.OpkgTunScan(ctx, description)
		if err != nil {
			verdict = ownershipUnknown
			continue
		}
		if slices.Contains(ids, ndmsName) {
			return ownershipOurs
		}
	}
	return verdict
}

// ownsOpkgTun сообщает, несёт ли живой NDMS-интерфейс наше описание. Скана
// нет или он упал — false: «не знаем» ≠ «наш», а Create по чужому живому
// интерфейсу переписал бы его настройки (fail-closed, reuse-путь policy-tun).
func (s *ServiceImpl) ownsOpkgTun(ctx context.Context, ndmsName string, descriptions ...string) bool {
	return s.opkgTunOwnership(ctx, ndmsName, descriptions...) == ownershipOurs
}

// provenForeignOpkgTun — «доказанно чужой»: скан по нашему описанию УСПЕШЕН и
// имени в нём нет. Отличие от !ownsOpkgTun принципиально: там «не знаем ≠ наш»
// (fail-closed для reuse), здесь «не знаем ≠ чужой» — недоступный скан не
// должен ронять идемпотентность в вечный re-provision.
func (s *ServiceImpl) provenForeignOpkgTun(ctx context.Context, ndmsName string, descriptions ...string) bool {
	return s.opkgTunOwnership(ctx, ndmsName, descriptions...) == ownershipForeign
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
func (s *ServiceImpl) needsReprovision(ctx context.Context, st *storage.OpkgTunState, live map[int]bool, probeErr error, descs ...string) bool {
	if st == nil || !st.Provisioned {
		return true
	}
	if probeErr != nil {
		return false
	}
	if !live[st.Index] {
		return true
	}
	return s.provenForeignOpkgTun(ctx, tunNDMSName(st.Index), descs...)
}

// errOpkgTunOwnershipUnknown — скан владения подключён, но недоступен:
// интерфейс по имени из записи не трогаем и запись не снимаем; повтор —
// следующим тиком или бутом, а persist-less хвост с нашим описанием добирает
// reapOrphansByDescription, когда скан заработает (F493).
var errOpkgTunOwnershipUnknown = errors.New("владение OpkgTun не установлено: скан NDMS недоступен")

// teardownGate решает, можно ли сносить интерфейс по имени из записи владения:
// индекс мог занять посторонний OpkgTun после смерти нашего, и снос по имени
// убил бы чужое. Зеркало provenForeignOpkgTun-гарда на присвоении: тот
// запрещает БРАТЬ чужой интерфейс, этот — УДАЛЯТЬ его.
//
// proceed=true — наш, либо скана нет вовсе (обвязка без скана убирает свои
// сироты как раньше). Доказанно чужой — (false, nil): сносить нечего, запись
// отработана. Скан упал — (false, errOpkgTunOwnershipUnknown): не сносим и не
// снимаем запись. Оба пропуска логируются здесь, вызывающие не дублируют.
func (s *ServiceImpl) teardownGate(ctx context.Context, ndmsName, scope string, descriptions ...string) (proceed bool, err error) {
	switch s.opkgTunOwnership(ctx, ndmsName, descriptions...) {
	case ownershipForeign:
		s.appLog.Warn(scope, ndmsName, "на этом номере нет нашего OpkgTun — снос пропущен")
		return false, nil
	case ownershipUnknown:
		s.appLog.Warn(scope, ndmsName, "скан владения NDMS недоступен — снос отложен до следующего тика")
		return false, errOpkgTunOwnershipUnknown
	}
	return true, nil
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
	descs := []string{fakeIPTunDescription}
	if st.Mode == storage.OpkgTunModePolicyTun {
		descs = policyTunOwnDescriptions(st)
	}
	// Чужой → (false, nil): сносить нечего, запись отработана. Скан упал →
	// (false, errOpkgTunOwnershipUnknown): реап держит запись до следующего
	// тика, handover в enable идёт дальше как при провале release — хвост с
	// прежним описанием добирает description-реап.
	if proceed, gerr := s.teardownGate(ctx, ndmsName, scope, descs...); !proceed {
		return false, gerr
	}
	if err := s.teardownOpkgTun(ctx, ndmsName, scope); err != nil {
		return false, err
	}
	return true, nil
}
