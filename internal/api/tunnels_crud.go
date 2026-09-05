package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// List returns all tunnels.
//
//	@Summary		List tunnels
//	@Tags			tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	TunnelListResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/list [get]
func (h *TunnelsHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	items, err := h.listItems(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}

	response.Success(w, items)
}

// GetAll returns the composite tunnels snapshot ({tunnels, external,
// system}) the frontend polls instead of listening to a legacy
// snapshot SSE event.
// GET /api/tunnels/all
//
//	@Summary		Composite tunnels snapshot
//	@Tags			tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	TunnelsAllResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/all [get]
func (h *TunnelsHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	h.writeAll(w, r)
}

// parseTrafficPeriod maps the period query value to a duration.
func parseTrafficPeriod(raw string) (time.Duration, bool) {
	switch raw {
	case "5m":
		return 5 * time.Minute, true
	case "10m":
		return 10 * time.Minute, true
	case "30m":
		return 30 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "3h":
		return 3 * time.Hour, true
	case "6h":
		return 6 * time.Hour, true
	case "12h":
		return 12 * time.Hour, true
	case "24h":
		return 24 * time.Hour, true
	default:
		return 0, false
	}
}

// Traffic returns rate history + aggregates for a single tunnel.
// GET /api/tunnels/traffic?id=<tunnelID>&period=5m|10m|30m|1h|3h|6h|12h|24h
//
// Only a fixed set of short/long-range presets is accepted — anything
// else returns 400. 1h is what the card chart fetches on mount to
// backfill before SSE takes over; the detail modal can request any of
// the supported presets.
//
// data.stats.volumeRx and data.stats.volumeTx are byte estimates for the
// selected window from raw in-memory samples (zero if fewer than two samples).
//
//	@Summary		Tunnel traffic history
//	@Tags			tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		query	string	true	"Tunnel id"
//	@Param			period	query	string	true	"5m, 10m, 30m, 1h, 3h, 6h, 12h, or 24h"
//	@Success		200	{object}	TunnelTrafficResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/traffic [get]
func (h *TunnelsHandler) Traffic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	// Read-only handler reading an in-memory map: tolerate non-AWG ids
	// (singbox subscription tags include emoji/spaces). Sanity-check still
	// rejects binary garbage and oversized ids. Unknown id → 200 + empty.
	if len(id) > 256 || !utf8.ValidString(id) || strings.ContainsFunc(id, func(r rune) bool { return r < 0x20 }) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	since, ok := parseTrafficPeriod(r.URL.Query().Get("period"))
	if !ok {
		response.Error(w, "period must be one of: 5m, 10m, 30m, 1h, 3h, 6h, 12h, 24h", "INVALID_PERIOD")
		return
	}

	const maxPoints = 360

	resp := map[string]any{
		"points": []traffic.Point{},
		"stats":  traffic.Stats{},
	}
	if h.traffic != nil {
		pts := h.traffic.Get(id, since, maxPoints)
		if pts == nil {
			pts = []traffic.Point{}
		}
		resp["points"] = pts
		resp["stats"] = h.traffic.Stats(id, since)
	}
	response.Success(w, resp)
}

// Get returns a single tunnel by ID.
//
//	@Summary		Get tunnel
//	@Tags			tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	TunnelDetailResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/get [get]
func (h *TunnelsHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	if stored, err := h.store.Get(id); err == nil && stored != nil && stored.Backend == backendWdttRaw {
		response.Success(w, h.buildWdttRawResponse(stored))
		return
	}

	resp, err := BuildTunnelResponse(r, h.svc, h.store, id, h.quiescentFor(id))
	if err != nil {
		response.Error(w, err.Error(), "NOT_FOUND")
		return
	}
	response.Success(w, resp)
}

