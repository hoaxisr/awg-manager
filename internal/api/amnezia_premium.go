package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/amneziacp"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Коды отказов ручек ключа подписки. Класс отказа определяется СЕНТИНЕЛОМ
// ошибки (см. cpFailure), а не текстом: по «зеркало лежит» пользователя
// отправляют повторить, по «ключ отклонён» — ввести другой ключ.
const (
	codePremiumNoKey              = "AMNEZIA_PREMIUM_NO_KEY"
	codePremiumKeyRejected        = "AMNEZIA_PREMIUM_KEY_REJECTED"
	codePremiumMirrorUnavailable  = "AMNEZIA_PREMIUM_MIRROR_UNAVAILABLE"
	codePremiumServiceUnavailable = "AMNEZIA_PREMIUM_UNAVAILABLE"
	codePremiumSettingsError      = "AMNEZIA_PREMIUM_SETTINGS_ERROR"
	codePremiumDeleteError        = "AMNEZIA_PREMIUM_DELETE_ERROR"
)

// logActionPremium — действие в журнале приложения; целью (target) идёт имя
// операции, как в internal/api/amnezia_cp.go.
const logActionPremium = "amnezia-premium"

// AmneziaPremiumKeyRequest — тело POST /amnezia/premium/key.
//
// Store и Remember — РАЗНЫЕ флаги с РАЗНЫМИ умолчаниями, и это не опечатка.
// Оба указателями: отличить «поля нет» от присланного false иначе нечем.
//
// Store — хранить ли ключ у НАС, умолчание false. Ключ подписки — секрет, и
// умолчание у секрета закрытое: положить его на флеш, когда об этом не
// просили явно, пользователь сам не отменит, а лишний повторный ввод ключа —
// отменит. Мастер шлёт флаг явно, так что умолчание достаётся только вызовам
// мимо него.
//
// Remember — срок cookie у ПОРТАЛА, умолчание true. Это не наш секрет, а
// длительность чужой сессии: отсутствие поля означает «как обычно», то есть
// долгую сессию, иначе пользователь получал бы ре-логин на каждом шаге.
type AmneziaPremiumKeyRequest struct {
	Key      string `json:"key" example:"vpn://..."`
	Store    *bool  `json:"store,omitempty" example:"false"`
	Remember *bool  `json:"remember,omitempty" example:"true"`
}

// AmneziaPremiumKeyData — состояние ключа подписки. Форма ОДНА на все три
// метода: состояние у ключа одно, и две формы ответа про него заставили бы
// интерфейс держать две ветки разбора. Сам ключ и сессия портала наружу не
// выходят ни в каком виде.
type AmneziaPremiumKeyData struct {
	// Stored — шифротекст ключа лежит в настройках.
	Stored bool `json:"stored" example:"true"`
	// Usable — сохранённый шифротекст расшифровывается секретом устройства.
	// Пара stored:false, usable:false читается как «ключа нет, расшифровывать
	// нечего», а не «ключ есть, но сломан»; сломанный ключ — это stored:true,
	// usable:false. Непригодный шифротекст НЕ стирается: пользователь видит
	// usable:false и вводит ключ заново, а секрет устройства ещё может
	// вернуться из бэкапа.
	Usable bool `json:"usable" example:"true"`
	// SaveError — почему сохранить не вышло; пусто, когда сохранять не просили
	// или сохранение прошло. Непустое значение вместе с успешным ответом POST
	// означает «вошли, но ключ не сохранён»: неудача сохранения не отменяет
	// состоявшийся вход. Без omitempty намеренно: поле, пропадающее из тела
	// там, где ошибки нет, — это и есть вторая форма ответа.
	SaveError string `json:"saveError"`
}

// AmneziaPremiumKeyResponse — конверт всех трёх методов /amnezia/premium/key.
type AmneziaPremiumKeyResponse struct {
	Success bool                  `json:"success" example:"true"`
	Data    AmneziaPremiumKeyData `json:"data"`
}

