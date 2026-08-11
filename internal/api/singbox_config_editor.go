package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"fmt"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// SingboxConfigEditorHandler — эксперт-редактор конфигурации sing-box.
// Читает любой слот config.d (read-only обзор), а ПИШЕТ только в
// пользовательский слот 90-user.json через draft-пайплайн оркестратора:
// PUT сохраняет черновик, check валидирует без записи, apply атомарно
// коммитит pending → active и взводит debounced reload. Ни один продюсер
// не пишет в user-слот — единственный владелец содержимого здесь.
type SingboxConfigEditorHandler struct {
	orch *orchestrator.Orchestrator
	log  *logging.ScopedLogger
}

// NewSingboxConfigEditorHandler constructs the handler.
// appLogger may be nil — the scoped logger is nil-safe.
func NewSingboxConfigEditorHandler(orch *orchestrator.Orchestrator, appLogger logging.AppLogger) *SingboxConfigEditorHandler {
	return &SingboxConfigEditorHandler{
		orch: orch,
		// Действия пользователя над конфигом — в главный app-журнал
		// (routing/singbox-router, как и остальной CRUD конфигурации);
		// singbox-бакет — для вывода самого процесса, там строки быстро
		// вытесняются форвардером.
		log: logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubSingboxRouter),
	}
}

// ConfigSlotInfo describes one config.d slot for the slots browser.
type ConfigSlotInfo struct {
	Slot     string `json:"slot"     example:"router"`
	Filename string `json:"filename" example:"20-router.json"`
	// Ownership: "system" — слот генерируется продюсером и перезаписывается
	// целиком; "user" — 90-user.json, пишется только редактором.
	Ownership string `json:"ownership" enums:"system,user"`
	Enabled   bool   `json:"enabled"`
	HasDraft  bool   `json:"hasDraft"`
	// Size/MTime описывают «эффективное» содержимое (pending → active →
	// disabled). Size 0 и пустой MTime — слот ещё не сконфигурирован.
	Size  int64  `json:"size"`
	MTime string `json:"mtime,omitempty" example:"2026-07-06T12:00:00Z"`
}

// ConfigSlotsResponse is the payload of GET /singbox/config/slots.
type ConfigSlotsResponse struct {
	Slots []ConfigSlotInfo `json:"slots"`
}

// ConfigSlotContentResponse is the payload of GET /singbox/config/slot.
type ConfigSlotContentResponse struct {
	Slot     string `json:"slot"`
	Filename string `json:"filename"`
	// Content — эффективное содержимое слота (pending-preferred), пустая
	// строка когда слот не сконфигурирован.
	Content string `json:"content"`
	// State: "active" | "disabled" | "absent" — где лежит применённый файл.
	State    string `json:"state" enums:"active,disabled,absent"`
	HasDraft bool   `json:"hasDraft"`
}

// UserConfigCheckResponse is the payload of POST /singbox/config/user/check.
// Errors mirror RouterStagingValidationError's validation entries; ошибка
// `sing-box check` приходит тем же списком (kind: "sing-box check").
// Warnings — advisory-замечания (severity=warning, например
// route-final-conflict): не блокируют применение, но молча терять их
// нельзя — first-wins merge тихо затенит пользовательский route.final.
// Форма RouterStagingValidationError (роутерный staging) не меняется —
// warnings добавлены только в ответы user-эндпоинтов.
type UserConfigCheckResponse struct {
	Ok       bool                       `json:"ok"`
	Errors   []RouterValidationErrorDTO `json:"errors,omitempty"`
	Warnings []RouterValidationErrorDTO `json:"warnings,omitempty"`
}

// UserConfigApplyResponse is the 200 payload of POST /singbox/config/user/apply.
// Warnings — как в UserConfigCheckResponse: применение прошло, но advisory-
// замечания нужно показать пользователю.
type UserConfigApplyResponse struct {
	Ok       bool                       `json:"ok"`
	Warnings []RouterValidationErrorDTO `json:"warnings,omitempty"`
}

// splitUserValidationDTO раскладывает результат валидации на блокирующие
// ошибки и advisory-предупреждения (SeverityWarning). validationDTOFrom
// для Ok()-результата возвращает nil — предупреждения без этого helper'а
// не доехали бы до DTO вовсе, а при провале смешивались бы с ошибками.
func splitUserValidationDTO(res orchestrator.ValidationResult) (errs, warns []RouterValidationErrorDTO) {
	for _, e := range res.Errors {
		dto := RouterValidationErrorDTO{
			Slot: string(e.Slot), Kind: e.Kind, Tag: e.Tag,
			InRule: e.InRule, Message: e.Message,
		}
		if e.Severity == orchestrator.SeverityWarning {
			warns = append(warns, dto)
		} else {
			errs = append(errs, dto)
		}
	}
	return errs, warns
}