// Create creates a new tunnel.
//
//	@Summary		Create tunnel
//	@Tags			tunnels
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/create [post]
func (h *TunnelsHandler) Create(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[storage.AWGTunnel](w, r, http.MethodPost)
	if !ok {
		return
	}

	if err := config.ValidateKeepaliveForBackend(req.Peer.PersistentKeepalive, req.Backend); err != nil {
		response.Error(w, err.Error(), "INVALID_KEEPALIVE")
		return
	}

	// Validate endpoint resolves
	if err := config.ValidateAWG3(&req.Interface.AWGObfuscation); err != nil {
		response.Error(w, err.Error(), "INVALID_AWG3")
		return
	}
	if req.Peer.Endpoint != "" {
		if _, _, err := netutil.ResolveEndpoint(req.Peer.Endpoint); err != nil {
			response.Error(w, "endpoint не резолвится: "+err.Error(), "INVALID_ENDPOINT")
			return
		}
	}

	// Generate ID if not provided
	tunnelID := req.ID
	if tunnelID == "" {
		var err error
		tunnelID, err = h.store.NextAvailableID(r.Context(), req.Backend, h.opkgOccupancy)
		if err != nil {
			response.Error(w, "failed to generate tunnel ID", "CREATE_FAILED")
			return
		}
	} else if !isValidTunnelID(tunnelID) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	} else if err := h.checkExplicitIDFree(r.Context(), tunnelID, req.Backend); err != nil {
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "INDEX_TAKEN")
		return
	}

	// Prepare tunnel data
	req.ID = tunnelID
	req.Type = "awg"
	req.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	if !req.Enabled {
		req.Enabled = true
	}
	req.ISPInterface = "" // auto mode: NDMS picks default gateway
	req.ISPInterfaceLabel = "Определяет роутер"

	// Gate from before the NDMS Create call through publishTunnelList so
	// the hook-driven snapshot rebroadcast sees the finalized store state.
	// Only relevant for NativeWG (kernel backend doesn't touch NDMS at
	// Create time), but always entering is cheap and keeps the flow
	// symmetric. The final publishTunnelList at the bottom triggers its
	// own snapshot refresh AFTER gate exit.
	if h.selfCreateGate != nil {
		h.selfCreateGate.EnterSelfCreate()
		defer h.selfCreateGate.ExitSelfCreate()
	}
	// Дефолты пингчека — ДО вызова: запись сохраняет сервис, и всё, что
	// должно попасть на диск, обязано быть проставлено раньше.
	if req.PingCheck == nil && h.pingCheck != nil {
		req.PingCheck = &storage.TunnelPingCheck{
			Enabled:       false,
			Method:        "icmp",
			Target:        "8.8.8.8",
			Interval:      45,
			DeadInterval:  120,
			FailThreshold: 3,
			MinSuccess:    1,
			Timeout:       5,
			Restart:       true,
		}
	}

	// Ресурс в NDMS, запись и конфиг создаёт сервис одной операцией — вместе
	// с откатом. Раньше запись и конфиг писал этот хендлер уже после
	// возврата, и при их провале созданный ресурс оставался сиротой.
	if err := h.svc.Create(r.Context(), &req); err != nil {
		h.log.Warn("create", req.Name, "Service create failed: "+err.Error())
		response.Error(w, err.Error(), "CREATE_FAILED")
		return
	}

	h.log.Info("create", req.Name, "Tunnel created")
	h.publishTunnelList(r.Context())

	// Return the created tunnel
	resp, err := BuildTunnelResponse(r, h.svc, h.store, tunnelID, h.quiescentFor(tunnelID))
	if err != nil {
		response.Error(w, err.Error(), "CREATE_FAILED")
		return
	}
	response.Success(w, resp)
}

// checkExplicitIDFree проверяет присланный клиентом идентификатор той же
// занятостью, что и сгенерированный.
//
// Идентификатор задаёт номер интерфейса OpkgTun — включая клиентские вроде
// "myvpn", которым extractTunnelNum подставляет ноль. Без этой проверки запись
// создавалась бы на номере, который держит чужая подсистема, и первое же
// включение усыновило бы её интерфейс: kernel-путь опознаёт свой интерфейс по
// номеру, а не по описанию.
//
// nativewg не спрашивается: он живёт как Wireguard<N> и номеров OpkgTun не
// занимает. Пустой источник занятости — тоже не отказ: явный идентификатор
// принимали и до появления занятости, ломать это на неполной проводке незачем.
func (h *TunnelsHandler) checkExplicitIDFree(ctx context.Context, tunnelID, backend string) error {
	if backend == "nativewg" || h.opkgOccupancy == nil {
		return nil
	}
	idx, occupies := tunnel.OpkgTunIndexOf(tunnelID)
	if !occupies {
		return nil
	}
	taken, err := h.opkgOccupancy(ctx)
	if err != nil {
		return fmt.Errorf("не удалось проверить занятость номеров: %w", err)
	}
	if taken[idx] {
		return fmt.Errorf("номер интерфейса OpkgTun%d уже занят — выберите другой идентификатор или не задавайте его вовсе", idx)
	}
	return nil
}

