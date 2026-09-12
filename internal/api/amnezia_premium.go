package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	codePremiumStateChanged       = "AMNEZIA_PREMIUM_STATE_CHANGED"
	codePremiumMirrorUnavailable  = "AMNEZIA_PREMIUM_MIRROR_UNAVAILABLE"
	codePremiumServiceUnavailable = "AMNEZIA_PREMIUM_UNAVAILABLE"
	codePremiumSettingsError      = "AMNEZIA_PREMIUM_SETTINGS_ERROR"
	codePremiumDeleteError        = "AMNEZIA_PREMIUM_DELETE_ERROR"
	codePremiumNoCountry          = "AMNEZIA_PREMIUM_NO_COUNTRY"
	codePremiumBadCountry         = "AMNEZIA_PREMIUM_BAD_COUNTRY"
	codePremiumConfigBusy         = "AMNEZIA_PREMIUM_CONFIG_BUSY"
	// codePremiumForbidden — портал ответил 403: операцию он запретил. Свой
	// код, а не codePremiumKeyRejected: «ключ отклонён» зовёт человека ввести
	// другой ключ, а самое вероятное живое значение 403 — исчерпанный лимит
	// устройств подписки, где рабочий ключ менять не надо.
	codePremiumForbidden = "AMNEZIA_PREMIUM_FORBIDDEN"
	// codePremiumOutcomeUnknown — расходный запрос до портала дошёл, а исход
	// его неизвестен: перенаправление, обрыв связи после отправки, непригодный
	// ответ. Свой код, потому что это единственный класс отказа линии, на
	// котором повторять НЕЛЬЗЯ: повтор потратит второй слот подписки.
	codePremiumOutcomeUnknown = "AMNEZIA_PREMIUM_OUTCOME_UNKNOWN"
	// codeInvalidAmneziaMirrorURL — тот же код, которым непригодный адрес
	// зеркала отвергали настройки, пока поле принадлежало им: класс отказа не
	// изменился, менять код значило бы ломать клиента ради переезда ручки.
	codeInvalidAmneziaMirrorURL = "INVALID_AMNEZIA_MIRROR_URL"
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
	// keyGen — поколение состояния ключа: растёт на КАЖДОМ удалении, было ли
	// что удалять или нет. Поколение выражает НАМЕРЕНИЕ пользователя «ключа у
	// меня быть не должно», а не факт смены байтов на диске или в памяти
	// (см. DeleteKey).
	//
	// SaveKey снимает поколение ДО похода в портал и сверяет на возврате,
	// потому что поход длится до таймаута клиента: без сверки DELETE,
	// пришедший в это окно, молча отменялся бы вернувшимся SaveKey — тот
	// безусловно вернул бы ключ и в память, и на флеш. «Забудь мой секрет»
	// обязано побеждать.
	//
	// Сохранения поколение НЕ двигают: гейт стережёт удаление, а не очередь
	// сохранений. Два сохранения одного и того же ключа — это двойной клик по
	// «Сохранить», а не отмена чужой команды, и ведут они себя как всякая
	// запись настроек: побеждает вернувшееся последним.
	keyGen     uint64
	httpClient *http.Client
	cp         *amneziacp.Client
	// configInFlight — страны, по которым выдача конфигурации сейчас летит.
	// Замок расходной операции: она тратит слот устройств подписки, и второй
	// запрос той же страны обязан получить отказ, а не уйти в портал вторым.
	// Ключ — код страны в нижнем регистре, тот же, что уезжает в портал.
	//
	// Это правило НЕЗАВИСИМО от поколения ключа выше: там гейт «удаление
	// побеждает летящее сохранение», здесь — «расходная операция не идёт
	// дважды». Общий у них только лок, и он никогда не удерживается на время
	// похода в сеть.
	configInFlight map[string]struct{}
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

// Key — единственная точка входа ручки /amnezia/premium/key: метод выбирает
// операцию. Разбор метода живёт здесь, а не в закрытии регистрации маршрута,
// ровно ради чужого метода: отказ обязан приехать тем же конвертом API
// (response.MethodNotAllowed), что и отказы самих операций, иначе фронт
// получает на одном пути то JSON, то текст. Ср. AccessPolicyHandler.PermitInterface.
func (h *AmneziaPremiumHandler) Key(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.KeyStatus(w, r)
	case http.MethodPost:
		h.SaveKey(w, r)
	case http.MethodDelete:
		h.DeleteKey(w, r)
	default:
		response.MethodNotAllowed(w)
	}
}

