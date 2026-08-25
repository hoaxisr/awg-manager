// Package captcha — решатель VK-капчи FreeTurn на новой поверхности.
// Перенос internal/freeturn/captcha*.go: логика решателя (разбор журнала,
// владелец порта :8765, обратный прокси и переписывание страницы) прежняя,
// изменились только источники данных (записи инстансов и снимки control-сокета
// вместо конфига и супервизора старого мира) и адреса ручек.
package captcha

import (
	"net/url"
	"strings"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// DefaultPort — порт локального сервера капчи. Захардкожен в самом
// free-turn-proxy: он один на все инстансы, поэтому второй ожидающий клиент
// встаёт в очередь (PortContention), а не поднимает свой порт.
const DefaultPort = 8765

// RecordLister — перечень записей всех ролей (срез manager.Records).
// Канонический RecordSource задачи 8 умеет только Get по ключу, а обзор капчи
// обязан посчитать состояние по ВСЕМ freeturn-клиентам сразу: владелец порта
// один, и очередь без полного списка не построить.
type RecordLister interface {
	Records() []instancestore.Record
}

// Deps — источники решателя. Прод-реализации собирает проводка.
type Deps struct {
	// Records — запись инстанса по ключу (форма задачи 8).
	Records wdttlink.RecordSource
	// Instances — полный перечень записей (см. RecordLister).
	Instances RecordLister
	// Snapshots — снимок control-сокета инстанса (форма задачи 8): pid
	// процесса, по которому опознаётся владелец порта капчи.
	Snapshots wdttlink.Snapshots
	// Log — хвост журнала процесса по ключу инстанса; тот же источник, что у
	// ручки инстансов. Пусто (или nil) означает «журнала нет».
	Log func(key string) string
	// Listener — кто держит порт капчи (nil = настоящий зонд ОС).
	// ЕДИНСТВЕННЫЙ шов пакета с системой: без него ни обзор, ни гейт 503 не
	// проверяемы — ответ зависел бы от того, слушает ли машина :8765.
	Listener Listener
}

// Listener — владелец порта капчи среди кандидатов. Первый результат — pid
// владельца (0 = порт открыт, но владелец не из кандидатов), второй — открыт ли
// порт вообще.
type Listener func(candidatePIDs []int) (ownerPID int, open bool)

// Service — решатель капчи. Состояния не держит: каждый ответ считается из
// записей, снимков и журналов.
type Service struct {
	deps Deps
}

func New(d Deps) *Service { return &Service{deps: d} }

// ClientStatus describes captcha state for one freeturn-client instance.
type ClientStatus struct {
	// ClientID — КЛЮЧ инстанса (freeturn-client:default), а не его id: в новом
	// мире инстанс адресуется ключом, и по нему же строится url ниже.
	// Json-имя оставлено прежним (форма ответа не меняется).
	ClientID   string `json:"clientId"`
	ClientName string `json:"clientName"`
	// Waiting: client log indicates manual captcha is required (auto failed or -manual-captcha).
	Waiting bool `json:"waiting"`
	// Active: this client's process currently owns the local captcha HTTP server (:8765).
	Active bool `json:"active"`
	// Queued: waiting but another client holds :8765 — solve the active one first.
	Queued bool `json:"queued"`
	// CanOpen: UI may open the proxied captcha page for this client.
	CanOpen bool   `json:"canOpen"`
	URL     string `json:"url,omitempty"`
	// PendingStreams: VK-auth streams in this client still waiting on manual captcha.
	PendingStreams int `json:"pendingStreams,omitempty"`
	// PortContention: manual captcha port :8765 is busy with another stream's session.
	PortContention bool `json:"portContention,omitempty"`
	// CaptchaSession: manual captcha (re)starts in this process run — bump to reload iframe.
	CaptchaSession int `json:"captchaSession,omitempty"`
}

// Overview aggregates captcha state across all client instances.
type Overview struct {
	PortOpen      bool           `json:"portOpen"`
	OwnerClientID string         `json:"ownerClientId,omitempty"`
	OwnerName     string         `json:"ownerName,omitempty"`
	Clients       []ClientStatus `json:"clients"`
}

var logMarkers = []string{
	"MANUAL CAPTCHA SOLVING NEEDED",
	"Triggering manual captcha fallback",
	"ACTION REQUIRED: MANUAL CAPTCHA",
}

// Lines after a captcha marker that mean the challenge was solved and the client moved on.
var resolvedMarkers = []string{
	"[Captcha] received success token from browser",
	"Established DTLS connection",
}

// logIndicatesWaiting reports whether recent process output shows that
// manual captcha solving is in progress. Only the tail is checked so an old
// captcha banner from a previous run does not keep the UI open forever.
// If a captcha marker is followed by a resolved marker (token received, DTLS
// up), the client is no longer waiting even though the marker remains in log.
func logIndicatesWaiting(log string) bool {
	if log == "" {
		return false
	}
	if summary := analyzeLog(log); summary.PendingStreams > 0 {
		return true
	}
	lines := logTailLines(log, 40)
	lastMarker := -1
	for i, line := range lines {
		for _, marker := range logMarkers {
			if strings.Contains(line, marker) {
				lastMarker = i
			}
		}
	}
	if lastMarker < 0 {
		return false
	}
	for _, line := range lines[lastMarker+1:] {
		for _, resolved := range resolvedMarkers {
			if strings.Contains(line, resolved) {
				return false
			}
		}
	}
	return true
}

func logTailLines(log string, maxLines int) []string {
	lines := strings.Split(strings.TrimSpace(log), "\n")
	if maxLines <= 0 || len(lines) <= maxLines {
		return lines
	}
	return lines[len(lines)-maxLines:]
}

// proxyPath — путь страницы капчи инстанса. Ключ содержит двоеточие
// (freeturn-client:default); url.PathEscape его НЕ экранирует — в сегменте
// пути двоеточие законно (RFC 3986, pchar), и ссылка остаётся читаемой.
func proxyPath(key string) string {
	return instancesPath + url.PathEscape(key) + "/captcha/"
}

// Status inspects freeturn-client instances, their logs and the shared captcha
// listen port (8765 — hardcoded in upstream free-turn-proxy).
func (s *Service) Status() Overview {
	recs := s.freeturnClients()

	candidatePIDs := make([]int, 0, len(recs))
	logs := make([]string, len(recs))
	running := make([]bool, len(recs))
	pids := make([]int, len(recs))
	for i, rec := range recs {
		snap, ok := s.snapshot(rec.Key())
		running[i] = ok && snap.PID > 0
		pids[i] = snap.PID
		logs[i] = s.log(rec.Key())
		if running[i] {
			candidatePIDs = append(candidatePIDs, snap.PID)
		}
	}

	ownerPID, portOpen := s.listener()(candidatePIDs)

	out := Overview{
		PortOpen: portOpen,
		Clients:  make([]ClientStatus, 0, len(recs)),
	}

	waitingIDs := make([]string, 0, len(recs))
	for i, rec := range recs {
		summary := analyzeLog(logs[i])
		waiting := running[i] && logIndicatesWaiting(logs[i])
		active := portOpen && running[i] && pids[i] != 0 && pids[i] == ownerPID

		if active {
			out.OwnerClientID = rec.Key()
			out.OwnerName = rec.Name
		}
		if waiting {
			waitingIDs = append(waitingIDs, rec.Key())
		}

		out.Clients = append(out.Clients, ClientStatus{
			ClientID:       rec.Key(),
			ClientName:     rec.Name,
			Waiting:        waiting,
			Active:         active,
			PendingStreams: summary.PendingStreams,
			PortContention: summary.PortContention,
			CaptchaSession: summary.CaptchaSession,
		})
	}

	multipleWaiting := len(waitingIDs) > 1
	for i := range out.Clients {
		c := &out.Clients[i]
		switch {
		case !portOpen || !c.Waiting:
			continue
		case c.Active:
			c.CanOpen = true
			c.URL = proxyPath(c.ClientID)
		case multipleWaiting && out.OwnerClientID != "" && out.OwnerClientID != c.ClientID:
			c.Queued = true
		default:
			// Single waiting client, or owner PID could not be resolved.
			c.CanOpen = true
			c.URL = proxyPath(c.ClientID)
		}
	}

	return out
}

// StatusForKey returns one instance's slice of Overview.
func (s *Service) StatusForKey(key string) (ClientStatus, bool) {
	all := s.Status()
	for _, c := range all.Clients {
		if c.ClientID == key {
			return c, true
		}
	}
	return ClientStatus{}, false
}

// freeturnClients — записи freeturn-клиентов; у прочих ролей капчи нет.
func (s *Service) freeturnClients() []instancestore.Record {
	if s.deps.Instances == nil {
		return nil
	}
	all := s.deps.Instances.Records()
	out := make([]instancestore.Record, 0, len(all))
	for _, rec := range all {
		if rec.Kind == instancestore.KindFreeTurnClient {
			out = append(out, rec)
		}
	}
	return out
}

func (s *Service) snapshot(key string) (awgmproto.State, bool) {
	if s.deps.Snapshots == nil {
		return awgmproto.State{}, false
	}
	return s.deps.Snapshots(key)
}

func (s *Service) log(key string) string {
	if s.deps.Log == nil {
		return ""
	}
	return s.deps.Log(key)
}

// portOpen is a cheap probe used before proxying.
func (s *Service) portOpen() bool {
	_, ok := s.listener()(nil)
	return ok
}

func (s *Service) listener() Listener {
	if s.deps.Listener == nil {
		return listenerPIDAmong
	}
	return s.deps.Listener
}

func listenerPIDAmong(candidatePIDs []int) (int, bool) {
	return socketListenerPIDAmong("127.0.0.1", DefaultPort, candidatePIDs)
}
