package query

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

const keenDNSTTL = 60 * time.Second

// KeenDNSInfo holds the router's KeenDNS domain registration, if any.
type KeenDNSInfo struct {
	Domain  string `json:"domain"`
	Enabled bool   `json:"enabled"`
}

// KeenDNSStore caches KeenDNS status from NDMS.
type KeenDNSStore struct {
	*cache.KeyedStore[string, *KeenDNSInfo]
	getter Getter
	log    Logger
}

func NewKeenDNSStore(g Getter, log Logger) *KeenDNSStore {
	s := &KeenDNSStore{getter: g, log: log}
	s.KeyedStore = cache.NewKeyedStore(keenDNSTTL, log, "keendns", s.fetch)
	return s
}

// Get returns the current KeenDNS registration. Missing/unconfigured → nil, nil.
func (s *KeenDNSStore) Get(ctx context.Context) (*KeenDNSInfo, error) {
	return s.KeyedStore.Get(ctx, "status")
}

func (s *KeenDNSStore) fetch(ctx context.Context, _ string) (*KeenDNSInfo, error) {
	// Только /show/ndns — авторитетный эндпоинт KeenDNS на всех поддерживаемых
	// прошивках. 404 означает, что подсистема отсутствует на этой OS → KeenDNS
	// не настроен (а не ошибка), без него поллер сыпал бы ошибками каждый тик.
	raw, err := s.getter.GetRaw(ctx, "/show/ndns")
	if err != nil {
		var httpErr *transport.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return parseKeenDNS(raw), nil
}

// parseKeenDNS строит FQDN доступа из полей booked + domain ответа /show/ndns
// (например booked="impod", domain="crazedns.ru" → "impod.crazedns.ru").
// Любой домен Keenetic покрывается автоматически — без allowlist суффиксов.
// Допущение: domain — это зона, а не уже готовый FQDN (verified на ребренд-OS,
// для стоковой *.keenetic.pro не перепроверялось).
func parseKeenDNS(raw []byte) *KeenDNSInfo {
	var v struct {
		Booked string `json:"booked"`
		Domain string `json:"domain"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	booked := strings.TrimSpace(v.Booked)
	domain := strings.TrimSpace(v.Domain)
	if booked == "" || domain == "" {
		return nil
	}
	return &KeenDNSInfo{Domain: booked + "." + domain, Enabled: true}
}