// resetPortalSession выбрасывает сессию портала, если клиент уже собран.
// Зовётся отовсюду, где ключ перестаёт быть нашим, — из удаления и из
// отменённой ветки SaveKey: сессия добыта ключом и жить дольше него не имеет
// права. Сброс грубый, на весь клиент, и может задеть сессию более новую, чем
// наша; цена этому — лишний ре-логин, и она меньше, чем живая сессия ключа,
// который у нас забрали. Клиент ради сброса не собирается — сбрасывать тогда
// нечего.
func (h *AmneziaPremiumHandler) resetPortalSession() {
	h.mu.Lock()
	cp := h.cp
	h.mu.Unlock()
	if cp != nil {
		cp.ResetSession()
	}
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
//	@Failure		403		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
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

	// Поколение снимается ДО похода в портал: всё время похода состояние
	// ключа принадлежит не нам, и пользователь волен его сменить.
	gen := h.keyGeneration()

	// Контекст запроса уезжает в портал: закрытая пользователем вкладка
	// обязана отменять поход наружу, а не висеть до таймаута клиента.
	if err := h.client().CheckKey(r.Context(), key, remember); err != nil {
		h.failCP(w, "key-check", err)
		return
	}

	cancelled, saveErr := h.commitKey(key, gen, store)
	if cancelled {
		// Вход состоялся, но записывать его результат некуда: состояние ключа
		// сбросили, пока мы ходили в портал. Отвечать успехом здесь значило бы
		// сказать «ключ принят» про ключ, которого у нас нет ни в памяти, ни
		// на диске.
		//
		// Сессию портала при этом роняем. CheckKey делает adopt ВНУТРИ себя,
		// прямо перед возвратом (internal/amneziacp.Client.CheckKey), так что в
		// кэше клиента сейчас лежит именно НАША сессия — та, что уже вытеснила
		// всё, что могло там оказаться, пока мы висели в портале. Оставить её
		// значит оставить живой сессию ключа, который у нас забрали.
		//
		// Сброс возможен только грубый, на весь клиент (прицельного нет:
		// CheckKey не отдаёт наружу идентификатор добытой сессии, ср.
		// amneziacp.dropSession), и он может задеть сессию более новую, чем
		// наша, — заведённую сохранением, прошедшим рядом. Это стоит лишнего
		// ре-логина; живая сессия забранного ключа стоит дороже.
		h.resetPortalSession()
		h.log.Info(logActionPremium, "key-check", "route=direct состояние ключа сменилось за время проверки — ключ не сохранён")
		response.ErrorWithStatus(w, http.StatusConflict,
			"Состояние ключа подписки изменилось, пока шла проверка — введите ключ заново", codePremiumStateChanged)
		return
	}

	var saveErrMsg string
	switch {
	case saveErr != nil:
		// Вход состоялся: отказ здесь — не отказ всего вызова, иначе
		// пользователь увидит «не вышло» после успешной проверки ключа.
		h.log.Warn(logActionPremium, "key-save", "route=direct сохранить ключ подписки не удалось: "+saveErr.Error())
		saveErrMsg = saveErrorMessage(saveErr)
	case store:
		h.bus.PublishInvalidated(events.ResourceAmneziaPremiumKey, "saved")
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
	out.SaveError = saveErrMsg
	h.log.Info(logActionPremium, "key-check", fmt.Sprintf(
		"route=direct remember=%v store=%v stored=%v", remember, store, out.Stored))
	response.Success(w, out)
}

// keyGeneration — поколение состояния ключа на сейчас.
func (h *AmneziaPremiumHandler) keyGeneration() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.keyGen
}

