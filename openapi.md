# OpenAPI / Swagger: краткий гайд

Проект генерирует OpenAPI (Swagger) YAML из Go-аннотаций через `swag`.

## 1) Как аннотировать хендлеры

- Добавляй swagger-комментарии над именованными функциями/методами в `internal/api/*`.
- Типичный блок:
  - `@Summary`, `@Tags`
  - `@Accept` / `@Produce`
  - `@Param` (query/path/body)
  - `@Success` / `@Failure`
  - `@Security CookieAuth` для защищенных роутов
  - `@Router /path [method]`
- Глобальные метаданные API и схема cookie-безопасности находятся в `cmd/awg-manager/docs.go`.

Пример:

```go
// GetSystemInfo godoc
// @Summary      Системная информация
// @Tags         system
// @Produce      json
// @Security     CookieAuth
// @Success      200 {object} map[string]interface{}
// @Failure      500 {object} response.ErrorResponse
// @Router       /system/info [get]
func (h *SystemHandler) Info(w http.ResponseWriter, r *http.Request) {
	// handler logic
}
```

## 2) Где раздается YAML

- Runtime-эндпоинт: `GET /api/openapi.yaml`
- Регистрируется в `internal/server/server.go`.
- Файл вшивается из `internal/openapi/swagger.yaml` через `internal/openapi/embed.go`.

## 3) Как открыть Swagger UI

- Запусти backend (daemon) и frontend dev-сервер.
- Открой страницу: `/dev/api-docs`
- Исходник страницы: `frontend/src/routes/dev/api-docs/+page.svelte`
- UI получает спеку с `/api/openapi.yaml` (через Vite proxy).

## 4) Как собрать/пересобрать OpenAPI YAML

Из корня репозитория:

```bash
go generate ./cmd/awg-manager
```

Команда запускает зафиксированную версию `swag` из `cmd/awg-manager/docs.go` и перезаписывает:

- `internal/openapi/swagger.yaml`

Запускай это перед коммитом, если менялись API-аннотации.

## 5) Как поднять mock-сервер на 8080

Запуск Prism напрямую по спеке (без изменений в коде):

```bash
cd frontend
npm run mock
```

Проверка напрямую:

- `http://127.0.0.1:8080/<path-из-openapi>`

Чтобы фронт на `/api/...` работал с Prism и Swagger UI открывался без backend:

```bash
cd frontend
npm run dev:mock
```

Команда `dev:mock`:
- делает `sync:openapi` (копирует `../internal/openapi/swagger.yaml` в `frontend/static/openapi.yaml`);
- запускает Vite с `VITE_API_STRIP_PREFIX=1` (роуты `/api/*` переписываются в `/*` для Prism).

или вручную включить в `frontend/.env`:

```bash
VITE_API_TARGET=http://127.0.0.1:8080
VITE_API_STRIP_PREFIX=1
```

и перезапустить `npm run dev`.

Примечания:

- Prism mock не выполняет backend-логику, а отдает ответы на основе примеров/схем OpenAPI.
- Убедись, что backend не занят на `8080` (или используй другой порт Prism).
