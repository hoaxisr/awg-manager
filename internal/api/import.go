package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/obfuscator"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/signature"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// ImportHandler handles config import operations.
type ImportHandler struct {
	svc            TunnelService
	store          *storage.AWGTunnelStore
	settingsStore  *storage.SettingsStore
	pingCheck      PingCheckService
	tunnelsHandler *TunnelsHandler
	proxyRecords   ProxyRecordLister
	log            *logging.ScopedLogger
}

// NewImportHandler creates a new import handler.
func NewImportHandler(svc TunnelService, store *storage.AWGTunnelStore, appLogger logging.AppLogger) *ImportHandler {
	return &ImportHandler{
		svc:   svc,
		store: store,
		log:   logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubLifecycle),
	}
}

// SetSettingsStore sets the settings store for reading defaults.
func (h *ImportHandler) SetSettingsStore(store *storage.SettingsStore) {
	h.settingsStore = store
}

// SetPingCheckService sets the ping check service.
func (h *ImportHandler) SetPingCheckService(svc PingCheckService) {
	h.pingCheck = svc
}

// SetTunnelsHandler sets the tunnels handler for SSE publishing after import.
func (h *ImportHandler) SetTunnelsHandler(th *TunnelsHandler) {
	h.tunnelsHandler = th
}

// SetProxyRecords wires the proxy instance store for linked-client
// listen → endpoint sync on import.
func (h *ImportHandler) SetProxyRecords(records ProxyRecordLister) {
	h.proxyRecords = records
}

// ImportConfRequest is the body for POST /import/conf.
type ImportConfRequest struct {
	Content          string `json:"content"`
	Name             string `json:"name"`
	Backend          string `json:"backend"` // "nativewg" | "kernel" (default: "kernel")
	FreeTurnClientID string `json:"freeTurnClientId,omitempty"`
	WdttClientID     string `json:"wdttClientId,omitempty"`
	// InstallURL — ссылка установки Phobos (…/api/install/<token>): роутер сам
	// качает package.tar.gz и берёт из него .conf. TLS панели не проверяется.
	InstallURL string `json:"installUrl,omitempty"`
	// Obfuscator — параметры релея руками (ClusterM). Несовместимо с [instance] в Content.
	Obfuscator *ObfuscatorImportRequest `json:"obfuscator,omitempty"`
	// AmneziaCountry — код страны подписки Amnezia Premium, из которой взята
	// конфигурация. Шлёт его мастер; обычный импорт файла поле не присылает,
	// и туннель остаётся без метки страны.
	AmneziaCountry string `json:"amneziaCountry,omitempty"`
}

// ObfuscatorImportRequest — пользовательские поля релея при ручном вводе.
type ObfuscatorImportRequest struct {
	Flavor         string `json:"flavor"` // "phobos" | "clusterm" (пусто = clusterm)
	Target         string `json:"target"`
	Key            string `json:"key"`
	Masking        string `json:"masking"`
	MaxDummy       int    `json:"maxDummy"`
	IdleTimeout    int    `json:"idleTimeout,omitempty"`
	ObfuscateBytes int    `json:"obfuscateBytes,omitempty"`
}