// commitKey — единственная точка записи проверенного ключа. Пишет его в память
// и, если просили, на диск, но ТОЛЬКО когда состояние ключа не сбрасывали за
// время похода в портал: cancelled=true означает «у нас этот ключ уже забрали»,
// и тогда не пишется ничего — ни в память, ни на диск.
//
// Сверка поколения и обе записи идут в ОДНОЙ критической секции: разнеси их —
// и DELETE снова встраивается между сверкой и записью, только окно станет уже,
// а класс отказа останется. Поход в портал внутрь не попадает, он уже позади;
// на время persistKey (шифрование + запись настроек) лок держится — это
// доли секунды против сорока пяти секунд похода наружу.
//
// Поколение здесь НЕ двигается: двигать его на каждой записи значит отвечать
// 409 «введите ключ заново» второму из двух одновременных сохранений, то есть
// показывать ошибку поверх успешно сохранённого ключа на двойной клик по
// «Сохранить». Сохранения друг друга не отменяют — побеждает вернувшееся
// последним; гейт стережёт удаление (см. keyGen).
//
// saveErr — неудача сохранения на диск; вход при этом состоялся, и ключ
// остаётся в памяти демона: иначе режим «не запоминать» ломается на первом же
// ре-логине, а при неудаче сохранения пользователь остался бы с работающей
// сессией и без ключа.
func (h *AmneziaPremiumHandler) commitKey(key string, gen uint64, store bool) (cancelled bool, saveErr error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.keyGen != gen {
		return true, nil
	}
	h.sessionKey = key
	if !store {
		return false, nil
	}
	return false, h.persistKey(key)
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
	// Забвение памяти и стирание шифротекста идут ПОД ОДНИМ захватом — тем же,
	// под которым пишет commitKey. Вынеси стирание наружу — и летящий SaveKey
	// встраивается между стиранием и сдвигом поколения: поколение он снимет
	// ещё прежним, гейт его пропустит, он запишет ключ в память и на флеш,
	// поколение сдвинется уже после — и удаление ответит «ключ удалён» про
	// ключ, который лежит на диске. Под одним захватом такого промежутка нет:
	// сохранение проходит либо целиком до удаления, либо целиком после — и
	// упирается в сдвинутое поколение.
	//
	// Память забывается ДО записи: если запись не удастся, у нас останется
	// меньше секрета, а не больше.
	h.mu.Lock()
	h.sessionKey = ""
	// Умолчание пессимистичное: мутатор исполняется не всегда (Update отказывает
	// до него, когда не сумел загрузить кэш), и тогда про шифротекст мы ничего
	// не знаем — считаем, что он был, и отвечаем отказом.
	hadCipher := true
	err := h.settings.Update(func(cur *storage.Settings) error {
		hadCipher = strings.TrimSpace(cur.AmneziaPremiumKeyCipher) != ""
		cur.AmneziaPremiumKeyCipher = ""
		return nil
	})
	// Поколение двигает КАЖДОЕ удаление — и то, которому было что удалять, и
	// то, которому не было. Поколение выражает НАМЕРЕНИЕ пользователя «ключа у
	// меня быть не должно», а не факт смены байтов: сохранение, начатое ДО
	// удаления, обязано быть отменено, даже если стирать было нечего, — человек
	// уже сказал, чего хочет, и молча вернуть ему ключ нельзя. Сохранение,
	// начатое ПОСЛЕ удаления, снимет уже сдвинутое поколение и не заденется.
	//
	// Это правило и правило кода ответа ниже НЕЗАВИСИМЫ, склеивать их нельзя.
	// Там исход считается по ДОСТИГНУТОМУ состоянию: отказ только когда
	// шифротекст пережил запись. Здесь — по намерению, высказанному запросом.
	// Вывести одно из другого пробовали дважды, и оба раза это стоило дефекта:
	// «двигать по успеху записи» открывало летящему сохранению дверь на отказе
	// записи, «двигать по факту смены состояния» теряло удаление, пришедшее на
	// пустом состоянии.
	//
	// Сохранения поколение по-прежнему НЕ двигают: ложный 409 на двойной клик
	// по «Сохранить» шёл именно оттуда (см. commitKey).
	h.keyGen++
	h.mu.Unlock()

	// Сессия портала роняется ВНЕ захвата: клиент CP берёт на сбросе свой лок,
	// а его геттеры ходят за нашим (subscriptionKey) — звать его под h.mu
	// значило бы завести порядок двух локов там, где его больше нигде нет.
	// Роняется и на неудаче стирания: сессия — не то, что стоит беречь, когда
	// ключ уже забыт в памяти.
	h.resetPortalSession()

	if err != nil && hadCipher {
		// Шифротекст пережил запись: состояния, которого просил пользователь,
		// мы не достигли.
		h.log.Warn(logActionPremium, "key-delete", "route=direct удалить ключ подписки не удалось: "+err.Error())
		response.ErrorWithStatus(w, http.StatusInternalServerError, "Не удалось удалить ключ подписки", codePremiumDeleteError)
		return
	}
	if err != nil {
		// Записать не вышло, но стирать было нечего: исход операции определяет
		// ДОСТИГНУТОЕ состояние, а не то, дошли ли мы до файла. Ключа нет ни в
		// памяти, ни на диске — это ровно то, чего просили, и отказ здесь гнал
		// бы пользователя повторять удавшееся удаление.
		h.log.Warn(logActionPremium, "key-delete", "route=direct настройки не записались, но стирать было нечего: "+err.Error())
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

// premiumCheckDeviceCount — хвост отказа расходной операции, исход которой у
// портала неизвестен. Совет один на все дороги сюда (перенаправление, обрыв
// после отправки, непригодный ответ), потому что и делать пользователю везде
// надо одно: посмотреть, что у портала, а не жать кнопку заново. Звать
// «проверьте, прежде чем запрашивать снова» нельзя — это приглашение ровно к
// тому повтору, который тратит второй слот подписки (F200).
const premiumCheckDeviceCount = "Откройте список стран и проверьте счётчик устройств подписки."

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
// Наш 403 на ErrForbidden со статусом портала совпал, но не переписан с него:
// класс отказа выбирает сентинел.
func cpFailure(err error) (status int, code, message string) {
	switch {
	case errors.Is(err, amneziacp.ErrKeyRejected):
		return http.StatusUnprocessableEntity, codePremiumKeyRejected, "Портал Amnezia отклонил ключ подписки"
	case errors.Is(err, amneziacp.ErrForbidden):
		// Про ключ в этом тексте не говорится сознательно: 403 — это запрет
		// операции, а не приговор ключу, и звать заменить рабочий ключ по
		// исчерпанному лимиту устройств нельзя.
		return http.StatusForbidden, codePremiumForbidden,
			"Портал Amnezia запретил операцию — возможно, исчерпан лимит устройств подписки"
	case errors.Is(err, amneziacp.ErrNoKey):
		return http.StatusBadRequest, codePremiumNoKey, "Ключ подписки Amnezia не задан"
	case errors.Is(err, amneziacp.ErrOutcomeUnknown):
		// Единственный отказ линии, где текст НЕ зовёт повторить: расходный
		// запрос до портала дошёл, слот подписки мог быть списан, и повтор
		// потратит второй (F200).
		//
		// Текст не утверждает ни списания, ни его отсутствия. Утверждать
		// нечем: «портал ответил успехом» доказывает только то, что 200
		// ответил КТО-ТО — интерстишл WAF перед CP или любой хост, поднятый по
		// адресу зеркала, которое задаёт сам пользователь.
		return http.StatusBadGateway, codePremiumOutcomeUnknown,
			"Портал Amnezia не подтвердил выдачу конфигурации — выдана она или нет, неизвестно. " +
				premiumCheckDeviceCount
	case errors.Is(err, amneziacp.ErrMirrorUnavailable):
		return http.StatusBadGateway, codePremiumMirrorUnavailable, "Зеркало Amnezia недоступно — попробуйте позже"
	default:
		// ErrServiceUnavailable и всё, что не опознано: отказ закрытый.
		return http.StatusServiceUnavailable, codePremiumServiceUnavailable, "Сервис Amnezia недоступен — попробуйте позже"
	}
}

// === Каталог подписки, выдача конфигурации, адрес зеркала ===

// AmneziaPremiumCountry — страна каталога подписки.
//
// Имена полей НАШИ (camelCase), а не портальные: ответ собирается полем за
// полем, и совпадение имён с чужим ответом создавало бы впечатление, что он
// пересылается как есть.
type AmneziaPremiumCountry struct {
	// Code — код страны, как его прислал портал (server_country_code).
	// Регистр не трогаем: он же уезжает обратно в запрос конфигурации, где
	// приводится к нижнему уже клиентом.
	Code string `json:"code" example:"nl"`
	// Name — название страны, как его прислал портал, вместе с суффиксами
	// вида «Switzerland [P2P]».
	Name string `json:"name" example:"Netherlands"`
	// Protocols — признак доступности страны: список протоколов, которыми её
	// отдаёт подписка (available_protocols). Нам годится только awg, но
	// решение «показывать ли страну» принимает интерфейс — здесь важно
	// сохранить РАЗЛИЧИЕ между пустым списком (страна не отдаётся ничем) и
	// отсутствующим полем (старый ответ портала его не содержал, и страну
	// отбрасывать нельзя). Поэтому без omitempty: nil уезжает как null,
	// пустой список — как [].
	Protocols []string `json:"protocols"`
}

// AmneziaPremiumIssuedConfig — конфигурация, уже выданная подпиской. Ровно
// четыре поля, и каждое — под названную механику мастера (спека §5.1): код
// страны, чтобы сопоставить запись с элементом списка стран, две отметки
// времени, по которым мастер решает, устарела ли выданная конфигурация, и вид
// записи, который решает, применимы ли эти механики к ней вообще. Всё прочее,
// что портал кладёт в issued_configs, до браузера не доезжает.
//
// Отметки и вид уезжают СЫРЫМИ, а не готовыми флагами («устарел»,
// «переиздаваема»), по двум причинам. Во-первых, оба признака уже вычисляются
// на фронте (isPremiumCountryConfigStale, isPremiumIssuedConfigReissuable в
// frontend/src/lib/utils/amneziaPremiumVpnPaste.ts), и посчитать их здесь
// значит завести второе место, где живёт правило, — в том числе второе
// знание про строку gateway_account, которому потом расходиться. Во-вторых,
// bool уничтожает различие «судить не по чему» (поля нет) и «судили, не
// подходит»: оба стали бы false, и мастер не смог бы промолчать там, где
// данных нет.
type AmneziaPremiumIssuedConfig struct {
	// CountryCode — страна выданной конфигурации, как её прислал портал
	// (server_country_code). Регистр не трогаем — сопоставление с кодом
	// страны каталога делает интерфейс, как и во всём остальном каталоге.
	CountryCode string `json:"countryCode" example:"nl"`
	// LastIssuedAt — когда конфигурацию выдавали в последний раз
	// (last_downloaded), строкой в том виде, в каком прислал портал.
	LastIssuedAt string `json:"lastIssuedAt" example:"2026-09-01T10:00:00Z"`
	// PortalUpdatedAt — когда конфигурацию в последний раз меняли на стороне
	// портала (worker_last_updated). Позже LastIssuedAt — значит выданное
	// пользователю устарело.
	PortalUpdatedAt string `json:"portalUpdatedAt" example:"2026-09-02T10:00:00Z"`
	// SourceType — вид записи, как его прислал портал (source_type), строкой
	// и без разбора. Мастер делит записи по нему: gateway_account — активное
	// устройство подписки, всё прочее (и пустое — старый ответ портала поля
	// не содержал) переиздаваемо, и обе механики выше применяются ТОЛЬКО к
	// переиздаваемым. Без этого поля страна, где выдано активное устройство,
	// считалась бы уже выданной, и мастер показал бы состояние, которого нет.
	SourceType string `json:"sourceType" example:"downloaded_config"`
}

// AmneziaPremiumCatalogData — данные подписки и список стран.
//
// БЕЛЫЙ СПИСОК, собираемый поле за полем: ответ портала наружу не
// проксируется. В его data лежит в том числе сам ключ подписки, и полагаться
// на вычистку по имени поля как на единственную защиту нельзя — имена живого
// ответа перечислить невозможно. Всё, чего нет в этой структуре, до браузера
// не доезжает по построению (закрывает F183).
//
// Состав — ровно то, что показывает мастер (спека §5.1, §5.2): название
// тарифа, срок действия, счётчик устройств, список стран и узкий срез уже
// выданных конфигураций. Продление, контакты поддержки и правовые ссылки
// портал отдаёт, но мы их не берём — решение владельца.
type AmneziaPremiumCatalogData struct {
	// PlanName — название тарифа (display_name). subscription_description для
	// подписи не годится: там рекламный абзац, а не название.
	PlanName string `json:"planName" example:"Premium"`
	// SubscriptionEndDate — «действует до», как прислал портал (ISO 8601).
	// Строкой, а не временем: активность считает интерфейс, и разбор даты на
	// две стороны разошёлся бы.
	SubscriptionEndDate string `json:"subscriptionEndDate" example:"2027-04-19T08:31:00Z"`
	// ActiveDeviceCount / MaxDeviceCount — счётчик устройств подписки, как
	// его отдаёт портал. Сами по issued_configs не считаем: наш подсчёт
	// расходился с портальным.
	ActiveDeviceCount int64 `json:"activeDeviceCount" example:"3"`
	MaxDeviceCount    int64 `json:"maxDeviceCount" example:"7"`
	// Countries — список стран подписки. Пустой список — не отказ: это
	// правдивый ответ портала, и придумывать по нему ошибку значило бы
	// решать за пользователя, что его подписка сломана. А вот ОТСУТСТВИЕ
	// поля у портала — отказ: см. premiumCatalog. Поле здесь всегда непустой
	// ссылкой ([] или список), null не уезжает никогда.
	Countries []AmneziaPremiumCountry `json:"countries"`
	// IssuedConfigs — уже выданные конфигурации подписки. Без omitempty и с
	// сохранением nil ровно по той же причине, что у Protocols выше: старый
	// ответ портала поля не содержал, и «портал про выданное не сказал» (null)
	// не то же самое, что «выданного нет» ([]). В первом случае мастер обязан
	// промолчать про повторную выдачу, во втором — считать, что страна ещё не
	// выдавалась.
	IssuedConfigs []AmneziaPremiumIssuedConfig `json:"issuedConfigs"`
}

// AmneziaPremiumCatalogResponse — конверт GET /amnezia/premium/catalog.
type AmneziaPremiumCatalogResponse struct {
	Success bool                      `json:"success" example:"true"`
	Data    AmneziaPremiumCatalogData `json:"data"`
}

// premiumAccountInfo — то, что мы ЧИТАЕМ из ответа портала. Имена полей
// портальные (snake_case); всё, чего здесь нет, json.Unmarshal отбрасывает
// сам — это и есть механизм белого списка.
type premiumAccountInfo struct {
	DisplayName         string `json:"display_name"`
	SubscriptionEndDate string `json:"subscription_end_date"`
	ActiveDeviceCount   int64  `json:"active_device_count"`
	MaxDeviceCount      int64  `json:"max_device_count"`
	AvailableCountries  []struct {
		Code      string   `json:"server_country_code"`
		Name      string   `json:"server_country_name"`
		Protocols []string `json:"available_protocols"`
	} `json:"available_countries"`
	IssuedConfigs []struct {
		Code              string `json:"server_country_code"`
		LastDownloaded    string `json:"last_downloaded"`
		WorkerLastUpdated string `json:"worker_last_updated"`
		SourceType        string `json:"source_type"`
	} `json:"issued_configs"`
}

// premiumCatalog переносит ответ портала в наш DTO поле за полем.
func premiumCatalog(raw []byte) (AmneziaPremiumCatalogData, error) {
	var in premiumAccountInfo
	if err := json.Unmarshal(raw, &in); err != nil {
		// Ответ неожиданной формы — тот же класс, что молчащий портал:
		// пользователю нечего исправлять в своём ключе.
		return AmneziaPremiumCatalogData{}, fmt.Errorf(
			"%w: данные подписки не разобраны: %w", amneziacp.ErrServiceUnavailable, err)
	}
	// Списка стран у портала НЕ БЫЛО (поля нет или оно null) — отказ, а не
	// пустой каталог: пустой каталог пользователь прочитает как «в моей
	// подписке нет ни одной страны». То же правило и по той же причине уже
	// стоит слоем ниже (amneziacp.scrubAccountInfo отвергает пустой объект);
	// безусловный make здесь стирал бы различие «портал про страны не сказал»
	// и «стран нет» ровно там, где ниже его берегут. Пустой список при этом
	// проходит: это правдивый ответ портала.
	if in.AvailableCountries == nil {
		return AmneziaPremiumCatalogData{}, fmt.Errorf(
			"%w: в данных подписки нет списка стран", amneziacp.ErrServiceUnavailable)
	}
	out := AmneziaPremiumCatalogData{
		PlanName:            in.DisplayName,
		SubscriptionEndDate: in.SubscriptionEndDate,
		ActiveDeviceCount:   in.ActiveDeviceCount,
		MaxDeviceCount:      in.MaxDeviceCount,
		Countries:           make([]AmneziaPremiumCountry, 0, len(in.AvailableCountries)),
	}
	for _, c := range in.AvailableCountries {
		out.Countries = append(out.Countries, AmneziaPremiumCountry{
			Code:      c.Code,
			Name:      c.Name,
			Protocols: c.Protocols,
		})
	}
	// Слайс заводится ТОЛЬКО когда поле у портала было: безусловный make
	// превратил бы отсутствующее поле в пустой список и стёр различие,
	// ради которого оно и едет без omitempty (см. IssuedConfigs).
	if in.IssuedConfigs != nil {
		out.IssuedConfigs = make([]AmneziaPremiumIssuedConfig, 0, len(in.IssuedConfigs))
		for _, c := range in.IssuedConfigs {
			out.IssuedConfigs = append(out.IssuedConfigs, AmneziaPremiumIssuedConfig{
				CountryCode:     c.Code,
				LastIssuedAt:    c.LastDownloaded,
				PortalUpdatedAt: c.WorkerLastUpdated,
				SourceType:      c.SourceType,
			})
		}
	}
	return out, nil
}

// Catalog отдаёт данные подписки и список стран.
//
//	@Summary		Каталог подписки Amnezia Premium
//	@Description	Данные подписки и список стран. Ответ собирается по белому списку: ключ подписки, сессия портала и прочие поля ответа портала наружу не выходят.
//	@Tags			amnezia-premium
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	AmneziaPremiumCatalogResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		403	{object}	APIErrorEnvelope
//	@Failure		405	{object}	APIErrorEnvelope
//	@Failure		422	{object}	APIErrorEnvelope
//	@Failure		502	{object}	APIErrorEnvelope
//	@Failure		503	{object}	APIErrorEnvelope
//	@Router			/amnezia/premium/catalog [get]
func (h *AmneziaPremiumHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	// Ключа нет — клиент отвечает ErrNoKey ДО всякого похода в сеть: ни к
	// порталу, ни к зеркалу запрос не уходит (см. amneziacp.Client.call).
	raw, err := h.client().AccountInfo(r.Context())
	if err != nil {
		h.failCP(w, "catalog", err)
		return
	}
	data, err := premiumCatalog(raw)
	if err != nil {
		h.failCP(w, "catalog", err)
		return
	}
	h.log.Info(logActionPremium, "catalog", fmt.Sprintf("route=direct стран=%d", len(data.Countries)))
	response.Success(w, data)
}

// AmneziaPremiumConfigRequest — тело POST /amnezia/premium/config.
type AmneziaPremiumConfigRequest struct {
	CountryCode string `json:"countryCode" example:"nl"`
}

// maxCountryCodeLen ограничивает длину кода страны В БАЙТАХ. Соображение то
// же, что у maxLoggedMirrorURLLen выше: цель — роутер со 128 МБ, журнал
// приложения кольцевой и в памяти, а принятый код уезжает в него на каждой
// выдаче. Общий предел на тело запроса (мегабайт) от этого не спасает: код в
// двести тысяч символов даёт строку журнала в двести тысяч байт и выбивает
// из буфера всё остальное. Живой код — две буквы («nl»), так что 64 байта
// дают тридцатикратный запас и остаются мелочью в журнале даже целиком.
const maxCountryCodeLen = 64

// AmneziaPremiumConfigData — выданная конфигурация.
type AmneziaPremiumConfigData struct {
	// CountryCode — страна в том виде, в каком ушла в портал.
	CountryCode string `json:"countryCode" example:"nl"`
	// Config — текст .conf. Строки с ключом подписки из него вырезаны
	// клиентом (amneziacp.withoutKeyLines): живой ответ несёт ключ всей
	// подписки в комментарии-шапке.
	Config string `json:"config"`
}

// AmneziaPremiumConfigResponse — конверт POST /amnezia/premium/config.
type AmneziaPremiumConfigResponse struct {
	Success bool                     `json:"success" example:"true"`
	Data    AmneziaPremiumConfigData `json:"data"`
}

// Config выдаёт конфигурацию выбранной страны.
//
// Операция РАСХОДНАЯ: каждая выдача тратит слот устройств подписки. Поэтому
// она сериализуется ЗДЕСЬ, а не на фронте: две вкладки и двойной клик фронт
// не ловит, а цена лишнего запроса — реальный слот пользователя.
//
//	@Summary		Получить конфигурацию страны Amnezia Premium
//	@Description	Расходная операция: тратит слот устройств подписки. Параллельный запрос той же страны отвергается (409); разные страны идут параллельно. Ключ подписки в ответе не возвращается и вырезается из самой конфигурации.
//	@Tags			amnezia-premium
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		AmneziaPremiumConfigRequest	true	"Код страны"
//	@Success		200		{object}	AmneziaPremiumConfigResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		403		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Failure		422		{object}	APIErrorEnvelope
//	@Failure		502		{object}	APIErrorEnvelope
//	@Failure		503		{object}	APIErrorEnvelope
//	@Router			/amnezia/premium/config [post]
func (h *AmneziaPremiumHandler) Config(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[AmneziaPremiumConfigRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	// Нижний регистр — тот же, что применит клиент: замок обязан запираться
	// тем же ключом, каким делается запрос, иначе «NL» и «nl» уедут в портал
	// оба.
	code := strings.ToLower(strings.TrimSpace(req.CountryCode))
	if code == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Страна не выбрана", codePremiumNoCountry)
		return
	}
	if len(code) > maxCountryCodeLen {
		// Отказ ДО похода в портал: расходная операция заведомо невозможна,
		// а присланное значение иначе уехало бы и в тело запроса к порталу, и
		// в журнал целиком. Сам код в эту строку НЕ попадает — он и есть то,
		// что не влезло; длины хватает, чтобы понять, что прислал клиент.
		h.log.Warn(logActionPremium, "country-config", fmt.Sprintf(
			"route=direct код страны длиннее %d байт (%d) — отказ", maxCountryCodeLen, len(code)))
		response.ErrorWithStatus(w, http.StatusBadRequest,
			fmt.Sprintf("Код страны длиннее %d байт", maxCountryCodeLen), codePremiumBadCountry)
		return
	}
	if !h.beginCountryConfig(code) {
		// Отказ, а не ожидание: ждущий запрос всё равно кончился бы вторым
		// походом в портал либо ответом, которого пользователь уже не ждёт.
		h.log.Info(logActionPremium, "country-config",
			fmt.Sprintf("route=direct country=%q запрос уже выполняется — отказ", code))
		response.ErrorWithStatus(w, http.StatusConflict,
			"Конфигурация для этой страны уже запрашивается — дождитесь ответа", codePremiumConfigBusy)
		return
	}
	// defer, а не вызов в конце: замок обязан отпускаться и на отказе, и на
	// панике внутри (её ловит уже http.Server, но замок к тому моменту должен
	// быть отпущен — иначе страна остаётся занятой до перезапуска демона).
	defer h.endCountryConfig(code)

	conf, err := h.client().CountryConfig(r.Context(), code)
	if err != nil {
		h.failCP(w, "country-config", err)
		return
	}
	h.log.Info(logActionPremium, "country-config",
		fmt.Sprintf("route=direct country=%q конфигурация выдана", code))
	// Публикуется ПОСЛЕ успеха портала: операция расходная, и подсказка
	// «перечитай каталог» на отказе звала бы перечитывать то, что не менялось.
	// Счётчик устройств и список выданных конфигураций у портала выдачей
	// изменились — вторая вкладка иначе продолжит показывать прежние.
	h.bus.PublishInvalidated(events.ResourceAmneziaPremiumCatalog, "config-issued")
	response.Success(w, AmneziaPremiumConfigData{CountryCode: code, Config: conf})
}

// beginCountryConfig занимает страну под выдачу конфигурации. false означает
// «по этой стране запрос уже летит».
//
// Замок держит h.mu — тот же, под которым живут ключ и его поколение. Второго
// лока здесь нет сознательно: он завёл бы порядок захватов там, где его нигде
// больше нет, а сам захват не переживает ни одного похода в сеть — под ним
// только вставка в карту.
//
// Карта, а не флаг: разные страны параллелить можно и нужно, слот тратится
// по каждой отдельно.
func (h *AmneziaPremiumHandler) beginCountryConfig(code string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, busy := h.configInFlight[code]; busy {
		return false
	}
	if h.configInFlight == nil {
		h.configInFlight = make(map[string]struct{})
	}
	h.configInFlight[code] = struct{}{}
	return true
}

// endCountryConfig отпускает страну. Запись удаляется, а не помечается: карта
// обязана быть пустой в покое, иначе её размер растёт с числом стран, по
// которым когда-либо ходили.
func (h *AmneziaPremiumHandler) endCountryConfig(code string) {
	h.mu.Lock()
	delete(h.configInFlight, code)
	h.mu.Unlock()
}

// AmneziaPremiumMirrorRequest — тело POST /amnezia/premium/mirror.
//
// Поле обычной строкой, а не указателем: «поля нет» и «поле пустое» означают
// здесь одно и то же — зеркало по умолчанию. Запись безусловна, и это же
// лечит испорченное хранимое значение (см. SaveMirror).
type AmneziaPremiumMirrorRequest struct {
	MirrorURL string `json:"mirrorUrl" example:"https://storage.googleapis.com/amnezia/cp?m-path=/ru"`
}

// AmneziaPremiumMirrorData — ДЕЙСТВУЮЩИЙ адрес зеркала.
//
// Наружу идёт именно действующий, а не хранимый: хранимое пустое означает
// «зеркало по умолчанию», и показать пользователю пустое поле значило бы
// скрыть от него адрес, по которому панель реально ходит. Хранимое при этом
// остаётся пустым — только так адрес продолжает ротироваться с релизом.
type AmneziaPremiumMirrorData struct {
	MirrorURL string `json:"mirrorUrl" example:"https://storage.googleapis.com/amnezia/cp?m-path=/ru"`
}

// AmneziaPremiumMirrorResponse — конверт обоих методов /amnezia/premium/mirror.
type AmneziaPremiumMirrorResponse struct {
	Success bool                     `json:"success" example:"true"`
	Data    AmneziaPremiumMirrorData `json:"data"`
}

// Mirror — точка входа ручки адреса зеркала: метод выбирает операцию,
// как у Key.
func (h *AmneziaPremiumHandler) Mirror(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.MirrorStatus(w, r)
	case http.MethodPost:
		h.SaveMirror(w, r)
	default:
		response.MethodNotAllowed(w)
	}
}

// MirrorStatus отдаёт действующий адрес зеркала.
//
//	@Summary		Действующий адрес зеркала Amnezia
//	@Description	Отдаёт адрес, по которому панель реально ходит: хранимое пустое (и непригодное) значение означает адрес по умолчанию.
//	@Tags			amnezia-premium
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	AmneziaPremiumMirrorResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Router			/amnezia/premium/mirror [get]
func (h *AmneziaPremiumHandler) MirrorStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	// Тот же геттер, которым адрес берёт клиент CP: два понимания
	// «действующего адреса» разошлись бы молча.
	response.Success(w, AmneziaPremiumMirrorData{MirrorURL: h.mirrorURL()})
}

