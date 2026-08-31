package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/manager"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// proxyrtInstancesPath — база поверхности управления прокси-инстансами.
// Неймспейс /api/proxy/* ЗАНЯТ соседней подсистемой (прокси для LAN-устройств,
// server_routes.go), а дубликат паттерна в http.ServeMux — паника на старте
// демона, поэтому новый мир живёт под /api/proxyrt/.
const proxyrtInstancesPath = "/api/proxyrt/instances"

// proxyrtListenMovesPath — уведомления о переезде listen-порта. Отдельный
// путь, а не действие инстанса: переезды переживают своих инстансов и
// снимаются все разом.
const proxyrtListenMovesPath = "/api/proxyrt/seed/listen-moves"

// Коды ошибок поверхности. PROXY_NOT_SEEDED и PROXY_DECLARE_FAILED предписаны
// планом (требования 15 и 17); остальные два заведены здесь под два гейта
// создания, у которых своя причина отказа и свой текст для пользователя.
const (
	proxyCodeNotSeeded       = "PROXY_NOT_SEEDED"
	proxyCodeDeclareFailed   = "PROXY_DECLARE_FAILED"
	proxyCodeNotFound        = "NOT_FOUND"
	proxyCodeConfigInvalid   = "PROXY_CONFIG_INVALID"
	proxyCodeOpkgUnsupported = "PROXY_OPKGTUN_UNSUPPORTED"
)

// ProxyManager — узкий срез *manager.Manager, нужный поверхности.
type ProxyManager interface {
	Records() []instancestore.Record
	Create(ctx context.Context, rec instancestore.Record) error
	Update(ctx context.Context, key string, mutate func(*instancestore.Record) error) error
	SetEnabled(ctx context.Context, key string, on bool) error
	Delete(ctx context.Context, key string) error
	Post(key string, k proxyrt.EventKind) bool
	SeedInfo() manager.SeedInfo
	AckListenMoves() error
}

// StateLister — срез *proxyrt.StateStore: состояние реконсиляции по инстансам.
// Ключ здесь — instancestore.Record.Key(), потому что проводка собирает
// instance.Config{ID: rec.Key()}.
type StateLister interface {
	List() []proxyrt.InstanceState
	Get(id string) (proxyrt.InstanceState, bool)
}

// ── DTO ответа ───────────────────────────────────────────────────

