package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/engine"
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
	Recheck(context.Context, string) error
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
	mux.HandleFunc("POST /recheck", h.actionValue(func(ctx context.Context, id string) (any, error) {
		if err := eng.Recheck(ctx, id); err != nil {
			return nil, err
		}
		// The refreshed card goes back to the caller; nil means the card was
		// discarded during the rerun (both paths blocked) and the client
		// refetches /status.
		cand, ok := h.state.Snapshot(time.Now()).Candidates[id]
		if !ok {
			return nil, nil
		}
		return cand, nil
	}))
	mux.HandleFunc("POST /history/clear", h.clearHistory)
	mux.HandleFunc("POST /list", h.list)
	return LimitBody(mux)
}

// statusDocument is the operational status: daemon state, observation and
// counters only. The unbounded candidate/history tables are paged through
// POST /list so this document stays small (BACKLOG section 5).
type statusDocument struct {
	engine.Status
	Version string `json:"version"`
}

func (h *Handler) status(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, statusDocument{Status: h.engine.Status(), Version: h.version})
}

func (h *Handler) getConfig(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, h.config.Config())
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
	WriteJSON(w, http.StatusOK, cfg)
}

func (h *Handler) clearHistory(w http.ResponseWriter, _ *http.Request) {
	if err := h.engine.ClearHistory(); err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

// listRequest is the unified paged-list contract for both tables: kind
// selects the table, state/q filter it, cursor continues a previous page,
// limit defaults to and caps at 128.
type listRequest struct {
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Q      string `json:"q"`
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	var req listRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	switch req.State {
	case "", "active", "waiting", "checking", "added", "rejected":
	default:
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown state " + req.State})
		return
	}
	switch req.Kind {
	case "candidates":
		page, err := h.state.ListCandidates(req.State, req.Q, req.Cursor, req.Limit)
		if err != nil {
			WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		WriteJSON(w, http.StatusOK, page)
	case "history":
		page, err := h.state.ListHistory(req.Q, req.Cursor, req.Limit)
		if err != nil {
			WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		WriteJSON(w, http.StatusOK, page)
	default:
		WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "kind must be candidates or history"})
	}
}

// actionRequest identifies one or more candidates for a per-ID action.
type actionRequest struct {
	ID  string   `json:"id"`
	IDs []string `json:"ids"`
}

// action returns per-ID raw results, including partial writer errors. A batch
// never turns one target's failure into an empty or generic success object.
func (h *Handler) action(run func(context.Context, string) error) http.HandlerFunc {
	return h.actionValue(func(ctx context.Context, id string) (any, error) {
		return nil, run(ctx, id)
	})
}

// actionValue is action with a per-ID payload: nil error stores the value in
// results[id] (nil stays nil), a non-nil error stores its text. Used where
// the answer itself carries data, e.g. /recheck returns the updated card.
func (h *Handler) actionValue(run func(context.Context, string) (any, error)) http.HandlerFunc {
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
			value, err := run(r.Context(), id)
			if err != nil {
				results[id] = err.Error()
			} else {
				results[id] = value
			}
		}
		WriteJSON(w, http.StatusOK, map[string]any{"results": results})
	}
}