// SaveMirror записывает адрес зеркала.
//
// Запись БЕЗУСЛОВНА, и это и есть лечение испорченного хранимого значения.
// Поле ушло из общего ответа настроек, а страница настроек шлёт обратно тело
// ответа целиком — значит прежний путь самоисцеления (прислать поле пустым
// через /settings/update) закрыт, и мусор из settings.json убрать было
// нечем. Здесь любая запись — и своим адресом, и пустая — кладёт на его место
// проверенное значение; GET при этом не лечит ничего сознательно: чтение,
// которое пишет на флеш, — это износ флеша на каждом открытии мастера.
//
// Замена непригодного значения слышна в журнале: человек, который правил
// settings.json руками и ошибся, иначе не узнает, куда делась его правка.
//
//	@Summary		Задать адрес зеркала Amnezia
//	@Description	Пустое значение и присланный адрес по умолчанию хранятся пустыми (так адрес продолжает ротироваться с релизом). Непригодное хранимое значение заменяется присланным. В ответе — действующий адрес.
//	@Tags			amnezia-premium
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		AmneziaPremiumMirrorRequest	true	"Адрес зеркала; пустое значение — зеркало по умолчанию"
//	@Success		200		{object}	AmneziaPremiumMirrorResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/amnezia/premium/mirror [post]
func (h *AmneziaPremiumHandler) SaveMirror(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[AmneziaPremiumMirrorRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	// Нормализация ДО проверки: присланный дефолт схлопывается в пустое, а
	// пустое годно.
	sent := normalizeAmneziaMirrorURL(req.MirrorURL)
	if err := storage.ValidateAmneziaMirrorURL(sent); err != nil {
		// Признак годности общий со storage; здесь к нему добавляется только
		// код ошибки — тот же, что был у прежнего пути через настройки.
		response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), codeInvalidAmneziaMirrorURL)
		return
	}

	// Прежнее значение снимается ПОД ЛОКОМ стора, тем же, под которым идёт
	// запись: прочитать его отдельным Get значило бы рассказать в журнал про
	// значение, которое к моменту записи могло смениться.
	var replaced string
	if err := h.settings.Update(func(cur *storage.Settings) error {
		replaced = cur.AmneziaPremiumMirrorURL
		cur.AmneziaPremiumMirrorURL = sent
		return nil
	}); err != nil {
		h.log.Warn(logActionPremium, "mirror-save", "route=direct записать адрес зеркала не удалось: "+err.Error())
		response.ErrorWithStatus(w, http.StatusInternalServerError,
			"Не удалось сохранить адрес зеркала", codePremiumSettingsError)
		return
	}

	// Строка пишется только когда что-то ДЕЙСТВИТЕЛЬНО отброшено: непригодное
	// хранимое значение, которое пользователь увидеть уже не сможет. Замена
	// годного адреса другим годным ничего не теряет, и говорить про неё
	// «отброшен» было бы неправдой.
	if strings.TrimSpace(replaced) != "" && storage.ValidateAmneziaMirrorURL(replaced) != nil {
		h.log.Warn(logActionPremium, "mirror-save", fmt.Sprintf(
			"route=direct непригодный адрес зеркала Amnezia в настройках отброшен (%q)", mirrorURLForLog(replaced)))
	}
	// Действующий адрес читается заново, а не выводится из присланного:
	// пустое присланное означает дефолт, и ответ обязан назвать его. Он же
	// идёт в журнал — секретом адрес не является, а при разборе жалобы
	// спрашивают именно его.
	shown := h.mirrorURL()
	h.log.Info(logActionPremium, "mirror-save", "route=direct действующий адрес зеркала: "+shown)
	// Публикуется ПОСЛЕ удавшейся записи: на отказе settings.Update выше уже
	// вернул, и подсказка там означала бы «перечитай» про несостоявшуюся
	// смену. Своим ключом, а не ResourceSettings: адрес ушёл из общего ответа
	// настроек и читается отдельной ручкой.
	h.bus.PublishInvalidated(events.ResourceAmneziaPremiumMirror, "saved")
	response.Success(w, AmneziaPremiumMirrorData{MirrorURL: shown})
}