// ImportConf imports a WireGuard/AmneziaWG config file.
//
//	@Summary		Import tunnel config
//	@Tags			import
//	@Accept			json
//	@Produce		json
//	@Param			body	body		ImportConfRequest	true	"Config content and optional metadata"
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/import/conf [post]
func (h *ImportHandler) ImportConf(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[ImportConfRequest](w, r, http.MethodPost)
	if !ok {
		return
	}

	if req.Content == "" && req.InstallURL == "" {
		response.Error(w, "missing config content", "MISSING_CONTENT")
		return
	}

	obf, obfWarnings, err := h.resolveObfuscatorImport(r.Context(), &req)
	if err != nil {
		response.Error(w, err.Error(), obfImportErrCode(err))
		return
	}

	req.Content = h.patchImportContentForLinkedClient(req.Content, req.FreeTurnClientID, req.WdttClientID)

	if existingID := findLinkedTunnelID(h.store, req.FreeTurnClientID, req.WdttClientID); existingID != "" {
		// ReplaceConfig параметров релея не принимает: доехав сюда, обфускатор
		// потерялся бы молча — туннель остался бы обычным, а пользователь
		// считал бы его обфусцированным. Fail-closed.
		if obf != nil {
			response.Error(w, "обфускатор нельзя сочетать со связанным прокси-клиентом (wdtt/freeturn)", "OBFUSCATOR_CONFLICT")
			return
		}
		// Страна подписки здесь НЕ трогается (nil): эта ветка — повторный
		// импорт связанного прокси-клиента (wdtt/freeturn), а не замена
		// конфигурации из мастера. Передать сюда "" значило бы стирать метку
		// у чужого туннеля на каждом обновлении клиента.
		if err := h.svc.ReplaceConfig(r.Context(), existingID, req.Content, req.Name, service.ReplaceOptions{}); err != nil {
			h.log.Warn("import", req.Name, "Failed to replace linked tunnel: "+err.Error())
			response.Error(w, err.Error(), "IMPORT_FAILED")
			return
		}
		h.log.Info("import", req.Name, "Linked tunnel config replaced")
		var quiescent time.Time
		if h.tunnelsHandler != nil {
			h.tunnelsHandler.publishTunnelList(r.Context())
			quiescent = h.tunnelsHandler.quiescentFor(existingID)
		}
		resp, err := BuildTunnelResponse(r, h.svc, h.store, existingID, quiescent)
		if err != nil {
			response.Error(w, err.Error(), "IMPORT_FAILED")
			return
		}
		if warnings := h.svc.CheckAddressConflicts(r.Context(), existingID); len(warnings) > 0 {
			resp["warnings"] = warnings
		}
		response.Success(w, resp)
		return
	}

	tunnel, err := h.svc.Import(r.Context(), req.Content, req.Name, req.Backend, service.ImportLink{
		WdttClientID:     req.WdttClientID,
		FreeTurnClientID: req.FreeTurnClientID,
		Obfuscator:       obf,
		AmneziaCountry:   req.AmneziaCountry,
	})
	if err != nil {
		h.log.Warn("import", req.Name, "Failed to import tunnel: "+err.Error())
		if errors.Is(err, signature.ErrPacketsTooLarge) {
			response.Error(w, err.Error(), "SIGNATURE_TOO_LARGE")
			return
		}
		if errors.Is(err, signature.ErrInvalidPacketTag) {
			response.Error(w, err.Error(), "SIGNATURE_INVALID_TAG")
			return
		}
		response.Error(w, err.Error(), "IMPORT_FAILED")
		return
	}

	// Post-import defaults: PingCheck. Отказ записи НЕ отменяет импорт (туннель
	// уже заведён, и IMPORT_FAILED спровоцировал бы повторный импорт
	// дубликатом), но и не глотается — профиль F48, а не F47.
	//
	// Связей здесь БОЛЬШЕ НЕТ: они уехали в сам Create через service.ImportLink.
	// Разница принципиальная — умолчание, не доехавшее до записи, читается как
	// «пользователь его не включал», а не доехавшая связь делает туннель
	// сиротой, которого не видит уборка связанных.
	if err := h.store.Update(tunnel.ID, func(stored *storage.AWGTunnel) error {
		changed := false
		if h.pingCheck != nil && stored.PingCheck == nil {
			stored.PingCheck = storage.DefaultTunnelPingCheck()
			changed = true
		}
		if !changed {
			return storage.ErrNoChange
		}
		return nil
	}); err != nil {
		h.log.Warn("import", tunnel.Name, "persist post-import defaults: "+err.Error())
	}

	h.log.Info("import", tunnel.Name, "Tunnel imported")
	var quiescent time.Time
	if h.tunnelsHandler != nil {
		h.tunnelsHandler.publishTunnelList(r.Context())
		quiescent = h.tunnelsHandler.quiescentFor(tunnel.ID)
	}

	resp, err := BuildTunnelResponse(r, h.svc, h.store, tunnel.ID, quiescent)
	if err != nil {
		response.Error(w, err.Error(), "IMPORT_FAILED")
		return
	}
	if warnings := append(obfWarnings, h.svc.CheckAddressConflicts(r.Context(), tunnel.ID)...); len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	response.Success(w, resp)
}