// ProcessView — process-блок ответа инстанса. Источники: снимок control-сокета
// (awgmproto.State), tail файла журнала форка (control.LogPath), install-пакет.
type ProcessView struct {
	Running       bool   `json:"running"` // НЕ поле State (его там нет): снимок есть и State.PID > 0
	PID           int    `json:"pid,omitempty"`
	Address       string `json:"address,omitempty"` // требование 5: адрес из НАБЛЮДЕНИЯ
	UptimeS       int64  `json:"uptimeS,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	Mode          string `json:"mode,omitempty"`
	WgConfig      string `json:"wgConfig,omitempty"` // State.WG.Config — источник ensure-wg
	Clients       *int   `json:"clients,omitempty"`  // dtlsConnections старого мира
	Log           string `json:"log,omitempty"`      // tail журнала процесса (форк пишет файл)
	Binary        string `json:"binary"`
	BinaryPresent bool   `json:"binaryPresent"`
}

// ProxyRtSeedView — состояние посева (Щ8, требование 17). Два РАЗНЫХ признака:
// Seeded — запуск подсистемы состоялся, Certified — посев подтверждён реестру
// и уборка разрешена. Слить их в один нельзя: «гейт заперт, уборка не
// работает» (seeded=true, certified=false) обязано быть видно снаружи.
type ProxyRtSeedView struct {
	Seeded    bool   `json:"seeded" example:"true"`
	Certified bool   `json:"certified" example:"true"`
	Error     string `json:"error,omitempty" example:"посев отложен: RCI недоступен"`
	// Skipped — старые конфиги, которые посев не разобрал и пропустил: их
	// инстансы не перенесены. Признак отдельный от Error: только по имени
	// файла интерфейс может сказать, ЧЬИ инстансы потеряны.
	Skipped []ProxyRtSkippedSourceView `json:"skipped,omitempty"`
	// MovedListen — инстансы, которым посев сменил listen-адрес, разводя
	// конфликт за порт. Молчать нельзя: снаружи мог быть настроен клиент на
	// прежний порт.
	MovedListen []ProxyRtListenMoveView `json:"movedListen,omitempty"`
}

// ProxyRtSkippedSourceView — один пропущенный старый конфиг.
type ProxyRtSkippedSourceView struct {
	File   string `json:"file" example:"wdtt.json"`
	Reason string `json:"reason,omitempty" example:"invalid character 'н'"`
}

// ProxyRtListenMoveView — один переезд listen-адреса, сделанный посевом.
type ProxyRtListenMoveView struct {
	Instance string `json:"instance" example:"freeturn-client:default"`
	Name     string `json:"name,omitempty" example:"Клиент"`
	From     string `json:"from" example:"127.0.0.1:9000"`
	To       string `json:"to" example:"127.0.0.1:9001"`
}

// ProxyRtResourceView — один ресурс инстанса в состоянии реконсиляции.
type ProxyRtResourceView struct {
	ID     string `json:"id" example:"ndms_iface"`
	Status string `json:"status" example:"ok"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ProxyRtStepView — шаг последнего плана.
type ProxyRtStepView struct {
	Resource string            `json:"resource" example:"ndms_iface"`
	Op       string            `json:"op" example:"create"`
	Args     map[string]string `json:"args,omitempty"`
	Reason   string            `json:"reason,omitempty"`
}

// ProxyRtStateView — proxyrt.InstanceState наружу. Собственная форма, а не
// прямая сериализация: у InstanceState json-тегов нет, и без вида ответ
// зависел бы от имён полей Go.
type ProxyRtStateView struct {
	Intent    string                `json:"intent" example:"enabled"`
	Phase     string                `json:"phase" example:"settled"`
	Resources []ProxyRtResourceView `json:"resources,omitempty"`
	LastPlan  []ProxyRtStepView     `json:"lastPlan,omitempty"`
	UpdatedAt string                `json:"updatedAt,omitempty" example:"2026-08-24T12:00:00Z"`
}

// ProxyRtInstanceView — запись инстанса + состояние + процесс.
//
// Абонентов сервера (Record.Users) здесь НЕТ намеренно: их отдаёт своя ручка
// (задача 9), которая одна умеет посчитать живые признаки — истёк, отозван,
// автоматический — и знает пароль, ключ всех операций над абонентом. Урезанный
// блок здесь был бы приманкой: из него собралась бы таблица, в которой
// истёкшие абоненты выглядят живыми.
//
// Config отдаётся ОДНИМ объектом (а не под ключом роли, как на диске): та же
// форма, что принимают POST/PATCH, — иначе фронт собирал бы тело правки из
// другого места, чем читает.
type ProxyRtInstanceView struct {
	Key       string `json:"key" example:"wdtt-client:default"`
	ID        string `json:"id" example:"default"`
	Kind      string `json:"kind" example:"wdtt-client"`
	Name      string `json:"name" example:"Нидерланды"`
	Enabled   bool   `json:"enabled" example:"true"`
	CreatedAt string `json:"createdAt,omitempty"`
	// SeededFrom — имя старого конфига, из которого запись перенёс посев.
	// Пусто у заведённых через UI. UI показывает по нему бейдж «перенесено».
	SeededFrom   string            `json:"seededFrom,omitempty"`
	Sub          string            `json:"sub,omitempty"`
	PeerWg       string            `json:"peerWg,omitempty"`
	PeerRaw      string            `json:"peerRaw,omitempty"`
	LinkPeer     string            `json:"linkPeer,omitempty"`
	LinkVKHashes string            `json:"linkVkHashes,omitempty"`
	StatsLog     string            `json:"statsLog,omitempty"`
	Config       map[string]any    `json:"config" swaggertype:"object"`
	State        *ProxyRtStateView `json:"state,omitempty"`
	Process      ProcessView       `json:"process"`
}

// ProxyRtListData — тело GET /proxyrt/instances.
type ProxyRtListData struct {
	Seed      ProxyRtSeedView       `json:"seed"`
	Instances []ProxyRtInstanceView `json:"instances"`
}

// ProxyRtListResponse — конверт списка инстансов.
type ProxyRtListResponse struct {
	Success bool            `json:"success" example:"true"`
	Data    ProxyRtListData `json:"data"`
}

// ProxyRtInstanceResponse — конверт одного инстанса.
type ProxyRtInstanceResponse struct {
	Success bool                `json:"success" example:"true"`
	Data    ProxyRtInstanceView `json:"data"`
}

// ── DTO запроса ──────────────────────────────────────────────────

// ProxyRtCreateRequest — тело POST /proxyrt/instances. Пустой id менеджеру не
// отдаётся: его подставляет хендлер. listen и пины присылать не нужно — их
// выделяет менеджер.
type ProxyRtCreateRequest struct {
	ID      string          `json:"id" example:"default"`
	Kind    string          `json:"kind" example:"wdtt-client"`
	Name    string          `json:"name" example:"Нидерланды"`
	Enabled bool            `json:"enabled" example:"true"`
	Config  json.RawMessage `json:"config" swaggertype:"object"`
}

// ProxyRtPatchRequest — тело PATCH /proxyrt/instances/{key}. Указатели, а не
// значения: отсутствие поля обязано отличаться от присланного нуля, иначе
// правка одного имени выключала бы инстанс.
//
// Sub и StatsLog живут на самой ЗАПИСИ, а не в конфиге роли, и без них у обоих
// полей не было писателя вовсе: подписка wdtt-клиента терялась при импорте, а
// режим журнала статистики сервера не переключался. Семантика у них НЕ
// секретная (пустое ≠ «не менять»): пустая строка — законное значение,
// «подписки больше нет» и «режим журнала по умолчанию», поэтому различие
// «поля нет» / «поле пустое» несёт указатель.
type ProxyRtPatchRequest struct {
	Name    *string         `json:"name,omitempty" example:"Нидерланды"`
	Enabled *bool           `json:"enabled,omitempty" example:"true"`
	Config  json.RawMessage `json:"config,omitempty" swaggertype:"object"`
	// Sub — URL подписки wdtt-клиента. Пустая строка снимает подписку.
	Sub *string `json:"sub,omitempty" example:"https://example.org/sub"`
	// StatsLog — режим журнала статистики wdtt-сервера: ram|off|disk.
	// Пустая строка означает дефолт (ram — журнал в tmpfs, не на флеш).
	StatsLog *string `json:"statsLog,omitempty" example:"ram"`
}

// ── хендлер ──────────────────────────────────────────────────────

// ProxyInstancesDeps — зависимости поверхности. Manager и States обязательны;
// остальные три поставляет проводка, и до неё поверхность работает без них
// (снимка процесса нет, журнал пуст, бинарь неизвестен).
type ProxyInstancesDeps struct {
	Manager ProxyManager
	States  StateLister
	// Snapshot — снимок управляющего сокета инстанса по его ключу.
	Snapshot func(key string) (awgmproto.State, bool)
	// Log — хвост файла журнала форка (~200 строк).
	Log func(key string) string
	// BinaryInfo — путь бинаря роли и его наличие (proxyapp/install).
	BinaryInfo func(kind instancestore.Kind) (path string, present bool)
	// OpkgTunSupported — поддерживает ли прошивка интерфейсы OpkgTun. nil
	// означает «гейта нет»: до проводки поверхность не имеет права запрещать
	// то, чего не умеет проверить.
	OpkgTunSupported func() bool
}

// ProxyInstancesHandler обслуживает /api/proxyrt/instances*.
type ProxyInstancesHandler struct {
	deps ProxyInstancesDeps
}

func NewProxyInstancesHandler(d ProxyInstancesDeps) *ProxyInstancesHandler {
	return &ProxyInstancesHandler{deps: d}
}

// Handle разбирает хвост пути вручную: в дереве нет ни одного wildcard-паттерна
// и ни одного PathValue, поэтому регистрируются оба паттерна — точный путь и
// путь со слэшем. Двоеточие в ключе (wdtt-client:default) сегменту пути
// законно и разбора не требует.
func (h *ProxyInstancesHandler) Handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path != proxyrtInstancesPath && !strings.HasPrefix(path, proxyrtInstancesPath+"/") {
		response.ErrorWithStatus(w, http.StatusNotFound, "неизвестный путь", proxyCodeNotFound)
		return
	}
	tail := strings.Trim(strings.TrimPrefix(path, proxyrtInstancesPath), "/")
	if tail == "" {
		switch r.Method {
		case http.MethodGet:
			h.list(w, r)
		case http.MethodPost:
			h.create(w, r)
		default:
			response.MethodNotAllowed(w)
		}
		return
	}

	key, action, _ := strings.Cut(tail, "/")
	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			h.get(w, r, key)
		case http.MethodPatch:
			h.patch(w, r, key)
		case http.MethodDelete:
			h.remove(w, r, key)
		default:
			response.MethodNotAllowed(w)
		}
	case "apply":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		h.apply(w, key)
	default:
		response.ErrorWithStatus(w, http.StatusNotFound, "неизвестный путь", proxyCodeNotFound)
	}
}

