package api

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/heavyop"
	"github.com/hoaxisr/awg-manager/internal/singbox/router/selective"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ── DTOs ────────────────────────────────────────────────────────────────────

// SelectiveStatusData is the payload for GET /singbox/router/selective/status.
type SelectiveStatusData struct {
	// Available reports whether the ipset binary is present on the router.
	Available bool `json:"available"`
	// XtSetAvailable reports whether the xt_set kernel module is loaded or
	// available as a .ko file (required for -m set iptables matching).
	XtSetAvailable bool `json:"xtSetAvailable"`
	// ConntrackAvailable reports whether the conntrack binary is present.
	// Without it a routing change applies only to new connections (existing
	// flows linger until they expire).
	ConntrackAvailable bool `json:"conntrackAvailable"`
	// Installing is true while opkg install ipset is running.
	Installing bool `json:"installing"`
	// Rebuilding is true while an ipset rebuild is in flight (POST /rebuild
	// responds 202 and runs the rebuild in the background; completion is
	// delivered via the singbox-router:selective-status SSE event).
	Rebuilding bool `json:"rebuilding"`
	// Enabled mirrors SingboxRouterSettings.SelectiveBypass.
	Enabled bool `json:"enabled"`
	// EntryCount is the current number of entries in the AWGM-SELECTIVE ipset.
	// 0 when the set does not exist or is empty.
	EntryCount int `json:"entryCount"`
	// LastRebuild is the RFC3339 timestamp of the last successful rebuild, or
	// empty string if no rebuild has run yet.
	LastRebuild string `json:"lastRebuild,omitempty"`
	// LastError is the error message from the last failed rebuild, or empty.
	LastError string `json:"lastError,omitempty"`
	// Snapshot is summary metadata from the last rebuild (no IP lists).
	Snapshot *SelectiveRebuildSnapshotDTO `json:"snapshot,omitempty"`
}

// SelectiveRebuildSnapshotDTO is the API shape of last-rebuild summary metadata.
type SelectiveRebuildSnapshotDTO struct {
	RebuiltAt          string   `json:"rebuiltAt"`
	StaticCIDRs        []string `json:"staticCidrs"`
	DomainResults      []any    `json:"domainResults"`
	EntryCount         int      `json:"entryCount"`
	StaticCIDRCount    int      `json:"staticCidrCount,omitempty"`
	DomainMatcherCount int      `json:"domainMatcherCount,omitempty"`
	LastCDNRefresh     string   `json:"lastCDNRefresh,omitempty"`
}

