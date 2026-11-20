package query

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

const keenDNSTTL = 60 * time.Second

// KeenDNSInfo holds the router's KeenDNS domain registration and active cloud relay endpoints.
type KeenDNSInfo struct {
	Domain  string `json:"domain"`
	Enabled bool   `json:"enabled"`
	// Address — IPv4 доступа к роутеру по имени KeenDNS. В режиме direct
	// статической записи у ndnproxy нет, и это единственный источник адреса
	// для обхода пресета keendns.
	Address string `json:"address"`
	// Address6 — то же для IPv6; пусто, если доступа по IPv6 нет.
	Address6 string `json:"address6"`
	// Access — режим доступа по имени. Значение `direct` означает прямой
	// доступ к белому статическому адресу роутера; ЛЮБОЕ другое значение —
	// доступ через прокси NDMS, который проксирует HTTP, а не произвольный
	// порт. Поэтому имя годится как адрес туннеля или ссылки ТОЛЬКО при
	// `direct` (F389, F392).
	Access       string   `json:"access"`
	RelayIPs     []string `json:"relay_ips,omitempty"`
	RelayDomains []string `json:"relay_domains,omitempty"`
	// RelayEndpoints retains the observed remote port for cloud TCP routing.
	// Keep it internal so existing KeenDNS API responses retain their shape.
	RelayEndpoints []netip.AddrPort `json:"-"`
}

// KeenDNSStatus provides detailed uncached KeenDNS status for External Access discovery and transactions.
type KeenDNSStatus struct {
	Enabled          bool   `json:"enabled"`
	Name             string `json:"name"`
	Domain           string `json:"domain"`
	FQDN             string `json:"fqdn"`
	Address          string `json:"address,omitempty"`
	DirectAccessible bool   `json:"directAccessible"`
	Mode             string `json:"mode"` // "direct", "cloud", or "disabled"
	Auto             bool   `json:"auto"`
	AcmeV2           bool   `json:"acmeV2"`
}

// KeenDNSAccessDirect — единственное значение Access, при котором по имени
// доступен произвольный порт.
const KeenDNSAccessDirect = "direct"

// DirectAccess — годится ли имя как адрес для туннеля или ссылки. Пустой info
// и любой не-direct режим дают false: ошибка в эту сторону оставляет
// измеренный IP (рабочий), в обратную — молча нерабочую конфигурацию.
func (i *KeenDNSInfo) DirectAccess() bool {
	return i != nil && strings.TrimSpace(i.Domain) != "" &&
		strings.TrimSpace(i.Access) == KeenDNSAccessDirect
}

// KeenDNSStore caches KeenDNS status from NDMS.
type KeenDNSStore struct {
	*cache.KeyedStore[string, *KeenDNSInfo]
	getter Getter
	log    Logger

	// absentUntil — до какого момента не спрашивать /show/ndns после 404.
	//
	// НЕ вечная защёлка: 404 не означает однозначно «подсистемы нет». В окне
	// старта ndm RCI отвечает 404 на всё подряд, и один такой промах запер бы
	// KeenDNS до перезапуска демона — а единственные потребители этих данных
	// (internal/api/server_peers.go) выдают клиентам .conf, и вместо
	// KeenDNS-имени туда уехал бы WAN-адрес. У пользователя с динамическим
	// адресом такой конфиг отваливается при смене IP.
	//
	// Бэкофф решает обе задачи: заведомо провальный GET больше не уходит раз
	// в минуту (и не пишет ERROR в журнал каждый раз), но прошивка с живой
	// подсистемой сама себя вылечит через absentBackoff.
	absentMu    sync.Mutex
	absentUntil time.Time
}

// absentBackoff — пауза после 404. Час: прошивка без KeenDNS не обретёт его
// за время работы процесса, так что для неё это «практически навсегда», а
// ложный 404 стартового окна стоит одного часа, а не всей сессии.
const absentBackoff = time.Hour

func NewKeenDNSStore(g Getter, log Logger) *KeenDNSStore {
	s := &KeenDNSStore{getter: g, log: log}
	s.KeyedStore = cache.NewKeyedStore(keenDNSTTL, log, "keendns", s.fetch)
	return s
}

// Get returns the current KeenDNS registration. Missing/unconfigured → nil, nil.
func (s *KeenDNSStore) Get(ctx context.Context) (*KeenDNSInfo, error) {
	return s.KeyedStore.Get(ctx, "status")
}