// AmneziaPremiumHandler — жизненный цикл ключа подписки Amnezia Premium:
// проверка ключа порталом, хранение шифротекста и его удаление.
//
// Ключ — единственный секрет аккаунта пользователя, и наружу он не выходит
// ни ответом, ни журналом, ни файлом настроек открытым текстом: на диск он
// едет зашифрованным DeviceCipher, то есть секретом, привязанным к установке.
type AmneziaPremiumHandler struct {
	settings *storage.SettingsStore
	cipher   *storage.DeviceCipher
	log      *logging.ScopedLogger
	bus      *events.Bus

	mu sync.Mutex
	// sessionKey — ключ режима «не запоминать»: живёт в памяти демона до
	// перезапуска. Без него ре-логин при протухшей сессии упирался бы в
	// ErrNoKey, и пользователь, отказавшийся хранить ключ у нас, терял бы
	// подписку на первом же протухшем sid.
	sessionKey string
	httpClient *http.Client
	cp         *amneziacp.Client
}

// NewAmneziaPremiumHandler собирает обработчик. appLogger может быть nil
// (тесты). Секрет устройства живёт рядом с settings.json — там же, где его
// ищет internal/backup.
func NewAmneziaPremiumHandler(settings *storage.SettingsStore, appLogger logging.AppLogger) *AmneziaPremiumHandler {
	return &AmneziaPremiumHandler{
		settings: settings,
		cipher:   storage.NewDeviceCipher(settings.DataDir()),
		log:      logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubDiagnostics),
	}
}

// SetEventBus подключает шину SSE; nil допустим (тесты).
func (h *AmneziaPremiumHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// SetHTTPClient подменяет транспорт к зеркалу и порталу. Шов для тестов:
// стенд на httptest.NewTLSServer отдаёт самоподписанный сертификат, и без
// своего клиента к нему не сходить. Уже собранный клиент CP сбрасывается —
// иначе он продолжил бы ходить прежним транспортом.
func (h *AmneziaPremiumHandler) SetHTTPClient(c *http.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.httpClient = c
	h.cp = nil
}

// client отдаёт клиента CP, собирая его при первом обращении.
//
// Собирается ОДИН раз и переиспользуется — это безопасно и сделано ради
// кэшей внутри клиента: там живут сессия портала и резолвнутый origin
// зеркала, и терять их на каждый запрос значит логиниться заново на каждое
// действие пользователя. Захвата настроек при сборке не происходит: адрес
// зеркала и ключ приходят в клиента ГЕТТЕРАМИ (mirrorURL, subscriptionKey),
// которые читают источник на каждом вызове. Кэши от смены настройки не
// протухают молча: кэш origin привязан к адресу зеркала (Mirror.Origin), а
// сессия — к паре «origin + отпечаток ключа» (Client.session), так что смена
// любого из двух промахивается мимо них сама.
func (h *AmneziaPremiumHandler) client() *amneziacp.Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cp == nil {
		h.cp = amneziacp.NewClient(h.httpClient, h.mirrorURL, h.subscriptionKey, h.logf)
	}
	return h.cp
}

// mirrorURL — ДЕЙСТВУЮЩИЙ адрес зеркала. Настройки читаются на КАЖДОМ
// вызове: смена адреса обязана доезжать без перезапуска панели. Правило
// «пусто ИЛИ негодно = зеркало по умолчанию» живёт одно на всех — в
// storage.EffectiveAmneziaMirrorURL; повторить здесь его условие значило бы
// завести второе понимание действующего адреса.
//
// Get(), а не Snapshot(): читается скалярное поле, а записи в стор
// публикуют НОВУЮ копию (SettingsStore.Update), то есть выданный указатель
// после публикации никто не правит. Snapshot тут гонял бы всё дерево
// настроек через JSON на каждый запрос к порталу.
func (h *AmneziaPremiumHandler) mirrorURL() string {
	cur, err := h.settings.Get()
	if err != nil {
		// Настройки не читаются — отдаём то же, что отдала бы функция на
		// пустом хранимом значении: зеркало по умолчанию. Пустая строка
		// здесь означала бы «зеркало не задано» и увела бы отказ в чужой
		// класс.
		return storage.EffectiveAmneziaMirrorURL("")
	}
	return storage.EffectiveAmneziaMirrorURL(cur.AmneziaPremiumMirrorURL)
}