// SelectiveDomainMatcherRecordDTO is one matcher row from the NDJSON snapshot.
type SelectiveDomainMatcherRecordDTO struct {
	Matcher    string   `json:"matcher"`
	Kind       string   `json:"kind"`
	QueryHosts []string `json:"queryHosts"`
	CDN        bool     `json:"cdn,omitempty"`
	Outbound   string   `json:"outbound,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// SelectiveSnapshotMatchersData is the payload for GET .../snapshot/matchers.
type SelectiveSnapshotMatchersData struct {
	Matchers []SelectiveDomainMatcherRecordDTO `json:"matchers"`
	Total    int                               `json:"total"`
	Offset   int                               `json:"offset"`
	Limit    int                               `json:"limit"`
}

func selectiveSnapshotDTO(s *selective.RebuildSnapshot) *SelectiveRebuildSnapshotDTO {
	if s == nil {
		return nil
	}
	domainResults := []any{}
	if s.DomainResults != nil {
		domainResults = make([]any, 0)
	}
	return &SelectiveRebuildSnapshotDTO{
		RebuiltAt:          s.RebuiltAt,
		StaticCIDRs:        s.StaticCIDRs,
		DomainResults:      domainResults,
		EntryCount:         s.EntryCount,
		StaticCIDRCount:    s.StaticCIDRCount,
		DomainMatcherCount: s.DomainMatcherCount,
		LastCDNRefresh:     s.LastCDNRefresh,
	}
}

func selectiveMatcherDTOs(in []selective.DomainMatcherRecord) []SelectiveDomainMatcherRecordDTO {
	out := make([]SelectiveDomainMatcherRecordDTO, len(in))
	for i, rec := range in {
		qh := rec.QueryHosts
		if qh == nil {
			qh = []string{}
		}
		out[i] = SelectiveDomainMatcherRecordDTO{
			Matcher:    rec.Matcher,
			Kind:       rec.Kind,
			QueryHosts: qh,
			CDN:        rec.CDN,
			Outbound:   rec.Outbound,
			Error:      rec.Error,
		}
	}
	return out
}

// ── Handler ──────────────────────────────────────────────────────────────────

// SelectiveHandler serves the /api/singbox/router/selective/* endpoints.
type SelectiveHandler struct {
	settings  *storage.SettingsStore
	configDir string
	// installing serializes opkg install runs: CompareAndSwap makes the
	// check-and-set atomic so two concurrent InstallDeps requests cannot
	// both slip past the guard and race opkg against itself.
	installing atomic.Bool
	// rebuilding serializes handler-launched ipset rebuilds the same way:
	// a concurrent POST /rebuild (nginx retry after 504, second tab) gets
	// 202 + rebuilding:true instead of a duplicate background run.
	rebuilding atomic.Bool
	builder    SelectiveRebuildTriggerer
	status     SelectiveStatusProvider
	log        *logging.ScopedLogger
}

// Настенного таймаута фоновой пересборки ipset больше нет: медленная, но
// идущая работа на MIPS-роутере легально длится дольше любого фиксированного
// потолка. Продолжительность ограничивает stall guard внутри билдера
// (selective.WithStallGuard) — отмена только при отсутствии прогресса
// (rebuildStallTimeout) либо по абсолютному предохранителю (rebuildHardCap).

// installTimeout is the overall backstop for one opkg install run.
const installTimeout = 10 * time.Minute

// SelectiveRebuildTriggerer is the narrow interface the handler needs from
// the router service to force an ipset rebuild. Implemented by a wrapper
// that calls ServiceImpl.triggerSelectiveRebuild or calls Rebuild directly.
type SelectiveRebuildTriggerer interface {
	Rebuild(ctx context.Context) error
}

// SelectiveStatusProvider exposes the last-rebuild bookkeeping the handler
// reports in GetStatus. *selective.Builder satisfies it.
type SelectiveStatusProvider interface {
	LastRebuild() string
	LastError() string
	LastSnapshot() *selective.RebuildSnapshot
	// Rebuilding reports a rebuild in flight regardless of who started it
	// (this handler or the boot/reconcile auto-rebuild).
	Rebuilding() bool
}

// SelectiveRebuildCanceller — необязательная способность status-провайдера
// останавливать текущую пересборку (CancelRebuild проверяет её type-assert'ом:
// *selective.Builder реализует, тестовые заглушки без неё получают
// cancelled:false). reason == nil трактуется как «отменено пользователем».
type SelectiveRebuildCanceller interface {
	CancelRun(reason error) bool
}

// SelectiveCancelData is the payload for POST .../rebuild/cancel.
type SelectiveCancelData struct {
	// Cancelled is true when an active rebuild run was found and its
	// cancellation was requested; false when no rebuild was running.
	Cancelled bool `json:"cancelled"`
}

// NewSelectiveHandler creates a new handler. configDir is the sing-box config.d
// path used to read NDJSON matcher snapshots. status may be nil.
// appLogger may be nil — the scoped logger is nil-safe.
func NewSelectiveHandler(settings *storage.SettingsStore, configDir string, builder SelectiveRebuildTriggerer, status SelectiveStatusProvider, appLogger logging.AppLogger) *SelectiveHandler {
	return &SelectiveHandler{
		settings:  settings,
		configDir: configDir,
		builder:   builder,
		status:    status,
		log:       logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubSelective),
	}
}

// GetStatus handles GET /api/singbox/router/selective/status.
//
//	@Summary		Selective-bypass status
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=SelectiveStatusData}
//	@Router			/singbox/router/selective/status [get]
func (h *SelectiveHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	response.Success(w, h.statusData(r.Context()))
}

// statusData assembles the SelectiveStatusData payload shared by GetStatus
// and the 202 responses of Rebuild/InstallDeps.
func (h *SelectiveHandler) statusData(ctx context.Context) SelectiveStatusData {
	enabled := false
	if h.settings != nil {
		if settings, err := h.settings.Get(); err == nil {
			enabled = settings.SingboxRouter.SelectiveBypass
		}
	}

	// EntryCount already returns 0 when the set does not exist — no separate
	// SetExists fork needed on this per-status-request path.
	entryCount := selective.EntryCount(ctx)

	lastRebuild, lastError := "", ""
	rebuilding := h.rebuilding.Load()
	var snapshot *selective.RebuildSnapshot
	if h.status != nil {
		lastRebuild = h.status.LastRebuild()
		lastError = h.status.LastError()
		snapshot = h.status.LastSnapshot()
		rebuilding = rebuilding || h.status.Rebuilding()
	}

	return SelectiveStatusData{
		Available:          selective.IsIPSetAvailable(),
		XtSetAvailable:     selective.IsXtSetAvailable(),
		ConntrackAvailable: selective.IsConntrackAvailable(),
		Installing:         h.installing.Load(),
		Rebuilding:         rebuilding,
		Enabled:            enabled,
		EntryCount:         entryCount,
		LastRebuild:        lastRebuild,
		LastError:          lastError,
		Snapshot:           selectiveSnapshotDTO(snapshot),
	}
}

// SelectiveSnapshotMatchersData is the payload for GET .../snapshot/matchers.
//
// GetSnapshotMatchers handles GET /api/singbox/router/selective/snapshot/matchers.
//
//	@Summary		List domain matchers from last ipset rebuild
//	@Description	Returns a paginated slice of matcher records (DNS query hosts, no IPs) from the NDJSON snapshot.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Param			offset	query		int	false	"Zero-based record offset"	default(0)
//	@Param			limit	query		int	false	"Page size (max 1000)"		default(200)
//	@Success		200		{object}	APIEnvelope{data=SelectiveSnapshotMatchersData}
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/selective/snapshot/matchers [get]
func (h *SelectiveHandler) GetSnapshotMatchers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	offset := queryIntDefault(r, "offset", 0)
	limit := queryIntDefault(r, "limit", 200)
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	matchers, total, err := selective.ReadSnapshotMatchers(h.configDir, offset, limit)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, SelectiveSnapshotMatchersData{
		Matchers: selectiveMatcherDTOs(matchers),
		Total:    total,
		Offset:   offset,
		Limit:    limit,
	})
}

func queryIntDefault(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

// InstallDeps handles POST /api/singbox/router/selective/install-deps.
// Runs `opkg install ipset` and emits progress to SSE.
//
//	@Summary		Install ipset package
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=SelectiveStatusData}
//	@Failure		409	{object}	APIErrorEnvelope	"already installing"
//	@Router			/singbox/router/selective/install-deps [post]
func (h *SelectiveHandler) InstallDeps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	// Свежий вердикт вместо кэша: бинарь мог сломаться или исчезнуть минуту
	// назад, и «уже установлено» по пятиминутному кэшу оставляло бы
	// пользователя без починки через UI.
	selective.RecheckIPSet()
	if selective.IsIPSetAvailable() {
		// Already installed and runnable — just return current status.
		h.GetStatus(w, r)
		return
	}
	if !h.installing.CompareAndSwap(false, true) {
		response.ErrorWithStatus(w, http.StatusConflict, "ipset installation already in progress", "INSTALLING")
		return
	}
	defer h.installing.Store(false)

	// Detach cancellation: a client disconnect must not SIGKILL opkg in the
	// middle of a package transaction (same rationale as Rebuild below).
	// The timeout backstops a wedged opkg (dead mirror) so the request —
	// still synchronous — cannot hang forever.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), installTimeout)
	defer cancel()
	if err := selective.InstallIPSet(ctx, nil); err != nil { // progress delivered via SSE by the builder
		h.log.Warn("install-deps", "ipset", "installation failed: "+err.Error())
		response.InternalError(w, "ipset installation failed: "+err.Error())
		return
	}

	// Try to load xt_set now that ipset is installed.
	_ = selective.EnsureXtSetModule(ctx)

	h.log.Info("install-deps", "ipset", "ipset package installed")
	h.GetStatus(w, r)
}

// InstallConntrack handles POST /api/singbox/router/selective/install-conntrack.
// Installs the conntrack-tools package so routing changes evict stale flows
// immediately instead of waiting for them to expire.
//
//	@Summary		Install conntrack-tools package
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=SelectiveStatusData}
//	@Router			/singbox/router/selective/install-conntrack [post]
func (h *SelectiveHandler) InstallConntrack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if selective.IsConntrackAvailable() {
		h.GetStatus(w, r)
		return
	}
	if !h.installing.CompareAndSwap(false, true) {
		response.ErrorWithStatus(w, http.StatusConflict, "package installation already in progress", "INSTALLING")
		return
	}
	defer h.installing.Store(false)

	// Detach cancellation + backstop — see InstallDeps.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), installTimeout)
	defer cancel()
	if err := selective.InstallConntrackTools(ctx, nil); err != nil {
		h.log.Warn("install-conntrack", "conntrack-tools", "installation failed: "+err.Error())
		response.InternalError(w, "conntrack installation failed: "+err.Error())
		return
	}
	h.log.Info("install-conntrack", "conntrack-tools", "conntrack-tools package installed")
	h.GetStatus(w, r)
}

// Rebuild handles POST /api/singbox/router/selective/rebuild.
// Starts an ipset rebuild in the background and responds 202 immediately:
// a full rebuild (DNS resolve over thousands of matchers) easily exceeds the
// ~60s proxy_read_timeout of the Keenetic nginx reverse proxy, and a
// synchronous handler handed the browser nginx's stock 504 page while the
// rebuild kept running. Progress/completion reach the UI via the
// singbox-router:selective-progress / selective-status SSE events.
//
//	@Summary		Force ipset rebuild (asynchronous)
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		202	{object}	APIEnvelope{data=SelectiveStatusData}	"rebuild started (or already running); rebuilding reflects live state"
//	@Failure		409	{object}	APIErrorEnvelope	"sing-box config apply in progress"
//	@Failure		503	{object}	APIErrorEnvelope	"builder not configured"
//	@Router			/singbox/router/selective/rebuild [post]
func (h *SelectiveHandler) Rebuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.builder == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "selective builder not configured", "NOT_CONFIGURED")
		return
	}
	// Пересборка при неактивном селективе (флаг снят, движок выключен или
	// «спящий» флаг в режиме fakeip-tun) отклоняется: завершившийся rebuild
	// включал бы припаркованный слот 19-selective-routes.json, и его правила
	// мерджились бы в работающий fakeip-конфиг (#564).
	if h.settings != nil {
		if st, err := h.settings.Load(); err == nil && !st.SingboxRouter.SelectiveActive() {
			response.ErrorWithStatus(w, http.StatusConflict,
				"селективный перехват не активен (выключен или режим не tproxy) — пересборка не выполняется",
				"SELECTIVE_INACTIVE")
			return
		}
	}
	// Pre-checks run BEFORE the CAS so the guard flips only at the point of
	// no return: flipping first and then bailing (already-rebuilding, gate
	// held) would let a concurrent request see the flag, answer 202
	// «rebuilding» and piggyback on a run that never starts.
	for {
		if h.status != nil && h.status.Rebuilding() {
			// A rebuild is already in flight (boot/reconcile auto-rebuild or
			// one launched via the adapter) — it serves the same intent.
			h.respondRebuildAccepted(w, r)
			return
		}
		if !heavyop.Default.TryLock() {
			// A sing-box config apply holds the heavy-op gate: fail fast with
			// an honest 409 instead of accepting work that would only queue
			// behind the apply.
			response.ErrorWithStatus(w, http.StatusConflict, selective.ErrBusy.Error(), "OPERATION_IN_PROGRESS")
			return
		}
		heavyop.Default.Unlock()
		if h.rebuilding.CompareAndSwap(false, true) {
			break
		}
		if h.rebuilding.Load() {
			// Concurrent POST (nginx retry, second tab) owns a live run — it
			// already serves the user's intent, don't start a second one.
			h.respondRebuildAccepted(w, r)
			return
		}
		// The concurrent run ended between the CAS and the Load — re-run the
		// pre-checks and try to own a fresh run.
	}

	// Point of no return: the flag is ours and the goroutine below is the
	// only thing that clears it.
	// Detach cancellation: a client disconnecting mid-rebuild must not abort
	// the populate (a partial ipset is worse than a stale one). Настенного
	// таймаута нет — застрявшую пересборку (и залипание флага) отлавливает
	// stall guard внутри билдера (selective.WithStallGuard): отмена при
	// отсутствии прогресса либо по абсолютному предохранителю; материализация
	// rule-set'ов (decompile/dat) подчинена guard-контексту и собственным
	// exec-таймаутам, так что зависнуть «мимо guard'а» ей негде. Медленную,
	// но живую пересборку guard легально не трогает до предохранителя —
	// ограниченный ручной выход даёт CancelRebuild (POST /rebuild/cancel).
	ctx := context.WithoutCancel(r.Context())
	go func() {
		defer h.rebuilding.Store(false)
		// Errors are already surfaced by the builder: terminal
		// selective-progress/selective-status SSE events plus lastError in
		// the status payload.
		_ = h.builder.Rebuild(ctx)
	}()

	h.respondRebuildAccepted(w, r)
}

// respondRebuildAccepted writes the 202 payload with the LIVE rebuilding
// flag: on the launch path h.rebuilding is already set, and on the piggyback
// paths the live value is the honest one — forcing true here could overwrite
// a terminal state the UI already received via SSE (an instantly-failed run
// would otherwise leave the button stuck at «Пересборка…»).
func (h *SelectiveHandler) respondRebuildAccepted(w http.ResponseWriter, r *http.Request) {
	response.Accepted(w, h.statusData(r.Context()))
}

// CancelRebuild handles POST /api/singbox/router/selective/rebuild/cancel.
// Останавливает текущую пересборку ipset по запросу пользователя —
// ограниченный выход из медленной, но «живой» пересборки, которую stall guard
// легально не трогает до 2-часового предохранителя (иначе единственным
// выходом был перезапуск демона). Завершение прилетает обычным путём: билдер
// публикует терминальные SSE-события selective-progress (PhaseError,
// «пересборка отменена пользователем») и selective-status.
//
//	@Summary		Cancel a running ipset rebuild
//	@Description	Requests cancellation of the in-flight selective ipset rebuild. Returns cancelled=false when no rebuild is running (safe no-op). Completion is delivered via the selective-progress/selective-status SSE events.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=SelectiveCancelData}
//	@Failure		405	{object}	APIErrorEnvelope
//	@Router			/singbox/router/selective/rebuild/cancel [post]
func (h *SelectiveHandler) CancelRebuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	cancelled := false
	if c, ok := h.status.(SelectiveRebuildCanceller); ok {
		cancelled = c.CancelRun(selective.ErrCancelledByUser)
	}
	if cancelled {
		h.log.Info("rebuild-cancel", "", "ipset rebuild cancellation requested")
	}
	response.Success(w, SelectiveCancelData{Cancelled: cancelled})
}