// normalizeAmneziaMirrorURL приводит присланный адрес зеркала к ХРАНИМОМУ
// виду: пробелы по краям срезаются, а адрес, совпавший с дефолтным,
// схлопывается в пустую строку. Смысл тот же — «зеркало по умолчанию».
//
// Схлопывание обязательно, потому что вписанный явно дефолт (руками или
// формой, подставившей действующий адрес) прибил бы литерал в settings.json;
// после этого «пусто = дефолт» перестаёт работать, и смена зеркала в новом
// релизе не доедет ни до одного такого пользователя, — то есть исчезает
// ровно та ротируемость, ради которой поле и сделали настраиваемым.
func normalizeAmneziaMirrorURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == storage.DefaultAmneziaMirrorURL {
		return ""
	}
	return raw
}

// maxLoggedMirrorURLLen ограничивает длину адреса зеркала В ЖУРНАЛЕ.
// storage.MaxAmneziaMirrorURLLen действует только на присланное через API, а
// в журнал попадает ХРАНИМОЕ значение — то самое, что легло в settings.json
// ручной правкой или откатом версии, мимо всякой проверки. Журнал
// приложения — кольцевой буфер в памяти роутера со 128 МБ; чтобы узнать свою
// опечатку, двух сотен байт хватает, а мегабайтное значение выбило бы из
// буфера всё остальное.
const maxLoggedMirrorURLLen = 200

