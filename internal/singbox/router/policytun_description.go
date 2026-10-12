package router

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Настраиваемое NDMS-описание интерфейса policy-tun: под ним OpkgTun виден в
// веб-интерфейсе роутера и в политиках доступа.
//
// Описание — это и метка владения (скан по точной строке, opkgTunOwnership),
// поэтому смена настройки держится на двух правилах:
//   - запись владения помнит описание, ПРИМЕНЁННОЕ к интерфейсу
//     (OpkgTunState.Description), и описание, которое переименование
//     СОБИРАЕТСЯ поставить (OpkgTunState.PendingDescription); владение
//     признаётся по обоим — и ТОЛЬКО по ним (policyTunOwnDescriptions).
//     Порядок переименования — «запись намерения → NDMS → запись результата»,
//     как persist-before-create у провижининга: сбой или крах на любом шаге
//     оставляет интерфейс под именем, которое запись называет. Желаемое из
//     настроек в набор владения НЕ входит: пользовательское имя не уникально,
//     и чужой OpkgTun под ним на нашем номере (наш пропал, пользователь
//     создал свой с тем же названием) признавался бы своим и перенастраивался.
//     Единственное окно, где интерфейс стоит под именем, которого запись не
//     называет, — откат enable после Create на переиспользованном номере
//     (интерфейс уже переименован, запись возвращена прежняя): откат пишет
//     туда намерение, см. policytun_enable.go;
//   - description-реап сирот (reapOrphansByDescription) по-прежнему сканирует
//     ТОЛЬКО штатное policyTunDescription. Пользовательское имя не уникально по
//     построению: туннели awg-manager и чужие OpkgTun несут в описании имена,
//     которые пользователь выбирает сам, и реап по такому имени снёс бы чужой
//     интерфейс. Сирота под пользовательским именем остаётся — удалить её
//     можно в веб-интерфейсе роутера; это дешевле удалённого чужого туннеля.
//
// Версии без этой настройки владение по пользовательскому имени не признают:
// после отката интерфейс под ним для них «доказанно чужой» — режим поднимется
// на другом номере, permit'ы в политиках пропадут. Перед откатом имя стоит
// вернуть штатное (CHANGELOG).

// policyTunDescriptionMaxRunes — предел длины описания. NDMS принимает до 256
// байт (tunnel.MaxNameBytes), но имя интерфейса в списках роутера длиннее
// пары десятков символов не читается; 32 руны — не больше 128 байт.
const policyTunDescriptionMaxRunes = 32

// policyTunReservedPrefix — префикс служебных описаний awg-manager: штампы
// режимов роутера ("awgm fakeip-tun", "awgm policy-tun"), интерфейсы
// прокси-рантайма ("AWGM WDTT …") и managed-сервера ("AWGM WG Server").
// Интерфейсы прокси-рантайма уборщик находит по ПРЕФИКСУ описания, так что
// policy-tun под таким именем он принял бы за свою сироту и снёс.
const policyTunReservedPrefix = "awgm "

// normalizePolicyTunDescription приводит sr.PolicyTunDescription к хранимой
// форме: пробелы по краям срезаны, штатное описание хранится пустым (одно
// значение — одно представление, и запись владения при дефолте не меняется).
func normalizePolicyTunDescription(sr *storage.SingboxRouterSettings) error {
	d := strings.TrimSpace(sr.PolicyTunDescription)
	if d == policyTunDescription {
		d = ""
	}
	sr.PolicyTunDescription = d
	if d == "" {
		return nil
	}
	if !utf8.ValidString(d) {
		return fmt.Errorf("policyTunDescription: invalid UTF-8")
	}
	if n := utf8.RuneCountInString(d); n > policyTunDescriptionMaxRunes {
		return fmt.Errorf("policyTunDescription: %d characters, max %d", n, policyTunDescriptionMaxRunes)
	}
	// Только печатаемые (unicode.IsPrint: буквы, цифры, знаки, символы и
	// пробел). Отсев одних управляющих (IsControl) пропускал бы форматирующие
	// — U+202E (смена направления текста), U+200B (нулевая ширина): имя в
	// списках роутера читалось бы не тем, чем записано.
	if strings.IndexFunc(d, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return fmt.Errorf("policyTunDescription: non-printable characters are not allowed")
	}
	if strings.HasPrefix(strings.ToLower(d), policyTunReservedPrefix) {
		return fmt.Errorf("policyTunDescription: prefix %q is reserved for awg-manager service interfaces", strings.TrimSpace(policyTunReservedPrefix))
	}
	return nil
}