// list — GET /api/proxyrt/instances
//
//	@Summary		Список прокси-инстансов
//	@Description	Записи намерения, состояние реконсиляции, снимок процесса и блок посева.
//	@Description	При seeded=false список пуст, а причина лежит в seed.error.
//	@Tags			proxyrt
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	ProxyRtListResponse
//	@Router			/proxyrt/instances [get]
func (h *ProxyInstancesHandler) list(w http.ResponseWriter, _ *http.Request) {
	info := h.deps.Manager.SeedInfo()
	seed := ProxyRtSeedView{Seeded: info.Booted, Certified: info.Certified, Error: info.Err}
	for _, s := range info.Skipped {
		seed.Skipped = append(seed.Skipped, ProxyRtSkippedSourceView{File: s.File, Reason: s.Reason})
	}
	for _, mv := range info.MovedListen {
		seed.MovedListen = append(seed.MovedListen, ProxyRtListenMoveView{
			Instance: mv.Instance, Name: mv.Name, From: mv.From, To: mv.To})
	}
	data := ProxyRtListData{
		Seed:      seed,
		Instances: []ProxyRtInstanceView{},
	}
	// Инстансы при несостоявшемся посеве не показываем: их состав неполон, а
	// причина тупика уже видна в блоке seed (Р9).
	if info.Booted {
		states := map[string]proxyrt.InstanceState{}
		if h.deps.States != nil {
			for _, st := range h.deps.States.List() {
				states[st.ID] = st
			}
		}
		recs := h.deps.Manager.Records()
		sort.Slice(recs, func(i, j int) bool { return recs[i].Key() < recs[j].Key() })
		for _, rec := range recs {
			var st *proxyrt.InstanceState
			if s, ok := states[rec.Key()]; ok {
				st = &s
			}
			data.Instances = append(data.Instances, h.viewOf(rec, st))
		}
	}
	response.Success(w, data)
}