// subscriptionKey — ключ для клиента CP: сперва сессионный, иначе
// расшифрованный сохранённый, иначе пусто (клиент ответит ErrNoKey).
//
// Расшифровка НЕ кэшируется: кэш пережил бы удаление ключа, и панель
// продолжила бы ходить в портал ключом, которого у неё уже нет.
func (h *AmneziaPremiumHandler) subscriptionKey() string {
	h.mu.Lock()
	sess := h.sessionKey
	h.mu.Unlock()
	if sess != "" {
		return sess
	}
	key, _, err := h.storedKey()
	if err != nil {
		return ""
	}
	return key
}

// storedKey отдаёт сохранённый ключ. stored — шифротекст в настройках есть,
// независимо от того, удалось ли его прочитать: «ключа нет» и «ключ не
// расшифровывается» — разные состояния, и схлопывать их нельзя, иначе
// непригодный шифротекст выглядел бы как отсутствие ключа и напрашивался на
// стирание.
func (h *AmneziaPremiumHandler) storedKey() (plain string, stored bool, err error) {
	cur, err := h.settings.Get()
	if err != nil {
		return "", false, err
	}
	token := strings.TrimSpace(cur.AmneziaPremiumKeyCipher)
	if token == "" {
		return "", false, nil
	}
	plain, err = h.cipher.Decrypt(token)
	if err != nil {
		return "", true, err
	}
	return plain, true, nil
}

// keyState — состояние сохранённого ключа в форме ответа. Сборщик один на
// все три метода: собери его в каждом по-своему — и методы начнут отвечать
// разное про одно и то же состояние. Ошибка чтения настроек отдаётся
// отдельно, а не полем ответа: это отказ ручки, а не состояние ключа.
func (h *AmneziaPremiumHandler) keyState() (AmneziaPremiumKeyData, error) {
	plain, stored, err := h.storedKey()
	return AmneziaPremiumKeyData{Stored: stored, Usable: stored && err == nil && plain != ""}, err
}

// logf — журнал клиента CP: его событие ложится целью записи, детали —
// сообщением. Ключа подписки и сессии в них нет по построению (клиент кладёт
// туда адрес, метод, путь и код ответа).
func (h *AmneziaPremiumHandler) logf(event, detail string) {
	h.log.Info(logActionPremium, event, detail)
}