// Update updates an existing tunnel.
//
//	@Summary		Update tunnel
//	@Tags			tunnels
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		403	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/update [post]
func (h *TunnelsHandler) Update(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}
	req, ok := parseJSON[storage.AWGTunnel](w, r, http.MethodPost)
	if !ok {
		return
	}

	// Get existing tunnel
	existing, err := h.store.Get(id)
	if err != nil {
		response.Error(w, "tunnel not found", "NOT_FOUND")
		return
	}

	// Защита (#818) отвергает правку до всякого побочного действия: и до
	// ветки зеркальной записи, которая пишет сама, и до svc.Update.
	if existing.Locked {
		response.ErrorWithStatus(w, http.StatusForbidden, tunnelLockedMessage, "TUNNEL_LOCKED")
		return
	}

	if existing.Backend == backendWdttRaw {
		if req.Name != "" && req.Name != existing.Name {
			// Имя зеркальной записи — производная конфига инстанса: зеркало
			// перезаписывает его на каждом объявлении (exitreg/mirror.go:105).
			// Принять переименование здесь значит подтвердить правку, которую
			// ближайшее объявление молча откатит.
			response.Error(w, "имя raw-записи задаётся инстансом WDTT — переименуйте инстанс", "WDTT_RAW_NAME_READONLY")
			return
		}
		// Маршрутизацией raw-выхода распоряжается прокси-рантайм: и маршрут
		// по умолчанию, и WAN-подключение он выставляет сам по конфигу
		// инстанса. Правка отсюда не применится никогда — отказ по образцу
		// имени вместо молчаливой потери.
		//
		// Условия «поле прислали» обязательны: запрос парсится в полную
		// AWGTunnel, и непришедшее поле приходит нулевым. Присылку булева
		// маршрута видно только по компаньону DefaultRouteSet, строкового
		// WAN — по непустоте. Без них частичный PATCH (форма связности шлёт
		// один connectivityCheck) ловил бы ложный отказ, и это было бы хуже
		// исходной потери.
		if req.DefaultRouteSet && req.DefaultRoute != existing.DefaultRoute {
			response.Error(w, "маршрут по умолчанию raw-записи ведёт прокси-рантайм — меняйте в настройках инстанса WDTT", "WDTT_RAW_ROUTING_READONLY")
			return
		}
		if req.ISPInterface != "" {
			// "auto" — это способ прислать пустое значение (нормализация
			// обычной ветки, :425), а не отдельный интерфейс.
			wantISP := req.ISPInterface
			if wantISP == tunnel.ISPInterfaceAuto {
				wantISP = ""
			}
			if wantISP != existing.ISPInterface {
				response.Error(w, "WAN-подключение raw-записи ведёт прокси-рантайм — меняйте в настройках инстанса WDTT", "WDTT_RAW_WAN_READONLY")
				return
			}
		}
		updated := *existing
		if req.ConnectivityCheck != nil {
			updated.ConnectivityCheck = req.ConnectivityCheck
			if updated.ConnectivityCheck.Method == "" {
				updated.ConnectivityCheck.Method = "http"
			}
		}
		// Измерение зеркальной записи разрешено — запрещено только автолечение
		// (pingcheck/monitor.go:118), значит настройка измерения обязана
		// сохраняться, а не теряться молча.
		if req.PingCheck != nil {
			updated.PingCheck = req.PingCheck
		}
		// Ту же запись переписывает волна зеркала (exitreg/mirror.go): правка
		// карточки ложится на свежую запись под локом стора, а не поверх
		// снимка existing, снятого до всех проверок выше.
		if err := h.store.Update(id, func(t *storage.AWGTunnel) error {
			if req.ConnectivityCheck != nil {
				t.ConnectivityCheck = updated.ConnectivityCheck
			}
			if req.PingCheck != nil {
				t.PingCheck = updated.PingCheck
			}
			return nil
		}); err != nil {
			response.Error(w, err.Error(), "UPDATE_FAILED")
			return
		}
		// Монитор поднимается ЗДЕСЬ, как и у обычных туннелей (:543-550):
		// иначе включённая пользователем проверка легла бы на диск и молчала
		// до перезапуска демона или постороннего события.
		if h.pingCheck != nil {
			oldOn := existing.PingCheck != nil && existing.PingCheck.Enabled
			newOn := updated.PingCheck != nil && updated.PingCheck.Enabled
			if oldOn != newOn {
				if newOn && h.svc.GetState(r.Context(), id).State == tunnel.StateRunning {
					h.pingCheck.StartMonitoring(id, updated.Name)
				} else if !newOn {
					h.pingCheck.StopMonitoring(id)
				}
			}
		}
		h.log.Info("update", updated.Name, "WDTT raw metadata updated")
		h.publishTunnelList(r.Context())
		response.Success(w, h.buildWdttRawResponse(&updated))
		return
	}

	// Detect changes before applying the patch.
	oldPingCheckEnabled := existing.PingCheck != nil && existing.PingCheck.Enabled
	oldISPInterface := existing.ISPInterface

	// NativeWG: convert ISPInterface to NDMS name for "connect via".
	// Frontend sends kernel names (from WAN model), but NDMS needs NDMS IDs.
	// Конверсия идёт ДО применения правки: она ходит в стор и в WAN-модель, а
	// внутри мутатора стора это запрещено контрактом (dir-lock нерекурсивен).
	// "auto" — сентинел очистки, конвертировать в нём нечего.
	if existing.Backend == "nativewg" && req.ISPInterface != "" && req.ISPInterface != tunnel.ISPInterfaceAuto {
		if tunnel.IsTunnelRoute(req.ISPInterface) {
			// Tunnel chaining: resolve parent tunnel's NDMS interface name.
			parentID := tunnel.TunnelRouteID(req.ISPInterface)
			if parent, err := h.store.Get(parentID); err == nil {
				if parent.Backend == "nativewg" {
					req.ISPInterface = nwg.NewNWGNames(parent.NWGIndex).NDMSName
				} else {
					req.ISPInterface = tunnel.NewNames(parentID).NDMSName
				}
			}
		} else if ndmsID := h.svc.WANModel().IDFor(req.ISPInterface); ndmsID != "" {
			req.ISPInterface = ndmsID
		}
	}

	// Правка ложится на копию снимка — по ней идут валидации и diff для RCI.
	// На СВЕЖУЮ запись та же функция ляжет внутри транзакции стора ниже.
	merged := *existing
	applyTunnelUpdate(&merged, &req)
	newPingCheckEnabled := merged.PingCheck != nil && merged.PingCheck.Enabled

	if err := config.ValidateKeepaliveForBackend(merged.Peer.PersistentKeepalive, merged.Backend); err != nil {
		response.Error(w, err.Error(), "INVALID_KEEPALIVE")
		return
	}
	if err := config.ValidateAWG3(&merged.Interface.AWGObfuscation); err != nil {
		response.Error(w, err.Error(), "INVALID_AWG3")
		return
	}

	// Validate endpoint resolves (only if changed)
	if merged.Peer.Endpoint != existing.Peer.Endpoint {
		if _, _, err := netutil.ResolveEndpoint(merged.Peer.Endpoint); err != nil {
			response.Error(w, "endpoint не резолвится: "+err.Error(), "INVALID_ENDPOINT")
			return
		}
	}

	// Service handles runtime RCI based on the diff between existing
	// (pre-patch snapshot) and merged (post-patch state). Storage save
	// happens AFTER service runs. Fail-closed: if the service can't apply
	// the change to the running interface, we don't persist it either,
	// otherwise on-disk state would diverge from the live state.
	if err := h.svc.Update(r.Context(), existing, &merged); err != nil {
		h.log.Warn("update", merged.Name, "Service update failed: "+err.Error())
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
	}

	// svc.Update выше — секунды RCI-обменов; за это время запись могли
	// переписать оркестратор, pingcheck, wdttlink. Транзакция читает её
	// заново под локом, и та же функция кладёт на неё ТОЛЬКО присланные
	// поля — всё остальное остаётся таким, каким его оставил сосед.
	if err := h.store.Update(id, func(t *storage.AWGTunnel) error {
		applyTunnelUpdate(t, &req)
		// svc.Update, настраивая маршрут до нового endpoint'а, попутно
		// резолвит его адрес и кладёт в merged. Это РЕЗУЛЬТАТ сервиса, а не
		// поле запроса, поэтому applyTunnelUpdate его не переносит: без этой
		// строки свежерезолвленный IP не доехал бы до записи, и ближайшему
		// Delete пришлось бы резолвить заново (F55).
		if merged.ResolvedEndpointIP != "" && merged.ResolvedEndpointIP != existing.ResolvedEndpointIP {
			t.ResolvedEndpointIP = merged.ResolvedEndpointIP
		}
		return nil
	}); err != nil {
		h.log.Warn("update", merged.Name, "Failed to update tunnel: "+err.Error())
		if errors.Is(err, storage.ErrNotFound) {
			// Туннель удалили, пока шёл RCI-обмен svc.Update.
			response.Error(w, "tunnel not found", "NOT_FOUND")
			return
		}
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
	}

	// Sync orchestrator's in-memory cache with the new storage state
	// before we hit StopMonitoring / RestartEvent etc. — decide() reads
	// the cache, and a stale PingCheck flag here causes later events to
	// emit phantom ActionRemovePingCheck that warns NDMS.
	if h.orch != nil {
		h.orch.RefreshTunnelState(id)
	}

	// Handle pingCheck changes
	if h.pingCheck != nil {
		stateInfo := h.svc.GetState(r.Context(), id)
		isRunning := stateInfo.State == tunnel.StateRunning

		if oldPingCheckEnabled != newPingCheckEnabled {
			// Toggle: start or stop monitoring
			if newPingCheckEnabled && isRunning {
				h.pingCheck.StartMonitoring(id, merged.Name)
			} else if !newPingCheckEnabled {
				h.pingCheck.StopMonitoring(id)
			}
		}
		// Settings-only changes (method, interval, threshold) are picked up
		// automatically by the monitor loop on each tick via getCheckConfig().
	}

	// Handle primary connection / ISP interface route changes for running tunnels.
	// Routing is only applied during Start, so restart the tunnel to pick up changes.
	routeChanged := merged.ISPInterface != oldISPInterface
	if routeChanged {
		stateInfo := h.svc.GetState(r.Context(), id)
		if stateInfo.State == tunnel.StateRunning {
			if err := h.orch.HandleEvent(r.Context(), orchestrator.Event{
				Type: orchestrator.EventRestart, Tunnel: id,
			}); err != nil {
				h.log.Warn("update", merged.Name, "Restart for routing changes failed: "+err.Error())
			} else {
				h.log.Info("update", merged.Name, "Tunnel restarted to apply routing changes")
			}
		}
	}

	h.log.Info("update", merged.Name, "Tunnel updated")
	h.publishTunnelList(r.Context())

	resp, err := BuildTunnelResponse(r, h.svc, h.store, id, h.quiescentFor(id))
	if err != nil {
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
	}
	if warnings := h.svc.CheckAddressConflicts(r.Context(), id); len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	response.Success(w, resp)
}

