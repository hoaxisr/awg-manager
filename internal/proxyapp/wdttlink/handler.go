package wdttlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ── узкие интерфейсы (шов Г-8 п. 4) ──────────────────────────────
//
// Формы ЗДЕСЬ каноничны для всех продуктовых пакетов волны: RecordSource и
// Mutator берутся отсюда, своих вариантов пакеты не заводят. Прод-реализации
// обоих — обёртки менеджера, их строит проводка.

// RecordSource — чтение записи инстанса по ключу (роль:id).
type RecordSource interface {
	Get(key string) (instancestore.Record, bool)
}

// Mutator — ЕДИНСТВЕННЫЙ путь правки записей: мутатор получает УКАЗАТЕЛЬ и
// правит поля ПО МЕСТУ (пересборка записи литералом молча теряет
// CreatedAt/Sub/Users/LinkPeer и слоты адресов).
type Mutator interface {
	Update(ctx context.Context, key string, mutate func(*instancestore.Record) error) error
	Create(ctx context.Context, rec instancestore.Record) error
}

// Snapshots — снимок управляющего сокета инстанса по его ключу; тот же
// снимщик, что у ручек инстансов. Второй результат — «снимка нет».
type Snapshots func(key string) (awgmproto.State, bool)

// LinkedCleaner — снос AWG-туннелей, связанных с ОДНИМ клиентом.
//
// Потребитель держит КАРТУ уборщиков по роли, а не одиночную реализацию: поле
// связи у подсистем разное (storage.AWGTunnel.WdttClientID против
// FreeTurnClientID), и один уборщик на обе роли физически не может выбрать
// поле — clientID роли не несёт.
type LinkedCleaner interface {
	// DeleteLinked сносит туннели, связанные с clientID (это Record.ID, а НЕ
	// Key: на id ссылается поле связи туннеля).
	//
	// Две обязанности прод-обёртки, которые типом не выражаются и потому
	// названы здесь: (1) снять историю трафика каждого снесённого туннеля;
	// (2) опубликовать список туннелей в шину событий, иначе снос для фронта
	// не случится. Третья — быть ГРОМКОЙ: при неподключённом хранилище
	// причина обязана уехать в errs. Старый deleteLinkedAwgTunnels отвечал в
	// этом случае «удалено ноль», и очистка выглядела успешной, ничего не
	// сделав.
	DeleteLinked(ctx context.Context, clientID string) (deleted []string, errs []string)
}

// TunnelImporter — узкий срез туннельной подсистемы, нужный ensure-wg.
// Прод-обёртку (поверх storage.AWGTunnelStore и tunnel/service) строит
// проводка.
//
// Побочные обязанности старого хендлера НЕ оставлены прозой: снятие истории
// трафика и публикация списка туннелей объявлены ОТДЕЛЬНЫМИ методами и
// зовутся отсюда. Обязанность, спрятанная в докстроке чужого Delete,
// теряется молча — осиротевшая история трафика и невидимый для фронта список.
type TunnelImporter interface {
	List() ([]storage.AWGTunnel, error)
	Get(tunnelID string) (*storage.AWGTunnel, error)
	Save(t *storage.AWGTunnel) error
	Delete(ctx context.Context, tunnelID string) error
	Import(ctx context.Context, conf, name string) (tunnelID, tunnelName string, err error)
	Start(ctx context.Context, tunnelID string) error
	// ForgetTraffic снимает историю трафика удалённого туннеля: id
	// переиспользуется, и чужая история подмешалась бы к новому туннелю.
	ForgetTraffic(tunnelID string)
	// PublishList публикует список туннелей в шину событий. Без него правка
	// доехала до диска, но не до фронта: страница показывает прежнее.
	PublishList(ctx context.Context)
}

// Deps — зависимости поверхности ссылок.
type Deps struct {
	Records   RecordSource
	Mutator   Mutator
	Snapshots Snapshots
	Tunnels   TunnelImporter
	// Cleaners — уборщики связанных туннелей ПО РОЛИ (см. LinkedCleaner).
	Cleaners map[instancestore.Kind]LinkedCleaner
	// Builders — диспетчер ручки ссылки по роли записи; собирает проводка
	// (wdtt-server — Builder этого пакета, freeturn-server — свой пакет).
	Builders map[instancestore.Kind]LinkBuilder
}

