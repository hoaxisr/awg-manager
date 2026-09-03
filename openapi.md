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
- Открой страницу: `/api-docs`
- Исходник страницы: `frontend/src/routes/api-docs/+page.svelte`
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

## 6) Stateful mock proxy (state-aware overrides)

`frontend/scripts/mock-proxy.mjs` располагается между Vite и Prism и
предоставляет небольшие override'ы поверх stateless Prism:

- Persistent `usageLevel` между `/settings/get` и `/settings/update`.
- Симуляция ошибки `/singbox/install` по флагу.
- Фейковые `singbox`-логи в `/logs` (8 записей, 6 подгрупп, разные уровни).
- Stateful `mcpEnabled` между `/settings/get` и `/settings/update`.
- Stateful MCP-ключи: `GET /mcp/keys`, `POST /mcp/keys/create`, `POST /mcp/keys/revoke` (плейнтекст `awgm_mock_…` возвращается только при создании, дальше — только id/name/даты).

Запуск рядом с Prism:

```bash
# Терминал A
cd frontend
npm run mock

# Терминал B
node frontend/scripts/mock-proxy.mjs

# Терминал C
cd frontend
VITE_API_TARGET=http://127.0.0.1:8081 npm run dev:mock
```

### Симуляция ошибки установки sing-box

```bash
# При запуске
MOCK_SINGBOX_INSTALL_FAIL=1 node frontend/scripts/mock-proxy.mjs

# В рантайме
curl -X POST http://127.0.0.1:8081/__mock/singbox-install-fail \
  -H 'Content-Type: application/json' \
  -d '{"enabled": true}'
```

Прокси отвечает HTTP 500 с кодом `SINGBOX_INSTALL_ERROR` и подделанным
opkg-style stderr. Отключить — `enabled: false`.

### Просмотр singbox-логов

`GET /logs?group=singbox` — только фейковые singbox-записи.
`GET /logs?group=singbox&subgroup=process` — фильтр по подгруппе.
`GET /logs?group=singbox&subgroup=process&level=error` — плюс уровень.
`GET /logs` (без фильтров) — Prism-заглушки + фейковые singbox-записи в одном потоке.