// SaveKey проверяет присланный ключ входом в портал и, если просили,
// сохраняет его зашифрованным.
//
//	@Summary		Проверить и сохранить ключ подписки Amnezia Premium
//	@Description	Проверяет ключ входом в портал Amnezia. При store=true сохраняет его зашифрованным секретом устройства; без поля store ключ не сохраняется. Ключ и сессия портала в ответе не возвращаются.
//	@Tags			amnezia-premium
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		AmneziaPremiumKeyRequest	true	"Ключ подписки, флаг сохранения и remember для портала"
//	@Success		200		{object}	AmneziaPremiumKeyResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		422		{object}	APIErrorEnvelope
//	@Failure		502		{object}	APIErrorEnvelope
//	@Failure		503		{object}	APIErrorEnvelope
//	@Router			/amnezia/premium/key [post]
func (h *AmneziaPremiumHandler) SaveKey(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[AmneziaPremiumKeyRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Ключ подписки Amnezia не указан", codePremiumNoKey)
		return
	}
	// Умолчания РАЗНЫЕ: remember уходит в портал и задаёт срок ЕГО cookie,
	// store решает судьбу НАШЕГО секрета и потому закрыт по умолчанию —
	// см. комментарий у AmneziaPremiumKeyRequest.
	remember := req.Remember == nil || *req.Remember
	store := req.Store != nil && *req.Store

	// Контекст запроса уезжает в портал: закрытая пользователем вкладка
	// обязана отменять поход наружу, а не висеть до таймаута клиента.
	if err := h.client().CheckKey(r.Context(), key, remember); err != nil {
		h.failCP(w, "key-check", err)
		return
	}

	// Ключ живёт в памяти демона независимо от того, сохранён ли он: иначе
	// режим «не запоминать» ломается на первом же ре-логине, а при неудаче
	// сохранения пользователь оставался бы с работающей сессией и без ключа.
	h.mu.Lock()
	h.sessionKey = key
	h.mu.Unlock()

	var saveErr string
	if store {
		if err := h.persistKey(key); err != nil {
			// Вход состоялся: отказ здесь — не отказ всего вызова, иначе
			// пользователь увидит «не вышло» после успешной проверки ключа.
			h.log.Warn(logActionPremium, "key-save", "route=direct сохранить ключ подписки не удалось: "+err.Error())
			saveErr = saveErrorMessage(err)
		} else {
			h.bus.PublishInvalidated(events.ResourceAmneziaPremiumKey, "saved")
		}
	}

	// Состояние читается заново, а не выводится из исхода сохранения: при
	// store=false в настройках может лежать ключ с прошлого раза, и POST
	// обязан сказать про него то же, что скажет GET.
	out, err := h.keyState()
	if err != nil {
		// Состояние не прочиталось — отдаём закрытое stored=false/usable=false
		// вместе с состоявшимся входом, а не роняем весь вызов.
		h.log.Warn(logActionPremium, "key-check", "route=direct состояние сохранённого ключа не прочитано: "+err.Error())
	}
	out.SaveError = saveErr
	h.log.Info(logActionPremium, "key-check", fmt.Sprintf(
		"route=direct remember=%v store=%v stored=%v", remember, store, out.Stored))
	response.Success(w, out)
}

// KeyStatus отдаёт состояние сохранённого ключа.
//
//	@Summary		Состояние ключа подписки Amnezia Premium
//	@Description	stored — сохранённый шифротекст есть; usable — он расшифровывается секретом устройства. Сам ключ не возвращается.
//	@Tags			amnezia-premium
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	AmneziaPremiumKeyResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/amnezia/premium/key [get]
func (h *AmneziaPremiumHandler) KeyStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	data, err := h.keyState()
	if err != nil && !data.Stored {
		// Настройки не прочитались: отказ закрытый. Пустой ответ здесь
		// интерфейс показал бы как «ключа нет» и предложил бы ввести новый.
		h.log.Warn(logActionPremium, "key-status", "route=direct настройки не прочитаны: "+err.Error())
		response.ErrorWithStatus(w, http.StatusInternalServerError, "Не удалось прочитать настройки", codePremiumSettingsError)
		return
	}
	if data.Stored && err != nil {
		// Шифротекст остаётся на месте (решение Р1). В журнал уходит, чем
		// именно он забракован: «секрета устройства нет» ещё лечится
		// возвратом файла из бэкапа, «не расшифровывается» — уже нет.
		h.log.Warn(logActionPremium, "key-status", "route=direct сохранённый ключ непригоден: "+err.Error())
	}
	response.Success(w, data)
}

// DeleteKey забывает ключ: и сохранённый, и сессионный.
//
//	@Summary		Удалить ключ подписки Amnezia Premium
//	@Description	Стирает сохранённый шифротекст, забывает ключ в памяти демона и роняет сессию портала.
//	@Tags			amnezia-premium
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	AmneziaPremiumKeyResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/amnezia/premium/key [delete]
func (h *AmneziaPremiumHandler) DeleteKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	// Память забывается ДО записи: если запись не удастся, у нас останется
	// меньше секрета, а не больше. Клиент здесь не собирается ради сброса
	// сессии, которой может и не быть.
	h.mu.Lock()
	h.sessionKey = ""
	cp := h.cp
	h.mu.Unlock()
	if cp != nil {
		cp.ResetSession()
	}

	if err := h.settings.Update(func(cur *storage.Settings) error {
		cur.AmneziaPremiumKeyCipher = ""
		return nil
	}); err != nil {
		h.log.Warn(logActionPremium, "key-delete", "route=direct удалить ключ подписки не удалось: "+err.Error())
		response.ErrorWithStatus(w, http.StatusInternalServerError, "Не удалось удалить ключ подписки", codePremiumDeleteError)
		return
	}
	h.log.Info(logActionPremium, "key-delete", "route=direct ключ подписки удалён")
	h.bus.PublishInvalidated(events.ResourceAmneziaPremiumKey, "deleted")
	// Ключа больше нет: ровно та же форма ответа, что у POST и GET.
	response.Success(w, AmneziaPremiumKeyData{})
}