// Handler обслуживает ручки ссылок, импорта, ensure-wg и очистки связей.
// Пути регистрирует проводка: у пакета нет своего мультиплексора, ключ
// инстанса приходит аргументом.
type Handler struct{ deps Deps }

func NewHandler(d Deps) *Handler { return &Handler{deps: d} }

// ── decode / import ──────────────────────────────────────────────

// linkRequestBody — тело decode/import. `link` — вербатим со старого
// api.WdttImportRequest; `id`/`name` новые: импорт СОЗДАЁТ инстанс (старый
// импортировал в единственного клиента по умолчанию), и его надо как-то
// назвать.
type linkRequestBody struct {
	Link string `json:"link"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// Конверты ответов объявлены ради спеки: генератор фронтовых схем ключует
// валидацию ПУТЁМ и без описанного ответа молча пропускает его без проверки.

// DecodeResponse — конверт разбора ссылки.
type DecodeResponse struct {
	Success bool             `json:"success" example:"true"`
	Data    LinkDecodeResult `json:"data"`
}

// ImportResult — тело импорта: ключ созданного инстанса и разобранный профиль.
type ImportResult struct {
	Key     string        `json:"key" example:"wdtt-client:default"`
	Payload ImportPayload `json:"payload"`
}

// ImportResponse — конверт импорта.
type ImportResponse struct {
	Success bool         `json:"success" example:"true"`
	Data    ImportResult `json:"data"`
}

// LinkResult — тело ручки ссылки. Поля объединяют обе роли: wdtt-сервер
// отдаёт link/linkQwdtt/peer, freeturn-сервер — link/peer/clientId.
type LinkResult struct {
	Link      string `json:"link" example:"wdtt://…"`
	LinkQwdtt string `json:"linkQwdtt,omitempty" example:"qwdtt://…"`
	Peer      string `json:"peer" example:"1.2.3.4:56002"`
	ClientID  string `json:"clientId,omitempty"`
}

// LinkResponse — конверт ручки ссылки.
type LinkResponse struct {
	Success bool       `json:"success" example:"true"`
	Data    LinkResult `json:"data"`
}

// ClearLinkedResult — тело очистки связей.
type ClearLinkedResult struct {
	DeletedTunnels []string `json:"deletedTunnels"`
	TunnelErrors   []string `json:"tunnelErrors"`
	Message        string   `json:"message" example:"linked AWG tunnels cleared"`
}

// ClearLinkedResponse — конверт очистки связей.
type ClearLinkedResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    ClearLinkedResult `json:"data"`
}

// EnsureWGResponse — конверт подготовки связанного WG-туннеля.
type EnsureWGResponse struct {
	Success bool                   `json:"success" example:"true"`
	Data    EnsureWGTunnelResponse `json:"data"`
}

// Decode — POST /api/proxyrt/wdtt/link/decode.
//
//	@Summary	Разобрать ссылку wdtt:// или адрес подписки
//	@Tags		proxyrt
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Param		request	body		linkRequestBody	true	"Ссылка"
//	@Success	200		{object}	DecodeResponse
//	@Failure	400		{object}	DecodeResponse
//	@Router		/proxyrt/wdtt/link/decode [post]
func (h *Handler) Decode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req linkRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	result, err := DecodeLink(req.Link)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_DECODE_FAILED")
		return
	}
	response.Success(w, result)
}

// Import — POST /api/proxyrt/wdtt/link/import: разбор ссылки и СОЗДАНИЕ
// инстанса клиента. listen и пины выделяет менеджер — здесь они не ставятся.
//
//	@Summary	Импортировать ссылку в новый инстанс клиента
//	@Tags		proxyrt
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Param		request	body		linkRequestBody	true	"Ссылка, id и имя"
//	@Success	200		{object}	ImportResponse
//	@Failure	400		{object}	ImportResponse
//	@Router		/proxyrt/wdtt/link/import [post]
func (h *Handler) Import(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req linkRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	payload, err := DecodeImport(req.Link)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_IMPORT_FAILED")
		return
	}
	if h.deps.Mutator == nil || h.deps.Records == nil {
		response.InternalError(w, "импорт не подключён")
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = h.freeID()
	}
	rec := recordFromPayload(payload, id, req.Name)
	if err := h.deps.Mutator.Create(r.Context(), rec); err != nil {
		response.Error(w, err.Error(), "WDTT_IMPORT_FAILED")
		return
	}
	// Сама запись отдаётся отдельной ручкой инстанса: там живёт маскировка
	// секретов, и второй копии этого правила здесь быть не должно.
	response.Success(w, map[string]any{
		"key":     rec.Key(),
		"payload": payload,
	})
}

// recordFromPayload — запись клиента из разобранного профиля. Listen НЕ
// ставится (его выделяет менеджер из своего пула), Enabled=false: импорт сам
// по себе не запуск, иначе автостарт поднял бы неготовый клиент.
func recordFromPayload(p ImportPayload, id, wantName string) instancestore.Record {
	name := strings.TrimSpace(wantName)
	if name == "" {
		name = strings.TrimSpace(p.Name)
	}
	if name == "" {
		name = "WDTT"
	}
	return instancestore.Record{
		ID:      id,
		Kind:    instancestore.KindWdttClient,
		Name:    name,
		Enabled: false,
		Sub:     p.SubURL,
		WdttClient: &roles.WdttClientConfig{
			Mode:     normalizeConnMode(p.ConnMode),
			Peer:     p.Peer,
			Password: p.Password,
			VKHashes: strings.Join(p.VKHashes, ","),
			Workers:  p.Workers,
			DeviceID: p.DeviceID,
		},
	}
}

// freeID — идентификатор для импорта без явного id: «default», а если занят —
// первый свободный «default2», «default3»… (та же формула, что у ручки
// создания инстанса).
func (h *Handler) freeID() string {
	taken := func(id string) bool {
		_, ok := h.deps.Records.Get(string(instancestore.KindWdttClient) + ":" + id)
		return ok
	}
	if !taken("default") {
		return "default"
	}
	for n := 2; ; n++ {
		id := fmt.Sprintf("default%d", n)
		if !taken(id) {
			return id
		}
	}
}

// ── ссылка ───────────────────────────────────────────────────────

// Link — POST /api/proxyrt/instances/{key}/link.
//
//	@Summary	Выдать ссылку абоненту серверного инстанса
//	@Tags		proxyrt
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Param		key		path		string		true	"Ключ инстанса (роль:id)"
//	@Param		request	body		LinkRequest	true	"Параметры ссылки"
//	@Success	200		{object}	LinkResponse
//	@Failure	400		{object}	LinkResponse
//	@Failure	404		{object}	LinkResponse
//	@Router		/proxyrt/instances/{key}/link [post]
func (h *Handler) Link(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	rec, ok := h.record(w, key)
	if !ok {
		return
	}
	var req LinkRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // пустое тело законно, нули годны
	}
	builder := h.deps.Builders[rec.Kind]
	if builder == nil {
		response.Error(w, "инстанс "+key+": ссылки для роли "+string(rec.Kind)+" не выдаются", "BAD_REQUEST")
		return
	}
	data, err := builder.BuildLink(r.Context(), rec, req)
	if err != nil {
		var le *LinkError
		if errors.As(err, &le) {
			response.Error(w, le.Msg, le.Code)
			return
		}
		response.Error(w, err.Error(), "PROXY_LINK_FAILED")
		return
	}
	response.Success(w, data)
}

// ── очистка связей ───────────────────────────────────────────────

// ClearLinkedTunnels — POST /api/proxyrt/instances/{key}/linked-tunnels/clear.
// Форма ответа прежняя (api/wdtt_linked_clear.go:26-30).
//
//	@Summary	Снять AWG-туннели, связанные с клиентским инстансом
//	@Tags		proxyrt
//	@Produce	json
//	@Security	CookieAuth
//	@Param		key	path		string	true	"Ключ инстанса (роль:id)"
//	@Success	200	{object}	ClearLinkedResponse
//	@Failure	400	{object}	ClearLinkedResponse
//	@Failure	404	{object}	ClearLinkedResponse
//	@Router		/proxyrt/instances/{key}/linked-tunnels/clear [post]
func (h *Handler) ClearLinkedTunnels(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	rec, ok := h.record(w, key)
	if !ok {
		return
	}
	// Роль записи сверяется ОБЯЗАТЕЛЬНО. Связь туннеля — id инстанса, а он
	// уникален только ВНУТРИ роли: «default» есть у всех четырёх (докстрока
	// instancestore.Record.Key). Без гейта запрос к СЕРВЕРУ default сносил бы
	// туннели КЛИЕНТА default. В старом мире роль задавал сам путь
	// (/wdtt/clients/{id}/…), здесь её задаёт только ключ.
	if !isClientKind(rec.Kind) {
		response.Error(w, "инстанс "+key+": связанные AWG-туннели есть только у клиентов, роль "+
			string(rec.Kind)+" их не заводит", "BAD_REQUEST")
		return
	}
	cleaner := h.deps.Cleaners[rec.Kind]
	if cleaner == nil {
		// Молчаливое «удалено ноль» здесь — худший исход: пользователь считал
		// бы связи снятыми.
		response.InternalError(w, "очистка связанных туннелей роли "+string(rec.Kind)+" не подключена")
		return
	}
	deleted, errs := cleaner.DeleteLinked(r.Context(), rec.ID)
	response.Success(w, map[string]any{
		"deletedTunnels": deleted,
		"tunnelErrors":   errs,
		"message":        "linked AWG tunnels cleared",
	})
}

// ── ensure-wg ────────────────────────────────────────────────────

// EnsureWGTunnelResponse — тело ответа ensure-wg, форма прежняя
// (api/wdtt_wg.go:13-18): фронт читает эти четыре поля.
type EnsureWGTunnelResponse struct {
	Created    bool   `json:"created"`
	TunnelID   string `json:"tunnelId,omitempty"`
	TunnelName string `json:"tunnelName,omitempty"`
	Message    string `json:"message,omitempty"`
}

// EnsureWGTunnel — POST /api/proxyrt/instances/{key}/ensure-wg-tunnel:
// AWG-туннель под WireGuard-конфиг, который клиент получил от сервера.
//
// Источник конфига — снимок процесса (State.WG.Config), а не разбор журнала:
// журнал в новом мире — файл форка, а конфиг приезжает по управляющему сокету.
//
//	@Summary		Завести AWG-туннель под WireGuard-конфиг клиента
//	@Description	409 WDTT_WG_NOT_READY означает «конфиг ещё не приехал от сервера» —
//	@Description	это ожидание, а не сбой: ручку зовёт автоэффект страницы.
//	@Tags			proxyrt
//	@Produce		json
//	@Security		CookieAuth
//	@Param			key	path		string	true	"Ключ инстанса (роль:id)"
//	@Success		200	{object}	EnsureWGResponse
//	@Failure		404	{object}	EnsureWGResponse
//	@Failure		409	{object}	EnsureWGResponse
//	@Router			/proxyrt/instances/{key}/ensure-wg-tunnel [post]
func (h *Handler) EnsureWGTunnel(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	rec, ok := h.record(w, key)
	if !ok {
		return
	}
	cfg, err := rec.WdttClientConfig()
	if err != nil {
		response.Error(w, err.Error(), "BAD_REQUEST")
		return
	}
	if h.deps.Tunnels == nil {
		response.Error(w, "tunnel import not wired", "INTERNAL")
		return
	}
	if normalizeConnMode(cfg.Mode) == ConnModeRaw {
		response.Success(w, EnsureWGTunnelResponse{
			Created: false,
			Message: "Режим Raw: AWG-туннель не используется — трафик идёт через OpkgTun (NDMS)",
		})
		return
	}

	snap, haveSnap := h.snapshot(key)
	wgRaw := ""
	if haveSnap && snap.WG != nil {
		wgRaw = strings.TrimSpace(snap.WG.Config)
	}
	if wgRaw == "" {
		response.ErrorWithStatus(w, http.StatusConflict,
			"WireGuard конфиг ещё не получен от wdtt-server — дождитесь успешного подключения клиента",
			"WDTT_WG_NOT_READY")
		return
	}

	port := ListenPortFromAddr(cfg.Listen)
	patched := PatchWgConfEndpoint(wgRaw, port)
	newPeerKey := ExtractPeerPublicKey(patched)
	wantName := TunnelNameFromClient(rec.Name)
	wantEndpoint := fmt.Sprintf("127.0.0.1:%d", port)
	running := haveSnap && snap.PID > 0
	// mutated — было ли что менять: публикация списка нужна ровно тогда,
	// когда фронту есть что перечитать.
	mutated := false

	tunnels, err := h.deps.Tunnels.List()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	// Снос связанных туннелей с ЧУЖИМ ключом пира: сервер сменил пир или
	// страну, и старая запись ведёт в никуда.
	for _, tun := range tunnels {
		if strings.TrimSpace(tun.WdttClientID) != rec.ID {
			continue
		}
		storedKey := strings.TrimSpace(tun.Peer.PublicKey)
		if newPeerKey != "" && storedKey != "" && newPeerKey != storedKey {
			if err := h.deps.Tunnels.Delete(r.Context(), tun.ID); err != nil {
				response.Error(w, "не удалось удалить устаревший AWG-туннель: "+err.Error(),
					"WDTT_WG_REPLACE_FAILED")
				return
			}
			h.deps.Tunnels.ForgetTraffic(tun.ID)
			mutated = true
		}
	}
	// Перечитываем после сносов.
	if tunnels, err = h.deps.Tunnels.List(); err != nil {
		response.InternalError(w, err.Error())
		return
	}

	if match := MatchingAWGTunnel(tunnels, patched); match != nil {
		stored, err := h.deps.Tunnels.Get(match.ID)
		if err != nil {
			response.InternalError(w, err.Error())
			return
		}
		changed := false
		if strings.TrimSpace(stored.WdttClientID) != rec.ID {
			stored.WdttClientID = rec.ID
			changed = true
		}
		if wantName != "" && stored.Name != wantName {
			stored.Name = wantName
			changed = true
		}
		if strings.TrimSpace(stored.Peer.Endpoint) != wantEndpoint {
			stored.Peer.Endpoint = wantEndpoint
			changed = true
		}
		if changed {
			if err := h.deps.Tunnels.Save(stored); err != nil {
				response.InternalError(w, err.Error())
				return
			}
			mutated = true
		}
		if running {
			_ = h.deps.Tunnels.Start(r.Context(), match.ID)
			mutated = true
		}
		if mutated {
			h.deps.Tunnels.PublishList(r.Context())
		}
		response.Success(w, EnsureWGTunnelResponse{
			Created:    false,
			TunnelID:   match.ID,
			TunnelName: match.Name,
			Message:    "AWG-туннель с таким WireGuard-конфигом уже существует",
		})
		return
	}

	tunnelID, tunnelName, err := h.deps.Tunnels.Import(r.Context(), patched, wantName)
	if err != nil {
		response.Error(w, err.Error(), "WDTT_WG_IMPORT_FAILED")
		return
	}
	stored, err := h.deps.Tunnels.Get(tunnelID)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	stored.WdttClientID = rec.ID
	if err := h.deps.Tunnels.Save(stored); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if running {
		_ = h.deps.Tunnels.Start(r.Context(), tunnelID)
	}
	h.deps.Tunnels.PublishList(r.Context())
	response.Success(w, EnsureWGTunnelResponse{
		Created:    true,
		TunnelID:   tunnelID,
		TunnelName: tunnelName,
		Message:    fmt.Sprintf("Создан AWG-туннель «%s» (Endpoint 127.0.0.1:%d)", tunnelName, port),
	})
}

// ── общее ────────────────────────────────────────────────────────

// isClientKind — у кого бывают связанные AWG-туннели: только у клиентов
// (поля связи storage.AWGTunnel.WdttClientID и FreeTurnClientID). Сервер —
// вход, туннеля на него не заводится.
func isClientKind(k instancestore.Kind) bool {
	return k == instancestore.KindWdttClient || k == instancestore.KindFreeTurnClient
}

func (h *Handler) record(w http.ResponseWriter, key string) (instancestore.Record, bool) {
	if h.deps.Records == nil {
		response.InternalError(w, "источник записей не подключён")
		return instancestore.Record{}, false
	}
	rec, ok := h.deps.Records.Get(key)
	if !ok {
		response.ErrorWithStatus(w, http.StatusNotFound, "инстанс "+key+" не найден", "NOT_FOUND")
		return instancestore.Record{}, false
	}
	return rec, true
}

func (h *Handler) snapshot(key string) (awgmproto.State, bool) {
	if h.deps.Snapshots == nil {
		return awgmproto.State{}, false
	}
	return h.deps.Snapshots(key)
}
