package openfluxlink

import (
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// decodeRequest — тело POST link/decode (паритет api.DecodeLinkRequest).
type decodeRequest struct {
	Link string `json:"link"`
}

// DecodeResponse — конверт разбора openflux://-ссылки. Тип объявлен ради
// спеки: генератор фронтовых схем ключует валидацию ПУТЁМ.
type DecodeResponse struct {
	Success bool        `json:"success" example:"true"`
	Data    LinkPayload `json:"data"`
}

// Decode — POST /api/proxyrt/openflux/link/decode: разбор openflux://-ссылки,
// чтобы фронт заполнил поля клиента без ручного перенабора.
//
//	@Summary	Разобрать ссылку openflux://
//	@Tags		proxyrt
//	@Accept		json
//	@Produce	json
//	@Security	CookieAuth
//	@Param		request	body		decodeRequest	true	"Ссылка"
//	@Success	200		{object}	DecodeResponse
//	@Failure	400		{object}	api.APIErrorEnvelope
//	@Router		/proxyrt/openflux/link/decode [post]
func Decode(w http.ResponseWriter, r *http.Request) {
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
		response.Error(w, err.Error(), "OPENFLUX_LINK_DECODE_FAILED")
		return
	}
	response.Success(w, payload)
}

// ClientConfigFromPayload — конфиг клиента из разобранной ссылки: то, чем
// фронт заполняет мастера. Ссылка выхода несёт Role=exit и Mode — их клиент
// НЕ забирает: бэкенд выбирается на выходной ноде (README upstream).
func ClientConfigFromPayload(p LinkPayload) roles.OpenFluxClientConfig {
	return roles.OpenFluxClientConfig{
		Transport:     p.Transport,
		URL:           p.URL,
		MaxToken:      p.MaxToken,
		MaxUID:        p.MaxUID,
		Codec:         p.Codec,
		EncryptionKey: p.Key,
	}
}