// policyTunWantDescription — описание, которое интерфейс ДОЛЖЕН нести по
// настройкам. TrimSpace — на случай руками правленого settings.json, мимо
// нормализации.
func policyTunWantDescription(sr storage.SingboxRouterSettings) string {
	if d := strings.TrimSpace(sr.PolicyTunDescription); d != "" {
		return d
	}
	return policyTunDescription
}

// policyTunAppliedDescription — описание, которое мы поставили на интерфейс
// записи владения. Пусто — штатное (в т.ч. записи версий без настройки).
func policyTunAppliedDescription(st *storage.OpkgTunState) string {
	if st != nil && st.Description != "" {
		return st.Description
	}
	return policyTunDescription
}

// storedPolicyTunDescription — форма для записи владения: штатное описание
// хранится пустым, и запись пользователя без переименования не меняется.
func storedPolicyTunDescription(desc string) string {
	if desc == policyTunDescription {
		return ""
	}
	return desc
}

// policyTunOwnDescriptions — описания, под которыми OpkgTun записи признаётся
// нашим: применённое и, если есть, ожидаемое — только из записи владения,
// без желаемого из настроек (см. шапку файла). Ожидаемое закрывает окно
// переименования: интерфейс уже под ним, запись ещё не подтвердила.
func policyTunOwnDescriptions(st *storage.OpkgTunState) []string {
	out := []string{policyTunAppliedDescription(st)}
	if st != nil && st.PendingDescription != "" && st.PendingDescription != out[0] {
		out = append(out, st.PendingDescription)
	}
	return out
}

// persistPolicyTunDescription пишет описания записи владения, если они
// отличаются от записанных: повтор после сбоя NDMS не трогает флеш.
func (s *ServiceImpl) persistPolicyTunDescription(st *storage.OpkgTunState, applied, pending string) error {
	applied = storedPolicyTunDescription(applied)
	if st.Description == applied && st.PendingDescription == pending {
		return nil
	}
	return s.deps.Settings.SetOpkgTunDescription(applied, pending)
}

// healPolicyTunDescription доводит NDMS-описание живого интерфейса до
// настройки: переименование из панели применяется на ближайшем reconcile
// (PUT настроек зовёт его сам). Порядок «намерение → NDMS → результат» — см.
// шапку файла; каждый шаг идемпотентен, сбой — Warn и повтор следующим тиком.
//
// Трогаем только доказанно свой интерфейс: «не знаем» ≠ «наш», и
// SetDescription по чужому переписал бы его имя. Обвязка без скана — свой по
// записи, как в teardownGate.
func (s *ServiceImpl) healPolicyTunDescription(ctx context.Context, st *storage.OpkgTunState,
	sr storage.SingboxRouterSettings, iface, ndmsName string,
) {
	const scope = "policy-tun-reconcile"
	if st == nil || s.deps.OpkgTun == nil {
		return
	}
	want := policyTunWantDescription(sr)
	applied, pending := policyTunAppliedDescription(st), st.PendingDescription
	if applied == want && pending == "" {
		return
	}
	switch s.opkgTunOwnership(ctx, ndmsName, policyTunOwnDescriptions(st)...) {
	case ownershipForeign, ownershipUnknown:
		return
	}
	// Незакрытое намерение: дошло ли прежнее переименование до NDMS, запись
	// не знает — спрашиваем роутер. Дошло — применённое теперь оно; нет —
	// намерение снимается, интерфейс так и стоит под применённым. Скан упал —
	// не гадаем, повтор следующим тиком. Без скана намерение снимается: без
	// него владение и так признаёт запись, а переименование ниже идемпотентно.
	if pending != "" {
		switch s.opkgTunOwnership(ctx, ndmsName, pending) {
		case ownershipUnknown:
			return
		case ownershipOurs:
			applied = pending
		}
		pending = ""
	}
	if applied == want {
		if err := s.persistPolicyTunDescription(st, applied, ""); err != nil {
			s.appLog.Warn(scope, iface, "persist description: "+err.Error())
		}
		return
	}
	if err := s.persistPolicyTunDescription(st, applied, want); err != nil {
		s.appLog.Warn(scope, iface, "persist pending description: "+err.Error())
		return
	}
	if err := s.deps.OpkgTun.SetDescription(ctx, ndmsName, want); err != nil {
		s.appLog.Warn(scope, iface, "rename: "+err.Error())
		return
	}
	if err := s.deps.Settings.SetOpkgTunDescription(storedPolicyTunDescription(want), ""); err != nil {
		s.appLog.Warn(scope, iface, "persist description: "+err.Error())
		return
	}
	s.appLog.Info(scope, iface, "интерфейс переименован: "+want)
}