// addressOrWarnings flags hand-authored rules that mix a rule_set with the
// rule's own destination addresses. sing-box ANDs the two unless the set holds
// exactly one destination-only rule, so such a rule matches almost nothing
// (issue #699). The slots we generate get this rewritten into logical(or)
// automatically; the expert slot is the user's own text, so we warn instead of
// rewriting it behind their back.
func addressOrWarnings(raw []byte) []RouterValidationErrorDTO {
	var out []RouterValidationErrorDTO
	for _, idx := range router.FlatAddressOrRuleIndexes(raw) {
		out = append(out, RouterValidationErrorDTO{
			Slot:   string(orchestrator.SlotUser),
			Kind:   "rule-set-with-own-addresses",
			InRule: fmt.Sprintf("route.rules[%d]", idx),
			Message: "правило требует совпадения И по rule_set, И по собственным domain/ip_cidr одновременно — " +
				"sing-box объединит их через ИЛИ только если в наборе ровно одно адресное правило. " +
				"Разнесите на два правила или запишите logical(or)",
		})
	}
	return out
}

// UserConfigEnableRequest is the body of POST /singbox/config/user/enable.
type UserConfigEnableRequest struct {
	Enabled bool `json:"enabled"`
}

// ListSlots returns the slots browser listing.
//
//	@Summary		List sing-box config.d slots
//	@Description	Возвращает все известные слоты config.d в порядке merge: имя, файл, владелец (system — генерируется автоматически, user — 90-user.json эксперт-редактора), включён ли слот, есть ли черновик и размер/время эффективного содержимого.
//	@Tags			singbox-config
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse{data=ConfigSlotsResponse}
//	@Failure		405	{object}	APIErrorEnvelope
//	@Router			/singbox/config/slots [get]
func (h *SingboxConfigEditorHandler) ListSlots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	out := ConfigSlotsResponse{Slots: []ConfigSlotInfo{}}
	for _, st := range h.orch.Snapshot() {
		info := ConfigSlotInfo{
			Slot:      string(st.Slot),
			Filename:  st.Filename,
			Ownership: slotOwnership(st.Slot),
			Enabled:   st.Enabled,
			HasDraft:  h.orch.HasDraft(st.Slot),
		}
		if size, mtime, ok := h.orch.EffectiveStat(st.Slot); ok {
			info.Size = size
			info.MTime = mtime.UTC().Format(time.RFC3339)
		}
		out.Slots = append(out.Slots, info)
	}
	response.Success(w, out)
}

// slotOwnership: всё "system", кроме пользовательского слота редактора.
func slotOwnership(slot orchestrator.Slot) string {
	if slot == orchestrator.SlotUser {
		return "user"
	}
	return "system"
}

// GetSlot returns the effective content of any slot (read-only browsing).
//
//	@Summary		Get sing-box config slot content
//	@Description	Эффективное содержимое слота (pending-preferred, затем active, затем disabled) — для read-only просмотра системных слотов и загрузки user-слота в редактор.
//	@Tags			singbox-config
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	query		string	true	"slot name (e.g. router, user)"
//	@Success		200		{object}	OkResponse{data=ConfigSlotContentResponse}
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope	"unknown slot"
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/config/slot [get]
func (h *SingboxConfigEditorHandler) GetSlot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		response.Error(w, "missing name parameter", "MISSING_NAME")
		return
	}
	slot := orchestrator.Slot(name)
	var meta *orchestrator.SlotMeta
	for _, m := range orchestrator.KnownSlots() {
		if m.Slot == slot {
			mm := m
			meta = &mm
			break
		}
	}
	if meta == nil {
		response.ErrorWithStatus(w, http.StatusNotFound, "unknown slot", "UNKNOWN_SLOT")
		return
	}
	data, err := h.orch.LoadEffective(slot)
	if err != nil {
		if errors.Is(err, orchestrator.ErrUnknownSlot) {
			response.ErrorWithStatus(w, http.StatusNotFound, "unknown slot", "UNKNOWN_SLOT")
			return
		}
		response.InternalError(w, err.Error())
		return
	}
	out := ConfigSlotContentResponse{
		Slot:     string(slot),
		Filename: meta.Filename,
		Content:  string(data),
		State:    "absent",
		HasDraft: h.orch.HasDraft(slot),
	}
	for _, st := range h.orch.Snapshot() {
		if st.Slot != slot {
			continue
		}
		if st.Present {
			if st.Enabled {
				out.State = "active"
			} else {
				out.State = "disabled"
			}
		}
		break
	}
	response.Success(w, out)
}