// obfImportError — отказ разбора параметров релея с кодом для фронта.
type obfImportError struct{ code, msg string }

func (e *obfImportError) Error() string { return e.msg }

func obfImportErrCode(err error) string {
	var oe *obfImportError
	if errors.As(err, &oe) {
		return oe.code
	}
	return "IMPORT_FAILED"
}

// resolveObfuscatorImport приводит запрос к (Content без обёрток, параметры релея):
// install-ссылка → phobos:// → `= none` → [instance] (phobos) | ручные поля (clusterm).
// Саму секцию [instance] из контента срезает Import — здесь она только читается.
func (h *ImportHandler) resolveObfuscatorImport(ctx context.Context, req *ImportConfRequest) (*storage.Obfuscator, []string, error) {
	if req.InstallURL != "" {
		conf, err := obfuscator.FetchPhobosConf(ctx, req.InstallURL)
		if err != nil {
			return nil, nil, &obfImportError{"INSTALL_LINK_FAILED", err.Error()}
		}
		req.Content = conf
	}
	if obfuscator.IsPhobosLink(req.Content) {
		conf, name, err := obfuscator.DecodePhobosLink(req.Content)
		if err != nil {
			return nil, nil, &obfImportError{"PHOBOS_LINK_INVALID", err.Error()}
		}
		req.Content = conf
		if req.Name == "" {
			req.Name = name
		}
	}
	// `= none` дописывает только производитель phobos://-ссылки; обычный .conf
	// с панели этого не содержит. Сервис нормализацию не делает (Task 8).
	req.Content = obfuscator.NormalizeNoneValues(req.Content)
	inst, unknown, present, err := obfuscator.ParseInstance(req.Content)
	if err != nil {
		return nil, nil, &obfImportError{"OBFUSCATOR_INVALID", err.Error()}
	}
	var o *storage.Obfuscator
	switch {
	case present && req.Obfuscator != nil:
		return nil, nil, &obfImportError{"OBFUSCATOR_CONFLICT", "конфиг уже содержит [instance] — это конфиг Phobos, импортируйте его во вкладке Phobos"}
	case present:
		o = inst
		if len(unknown) > 0 {
			h.log.Warn("import", req.Name, "[instance]: неизвестные ключи пропущены: "+strings.Join(unknown, ", "))
		}
	case req.InstallURL != "":
		// Ссылку установки даёт только вкладка Phobos: без [instance] импорт
		// молча дал бы обычный nativewg-туннель, который потом падает на
		// старте с чужим «добавьте параметры AWG».
		return nil, nil, &obfImportError{"OBFUSCATOR_INVALID", "в пакете Phobos нет секции [instance]"}
	case req.Obfuscator != nil:
		m := req.Obfuscator
		flavor := m.Flavor
		if flavor == "" {
			flavor = storage.ObfuscatorFlavorClusterM
		}
		o = &storage.Obfuscator{Flavor: flavor, Target: m.Target, Key: m.Key, Masking: strings.ToUpper(m.Masking),
			MaxDummy: m.MaxDummy, IdleTimeout: m.IdleTimeout, ObfuscateBytes: m.ObfuscateBytes}
	default:
		return nil, nil, nil
	}
	if err := obfuscator.Validate(o); err != nil {
		return nil, nil, &obfImportError{"OBFUSCATOR_INVALID", err.Error()}
	}
	var warnings []string
	if w := obfuscator.DetectForeign().Warning(); w != "" {
		warnings = append(warnings, w)
	}
	return o, warnings, nil
}

func findLinkedTunnelID(store *storage.AWGTunnelStore, freeTurnClientID, wdttClientID string) string {
	if store == nil {
		return ""
	}
	ftID := strings.TrimSpace(freeTurnClientID)
	wdID := strings.TrimSpace(wdttClientID)
	if ftID == "" && wdID == "" {
		return ""
	}
	tunnels, err := store.List()
	if err != nil {
		return ""
	}
	for _, tun := range tunnels {
		if ftID != "" && strings.TrimSpace(tun.FreeTurnClientID) == ftID {
			return tun.ID
		}
		if wdID != "" && strings.TrimSpace(tun.WdttClientID) == wdID {
			return tun.ID
		}
	}
	return ""
}
