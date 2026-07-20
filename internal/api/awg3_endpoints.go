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
	"github.com/hoaxisr/awg-manager/internal/response"
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
	ID               string `json:"id"`
	Tag              string `json:"tag"`
	Host             string `json:"host"`
	HeaderProtection bool   `json:"headerProtection"`
}

// awg3Service is the narrow slice of *awg3endpoint.Service the handler needs.
// Kept as an interface so tests can inject a fake without a real orchestrator.
type awg3Service interface {
	Sync() error
	ListTags() []awg3endpoint.TagInfo
}

// RuleLister exposes just the router rules the rename-conflict check reads.
// NB: ListRules covers ordinary route rules only — it does NOT see composite
// selector members, route.final or DownloadDetour references. Renaming a tag
// still referenced by one of those is a conscious v1 limitation.
type RuleLister interface {
	ListRules(ctx context.Context) ([]router.Rule, error)
}

// Awg3Handler serves CRUD for imported AWG3 (sing-box awg endpoint) tunnels.
type Awg3Handler struct {
	store *awg3endpoint.Store
	svc   awg3Service
	rules RuleLister
	bus   *events.Bus
}

// NewAwg3Handler builds the handler. svc is *awg3endpoint.Service in production
// (it satisfies awg3Service); rules is the router service (satisfies RuleLister).
func NewAwg3Handler(store *awg3endpoint.Store, svc awg3Service, rules RuleLister) *Awg3Handler {
	return &Awg3Handler{store: store, svc: svc, rules: rules}
}

// SetEventBus wires the SSE bus for resource:invalidated hints.
func (h *Awg3Handler) SetEventBus(bus *events.Bus) { h.bus = bus }

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
func (h *Awg3Handler) handleList(w http.ResponseWriter, _ *http.Request) {
	response.Success(w, h.listDTO())
}

// handleImport — POST /api/awg3-endpoints  body {tag, config}
func (h *Awg3Handler) handleImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tag    string          `json:"tag"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "невалидный JSON запроса: "+err.Error())
		return
	}
	rec, err := awg3endpoint.Parse(req.Config, req.Tag, h.store.Tags())
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
		_ = h.store.Delete(rec.ID)
		response.BadRequest(w, "sing-box отверг конфиг: "+err.Error())
		return
	}
	publishInvalidated(h.bus, ResourceAwg3, "import")
	response.Success(w, h.listDTO())
}

// handleDelete — DELETE /api/awg3-endpoints/{id}
func (h *Awg3Handler) handleDelete(w http.ResponseWriter, _ *http.Request, id string) {
	rec, ok := h.store.Get(id)
	if !ok {
		response.BadRequest(w, "awg3 endpoint not found: "+id)
		return
	}
	if err := h.store.Delete(id); err != nil {
		response.InternalError(w, err.Error())
		return
	}
	// Deleting an endpoint rarely invalidates the config, but roll back
	// symmetrically with import if sing-box rejects the result.
	if err := h.svc.Sync(); err != nil {
		_ = h.store.Add(rec)
		response.InternalError(w, "sing-box отверг конфиг после удаления: "+err.Error())
		return
	}
	publishInvalidated(h.bus, ResourceAwg3, "delete")
	response.Success(w, h.listDTO())
}

// handleRename — PATCH /api/awg3-endpoints/{id}  body {tag}
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
		response.BadRequest(w, "awg3 endpoint not found: "+id)
		return
	}
	oldTag := rec.Tag

	// Store.Rename validates uniqueness but not charset — check both here.
	newTag := strings.TrimSpace(req.Tag)
	if newTag == "" || !awg3TagRe.MatchString(newTag) {
		response.BadRequest(w, awg3endpoint.ErrTag.Error())
		return
	}
	if newTag != oldTag && h.store.Tags()[newTag] {
		response.BadRequest(w, awg3endpoint.ErrTag.Error())
		return
	}

	// Block the rename while an ordinary routing rule still points at the old
	// tag — the reference would dangle (see RuleLister NB for the limits).
	if newTag != oldTag && h.tagReferenced(r.Context(), oldTag) {
		response.ErrorWithStatus(w, http.StatusConflict,
			"тег используется в правилах маршрутизации — сначала измените правило",
			"AWG3_TAG_IN_USE")
		return
	}

	if err := h.store.Rename(id, newTag); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	if err := h.svc.Sync(); err != nil {
		_ = h.store.Rename(id, oldTag)
		response.InternalError(w, "sing-box отверг конфиг после переименования: "+err.Error())
		return
	}
	publishInvalidated(h.bus, ResourceAwg3, "rename")
	response.Success(w, h.listDTO())
}

// tagReferenced reports whether any ordinary routing rule routes to tag.
func (h *Awg3Handler) tagReferenced(ctx context.Context, tag string) bool {
	if h.rules == nil {
		return false
	}
	rules, err := h.rules.ListRules(ctx)
	if err != nil {
		// Fail-closed: if we can't confirm the tag is free, refuse the rename.
		return true
	}
	for _, ru := range rules {
		if ru.Outbound == tag {
			return true
		}
	}
	return false
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
		ID:               rec.ID,
		Tag:              rec.Tag,
		Host:             host,
		HeaderProtection: strings.TrimSpace(shape.HeaderProtectionKey) != "",
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