// tunnelLockedMessage — текст 403 у всех пяти защищённых операций (#818).
// Один на всех, чтобы пользователь везде читал одну и ту же подсказку.
const tunnelLockedMessage = "туннель защищён от изменений — снимите защиту на карточке"

// SetLock включает и снимает защиту туннеля от изменений (#818).
//
//	@Summary		Set tunnel lock
//	@Description	Включает или снимает защиту туннеля от изменений: у защищённого туннеля Stop, ToggleEnabled, Update и Delete отвечают 403.
//	@Tags			tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		query	string	true	"Tunnel id"
//	@Param			locked	query	bool	true	"Включить (true) или снять (false) защиту"
//	@Success		200	{object}	TunnelLockResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/lock [post]
func (h *TunnelsHandler) SetLock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}
	// Желаемое состояние приходит явно, а не переключением: две вкладки,
	// нажавшие замок одновременно, придут к одному и тому же результату,
	// а не к взаимной отмене.
	locked, err := strconv.ParseBool(r.URL.Query().Get("locked"))
	if err != nil {
		response.Error(w, "параметр locked должен быть true или false", "INVALID_LOCKED")
		return
	}
	// Повторный запрос того же значения не переписывает файл: ErrNoChange
	// гасит запись внутри Update, ответ остаётся успешным.
	if err := h.store.Update(id, func(t *storage.AWGTunnel) error {
		if t.Locked == locked {
			return storage.ErrNoChange
		}
		t.Locked = locked
		return nil
	}); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			response.Error(w, "tunnel not found", "NOT_FOUND")
			return
		}
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
	}
	h.publishTunnelList(r.Context())
	response.Success(w, TunnelLockResultData{ID: id, Locked: locked})
}

