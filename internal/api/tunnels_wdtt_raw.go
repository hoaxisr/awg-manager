package api

import (
	"errors"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// backendWdttRaw — значение storage.AWGTunnel.Backend у зеркальной записи
// прокси-выхода. Пакет-локальная копия: internal/wdtt снесён, а зеркало ведёт
// прокси-рантайм (exitreg/mirror.go держит такую же копию по той же причине).
const backendWdttRaw = "wdtt-raw"

// wdttRawConnectivityCheck — проверка связности карточки зеркала. Дефолт тот
// же, что кладёт в новую запись зеркало прокси-рантайма (exitreg/mirror.go:97).
func wdttRawConnectivityCheck(stored *storage.AWGTunnel) *storage.ConnectivityCheckConfig {
	if stored != nil && stored.ConnectivityCheck != nil && stored.ConnectivityCheck.Method != "" {
		return stored.ConnectivityCheck
	}
	return &storage.ConnectivityCheckConfig{Method: "http"}
}

// buildWdttRawResponse — ответ карточки зеркальной записи (GET и PATCH одного
// туннеля). Наложения живых полей поверх записи здесь больше нет: запись ведёт
// зеркало прокси-рантайма, и она сама есть источник этих полей.
func (h *TunnelsHandler) buildWdttRawResponse(stored *storage.AWGTunnel) map[string]interface{} {
	if stored == nil {
		return nil
	}
	status := "stopped"
	if stored.Enabled {
		status = "running"
	}
	ifaceName := strings.TrimSpace(stored.RawKernelIface)
	if ifaceName == "" {
		ifaceName = stored.ID
	}
	return map[string]interface{}{
		"id":                stored.ID,
		"name":              stored.Name,
		"type":              "awg",
		"enabled":           stored.Enabled,
		"defaultRoute":      stored.DefaultRoute,
		"interfaceName":     ifaceName,
		"ndmsName":          stored.RawNdmsIface,
		"state":             status,
		"backend":           backendWdttRaw,
		"interface":         stored.Interface,
		"peer":              stored.Peer,
		"connectivityCheck": wdttRawConnectivityCheck(stored),
	}
}

// mirrorOwnerKey — ключ прокси-инстанса, чьей проекцией является зеркальная
// запись, если инстанс ещё жив. Пустой ключ означает «владельца нет»: запись
// осиротела, и удалить её можно.
//
// Ошибка здесь — это «владение НЕ проверено», а не «владельца нет», и удалять
// на ней нельзя. Зеркало воскрешает запись на ближайшем объявлении, то есть на
// каждом бооте и на любой правке инстанса, но уже с ДЕФОЛТАМИ: настройки
// карточки (PingCheck и прочее) пропали бы молча, а удаление выглядело бы
// успешным (амендмент F2).
func (h *TunnelsHandler) mirrorOwnerKey(stored *storage.AWGTunnel) (string, error) {
	clientID := strings.TrimSpace(stored.WdttClientID)
	if clientID == "" {
		return "", nil
	}
	if h.proxyRecords == nil {
		return "", errors.New("хранилище прокси-инстансов не подключено")
	}
	st, err := h.proxyRecords.Load()
	if err != nil {
		return "", err
	}
	for _, rec := range st.Records {
		if rec.Kind == instancestore.KindWdttClient && rec.ID == clientID {
			return rec.Key(), nil
		}
	}
	return "", nil
}
