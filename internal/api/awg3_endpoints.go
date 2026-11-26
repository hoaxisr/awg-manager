package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/awg3endpoint"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/singbox/awgoutbounds"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

const awg3BasePath = "/api/awg3-endpoints"

// awg3TagRe mirrors the tag charset validated by awg3endpoint.Parse. Rename
// bypasses Parse, so the PATCH handler re-checks it here (Store.Rename only
// enforces uniqueness, not format).
var awg3TagRe = regexp.MustCompile(`^[\p{L}\p{N} ._-]+$`)

// Awg3TunnelDTO is the outward projection of an AWG3 endpoint. It deliberately
// omits the raw private_key / header_protection_key material — that stays in
// the store file and the 16-awg3.json slot only, never over the API.
type Awg3TunnelDTO struct {
	ID               string `json:"id" example:"awg3-Ab12Cd34Ef"`
	Tag              string `json:"tag" example:"amsterdam"`
	Host             string `json:"host" example:"vpn.example.com:51820"`
	HeaderProtection bool   `json:"headerProtection" example:"true"`
	// AWG3 device timers — read-only passthrough from the imported config
	// (edited only in RouteBox). Strings as RouteBox emits them (may be ranges
	// like "120-150"); omitted when the config carries no such field.
	RekeyTimeout         string `json:"rekeyTimeout,omitempty" example:"5"`
	RekeyAfterTime       string `json:"rekeyAfterTime,omitempty" example:"120-150"`
	RejectAfterTime      string `json:"rejectAfterTime,omitempty" example:"180"`
	KeepaliveTimeout     string `json:"keepaliveTimeout,omitempty" example:"25"`
	MaxHandshakeAttempts string `json:"maxHandshakeAttempts,omitempty" example:"5"`
	// AWG 3.1 device flags, read-only like the timers above. RandomTrailers is
	// not negotiated on the wire and needs 3.1 on both ends.
	RandomTrailers bool `json:"randomTrailers,omitempty" example:"true"`
	DisableCookies bool `json:"disableCookies,omitempty" example:"true"`
}

// Awg3ImportRequest is the POST /awg3-endpoints body: a human-readable tag and
// the raw endpoint config (RouteBox envelope or a bare sing-box awg endpoint).
type Awg3ImportRequest struct {
	Tag    string          `json:"tag" example:"amsterdam"`
	Config json.RawMessage `json:"config" swaggertype:"object"`
}

// Awg3RenameRequest is the PATCH /awg3-endpoints/{id} body.
type Awg3RenameRequest struct {
	Tag string `json:"tag" example:"berlin"`
}

// Awg3ListResponse is the envelope returned by every awg3 endpoint operation
// (list/import/delete/rename all return the fresh list).
type Awg3ListResponse struct {
	Success bool            `json:"success" example:"true"`
	Data    []Awg3TunnelDTO `json:"data"`
}

// awg3Service is the narrow slice of *awg3endpoint.Service the handler needs.
// Kept as an interface so tests can inject a fake without a real orchestrator.
type awg3Service interface {
	Sync() error
}

// RuleLister exposes just the router rules the rename-conflict check reads.
// NB: ListRules covers ordinary route rules only — composite selector members,
// route.final, DownloadDetour and fakeip references are seen by OutboundRefLister
// below (the same router service implements both).
type RuleLister interface {
	ListRules(ctx context.Context) ([]router.Rule, error)
}

// OutboundRefLister перечисляет ВСЕ места конфига, ссылающиеся на тег: члены
// композитов, route.final, dns detour, download_detour и fakeip-слот. Без него
// удаление/переименование доезжало до Sync и возвращало непрозрачный 500
// «sing-box отверг конфиг» вместо внятного 409 с указанием места.
type OutboundRefLister interface {
	OutboundReferenceLocations(tag string) []string
}

// OutboundTagLister exposes every outbound/endpoint tag sing-box already knows
// (managed AWG, subscriptions, composites, plus the awg3 tags themselves). The
// handler folds these into the collision set so an import or rename that clashes
// with a non-awg3 outbound fails early with a clear message, instead of a vague
// "sing-box отверг конфиг" 400 from SaveAndValidate. Optional (nil = skip the
// early check; SaveAndValidate remains the backstop).
type OutboundTagLister interface {
	ListTags(ctx context.Context) ([]awgoutbounds.TagInfo, error)
}

