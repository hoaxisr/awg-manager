package wdtt

import (
	"fmt"
	"strings"
)

// RefreshSubscription re-fetches the stored subscription URL and updates the
// client config if the current peer is still present in the document.
func (s *Service) RefreshSubscription(id string) (ClientInstance, ImportPayload, error) {
	inst, err := s.clientInstance(id)
	if err != nil {
		return ClientInstance{}, ImportPayload{}, err
	}
	subURL := normalizeSubURL(strings.TrimSpace(inst.Config.Sub))
	if subURL == "" {
		return ClientInstance{}, ImportPayload{}, fmt.Errorf("URL подписки не сохранён — импортируйте HTTPS _wdtt.json ещё раз")
	}
	if strings.TrimSpace(inst.Config.Peer) == "" {
		return ClientInstance{}, ImportPayload{}, fmt.Errorf("у клиента не задан peer — нечего сопоставить с подпиской")
	}

	decoded, err := DecodeLink(subURL)
	if err != nil {
		return ClientInstance{}, ImportPayload{}, fmt.Errorf("не удалось загрузить подписку: %w", err)
	}
	profiles := ProfilesFromDecode(decoded)
	if len(profiles) == 0 {
		return ClientInstance{}, ImportPayload{}, fmt.Errorf("подписка не содержит профилей")
	}
	profile := FindProfileByPeer(profiles, inst.Config.Peer)
	if profile == nil {
		return ClientInstance{}, ImportPayload{}, fmt.Errorf(
			"peer %s не найден в подписке (%d профилей) — выберите другой сервер через повторный импорт",
			inst.Config.Peer, len(profiles),
		)
	}
	if profile.SubURL == "" {
		profile.SubURL = subURL
	}

	oldListen := inst.Config.Listen
	cfg := ApplyImport(inst.Config, *profile)
	cfg.Listen = oldListen
	cfg.Sub = subURL

	// Финальный Load-modify-Save под s.mu (как RMW-методы в service.go).
	// Лок берём ТОЛЬКО здесь: DecodeLink выше — сетевой fetch до 20с,
	// держать s.mu во время него нельзя. Блок — хвост функции, defer покрывает
	// все return'ы; Load/Save/findClientIndex s.mu не берут (store.mu отдельный).
	s.mu.Lock()
	defer s.mu.Unlock()

	full, err := s.store.Load()
	if err != nil {
		return ClientInstance{}, ImportPayload{}, err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		return ClientInstance{}, ImportPayload{}, fmt.Errorf("клиент %q не найден", id)
	}
	full.Clients[idx].Config = cfg
	s.startBackoff.Forget(clientKey(id))
	if name := strings.TrimSpace(profile.Name); name != "" {
		full.Clients[idx].Name = name
	}
	if err := s.store.Save(full); err != nil {
		return ClientInstance{}, ImportPayload{}, err
	}
	return full.Clients[idx], *profile, nil
}
