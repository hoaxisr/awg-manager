# Contributing

## Структура проекта

```
awg-manager/
├── cmd/awg-manager/       # точка входа бекенда, docs.go (swag директивы)
├── cmd/mcp-dev/           # dev-only MCP-сервер на фейковых данных (mock stack)
├── internal/
│   ├── api/               # HTTP-хендлеры — здесь живут OpenAPI аннотации
│   ├── mcp/               # MCP-сервер: инструменты, localdeps, mcptest
│   ├── openapi/           # embed.go + swagger.yaml (АВТОГЕНЕРАТ — не редактировать руками)
│   └── ...                # остальная бизнес-логика
├── frontend/
│   ├── src/               # SvelteKit-приложение
│   ├── scripts/           # mock-сервер, прокси, генераторы иконок
│   └── static/            # openapi.yaml сюда копируется при dev:mock (gitignored)
├── scripts/               # сборочные shell-скрипты
└── openapi.md             # подробный гайд по OpenAPI/Swagger
```

## Окружение разработки

### Бекенд

Требуется Go 1.23. Для запуска бекенда локально — стандартный `go run`:

```bash
go run ./cmd/awg-manager
```

Бекенд слушает порт **8080**.

### Фронтенд

```bash
cd frontend
npm install
npm run dev        # dev-сервер с проксированием на бекенд (порт 8080)
```

Swagger UI доступен по `/dev/api-docs` при запущенном бекенде и dev-сервере.

Если хочется работать с реальным устройством без локального бекенда, можно натравить dev-сервер прямо на роутер — достаточно указать его адрес через переменную окружения:

```bash
cd frontend
VITE_API_TARGET=http://192.168.1.1 npm run dev
```

Все запросы `/api/*` будут проксироваться на роутер, а фронтенд — подхватывать изменения на лету.

## OpenAPI / Swagger

> Подробный гайд — в [`openapi.md`](./openapi.md)

### Главное правило

**Swagger-файл генерируется автоматически из Go-аннотаций. Не редактируй `internal/openapi/swagger.yaml` руками** — изменения будут перезаписаны при следующем `go generate`.

Если ИИ-инструмент предлагает напрямую отредактировать `swagger.yaml` — отклоняй: файл строго автогенерат, правильный путь — аннотации в хендлерах.

### Что делать при добавлении или изменении API

1. Добавляй/обновляй swagger-аннотации над хендлером в `internal/api/*`:

```go
// GetFoo godoc
// @Summary      Краткое описание
// @Tags         foo
// @Produce      json
// @Security     CookieAuth
// @Success      200 {object} FooResponse
// @Failure      500 {object} response.ErrorResponse
// @Router       /foo [get]
func (h *FooHandler) GetFoo(w http.ResponseWriter, r *http.Request) { ... }
```

2. В конце работы над фичей (перед коммитом) — пересобирай спеку:

```bash
# В корне репозитория
go generate ./cmd/awg-manager
```

Команда перезаписывает `internal/openapi/swagger.yaml`. Коммить обновлённый файл вместе с изменениями хендлеров.

3. Без аннотаций новый эндпоинт **не попадёт в мок-сервер** и фронтенд не сможет с ним работать в dev:mock режиме.

## Mock-сервер

Для разработки фронтенда без запущенного бекенда используется Prism, который поднимает мок-сервер на основе OpenAPI-спеки.

```bash
cd frontend
npm run dev:mock   # синхронизирует swagger.yaml и запускает Vite + Prism
```

### Важный момент: мок работает только по спеке

Prism отдаёт ответы строго по тому, что описано в `swagger.yaml`. Если эндпоинт не аннотирован или аннотации устарели — он либо не замокается вообще, либо вернёт неверную схему.

**Пример из практики:** фича с параметрами кинетика не мокалась в настройках именно потому, что аннотации отсутствовали. Не повторяй эту ошибку — аннотируй всё, что добавляешь.

### Stateful mock proxy

Если нужен stateful-мок (например, сохранение состояния между запросами), используй `mock-proxy.mjs`:

```bash
# Терминал A — Prism
cd frontend && npm run mock

# Терминал B — stateful прокси
node frontend/scripts/mock-proxy.mjs

# Терминал C — Vite через прокси
cd frontend && VITE_API_TARGET=http://127.0.0.1:8081 npm run dev:mock
```

