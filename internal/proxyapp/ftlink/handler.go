package ftlink

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// addRequest — тело POST allowlist; json-имена вербатим старого
// api.allowlistAddRequest (api/freeturn.go:793-796).
type addRequest struct {
	ClientID string `json:"clientId"`
	Comment  string `json:"comment"`
}

// decodeRequest — тело POST link/decode (api.DecodeLinkRequest).
type decodeRequest struct {
	Link string `json:"link"`
}

// DecodeResponse — конверт разбора freeturn://-ссылки. Тип объявлен ради
// спеки: генератор фронтовых схем ключует валидацию ПУТЁМ.
type DecodeResponse struct {
	Success bool        `json:"success" example:"true"`
	Data    LinkPayload `json:"data"`
}

// Serve обслуживает список разрешённых Client ID. Пути регистрирует проводка:
// у пакета нет своего мультиплексора, ключ инстанса и хвост пути приходят
// аргументами.
//
//	GET    /api/proxyrt/instances/{key}/allowlist
//	POST   /api/proxyrt/instances/{key}/allowlist
//	DELETE /api/proxyrt/instances/{key}/allowlist            (выключить проверку)
//	DELETE /api/proxyrt/instances/{key}/allowlist/{clientId} (вычеркнуть один id)
func (s *Service) Serve(w http.ResponseWriter, r *http.Request, key string, sub []string) {
	if len(sub) == 0 {
		switch r.Method {
		case http.MethodGet:
			st, err := s.List(key)
			if err != nil {
				s.fail(w, err, "FREETURN_ALLOWLIST_LIST_FAILED")
				return
			}
			response.Success(w, st)
		case http.MethodPost:
			var req addRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				response.Error(w, "invalid request body", "BAD_REQUEST")
				return
			}
			res, err := s.Add(r.Context(), key, req.ClientID, req.Comment)
			if err != nil {
				s.fail(w, err, "FREETURN_ALLOWLIST_ADD_FAILED")
				return
			}
			response.Success(w, res)
		case http.MethodDelete:
			needsRestart, err := s.Disable(r.Context(), key)
			if err != nil {
				s.fail(w, err, "FREETURN_ALLOWLIST_DISABLE_FAILED")
				return
			}
			response.Success(w, map[string]any{"message": "allowlist disabled", "needsRestart": needsRestart})
		default:
			response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		}
		return
	}

	if len(sub) == 1 && r.Method == http.MethodDelete {
		if err := s.Remove(key, sub[0]); err != nil {
			s.fail(w, err, "FREETURN_ALLOWLIST_REMOVE_FAILED")
			return
		}
		response.Success(w, map[string]string{"message": "removed"})
		return
	}

	response.ErrorWithStatus(w, http.StatusNotFound, "Not found", "NOT_FOUND")
}

// Decode — POST /api/proxyrt/freeturn/link/decode: разбор freeturn://-ссылки,
// чтобы фронт заполнил поля клиента без ручного перенабора.
//
//	@Summary	Разобрать ссылку freeturn://
//	@Tags		proxyrt
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Param		request	body		decodeRequest	true	"Ссылка"
//	@Success	200		{object}	DecodeResponse
//	@Failure	400		{object}	api.APIErrorEnvelope
//	@Router		/proxyrt/freeturn/link/decode [post]
func (s *Service) Decode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	var req decodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "BAD_REQUEST")
		return
	}
	payload, err := DecodeLink(req.Link)
	if err != nil {
		response.Error(w, err.Error(), "FREETURN_LINK_DECODE_FAILED")
		return
	}
	response.Success(w, payload)
}

// fail отдаёт отказ. Отсутствие инстанса — 404: в старом мире роль и
// существование задавал путь, здесь их несёт только ключ, и «нет такого
// ключа» обязано отличаться от «операция не удалась».
func (s *Service) fail(w http.ResponseWriter, err error, code string) {
	if errors.Is(err, ErrInstanceNotFound) {
		response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
		return
	}
	response.Error(w, err.Error(), code)
}
