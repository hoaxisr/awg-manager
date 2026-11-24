package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// ── Response DTOs ────────────────────────────────────────────────

// GeoFileEntryDTO mirrors frontend GeoFileEntry.
type GeoFileEntryDTO struct {
	Type     string `json:"type" example:"geosite"`
	Path     string `json:"path" example:"/opt/etc/awg-manager/geo/geosite.dat"`
	URL      string `json:"url" example:"https://cdn.example.com/geosite.db"`
	Size     int64  `json:"size" example:"3145728"`
	TagCount int    `json:"tagCount" example:"420"`
	Updated  string `json:"updated" example:"2024-01-15T02:00:00Z"`
	External bool   `json:"external,omitempty" example:"true"`
}

// GeoFilesResponse is the envelope for GET /hydraroute/geo-files.
type GeoFilesResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    []GeoFileEntryDTO `json:"data"`
}

// GeoTagDTO mirrors frontend GeoTag.
type GeoTagDTO struct {
	Name  string `json:"name" example:"google"`
	Count int    `json:"count" example:"1250"`
}

// GeoTagsResponse is the envelope for GET /hydraroute/geo-tags.
type GeoTagsResponse struct {
	Success bool        `json:"success" example:"true"`
	Data    []GeoTagDTO `json:"data"`
}

// GeoExpandData is the payload for GET /hydraroute/geo-expand.
type GeoExpandData struct {
	Lines []string `json:"lines"`
	Path  string   `json:"path"`
	Count int      `json:"count"`
}

// GeoFileResponse is the envelope for endpoints that return a single
// geo file entry (POST /hydraroute/geo-files/add).
type GeoFileResponse struct {
	Success bool            `json:"success" example:"true"`
	Data    GeoFileEntryDTO `json:"data"`
}

// GeoFileUpdatedData reports how many geo files were re-downloaded by
// POST /hydraroute/geo-files/update. The shape is the same whether the
// caller targeted one path or all paths.
type GeoFileUpdatedData struct {
	Updated int    `json:"updated" example:"1"`
	Partial bool   `json:"partial,omitempty" example:"true"`
	Error   string `json:"error,omitempty"`
}

// GeoFileUpdatedResponse is the envelope for POST /hydraroute/geo-files/update.
type GeoFileUpdatedResponse struct {
	Success bool               `json:"success" example:"true"`
	Data    GeoFileUpdatedData `json:"data"`
}

// ── Request DTOs ─────────────────────────────────────────────────

// AddGeoFileRequest is the body for POST /hydraroute/geo-files/add.
type AddGeoFileRequest struct {
	Type  string            `json:"type" example:"geosite"`
	URL   string            `json:"url" example:"https://cdn.example.com/geosite.db"`
	Route *DownloadRouteDTO `json:"route,omitempty"`
}

// UpdateGeoFileRequest is the body for POST /hydraroute/geo-files/update.
// Empty path triggers a bulk refresh of every tracked geo file.
type UpdateGeoFileRequest struct {
	Path  string            `json:"path" example:"/opt/etc/awg-manager/geo/geosite.dat"`
	Route *DownloadRouteDTO `json:"route,omitempty"`
}

// GeoDataHandler handles geo data file endpoints.
type GeoDataHandler struct {
	store       *hydraroute.GeoDataStore
	downloadSvc *downloader.Service
}

// NewGeoDataHandler creates a new geo data handler.
func NewGeoDataHandler(store *hydraroute.GeoDataStore, downloadSvc *downloader.Service) *GeoDataHandler {
	if downloadSvc == nil {
		downloadSvc = downloader.NewService(downloader.Deps{})
	}
	return &GeoDataHandler{store: store, downloadSvc: downloadSvc}
}

func toDownloaderRoute(route *DownloadRouteDTO) *downloader.Route {
	if route == nil {
		return nil
	}
	return &downloader.Route{
		Tag:  route.Tag,
		Kind: route.Kind,
	}
}

// ListGeoFiles returns all tracked geo data files.
//
//	@Summary		List geo files
//	@Tags			hydraroute
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	GeoFilesResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/hydraroute/geo-files [get]
func (h *GeoDataHandler) ListGeoFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	response.Success(w, response.MustNotNil(h.store.List()))
}

// AddGeoFile downloads and registers a new geo data file.
//
//	@Summary		Add geo file
//	@Tags			hydraroute
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		AddGeoFileRequest	true	"Geo file source descriptor"
//	@Success		200		{object}	GeoFileResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/hydraroute/geo-files/add [post]
func (h *GeoDataHandler) AddGeoFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req AddGeoFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body: "+err.Error(), "BAD_REQUEST")
		return
	}

	lease, err := h.downloadSvc.ResolveClient(r.Context(), toDownloaderRoute(req.Route))
	if err != nil {
		response.Error(w, err.Error(), "GEO_DOWNLOAD_ROUTE_ERROR")
		return
	}
	defer lease.Close()

	routeLabel := lease.Route.DisplayName()

	entry, err := h.store.DownloadWithClientVia(r.Context(), req.Type, req.URL, lease.Client, routeLabel)
	if err != nil {
		response.Error(w, fmt.Sprintf("download via %s: %v", routeLabel, err), "GEO_DOWNLOAD_ERROR")
		return
	}

	response.Success(w, entry)
}

