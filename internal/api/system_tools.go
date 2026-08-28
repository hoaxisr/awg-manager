package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	sysfiles "github.com/hoaxisr/awg-manager/internal/sys/files"
	"github.com/hoaxisr/awg-manager/internal/sys/opkg"
	sysports "github.com/hoaxisr/awg-manager/internal/sys/ports"
	"github.com/hoaxisr/awg-manager/internal/sys/procmon"
	"github.com/hoaxisr/awg-manager/internal/sys/services"
)

// SystemToolsHandler exposes Entware file manager, init.d services, opkg, port inspector, and process monitor.
type SystemToolsHandler struct {
	settings *storage.SettingsStore
	log      *logging.ScopedLogger
	files    *sysfiles.Sandbox
	services *services.Scanner
	opkg     *opkg.Client
	ports    *sysports.Scanner
	procmon  *procmon.Sampler
	bus      *events.Bus
}

func (h *SystemToolsHandler) SetEventBus(bus *events.Bus) {
	h.bus = bus
}

func (h *SystemToolsHandler) emitEvent(action, subject, details string) {
	h.log.Info(action, subject, details)
	if h.bus != nil {
		h.bus.Publish("system:tool-action", map[string]string{
			"type":    "system_tool_action",
			"action":  action,
			"subject": subject,
			"details": details,
		})
	}
}

// NewSystemToolsHandler creates the handler.
func NewSystemToolsHandler(settings *storage.SettingsStore, log logging.AppLogger) *SystemToolsHandler {
	return &SystemToolsHandler{
		settings: settings,
		log:      logging.NewScopedLogger(log, "system", "tools"),
		files:    sysfiles.NewSandbox(nil),
		services: services.NewScanner(),
		opkg:     opkg.NewClient(),
		ports:    sysports.NewScanner(),
		procmon:  procmon.NewSampler(),
	}
}

// ExpertOnly оборачивает хендлер проверкой usage level: вкладка «Система»
// целиком expert-only, и гейт стоит один раз на регистрации маршрута, а не
// копией в начале каждого из 32 хендлеров.
func (h *SystemToolsHandler) ExpertOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.requireExpert(w, r) {
			return
		}
		next(w, r)
	}
}

func (h *SystemToolsHandler) requireExpert(w http.ResponseWriter, r *http.Request) bool {
	if h.settings == nil {
		response.InternalError(w, "settings unavailable")
		return false
	}
	st, err := h.settings.Get()
	if err != nil {
		response.InternalError(w, "settings unavailable")
		return false
	}
	if storage.NormalizeUsageLevel(st.UsageLevel) != storage.UsageLevelExpert {
		response.ErrorWithStatus(w, http.StatusForbidden, "expert usage level required", "FORBIDDEN")
		return false
	}
	return true
}