// Delete deletes a tunnel.
//
//	@Summary		Delete tunnel
//	@Description	409 приходит в двух формах. tunnel_referenced (тело TunnelReferencedResponse) —
//	@Description	туннель кем-то используется. Обычный конверт ошибки с кодом WDTT_RAW_OWNED или
//	@Description	WDTT_RAW_OWNER_UNKNOWN — запись есть проекция прокси-инстанса и удаляется только
//	@Description	вместе с ним.
//	@Tags			tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	TunnelDeleteResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		403	{object}	APIErrorEnvelope
//	@Failure		409	{object}	TunnelReferencedResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/delete [post]
func (h *TunnelsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	// Защита (#818) отвергает удаление до всякого побочного действия.
	if stored, err := h.store.Get(id); err == nil && stored != nil && stored.Locked {
		response.ErrorWithStatus(w, http.StatusForbidden, tunnelLockedMessage, "TUNNEL_LOCKED")
		return
	}

	if stored, err := h.store.Get(id); err == nil && stored != nil && stored.Backend == backendWdttRaw {
		// Зеркальная запись — не самостоятельная сущность, а проекция
		// прокси-инстанса: пока инстанс жив, удалять её отдельно нельзя.
		// Ближайшее объявление создало бы её заново с дефолтами, и потеря
		// настроек карточки прошла бы молча (амендмент F2).
		owner, ownErr := h.mirrorOwnerKey(stored)
		switch {
		case ownErr != nil:
			h.log.Warn("delete", stored.Name, "Refused: владелец raw-записи не проверен: "+ownErr.Error())
			response.ErrorWithStatus(w, http.StatusConflict,
				"владелец raw-записи не проверен: "+ownErr.Error(), "WDTT_RAW_OWNER_UNKNOWN")
			return
		case owner != "":
			h.log.Info("delete", stored.Name, "Refused: запись принадлежит инстансу "+owner)
			response.ErrorWithStatus(w, http.StatusConflict,
				"запись принадлежит прокси-инстансу "+owner+"; удалите инстанс", "WDTT_RAW_OWNED")
			return
		}
		tunnelName := stored.Name
		if err := h.store.Delete(id); err != nil {
			response.Error(w, err.Error(), "DELETE_FAILED")
			return
		}
		if h.traffic != nil {
			h.traffic.Clear(id)
		}
		h.log.Info("delete", tunnelName, "WDTT raw registry entry removed")
		h.publishTunnelList(r.Context())
		response.Success(w, map[string]interface{}{
			"success":  true,
			"tunnelId": id,
			"verified": true,
		})
		return
	}

	// Get tunnel name for logging before delete
	var tunnelName string
	if t, err := h.svc.Get(r.Context(), id); err == nil {
		tunnelName = t.Name
	}

	// Route through svc.Delete so the refuse-on-delete check fires
	// (returns ErrTunnelReferenced if the tunnel's awg-{id} tag is
	// referenced by deviceproxy selector or any router rule).
	if err := h.svc.Delete(r.Context(), id); err != nil {
		var refErr service.ErrTunnelReferenced
		if errors.As(err, &refErr) {
			h.log.Info("delete", tunnelName, "Refused: "+refErr.Error())
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(TunnelReferencedResponse{
				Error: "tunnel_referenced",
				Details: TunnelReferencedDetails{
					TunnelID:    refErr.TunnelID,
					DeviceProxy: refErr.DeviceProxy,
					RouterRules: refErr.RouterRules,
					RouterOther: refErr.RouterOther,
				},
			})
			return
		}
		if errors.Is(err, tunnel.ErrOperationInProgress) {
			// Занятый замок — ретраибельный конфликт, не отказ удаления.
			// Тот же контракт, что у start/stop/restart в control.go.
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "OPERATION_IN_PROGRESS")
			return
		}
		h.log.Warn("delete", tunnelName, "Failed to delete tunnel: "+err.Error())
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "DELETE_FAILED")
		return
	}

	// Clear traffic history for deleted tunnel
	if h.traffic != nil {
		h.traffic.Clear(id)
	}

	h.log.Info("delete", tunnelName, "Tunnel deleted")
	h.publishTunnelList(r.Context())

	response.Success(w, map[string]interface{}{
		"success":  true,
		"tunnelId": id,
		"verified": true,
	})
}