// Awg3Handler serves CRUD for imported AWG3 (sing-box awg endpoint) tunnels.
type Awg3Handler struct {
	store     *awg3endpoint.Store
	svc       awg3Service
	rules     RuleLister
	outbounds OutboundTagLister // optional; nil = skip the early tag-collision check
	bus       *events.Bus
	log       *logging.ScopedLogger
}

// NewAwg3Handler builds the handler. svc is *awg3endpoint.Service in production
// (it satisfies awg3Service); rules is the router service (satisfies RuleLister).
func NewAwg3Handler(store *awg3endpoint.Store, svc awg3Service, rules RuleLister, appLogger logging.AppLogger) *Awg3Handler {
	return &Awg3Handler{
		store: store,
		svc:   svc,
		rules: rules,
		log:   logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubAwg3),
	}
}

// SetEventBus wires the SSE bus for resource:invalidated hints.
func (h *Awg3Handler) SetEventBus(bus *events.Bus) { h.bus = bus }

// SetOutboundTagLister wires the outbound-tag source for the early
// collision check (see OutboundTagLister). Optional; nil-safe.
func (h *Awg3Handler) SetOutboundTagLister(l OutboundTagLister) { h.outbounds = l }

// Handle dispatches by method + path. It is registered on both
// "/api/awg3-endpoints" (list/import) and "/api/awg3-endpoints/" (item ops).
func (h *Awg3Handler) Handle(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, awg3BasePath)
	id = strings.Trim(id, "/")

	if id == "" {
		switch r.Method {
		case http.MethodGet:
			h.handleList(w, r)
		case http.MethodPost:
			h.handleImport(w, r)
		default:
			response.MethodNotAllowed(w)
		}
		return
	}

	switch r.Method {
	case http.MethodDelete:
		h.handleDelete(w, r, id)
	case http.MethodPatch:
		h.handleRename(w, r, id)
	default:
		response.MethodNotAllowed(w)
	}
}

// handleList — GET /api/awg3-endpoints
//
//	@Summary		List AWG3 endpoints
//	@Tags			awg3-endpoints
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	Awg3ListResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/awg3-endpoints [get]
func (h *Awg3Handler) handleList(w http.ResponseWriter, _ *http.Request) {
	response.Success(w, h.listDTO())
}

// handleImport — POST /api/awg3-endpoints  body {tag, config}
//
//	@Summary		Import an AWG3 endpoint
//	@Tags			awg3-endpoints
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			request	body		Awg3ImportRequest	true	"Tag and endpoint config"
//	@Success		200		{object}	Awg3ListResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/awg3-endpoints [post]
func (h *Awg3Handler) handleImport(w http.ResponseWriter, r *http.Request) {
	var req Awg3ImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "невалидный JSON запроса: "+err.Error())
		return
	}
	cfg := req.Config
	if len(cfg) > 0 && cfg[0] == '"' {
		// config пришёл JSON-строкой → это текст нативного .conf.
		var text string
		if err := json.Unmarshal(cfg, &text); err != nil {
			response.BadRequest(w, "невалидная .conf-строка: "+err.Error())
			return
		}
		converted, err := awg3endpoint.ParseConf(text)
		if err != nil {
			response.BadRequest(w, "не удалось разобрать .conf: "+err.Error())
			return
		}
		cfg = converted
	}
	rec, err := awg3endpoint.Parse(cfg, req.Tag, h.takenTags(r.Context()))
	if err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	rec.ID = newAwg3ID()
	if err := h.store.Add(rec); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	// Sync projects the store into 16-awg3.json via SaveAndValidate (sing-box
	// check gate). On rejection roll the just-added record back — fail-closed.
	if err := h.svc.Sync(); err != nil {
		if delErr := h.store.Delete(rec.ID); delErr != nil {
			h.log.Error("import-rollback", rec.Tag, delErr.Error())
		}
		response.BadRequest(w, "sing-box отверг конфиг: "+err.Error())
		return
	}
	h.bus.PublishInvalidated(events.ResourceAwg3, "import")
	response.Success(w, h.listDTO())
}

