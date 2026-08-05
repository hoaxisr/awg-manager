package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// validationDTOFrom converts orchestrator.ValidationResult to the DTO.
// Returns nil if the result is Ok.
func validationDTOFrom(res orchestrator.ValidationResult) *RouterValidationDTO {
	if res.Ok() {
		return nil
	}
	out := &RouterValidationDTO{Errors: make([]RouterValidationErrorDTO, 0, len(res.Errors))}
	for _, e := range res.Errors {
		out.Errors = append(out.Errors, RouterValidationErrorDTO{
			Slot: string(e.Slot), Kind: e.Kind, Tag: e.Tag,
			InRule: e.InRule, Message: e.Message,
		})
	}
	return out
}

// ansiCSIRegex strips ECMA-48 CSI escapes from sing-box check error output.
var ansiCSIRegex = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")

func stripAnsiFromErr(err error) string {
	return ansiCSIRegex.ReplaceAllString(err.Error(), "")
}

// writeJSONStatus writes v as JSON with the given HTTP status code.
func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// GetStaging returns the current draft state for the router slot.
//
//	@Summary		Get router staging status
//	@Description	Returns whether a pending draft exists for the router slot and, if so, its draft timestamp and a preview of the cross-slot validation result.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope{data=RouterStagingStatusResponse}
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/staging [get]
func (h *SingboxRouterHandler) GetStaging(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	st := h.svc.StagingStatus(r.Context())
	out := RouterStagingStatusResponse{HasDraft: st.HasDraft}
	if st.HasDraft {
		t := st.DraftedAt
		out.DraftedAt = &t
		if st.Validation != nil {
			out.Validation = validationDTOFrom(*st.Validation)
		}
	}
	response.Success(w, out)
}

// PostStagingApply commits the pending draft.
//
//	@Summary		Apply router staging draft
//	@Description	Validates the pending draft (cross-slot + sing-box check) then atomically swaps pending → active and arms a reload.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope	"no draft to apply"
//	@Failure		422	{object}	RouterStagingValidationError
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/staging/apply [post]
func (h *SingboxRouterHandler) PostStagingApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	res, err := h.svc.ApplyStaging(r.Context())
	if errors.Is(err, orchestrator.ErrNoDraft) {
		response.ErrorWithStatus(w, http.StatusConflict, "no draft to apply", "NO_DRAFT")
		return
	}
	if err != nil {
		h.log.Warn("staging-apply", "", "apply rejected by sing-box check: "+err.Error())
		writeJSONStatus(w, http.StatusUnprocessableEntity, RouterStagingValidationError{SbCheck: stripAnsiFromErr(err)})
		return
	}
	if !res.Ok() {
		h.log.Warn("staging-apply", "", "apply rejected: config validation failed")
		writeJSONStatus(w, http.StatusUnprocessableEntity, RouterStagingValidationError{Validation: validationDTOFrom(res)})
		return
	}
	h.log.Info("staging-apply", "", "staging draft applied")
	response.Success(w, OkData{Ok: true})
}

// PostStagingDiscard removes the pending draft.
//
//	@Summary		Discard router staging draft
//	@Description	Removes pending/21-routing.json. Idempotent.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/router/staging/discard [post]
func (h *SingboxRouterHandler) PostStagingDiscard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.DiscardStaging(r.Context()); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	h.log.Info("staging-discard", "", "staging draft discarded")
	response.Success(w, OkData{Ok: true})
}