// Export returns a single tunnel config as a downloadable .conf file.
//
//	@Summary		Export tunnel config
//	@Tags			tunnels
//	@Produce		plain
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{file}	binary
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/export [get]
func (h *TunnelsHandler) Export(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	stored, err := h.store.Get(id)
	if err != nil {
		response.Error(w, "tunnel not found", "NOT_FOUND")
		return
	}

	content := config.GenerateForExport(stored)
	filename := stored.Name + ".conf"

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Write([]byte(content))
}

// ExportAll returns all tunnel configs as a downloadable ZIP archive.
//
//	@Summary		Export all tunnels (zip)
//	@Tags			tunnels
//	@Produce		application/zip
//	@Security		CookieAuth
//	@Success		200	{file}	binary
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/export-all [get]
func (h *TunnelsHandler) ExportAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	tunnels, err := h.store.List()
	if err != nil {
		response.Error(w, "failed to list tunnels", "LIST_FAILED")
		return
	}

	if len(tunnels) == 0 {
		response.Error(w, "no tunnels to export", "NO_TUNNELS")
		return
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for _, t := range tunnels {
		stored, err := h.store.Get(t.ID)
		if err != nil {
			continue
		}
		content := config.GenerateForExport(stored)
		fw, err := zw.Create(stored.Name + ".conf")
		if err != nil {
			continue
		}
		fw.Write([]byte(content))
	}

	zw.Close()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"awg-tunnels.zip\"")
	w.Write(buf.Bytes())
}

