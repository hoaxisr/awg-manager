package wdttusers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// addRequest — тело POST users. json-имена вербатим старого
// api.wdttServerClientAddRequest (api/wdtt_server.go:170-175).
type addRequest struct {
	Password     string `json:"password,omitempty"`
	Comment      string `json:"comment,omitempty"`
	VkHash       string `json:"vkHash,omitempty"`
	MainPassword string `json:"mainPassword,omitempty"`
}

// renameRequest — тело PATCH users/{password}; форма как у переименования
// самого инстанса.
type renameRequest struct {
	Name string `json:"name"`
}

// UsersStatusResponse — конверт ВСЕХ ручек абонентов: форма ответа у них одна.
// Тип объявлен ради спеки: генератор фронтовых схем ключует валидацию ПУТЁМ и
// без описанного ответа молча пропускает его без проверки.
type UsersStatusResponse struct {
	Success bool        `json:"success" example:"true"`
	Data    UsersStatus `json:"data"`
}

// Serve обслуживает ручки абонентов сервера. Пути регистрирует проводка: у
// пакета нет своего мультиплексора, ключ инстанса и хвост пути приходят
// аргументами.
//
//	GET    /api/proxyrt/instances/{key}/users
//	POST   /api/proxyrt/instances/{key}/users
//	DELETE /api/proxyrt/instances/{key}/users
//	DELETE /api/proxyrt/instances/{key}/users/{password}
//	PATCH  /api/proxyrt/instances/{key}/users/{password}
//
// Аннотации спеки перечисляют все пять адресов одним блоком: форма ответа у
// них ОДНА (UsersStatus), и делить блок значило бы делить сам обработчик.
//
//	@Summary		Абоненты wdtt-сервера
//	@Description	Ручки состава: чтение, добавление, удаление всех, удаление и
//	@Description	переименование одного. Поле reload заполняют только мутации состава.
//	@Tags			proxyrt
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			key	path		string	true	"Ключ инстанса (роль:id)"
//	@Success		200	{object}	UsersStatusResponse
//	@Failure		400	{object}	api.APIErrorEnvelope
//	@Failure		404	{object}	api.APIErrorEnvelope
//	@Router			/proxyrt/instances/{key}/users [get]
//	@Router			/proxyrt/instances/{key}/users [post]
//	@Router			/proxyrt/instances/{key}/users [delete]
//	@Router			/proxyrt/instances/{key}/users/{password} [delete]
//	@Router			/proxyrt/instances/{key}/users/{password} [patch]
func (s *Service) Serve(w http.ResponseWriter, r *http.Request, key string, sub []string) {
	switch {
	case len(sub) == 0:
		switch r.Method {
		case http.MethodGet:
			st, err := s.List(r.Context(), key)
			s.respond(w, st, err, "WDTT_SERVER_CLIENTS_FAILED")
		case http.MethodPost:
			var req addRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				response.Error(w, "invalid request body", "BAD_REQUEST")
				return
			}
			st, err := s.Add(r.Context(), key, req.Password, req.Comment, req.VkHash, req.MainPassword)
			s.respond(w, st, err, addErrorCode(err))
		case http.MethodDelete:
			st, err := s.RemoveAll(r.Context(), key)
			s.respond(w, st, err, "WDTT_SERVER_CLIENT_DELETE_FAILED")
		default:
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		}
	case len(sub) == 1:
		password := sub[0]
		switch r.Method {
		case http.MethodDelete:
			st, err := s.Remove(r.Context(), key, password)
			s.respond(w, st, err, "WDTT_SERVER_CLIENT_DELETE_FAILED")
		case http.MethodPatch:
			var req renameRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				response.Error(w, "invalid request body", "BAD_REQUEST")
				return
			}
			st, err := s.Rename(r.Context(), key, password, req.Name)
			s.respond(w, st, err, "WDTT_SERVER_CLIENT_RENAME_FAILED")
		default:
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		}
	default:
		response.ErrorWithStatus(w, http.StatusNotFound, "Not found", "NOT_FOUND")
	}
}

// addErrorCode различает два ЧАСТИЧНЫХ успеха добавления: конверт отказа несёт
// только message и code, поля для признака в нём нет.
//
//	WDTT_SERVER_CLIENT_ADD_NOT_APPLIED — абонент заведён в записи,
//	passwords.json не записан: доступ появится при следующем запуске сервера,
//	и в списке абонент уже есть.
//	WDTT_SERVER_MAIN_PASSWORD_NOT_SAVED — абонент заведён и применён целиком,
//	не сохранился пароль сервера: сервер не стартует, пока его не задать.
func addErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrFileNotWritten):
		return "WDTT_SERVER_CLIENT_ADD_NOT_APPLIED"
	case errors.Is(err, ErrMainPasswordNotSaved):
		return "WDTT_SERVER_MAIN_PASSWORD_NOT_SAVED"
	}
	return "WDTT_SERVER_CLIENT_ADD_FAILED"
}

// respond отдаёт статус или отказ. Отсутствие инстанса — 404: в старом мире
// роль задавал путь, здесь её несёт только ключ, и «нет такого ключа» обязано
// отличаться от «операция не удалась».
func (s *Service) respond(w http.ResponseWriter, st UsersStatus, err error, code string) {
	if err != nil {
		if errors.Is(err, ErrInstanceNotFound) {
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
			return
		}
		response.Error(w, err.Error(), code)
		return
	}
	response.Success(w, st)
}
