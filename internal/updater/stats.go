package updater

import (
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Анонимная статистика установок. Уходит ТОЛЬКО заголовками запроса
// Packages.gz при проверке обновлений и ТОЛЬКО при Updates.StatsEnabled:
// случайный ID установки (счёт уникальных установок без привязки к IP) и
// флаги используемых механизмов маршрутизации.
const (
	headerInstance = "X-Awgm-Instance"
	headerFeatures = "X-Awgm-Features"
)

// Features — что включено на установке. Только флаги: ни чисел, ни имён,
// ни адресов сюда не положить — их нет в типе. Семантика по источнику:
// sing-box — включён в настройках, остальные —
// есть хотя бы одна включённая запись.
type Features struct {
	SingboxRouter bool
	// SingboxMode — RoutingMode sing-box; наружу уходит только значение из
	// knownSingboxModes, остальное — "other".
	SingboxMode string
	DNSRoute    bool // DNS-маршруты NDMS
	ClientRoute bool // per-client маршрутизация
	DeviceProxy bool // прокси для устройств
}

var knownSingboxModes = map[string]bool{"tproxy": true, "fakeip-tun": true, "policy-tun": true}

func (f Features) tokens() []string {
	sb := "sb-off"
	if f.SingboxRouter {
		mode := f.SingboxMode
		if mode == "" {
			mode = "tproxy" // пусто = дефолт, как в SingboxRouterSettings
		}
		if !knownSingboxModes[mode] {
			mode = "other"
		}
		sb = "sb-" + mode
	}
	out := []string{sb}
	for _, t := range []struct {
		on   bool
		name string
	}{
		{f.DNSRoute, "dnsroute"},
		{f.ClientRoute, "clientroute"},
		{f.DeviceProxy, "deviceproxy"},
	} {
		if t.on {
			out = append(out, t.name)
		}
	}
	return out
}

// SetFeatures подключает источник флагов. Зовётся из wiring, когда сервисы
// маршрутизации уже собраны; nil — флаги не отправляются.
func (s *Service) SetFeatures(fn func() Features) {
	s.mu.Lock()
	s.features = fn
	s.mu.Unlock()
}

// statsHeaders — заголовки статистики для запроса проверки обновлений; nil,
// если статистика выключена или ID недоступен.
func (s *Service) statsHeaders() http.Header {
	if s.settings == nil {
		return nil
	}
	st, err := s.settings.Get()
	if err != nil || !st.Updates.StatsEnabled {
		return nil
	}
	// Под mu: первая плановая проверка и ручная из API не должны завести
	// каждая свой ID.
	s.mu.Lock()
	if s.instanceID == "" {
		id, err := storage.LoadOrCreateInstanceID(s.dataDir)
		if err != nil {
			s.mu.Unlock()
			s.appLog.Warn("stats", "", "ID установки недоступен: "+err.Error())
			return nil
		}
		s.instanceID = id
	}
	id, fn := s.instanceID, s.features
	s.mu.Unlock()
	h := http.Header{}
	h.Set(headerInstance, id)
	if fn != nil {
		h.Set(headerFeatures, strings.Join(fn().tokens(), ","))
	}
	return h
}