// ReplaceConf replaces a tunnel's configuration from a new .conf file.
// If the tunnel is running, it is stopped before replacement and restarted after.
//
//	@Summary		Replace tunnel from conf
//	@Tags			tunnels
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		403	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/replace [post]
func (h *TunnelsHandler) ReplaceConf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}
	req, ok := parseJSON[struct {
		Content string `json:"content"`
		Name    string `json:"name"`
	}](w, r, http.MethodPost)
	if !ok {
		return
	}

	if req.Content == "" {
		response.BadRequest(w, "missing config content")
		return
	}

	// Check tunnel exists
	stored, err := h.store.Get(id)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusNotFound, "tunnel not found", "NOT_FOUND")
		return
	}

	// Защита (#818) отвергает замену конфига до всякого побочного действия:
	// и до svc.Stop, и до самой записи конфига.
	if stored.Locked {
		response.ErrorWithStatus(w, http.StatusForbidden, tunnelLockedMessage, "TUNNEL_LOCKED")
		return
	}

	// Check if running — need to stop before replacing config
	stateInfo := h.svc.GetState(r.Context(), id)
	wasRunning := stateInfo.State == tunnel.StateRunning

	if wasRunning {
		if err := h.svc.Stop(r.Context(), id); err != nil {
			if errors.Is(err, tunnel.ErrOperationInProgress) {
				// Замок занят — конфиг не тронут, повтор через несколько
				// секунд пройдёт. Ветка ловит только случай «туннель виден
				// работающим»: если залипшая операция уже уронила видимое
				// состояние, Stop пропускается и замена идёт мимо замка
				// оркестратора — отдельная дыра, не эта.
				response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "OPERATION_IN_PROGRESS")
				return
			}
			response.InternalError(w, "failed to stop tunnel before config replace: "+err.Error())
			return
		}
	}

	// Replace config
	var warnings []string
	if err := h.svc.ReplaceConfig(r.Context(), id, req.Content, req.Name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
			return
		}
		if strings.Contains(err.Error(), "parse conf") {
			response.BadRequest(w, err.Error())
			return
		}
		response.InternalError(w, err.Error())
		return
	}

	// Restart if was running
	if wasRunning {
		if err := h.svc.Start(r.Context(), id); err != nil {
			warnings = append(warnings, "tunnel config replaced but failed to restart: "+err.Error())
		}
	}

	h.publishTunnelList(r.Context())

	resp, err := BuildTunnelResponse(r, h.svc, h.store, id, h.quiescentFor(id))
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if conflicts := h.svc.CheckAddressConflicts(r.Context(), id); len(conflicts) > 0 {
		warnings = append(warnings, conflicts...)
	}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	response.Success(w, resp)
}