// handleDelete — DELETE /api/awg3-endpoints/{id}
//
//	@Summary		Delete an AWG3 endpoint
//	@Tags			awg3-endpoints
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	path		string	true	"Endpoint id"
//	@Success		200	{object}	Awg3ListResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		409	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/awg3-endpoints/{id} [delete]
func (h *Awg3Handler) handleDelete(w http.ResponseWriter, r *http.Request, id string) {
	rec, ok := h.store.Get(id)
	if !ok {
		response.ErrorWithStatus(w, http.StatusNotFound, "awg3 endpoint not found: "+id, "AWG3_NOT_FOUND")
		return
	}
	// Block the delete while an ordinary routing rule still points at the tag —
	// otherwise Sync would fail its cross-slot check with an unhelpful 500.
	referenced, err := h.tagReferenced(r.Context(), rec.Tag)
	if err != nil {
		response.InternalError(w, "не удалось проверить ссылки на тег: "+err.Error())
		return
	}
	if referenced {
		response.ErrorWithStatus(w, http.StatusConflict,
			"тег используется в конфигурации sing-box (правило, композит, route.final или DNS) — сначала уберите ссылку",
			"AWG3_TAG_IN_USE")
		return
	}
	if err := h.store.Delete(id); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	// Deleting an endpoint rarely invalidates the config, but roll back
	// symmetrically with import if sing-box rejects the result.
	if err := h.svc.Sync(); err != nil {
		if addErr := h.store.Add(rec); addErr != nil {
			h.log.Error("delete-rollback", rec.Tag, addErr.Error())
		}
		response.InternalError(w, "sing-box отверг конфиг после удаления: "+err.Error())
		return
	}
	h.bus.PublishInvalidated(events.ResourceAwg3, "delete")
	response.Success(w, h.listDTO())
}

// handleRename — PATCH /api/awg3-endpoints/{id}  body {tag}
//
//	@Summary		Rename an AWG3 endpoint
//	@Tags			awg3-endpoints
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		path		string				true	"Endpoint id"
//	@Param			request	body		Awg3RenameRequest	true	"New tag"
//	@Success		200		{object}	Awg3ListResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/awg3-endpoints/{id} [patch]
func (h *Awg3Handler) handleRename(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Tag string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "невалидный JSON запроса: "+err.Error())
		return
	}
	rec, ok := h.store.Get(id)
	if !ok {
		response.ErrorWithStatus(w, http.StatusNotFound, "awg3 endpoint not found: "+id, "AWG3_NOT_FOUND")
		return
	}
	oldTag := rec.Tag

	// Store.Rename validates uniqueness but not charset — check both here.
	newTag := strings.TrimSpace(req.Tag)
	if newTag == "" || !awg3TagRe.MatchString(newTag) {
		response.BadRequest(w, awg3endpoint.ErrTag.Error())
		return
	}
	// Collision against every known tag (store + foreign outbounds), so a rename
	// onto a subscription / 15-awg / composite tag fails early and clearly. The
	// record's own tag is excluded by the newTag != oldTag guard.
	if newTag != oldTag && h.takenTags(r.Context())[newTag] {
		response.BadRequest(w, awg3endpoint.ErrTag.Error())
		return
	}

	// Block the rename while an ordinary routing rule still points at the old
	// tag — the reference would dangle (see RuleLister NB for the limits).
	if newTag != oldTag {
		referenced, err := h.tagReferenced(r.Context(), oldTag)
		if err != nil {
			response.InternalError(w, "не удалось проверить ссылки на тег: "+err.Error())
			return
		}
		if referenced {
			response.ErrorWithStatus(w, http.StatusConflict,
				"тег используется в конфигурации sing-box (правило, композит, route.final или DNS) — сначала уберите ссылку",
				"AWG3_TAG_IN_USE")
			return
		}
	}

	if err := h.store.Rename(id, newTag); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.Sync(); err != nil {
		if renErr := h.store.Rename(id, oldTag); renErr != nil {
			h.log.Error("rename-rollback", oldTag, renErr.Error())
		}
		response.InternalError(w, "sing-box отверг конфиг после переименования: "+err.Error())
		return
	}
	h.bus.PublishInvalidated(events.ResourceAwg3, "rename")
	response.Success(w, h.listDTO())
}

