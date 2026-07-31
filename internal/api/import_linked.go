package api

import (
	"strings"

	"github.com/hoaxisr/awg-manager/internal/freeturn"
	"github.com/hoaxisr/awg-manager/internal/wdtt"
)

// patchImportContentForLinkedClient sets [Peer] Endpoint from the linked
// FreeTurn/WDTT client listen (authoritative). Frontend may pass stale 9000
// from freeturn:// / wdtt:// payloads when CreateClient already assigned 9001.
func (h *ImportHandler) patchImportContentForLinkedClient(content, freeTurnClientID, wdttClientID string) string {
	listen := h.linkedProxyClientListen(freeTurnClientID, wdttClientID)
	if listen == "" {
		return content
	}
	port, ok := freeturn.LocalListenPort(listen)
	if !ok || port <= 0 {
		port = wdtt.ListenPortFromAddr(listen)
	}
	if port <= 0 {
		return content
	}
	return wdtt.PatchWgConfEndpoint(content, port)
}

func (h *ImportHandler) linkedProxyClientListen(freeTurnClientID, wdttClientID string) string {
	ftID := strings.TrimSpace(freeTurnClientID)
	if ftID != "" && h.freeturn != nil {
		cfg, err := h.freeturn.GetConfig()
		if err == nil {
			for _, c := range cfg.Clients {
				if c.ID == ftID {
					return strings.TrimSpace(c.Config.Listen)
				}
			}
		}
	}
	wdID := strings.TrimSpace(wdttClientID)
	if wdID != "" && h.wdtt != nil {
		cfg, err := h.wdtt.GetConfig()
		if err == nil {
			for _, c := range cfg.Clients {
				if c.ID == wdID {
					return strings.TrimSpace(c.Config.Listen)
				}
			}
		}
	}
	return ""
}