// readRawUserConfigBody читает тело запроса как сырой JSON слота (не DTO):
// кап 1 МБ, срез UTF-8 BOM. Возвращает (nil, true) при пустом теле —
// интерпретация пустоты за вызывающим (PUT → 400, check → «текущий черновик»).
func readRawUserConfigBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid body", "INVALID_BODY")
		return nil, false
	}
	raw = bytes.TrimPrefix(raw, utf8BOM)
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, true
	}
	return raw, true
}

// validateUserConfigJSON: синтаксически валидный JSON и top-level объект.
// Пишет 400 и возвращает false при нарушении.
func validateUserConfigJSON(w http.ResponseWriter, raw []byte) bool {
	if !json.Valid(raw) {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON", "INVALID_JSON")
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		response.ErrorWithStatus(w, http.StatusBadRequest, "top-level value must be a JSON object", "NOT_OBJECT")
		return false
	}
	return true
}

// PutUserConfig saves the user slot draft.
//
//	@Summary		Save user config slot draft
//	@Description	Тело запроса — сырой JSON слота целиком (не DTO, кап 1 МБ). Сервер проверяет синтаксис и что верхний уровень — объект, затем пишет черновик pending/90-user.json. Черновик инертен до /apply.
//	@Tags			singbox-config
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		object	true	"полный JSON слота"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope	"malformed JSON / not an object"
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/config/user [put]
func (h *SingboxConfigEditorHandler) PutUserConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	raw, ok := readRawUserConfigBody(w, r)
	if !ok {
		return
	}
	if raw == nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "empty body", "INVALID_JSON")
		return
	}
	if !validateUserConfigJSON(w, raw) {
		return
	}
	if err := h.orch.SaveDraft(orchestrator.SlotUser, raw); err != nil {
		h.log.Warn("user-config-save", "90-user.json", "save draft failed: "+err.Error())
		response.InternalError(w, err.Error())
		return
	}
	h.log.Info("user-config-save", "90-user.json", "user config draft saved")
	response.Success(w, OkData{Ok: true})
}

// CheckUserConfig validates without writing anything.
//
//	@Summary		Validate user config slot
//	@Description	Полный пайплайн валидации (кросс-слот + `sing-box check` над tmpdir-снапшотом «как если бы применили») БЕЗ записи. Тело опционально: непустое — проверяем его; пустое — проверяем текущий черновик; ни того ни другого — 409. Это запрос-вопрос, поэтому логический провал — 200 {ok:false}, а не 422.
//	@Tags			singbox-config
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		object	false	"полный JSON слота (опционально)"
//	@Success		200		{object}	OkResponse{data=UserConfigCheckResponse}
//	@Failure		400		{object}	APIErrorEnvelope	"malformed JSON / not an object"
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope	"no body and no draft"
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/config/user/check [post]
func (h *SingboxConfigEditorHandler) CheckUserConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	raw, ok := readRawUserConfigBody(w, r)
	if !ok {
		return
	}
	if raw == nil {
		draft, err := h.orch.LoadDraft(orchestrator.SlotUser)
		if err != nil {
			response.InternalError(w, err.Error())
			return
		}
		if draft == nil {
			response.ErrorWithStatus(w, http.StatusConflict, "no draft to check", "NO_DRAFT")
			return
		}
		raw = draft
	} else if !validateUserConfigJSON(w, raw) {
		return
	}
	res, err := h.orch.CheckMerged(orchestrator.SlotUser, raw)
	if err != nil {
		// Dry-run проверка, не применение — отдельная метка.
		h.log.Warn("user-config-check", "90-user.json", "merged check failed: "+err.Error())
		response.InternalError(w, err.Error())
		return
	}
	out := UserConfigCheckResponse{Ok: res.Ok()}
	out.Errors, out.Warnings = splitUserValidationDTO(res)
	out.Warnings = append(out.Warnings, addressOrWarnings(raw)...)
	response.Success(w, out)
}

