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
//     (OpkgTunState.Description), а владение признаётся по нему И по
//     желаемому из настроек (policyTunOwnDescriptions). Переименование пишет
//     сперва NDMS, потом запись: сбой между ними оставляет интерфейс под
//     желаемым описанием, которое владение уже признаёт. Обратный порядок
//     оставил бы запись с именем, которого на интерфейсе нет, — «доказанно
//     чужой» и re-provision поверх собственного живого интерфейса;
//   - description-реап сирот (reapOrphansByDescription) по-прежнему сканирует
//     ТОЛЬКО штатное policyTunDescription. Пользовательское имя не уникально по
//     построению: туннели awg-manager и чужие OpkgTun несут в описании имена,
//     которые пользователь выбирает сам, и реап по такому имени снёс бы чужой
//     интерфейс. Сирота под пользовательским именем остаётся — удалить её
//     можно в веб-интерфейсе роутера; это дешевле удалённого чужого туннеля.

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
	if strings.IndexFunc(d, unicode.IsControl) >= 0 {
		return fmt.Errorf("policyTunDescription: control characters are not allowed")
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
// нашим: применённое (запись) и желаемое (настройки), без повторов. Второе
// нужно окну «NDMS уже переименован, запись ещё нет» — см. шапку файла.
func policyTunOwnDescriptions(st *storage.OpkgTunState, sr storage.SingboxRouterSettings) []string {
	applied, want := policyTunAppliedDescription(st), policyTunWantDescription(sr)
	if applied == want {
		return []string{applied}
	}
	return []string{applied, want}
}

// policyTunOwnDescriptionsStored — policyTunOwnDescriptions там, где под рукой
// только запись владения: желаемое берётся из кэша стора (Get не читает
// флеш). Ошибка чтения даёт штатное описание вторым кандидатом — лишний
// кандидат безвреден, скан по нему лишь не найдёт нашего имени.
func (s *ServiceImpl) policyTunOwnDescriptionsStored(st *storage.OpkgTunState) []string {
	var sr storage.SingboxRouterSettings
	if s.deps.Settings != nil {
		if cur, err := s.deps.Settings.Get(); err == nil {
			sr = cur.SingboxRouter
		}
	}
	return policyTunOwnDescriptions(st, sr)
}

// healPolicyTunDescription доводит NDMS-описание живого интерфейса до
// настройки: переименование из панели применяется на ближайшем reconcile
// (PUT настроек зовёт его сам). Порядок «NDMS → запись» — см. шапку файла.
//
// Трогаем только доказанно свой интерфейс: «не знаем» ≠ «наш», и
// SetDescription по чужому переписал бы его имя. Обвязка без скана — свой по
// записи, как в teardownGate. Сбой — Warn и повтор следующим тиком: запись
// при этом называет прежнее описание, а владение признаёт оба.
func (s *ServiceImpl) healPolicyTunDescription(ctx context.Context, st *storage.OpkgTunState,
	sr storage.SingboxRouterSettings, iface, ndmsName string,
) {
	want := policyTunWantDescription(sr)
	if st == nil || s.deps.OpkgTun == nil || policyTunAppliedDescription(st) == want {
		return
	}
	switch s.opkgTunOwnership(ctx, ndmsName, policyTunOwnDescriptions(st, sr)...) {
	case ownershipForeign, ownershipUnknown:
		return
	}
	if err := s.deps.OpkgTun.SetDescription(ctx, ndmsName, want); err != nil {
		s.appLog.Warn("policy-tun-reconcile", iface, "rename: "+err.Error())
		return
	}
	if err := s.deps.Settings.SetOpkgTunDescription(storedPolicyTunDescription(want)); err != nil {
		s.appLog.Warn("policy-tun-reconcile", iface, "persist description: "+err.Error())
		return
	}
	s.appLog.Info("policy-tun-reconcile", iface, "интерфейс переименован: "+want)
}
