package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/adaptiverouting"
	"github.com/hoaxisr/awg-manager/internal/response"
)

type AdaptiveRoutingHandler struct {
	svc *adaptiverouting.Service
}

func NewAdaptiveRoutingHandler(svc *adaptiverouting.Service) *AdaptiveRoutingHandler {
	return &AdaptiveRoutingHandler{svc: svc}
}

func (h *AdaptiveRoutingHandler) RegisterRoutes(mux *http.ServeMux, guarded func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/adaptive-routing/status", guarded(h.handleStatus))
	mux.HandleFunc("GET /api/adaptive-routing/settings", guarded(h.handleGetSettings))
	mux.HandleFunc("PUT /api/adaptive-routing/settings", guarded(h.handlePutSettings))
	mux.HandleFunc("GET /api/adaptive-routing/egresses", guarded(h.handleListEgresses))
	mux.HandleFunc("POST /api/adaptive-routing/preview", guarded(h.handlePreview))
	mux.HandleFunc("POST /api/adaptive-routing/apply", guarded(h.handleApply))
	mux.HandleFunc("POST /api/adaptive-routing/start", guarded(h.handleStart))
	mux.HandleFunc("POST /api/adaptive-routing/stop", guarded(h.handleStop))
	mux.HandleFunc("POST /api/adaptive-routing/restart", guarded(h.handleRestart))
	mux.HandleFunc("POST /api/adaptive-routing/install", guarded(h.handleInstall))
	mux.HandleFunc("POST /api/adaptive-routing/uninstall", guarded(h.handleUninstall))
	mux.HandleFunc("POST /api/adaptive-routing/test-egress", guarded(h.handleTestEgress))
	mux.HandleFunc("GET /api/adaptive-routing/learned", guarded(h.handleLearned))
	mux.HandleFunc("GET /api/adaptive-routing/logs", guarded(h.handleLogs))
	mux.HandleFunc("POST /api/adaptive-routing/forget", guarded(h.handleForget))
	mux.HandleFunc("POST /api/adaptive-routing/cache/clear", guarded(h.handleClearCache))
}

func (h *AdaptiveRoutingHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	state, settings, err := h.svc.GetStatus(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]interface{}{
		"state":    state,
		"settings": settings,
	})
}

func (h *AdaptiveRoutingHandler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	response.Success(w, h.svc.GetStore().GetSettings())
}

func (h *AdaptiveRoutingHandler) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var settings adaptiverouting.Settings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "INVALID_REQUEST")
		return
	}

	state, err := h.svc.Apply(r.Context(), settings)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "APPLY_FAILED")
		return
	}

	response.Success(w, map[string]interface{}{
		"state":    state,
		"settings": h.svc.GetStore().GetSettings(),
	})
}

func (h *AdaptiveRoutingHandler) handleListEgresses(w http.ResponseWriter, r *http.Request) {
	egresses, err := h.svc.GetCatalog().ListEgresses(r.Context())
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]interface{}{
		"items": egresses,
	})
}

func (h *AdaptiveRoutingHandler) handlePreview(w http.ResponseWriter, r *http.Request) {
	var candidate adaptiverouting.Settings
	if err := json.NewDecoder(r.Body).Decode(&candidate); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "INVALID_REQUEST")
		return
	}

	resolved, err := h.svc.Preview(r.Context(), candidate)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "PREVIEW_FAILED")
		return
	}

	response.Success(w, map[string]interface{}{
		"egress": resolved,
	})
}

func (h *AdaptiveRoutingHandler) handleApply(w http.ResponseWriter, r *http.Request) {
	var settings adaptiverouting.Settings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "INVALID_REQUEST")
		return
	}

	state, err := h.svc.Apply(r.Context(), settings)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "APPLY_FAILED")
		return
	}

	response.Success(w, map[string]interface{}{
		"state":    state,
		"settings": h.svc.GetStore().GetSettings(),
	})
}

func (h *AdaptiveRoutingHandler) handleStart(w http.ResponseWriter, r *http.Request) {
	state, err := h.svc.Start(r.Context())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "START_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{
		"state": state,
	})
}

func (h *AdaptiveRoutingHandler) handleStop(w http.ResponseWriter, r *http.Request) {
	state, err := h.svc.Stop(r.Context())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "STOP_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{
		"state": state,
	})
}