// takenTags is the collision set for import/rename: store tags plus every
// outbound tag sing-box already knows. The set INCLUDES the record's own tag
// (store.Tags() contains it), so rename must guard self with newTag != oldTag
// at the call-site before consulting this map. Best-effort: a ListTags failure
// degrades to store-only, with SaveAndValidate still the fail-closed backstop.
func (h *Awg3Handler) takenTags(ctx context.Context) map[string]bool {
	taken := h.store.Tags()
	if h.outbounds == nil {
		return taken
	}
	tags, err := h.outbounds.ListTags(ctx)
	if err != nil {
		h.log.Warn("tag-collision", "", "не удалось перечислить outbound-теги: "+err.Error())
		return taken
	}
	for _, t := range tags {
		taken[t.Tag] = true
	}
	return taken
}

// tagReferenced reports whether any ordinary routing rule routes to tag, ИЛИ
// тег упомянут в другом месте конфига (композит, route.final, dns detour,
// download_detour, fakeip) — их видит OutboundRefLister. A ListRules failure is
// surfaced as an error (not a false 409) so a transient router fault stays
// debuggable.
func (h *Awg3Handler) tagReferenced(ctx context.Context, tag string) (bool, error) {
	if h.rules == nil {
		return false, nil
	}
	if lister, ok := h.rules.(OutboundRefLister); ok {
		if locs := lister.OutboundReferenceLocations(tag); len(locs) > 0 {
			return true, nil
		}
	}
	rules, err := h.rules.ListRules(ctx)
	if err != nil {
		return false, err
	}
	for _, ru := range rules {
		if ru.Outbound == tag {
			return true, nil
		}
	}
	return false, nil
}

// listDTO projects the store into the outward, key-free DTO list.
func (h *Awg3Handler) listDTO() []Awg3TunnelDTO {
	list, _ := h.store.List()
	out := make([]Awg3TunnelDTO, 0, len(list))
	for _, rec := range list {
		out = append(out, awg3RecordToDTO(rec))
	}
	return out
}

// awg3RecordToDTO derives the safe projection: host from peers[0].address:port,
// headerProtection from a non-empty header_protection_key.
func awg3RecordToDTO(rec awg3endpoint.Record) Awg3TunnelDTO {
	var shape struct {
		HeaderProtectionKey string `json:"header_protection_key"`
		Peers               []struct {
			Address string `json:"address"`
			Port    int    `json:"port"`
		} `json:"peers"`
		RekeyTimeout         string `json:"rekey_timeout"`
		RekeyAfterTime       string `json:"rekey_after_time"`
		RejectAfterTime      string `json:"reject_after_time"`
		KeepaliveTimeout     string `json:"keepalive_timeout"`
		MaxHandshakeAttempts string `json:"max_handshake_attempts"`
		RandomTrailers       bool   `json:"random_trailers"`
		DisableCookies       bool   `json:"disable_cookies"`
	}
	_ = json.Unmarshal(rec.Endpoint, &shape)

	host := ""
	if len(shape.Peers) > 0 {
		host = shape.Peers[0].Address
		if shape.Peers[0].Port > 0 {
			host = fmt.Sprintf("%s:%d", host, shape.Peers[0].Port)
		}
	}
	return Awg3TunnelDTO{
		ID:                   rec.ID,
		Tag:                  rec.Tag,
		Host:                 host,
		HeaderProtection:     strings.TrimSpace(shape.HeaderProtectionKey) != "",
		RekeyTimeout:         shape.RekeyTimeout,
		RekeyAfterTime:       shape.RekeyAfterTime,
		RejectAfterTime:      shape.RejectAfterTime,
		KeepaliveTimeout:     shape.KeepaliveTimeout,
		MaxHandshakeAttempts: shape.MaxHandshakeAttempts,
		RandomTrailers:       shape.RandomTrailers,
		DisableCookies:       shape.DisableCookies,
	}
}

// newAwg3ID returns "awg3-" + 10 alphanumeric chars. No shared uuid helper
// exists in the project; clientroute/staticroute each roll their own.
func newAwg3ID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "awg3-" + string(b)
}
