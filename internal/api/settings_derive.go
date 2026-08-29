package api

import (
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Деривация настроек для POST /settings/update — единственное место, где патч
// превращается в запись. Вызывается ДВАЖДЫ: на черновой копии (чтобы отдать
// клиенту ошибку валидации до всякой записи) и внутри мутатора
// SettingsStore.Update (чтобы на диск уехала запись, выведенная из АКТУАЛЬНОГО
// состояния, а не из снимка, снятого до похода в downloader и до N записей
// enablePingCheckOnAllTunnels). Обе стороны обязаны считать одно и то же —
// поэтому код один, а не два похожих.
//
// Функции чистые относительно стора, сети и файловой системы: всё, что
// требует похода наружу, приходит параметром (settingsProbes). Это же делает
// их исполнимыми под локом стора — см. докстринг SettingsStore.Update.
//
// Разрезаны на голову и хвост ровно там, где в середине стоит поход в
// downloader: порядок проверок и, значит, приоритет ошибок при нескольких
// невалидных полях сохраняется прежним.

// settingsError — отказ валидации. Все они отдаются как 400, различаются кодом.
type settingsError struct {
	msg  string
	code string
}

func (e *settingsError) Error() string { return e.msg }

func settingsErr(msg, code string) error { return &settingsError{msg: msg, code: code} }

// respondSettingsError отдаёт отказ деривации клиенту. Все отказы — 400,
// различаются кодом; неопознанная ошибка идёт как ошибка записи.
func respondSettingsError(w http.ResponseWriter, err error) {
	var se *settingsError
	if errors.As(err, &se) {
		response.ErrorWithStatus(w, http.StatusBadRequest, se.msg, se.code)
		return
	}
	response.Error(w, err.Error(), "SETTINGS_SAVE_ERROR")
}

// settingsPrev — значения ДО применения патча, нужные хвосту для проверок
// «менялось ли поле».
type settingsPrev struct {
	clashPort int
}

// settingsProbes — результаты проверок, которым нужно ходить наружу. Считаются
// на черновике, ВНЕ лока стора, и отдаются хвосту готовыми: проверка маршрута
// загрузок дёргает downloader, а проверка порта Clash API сканирует
// /proc/net/* и обходит /proc/[pid]/fd — под локом стора такое держать нельзя,
// оно заблокировало бы все чтения настроек, включая auth-middleware.
//
// Сами УСЛОВИЯ, при которых проверки применимы, остаются в хвосте: они
// считаются по той записи, что уходит на диск.
type settingsProbes struct {
	// downloadKind — nil, когда в downloader не ходили; иначе канонический
	// Kind, в том числе пустой (различие важно: прежний код писал результат
	// безусловно, раз уж сходил).
	downloadKind *string
	// clashPortMsg — текст отказа по порту Clash API, пусто = порт годен.
	clashPortMsg string
}

// deriveSettingsHead применяет патч и нормализует/проверяет всё, что стоит до
// похода в downloader. После него cur.Download.RouteTag уже нормализован —
// именно его читает вызывающий, решая, идти ли в сервис.
func (h *SettingsHandler) deriveSettingsHead(cur *storage.Settings, patch *storage.SettingsPatch) (settingsPrev, error) {
	prev := settingsPrev{clashPort: cur.SingboxClashPort}

	// Режим перехвата и вкл/выкл sing-box-роутера этим путём НЕ меняются:
	// запись здесь идёт мимо SwitchRoutingMode, а тик планировщика подхватил бы
	// чужой режим и оставил ресурсы прежнего жить (см. шапку
	// internal/singbox/router/fakeip_transition.go). Режим меняется только
	// POST /singbox/router/mode, Enabled — /enable и /disable. Поля из тела
	// молча игнорируются — patch-семантика, как у пустого ApiKey у вызывающего.
	mode := cur.SingboxRouter.RoutingMode
	enabled := cur.SingboxRouter.Enabled
	storage.ApplyPatch(cur, patch)
	cur.SingboxRouter.RoutingMode = mode
	cur.SingboxRouter.Enabled = enabled

	cur.PingCheck.Defaults.Target = normalizePingCheckTarget(cur.PingCheck.Defaults.Target)
	if err := validatePingCheckTarget(cur.PingCheck.Defaults.Target); err != nil {
		return prev, settingsErr(err.Error(), "INVALID_PING_CHECK_TARGET")
	}

	cur.ConnectivityCheckURL = normalizeConnectivityCheckURL(cur.ConnectivityCheckURL)
	if err := validateConnectivityCheckURL(cur.ConnectivityCheckURL); err != nil {
		return prev, settingsErr(err.Error(), "INVALID_CONNECTIVITY_CHECK_URL")
	}

	cur.Download.RouteTag = strings.TrimSpace(cur.Download.RouteTag)
	if cur.Download.RouteTag == "" {
		cur.Download.RouteTag = "direct"
	}
	if cur.Download.RouteTag == "direct" {
		cur.Download.RouteKind = "direct"
	}
	return prev, nil
}

// deriveSettingsTail дописывает результаты внешних проверок и доводит
// остальные поля.
func (h *SettingsHandler) deriveSettingsTail(cur *storage.Settings, patch *storage.SettingsPatch, prev settingsPrev, probes settingsProbes) error {
	if probes.downloadKind != nil {
		cur.Download.RouteKind = *probes.downloadKind
	}

	// Время жизни сессии проверяем ТОЛЬКО когда клиент его прислал. Посторонняя
	// запись не должна падать на уже лежащем нуле — например, downgrade
	// переписал settings.json на текущем schemaVersion без поля, и migrateToV29
	// больше не отработает; хранимое значение самолечится в SettingsStore.Load.
	if patch.SessionTtlHours != nil &&
		(cur.SessionTtlHours < storage.MinSessionTTLHours || cur.SessionTtlHours > storage.MaxSessionTTLHours) {
		return settingsErr("время жизни сессии должно быть от 1 до 720 часов", "INVALID_SESSION_TTL")
	}

	// Bootstrap-резолвер поднимается ДО всякого DNS, поэтому домен здесь
	// неработоспособен — только литеральный IP (issue #770). Пустое значение
	// снимает настройку: 00-base.json остаётся как есть.
	// Трогаем и валидируем ТОЛЬКО присланное: невалидное (или неаккуратно
	// записанное) значение, уже лежащее в settings.json — downgrade, ручная
	// правка, — иначе заперло бы пользователя вне всех настроек целиком. Тот
	// же контракт, что у sessionTtlHours выше.
	if patch.SingboxBootstrapDNS != nil {
		cur.SingboxBootstrapDNS = strings.TrimSpace(cur.SingboxBootstrapDNS)
		if cur.SingboxBootstrapDNS != "" && net.ParseIP(cur.SingboxBootstrapDNS) == nil {
			return settingsErr("адрес bootstrap-DNS должен быть IP-адресом без порта", "INVALID_SINGBOX_BOOTSTRAP_DNS")
		}
	}

	// Порт Clash API проверяем ТОЛЬКО при реальной смене: наш sing-box в этот
	// момент слушает СТАРЫЙ порт, так что сверка «а не мы ли держим новый»
	// не нужна — совпасть значения могут лишь при сохранении того же порта,
	// а это не смена (issue #788).
	if patch.SingboxClashPort != nil && prev.clashPort != cur.SingboxClashPort && probes.clashPortMsg != "" {
		return settingsErr(probes.clashPortMsg, "SINGBOX_CLASH_PORT_INVALID")
	}

	// usageLevel проверяем после merge. Пустым он быть не может — дефолты его
	// заполняют, миграция v15 добивает, — поэтому отвергаем только явный мусор.
	if storage.NormalizeUsageLevel(cur.UsageLevel) != cur.UsageLevel {
		return settingsErr("invalid usageLevel: must be one of basic, advanced, expert", "INVALID_USAGE_LEVEL")
	}

	// Проверка ДО нормализации: normalize схлопнул бы мусор в дефолт и мусор
	// прошёл бы молча.
	if !storage.IsValidSingboxLogLevel(cur.Logging.SingboxLogLevel) {
		return settingsErr(
			"invalid singboxLogLevel: must be one of trace, debug, info, warn, error, fatal, panic",
			"INVALID_SINGBOX_LOG_LEVEL",
		)
	}
	cur.Logging.SingboxLogLevel = storage.NormalizeSingboxLogLevel(cur.Logging.SingboxLogLevel)

	// Расписание автоустановки проверяем только когда клиент прислал блок
	// updates (он патчится целиком, как DNSRoute/GeoFile/PingCheck).
	if patch.Updates != nil {
		if cur.Updates.AutoInstallIntervalDays < 1 || cur.Updates.AutoInstallIntervalDays > 30 {
			return settingsErr("updates.autoInstallIntervalDays must be between 1 and 30", "INVALID_AUTO_INSTALL_INTERVAL")
		}
		if !autoInstallTimePattern.MatchString(cur.Updates.AutoInstallTime) {
			return settingsErr("updates.autoInstallTime must be in HH:MM (24h) format", "INVALID_AUTO_INSTALL_TIME")
		}
	}
	return nil
}
