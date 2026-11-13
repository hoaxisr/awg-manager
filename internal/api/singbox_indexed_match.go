package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

const (
	indexedMatchMaxInput = 4096
	indexedMatchMaxTags  = 128
	indexedMatchMaxTag   = 256
)

// indexedCoverageService is intentionally not added to router.Service: the
// existing interface and all Inspector/mock implementations stay unchanged.
type indexedCoverageService interface {
	MatchRuleSetsIndexed(context.Context, string, []string) (router.IndexedRuleSetMatch, error)
	CoverageIndexSnapshot(context.Context, []string) (router.CoverageSnapshot, error)
}

// SingboxRouterIndexedMatchResponse is the additive indexed-match envelope.
type SingboxRouterIndexedMatchResponse struct {
	Success bool                       `json:"success" example:"true"`
	Data    router.IndexedRuleSetMatch `json:"data"`
}

// MatchRuleSetsIndexed matches against the immutable applied active-slot
// generation. It never falls back to the existing CLI endpoint or to no-match.
func (h *SingboxRouterHandler) MatchRuleSetsIndexed(w http.ResponseWriter, r *http.Request) {
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
	if len(req.Input) > indexedMatchMaxInput {
		response.Error(w, "input слишком длинный", "INPUT_TOO_LONG")
		return
	}
	if len(req.Tags) == 0 {
		response.Error(w, "tags обязательны", "MISSING_TAGS")
		return
	}
	if !validIndexedTags(req.Tags) {
		returnIndexedTagError(w)
		return
	}
	svc, ok := h.svc.(indexedCoverageService)
	if !ok {
		response.InternalError(w, "indexed coverage service is unavailable")
		return
	}
	results, err := svc.MatchRuleSetsIndexed(r.Context(), req.Input, req.Tags)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, results)
}

// CoverageIndexSnapshot returns the applied active slot and installs the
// complete current Autolist exclusion working set. An empty tags array is a
// valid request and removes exclusions from the next generation.
func (h *SingboxRouterHandler) CoverageIndexSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req struct {
		Tags []string `json:"tags"`
	}
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if !validIndexedTags(req.Tags) {
		returnIndexedTagError(w)
		return
	}
	svc, ok := h.svc.(indexedCoverageService)
	if !ok {
		response.InternalError(w, "indexed coverage service is unavailable")
		return
	}
	snapshot, err := svc.CoverageIndexSnapshot(r.Context(), req.Tags)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	response.Success(w, snapshot)
}

func validIndexedTags(tags []string) bool {
	if len(tags) > indexedMatchMaxTags {
		return false
	}
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if tag == "" || strings.TrimSpace(tag) != tag || len(tag) > indexedMatchMaxTag {
			return false
		}
		if _, duplicate := seen[tag]; duplicate {
			return false
		}
		seen[tag] = struct{}{}
	}
	return true
}

func returnIndexedTagError(w http.ResponseWriter) {
	response.Error(w, "tags должны быть непустыми уникальными строками, максимум 128 по 256 байт", "INVALID_TAGS")
}
