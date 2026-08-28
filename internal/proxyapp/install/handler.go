package install

import (
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// installRequest — тело POST установки.
type installRequest struct {
	Subsystem string `json:"subsystem"`
}

// InstallStatusResponse — конверт статуса установки. Тип объявлен ради спеки:
// генератор фронтовых схем ключует валидацию ПУТЁМ и без описанного ответа
// молча пропускает его без проверки.
type InstallStatusResponse struct {
	Success bool          `json:"success" example:"true"`
	Data    InstallStatus `json:"data"`
}

// InstallMessage — тело успеха установки.
type InstallMessage struct {
	Message string `json:"message" example:"installed"`
}

// InstallResponse — конверт установки.
type InstallResponse struct {
	Success bool           `json:"success" example:"true"`
	Data    InstallMessage `json:"data"`
}

// ServeStatus обслуживает GET /api/proxyrt/install/status?subsystem=wdtt|freeturn.
// Пути регистрирует проводка: своего мультиплексора у пакета нет.
//
//	@Summary		Статус установки бинарей подсистемы
//	@Description	Семь полей install-блока: поддержка сервера архитектурой, доступность
//	@Description	установки, версии, наличие обновления, идёт ли установка, часы роутера.
//	@Tags			proxyrt
//	@Produce		json
//	@Security		CookieAuth
//	@Param			subsystem	query		string	true	"Подсистема: wdtt|freeturn"
//	@Success		200			{object}	InstallStatusResponse
//	@Failure		400			{object}	api.APIErrorEnvelope
//	@Router			/proxyrt/install/status [get]
func (s *Service) ServeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	st, err := s.Status(r.URL.Query().Get("subsystem"))
	if err != nil {
		response.Error(w, err.Error(), "BAD_REQUEST")
		return
	}
	response.Success(w, st)
}

// ServeInstall обслуживает POST /api/proxyrt/install (тело {"subsystem":...}).
// Синхронный — ассеты небольшие (6-17 МБ); фронт блокирует кнопку по
// installing из статуса.
//
//	@Summary	Установить бинари подсистемы
//	@Tags		proxyrt
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Param		request	body		installRequest	true	"Подсистема"
//	@Success	200		{object}	InstallResponse
//	@Failure	400		{object}	api.APIErrorEnvelope
//	@Router		/proxyrt/install [post]
func (s *Service) ServeInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req installRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	sub, err := s.pick(req.Subsystem)
	if err != nil {
		response.Error(w, err.Error(), "BAD_REQUEST")
		return
	}
	if err := s.Install(r.Context(), req.Subsystem); err != nil {
		response.Error(w, err.Error(), installErrorCode(sub.name))
		return
	}
	response.Success(w, map[string]string{"message": installedMessage(sub.name)})
}

// installErrorCode — код отказа установки; вербатим старых ручек
// /api/wdtt/install и /api/freeturn/install.
func installErrorCode(name Subsystem) string {
	if name == SubsystemFreeTurn {
		return "FREETURN_INSTALL_FAILED"
	}
	return "WDTT_INSTALL_FAILED"
}

// installedMessage — тело успеха; тексты у подсистем разные и оставлены
// прежними.
func installedMessage(name Subsystem) string {
	if name == SubsystemFreeTurn {
		return "freeturn installed"
	}
	return "installed"
}