// ApplyUserConfig commits the pending draft.
//
//	@Summary		Apply user config slot draft
//	@Description	Валидирует черновик (кросс-слот + sing-box check) и атомарно коммитит pending → active. Отключённый слот включается ПОСЛЕ успешного apply (провал валидации оставляет слот припаркованным, старый конфиг не воскресает). В 200-ответе могут быть advisory-предупреждения (warnings). Применение — debounced SIGHUP; добавление/удаление tun-inbound вызовет полный рестарт sing-box.
//	@Tags			singbox-config
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse{data=UserConfigApplyResponse}
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope	"no draft to apply"
//	@Failure		422	{object}	RouterStagingValidationError
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/config/user/apply [post]
func (h *SingboxConfigEditorHandler) ApplyUserConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	// Явный pre-check ради чистого 409 без побочных эффектов.
	if !h.orch.HasDraft(orchestrator.SlotUser) {
		response.ErrorWithStatus(w, http.StatusConflict, "no draft to apply", "NO_DRAFT")
		return
	}
	// Порядок — apply, ПОТОМ enable. ApplyDraft валидирует черновик «как
	// будто применён и включён» (цель попадает в снапшот sing-box check
	// безусловно), поэтому включать слот заранее не нужно; включение после
	// успеха безопасно и не оставляет побочек на провале: 422 оставляет
	// припаркованный слот выключенным, а его старый конфиг в disabled/ —
	// нетронутым (не воскресает). ApplyDraft у выключенного слота кладёт
	// файл в active/ — немедленный SetEnabled ниже (внутри debounce-окна
	// reload) убирает возможный устаревший дубль из disabled/ и выравнивает
	// enabled-карту с диском.
	// Черновик читаем ДО применения: после коммита он уже удалён, а advisory
	// по его содержимому нужно вернуть вместе с результатом.
	draft, _ := h.orch.LoadDraft(orchestrator.SlotUser)
	res, err := h.orch.ApplyDraft(orchestrator.SlotUser)
	if errors.Is(err, orchestrator.ErrNoDraft) {
		response.ErrorWithStatus(w, http.StatusConflict, "no draft to apply", "NO_DRAFT")
		return
	}
	if err != nil {
		h.log.Warn("user-config-apply", "90-user.json", "apply rejected by sing-box check: "+err.Error())
		writeJSONStatus(w, http.StatusUnprocessableEntity, RouterStagingValidationError{SbCheck: stripAnsiFromErr(err)})
		return
	}
	if !res.Ok() {
		h.log.Warn("user-config-apply", "90-user.json", "apply rejected: config validation failed")
		writeJSONStatus(w, http.StatusUnprocessableEntity, RouterStagingValidationError{Validation: validationDTOFrom(res)})
		return
	}
	if err := h.orch.SetEnabled(orchestrator.SlotUser, true); err != nil {
		h.log.Warn("user-config-apply", "90-user.json", "enable slot failed: "+err.Error())
		response.InternalError(w, err.Error())
		return
	}
	h.log.Info("user-config-apply", "90-user.json", "user config draft applied")
	out := UserConfigApplyResponse{Ok: true}
	_, out.Warnings = splitUserValidationDTO(res)
	out.Warnings = append(out.Warnings, addressOrWarnings(draft)...)
	response.Success(w, out)
}

// DiscardUserConfig removes the pending draft.
//
//	@Summary		Discard user config slot draft
//	@Description	Удаляет pending/90-user.json. Идемпотентно.
//	@Tags			singbox-config
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	OkResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/singbox/config/user/discard [post]
func (h *SingboxConfigEditorHandler) DiscardUserConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.orch.DiscardDraft(orchestrator.SlotUser); err != nil {
		h.log.Warn("user-config-discard", "90-user.json", "discard draft failed: "+err.Error())
		response.InternalError(w, err.Error())
		return
	}
	h.log.Info("user-config-discard", "90-user.json", "user config draft discarded")
	response.Success(w, OkData{Ok: true})
}

// EnableUserConfig toggles the user slot on/off without deleting content.
//
//	@Summary		Enable/disable user config slot
//	@Description	Переносит 90-user.json между config.d/ и config.d/disabled/ — слот можно «припарковать» целиком, не удаляя содержимое (вместо применения пустого {}).
//	@Tags			singbox-config
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		UserConfigEnableRequest	true	"target state"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/singbox/config/user/enable [post]
func (h *SingboxConfigEditorHandler) EnableUserConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req UserConfigEnableRequest
	if err := decodeBody(r, &req); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.orch.SetEnabled(orchestrator.SlotUser, req.Enabled); err != nil {
		h.log.Warn("user-config-enable", "90-user.json", "toggle slot failed: "+err.Error())
		response.InternalError(w, err.Error())
		return
	}
	state := "disabled"
	if req.Enabled {
		state = "enabled"
	}
	h.log.Info("user-config-enable", "90-user.json", "user config slot "+state)
	response.Success(w, OkData{Ok: true})
}
