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

// LinkedCleaner — снос AWG-туннелей, связанных с клиентом (связь —
// storage.AWGTunnel.WdttClientID). Прод-обёртка обязана быть ГРОМКОЙ: старый
// deleteLinkedAwgTunnels при неподключённом хранилище молча отвечал «удалено
// ноль», и очистка выглядела успешной, ничего не сделав.
type LinkedCleaner interface {
	DeleteLinked(ctx context.Context, clientID string) (deleted []string, errs []string)
}

// TunnelImporter — узкий срез туннельной подсистемы, нужный ensure-wg.
// Прод-обёртку (поверх storage.AWGTunnelStore и tunnel/service) строит
// проводка; на ней же две обязанности, которых в этом интерфейсе нет и быть
// не может: снятие истории трафика при Delete и публикация списка туннелей в
// шину событий после Save/Import/Delete/Start.
type TunnelImporter interface {
	List() ([]storage.AWGTunnel, error)
	Get(tunnelID string) (*storage.AWGTunnel, error)
	Save(t *storage.AWGTunnel) error
	Delete(ctx context.Context, tunnelID string) error
	Import(ctx context.Context, conf, name string) (tunnelID, tunnelName string, err error)
	Start(ctx context.Context, tunnelID string) error
}

// Deps — зависимости поверхности ссылок.
type Deps struct {
	Records   RecordSource
	Mutator   Mutator
	Snapshots Snapshots
	Linked    LinkedCleaner
	Tunnels   TunnelImporter
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

// Decode — POST /api/proxyrt/wdtt/link/decode.
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
func (h *Handler) ClearLinkedTunnels(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	rec, ok := h.record(w, key)
	if !ok {
		return
	}
	if h.deps.Linked == nil {
		// Молчаливое «удалено ноль» здесь — худший исход: пользователь считал
		// бы связи снятыми.
		response.InternalError(w, "очистка связанных туннелей не подключена")
		return
	}
	// Связь туннеля — id инстанса, не ключ: на него ссылается
	// storage.AWGTunnel.WdttClientID.
	deleted, errs := h.deps.Linked.DeleteLinked(r.Context(), rec.ID)
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
			"wg-конфиг ещё не получен от wdtt-server — дождитесь успешного подключения клиента",
			"WDTT_WG_NOT_READY")
		return
	}

	port := ListenPortFromAddr(cfg.Listen)
	patched := PatchWgConfEndpoint(wgRaw, port)
	newPeerKey := ExtractPeerPublicKey(patched)
	wantName := TunnelNameFromClient(rec.Name)
	wantEndpoint := fmt.Sprintf("127.0.0.1:%d", port)
	running := haveSnap && snap.PID > 0

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
		}
		if running {
			_ = h.deps.Tunnels.Start(r.Context(), match.ID)
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
	response.Success(w, EnsureWGTunnelResponse{
		Created:    true,
		TunnelID:   tunnelID,
		TunnelName: tunnelName,
		Message:    fmt.Sprintf("Создан AWG-туннель «%s» (Endpoint 127.0.0.1:%d)", tunnelName, port),
	})
}

// ── общее ────────────────────────────────────────────────────────

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