// get — GET /api/proxyrt/instances/{key}
//
//	@Summary		Один прокси-инстанс
//	@Tags			proxyrt
//	@Produce		json
//	@Security		CookieAuth
//	@Param			key	path		string	true	"Ключ инстанса (роль:id)"
//	@Success		200	{object}	ProxyRtInstanceResponse
//	@Failure		404	{object}	APIErrorEnvelope
//	@Router			/proxyrt/instances/{key} [get]
func (h *ProxyInstancesHandler) get(w http.ResponseWriter, _ *http.Request, key string) {
	rec, ok := h.recordByKey(key)
	if !ok {
		h.notFound(w, key)
		return
	}
	var st *proxyrt.InstanceState
	if h.deps.States != nil {
		if s, found := h.deps.States.Get(key); found {
			st = &s
		}
	}
	response.Success(w, h.viewOf(rec, st))
}

// create — POST /api/proxyrt/instances
//
//	@Summary		Создать прокси-инстанс
//	@Description	listen и пины интерфейсов выделяет менеджер — присылать их не нужно.
//	@Tags			proxyrt
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			request	body		ProxyRtCreateRequest	true	"Роль, имя, намерение и конфиг"
//	@Success		200		{object}	ProxyRtInstanceResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		422		{object}	APIErrorEnvelope
//	@Failure		503		{object}	APIErrorEnvelope
//	@Router			/proxyrt/instances [post]
func (h *ProxyInstancesHandler) create(w http.ResponseWriter, r *http.Request) {
	if !h.requireSeeded(w) {
		return
	}
	var req ProxyRtCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "невалидный JSON запроса: "+err.Error())
		return
	}
	rec := instancestore.Record{
		ID:      strings.TrimSpace(req.ID),
		Kind:    instancestore.Kind(strings.TrimSpace(req.Kind)),
		Name:    strings.TrimSpace(req.Name),
		Enabled: req.Enabled,
	}
	if err := proxyApplyConfig(&rec, req.Config); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if rec.ID == "" {
		rec.ID = h.freeID(rec.Kind)
	}
	if err := h.gateCheck(rec); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.deps.Manager.Create(r.Context(), rec); err != nil {
		h.fail(w, err)
		return
	}
	h.respondRecord(w, rec.Key())
}

