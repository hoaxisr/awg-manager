package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/routerclock"
)

// ManagedServerBackupHandler exposes Export / Import / Drift / RestoreDrift.
type ManagedServerBackupHandler struct {
	svc *managed.Service
	bus *events.Bus
}

// SetEventBus wires the SSE bus so Import and RestoreDrift can publish
// resource:invalidated after a successful restore.
func (h *ManagedServerBackupHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// NewManagedServerBackupHandler creates a new backup handler.
func NewManagedServerBackupHandler(svc *managed.Service) *ManagedServerBackupHandler {
	return &ManagedServerBackupHandler{svc: svc}
}

// ManagedServerBackupDTO mirrors storage.ManagedServer for backup/restore
// transport. Exists in api/ (rather than reusing managed.ManagedServerExport
// directly) so swag can resolve it without crossing package boundaries —
// matches the existing ManagedServerDTO pattern but includes the secret
// fields (PrivateKey, I1, I2) needed for full restore.
type ManagedServerBackupDTO struct {
	InterfaceName string           `json:"interfaceName" example:"Wireguard1"`
	Description   string           `json:"description,omitempty" example:"My VPN"`
	Address       string           `json:"address" example:"10.10.0.1"`
	Mask          string           `json:"mask" example:"255.255.255.0"`
	ListenPort    int              `json:"listenPort" example:"51821"`
	Endpoint      string           `json:"endpoint,omitempty" example:"vpn.example.com:51821"`
	DNS           string           `json:"dns,omitempty" example:"8.8.8.8"`
	MTU           int              `json:"mtu,omitempty" example:"1420"`
	NATEnabled    bool             `json:"natEnabled,omitempty" example:"true"`
	PrivateKey    string           `json:"privateKey,omitempty" example:"oA..."`
	Policy        string           `json:"policy" example:"none"`
	Peers         []ManagedPeerDTO `json:"peers"`
	I1            string           `json:"i1,omitempty"`
	I2            string           `json:"i2,omitempty"`
	I3            string           `json:"i3,omitempty"`
	I4            string           `json:"i4,omitempty"`
	I5            string           `json:"i5,omitempty"`
	ASC           json.RawMessage  `json:"asc,omitempty" swaggertype:"object"`
}

// ManagedServerBackupFile is the on-disk JSON shape.
type ManagedServerBackupFile struct {
	Version        int                      `json:"version"`
	Type           string                   `json:"type"`
	ExportedAt     time.Time                `json:"exportedAt"`
	ManagedServers []ManagedServerBackupDTO `json:"managedServers"`
	Warnings       []BackupWarningDTO       `json:"warnings,omitempty"`
}

type BackupWarningDTO struct {
	InterfaceName string `json:"interfaceName,omitempty"`
	Message       string `json:"message"`
}

const (
	backupFileType    = "awg-manager-managed-server-backup"
	backupFileVersion = 1
)

// RestoreOptionsDTO mirrors managed.RestoreOptions on the wire. Defined
// in api/ so swag resolves it without crossing package boundaries.
type RestoreOptionsDTO struct {
	AllowRenumber bool `json:"allowRenumber" example:"false"`
}

// RestoreOutcomeDTO mirrors managed.RestoreOutcome on the wire.
type RestoreOutcomeDTO struct {
	Name       string   `json:"name" example:"Wireguard1"`
	NewName    string   `json:"newName,omitempty" example:"Wireguard2"`
	Action     string   `json:"action" example:"created"`
	AddedPeers int      `json:"addedPeers,omitempty" example:"2"`
	Conflicts  []string `json:"conflicts,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// ManagedServerImportRequest is the body of POST /api/managed/import.
type ManagedServerImportRequest struct {
	ManagedServers []ManagedServerBackupDTO `json:"managedServers"`
	Options        RestoreOptionsDTO        `json:"options"`
	Version        int                      `json:"version"`
	Type           string                   `json:"type"`
}

// ManagedServerRestoreDriftRequest is the body of POST /api/managed/restore-drift.
type ManagedServerRestoreDriftRequest struct {
	Options RestoreOptionsDTO `json:"options"`
}

// ManagedServerRestoreResponse is the response of /import and /restore-drift.
type ManagedServerRestoreResponse struct {
	Outcomes []RestoreOutcomeDTO `json:"outcomes"`
}

// ManagedServerDriftResponse is the response of GET /api/managed/drift.
type ManagedServerDriftResponse struct {
	Drift []ManagedServerBackupDTO `json:"drift"`
}

// ── Wire envelopes (swag-only) ──
// response.Success() wraps inner data in {success, data: <inner>} on the
// wire. The envelope types below carry that wire shape so swag annotations
// match what the daemon actually serves and Prism mocks return realistic
// payloads. Handlers continue to write the inner types via response.Success.

// ManagedServerExportEnvelope is the wire shape of GET /api/managed/export.
type ManagedServerExportEnvelope struct {
	Success bool                    `json:"success" example:"true"`
	Data    ManagedServerBackupFile `json:"data"`
}

// ManagedServerImportEnvelope is the wire shape of POST /api/managed/import
// and POST /api/managed/restore-drift.
type ManagedServerImportEnvelope struct {
	Success bool                         `json:"success" example:"true"`
	Data    ManagedServerRestoreResponse `json:"data"`
}

// ManagedServerDriftEnvelope is the wire shape of GET /api/managed/drift.
type ManagedServerDriftEnvelope struct {
	Success bool                       `json:"success" example:"true"`
	Data    ManagedServerDriftResponse `json:"data"`
}

// managedServerToBackupDTO converts a storage entry to its wire form.
func managedServerToBackupDTO(s storage.ManagedServer) ManagedServerBackupDTO {
	peers := make([]ManagedPeerDTO, len(s.Peers))
	for i, p := range s.Peers {
		peers[i] = ManagedPeerDTO{
			PublicKey:    p.PublicKey,
			PrivateKey:   p.PrivateKey,
			PresharedKey: p.PresharedKey,
			Description:  p.Description,
			TunnelIP:     p.TunnelIP,
			DNS:          p.DNS,
			Enabled:      p.Enabled,
		}
	}
	return ManagedServerBackupDTO{
		InterfaceName: s.InterfaceName,
		Description:   s.Description,
		Address:       s.Address,
		Mask:          s.Mask,
		ListenPort:    s.ListenPort,
		Endpoint:      s.Endpoint,
		DNS:           s.DNS,
		MTU:           s.MTU,
		NATEnabled:    s.NATEnabled,
		PrivateKey:    s.PrivateKey,
		Policy:        s.Policy,
		Peers:         peers,
		I1:            s.I1,
		I2:            s.I2,
		I3:            s.I3,
		I4:            s.I4,
		I5:            s.I5,
		ASC:           s.ASC,
	}
}

// restoreOptionsFromDTO converts the wire form to managed.RestoreOptions.
func restoreOptionsFromDTO(d RestoreOptionsDTO) managed.RestoreOptions {
	return managed.RestoreOptions{AllowRenumber: d.AllowRenumber}
}

// outcomesToDTO converts a slice of restore outcomes to wire form.
func outcomesToDTO(in []managed.RestoreOutcome) []RestoreOutcomeDTO {
	out := make([]RestoreOutcomeDTO, len(in))
	for i, o := range in {
		out[i] = RestoreOutcomeDTO{
			Name:       o.Name,
			NewName:    o.NewName,
			Action:     o.Action,
			AddedPeers: o.AddedPeers,
			Conflicts:  o.Conflicts,
			Error:      o.Error,
		}
	}
	return out
}

// backupDTOToManagedServer converts wire form back to a storage entry,
// used on import before handing off to managed.Service.Restore.
func backupDTOToManagedServer(d ManagedServerBackupDTO) storage.ManagedServer {
	peers := make([]storage.ManagedPeer, len(d.Peers))
	for i, p := range d.Peers {
		peers[i] = storage.ManagedPeer{
			PublicKey:    p.PublicKey,
			PrivateKey:   p.PrivateKey,
			PresharedKey: p.PresharedKey,
			Description:  p.Description,
			TunnelIP:     p.TunnelIP,
			DNS:          p.DNS,
			Enabled:      p.Enabled,
		}
	}
	return storage.ManagedServer{
		InterfaceName: d.InterfaceName,
		Description:   d.Description,
		Address:       d.Address,
		Mask:          d.Mask,
		ListenPort:    d.ListenPort,
		Endpoint:      d.Endpoint,
		DNS:           d.DNS,
		MTU:           d.MTU,
		NATEnabled:    d.NATEnabled,
		PrivateKey:    d.PrivateKey,
		Policy:        d.Policy,
		Peers:         peers,
		I1:            d.I1,
		I2:            d.I2,
		I3:            d.I3,
		I4:            d.I4,
		I5:            d.I5,
		ASC:           d.ASC,
	}
}

// Export handles GET /api/managed/export.
//
//	@Summary		Export all managed servers
//	@Description	Returns a JSON backup file with every managed server including private keys.
//	@Tags			managed
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	ManagedServerExportEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/managed/export [get]
func (h *ManagedServerBackupHandler) Export(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	servers, err := h.svc.ExportAll(r.Context())
	if err != nil {
		response.InternalError(w, "export: "+err.Error())
		return
	}
	dtos := make([]ManagedServerBackupDTO, len(servers))
	warnings := make([]BackupWarningDTO, 0)
	for i, s := range servers {
		dtos[i] = managedServerToBackupDTO(s)
		asc, err := h.svc.GetASCParams(r.Context(), s.InterfaceName)
		if err != nil || isEmptyASC(asc) {
			warnings = append(warnings, BackupWarningDTO{
				InterfaceName: s.InterfaceName,
				Message:       "ASC params could not be exported",
			})
		} else {
			dtos[i].ASC = asc
		}
		if s.PrivateKey == "" {
			warnings = append(warnings, BackupWarningDTO{
				InterfaceName: s.InterfaceName,
				Message:       "server private key is missing; restore is impossible",
			})
		}
	}
	response.Success(w, ManagedServerBackupFile{
		Version:        backupFileVersion,
		Type:           backupFileType,
		ExportedAt:     routerclock.Get().Now,
		ManagedServers: dtos,
		Warnings:       warnings,
	})
}

// Import handles POST /api/managed/import.
//
//	@Summary		Import a managed-server backup
//	@Description	Restores managed servers from a backup file. Per-server atomic with pre-flight conflict detection.
//	@Tags			managed
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		ManagedServerImportRequest	true	"backup contents + options"
//	@Success		200		{object}	ManagedServerImportEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/managed/import [post]
func (h *ManagedServerBackupHandler) Import(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req ManagedServerImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request: "+err.Error(), "INVALID_REQUEST")
		return
	}
	if req.Type != backupFileType {
		response.Error(w, "unknown file type: "+req.Type, "INVALID_REQUEST")
		return
	}
	if req.Version != backupFileVersion {
		response.Error(w, fmt.Sprintf("unsupported version %d (only %d)", req.Version, backupFileVersion), "INVALID_REQUEST")
		return
	}
	servers := make([]storage.ManagedServer, len(req.ManagedServers))
	for i, d := range req.ManagedServers {
		servers[i] = backupDTOToManagedServer(d)
	}
	outcomes := h.svc.Restore(r.Context(), servers, restoreOptionsFromDTO(req.Options))
	response.Success(w, ManagedServerRestoreResponse{Outcomes: outcomesToDTO(outcomes)})
	if hasActionableMutation(outcomes) {
		h.bus.PublishInvalidated(events.ResourceServers, "managed-restore")
	}
}

func isEmptyASC(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return true
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return true
	}
	if len(obj) == 0 {
		return true
	}
	return managed.ValidateASCParams(trimmed) != nil
}

// Drift handles GET /api/managed/drift.
//
//	@Summary		List managed servers missing from NDMS
//	@Description	Returns settings.json entries whose NDMS interface is absent.
//	@Tags			managed
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	ManagedServerDriftEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/managed/drift [get]
func (h *ManagedServerBackupHandler) Drift(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	drift, err := h.svc.Drift(r.Context())
	if err != nil {
		response.InternalError(w, "drift: "+err.Error())
		return
	}
	dtos := make([]ManagedServerBackupDTO, len(drift))
	for i, s := range drift {
		dtos[i] = managedServerToBackupDTO(s)
	}
	response.Success(w, ManagedServerDriftResponse{Drift: dtos})
}

// RestoreDrift handles POST /api/managed/restore-drift.
//
//	@Summary		Restore drifted managed servers
//	@Description	Detects drift internally then runs Restore on it. Convenience entry for the boot-time banner.
//	@Tags			managed
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		ManagedServerRestoreDriftRequest	false	"options"
//	@Success		200		{object}	ManagedServerImportEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/managed/restore-drift [post]
func (h *ManagedServerBackupHandler) RestoreDrift(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req ManagedServerRestoreDriftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		response.Error(w, "invalid request: "+err.Error(), "INVALID_REQUEST")
		return
	}
	drift, err := h.svc.Drift(r.Context())
	if err != nil {
		response.InternalError(w, "drift: "+err.Error())
		return
	}
	outcomes := h.svc.RestoreDrift(r.Context(), drift, restoreOptionsFromDTO(req.Options))
	response.Success(w, ManagedServerRestoreResponse{Outcomes: outcomesToDTO(outcomes)})
	if hasActionableMutation(outcomes) {
		h.bus.PublishInvalidated(events.ResourceServers, "managed-restore-drift")
	}
}

// hasActionableMutation reports whether any outcome action warrants an SSE
// invalidation (i.e. a server was actually created, merged, or renamed).
func hasActionableMutation(outcomes []managed.RestoreOutcome) bool {
	for _, o := range outcomes {
		switch o.Action {
		case "created", "merged", "renamed":
			return true
		}
	}
	return false
}
