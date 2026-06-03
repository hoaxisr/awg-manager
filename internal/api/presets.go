package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/presets"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// PresetsHandler serves the unified preset catalog (read-only in U0).
type PresetsHandler struct {
	catalog *presets.Catalog
}

func NewPresetsHandler(catalog *presets.Catalog) *PresetsHandler {
	return &PresetsHandler{catalog: catalog}
}

// PresetsListResponse is the envelope payload for GET /presets.
type PresetsListResponse struct {
	Presets []presets.Preset `json:"presets"`
}

// List returns the merged preset catalog.
//
//	@Summary	List unified presets
//	@Tags		presets
//	@Produce	json
//	@Success	200	{object}	PresetsListResponse
//	@Router		/presets [get]
func (h *PresetsHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	list, err := h.catalog.List()
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, PresetsListResponse{Presets: list})
}