// patch — PATCH /api/proxyrt/instances/{key}
//
//	@Summary		Изменить прокси-инстанс
//	@Description	Присланные поля применяются к существующей записи ПО МЕСТУ.
//	@Description	Секретные поля (password/obfKey) без значения или с пустым
//	@Description	значением означают «не менять».
//	@Description	sub и statsLog — поля записи: отсутствие означает «не менять»,
//	@Description	пустая строка — законное значение (снять подписку / дефолтный режим).
//	@Tags			proxyrt
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			key		path		string				true	"Ключ инстанса (роль:id)"
//	@Param			request	body		ProxyRtPatchRequest	true	"Изменяемые поля"
//	@Success		200		{object}	ProxyRtInstanceResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		422		{object}	APIErrorEnvelope
//	@Failure		503		{object}	APIErrorEnvelope
//	@Router			/proxyrt/instances/{key} [patch]
func (h *ProxyInstancesHandler) patch(w http.ResponseWriter, r *http.Request, key string) {
	if !h.requireSeeded(w) {
		return
	}
	if _, ok := h.recordByKey(key); !ok {
		h.notFound(w, key)
		return
	}
	var req ProxyRtPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "невалидный JSON запроса: "+err.Error())
		return
	}
	cfg, err := proxyPruneBlankSecrets(req.Config)
	if err != nil {
		response.BadRequest(w, "невалидный конфиг: "+err.Error())
		return
	}

	// Только намерение — отдельным вызовом: это самая частая правка (тумблер),
	// и у менеджера для неё есть своя точка входа. Условие перечисляет ВСЕ
	// прочие поля тела: забытое поле уехало бы в эту ветку и потерялось молча.
	if req.Name == nil && len(cfg) == 0 && req.Sub == nil && req.StatsLog == nil && req.Enabled != nil {
		if err := h.deps.Manager.SetEnabled(r.Context(), key, *req.Enabled); err != nil {
			h.fail(w, err)
			return
		}
		h.respondRecord(w, key)
		return
	}

	err = h.deps.Manager.Update(r.Context(), key, func(rec *instancestore.Record) error {
		// Правка ПО МЕСТУ (замечание 11): пересборка записи литералом молча
		// потеряла бы CreatedAt/Sub/Users/LinkPeer и слоты адресов.
		if req.Name != nil {
			rec.Name = strings.TrimSpace(*req.Name)
		}
		if req.Enabled != nil {
			rec.Enabled = *req.Enabled
		}
		if req.Sub != nil {
			rec.Sub = strings.TrimSpace(*req.Sub)
		}
		if req.StatsLog != nil {
			rec.StatsLog = strings.TrimSpace(*req.StatsLog)
		}
		if err := proxyApplyConfig(rec, cfg); err != nil {
			return err
		}
		// Гейты считаются от СЛИТОГО конфига и внутри мутатора: их отказ
		// отменяет запись, диск не тронут.
		return h.gateCheck(*rec)
	})
	if err != nil {
		h.fail(w, err)
		return
	}
	h.respondRecord(w, key)
}

// remove — DELETE /api/proxyrt/instances/{key}
//
//	@Summary		Удалить прокси-инстанс
//	@Description	Порядок сноса (teardown → ожидание → запись → уборка) держит менеджер.
//	@Tags			proxyrt
//	@Produce		json
//	@Security		CookieAuth
//	@Param			key	path		string	true	"Ключ инстанса (роль:id)"
//	@Success		200	{object}	OkResponse
//	@Failure		404	{object}	APIErrorEnvelope
//	@Failure		422	{object}	APIErrorEnvelope
//	@Failure		503	{object}	APIErrorEnvelope
//	@Router			/proxyrt/instances/{key} [delete]
func (h *ProxyInstancesHandler) remove(w http.ResponseWriter, r *http.Request, key string) {
	if !h.requireSeeded(w) {
		return
	}
	if _, ok := h.recordByKey(key); !ok {
		h.notFound(w, key)
		return
	}
	if err := h.deps.Manager.Delete(r.Context(), key); err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, OkData{Ok: true})
}

// AckListenMoves — DELETE /api/proxyrt/seed/listen-moves
//
//	@Summary		Снять уведомления о переезде listen-порта
//	@Description	Пользователь прочитал сообщение о разведении портов при посеве.
//	@Description	Без признания плашка висела бы вечно: посев не повторяется.
//	@Tags			proxyrt
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/proxyrt/seed/listen-moves [delete]
func (h *ProxyInstancesHandler) AckListenMoves(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != proxyrtListenMovesPath {
		response.ErrorWithStatus(w, http.StatusNotFound, "неизвестный путь", proxyCodeNotFound)
		return
	}
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.deps.Manager.AckListenMoves(); err != nil {
		h.fail(w, err)
		return
	}
	response.Success(w, OkData{Ok: true})
}

// apply — POST /api/proxyrt/instances/{key}/apply
//
//	@Summary		Разбудить реконсиляцию инстанса
//	@Tags			proxyrt
//	@Produce		json
//	@Security		CookieAuth
//	@Param			key	path		string	true	"Ключ инстанса (роль:id)"
//	@Success		200	{object}	OkResponse
//	@Failure		404	{object}	APIErrorEnvelope
//	@Failure		503	{object}	APIErrorEnvelope
//	@Router			/proxyrt/instances/{key}/apply [post]
func (h *ProxyInstancesHandler) apply(w http.ResponseWriter, key string) {
	if !h.requireSeeded(w) {
		return
	}
	// Post=false означает «живого инстанса с таким ключом нет». Молчаливое «ок»
	// здесь — худший исход: пользователь считал бы, что применение запрошено.
	if !h.deps.Manager.Post(key, proxyrt.EventIntentChanged) {
		response.ErrorWithStatus(w, http.StatusNotFound,
			"инстанс "+key+" не запущен: будить нечего", proxyCodeNotFound)
		return
	}
	response.Success(w, OkData{Ok: true})
}

