package query

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

// StaticRouteEntry — запись /show/rc/ip/route (стенд 5.02.A.11, 27.09.2026):
// сетевая форма {network, mask, interface, auto, index, comment}, host-форма
// несёт host вместо network+mask. В отличие от /show/ip/route здесь есть
// комментарий и записи опущенных интерфейсов — по нему сверяется владение
// маршрутами сетей за клиентом (#713).
type StaticRouteEntry struct {
	Network   string `json:"network"`
	Mask      string `json:"mask"`
	Host      string `json:"host"`
	Interface string `json:"interface"`
	Comment   string `json:"comment"`
}

// IPv4Net — сеть записи; host-форма — /32. Записи без читаемой IPv4-сети
// (неканоническая маска, не IPv4) — nil.
func (e StaticRouteEntry) IPv4Net() *net.IPNet {
	if e.Host != "" {
		if ip := net.ParseIP(e.Host).To4(); ip != nil {
			return &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}
		}
		return nil
	}
	ip, m := net.ParseIP(e.Network).To4(), net.ParseIP(e.Mask).To4()
	if ip == nil || m == nil {
		return nil
	}
	mask := net.IPMask(m)
	if ones, bits := mask.Size(); bits == 0 && ones == 0 {
		return nil
	}
	return &net.IPNet{IP: ip.Mask(mask), Mask: mask}
}

const staticRouteTTL = 30 * time.Second

// StaticRouteStore кэширует /show/rc/ip/route; сбрасывается RouteCommands
// после каждой мутации маршрутов и managed.rciPost.
type StaticRouteStore struct {
	*cache.ListStore[[]StaticRouteEntry]
	getter Getter
}

func NewStaticRouteStore(g Getter, log Logger) *StaticRouteStore {
	s := &StaticRouteStore{getter: g}
	s.ListStore = cache.NewListStore(staticRouteTTL, log, "ip-route-rc", s.fetch)
	return s
}

// fetch: массив, `[]` или одиночный объект — как у StaticNATStore.
func (s *StaticRouteStore) fetch(ctx context.Context) ([]StaticRouteEntry, error) {
	raw, err := s.getter.GetRaw(ctx, "/show/rc/ip/route")
	if err != nil {
		return nil, fmt.Errorf("fetch ip route rc: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var entries []StaticRouteEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		var single StaticRouteEntry
		if err2 := json.Unmarshal(raw, &single); err2 != nil {
			return nil, fmt.Errorf("decode ip route rc: %w", err)
		}
		if single.Interface != "" {
			return []StaticRouteEntry{single}, nil
		}
		return nil, nil
	}
	return entries, nil
}
