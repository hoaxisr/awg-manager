package api

import (
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// SingboxRouterRuleSetMatchRequest is the body for POST
// /singbox/router/rulesets/match.
type SingboxRouterRuleSetMatchRequest struct {
	Input string   `json:"input" example:"google.com"`
	Tags  []string `json:"tags"`
}

// SingboxRouterRuleSetMatchDTO describes one selected rule-set. Error is
// omitted for a successfully evaluated rule-set.
type SingboxRouterRuleSetMatchDTO struct {
	Tag       string `json:"tag" example:"geosite-google"`
	Matched   bool   `json:"matched" example:"true"`
	Supported bool   `json:"supported" example:"true"`
	Error     string `json:"error,omitempty" example:"rule-set is not defined"`
}

// SingboxRouterRuleSetMatchResponse is the envelope for POST
// /singbox/router/rulesets/match. Data preserves the requested tag order.
type SingboxRouterRuleSetMatchResponse struct {
	Success bool                           `json:"success" example:"true"`
	Data    []SingboxRouterRuleSetMatchDTO `json:"data"`
}

// MatchRuleSets matches a domain/IP against selected existing rule-sets.
//
//	@Summary		Match selected router rule-sets
//	@Description	Read-only match of a domain or IP against the selected rule-sets in the active routing slot. Uses the same materialization, remote cache and sing-box rule-set match path as the route inspector. Results preserve tag order; an unknown or unsupported rule-set is reported in its own result and does not fail the other tags.
//	@Tags			singbox-router
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		SingboxRouterRuleSetMatchRequest	true	"Domain or IP and rule-set tags to test"
//	@Success		200		{object}	SingboxRouterRuleSetMatchResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/router/rulesets/match [post]
func (h *SingboxRouterHandler) MatchRuleSets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req SingboxRouterRuleSetMatchRequest
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if strings.TrimSpace(req.Input) == "" {
		response.Error(w, "input обязателен", "MISSING_INPUT")
		return
	}
	if len(req.Tags) == 0 {
		response.Error(w, "tags обязательны", "MISSING_TAGS")
		return
	}
	results, err := h.svc.MatchRuleSets(r.Context(), req.Input, req.Tags)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, results)
}
