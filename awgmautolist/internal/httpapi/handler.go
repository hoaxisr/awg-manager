package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/engine"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/store"
)

// Engine is the operational surface needed by the loopback API.
type Engine interface {
	Status() engine.Status
	PatchConfig([]byte) (config.Config, error)
	Add(context.Context, string) error
	Dismiss(string) error
	Delete(context.Context, string) error
	Forget(string) error
	Restore(string) error
	ClearHistory() error
}

type Handler struct {
	engine  Engine
	config  *config.Store
	state   *store.Store
	version string
}

func New(eng Engine, cfg *config.Store, state *store.Store, version string) http.Handler {
	h := &Handler{engine: eng, config: cfg, state: state, version: version}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", h.status)
	mux.HandleFunc("GET /config", h.getConfig)
	mux.HandleFunc("POST /config", h.patchConfig)
	mux.HandleFunc("POST /add", h.action(func(ctx context.Context, id string) error { return eng.Add(ctx, id) }))
	mux.HandleFunc("POST /dismiss", h.action(func(_ context.Context, id string) error { return eng.Dismiss(id) }))
	mux.HandleFunc("POST /forget", h.action(func(_ context.Context, id string) error { return eng.Forget(id) }))
	mux.HandleFunc("POST /restore", h.action(func(_ context.Context, id string) error { return eng.Restore(id) }))
	mux.HandleFunc("POST /remove", h.action(func(ctx context.Context, id string) error { return eng.Delete(ctx, id) }))
	mux.HandleFunc("POST /history/clear", h.clearHistory)
	return LimitBody(mux)
}

type statusDocument struct {
	engine.Status
	Version    string               `json:"version"`
	Candidates []model.Candidate    `json:"candidates"`
	History    []model.HistoryEntry `json:"history"`
}

func (h *Handler) status(w http.ResponseWriter, _ *http.Request) {
	candidates := h.state.CandidatesByFirstSeen(time.Now())
	historyState := h.state.Snapshot(time.Now()).History
	history := make([]model.HistoryEntry, len(historyState))
	for i := range historyState {
		history[len(historyState)-1-i] = historyState[i]
	}
	WriteJSON(w, http.StatusOK, statusDocument{Status: h.engine.Status(), Version: h.version, Candidates: candidates, History: history})
}

func (h *Handler) getConfig(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, h.effectiveConfig(h.config.Config()))
}

func (h *Handler) patchConfig(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := h.engine.PatchConfig(data)
	if err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	WriteJSON(w, http.StatusOK, h.effectiveConfig(cfg))
}

func (h *Handler) effectiveConfig(cfg config.Config) config.Config {
	if cfg.TargetInterface == "" {
		cfg.TargetInterface = h.engine.Status().TargetInterface
	}
	return cfg
}

func (h *Handler) clearHistory(w http.ResponseWriter, _ *http.Request) {
	if err := h.engine.ClearHistory(); err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

type actionRequest struct {
	ID  string   `json:"id"`
	IDs []string `json:"ids"`
}

// action returns per-ID raw results, including partial writer errors. A batch
// never turns one target's failure into an empty or generic success object.
func (h *Handler) action(run func(context.Context, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req actionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		ids := append([]string(nil), req.IDs...)
		if req.ID != "" {
			ids = append(ids, req.ID)
		}
		if len(ids) == 0 {
			WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "id or ids is required"})
			return
		}
		seen := map[string]bool{}
		results := map[string]any{}
		for _, id := range ids {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			if err := run(r.Context(), id); err != nil {
				results[id] = err.Error()
			} else {
				results[id] = nil
			}
		}
		WriteJSON(w, http.StatusOK, map[string]any{"results": results})
	}
}