// applyTunnelUpdate накладывает декодированное тело PATCH req на запись t.
//
// ЕДИНСТВЕННОЕ место, определяющее, что правится через /api/tunnels/update:
// поле без присваивания здесь этим handler'ом не правится вовсе. Новое поле
// AWGTunnel по умолчанию неправимо — отказ фейлится закрыто («моё поле не
// сохраняется»), а не молчаливой потерей чужих данных. Классификацию всех
// полей пинит TestTunnelUpdate_FieldInventoryComplete.
//
// Чистая: req не мутирует и в его указуемые объекты не пишет — функция
// применяется дважды (к копии снимка для валидаций и diff, и к свежей записи
// внутри мутатора стора, где по контракту K10 §1.1 позволены только
// присваивания заранее вычисленных значений).
func applyTunnelUpdate(t *storage.AWGTunnel, req *storage.AWGTunnel) {
	if req.Name != "" {
		// "" — и «ключ не прислали», и явная пустая строка: оба значат
		// «имя не менять», различить их в этом контракте нечем.
		t.Name = req.Name
	}
	t.Interface = mergedInterface(t.Interface, req.Interface)
	if req.Peer.PublicKey != "" && req.Peer.Endpoint != t.Peer.Endpoint {
		// Кэш резолва валиден только для endpoint'а, под которым получен:
		// перенос через смену endpoint'а подставлял бы DNS-фолбэкам адрес
		// ПРЕЖНЕГО имени. Сравнение — с endpoint'ом самой записи t.
		t.ResolvedEndpointIP = ""
	}
	t.Peer = mergedPeer(t.Peer, req.Peer)
	if req.DefaultRouteSet {
		// DefaultRouteSet — компаньон «поле прислали»: модалки эхо-шлют
		// defaultRoute из GET-ответа, где компаньона нет, и без гарда
		// протухшее эхо перебивало бы тумблер.
		t.DefaultRoute, t.DefaultRouteSet = req.DefaultRoute, true
	}
	switch {
	case req.ISPInterface == tunnel.ISPInterfaceAuto:
		// Страница маршрутизации так шлёт «авто» — это очистка, не «не прислали».
		t.ISPInterface, t.ISPInterfaceLabel = "", ""
	case req.ISPInterface != "":
		t.ISPInterface, t.ISPInterfaceLabel = req.ISPInterface, req.ISPInterfaceLabel
	}
	if req.PingCheck != nil {
		t.PingCheck = req.PingCheck
	}
	if req.ConnectivityCheck != nil && req.ConnectivityCheck.Method != "" {
		t.ConnectivityCheck = req.ConnectivityCheck
	}
}

// mergedInterface applies the edit-form whitelist of req on top of base.
// Address, MTU, DNS, and the AmneziaWG obfuscation block (Qlen, Jc, Jmin,
// Jmax, S1-S4, H1-H4, I1-I5) are taken from req; PrivateKey is taken from req
// only when non-empty so a save without a fresh key keeps the existing one.
//
// Partial-update safety net: when req.Address is empty the entire Interface is
// treated as missing (routing-page calls that only touch ispInterface) and
// base is returned untouched. Callers that send Address MUST send the rest of
// the interface body too, otherwise the empty fields will overwrite existing
// values — the frontend's buildUpdatePayload spreads ...tunnel.interface for
// that reason.
func mergedInterface(base, req storage.AWGInterface) storage.AWGInterface {
	if req.Address == "" {
		return base
	}
	base.Address = req.Address
	base.MTU = req.MTU
	base.DNS = req.DNS
	if req.PrivateKey != "" {
		base.PrivateKey = req.PrivateKey
	}
	// AWG obfuscation block (issue #131): editable in the full edit form,
	// so req is the source of truth — including explicit clears (i1 -> "").
	base.AWGObfuscation = req.AWGObfuscation
	return base
}

// mergedPeer applies the edit-form whitelist of req on top of base. Five
// fields (PublicKey, PresharedKey, Endpoint, AllowedIPs, PersistentKeepalive)
// are taken from req when PublicKey is non-empty; otherwise the entire Peer
// preserves from base (partial update without peer).
func mergedPeer(base, req storage.AWGPeer) storage.AWGPeer {
	if req.PublicKey == "" {
		return base
	}
	base.PublicKey = req.PublicKey
	if req.PresharedKey != "" {
		// Пусто = «оставить прежний», как у PrivateKey в mergedInterface: GET
		// больше не отдаёт PSK, и эхо трёх модалок шлёт пустоту. ОСОЗНАННАЯ
		// ПОТЕРЯ (решение владельца, F70): очистить PSK через API теперь
		// нельзя — нужен явный контракт (компаньон-флаг вроде DefaultRouteSet
		// или отдельная ручка), если очистка когда-нибудь понадобится.
		base.PresharedKey = req.PresharedKey
	}
	base.Endpoint = req.Endpoint
	base.AllowedIPs = req.AllowedIPs
	base.PersistentKeepalive = req.PersistentKeepalive
	return base
}
