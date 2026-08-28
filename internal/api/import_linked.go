package api

import (
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// ProxyRecordLister — срез хранилища прокси-инстансов: одно чтение записей.
// Объявлен здесь, у потребителя: импорту нужен ровно этот метод.
type ProxyRecordLister interface {
	Load() (instancestore.State, error)
}

// patchImportContentForLinkedClient sets [Peer] Endpoint from the linked
// FreeTurn/WDTT client listen (authoritative). Frontend may pass stale 9000
// from freeturn:// / wdtt:// payloads when CreateClient already assigned 9001.
func (h *ImportHandler) patchImportContentForLinkedClient(content, freeTurnClientID, wdttClientID string) string {
	listen := h.linkedProxyClientListen(freeTurnClientID, wdttClientID)
	if listen == "" {
		return content
	}
	return wdttlink.PatchWgConfEndpoint(content, wdttlink.ListenPortFromAddr(listen))
}

func (h *ImportHandler) linkedProxyClientListen(freeTurnClientID, wdttClientID string) string {
	ftID := strings.TrimSpace(freeTurnClientID)
	wdID := strings.TrimSpace(wdttClientID)
	if h.proxyRecords == nil || (ftID == "" && wdID == "") {
		return ""
	}
	st, err := h.proxyRecords.Load()
	if err != nil {
		return ""
	}
	// Порядок ролей прежний: FreeTurn главнее — на импорте связь задаётся
	// одним из двух полей, а при обоих заполненных выбор не должен зависеть
	// от порядка записей в файле.
	if ftID != "" {
		if listen, ok := proxyClientListen(st.Records, instancestore.KindFreeTurnClient, ftID); ok {
			return listen
		}
	}
	if wdID != "" {
		if listen, ok := proxyClientListen(st.Records, instancestore.KindWdttClient, wdID); ok {
			return listen
		}
	}
	return ""
}

func proxyClientListen(recs []instancestore.Record, kind instancestore.Kind, id string) (string, bool) {
	for _, rec := range recs {
		if rec.Kind != kind || rec.ID != id {
			continue
		}
		switch kind {
		case instancestore.KindFreeTurnClient:
			if cfg, err := rec.FreeTurnClientConfig(); err == nil {
				return cfg.Listen, true
			}
		case instancestore.KindWdttClient:
			if cfg, err := rec.WdttClientConfig(); err == nil {
				return cfg.Listen, true
			}
		}
		return "", false
	}
	return "", false
}
