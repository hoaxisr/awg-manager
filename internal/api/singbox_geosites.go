package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/sys/appver"
)

// Каталог SagerNet sing-geosite: полный список geosite-наборов из ветки
// rule-set (~1.5 тыс. имён) для добавления remote rule-set'ов в один клик —
// дополнение к курируемому каталогу пресетов, не замена. Список берётся из
// GitHub tree API через маршрут загрузок пользователя (GitHub может быть
// доступен только через туннель) и кэшируется в памяти: лимит анонимного
// GitHub API — 60 запросов/час с IP, а список меняется редко.

const (
	geositeTreeAPIURL = "https://api.github.com/repos/SagerNet/sing-geosite/git/trees/rule-set"
	// GeositeRawURLBase — база raw-ссылок на .srs; отдаётся фронту, чтобы
	// URL добавляемых rule-set'ов строились в одном месте.
	geositeRawURLBase = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/"

	geositeCacheTTL     = 24 * time.Hour
	geositeFetchTimeout = 45 * time.Second
	// geositeMaxResponse ограничивает чтение ответа tree API: ~1.5 тыс.
	// записей занимают ~500 КБ, 20 МБ — щедрый потолок против мусорного
	// ответа промежуточного прокси.
	geositeMaxResponse = 20 << 20
)

const (
	// geositeFailCooldown — пауза между попытками после неудачного фетча:
	// без неё каждый запрос при протухшем кэше и недоступном GitHub жёг бы
	// анонимный rate-limit (60/час) и висел бы до geositeFetchTimeout.
	geositeFailCooldown = time.Minute
	// geositeAttemptDebounce гасит дубли refresh=1 (двойной клик, два
	// открытых каталога): попытка только что была — её результат и отдаём.
	geositeAttemptDebounce = 5 * time.Second
)

// SingboxGeositesData is the payload for GET /singbox/router/geosites/list.
type SingboxGeositesData struct {
	// Names — имена наборов без префикса geosite- и суффикса .srs
	// (например "youtube", "category-ads-all", "xiaomi@cn").
	Names []string `json:"names"`
	// BaseURL + "geosite-" + name + ".srs" — готовый URL для rule-set.
	BaseURL   string `json:"baseUrl" example:"https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/"`
	FetchedAt string `json:"fetchedAt" example:"2026-07-12T10:00:00Z"`
	// Stale — список отдан из кэша, потому что обновление не удалось
	// (GitHub недоступен). Фронт показывает предупреждение при refresh=1.
	Stale bool `json:"stale,omitempty"`
}

// SingboxGeositesHandler serves the SagerNet geosite catalog.
type SingboxGeositesHandler struct {
	downloadSvc *downloader.Service
	treeURL     string

	mu          sync.Mutex
	names       []string
	fetchedAt   time.Time
	lastAttempt time.Time
	lastErr     string
	// inflight != nil — фетч уже идёт; закрывается по завершении. Ожидают
	// его только запросы без кэша, остальные отдают stale немедленно.
	inflight chan struct{}
}

func NewSingboxGeositesHandler(downloadSvc *downloader.Service) *SingboxGeositesHandler {
	if downloadSvc == nil {
		downloadSvc = downloader.NewService(downloader.Deps{})
	}
	return &SingboxGeositesHandler{downloadSvc: downloadSvc, treeURL: geositeTreeAPIURL}
}

// List handles GET /api/singbox/router/geosites/list.
//
//	@Summary		List SagerNet sing-geosite catalog
//	@Description	Full list of geosite rule-set names from the SagerNet/sing-geosite rule-set branch, cached for 24h. Pass refresh=1 to force a re-fetch.
//	@Tags			singbox-router
//	@Produce		json
//	@Security		CookieAuth
//	@Param			refresh	query		int	false	"1 = force refresh"
//	@Success		200		{object}	OkResponse{data=SingboxGeositesData}
//	@Failure		502		{object}	APIErrorEnvelope	"listing fetch failed"
//	@Router			/singbox/router/geosites/list [get]
func (h *SingboxGeositesHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	force := r.URL.Query().Get("refresh") == "1"
	names, fetchedAt, stale, err := h.get(r, force)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusBadGateway,
			"не удалось получить список geosite: "+err.Error(), "GEOSITE_FETCH_FAILED")
		return
	}
	response.Success(w, SingboxGeositesData{
		Names:     names,
		BaseURL:   geositeRawURLBase,
		FetchedAt: fetchedAt.UTC().Format(time.RFC3339),
		Stale:     stale,
	})
}