// ── общее ────────────────────────────────────────────────────────

func (h *ProxyInstancesHandler) requireSeeded(w http.ResponseWriter) bool {
	info := h.deps.Manager.SeedInfo()
	if info.Booted {
		return true
	}
	msg := "прокси-подсистема не загружена: мутации отклоняются"
	if info.Err != "" {
		msg += " (" + info.Err + ")"
	}
	response.ErrorWithStatus(w, http.StatusServiceUnavailable, msg, proxyCodeNotSeeded)
	return false
}

func (h *ProxyInstancesHandler) notFound(w http.ResponseWriter, key string) {
	response.ErrorWithStatus(w, http.StatusNotFound, "инстанс "+key+" не найден", proxyCodeNotFound)
}

// fail — единственная точка отказа мутации. Отказ гейта несёт свой код; всё
// остальное, что вернул менеджер, — это отказ ДО записи на диск, и ведущая его
// причина — отвергнутое объявление выходов (требование 15).
func (h *ProxyInstancesHandler) fail(w http.ResponseWriter, err error) {
	var ge *proxyGateError
	if errors.As(err, &ge) {
		response.ErrorWithStatus(w, http.StatusUnprocessableEntity, ge.msg, ge.code)
		return
	}
	response.ErrorWithStatus(w, http.StatusUnprocessableEntity, err.Error(), proxyCodeDeclareFailed)
}

func (h *ProxyInstancesHandler) recordByKey(key string) (instancestore.Record, bool) {
	for _, rec := range h.deps.Manager.Records() {
		if rec.Key() == key {
			return rec, true
		}
	}
	return instancestore.Record{}, false
}

// respondRecord отдаёт запись после мутации, перечитывая её у менеджера:
// listen и пины интерфейсов выделяются внутри, и снимок «до» их не содержит.
func (h *ProxyInstancesHandler) respondRecord(w http.ResponseWriter, key string) {
	rec, ok := h.recordByKey(key)
	if !ok {
		h.notFound(w, key)
		return
	}
	var st *proxyrt.InstanceState
	if h.deps.States != nil {
		if s, found := h.deps.States.Get(key); found {
			st = &s
		}
	}
	response.Success(w, h.viewOf(rec, st))
}

// freeID — идентификатор для POST без id. ID уникален внутри роли, поэтому
// берётся «default», а если занят — первый свободный «default2», «default3»…
func (h *ProxyInstancesHandler) freeID(kind instancestore.Kind) string {
	taken := map[string]bool{}
	for _, rec := range h.deps.Manager.Records() {
		if rec.Kind == kind {
			taken[rec.ID] = true
		}
	}
	if !taken["default"] {
		return "default"
	}
	for n := 2; ; n++ {
		id := fmt.Sprintf("default%d", n)
		if !taken[id] {
			return id
		}
	}
}

// ── гейты создания ───────────────────────────────────────────────

// proxyGateError — отказ гейта со своим кодом. Отдельный тип, потому что
// путь PATCH возвращает его ИЗ МУТАТОРА: менеджер отдаёт ошибку мутатора без
// обёртки, и хендлер узнаёт её через errors.As.
type proxyGateError struct {
	code string
	msg  string
}

func (e *proxyGateError) Error() string { return e.msg }

func (h *ProxyInstancesHandler) opkgTunSupported() bool {
	if h.deps.OpkgTunSupported == nil {
		return true
	}
	return h.deps.OpkgTunSupported()
}

// gateCheck — два отказа, которые обязаны случиться ДО записи.
func (h *ProxyInstancesHandler) gateCheck(rec instancestore.Record) error {
	if rec.Kind == instancestore.KindWdttServer && rec.WdttServer != nil {
		c := rec.WdttServer
		// Выход берётся через StaticNATList — тем же способом, что и
		// roles.Validate. Читать одну legacy-одиночку нельзя: старый движок
		// после PR #750 писал СПИСОК и явно чистил одиночку («источник правды
		// теперь список», 722cda888), а посев переносит обе формы как есть,
		// — такая запись валидна для рантайма, но здесь отбивалась 400 на
		// любом PATCH, включая переименование (F59).
		if strings.TrimSpace(c.NatMode) == "internet-only" && len(c.StaticNATList()) == 0 {
			return &proxyGateError{code: proxyCodeConfigInvalid,
				msg: "natMode internet-only: не выбран WAN (natStaticWan)"}
		}
	}
	if proxyNeedsOpkgTun(rec) && !h.opkgTunSupported() {
		return &proxyGateError{code: proxyCodeOpkgUnsupported,
			msg: "прошивка не поддерживает интерфейсы OpkgTun: доступны только wg-клиенты"}
	}
	return nil
}