// GetStatus reads uncached KeenDNS status from `/show/ndns` (and `/show/rc/ndns` when available),
// preserving HTTP 404/403 errors so callers can distinguish unsupported firmware from unconfigured KeenDNS.
func (s *KeenDNSStore) GetStatus(ctx context.Context) (*KeenDNSStatus, error) {
	if s == nil || s.getter == nil {
		return nil, errors.New("keendns store not initialized")
	}
	raw, err := s.getter.GetRaw(ctx, "/show/ndns")
	if err != nil {
		return nil, err
	}
	var rt struct {
		Booked           string `json:"booked"`
		Domain           string `json:"domain"`
		Address          string `json:"address"`
		Access           string `json:"access"`
		DirectAccessible *bool  `json:"direct-accessible"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &rt)
	}

	var rc struct {
		Booked string `json:"booked"`
		Domain string `json:"domain"`
		Policy string `json:"policy"`
		Auto   bool   `json:"auto"`
		Proxy  struct {
			AcmeV2 bool `json:"acme-v2"`
		} `json:"proxy"`
	}
	if rcRaw, rcErr := s.getter.GetRaw(ctx, "/show/rc/ndns"); rcErr == nil && len(rcRaw) > 0 {
		_ = json.Unmarshal(rcRaw, &rc)
	}

	name := strings.TrimSpace(rt.Booked)
	if name == "" {
		name = strings.TrimSpace(rc.Booked)
	}
	zone := strings.TrimSpace(rt.Domain)
	if zone == "" {
		zone = strings.TrimSpace(rc.Domain)
	}
	if name == "" || zone == "" {
		return &KeenDNSStatus{
			Enabled: false,
			Mode:    "disabled",
		}, nil
	}

	access := strings.ToLower(strings.TrimSpace(rt.Access))
	direct := access == KeenDNSAccessDirect
	if rt.DirectAccessible != nil {
		direct = *rt.DirectAccessible
	} else if access == "" && strings.EqualFold(strings.TrimSpace(rc.Policy), "direct") {
		direct = true
	}

	mode := "cloud"
	if direct {
		mode = "direct"
	}

	return &KeenDNSStatus{
		Enabled:          true,
		Name:             name,
		Domain:           zone,
		FQDN:             name + "." + zone,
		Address:          strings.TrimSpace(rt.Address),
		DirectAccessible: direct,
		Mode:             mode,
		Auto:             rc.Auto,
		AcmeV2:           rc.Proxy.AcmeV2,
	}, nil
}

func (s *KeenDNSStore) fetch(ctx context.Context, _ string) (*KeenDNSInfo, error) {
	// Только /show/ndns — авторитетный эндпоинт KeenDNS на всех поддерживаемых
	// прошивках. 404 означает, что подсистема отсутствует на этой OS → KeenDNS
	// не настроен (а не ошибка), без него поллер сыпал бы ошибками каждый тик.
	if s.absentNow() {
		return nil, nil
	}
	raw, err := s.getter.GetRaw(ctx, "/show/ndns")
	if err != nil {
		var httpErr *transport.HTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			s.markAbsent()
			return nil, nil
		}
		return nil, err
	}
	return parseKeenDNS(raw), nil
}

func (s *KeenDNSStore) absentNow() bool {
	s.absentMu.Lock()
	defer s.absentMu.Unlock()
	return time.Now().Before(s.absentUntil)
}

func (s *KeenDNSStore) markAbsent() {
	s.absentMu.Lock()
	s.absentUntil = time.Now().Add(absentBackoff)
	s.absentMu.Unlock()
}

// parseKeenDNS строит FQDN доступа из полей booked + domain ответа /show/ndns
// (например booked="example", domain="crazedns.ru" → "example.crazedns.ru").
// Также извлекает активные реле-серверы из блока ttp.tunnel (IP-адреса и домены).
// Любой домен Keenetic покрывается автоматически — без allowlist суффиксов.
// Допущение: domain — это зона, а не уже готовый FQDN (verified на ребренд-OS,
// для стоковой *.keenetic.pro не перепроверялось).
func parseKeenDNS(raw []byte) *KeenDNSInfo {
	var v struct {
		Booked   string `json:"booked"`
		Domain   string `json:"domain"`
		Address  string `json:"address"`
		Address6 string `json:"address6"`
		Access   string `json:"access"`
		TTP      struct {
			Tunnel []struct {
				Target       string `json:"target"`
				TargetRemote string `json:"target-remote"`
			} `json:"tunnel"`
		} `json:"ttp"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	booked := strings.TrimSpace(v.Booked)
	domain := strings.TrimSpace(v.Domain)
	if booked == "" || domain == "" {
		return nil
	}

	var relayIPs []string
	var relayDomains []string
	var relayEndpoints []netip.AddrPort
	seenIP := make(map[string]struct{})
	seenDomain := make(map[string]struct{})
	seenEndpoint := make(map[netip.AddrPort]bool)

	for _, t := range v.TTP.Tunnel {
		if remote := strings.TrimSpace(t.TargetRemote); remote != "" {
			if endpoint, err := netip.ParseAddrPort(remote); err == nil && endpoint.Port() != 0 && endpoint.Addr().Zone() == "" {
				endpoint = netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
				if !seenEndpoint[endpoint] {
					seenEndpoint[endpoint] = true
					relayEndpoints = append(relayEndpoints, endpoint)
				}
			}
			host := remote
			if h, _, err := net.SplitHostPort(remote); err == nil {
				host = h
			}
			if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
				ipStr := ip.String()
				if _, ok := seenIP[ipStr]; !ok {
					seenIP[ipStr] = struct{}{}
					relayIPs = append(relayIPs, ipStr)
				}
			}
		}
		if target := strings.TrimSpace(t.Target); target != "" {
			host := target
			if h, _, err := net.SplitHostPort(target); err == nil {
				host = h
			}
			host = strings.ToLower(strings.TrimSpace(host))
			if host != "" {
				if _, ok := seenDomain[host]; !ok {
					seenDomain[host] = struct{}{}
					relayDomains = append(relayDomains, host)
				}
			}
		}
	}

	return &KeenDNSInfo{
		Domain:         booked + "." + domain,
		Enabled:        true,
		Address:        strings.TrimSpace(v.Address),
		Address6:       strings.TrimSpace(v.Address6),
		Access:         strings.TrimSpace(v.Access),
		RelayIPs:       relayIPs,
		RelayDomains:   relayDomains,
		RelayEndpoints: relayEndpoints,
	}
}