// persistKey шифрует ключ и кладёт шифротекст в настройки.
//
// Шифрование идёт ДО SettingsStore.Update: мутатор исполняется под локом
// стора и обязан быть чистым и быстрым, а Encrypt ходит на диск за секретом
// устройства (и при первом вызове заводит его).
func (h *AmneziaPremiumHandler) persistKey(key string) error {
	token, err := h.cipher.Encrypt(key)
	if err != nil {
		return err
	}
	return h.settings.Update(func(cur *storage.Settings) error {
		cur.AmneziaPremiumKeyCipher = token
		return nil
	})
}

// saveErrorMessage — что сказать пользователю о неудаче сохранения. Текст
// чужой ошибки наружу не идёт: в ответ уходит фиксированная строка, а
// причина целиком уже записана в журнал.
func saveErrorMessage(err error) string {
	if errors.Is(err, storage.ErrDeviceKeyMissing) {
		// Секрета устройства нет и завести его не вышло — чинится местом на
		// флеше и файлом .device-key, а не другим ключом подписки.
		return "Ключ проверен, но секрет устройства недоступен — сохранить ключ не удалось"
	}
	return "Ключ проверен, но сохранить его не удалось"
}

// failCP отвечает на отказ похода в портал. Наружу — фиксированный текст и
// наш код; причина целиком уходит в журнал, где её ищут при разборе жалобы.
func (h *AmneziaPremiumHandler) failCP(w http.ResponseWriter, event string, err error) {
	status, code, msg := cpFailure(err)
	h.log.Warn(logActionPremium, event, fmt.Sprintf("route=direct code=%s: %v", code, err))
	response.ErrorWithStatus(w, status, msg, code)
}

// cpFailure переводит отказ клиента CP в наш ответ. Разбор идёт по
// СЕНТИНЕЛАМ, и порядок значим: ErrMirrorUnavailable, дойдя до вызывающего,
// обёрнут в ErrServiceUnavailable — общая ветка обязана быть последней.
//
// Ветки под amneziacp.ErrMirrorNotConfigured здесь нет: адрес зеркала
// приходит из storage.EffectiveAmneziaMirrorURL, а она пустого не отдаёт, так
// что этот сентинел до нас не доходит.
//
// Статус портала наружу не транслируется: 401 от CP, отданный наружу как
// 401, разлогинил бы панель. Отклонённый ключ — 422, как и у прежних ручек.
func cpFailure(err error) (status int, code, message string) {
	switch {
	case errors.Is(err, amneziacp.ErrKeyRejected):
		return http.StatusUnprocessableEntity, codePremiumKeyRejected, "Портал Amnezia отклонил ключ подписки"
	case errors.Is(err, amneziacp.ErrNoKey):
		return http.StatusBadRequest, codePremiumNoKey, "Ключ подписки Amnezia не задан"
	case errors.Is(err, amneziacp.ErrMirrorUnavailable):
		return http.StatusBadGateway, codePremiumMirrorUnavailable, "Зеркало Amnezia недоступно — попробуйте позже"
	default:
		// ErrServiceUnavailable и всё, что не опознано: отказ закрытый.
		return http.StatusServiceUnavailable, codePremiumServiceUnavailable, "Сервис Amnezia недоступен — попробуйте позже"
	}
}
