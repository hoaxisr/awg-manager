package api

import (
	"strings"

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
