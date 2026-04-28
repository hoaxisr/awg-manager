// internal/api/awg_outbounds.go
package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/singbox/awgoutbounds"
)

// AWGOutboundsService is the narrow contract this handler needs.
// Implemented by awgoutbounds.Service.
type AWGOutboundsService interface {
	ListTags(ctx context.Context) ([]awgoutbounds.TagInfo, error)
}

// AWGOutboundsHandler exposes the catalog of AWG-direct outbound tags
// for the frontend (singbox-router rule editor outbound dropdown).
type AWGOutboundsHandler struct {
	svc AWGOutboundsService
}

func NewAWGOutboundsHandler(svc AWGOutboundsService) *AWGOutboundsHandler {
	return &AWGOutboundsHandler{svc: svc}
}

// ServeHTTP returns the catalog of AWG-direct outbound tags.
//
//	@Summary		List AWG outbound tags
//	@Description	Returns the catalog of AWG-direct outbound tags currently exposed to sing-box (one per managed/system AWG tunnel). Used by the singbox-router rule editor outbound dropdown.
//	@Tags			singbox
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{array}		map[string]interface{}
//	@Failure		405	{string}	string
//	@Failure		500	{string}	string
//	@Router			/singbox/awg-outbounds/tags [get]
func (h *AWGOutboundsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tags, err := h.svc.ListTags(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if tags == nil {
		tags = []awgoutbounds.TagInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tags)
}
