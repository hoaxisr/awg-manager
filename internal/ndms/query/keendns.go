package query

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

const keenDNSTTL = 60 * time.Second

// KeenDNSInfo holds the router's KeenDNS domain registration, if any.
type KeenDNSInfo struct {
	Domain  string `json:"domain"`
	Enabled bool   `json:"enabled"`
	// Address — IPv4 доступа к роутеру по имени KeenDNS. В режиме direct
	// статической записи у ndnproxy нет, и это единственный источник адреса
	// для обхода пресета keendns.
	Address string `json:"address"`
	// Access — режим доступа по имени. Значение `direct` означает прямой
	// доступ к белому статическому адресу роутера; ЛЮБОЕ другое значение —
	// доступ через прокси NDMS, который проксирует HTTP, а не произвольный
	// порт. Поэтому имя годится как адрес туннеля или ссылки ТОЛЬКО при
	// `direct` (F389, F392).
	Access string `json:"access"`
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

// parseKeenDNS строит FQDN доступа из полей booked + domain ответа /show/ndns
// (например booked="example", domain="crazedns.ru" → "example.crazedns.ru").
// Любой домен Keenetic покрывается автоматически — без allowlist суффиксов.
// Допущение: domain — это зона, а не уже готовый FQDN (verified на ребренд-OS,
// для стоковой *.keenetic.pro не перепроверялось).
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

func parseKeenDNS(raw []byte) *KeenDNSInfo {
	var v struct {
		Booked  string `json:"booked"`
		Domain  string `json:"domain"`
		Address string `json:"address"`
		Access  string `json:"access"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	booked := strings.TrimSpace(v.Booked)
	domain := strings.TrimSpace(v.Domain)
	if booked == "" || domain == "" {
		return nil
	}
	return &KeenDNSInfo{
		Domain:  booked + "." + domain,
		Enabled: true,
		Address: strings.TrimSpace(v.Address),
		Access:  strings.TrimSpace(v.Access),
	}
}