Подробнее — в [`openapi.md`](./openapi.md#6-stateful-mock-proxy-state-aware-overrides).

### MCP в mock-режиме

`npm run dev:mock:proxy` дополнительно поднимает `go run ../cmd/mcp-dev` на `http://127.0.0.1:8090/mcp` (нужен Go; отключить — `MOCK_MCP=0`). Подключение: `claude mcp add --transport http awgm-dev http://127.0.0.1:8090/mcp`. Данные — фейковые (`internal/mcp/mcptest`), карточка ключей в UI работает через `mock-proxy.mjs` и с dev-сервером не связана.

## Процесс работы над фичей

1. Реализуй хендлеры в `internal/api/`.
2. Добавь swagger-аннотации ко всем новым и изменённым эндпоинтам, а так же типизируй DTO.
3. Запусти `go generate ./cmd/awg-manager` — убедись, что спека обновилась без ошибок.
4. Проверь фронт в dev:mock режиме — убедись, что мок работает корректно.
5. Закоммить `internal/openapi/swagger.yaml` вместе с остальными изменениями.

## Сборка

IPK-пакет для Entware (Keenetic):

```bash
./scripts/build-ipk.sh [VERSION] [ARCH]
# Поддерживаемые архитектуры: mipsel-3.4, mips-3.4, aarch64-3.10
```

Только бекенд (кросс-компиляция):

```bash
./scripts/build-backend.sh
```

Только фронтенд:

```bash
./scripts/build-frontend.sh
```

## Тестирование

Команды ниже одинаково работают на macOS, Linux и Windows — нужен только Docker (Desktop или Engine) с Compose v2.

### Backend (Go)

Большая часть backend-тестов завязана на Linux (ndms, iptables, sing-box и т.д.). Локально их гоняют в Linux-контейнере из `Dockerfile.go-test`.

**Обёртка (удобнее всего):**

```bash
./scripts/dev/go-test-docker.sh start
./scripts/dev/go-test-docker.sh run ./internal/managed
./scripts/dev/go-test-docker.sh run ./internal/managed -run TestService_Create
./scripts/dev/go-test-docker.sh full
./scripts/dev/go-test-docker.sh coverage
./scripts/dev/go-test-docker.sh stop
```

**Или напрямую через docker compose:**

```bash
docker compose -f docker-compose.go-test.yml up -d runner
docker compose -f docker-compose.go-test.yml exec runner go test -count=1 ./internal/managed
docker compose -f docker-compose.go-test.yml down
```

**Одноразовый прогон** (без постоянного контейнера):

```bash
docker compose -f docker-compose.go-test.yml --profile oneshot run --rm test ./internal/managed
```

Кэши Go (`go-build`, `go-mod`) сохраняются в `.cache/docker-go-test/` между запусками.

**Как прогонять во время разработки:**

1. Сначала точечные тесты только по изменённым пакетам (`run ./internal/...`).
2. Полный `./...` — один раз перед PR, когда точечные уже зелёные.
3. Всегда с `-count=1`, чтобы не получить ложноположительный результат из test-cache (обёртка добавляет флаг сама).

### Frontend

```bash
cd frontend
npm install   # если зависимости ещё не ставили
npm test
```

Watch-режим для отладки: `npx vitest`.

### CI

В GitHub Actions (ubuntu-latest) те же проверки: `go test ./...` для backend и `npm test` для frontend. Локальный `full` перед PR должен проходить так же, как CI.

## Pull Requests

- Ветки от `develop`, PR — в `develop`.
- В репозитории используется **fast-forward merge** — перед мержем нужно отребейзить ветку на актуальный `develop`:

```bash
git fetch origin
git rebase origin/develop
```

- В описании PR кратко опиши что изменилось и зачем.
- Если добавлял/менял API — убедись, что `swagger.yaml` обновлён и закоммичен.
- Перед PR прогони тесты: `./scripts/dev/go-test-docker.sh full` (backend) и `npm test` во `frontend/` (если менялся соответствующий код).
- Старайся не смешивать рефакторинг и новую функциональность в одном PR.

## Conventional Commits

Проект придерживается [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/).

Формат: `<type>(<scope>): <description>`

Примеры:

```
feat(api): add kinetic parameters endpoint
fix(singbox): correct config merge on update
chore: run go generate, update swagger.yaml
refactor(tunnel): extract WAN detection logic
docs: add CONTRIBUTING.md
```

Распространённые типы: `feat`, `fix`, `refactor`, `chore`, `docs`, `test`, `perf`, `ci`.

**Требование к PR:** выполни одно из двух условий:

- **Все коммиты в ветке** соответствуют конвенции — тогда мержится as-is.
- **Название PR** соответствует конвенции — тогда ставишь squash и итоговый коммит будет по конвенции.