// DeleteGeoFile removes a tracked geo data file.
//
//	@Summary		Delete geo file
//	@Description	Removes the tracked geo data file at the given path. A file of another program (external) stays on disk: only the catalog entry is removed.
//	@Tags			hydraroute
//	@Produce		json
//	@Security		CookieAuth
//	@Param			path	query		string	true	"Filesystem path of the geo file"
//	@Success		200		{object}	OkResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/hydraroute/geo-files/delete [delete]
func (h *GeoDataHandler) DeleteGeoFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		response.Error(w, "path query parameter is required", "BAD_REQUEST")
		return
	}

	if err := h.store.Delete(path); err != nil {
		response.Error(w, err.Error(), "GEO_DELETE_ERROR")
		return
	}

	response.Success(w, map[string]bool{"ok": true})
}

// UpdateGeoFile re-downloads a geo data file (or all files if path is empty).
//
//	@Summary		Refresh geo file(s)
//	@Description	Empty path triggers a bulk refresh of every tracked geo file. Single path refreshes one file. Both branches return an updated count for caller-side feedback; the frontend refetches the list afterwards.
//	@Tags			hydraroute
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		UpdateGeoFileRequest	true	"Path to refresh, or empty to refresh all"
//	@Success		200		{object}	GeoFileUpdatedResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/hydraroute/geo-files/update [post]
func (h *GeoDataHandler) UpdateGeoFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req UpdateGeoFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body: "+err.Error(), "BAD_REQUEST")
		return
	}

	lease, err := h.downloadSvc.ResolveClient(r.Context(), toDownloaderRoute(req.Route))
	if err != nil {
		response.Error(w, err.Error(), "GEO_DOWNLOAD_ROUTE_ERROR")
		return
	}
	defer lease.Close()

	routeLabel := lease.Route.DisplayName()

	out := GeoFileUpdatedData{}
	if req.Path == "" {
		count, err := h.store.UpdateAllWithClientVia(r.Context(), lease.Client, routeLabel)
		out.Updated = count
		if err != nil {
			out.Partial = count > 0
			out.Error = fmt.Sprintf("update via %s: %v", routeLabel, err)
			if count == 0 {
				response.Error(w, fmt.Sprintf("update via %s: %v", routeLabel, err), "GEO_UPDATE_ERROR")
				return
			}
		}
	} else {
		if _, err := h.store.UpdateWithClientVia(r.Context(), req.Path, lease.Client, routeLabel); err != nil {
			response.Error(w, fmt.Sprintf("update via %s: %v", routeLabel, err), "GEO_UPDATE_ERROR")
			return
		}
		out.Updated = 1
	}

	response.Success(w, out)
}

// GetGeoTags returns the tag list for a specific geo data file.
//
//	@Summary		Geo tags
//	@Tags			hydraroute
//	@Produce		json
//	@Security		CookieAuth
//	@Param			path	query	string	true	"Geo file path"
//	@Success		200	{object}	GeoTagsResponse
//	@Router			/hydraroute/geo-tags [get]
func (h *GeoDataHandler) GetGeoTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		response.Error(w, "path query parameter is required", "BAD_REQUEST")
		return
	}

	tags, err := h.store.GetTags(path)
	if err != nil {
		response.Error(w, err.Error(), "GEO_TAGS_ERROR")
		return
	}

	response.Success(w, response.MustNotNil(tags))
}

// ExpandGeoTag expands a geosite:/geoip: tag into inline rule list lines.
//
//	@Summary		Geo tag expand
//	@Tags			hydraroute
//	@Produce		json
//	@Security		CookieAuth
//	@Param			kind	query	string	true	"geosite or geoip"
//	@Param			tag	query	string	true	"Tag name"
//	@Success		200	{object}	APIEnvelope{data=GeoExpandData}
//	@Router			/hydraroute/geo-expand [get]
func (h *GeoDataHandler) ExpandGeoTag(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	kind := r.URL.Query().Get("kind")
	tag := r.URL.Query().Get("tag")
	if kind == "" || tag == "" {
		response.Error(w, "kind and tag query parameters are required", "BAD_REQUEST")
		return
	}

	lines, path, err := h.store.ExpandGeoTag(kind, tag)
	if err != nil {
		response.Error(w, err.Error(), "GEO_EXPAND_ERROR")
		return
	}

	response.Success(w, GeoExpandData{
		Lines: lines,
		Path:  path,
		Count: len(lines),
	})
}