func (h *AdaptiveRoutingHandler) handleRestart(w http.ResponseWriter, r *http.Request) {
	state, err := h.svc.Restart(r.Context())
	if err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "RESTART_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{
		"state": state,
	})
}

func (h *AdaptiveRoutingHandler) handleInstall(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Install(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "INSTALL_FAILED")
		return
	}
	state, settings, _ := h.svc.GetStatus(r.Context())
	response.Success(w, map[string]interface{}{
		"state":    state,
		"settings": settings,
	})
}

func (h *AdaptiveRoutingHandler) handleUninstall(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Uninstall(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "UNINSTALL_FAILED")
		return
	}
	state, settings, _ := h.svc.GetStatus(r.Context())
	response.Success(w, map[string]interface{}{
		"state":    state,
		"settings": settings,
	})
}

func (h *AdaptiveRoutingHandler) handleTestEgress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target adaptiverouting.EgressRef `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid request body", "INVALID_REQUEST")
		return
	}

	resolved, err := h.svc.GetCatalog().Resolve(r.Context(), body.Target)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "RESOLVE_FAILED")
		return
	}

	response.Success(w, map[string]interface{}{
		"available": resolved.Available,
		"interface": resolved.Interface,
		"reason":    resolved.UnavailableReason,
	})
}

func (h *AdaptiveRoutingHandler) handleLearned(w http.ResponseWriter, r *http.Request) {
	state := h.svc.GetStore().GetState()
	settings := h.svc.GetStore().GetSettings()

	learnedSets, _ := h.svc.GetLearnedEntries(r.Context())
	if learnedSets == nil {
		learnedSets = make(map[string][]string)
	}

	okTcp := learnedSets[adaptiverouting.SetOkTcp]
	if okTcp == nil {
		okTcp = []string{}
	}
	okUdp := learnedSets[adaptiverouting.SetOkUdp]
	if okUdp == nil {
		okUdp = []string{}
	}
	testTcp := learnedSets[adaptiverouting.SetTestTcp]
	if testTcp == nil {
		testTcp = []string{}
	}
	testUdp := learnedSets[adaptiverouting.SetTestUdp]
	if testUdp == nil {
		testUdp = []string{}
	}
	okNet := learnedSets[adaptiverouting.SetOkNet]
	if okNet == nil {
		okNet = []string{}
	}
	never := learnedSets[adaptiverouting.SetNever]
	if len(never) == 0 {
		never = settings.NeverEntries
	}

	knowledgeMap := make(map[string]map[string]string)
	allLearned := append(append(append([]string{}, okTcp...), okUdp...), okNet...)
	for _, ip := range allLearned {
		if info := adaptiverouting.FindDomainKnowledge("", ip); info != nil {
			knowledgeMap[ip] = map[string]string{
				"title":       info.Title,
				"description": info.Description,
				"org":         info.Org,
				"country":     info.Country,
				"cc":          info.CountryCode,
				"category":    info.Category,
				"icon":        info.Icon,
			}
		}
	}

	response.Success(w, map[string]interface{}{
		"testTcpCount": state.TestingTCPCount,
		"testUdpCount": state.TestingUDPCount,
		"okTcpCount":   state.LearnedTCPCount,
		"okUdpCount":   state.LearnedUDPCount,
		"always":       settings.AlwaysEntries,
		"never":        never,
		"okTcp":        okTcp,
		"okUdp":        okUdp,
		"okNet":        okNet,
		"testTcp":      testTcp,
		"testUdp":      testUdp,
		"knowledge":    knowledgeMap,
	})
}

func (h *AdaptiveRoutingHandler) handleLogs(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}
	events, err := h.svc.GetRecentLogs(r.Context(), limit)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]interface{}{
		"events": events,
	})
}


func (h *AdaptiveRoutingHandler) handleForget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target   string `json:"target"`
		Protocol string `json:"protocol,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Target == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "target is required", "INVALID_REQUEST")
		return
	}

	proto := body.Protocol
	if proto == "" {
		proto = "all"
	}

	if err := h.svc.Forget(r.Context(), body.Target, proto); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]interface{}{
		"forgotten": body.Target,
		"protocol":  proto,
	})
}

func (h *AdaptiveRoutingHandler) handleClearCache(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.ClearCache(r.Context()); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, map[string]interface{}{
		"cleared": true,
	})
}
