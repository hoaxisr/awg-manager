package subscription

import (
	"errors"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// Serve — POST /api/proxyrt/instances/{key}/subscription/refresh. Путь
// регистрирует проводка: у пакета нет своего мультиплексора, ключ инстанса
// приходит аргументом.
//
// Тело ответа: прежние `payload` и `message` (api/wdtt_subscription.go:27-31)
// плюс `key` вместо `instance`. Саму запись отдаёт отдельная ручка инстанса:
// там живёт маскировка секретов, и второй копии этого правила здесь быть не
// должно (тот же разворот, что у импорта ссылки, задача 8).
func (s *Service) Serve(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	payload, err := s.Refresh(r.Context(), key)
	if err != nil {
		if errors.Is(err, ErrInstanceNotFound) {
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
			return
		}
		response.Error(w, err.Error(), "WDTT_SUBSCRIPTION_REFRESH_FAILED")
		return
	}
	response.Success(w, map[string]any{
		"key":     key,
		"payload": payload,
		"message": "Подписка обновлена — проверьте пароль и VK-хеши, при необходимости перезапустите клиент",
	})
}