// proxyNeedsOpkgTun — нужен ли инстансу интерфейс OpkgTun: серверу — обе
// половины, клиенту — только в raw-режиме.
func proxyNeedsOpkgTun(rec instancestore.Record) bool {
	switch rec.Kind {
	case instancestore.KindWdttServer:
		return true
	case instancestore.KindWdttClient:
		// Сравнение НОРМАЛИЗОВАННОЕ, тем же правилом, что у store
		// (instancestore/store.go:253-256): гейт считается до записи, на сыром
		// конфиге из тела запроса, а режим store приводит потом. Сырое
		// сравнение пропускало бы "RAW" и " raw " — гейт молчит, store делает
		// raw, и на прошивке без OpkgTun создаётся клиент, которому интерфейс
		// выделить нечем.
		return rec.WdttClient != nil &&
			strings.ToLower(strings.TrimSpace(rec.WdttClient.Mode)) == "raw"
	}
	return false
}

// ── конфиг: приём и выдача ───────────────────────────────────────

// proxySecretFields — секретные поля конфигов ролей (json-имена). Значения
// наружу не уходят, а на входе пустое значение означает «не менять» (Н5).
var proxySecretFields = []string{"password", "obfKey"}

// proxySecretsOf — секреты, объявленные конфигом роли. Список нужен ВЫДАЧЕ:
// без него признак `passwordSet` пропадал бы вместе с пустым значением
// (omitempty), и «секрет не задан» стало бы неотличимо от «поля нет».
func proxySecretsOf(kind instancestore.Kind) []string {
	switch kind {
	case instancestore.KindWdttClient:
		return []string{"password"}
	case instancestore.KindFreeTurnClient, instancestore.KindFreeTurnServer:
		return []string{"obfKey"}
	}
	return nil
}

// proxyConfigPtr — указатель на конфиг роли внутри записи; nil у чужой роли.
func proxyConfigPtr(rec *instancestore.Record) any {
	switch rec.Kind {
	case instancestore.KindWdttClient:
		return rec.WdttClient
	case instancestore.KindWdttServer:
		return rec.WdttServer
	case instancestore.KindFreeTurnClient:
		return rec.FreeTurnClient
	case instancestore.KindFreeTurnServer:
		return rec.FreeTurnServer
	}
	return nil
}

// proxyApplyConfig накладывает присланный конфиг на запись ПО МЕСТУ: unmarshal
// в существующую структуру трогает только присланные ключи, поэтому поля,
// которых в теле нет, сохраняются сами собой.
func proxyApplyConfig(rec *instancestore.Record, raw json.RawMessage) error {
	switch rec.Kind {
	case instancestore.KindWdttClient:
		if rec.WdttClient == nil {
			rec.WdttClient = &roles.WdttClientConfig{}
		}
	case instancestore.KindWdttServer:
		if rec.WdttServer == nil {
			rec.WdttServer = &roles.WdttServerConfig{}
		}
	case instancestore.KindFreeTurnClient:
		if rec.FreeTurnClient == nil {
			rec.FreeTurnClient = &roles.FreeTurnClientConfig{}
		}
	case instancestore.KindFreeTurnServer:
		if rec.FreeTurnServer == nil {
			rec.FreeTurnServer = &roles.FreeTurnServerConfig{}
		}
	default:
		return fmt.Errorf("неизвестная роль %q", rec.Kind)
	}
	if len(raw) == 0 {
		return nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return fmt.Errorf("невалидный конфиг роли %s: %w", rec.Kind, err)
	}
	proxyResetPresentSlices(proxyConfigPtr(rec), keys)
	if err := json.Unmarshal(raw, proxyConfigPtr(rec)); err != nil {
		return fmt.Errorf("невалидный конфиг роли %s: %w", rec.Kind, err)
	}
	// natStaticWan и natStaticWans — две формы одного поля, и присланная
	// форма обязана стать источником правды целиком. Иначе выбор WAN в UI
	// (фронт говорит ТОЛЬКО на одиночку) молча не вступал бы в силу:
	// StaticNATList предпочитает список, а он остался бы от старого движка,
	// который писал список и чистил одиночку (F59, коммит 722cda888).
	if c := rec.WdttServer; c != nil {
		_, sentOne := keys["natStaticWan"]
		_, sentList := keys["natStaticWans"]
		if sentOne && !sentList {
			c.NatStaticWANs = nil
		}
	}
	return nil
}