// get возвращает кэшированный список или обновляет его. Фетч выполняется
// ВНЕ мьютекса (зависший GitHub не должен блокировать warm-cache чтения) и
// дедуплицируется каналом inflight: конкурентные промахи ждут один общий
// запрос вместо расходования анонимного rate-limit GitHub; запросы с живым
// кэшем во время чужого фетча немедленно получают stale-копию.
func (h *SingboxGeositesHandler) get(r *http.Request, force bool) (names []string, fetchedAt time.Time, stale bool, err error) {
	for {
		h.mu.Lock()
		fresh := h.names != nil && time.Since(h.fetchedAt) < geositeCacheTTL
		if fresh && !force {
			names, fetchedAt = h.names, h.fetchedAt
			h.mu.Unlock()
			return names, fetchedAt, false, nil
		}
		// Дебаунс: попытка (удачная или нет) была только что — отдаём её
		// результат, не плодя фетчи на двойной клик по «Обновить».
		if time.Since(h.lastAttempt) < geositeAttemptDebounce {
			return h.finishLocked()
		}
		// Пауза после неудачи: не жжём rate-limit на каждый запрос, пока
		// GitHub лежит. Явный refresh=1 паузу пробивает.
		if !force && h.lastErr != "" && time.Since(h.lastAttempt) < geositeFailCooldown {
			return h.finishLocked()
		}
		if h.inflight != nil {
			ch := h.inflight
			if h.names != nil {
				// stale-while-revalidate: чужой фетч уже идёт — не ждём его.
				names, fetchedAt = h.names, h.fetchedAt
				h.mu.Unlock()
				return names, fetchedAt, true, nil
			}
			h.mu.Unlock()
			<-ch
			continue
		}
		ch := make(chan struct{})
		h.inflight = ch
		h.mu.Unlock()

		fetched, ferr := h.fetch(r)

		h.mu.Lock()
		h.inflight = nil
		close(ch)
		h.lastAttempt = time.Now()
		if ferr != nil {
			h.lastErr = ferr.Error()
		} else {
			h.lastErr = ""
			h.names = fetched
			h.fetchedAt = time.Now()
		}
		return h.finishLocked()
	}
}

// finishLocked формирует ответ из текущего состояния кэша и отпускает
// мьютекс: свежий/протухший список — успех (stale по наличию lastErr),
// пустой кэш — последняя ошибка фетча.
func (h *SingboxGeositesHandler) finishLocked() ([]string, time.Time, bool, error) {
	defer h.mu.Unlock()
	if h.names != nil {
		return h.names, h.fetchedAt, h.lastErr != "", nil
	}
	err := h.lastErr
	if err == "" {
		err = "список ещё не загружен"
	}
	return nil, time.Time{}, false, fmt.Errorf("%s", err)
}

func (h *SingboxGeositesHandler) fetch(r *http.Request) ([]string, error) {
	// Отвязка от клиентского контекста: обрыв соединения (пользователь
	// закрыл модалку) не должен ронять фетч, за которым в очереди под
	// мьютексом могут ждать другие запросы.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), geositeFetchTimeout)
	defer cancel()

	lease, err := h.downloadSvc.ResolveClient(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("resolve download client: %w", err)
	}
	defer lease.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.treeURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", appver.UA())
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := lease.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body := io.LimitReader(resp.Body, geositeMaxResponse)
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(body, 300))
		return nil, fmt.Errorf("github tree api: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var tree struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(body).Decode(&tree); err != nil {
		return nil, fmt.Errorf("decode tree listing: %w", err)
	}
	if tree.Truncated {
		// Частичный листинг хуже ошибки: он закэшируется на сутки, и
		// пользователь молча не увидит часть каталога.
		return nil, fmt.Errorf("github tree api: listing truncated (%d entries)", len(tree.Tree))
	}

	names := make([]string, 0, len(tree.Tree))
	for _, entry := range tree.Tree {
		name, ok := strings.CutPrefix(entry.Path, "geosite-")
		if !ok {
			continue
		}
		name, ok = strings.CutSuffix(name, ".srs")
		if !ok || name == "" {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("listing came back empty (truncated=%v, entries=%d)", tree.Truncated, len(tree.Tree))
	}
	sort.Strings(names)
	return names, nil
}