// mirrorURLForLog готовит непригодный адрес зеркала к записи в журнал.
// Журнал — граница: он виден на странице /logs и уезжает в поддержку.
// ValidateAmneziaMirrorURL отвергает user:pass@ ровно потому, что паре
// логин/пароль нечего делать в settings.json (см. её шапку), — и отказ
// валидатора не смеет сам стать каналом публикации этой пары.
// (*url.URL).Redacted() здесь мало: он прячет пароль, но оставляет имя
// пользователя, поэтому userinfo снимается целиком. Значение, которое
// разборщику не далось — или спрятало пару в Opaque, как бессхемное
// "user:pass@host", — не показываем вовсе: что в нём лежит, мы не знаем.
func mirrorURLForLog(raw string) string {
	v := strings.TrimSpace(raw)
	u, err := url.Parse(v)
	if err != nil {
		return "<адрес не разбирается>"
	}
	switch {
	case u.User != nil:
		u.User = url.User("xxxxx")
		v = u.String()
	case strings.Contains(u.Opaque, "@"):
		return "<адрес не разбирается>"
	}
	if len(v) > maxLoggedMirrorURLLen {
		// ToValidUTF8 убирает руну, разрубленную пополам границей среза.
		v = strings.ToValidUTF8(v[:maxLoggedMirrorURLLen], "") +
			fmt.Sprintf("…(всего %d байт)", len(raw))
	}
	return v
}
