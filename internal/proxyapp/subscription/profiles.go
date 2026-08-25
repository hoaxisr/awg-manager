// Package subscription — подписки прокси-клиентов: перечитывание сохранённого
// URL подписки и обновление записи инстанса по найденному профилю.
//
// Разбор ссылки и подписочного документа живёт в proxyapp/wdttlink (его
// возвращает DecodeLink); здесь — только выбор профиля и применение его к
// записи.
package subscription

import (
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
)

// ProfilesFromDecode — профили разобранной ссылки: все профили подписки, либо
// единственный профиль одиночной ссылки.
func ProfilesFromDecode(res wdttlink.LinkDecodeResult) []wdttlink.ImportPayload {
	if res.Subscription != nil && len(res.Subscription.Profiles) > 0 {
		return res.Subscription.Profiles
	}
	if res.Profile != nil {
		return []wdttlink.ImportPayload{*res.Profile}
	}
	return nil
}

// FindProfileByPeer — профиль с тем же адресом сервера. Сравнение через
// wdttlink.PeersEqual: адрес без порта означает 56000, и «1.2.3.4» обязан
// совпасть с «1.2.3.4:56000».
func FindProfileByPeer(profiles []wdttlink.ImportPayload, peer string) *wdttlink.ImportPayload {
	for _, p := range profiles {
		if wdttlink.PeersEqual(p.Peer, peer) {
			found := p
			return &found
		}
	}
	return nil
}

// normalizeSubURL — копия правила из wdttlink (там оно неэкспортировано):
// подписка обязана быть HTTP(S)-ссылкой, всё остальное — не подписка.
// Строка запроса СОХРАНЯЕТСЯ: в ней едет токен подписки.
func normalizeSubURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return ""
	}
	return raw
}

// normalizeConnMode — та же копия по той же причине (wdttlink/ports.go:29).
func normalizeConnMode(mode string) string {
	if strings.ToLower(strings.TrimSpace(mode)) == wdttlink.ConnModeRaw {
		return wdttlink.ConnModeRaw
	}
	return wdttlink.ConnModeWG
}