// proxyResetPresentSlices обнуляет срезы конфига, чьи ключи есть в присланном
// теле. Иначе декодер сливает СТРУКТУРЫ ВНУТРИ СРЕЗА поэлементно: он
// переиспользует старый элемент по позиции и заполняет в нём только присланные
// ключи. PATCH policies [{"name":"B"}] поверх [{"name":"A","order":0}] дал бы
// {"name":"B","order":0} — позиционный пин старого permit'а уехал бы на новое
// имя, а order:0 по докстроке roles.PolicyPermit это не пустое место, а САМЫЙ
// ВЕРХ политики. Присланный срез обязан заменять старый целиком.
//
// Обнуляются ВСЕ срезы, а не только срезы структур: для срезов скаляров это
// ничего не меняет (элементы и так переписываются целиком), а перечень полей,
// за которым надо следить, не заводится.
func proxyResetPresentSlices(cfg any, keys map[string]json.RawMessage) {
	v := reflect.ValueOf(cfg)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() != reflect.Slice || !f.CanSet() {
			continue
		}
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		if _, ok := keys[name]; ok {
			f.Set(reflect.Zero(f.Type()))
		}
	}
}

// proxyPruneBlankSecrets выбрасывает из тела пустые секретные поля (Н5):
// отсутствующее И пустое значение означают «не менять». Стирание секрета в
// пусто отдельной операцией не поддерживается — как и в старом мире, где
// пустой пароль был невалиден.
func proxyPruneBlankSecrets(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	dropped := false
	for _, f := range proxySecretFields {
		v, ok := m[f]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil || strings.TrimSpace(s) == "" {
			delete(m, f)
			dropped = true
		}
	}
	if !dropped {
		return raw, nil
	}
	return json.Marshal(m)
}

// proxyConfigView — конфиг роли наружу: те же json-имена, что на входе, но
// значения секретов заменены признаком «задан».
func proxyConfigView(rec instancestore.Record) map[string]any {
	out := map[string]any{}
	cfg := proxyConfigPtr(&rec)
	if cfg == nil {
		return out
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	for _, f := range proxySecretsOf(rec.Kind) {
		s, _ := out[f].(string)
		delete(out, f)
		out[f+"Set"] = strings.TrimSpace(s) != ""
	}
	return out
}

// ── сборка вида ──────────────────────────────────────────────────

func (h *ProxyInstancesHandler) viewOf(rec instancestore.Record, st *proxyrt.InstanceState) ProxyRtInstanceView {
	v := ProxyRtInstanceView{
		Key:          rec.Key(),
		ID:           rec.ID,
		Kind:         string(rec.Kind),
		Name:         rec.Name,
		Enabled:      rec.Enabled,
		CreatedAt:    rec.CreatedAt,
		SeededFrom:   rec.SeededFrom,
		Sub:          rec.Sub,
		PeerWg:       rec.PeerWg,
		PeerRaw:      rec.PeerRaw,
		LinkPeer:     rec.LinkPeer,
		LinkVKHashes: rec.LinkVKHashes,
		StatsLog:     rec.StatsLog,
		Config:       proxyConfigView(rec),
		State:        proxyStateView(st),
		Process:      h.processView(rec),
	}
	return v
}

func proxyStateView(st *proxyrt.InstanceState) *ProxyRtStateView {
	if st == nil {
		return nil
	}
	out := &ProxyRtStateView{Intent: string(st.Intent), Phase: string(st.Phase)}
	if !st.UpdatedAt.IsZero() {
		out.UpdatedAt = st.UpdatedAt.UTC().Format(time.RFC3339)
	}
	for _, r := range st.Resources {
		out.Resources = append(out.Resources, ProxyRtResourceView{
			ID: string(r.ID), Status: string(r.Status), Detail: r.Detail, Error: r.Error,
		})
	}
	for _, s := range st.LastPlan {
		out.LastPlan = append(out.LastPlan, ProxyRtStepView{
			Resource: string(s.Resource), Op: s.Op, Args: s.Args, Reason: s.Reason,
		})
	}
	return out
}

func (h *ProxyInstancesHandler) processView(rec instancestore.Record) ProcessView {
	key := rec.Key()
	var v ProcessView
	if h.deps.BinaryInfo != nil {
		v.Binary, v.BinaryPresent = h.deps.BinaryInfo(rec.Kind)
	}
	if h.deps.Log != nil {
		v.Log = h.deps.Log(key)
	}
	if h.deps.Snapshot == nil {
		return v
	}
	snap, ok := h.deps.Snapshot(key)
	if !ok {
		return v
	}
	v.Running = snap.PID > 0
	v.PID = snap.PID
	v.Address = snap.Address
	v.UptimeS = snap.UptimeS
	v.LastError = snap.LastError
	v.Mode = snap.Mode
	if snap.WG != nil {
		v.WgConfig = snap.WG.Config
	}
	if snap.Clients != nil {
		n := *snap.Clients
		v.Clients = &n
	}
	return v
}
