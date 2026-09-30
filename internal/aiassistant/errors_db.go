package aiassistant

import (
	"fmt"
	"regexp"
	"strings"
)

// ErrorRecord представляет отдельную запись в справочнике ошибок.
type ErrorRecord struct {
	ID          string                 `json:"id"`
	Category    string                 `json:"category"`    // KeeneticOS, AmneziaWG, Mihomo, Sing-box, DNS, System, Linux
	Code        string                 `json:"code"`        // Системный код (например: 0xcffd0218, EADDRINUSE, 122)
	Title       string                 `json:"title"`       // Понятный заголовок
	Summary     string                 `json:"summary"`     // Что произошло простыми словами
	Cause       string                 `json:"cause"`       // Техническая причина сбоя
	ActionSteps []string               `json:"actionSteps"` // Пошаговая инструкция для пользователя
	Tip         string                 `json:"tip"`         // Совет на будущее
	ExampleLog  string                 `json:"exampleLog"`  // Пример строки из лога
	Keywords    []string               `json:"keywords"`    // Ключевые слова для поиска
	Match       func(text string) bool `json:"-"`
}

// KnownError — псевдоним для обратной совместимости.
type KnownError = ErrorRecord

func matchAny(text string, substrs ...string) bool {
	low := strings.ToLower(text)
	for _, s := range substrs {
		if strings.Contains(low, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

func matchAll(text string, substrs ...string) bool {
	low := strings.ToLower(text)
	for _, s := range substrs {
		if !strings.Contains(low, strings.ToLower(s)) {
			return false
		}
	}
	return true
}

var (
	rxCFFD = regexp.MustCompile(`(?i)0xcffd([0-9a-f]{4})`)
)

// FullErrorDatabase — исчерпывающий каталог ошибок роутера Keenetic, протоколов и ядра Linux.
var FullErrorDatabase = []ErrorRecord{

	// =========================================================================
	// 1. KEENETICOS (NDMS / RCI КОДЫ И ДЕМОН NDM)
	// =========================================================================
	{
		ID:       "engine_stop_required_before_switch",
		Category: "DNS и Маршрутизация",
		Code:     "ENGINE_SWITCH_STOP_REQUIRED",
		Title:    "Требуется остановка текущего движка перед сменой ядра",
		Summary:  "Роутер не может переключить активное ядро маршрутизации (Sing-box ↔ Mihomo), пока предыдущий движок запущен и удерживает системные порты, правила tproxy и таблицы маршрутизации.",
		Cause:    "Одновременная работа двух движков маршрутизации запрещена во избежание конфликтов в ядре Linux (коллизии ip rule fwmark, порты 7890/10808 и утечки DNS).",
		ActionSteps: []string{
			"В панели статуса ядра (вверху справа или в шторке статуса) нажмите красную кнопку «Остановить движок».",
			"Подождите 2-3 секунды, пока статус текущего ядра не сменится на «Остановлен».",
			"Выберите нужное ядро маршрутизации (Sing-box или Mihomo).",
			"Нажмите зеленую кнопку «Запустить движок».",
		},
		Tip:        "Перед сменой ядра на Mihomo убедитесь, что подписки и конфигурационный файл загружены без ошибок.",
		ExampleLog: "Остановите текущий движок перед сменой ядра",
		Keywords:   []string{"смена ядра", "остановите текущий движок", "сменить движок", "singbox", "mihomo", "ядро"},
		Match: func(text string) bool {
			return matchAny(text, "остановите текущий движок", "остановите движок перед сменой", "перед сменой ядра")
		},
	},
	{
		ID:       "ndms_0xcffd0218_subnet_conflict",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd0218",
		Title:    "Конфликт IP-адресов или подсетей на роутере",
		Summary:  "Роутер отклонил команду назначения IP-адреса интерфейсу туннеля, потому что эта подсеть уже используется другим сетевым подключением.",
		Cause:    "KeeneticOS категорически запрещает дублирование и пересечение одинаковых IP-подсетей на разных интерфейсах (домашняя сеть, гостевая сеть, другой WireGuard/AWG, OpenVPN или провайдер).",
		ActionSteps: []string{
			"Перейдите на вкладку «Туннели» в AWG Manager.",
			"Нажмите иконку редактирования (карандаш) проблемного туннеля.",
			"Найдите поле «Адрес интерфейса» (например, 10.0.0.2/24).",
			"Смените третью цифру подсети на уникальную (например, с 10.0.0.2/24 на 10.84.21.2/24).",
			"Сохраните туннель и включите его заново.",
		},
		Tip:        "Убедитесь, что ни один другой туннель или подключение в Keenetic не использует ту же самую подсеть.",
		ExampleLog: "Core::Configurator: [0xcffd0218] Subnet is in use by another interface",
		Keywords:   []string{"0xcffd0218", "subnet is in use", "конфликт подсетей", "адрес уже используется", "пересечение подсетей"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd0218", "subnet is in use", "ip already exists on interface")
		},
	},
	{
		ID:       "ndms_0xcffd0217_clear_address",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd0217",
		Title:    "Попытка очистки отсутствующего IP-адреса интерфейса",
		Summary:  "Команда удаления IP-адреса интерфейса не выполнена, так как на интерфейсе не был назначен IP.",
		Cause:    "При переинициализации или остановке туннеля система отправила в NDMS команду 'no ip address', но адрес уже был очищен или не успел назначиться.",
		ActionSteps: []string{
			"Если туннель запускается нормально, эту ошибку остановки можно безопасно проигнорировать.",
			"Если интерфейс завис в переходном состоянии, перейдите в «Туннели» и нажмите тумблер выключения.",
			"Подождите 3 секунды и включите туннель снова.",
		},
		Tip:        "Это некритичное предупреждение при перезапуске сетевых интерфейсов.",
		ExampleLog: "Core::Configurator: [0xcffd0217] Unable to clear IP address: not configured",
		Keywords:   []string{"0xcffd0217", "clear address", "unable to clear ip", "no ip address"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd0217", "unable to clear ip address", "cannot clear address")
		},
	},
	{
		ID:       "ndms_0xcffd01b9_interface_down",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd01b9",
		Title:    "Сетевой интерфейс выключен или недоступен",
		Summary:  "Команда маршрутизации или настройки адреса адресована сетевому интерфейсу, который находится в состоянии DOWN (выключен).",
		Cause:    "Интерфейс туннеля был деактивирован в системе, либо родительский интерфейс провайдера потерял линк (физический обрыв кабеля или сбой Wi-Fi).",
		ActionSteps: []string{
			"Проверьте, есть ли связь с интернетом на роутере (главная страница веб-интерфейса Keenetic).",
			"В AWG Manager перейдите в раздел «Туннели» и убедитесь, что нужный туннель включен (зеленый индикатор).",
			"Если интерфейс выключен, нажмите тумблер активации.",
		},
		Tip:        "Интерфейс автоматически поднимается при успешном старте туннеля.",
		ExampleLog: "Core::Configurator: [0xcffd01b9] Interface is down, operation aborted",
		Keywords:   []string{"0xcffd01b9", "interface is down", "интерфейс выключен", "линк опущен"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd01b9", "interface is down")
		},
	},
	{
		ID:       "ndms_0xcffd00bd_session_expired",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd00bd",
		Title:    "Сессия RCI API роутера устарела или сброшена",
		Summary:  "AWG Manager потерял авторизованную сессию управления роутером Keenetic.",
		Cause:    "Служба NDMS роутера перезапустилась, либо истек таймаут авторизационного токена Keenetic RCI.",
		ActionSteps: []string{
			"Перезагрузите страницу AWG Manager в браузере (F5 или Ctrl+F5).",
			"Если появится окно входа, введите учетные данные администратора роутера (root/admin).",
			"AWG Manager автоматически обновит токен сессии RCI.",
		},
		Tip:        "Сессия автоматически пересоздается при следующем запросе к роутеру.",
		ExampleLog: "Rci::Client: [0xcffd00bd] Session expired or invalid token",
		Keywords:   []string{"0xcffd00bd", "session expired", "токен устарел", "rci session"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd00bd", "session expired", "rci session timeout")
		},
	},
	{
		ID:       "ndms_0xcffd0010_auth_failed",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd0010",
		Title:    "Ошибка авторизации в KeeneticOS (неверный логин/пароль)",
		Summary:  "Роутер отклонил попытку авторизации API или CLI из-за неверного пароля или логина.",
		Cause:    "Учетные данные администратора роутера были изменены в настройках Keenetic, либо пароль введен с ошибкой.",
		ActionSteps: []string{
			"Проверьте правильность логина и пароля администратора роутера.",
			"Войдите в официальный веб-интерфейс Keenetic (192.168.1.1 или 192.168.90.1) и убедитесь в валидности пароля.",
			"В случае смены пароля роутера обновите его в настройках AWG Manager.",
		},
		Tip:        "По умолчанию для RCI используется учетная запись root или admin.",
		ExampleLog: "Rci::Client: [0xcffd0010] Authentication failed: invalid credentials",
		Keywords:   []string{"0xcffd0010", "authentication failed", "неверный пароль", "ошибка авторизации rci"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd0010", "authentication failed", "invalid credentials")
		},
	},
	{
		ID:       "ndms_0xcffd023c_mtu_range",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd023c",
		Title:    "Недопустимое значение MTU сетевого интерфейса",
		Summary:  "KeeneticOS отклонил значение MTU, так как оно выходит за пределы допустимого диапазона (576 - 1500).",
		Cause:    "В конфигурации туннеля указано слишком маленькое (< 576) или слишком большое (> 1500) значение MTU.",
		ActionSteps: []string{
			"В AWG Manager перейдите в «Туннели» и откройте редактирование проблемного туннеля.",
			"Найдите поле «MTU» в блоке параметров интерфейса.",
			"Установите стандартное безопасное значение: 1280 (для WireGuard/AWG) или 1360.",
			"Сохраните конфигурацию и активируйте туннель.",
		},
		Tip:        "Для AmneziaWG рекомендуется MTU 1280 во избежание фрагментации пакетов с учетом обфускации.",
		ExampleLog: "Core::Configurator: [0xcffd023c] MTU value out of range (576..1500)",
		Keywords:   []string{"0xcffd023c", "mtu out of range", "недопустимый mtu", "размер mtu"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd023c", "mtu out of range")
		},
	},
	{
		ID:       "ndms_0xcffd0014_syntax_error",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd0014",
		Title:    "Синтаксическая ошибка команды в CLI KeeneticOS",
		Summary:  "NDMS не распознал переданную команду из-за синтаксической ошибки в параметрах или аргументах.",
		Cause:    "Команда RCI/CLI содержит недопустимый символ, лишний пробел, отсутствующий обязательный аргумент или опечатку в названии интерфейса.",
		ActionSteps: []string{
			"Проверьте имена интерфейсов и туннелей: они не должны содержать спецсимволы, кириллицу или пробелы.",
			"Убедитесь, что название интерфейса соответствует стандарту (например, OpkgTun10 или nwg0).",
			"Проверьте синтаксис команды в «Журнале» роутера.",
		},
		Tip:        "Используйте латинские буквы и цифры без пробелов для названий туннелей и интерфейсов.",
		ExampleLog: "Core::Configurator: [0xcffd0014] Command syntax error",
		Keywords:   []string{"0xcffd0014", "syntax error", "ошибка синтаксиса ndms", "неверный аргумент команды"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd0014", "command syntax error")
		},
	},
	{
		ID:       "ndms_0xcffd002f_not_found",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd002f",
		Title:    "Сетевой объект или интерфейс не найден в системе",
		Summary:  "Попытка обращения к интерфейсу, маршруту или политике, которая была удалена или не создана.",
		Cause:    "Интерфейс туннеля был удален, но связанные с ним статические маршруты или правила политик остались в конфигурации.",
		ActionSteps: []string{
			"Перейдите в раздел «Маршрутизация» в AWG Manager.",
			"Проверьте список правил: если есть правила, указывающие на удаленный туннель, удалите их или переназначьте.",
			"В официальном интерфейсе Keenetic («Сетевые правила» -> «Маршрутизация») удалите устаревшие статические маршруты.",
		},
		Tip:        "Всегда отвязывайте маршруты от интерфейса перед его окончательным удалением.",
		ExampleLog: "Core::Configurator: [0xcffd002f] Object not found",
		Keywords:   []string{"0xcffd002f", "object not found", "интерфейс не найден", "объект не существует"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd002f", "object not found", "interface not found")
		},
	},
	{
		ID:       "ndms_0xcffd0311_dns_route_conflict",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd0311",
		Title:    "Конфликт правила DNS-маршрутизации в KeeneticOS",
		Summary:  "Попытка добавить доменное правило DNS, которое конфликтует с уже существующим правилом или шлюзом.",
		Cause:    "Один и тот же домен или доменная маска (*.youtube.com) уже назначена другому интерфейсу или политике доступа в Keenetic.",
		ActionSteps: []string{
			"В меню Keenetic перейдите в «Сетевые правила» -> «DNS-маршрутизация».",
			"Найдите дублирующееся правило для проблемного домена.",
			"Удалите старое правило или измените назначенный интерфейс.",
			"Повторите попытку применения правила в AWG Manager.",
		},
		Tip:        "В KeeneticOS для одного домена может быть назначен только один интерфейс разрешения DNS.",
		ExampleLog: "Core::Configurator: [0xcffd0311] DNS route conflict for domain",
		Keywords:   []string{"0xcffd0311", "dns route conflict", "конфликт dns маршрута", "домен уже назначен"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd0311", "dns route conflict")
		},
	},
	{
		ID:       "ndms_0xcffd01a5_interface_locked",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd01a5",
		Title:    "Интерфейс заблокирован другой операцией конфигурации",
		Summary:  "Сетевой интерфейс занят выполнением предыдущей команды и временно недоступен для модификации.",
		Cause:    "Одновременная отправка нескольких команд настройки (например, смена адреса и включение) в короткий промежуток времени.",
		ActionSteps: []string{
			"Подождите 3-5 секунд для завершения фоновой транзакции NDMS.",
			"Обновите страницу и повторите нужное действие.",
			"Не нажимайте кнопки переключения интерфейса многократно подряд.",
		},
		Tip:        "KeeneticOS обрабатывает сетевые транзакции последовательно.",
		ExampleLog: "Core::Configurator: [0xcffd01a5] Interface is locked by transaction",
		Keywords:   []string{"0xcffd01a5", "interface is locked", "интерфейс заблокирован", "транзакция занята"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd01a5", "interface is locked")
		},
	},
	{
		ID:       "ndms_allow_ips_already_in_use",
		Category: "KeeneticOS (NDMS)",
		Code:     "WG_ALLOWED_IPS_COLLISION",
		Title:    "Коллизия WireGuard AllowedIPs между интерфейсами",
		Summary:  "KeeneticOS запретил добавление пира, так как указанные AllowedIPs пересекаются с другим WireGuard туннелем.",
		Cause:    "В двух разных WireGuard/AWG подключениях указан один и тот же AllowedIPs (например, 10.0.0.2/32 или 0.0.0.0/0 без policy routing).",
		ActionSteps: []string{
			"Откройте список туннелей в AWG Manager.",
			"Проверьте списки «AllowedIPs» у активных туннелей.",
			"Убедитесь, что IP-адреса клиентов в подсетях не дублируются.",
			"Если требуется маршрутизировать 0.0.0.0/0 через несколько туннелей, используйте политики маршрутизации (Приоритеты подключений).",
		},
		Tip:        "В ядре WireGuard для каждого IP в AllowedIPs может существовать ровно один маршрут на пир.",
		ExampleLog: "Wireguard::Manager: allow-ips already in use by interface Wireguard0",
		Keywords:   []string{"allow-ips already in use", "allowed-ips collision", "конфликт allowed-ips", "коллизия wireguard"},
		Match: func(text string) bool {
			return matchAny(text, "allow-ips already in use", "allowed-ips already in use")
		},
	},
	{
		ID:       "ndms_netlink_file_exists",
		Category: "KeeneticOS (NDMS)",
		Code:     "NETLINK_EEXIST",
		Title:    "Маршрут или правило уже существует в таблице ядра",
		Summary:  "NDMS попытался добавить системный маршрут через Netlink, но запись с такими параметрами уже присутствует в ядре.",
		Cause:    "Повторная попытка создания статического маршрута без предварительного удаления старого.",
		ActionSteps: []string{
			"Перейдите в раздел «Маршрутизация» в веб-интерфейсе Keenetic.",
			"Проверьте список статических маршрутов: найдите и удалите дублирующуюся запись.",
			"В AWG Manager нажмите кнопку перезапуска движка маршрутизации.",
		},
		Tip:        "Netlink EEXIST возникает при рассинхронизации базы данных Keenetic и таблицы ядра Linux.",
		ExampleLog: "ndm: Netlink: File exists: unable to add route",
		Keywords:   []string{"netlink: file exists", "netlink file exists", "маршрут уже существует"},
		Match: func(text string) bool {
			return matchAny(text, "netlink: file exists", "netlink: route exists")
		},
	},
	{
		ID:       "ndms_dns_route_rule_missing",
		Category: "KeeneticOS (NDMS)",
		Code:     "NDMS_DNS_RULE_NOT_FOUND",
		Title:    "Правило DNS-маршрутизации не найдено при удалении",
		Summary:  "Попытка удаления правила DNS-маршрутизации, которого нет в конфигурационном файле.",
		Cause:    "Правило было удалено вручную через CLI роутера или веб-интерфейс Keenetic, либо имя домена было изменено.",
		ActionSteps: []string{
			"Обновите список доменных правил в интерфейсе AWG Manager.",
			"Если правило отображается как активное, перезапустите службу AWG Manager в «Службы».",
			"Конфигурация синхронизируется с текущим состоянием KeeneticOS.",
		},
		Tip:        "Это безопасное предупреждение при очистке временных правил.",
		ExampleLog: "ndm: Core::Configurator: unable to find dns route rule for domain",
		Keywords:   []string{"unable to find dns route rule", "dns route rule not found", "dns правило не найдено"},
		Match: func(text string) bool {
			return matchAny(text, "unable to find dns route rule", "dns route rule not found")
		},
	},
	{
		ID:       "ndms_0xcffd0102_missing_component",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd0102",
		Title:    "Необходимый компонент KeeneticOS не установлен",
		Summary:  "Запрошенная сетевая функция не может быть запущена, так как в прошивке отсутствует нужный пакет.",
		Cause:    "В операционной системе роутера не установлены системные модули: «Протокол WireGuard», «Поддержка протокола IPv6» или «Netfilter/iptables».",
		ActionSteps: []string{
			"Откройте веб-интерфейс Keenetic (http://192.168.1.1 или http://192.168.90.1).",
			"Перейдите в «Управление» -> «Общие настройки» -> «Компоненты операционной системы».",
			"Найдите и отметьте галочкой компонент «WireGuard VPN».",
			"Нажмите «Установить обновление» и дождитесь перезагрузки роутера.",
		},
		Tip:        "Для работы AmneziaWG и Sing-box требуется установленный компонент WireGuard и Netfilter.",
		ExampleLog: "Core::Configurator: [0xcffd0102] Component wireguard is not installed",
		Keywords:   []string{"0xcffd0102", "component is not installed", "компонент не установлен", "отсутствует компонент keenetic"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd0102", "component is not installed", "component not found")
		},
	},
	{
		ID:       "ndms_0xcffd0219_gw_conflict",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd0219",
		Title:    "Конфликт IP-адреса шлюза с локальной сетью",
		Summary:  "Указанный шлюз маршрута совпадает с адресом самого роутера или находится в недопустимой подсети.",
		Cause:    "В конфигурации статического маршрута в качестве шлюза (gateway) указан IP-адрес самого роутера (например, 192.168.1.1).",
		ActionSteps: []string{
			"В разделе «Маршрутизация» проверьте настройки статических маршрутов.",
			"Шлюзом должен быть IP-адрес удаленного сервера или внутренний IP туннеля на стороне сервера (например, 10.0.0.1), а не адрес роутера.",
			"Исправьте IP шлюза и сохраните маршрут.",
		},
		Tip:        "В качестве шлюза для point-to-point туннелей обычно достаточно указать сам интерфейс.",
		ExampleLog: "Core::Configurator: [0xcffd0219] Gateway IP conflicts with local address",
		Keywords:   []string{"0xcffd0219", "gateway conflicts", "конфликт шлюза", "адрес шлюза"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd0219", "gateway conflicts")
		},
	},
	{
		ID:       "ndms_0xcffd021a_host_unreachable",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd021a",
		Title:    "Шлюз маршрута недостижим через указанный интерфейс",
		Summary:  "KeeneticOS не может добавить маршрут, так как адрес шлюза не принадлежит подсети указанного интерфейса.",
		Cause:    "Шлюз маршрута находится вне диапазона IP-адресов, назначенных на выбранный туннель или подключение к провайдеру.",
		ActionSteps: []string{
			"Проверьте IP-адрес и маску подсети на вкладке «Туннели».",
			"Убедитесь, что IP-адрес шлюза входит в ту же подсеть, что и адрес туннеля (например, если адрес роутера 10.8.0.2/24, шлюз должен быть 10.8.0.1).",
			"Исправьте подсеть или адрес шлюза.",
		},
		Tip:        "Если маска подсети /32, шлюз должен быть прямо достижим через интерфейс point-to-point.",
		ExampleLog: "Core::Configurator: [0xcffd021a] Gateway is unreachable via interface",
		Keywords:   []string{"0xcffd021a", "gateway is unreachable", "шлюз недостижим", "недостижимый шлюз"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd021a", "gateway is unreachable", "host unreachable")
		},
	},
	{
		ID:       "ndms_0xcffd0238_route_metric",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd0238",
		Title:    "Конфликт метрики маршрута или дублирование шлюза по умолчанию",
		Summary:  "Попытка добавления маршрута с метрикой, создающей коллизию с существующим маршрутом по умолчанию.",
		Cause:    "Два подключения имеют одинаковую приоритетную метрику шлюза по умолчанию (default route metric).",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic откройте «Сетевые правила» -> «Приоритеты подключений».",
			"Перетащите основное подключение к интернету вверх списка, а туннели распределите по политикам.",
			"Не назначайте туннелю приоритет основного провайдера без настроенного резервирования.",
		},
		Tip:        "Для выборочной маршрутизации используйте политики доступа, а не глобальный шлюз.",
		ExampleLog: "Core::Configurator: [0xcffd0238] Route metric collision",
		Keywords:   []string{"0xcffd0238", "route metric collision", "конфликт метрики", "метрика маршрута"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd0238", "route metric collision")
		},
	},
	{
		ID:       "ndms_0xcffd0245_dhcp_collision",
		Category: "KeeneticOS (NDMS)",
		Code:     "0xcffd0245",
		Title:    "Конфликт статического адреса с пулом DHCP Keenetic",
		Summary:  "Статический IP-адрес назначенного устройства совпадает с активным динамическим пулом DHCP роутера.",
		Cause:    "Клиенту вручную выдан IP-адрес, который сервер DHCP роутера уже назначил другому устройству домашней сети.",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic перейдите в «Мои сети и Wi-Fi» -> «Список устройств».",
			"Найдите устройство с конфликтующим IP и включите «Постоянный IP-адрес».",
			"Выберите свободный адрес вне активного диапазона DHCP (например, 192.168.1.200).",
		},
		Tip:        "Рекомендуется резервировать статические IP через привязку MAC-адреса в Keenetic.",
		ExampleLog: "Core::Configurator: [0xcffd0245] DHCP lease address collision",
		Keywords:   []string{"0xcffd0245", "dhcp collision", "конфликт dhcp", "коллизия ip адресов dhcp"},
		Match: func(text string) bool {
			return matchAny(text, "0xcffd0245", "dhcp collision", "dhcp lease collision")
		},
	},
	{
		ID:       "ndms_hw_nat_tproxy_conflict",
		Category: "KeeneticOS (NDMS)",
		Code:     "HW_NAT_OFFLOAD_CONFLICT",
		Title:    "Аппаратный ускоритель (HW NAT / Flow Offload) обходит TProxy",
		Summary:  "Сетевой трафик проходит мимо прозрачного прокси (Sing-box/Mihomo), так как аппаратный чип роутера ускоряет пакеты в обход ядра Linux.",
		Cause:    "Включенное аппаратное ускорение Hardware NAT / PPE / NSS разгружает процессор, пересылая пакеты на уровне чипа коммутатора, минуя цепочки iptables PREROUTING.",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic перейдите в «Сетевые правила» -> «Маршрутизация».",
			"Если проксирование не перехватывает определенные устройства, в CLI роутера выполните: 'no ip flow-offload' или отключите HW NAT для клиентов политики.",
			"В AWG Manager убедитесь, что в настройках маршрутизации включен TProxy с корректным fwmark.",
		},
		Tip:        "В современных версиях KeeneticOS TProxy автоматически маркирует пакеты для отключения offload.",
		ExampleLog: "ndm: Kernel: Flow offload bypass: packet routed without netfilter inspection",
		Keywords:   []string{"hw nat", "flow offload", "аппаратный ускоритель", "tproxy bypass", "трафик идет мимо прокси"},
		Match: func(text string) bool {
			return matchAny(text, "flow offload", "hw nat", "ppe offload", "hardware nat")
		},
	},
	{
		ID:       "ndms_keendns_cert_failed",
		Category: "KeeneticOS (NDMS)",
		Code:     "KEENDNS_LETSENCRYPT_FAIL",
		Title:    "Ошибка получения SSL-сертификата KeenDNS (Let's Encrypt)",
		Summary:  "Роутер не смог выпустить или продлить бесплатный SSL-сертификат для доменного имени *.keenetic.pro / *.keenetic.link.",
		Cause:    "Порты 80 (HTTP) и 443 (HTTPS) заблокированы провайдером, роутер находится за «серым» NAT без прямого доступа, либо DNS-серверы Let's Encrypt не ответили вовремя.",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic откройте «Сетевые правила» -> «Доменное имя» (KeenDNS).",
			"Убедитесь, что выбран режим «Через облако» (Cloud), если у вас серый IP-адрес от провайдера.",
			"Если порты 80/443 заняты другим приложением на роутере, измените порт веб-интерфейса в настройках.",
			"Нажмите «Обновить сертификат».",
		},
		Tip:        "В режиме «Через облако» KeenDNS работает даже при отсутствии белого публичного IP.",
		ExampleLog: "KeenDNS::Client: Let's Encrypt challenge failed: acme: error 403",
		Keywords:   []string{"keendns", "let's encrypt", "ssl сертификат keenetic", "ошибка keendns", "acme error"},
		Match: func(text string) bool {
			return matchAny(text, "keendns", "let's encrypt challenge failed", "acme: error")
		},
	},
	{
		ID:       "ndms_policy_unbound",
		Category: "KeeneticOS (NDMS)",
		Code:     "POLICY_PROFILE_UNBOUND",
		Title:    "Политика маршрутизации не привязана к сетевым интерфейсам",
		Summary:  "Созданная политика доступа («Приоритет подключений») пуста — в неё не добавлены туннели или подключения провайдера.",
		Cause:    "Клиентское устройство или доменный список привязаны к политике, в которой нет активных шлюзов для выхода в интернет.",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic перейдите в «Сетевые правила» -> «Приоритеты подключений».",
			"Найдите созданную политику доступа (например, «VPN» или «Туннели»).",
			"Поставьте галочку напротив нужного AWG/WireGuard туннеля или провайдера.",
			"Нажмите «Сохранить».",
		},
		Tip:        "Если в политике нет активных подключений, трафик привязанных устройств будет блокироваться.",
		ExampleLog: "Policy::Manager: Policy 'VPN' has no active outbound interfaces",
		Keywords:   []string{"policy unbound", "политика пуста", "приоритеты подключений", "нет интерфейсов в политике"},
		Match: func(text string) bool {
			return matchAny(text, "has no active outbound interfaces", "policy has no interfaces", "policy unbound")
		},
	},
	{
		ID:       "ndms_opkg_init_failed",
		Category: "KeeneticOS (NDMS)",
		Code:     "OPKG_INITIALIZATION_ERROR",
		Title:    "Сбой инициализации подсистемы Entware / OPKG на USB-диске",
		Summary:  "KeeneticOS не смогла смонтировать и запустить среду пакетов OPKG с внешнего накопителя.",
		Cause:    "USB-накопитель отключен, файловая система повреждена, либо на диске отсутствует каталог /opt.",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic откройте «Управление» -> «Приложения» -> «OPKG».",
			"Убедитесь, что в выпадающем списке выбран ваш USB-накопитель и переключатель находится во включенном состоянии.",
			"Если накопитель не определяется, извлеките его и проверьте файловую систему на ПК.",
			"Перезагрузите роутер.",
		},
		Tip:        "Для надежной работы OPKG используйте файловую систему ext4 вместо NTFS/FAT32.",
		ExampleLog: "ndm: Opkg::Manager: failed to initialize /opt environment on sda1",
		Keywords:   []string{"opkg init failed", "сбой opkg", "диск entware", "ошибка инициализации opkg"},
		Match: func(text string) bool {
			return matchAny(text, "failed to initialize /opt", "opkg initialization error", "opkg::manager: failed")
		},
	},
	{
		ID:       "ndms_port_forward_conflict",
		Category: "KeeneticOS (NDMS)",
		Code:     "PORT_FORWARD_CONFLICT",
		Title:    "Конфликт проброса портов с системными службами роутера",
		Summary:  "Правило переадресации портов (Port Forwarding / NAT) конфликтует с портами управления Keenetic (80, 443, 22).",
		Cause:    "Попытка пробросить внешний порт 80 или 443 на внутреннее устройство, в то время как веб-консоль роутера слушает эти же порты.",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic перейдите в «Сетевые правила» -> «Переадресация портов».",
			"Измените внешний порт (например, с 80 на 8080 или с 443 на 8443).",
			"Либо перенесите порт веб-интерфейса роутера в «Пользователи и доступ».",
		},
		Tip:        "Не занимайте стандартные порты 80/443 на внешнем интерфейсе роутера без переноса веб-консоли.",
		ExampleLog: "Core::Configurator: port 80 is already used by web interface",
		Keywords:   []string{"port forward conflict", "порт 80 уже используется", "конфликт переадресации", "проброс портов"},
		Match: func(text string) bool {
			return matchAny(text, "port is already used by web", "port forward conflict", "forwarding port conflict")
		},
	},
	{
		ID:       "ndms_guest_isolation_block",
		Category: "KeeneticOS (NDMS)",
		Code:     "GUEST_NETWORK_ISOLATION",
		Title:    "Изоляция гостевой сети блокирует доступ к AWG Manager и DNS",
		Summary:  "Устройство из гостевого сегмента сети не может открыть интерфейс AWG Manager или использовать локальный DNS.",
		Cause:    "В KeeneticOS гостевой сегмент сети по умолчанию изолирован от доступа к службам роутера и локальным ресурсам /opt.",
		ActionSteps: []string{
			"Если устройству требуется доступ к управлению AWG Manager, подключите его к «Домашней сети» вместо гостевой.",
			"В веб-интерфейсе Keenetic («Мои сети и Wi-Fi» -> «Гостевая сеть») при необходимости разрешите доступ к интернету через VPN-политику.",
		},
		Tip:        "Гостевая сеть создана для изоляции непроверенных устройств от локальной сети роутера.",
		ExampleLog: "ndm: Firewall: drop guest client packet to router local service",
		Keywords:   []string{"guest network isolation", "гостевая сеть блокирует", "изоляция гостей", "доступ из гостевой сети"},
		Match: func(text string) bool {
			return matchAny(text, "guest network isolation", "drop guest client packet")
		},
	},
	{
		ID:       "ndms_firmware_update_lock",
		Category: "KeeneticOS (NDMS)",
		Code:     "NDMS_SYSTEM_BUSY_UPDATING",
		Title:    "Система занята установкой обновления KeeneticOS",
		Summary:  "Любые изменения сетевых настроек заблокированы до завершения обновления прошивки роутера.",
		Cause:    "В фоновом режиме происходит загрузка или запись новой версии KeeneticOS во внутреннюю флеш-память роутера.",
		ActionSteps: []string{
			"Ни в коем случае не выключайте питание роутера!",
			"Подождите 3-5 минут до окончания перезагрузки роутера со свежей версией прошивки.",
			"После перезагрузки откройте веб-интерфейс заново.",
		},
		Tip:        "Во время прошивки светодиодный индикатор статуса роутера непрерывно мигает.",
		ExampleLog: "Core::Configurator: system is busy updating firmware",
		Keywords:   []string{"system busy updating", "обновление прошивки", "система занята обновлением", "firmware update lock"},
		Match: func(text string) bool {
			return matchAny(text, "system is busy updating", "firmware update in progress", "updating firmware")
		},
	},
	{
		ID:       "ndms_dns_rebind_protection",
		Category: "KeeneticOS (NDMS)",
		Code:     "DNS_REBIND_ATTACK_DETECTED",
		Title:    "Сработала защита от DNS Rebinding атак",
		Summary:  "Роутер заблокировал DNS-ответ, потому что внешнее доменное имя вернуло приватный локальный IP-адрес вашей сети (192.168.x.x или 10.x.x.x).",
		Cause:    "Встроенная защита KeeneticOS предотвращает атаки DNS Rebind, запрещая публичным доменам указывать на внутренние узлы LAN.",
		ActionSteps: []string{
			"Если вам необходим доступ к локальному сервису по внешнему домену, добавьте исключение в Keenetic.",
			"В веб-интерфейсе Keenetic перейдите в «Сетевые правила» → «DNS».",
			"Или добавьте домен в настройках переопределения локального DNS в AWG Manager.",
			"Для полного отключения проверки rebind выполните в CLI: `no ip dns-proxy protect-rebind`.",
		},
		Tip:        "В большинстве случаев правильнее настроить локальную A-запись в DNS роутера, чем отключать защиту целиком.",
		ExampleLog: "Dns::Proxy: DNS rebind attack detected for domain myhome.local, blocking 192.168.1.50",
		Keywords:   []string{"dns rebind", "rebind attack", "защита dns", "protect-rebind"},
		Match: func(text string) bool {
			return matchAny(text, "dns rebind", "rebind attack", "protect-rebind")
		},
	},
	{
		ID:       "ndms_ttl_expired_transit",
		Category: "KeeneticOS (NDMS)",
		Code:     "TTL_EXPIRED_IN_TRANSIT",
		Title:    "Превышено время жизни пакета (TTL Expired / Петля маршрутизации)",
		Summary:  "Трафик бесконечно ходит по кругу между роутером и шлюзом туннеля, пока счетчик TTL (Time to Live) не обнулится.",
		Cause:    "Петля маршрутизации (Routing Loop). Роутер отправляет пакет в туннель, а удаленный сервер или таблица маршрутов возвращает его обратно на роутер.",
		ActionSteps: []string{
			"Перейдите на вкладку «Маршрутизация» в AWG Manager.",
			"Проверьте, чтобы подсеть туннеля или адрес удаленного сервера (Endpoint IP) не были направлены внутрь самого себя.",
			"В настройках туннеля убедитесь, что в AllowedIPs не указан `0.0.0.0/0` без исключения внешнего IP-адреса сервера.",
			"Перезапустите сетевой туннель.",
		},
		Tip:        "Используйте встроенную утилиту `traceroute <IP>` на вкладке «Диагностика», чтобы увидеть, на каком хопе зацикливается маршрут.",
		ExampleLog: "ICMP: time exceeded in transit from 192.168.1.1 for packet to 8.8.8.8",
		Keywords:   []string{"ttl expired", "time exceeded in transit", "петля", "routing loop", "ttl"},
		Match: func(text string) bool {
			return matchAny(text, "ttl expired", "time exceeded in transit", "time to live exceeded")
		},
	},
	{
		ID:       "ndms_conntrack_table_full",
		Category: "KeeneticOS (NDMS)",
		Code:     "NF_CONNTRACK_FULL",
		Title:    "Переполнение таблицы отслеживания соединений (Conntrack Full)",
		Summary:  "Роутер исчерпал лимит одновременных сетевых соединений в ядре Linux и начал сбрасывать новые сетевые пакеты.",
		Cause:    "Слишком большое количество одновременных TCP/UDP сессий (активные торрент-клиенты, сканирование сети или P2P протоколы).",
		ActionSteps: []string{
			"Ограничьте максимальное число соединений в торрент-клиентах (BitTorrent, qBittorrent).",
			"В AWG Manager на вкладке «Настройки» проверьте таймауты UDP сессий.",
			"В веб-интерфейсе Keenetic перезагрузите роутер для немедленной очистки таблицы соединений.",
			"При необходимости увеличьте системный лимит в CLI Keenetic: `sysctl -w net.netfilter.nf_conntrack_max=65536`.",
		},
		Tip:        "Закрытие фоновых торрентов на ПК моментально снижает размер таблицы conntrack с десятков тысяч до нескольких сотен.",
		ExampleLog: "kernel: nf_conntrack: table full, dropping packet",
		Keywords:   []string{"nf_conntrack", "table full", "dropping packet", "conntrack full", "переполнение соединений"},
		Match: func(text string) bool {
			return matchAny(text, "nf_conntrack: table full", "table full, dropping packet", "conntrack full")
		},
	},
	{
		ID:       "ndms_usb_unmounted_io_error",
		Category: "KeeneticOS (NDMS)",
		Code:     "USB_STORAGE_IO_ERROR",
		Title:    "Сбой USB-диска или аварийное отключение накопителя",
		Summary:  "Ядро роутера потеряло связь с USB-накопителем из-за аппаратной ошибки ввода-вывода или просадки по питанию.",
		Cause:    "Недостаток питания на USB-порту (особенно при подключении 2.5\" HDD без дополнительного блока питания) либо повреждение файловой системы флешки.",
		ActionSteps: []string{
			"Не выдергивайте диск на горячую во время активной записи.",
			"Подключите внешний диск через USB-хаб с собственным блоком питания.",
			"Вставьте накопитель в ПК и проверьте файловую систему утилитой `chkdsk` (Windows) или `fsck.ext4` (Linux).",
			"Переподключите диск в USB-разъем роутера и проверьте журнал сообщений.",
		},
		Tip:        "Для роутеров Keenetic рекомендуется форматировать накопители в Ext4, так как она наиболее устойчива к сбоям питания.",
		ExampleLog: "kernel: usb 1-1: reset high-speed USB device number 2 using xhci-hcd",
		Keywords:   []string{"usb storage", "io error", "reset high-speed", "отключение usb", "сбой диска"},
		Match: func(text string) bool {
			return matchAny(text, "reset high-speed usb device", "i/o error, dev sd", "buffer i/o error on dev")
		},
	},
	{
		ID:       "ndms_wan_pppoe_auth_fail",
		Category: "KeeneticOS (NDMS)",
		Code:     "PPPOE_AUTH_FAILED",
		Title:    "Сбой авторизации подключения к интернету (PPPoE / L2TP)",
		Summary:  "Сервер интернет-провайдера отклонил логин или пароль для доступа в интернет.",
		Cause:    "Неверно введен пароль в договоре с провайдером, отрицательный баланс лицевого счета либо сбой на стороне оборудования провайдера.",
		ActionSteps: []string{
			"Откройте веб-интерфейс Keenetic → «Интернет» → «Проводной» (или PPPoE/L2TP).",
			"Сверьте логин и пароль с договором на оказание услуг связи.",
			"Проверьте баланс лицевого счета в личном кабинете провайдера.",
			"Позвоните в техподдержку провайдера для проверки привязки по MAC-адресу.",
		},
		Tip:        "У многих провайдеров смена роутера требует клонирования MAC-адреса старого устройства.",
		ExampleLog: "pppd[1234]: Remote message: Authentication failure (PAP/CHAP)",
		Keywords:   []string{"pppoe auth", "pap failed", "chap failed", "authentication failure", "ошибка провайдера"},
		Match: func(text string) bool {
			return matchAny(text, "remote message: authentication failure", "chap authentication failed", "pap authentication failed")
		},
	},
	{
		ID:       "ndms_rci_permission_denied",
		Category: "KeeneticOS (NDMS)",
		Code:     "RCI_ACCESS_DENIED",
		Title:    "Отказано в доступе к RCI API роутера (Недостаточно прав)",
		Summary:  "Учетная запись, используемая AWG Manager, не имеет прав администратора на чтение или изменение конфигурации роутера.",
		Cause:    "В настройках пользователя KeeneticOS отключены привилегии на управление CLI/RCI либо пользователь ограничен гостевым доступом.",
		ActionSteps: []string{
			"Войдите в веб-интерфейс Keenetic под учетной записью администратора (admin).",
			"Перейдите в «Управление» → «Пользователи и доступ».",
			"Убедитесь, что у учетной записи включены галочки «Администратор» и доступ к командной строке.",
			"В AWG Manager проверьте реквизиты доступа к роутеру.",
		},
		Tip:        "AWG Manager требует полных прав администратора для создания виртуальных интерфейсов и настройки таблиц маршрутизации.",
		ExampleLog: "Core::Rci: access denied for user guest on /rci/show/interface",
		Keywords:   []string{"rci access denied", "permission denied", "права rci", "недостаточно прав"},
		Match: func(text string) bool {
			return matchAny(text, "rci: access denied", "permission denied for rci", "403 forbidden on /rci")
		},
	},
	{
		ID:       "ndms_nat_loopback_hairpin_failed",
		Category: "KeeneticOS (NDMS)",
		Code:     "NAT_LOOPBACK_FAILED",
		Title:    "Сбой NAT Loopback (Hairpin NAT): внешний IP недоступен из LAN",
		Summary:  "Устройства из домашней сети не могут открыть локальный сервер (NAS, веб-сервер) по внешнему белому IP или KeenDNS имени.",
		Cause:    "Фаервол роутера не транслирует обратный трафик (NAT Loopback) для локальных инициаторов соединения.",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic перейдите в «Сетевые правила» → «Переадресация портов».",
			"Убедитесь, что для правила переадресации портов включен пункт «Разрешить доступ из локальной сети (NAT loopback)».",
			"Если используется KeenDNS в облачном режиме (Cloud), прямой доступ из LAN не поддерживается, используйте локальный IP.",
			"В AWG Manager настройте прямое разрешение имени через hosts.",
		},
		Tip:        "В режиме KeenDNS «Прямой доступ» NAT loopback работает прозрачно при наличии белого IP-адреса.",
		ExampleLog: "Nat::Loopback: packet from 192.168.1.33 to WAN IP dropped",
		Keywords:   []string{"nat loopback", "hairpin nat", "доступ по внешнему ip", "локальный сервер"},
		Match: func(text string) bool {
			return matchAny(text, "nat loopback", "hairpin nat", "nat::loopback")
		},
	},
	{
		ID:       "ndms_mesh_node_offline",
		Category: "KeeneticOS (NDMS)",
		Code:     "MESH_NODE_DISCONNECTED",
		Title:    "Узел Wi-Fi системы (Mesh Extender) потерял связь с контроллером",
		Summary:  "Ретранслятор Wi-Fi системы Keenetic перестал отвечать контроллеру по кабелю или беспроводному транспортному каналу.",
		Cause:    "Обрыв кабеля Ethernet, помехи в диапазоне 5 ГГц либо сбой питания на удаленном ретрансляторе.",
		ActionSteps: []string{
			"Проверьте индикаторы питания и подключения на ретрансляторе Keenetic.",
			"В веб-интерфейсе главного роутера откройте раздел «Wi-Fi система».",
			"Проверьте качество транспортного соединения (Backhaul) между узлами.",
			"Перезагрузите узел Wi-Fi системы.",
		},
		Tip:        "Для максимальной скорости и стабильности подключайте ретрансляторы Wi-Fi системы по витой паре (Ethernet Backhaul).",
		ExampleLog: "Mesh::Controller: node 50:ff:20:xx:xx:xx connection lost",
		Keywords:   []string{"mesh node", "wi-fi система", "ретранслятор", "extender offline", "mesh"},
		Match: func(text string) bool {
			return matchAny(text, "mesh::controller: node", "extender connection lost", "mesh node disconnected")
		},
	},
	{
		ID:       "ndms_mac_filter_reject",
		Category: "KeeneticOS (NDMS)",
		Code:     "MAC_FILTER_BLOCK",
		Title:    "Устройство заблокировано фильтром MAC-адресов",
		Summary:  "Подключение клиентского устройства отклонено роутером из-за ограничений в списке контроля доступа Wi-Fi.",
		Cause:    "В настройках беспроводной сети включен белый список MAC-адресов или устройство добавлено в список заблокированных.",
		ActionSteps: []string{
			"Откройте веб-интерфейс Keenetic → «Мои сети» → «Список устройств».",
			"Найдите заблокированное устройство в списке «Заблокированные».",
			"Нажмите на устройство и снимите галочку блокировки доступа.",
			"Если на телефоне включен «Случайный MAC-адрес», отключите его в свойствах Wi-Fi сети на смартфоне.",
		},
		Tip:        "Функция случайного MAC-адреса на iOS и Android меняет адрес при каждом переподключении, что может вызывать ложные блокировки.",
		ExampleLog: "Wlan::Acl: association rejected by mac filter for 12:34:56:78:9a:bc",
		Keywords:   []string{"mac filter", "wlan::acl", "association rejected", "фильтр mac", "блокировка mac"},
		Match: func(text string) bool {
			return matchAny(text, "association rejected by mac filter", "wlan::acl", "mac filter rejected")
		},
	},
	{
		ID:       "ndms_startup_config_corrupted",
		Category: "KeeneticOS (NDMS)",
		Code:     "STARTUP_CONFIG_CRC_ERROR",
		Title:    "Повреждение файла конфигурации startup-config",
		Summary:  "При загрузке роутера обнаружена ошибка контрольной суммы файла конфигурации. Роутер может загрузиться с настройками по умолчанию.",
		Cause:    "Внезапное отключение электропитания во время сохранения настроек в энергонезависимую память flash.",
		ActionSteps: []string{
			"Немедленно сделайте резервную копию настроек через веб-интерфейс: «Управление» → «Общие настройки» → «Сохранить конфигурацию».",
			"Восстановите конфигурацию из ранее сохраненного файла `.txt`.",
			"Если роутер перезагружается циклически, выполните сброс до заводских настроек кнопкой Reset и восстановите бэкап.",
			"Рекомендуется подключить роутер через источник бесперебойного питания (ИБП).",
		},
		Tip:        "Всегда сохраняйте бэкап конфигурации роутера перед крупными обновлениями или установкой новых пакетов.",
		ExampleLog: "Core::Config: startup-config CRC mismatch, restoring defaults",
		Keywords:   []string{"startup-config", "crc mismatch", "повреждение конфига", "битый конфиг", "corrupted config"},
		Match: func(text string) bool {
			return matchAny(text, "startup-config crc mismatch", "startup-config corrupted", "failed to parse startup-config")
		},
	},

	// =========================================================================
	// 2. AMNEZIAWG И WIREGUARD (ПРОТОКОЛ, КЛЮЧИ, ОБФУСКАЦИЯ)
	// =========================================================================
	{
		ID:       "awg_syntax_leading_space",
		Category: "AmneziaWG",
		Code:     "AWG_SYNTAX_LEADING_SPACE",
		Title:    "Синтаксическая ошибка: пробел перед параметром в конфиге AWG",
		Summary:  "Утилита awg setconf не смогла распарсить конфигурационный файл из-за лишнего пробела или отступа перед именем параметра (например, ' H1=' вместо 'H1=').",
		Cause:    "При ручном редактировании или копировании из буфера обмена в начале строки перед параметрами обфускации (Jc, Jmin, Jmax, S1, S2, H1-H4) попал пробел.",
		ActionSteps: []string{
			"Перейдите на вкладку «Туннели» в AWG Manager.",
			"Нажмите иконку редактирования (карандаш) проблемного туннеля.",
			"Перейдите в режим редактирования исходного текста (Raw конфигурация).",
			"Найдите строку с пробелом перед параметром (например, ' H1=') и удалите пробел в самом начале строки.",
			"Нажмите «Сохранить» и активируйте туннель.",
		},
		Tip:        "Имена секций ([Interface], [Peer]) и параметры в конфигурациях WireGuard и AmneziaWG обязаны начинаться строго с первого символа строки без отступов.",
		ExampleLog: "start awg10 [wg]: awg setconf opkgtun10: Line unrecognized: ` H1=' Configuration parsing error",
		Keywords:   []string{"line unrecognized", "configuration parsing error", "h1=", "пробел перед параметром", "синтаксис awg"},
		Match: func(text string) bool {
			return matchAny(text, "line unrecognized: ` h", "line unrecognized: ` j", "line unrecognized: ` s", "configuration parsing error")
		},
	},
	{
		ID:       "awg_syntax_unknown_param",
		Category: "AmneziaWG",
		Code:     "AWG_UNKNOWN_PARAMETER",
		Title:    "Параметры обфускации AWG отклонены стандартным WireGuard",
		Summary:  "Стандартная утилита wg или wg-quick отклонила параметры обфускации Jc, Jmin, Jmax, S1, S2, H1, H2, H3, H4.",
		Cause:    "Попытка запуска AmneziaWG-конфигурации через стандартный драйвер WireGuard, не поддерживающий расширения обфускации AWG.",
		ActionSteps: []string{
			"Убедитесь, что для туннеля выбран тип «AmneziaWG», а не обычный «WireGuard».",
			"В настройках туннеля переключите бэкенд на awg / amneziawg-go.",
			"Если используется встроенный модуль ядра, проверьте наличие установленного kmod-amneziawg.",
		},
		Tip:        "Для работы с параметрами Jc/H1 используйте AmneziaWG вместо стандартного WireGuard.",
		ExampleLog: "wg setconf: Unknown parameter: `Jc='",
		Keywords:   []string{"unknown parameter jc", "unknown parameter h1", "неизвестный параметр wg", "amneziawg parameters rejected"},
		Match: func(text string) bool {
			return matchAny(text, "unknown parameter: `jc", "unknown parameter: `h1", "unknown parameter: `s1")
		},
	},
	{
		ID:       "awg_invalid_base64_key",
		Category: "AmneziaWG",
		Code:     "AWG_INVALID_BASE64_KEY",
		Title:    "Некорректный ключ шифрования (ошибка Base64 / длина != 32 байта)",
		Summary:  "WireGuard или AmneziaWG отклонил PrivateKey, PublicKey или PresharedKey из-за неверной длины или недопустимых символов.",
		Cause:    "Ключ был скопирован не полностью (не хватает символов в конце), содержит пробелы или не является валидной строкой Base64 (ровно 44 символа со знаком =).",
		ActionSteps: []string{
			"Откройте настройки туннеля в AWG Manager.",
			"Проверьте поля «PrivateKey» (закрытый ключ интерфейса) и «PublicKey» (открытый ключ сервера).",
			"Убедитесь, что длина ключа ровно 44 символа и в конце нет случайных пробелов или переносов строк.",
			"Скопируйте ключ заново из исходного файла конфигурации провайдера.",
		},
		Tip:        "Ключи WireGuard всегда состоят из 44 символов Base64 и часто оканчиваются знаком '='.",
		ExampleLog: "awg set: Key is not valid base64 or has wrong length",
		Keywords:   []string{"invalid base64", "wrong length key", "неверный ключ", "битый ключ wireguard"},
		Match: func(text string) bool {
			return matchAny(text, "key is not valid base64", "has wrong length", "invalid base64 key")
		},
	},
	{
		ID:       "awg_handshake_timeout_rx0",
		Category: "AmneziaWG",
		Code:     "AWG_HANDSHAKE_TIMEOUT",
		Title:    "Таймаут рукопожатия (Handshake Timeout) / блокировка DPI провайдером",
		Summary:  "Роутер отправляет пакеты инициализации соединения (Tx > 0), но от сервера не поступает ни одного байта ответа (Rx = 0).",
		Cause:    "Провайдер интернета блокирует стандартный протокол WireGuard с помощью ТСПУ/DPI, либо сервер выключен, либо изменен IP/порт сервера.",
		ActionSteps: []string{
			"Если используется обычный WireGuard — перейдите на AmneziaWG с включенной обфускацией пакетов (параметры Jc, Jmin, Jmax, H1-H4).",
			"В AWG Manager проверьте пинг до IP-адреса Endpoint в разделе «Проверки».",
			"Попробуйте изменить порт подключения на стороне сервера (например, на порт 443/UDP или нестандартный порт 40000+).",
			"Убедитесь, что сервер не заблокирован по IP.",
		},
		Tip:        "Признак блокировки протокола DPI: отправка растет (Tx: несколько килобайт), прием строго 0 (Rx: 0 B), а статус 'latest handshake' отсутствует.",
		ExampleLog: "awg: opkgtun10: handshake for peer 1 did not complete after 5 seconds, retrying. rx: 0, tx: 1480",
		Keywords:   []string{"handshake timeout", "rx: 0", "нет ответа от сервера", "dpi блокировка", "рукопожатие не завершено"},
		Match: func(text string) bool {
			return matchAny(text, "did not complete after 5 seconds", "handshake timeout", "rx: 0, tx:", "rx: 0 B")
		},
	},
	{
		ID:       "awg_endpoint_dns_failed",
		Category: "AmneziaWG",
		Code:     "AWG_ENDPOINT_RESOLV_FAIL",
		Title:    "Не удалось разрешить доменное имя сервера (Endpoint DNS failed)",
		Summary:  "Роутер не смог определить IP-адрес сервера по указанному доменному имени Endpoint.",
		Cause:    "На роутере отсутствует интернет при старте службы, DNS-серверы провайдера заблокировали домен, либо в имени домена допущена опечатка.",
		ActionSteps: []string{
			"Проверьте интернет-соединение на роутере.",
			"В настройках туннеля проверьте правильность доменного имени Endpoint.",
			"В качестве временного решения замените доменное имя в Endpoint на прямой IP-адрес сервера.",
			"В разделе «Сведения о DNS» проверьте работу вышестоящих серверов DNS (1.1.1.1, 77.88.8.8).",
		},
		Tip:        "Использование прямого IP-адреса вместо домена защищает от сбоев DNS при старте туннеля.",
		ExampleLog: "awg-quick: Resolving host `vpn.example.com' failed: Name or service not known",
		Keywords:   []string{"resolving host failed", "name or service not known", "сбой dns endpoint", "не разрешается домен endpoint"},
		Match: func(text string) bool {
			return matchAny(text, "resolving host", "name or service not known", "resolving endpoint failed")
		},
	},
	{
		ID:       "awg_exit_status_122_mtu",
		Category: "AmneziaWG",
		Code:     "AWG_EXIT_STATUS_122",
		Title:    "Размер пакета превышает допустимый MTU (Exit status 122)",
		Summary:  "Сетевой драйвер вернул ошибку переполнения буфера пакета при попытке отправки через AmneziaWG.",
		Cause:    "Суммарный размер пакета с учетом обфускационных заголовков и мусорных пакетов (Junk Packet Count) превысил размер кадра MTU физического сетевого адаптера.",
		ActionSteps: []string{
			"Откройте параметры туннеля в AWG Manager.",
			"Уменьшите значение «MTU» туннеля до 1280 (минимальный безопасный размер IPv4/IPv6).",
			"В блоке обфускации проверьте параметр «Jmax»: уменьшите его до 100-200 байт.",
			"Сохраните конфигурацию и перезапустите туннель.",
		},
		Tip:        "Обфускация AmneziaWG добавляет байты заголовков к каждому пакету, поэтому MTU должен быть не более 1280-1360.",
		ExampleLog: "awg-quick: failed with exit status 122: Message too long",
		Keywords:   []string{"exit status 122", "message too long", "переполнение mtu", "размер пакета awg"},
		Match: func(text string) bool {
			return matchAny(text, "exit status 122", "message too long")
		},
	},
	{
		ID:       "awg_psk_mismatch",
		Category: "AmneziaWG",
		Code:     "AWG_PSK_MISMATCH",
		Title:    "Несоответствие дополнительного ключа PresharedKey",
		Summary:  "Туннель запущен, но трафик не проходит из-за несовпадения симметричного ключа постквантовой защиты PresharedKey.",
		Cause:    "На сервере и на роутере заданы разные значения ключа PresharedKey, либо ключ указан на одной стороне и пропущен на другой.",
		ActionSteps: []string{
			"Откройте конфигурацию туннеля на роутере и конфигурацию на стороне сервера.",
			"Сверьте значение строки «PresharedKey»: они должны быть абсолютно идентичны.",
			"Если на сервере PresharedKey не используется, удалите строку PresharedKey в настройках туннеля на роутере.",
		},
		Tip:        "При несовпадении PresharedKey соединение тихо сбрасывается без явного сообщения об ошибке.",
		ExampleLog: "awg: peer handshake failed: preshared key authentication mismatch",
		Keywords:   []string{"preshared key", "psk mismatch", "несовпадение psk", "ошибка preshared key"},
		Match: func(text string) bool {
			return matchAny(text, "preshared key authentication mismatch", "preshared key mismatch", "invalid preshared key")
		},
	},
	{
		ID:       "awg_interface_already_exists",
		Category: "AmneziaWG",
		Code:     "AWG_INTERFACE_EXISTS",
		Title:    "Сетевой интерфейс opkgtunXX уже существует в системе",
		Summary:  "Система не смогла создать интерфейс туннеля, так как устройство с таким именем уже зарегистрировано в ядре.",
		Cause:    "Предыдущий процесс аварийно завершился, оставив сетевой интерфейс 'висеть' в таблице устройств ядра.",
		ActionSteps: []string{
			"В AWG Manager перейдите во вкладку «Терминал».",
			"Выполните команду удаления зависшего интерфейса: ip link delete opkgtunXX (заменив XX на номер интерфейса из лога).",
			"В качестве альтернативы нажмите «Перезагрузить службу» в меню «Службы».",
			"Запустите туннель заново.",
		},
		Tip:        "AWG Manager умеет автоматически очищать 'осиротевшие' интерфейсы при штатном перезапуске.",
		ExampleLog: "RTNETLINK answers: File exists: unable to create interface opkgtun10",
		Keywords:   []string{"interface already exists", "opkgtun file exists", "интерфейс уже существует"},
		Match: func(text string) bool {
			return matchAny(text, "unable to create interface", "interface already exists")
		},
	},
	{
		ID:       "awg_rtnetlink_addr_in_use",
		Category: "AmneziaWG",
		Code:     "AWG_ADDR_IN_USE",
		Title:    "Порт прослушивания WireGuard уже занят (Address already in use)",
		Summary:  "Драйвер AmneziaWG не смог привязаться к указанному ListenPort на роутере.",
		Cause:    "Указанный UDP-порт прослушивания уже используется другим туннелем WireGuard, процессом Sing-box или системной службой роутера.",
		ActionSteps: []string{
			"В настройках туннеля найдите параметр «ListenPort».",
			"Если указан конкретный порт, измените его на другое свободное значение (например, 51821 вместо 51820).",
			"Либо удалите строку ListenPort вовсе, чтобы система выбрала случайный свободный порт автоматически.",
		},
		Tip:        "Для клиентских подключений указывать фиксированный ListenPort необязательно.",
		ExampleLog: "awg set: RTNETLINK answers: Address already in use",
		Keywords:   []string{"rtnetlink answers: address already in use", "listenport in use", "порт уже занят awg"},
		Match: func(text string) bool {
			return matchAny(text, "rtnetlink answers: address already in use", "cannot bind listen port")
		},
	},
	{
		ID:       "awg_rtnetlink_file_exists_peer",
		Category: "AmneziaWG",
		Code:     "AWG_DUPLICATE_PEER_KEY",
		Title:    "Дублирующийся публичный ключ пира в конфигурации",
		Summary:  "Попытка добавления пира с открытым ключом, который уже назначен другому пиру на этом интерфейсе.",
		Cause:    "В конфигурации туннеля несколько секций [Peer] имеют одинаковый PublicKey.",
		ActionSteps: []string{
			"Откройте конфигурацию туннеля в AWG Manager.",
			"Проверьте все секции [Peer]: убедитесь, что у каждого сервера или клиента свой уникальный PublicKey.",
			"Удалите дублирующуюся секцию.",
		},
		Tip:        "Каждый пир внутри одного WireGuard-интерфейса обязан иметь уникальный открытый ключ.",
		ExampleLog: "awg set: RTNETLINK answers: File exists (peer public key duplicate)",
		Keywords:   []string{"peer public key duplicate", "duplicate peer", "дубликат ключа пира"},
		Match: func(text string) bool {
			return matchAny(text, "peer public key duplicate", "duplicate peer key")
		},
	},
	{
		ID:       "awg_allowed_ips_route_loop",
		Category: "AmneziaWG",
		Code:     "AWG_ROUTE_LOOP_DEFAULT",
		Title:    "Петля маршрутизации: перехват шлюза роутера через 0.0.0.0/0",
		Summary:  "Указание AllowedIPs = 0.0.0.0/0 без маркировки трафика привело к перехвату собственного соединения роутера с интернетом.",
		Cause:    "Пакеты до VPN-сервера пытаются уйти внутрь самого VPN-туннеля, создавая замкнутый цикл и обрывая связь роутера.",
		ActionSteps: []string{
			"В AWG Manager перейдите в «Туннели» и отредактируйте конфигурацию.",
			"Убедитесь, что для туннеля настроен FwMark (маркировка трафика) во избежание захвата служебных пакетов.",
			"Для выборочной маршрутизации укажите в AllowedIPs конкретные подсети вместо 0.0.0.0/0, либо используйте политики маршрутизации AWG Manager.",
		},
		Tip:        "В AWG Manager маршрутизация через политики изолирует туннели и предотвращает петли.",
		ExampleLog: "awg: routing loop detected: endpoint traffic redirected into tunnel",
		Keywords:   []string{"routing loop", "петля маршрутизации", "allowedips 0.0.0.0/0 loop", "потеря связи с роутером"},
		Match: func(text string) bool {
			return matchAny(text, "routing loop detected", "endpoint traffic redirected into tunnel")
		},
	},
	{
		ID:       "awg_keepalive_zero_nat",
		Category: "AmneziaWG",
		Code:     "AWG_KEEPALIVE_DISABLED",
		Title:    "Отсутствует PersistentKeepalive за NAT провайдера",
		Summary:  "Туннель засыпает и перестает принимать входящий трафик через несколько десятков секунд после открытия.",
		Cause:    "Роутер находится за NAT интернет-провайдера (мобильный интернет, оптика с серым IP). Таблица трансляции NAT закрывает порт при отсутствии исходящих пакетов.",
		ActionSteps: []string{
			"В конфигурации туннеля найдите секцию [Peer].",
			"Добавьте или измените строку: PersistentKeepalive = 25 (или 15 для мобильных сетей).",
			"Сохраните конфигурацию и активируйте туннель.",
		},
		Tip:        "Значение 25 секунд — общепринятый стандарт для удержания NAT-сессии открытой.",
		ExampleLog: "awg: NAT mapping expired, incoming packets dropped: keepalive disabled",
		Keywords:   []string{"persistentkeepalive", "nat mapping expired", "туннель засыпает", "пропадает входящий трафик"},
		Match: func(text string) bool {
			return matchAny(text, "nat mapping expired", "persistentkeepalive is required", "keepalive disabled")
		},
	},
	{
		ID:       "awg_kmod_missing",
		Category: "AmneziaWG",
		Code:     "AWG_KMOD_MISSING",
		Title:    "Отсутствует модуль ядра kmod-amneziawg",
		Summary:  "Попытка запуска AmneziaWG в режиме ядра не удалась из-за отсутствия скомпилированного драйвера kmod-amneziawg.",
		Cause:    "Прошивка роутера не содержит встроенного модуля ядра AmneziaWG, либо ядро было обновлено.",
		ActionSteps: []string{
			"В настройках AWG Manager переключите режим работы на «Go-бэкенд» (amneziawg-go в user-space).",
			"Пользовательский бэкенд не требует модулей ядра и работает на любой версии KeeneticOS.",
			"Если требуется ядерный модуль, проверьте доступные пакеты в «Пакеты opkg».",
		},
		Tip:        "User-space реализация amneziawg-go стабильно работает без модификации ядра роутера.",
		ExampleLog: "modprobe: can't load module amneziawg (kernel-module not found)",
		Keywords:   []string{"kmod-amneziawg", "kernel-module not found", "модуль ядра amneziawg", "can't load module amneziawg"},
		Match: func(text string) bool {
			return matchAny(text, "can't load module amneziawg", "kmod-amneziawg not found", "module amneziawg not found")
		},
	},
	{
		ID:       "awg_junk_packet_range",
		Category: "AmneziaWG",
		Code:     "AWG_JUNK_RANGE_INVALID",
		Title:    "Недопустимый диапазон мусорных пакетов (Jmin > Jmax)",
		Summary:  "AmneziaWG отклонил параметры обфускации, так как минимальный размер мусорного пакета больше максимального.",
		Cause:    "В конфигурации задано некорректное соотношение: значение параметра Jmin превышает значение Jmax.",
		ActionSteps: []string{
			"Откройте конфигурацию туннеля в AWG Manager.",
			"Проверьте строки Jmin и Jmax в секции [Interface].",
			"Убедитесь, что Jmin меньше или равен Jmax (например: Jmin = 50, Jmax = 100).",
			"Сохраните конфигурацию.",
		},
		Tip:        "Стандартные рекомендуемые значения для обхода DPI: Jc = 4, Jmin = 40, Jmax = 70.",
		ExampleLog: "awg: invalid junk packet configuration: Jmin cannot exceed Jmax",
		Keywords:   []string{"jmin cannot exceed jmax", "jmin > jmax", "недопустимый диапазон junk", "ошибка параметров amneziawg"},
		Match: func(text string) bool {
			return matchAny(text, "jmin cannot exceed jmax", "invalid junk packet configuration")
		},
	},
	{
		ID:       "awg_mss_clamping_needed",
		Category: "AmneziaWG",
		Code:     "AWG_TCP_MSS_FREEZE",
		Title:    "Зависание сайтов по HTTPS из-за отсутствия TCP MSS Clamping",
		Summary:  "DNS-запросы и пинги проходят нормально, но тяжелые веб-страницы и видео бесконечно грузятся через туннель.",
		Cause:    "Размер TCP-пакетов превышает MTU туннеля. Из-за блокировки ICMP 'Fragmentation Needed' в интернете возникает 'черная дыра' Path MTU.",
		ActionSteps: []string{
			"В AWG Manager перейдите в «Инструменты» -> «Система» -> «Службы».",
			"Убедитесь, что в правилах файрвола активно правило iptables TCPMSS: '--clamp-mss-to-pmtu'.",
			"В настройках туннеля вручную понизьте MTU до 1280.",
			"Переподключите туннель.",
		},
		Tip:        "Автоматический MSS Clamping подгоняет размер пакетов TCP под размер VPN-туннеля.",
		ExampleLog: "Kernel: TCP: packet exceeds path MTU, black hole detected: enable MSS clamping",
		Keywords:   []string{"tcp mss clamping", "зависают сайты", "бесконечная загрузка https", "черная дыра mtu"},
		Match: func(text string) bool {
			return matchAny(text, "enable mss clamping", "black hole detected", "clamp-mss-to-pmtu")
		},
	},
	{
		ID:       "awg_jc_packet_count_exceeded",
		Category: "AmneziaWG",
		Code:     "AWG_JC_OUT_OF_RANGE",
		Title:    "Превышен лимит мусорных пакетов Jc в AmneziaWG",
		Summary:  "Параметр Jc (Junk Packet Count) задан со значением больше 128. Модуль ядра AmneziaWG не принимает такие значения.",
		Cause:    "Спецификация AmneziaWG ограничивает количество мусорных пакетов перед инициацией рукопожатия диапазоном от 0 до 128.",
		ActionSteps: []string{
			"В AWG Manager откройте настройки туннеля (иконка карандаша).",
			"Найдите поле «Jc (Количество мусорных пакетов)».",
			"Установите значение в диапазоне от 3 до 10 (рекомендуется: 4-5).",
			"Сохраните конфигурацию и перезапустите туннель.",
		},
		Tip:        "Слишком большое значение Jc (например, >20) замедляет установку соединения и создает лишнюю нагрузку на слабый процессор роутера.",
		ExampleLog: "awg: invalid parameter Jc: value 200 exceeds maximum 128",
		Keywords:   []string{"jc out of range", "junk packet count", "параметр jc", "amneziawg jc"},
		Match: func(text string) bool {
			return matchAny(text, "invalid parameter jc", "jc exceeds maximum", "jc out of range")
		},
	},
	{
		ID:       "awg_h1_h4_headers_conflict",
		Category: "AmneziaWG",
		Code:     "AWG_HEADERS_COLLISION",
		Title:    "Коллизия типов пакетов (H1, H2, H3, H4) в AmneziaWG",
		Summary:  "Значения магических заголовков H1-H4 совпадают между собой или равны стандартным типам пакетов WireGuard.",
		Cause:    "Каждый из 4 параметров заголовков (H1, H2, H3, H4) должен быть строго уникальным 32-битным числом, отличным от стандартных кодов 1, 2, 3, 4.",
		ActionSteps: []string{
			"Откройте конфигурацию туннеля на вкладке «Туннели».",
			"Проверьте блоки параметров [Interface] и убедитесь, что H1, H2, H3 и H4 имеют разные числовые значения.",
			"Если конфиг сгенерирован сторонним клиентом, пересоздайте конфигурацию в панели управления вашего сервера Amnezia.",
			"Примените обновленную конфигурацию.",
		},
		Tip:        "Используйте кнопку автогенерации параметров обфускации в мастере создания туннеля, чтобы избежать ручных ошибок.",
		ExampleLog: "awg: duplicate message type in headers: H1 and H2 cannot be identical",
		Keywords:   []string{"h1 and h2", "duplicate message type", "headers collision", "amneziawg headers"},
		Match: func(text string) bool {
			return matchAny(text, "duplicate message type in headers", "headers collision", "h1 and h2 cannot be identical")
		},
	},
	{
		ID:       "awg_s1_s2_sum_exceeds_mtu",
		Category: "AmneziaWG",
		Code:     "AWG_INIT_PACKET_OVERFLOW",
		Title:    "Сумма мусорных байт S1/S2 превышает MTU",
		Summary:  "Размер пакета инициализации рукопожатия вместе с добавленными мусорными байтами S1/S2 превысил размер MTU туннеля.",
		Cause:    "Стандартный пакет Handshake имеет размер 148 байт. Если S1 + 148 > MTU (обычно 1280-1420), пакет фрагментируется или сбрасывается сетевым драйвером.",
		ActionSteps: []string{
			"В настройках туннеля уменьшите значение параметра S1 (размер мусорного префикса инициации).",
			"Рекомендуемое значение S1 — от 15 до 60 байт.",
			"Проверьте, чтобы MTU туннеля было не менее 1280 байт.",
			"Сохраните настройки и перезапустите подключение.",
		},
		Tip:        "Для надежного прохождения через любого мобильного оператора держите S1 в пределах 30-50 байт при MTU 1280.",
		ExampleLog: "awg: init packet size with S1 exceeds interface MTU 1280",
		Keywords:   []string{"s1 exceeds mtu", "init packet size", "мусорные байты s1", "awg mtu overflow"},
		Match: func(text string) bool {
			return matchAny(text, "packet size with s1 exceeds", "s1 exceeds interface mtu", "s1+init size exceeds mtu")
		},
	},
	{
		ID:       "awg_roaming_endpoint_drift",
		Category: "AmneziaWG",
		Code:     "AWG_ROAMING_DRIFT",
		Title:    "Спонтанный дрейф адреса эндпоинта (UDP Roaming Drift)",
		Summary:  "Туннель внезапно переключился на неверный IP-адрес из-за входящих пакетов со стороннего узла в интернете.",
		Cause:    "Протокол WireGuard по умолчанию обновляет адрес пира при получении валидного аутентифицированного пакета с нового адреса (Roaming). Если сетевой трафик подменяется провайдером, соединение зависает.",
		ActionSteps: []string{
			"В веб-интерфейсе роутера проверьте стабильность внешнего WAN IP-адреса.",
			"На вкладке «Туннели» выключите и снова включите туннель, чтобы сбросить адрес пира к исходному Endpoint.",
			"Убедитесь, что порт туннеля на сервере не совпадает с общедоступными сканируемыми портами (51820).",
			"Включите обфускацию AmneziaWG (H1-H4, Jc, S1-S2) для защиты от поддельных пакетов.",
		},
		Tip:        "Использование нестандартного UDP-порта на сервере (например, 38452 вместо 51820) защищает от сканеров сети.",
		ExampleLog: "wireguard: opkgtun0: peer 1 (endpoint changed from 198.51.100.1:51820 to 203.0.113.88:41234)",
		Keywords:   []string{"endpoint changed", "roaming drift", "дрейф адреса", "wireguard roaming"},
		Match: func(text string) bool {
			return matchAny(text, "endpoint changed from", "peer endpoint changed", "udp roaming drift")
		},
	},
	{
		ID:       "awg_keys_private_public_identical",
		Category: "AmneziaWG",
		Code:     "AWG_KEY_PAIR_IDENTICAL",
		Title:    "Приватный и публичный ключ в конфигурации совпадают",
		Summary:  "В поле PublicKey пира по ошибке скопирован собственный PrivateKey или наоборот.",
		Cause:    "Асимметричная криптография Curve25519 требует строгого разделения: роутер хранит свой PrivateKey, а серверу отдает соответствующий PublicKey.",
		ActionSteps: []string{
			"Откройте конфигурацию туннеля в AWG Manager.",
			"Сверьте строку `PrivateKey` в блоке [Interface] и `PublicKey` в блоке [Peer] — они должны быть абсолютно разными!",
			"Сгенерируйте правильную пару ключей или скопируйте клиентский конфиг из панели Amnezia заново.",
			"Сохраните конфигурацию туннеля.",
		},
		Tip:        "Приватный ключ никогда не передается удаленной стороне; удаленный сервер знает только ваш публичный ключ.",
		ExampleLog: "awg: invalid configuration: private key and peer public key must differ",
		Keywords:   []string{"private key and peer public key", "ключи совпадают", "identical keys", "curve25519 identical"},
		Match: func(text string) bool {
			return matchAny(text, "private key and peer public key must differ", "identical keys in config", "public key matches private key")
		},
	},
	{
		ID:       "awg_allowed_ips_empty",
		Category: "AmneziaWG",
		Code:     "AWG_ALLOWED_IPS_EMPTY",
		Title:    "Список разрешенных IP-адресов пира пуст (AllowedIPs is empty)",
		Summary:  "WireGuard не знает, какие IP-пакеты разрешено отправлять и принимать через данный туннель.",
		Cause:    "В секции [Peer] пропущен параметр AllowedIPs либо он задан пустым значением.",
		ActionSteps: []string{
			"Откройте редактирование туннеля в AWG Manager.",
			"В секции пира найдите поле «Разрешенные IP (AllowedIPs)».",
			"Для полного туннелирования всего трафика укажите: `0.0.0.0/0`.",
			"Для туннелирования только локальной сети сервера укажите адрес подсети (например, `10.8.0.0/24`).",
			"Сохраните туннель и нажмите «Применить».",
		},
		Tip:        "Если в AllowedIPs указать `0.0.0.0/0`, роутер направит весь интернет-трафик через туннель.",
		ExampleLog: "wireguard: peer must have at least one allowed ip entry",
		Keywords:   []string{"allowed ips empty", "peer must have at least one", "allowedips пуст", "разрешенные ip"},
		Match: func(text string) bool {
			return matchAny(text, "peer must have at least one allowed ip", "allowedips is empty", "no allowed ips for peer")
		},
	},
	{
		ID:       "awg_device_busy_down",
		Category: "AmneziaWG",
		Code:     "AWG_DEVICE_BUSY",
		Title:    "Сетевой интерфейс туннеля занят ядром (Device or resource busy)",
		Summary:  "Роутер не может отключить или удалить сетевой интерфейс opkgtun, так как его сокет удерживается зависшим процессом.",
		Cause:    "Процесс маршрутизации или демон NDMS держит дескриптор виртуального сетевого адаптера в момент переключения.",
		ActionSteps: []string{
			"Подождите 3-5 секунд — ядро Linux часто освобождает сокет самостоятельно.",
			"В шторке управления туннелями нажмите «Перезапустить туннель».",
			"Если интерфейс завис, в терминале Entware выполните: `ip link set dev opkgtunX down`.",
			"В крайнем случае выполните мягкую перезагрузку роутера.",
		},
		Tip:        "Всегда останавливайте туннель перед удалением или перезаписью его конфигурационного файла.",
		ExampleLog: "RTNETLINK answers: Device or resource busy (ip link delete opkgtun1)",
		Keywords:   []string{"device or resource busy", "интерфейс занят", "opkgtun busy", "rtnetlink busy"},
		Match: func(text string) bool {
			return matchAny(text, "device or resource busy", "cannot remove interface: busy", "link delete opkgtun")
		},
	},
	{
		ID:       "awg_fwmark_loop_crypto",
		Category: "AmneziaWG",
		Code:     "AWG_FWMARK_LOOP",
		Title:    "Петля маркировки трафика (FWMARK Loop) в правилах маршрутизации",
		Summary:  "Собственные зашифрованные UDP-пакеты WireGuard попадают под правило перехвата и заворачиваются обратно в туннель.",
		Cause:    "Отсутствует исключение для fwmark или порта WireGuard в iptables mangle. Роутер шифрует пакет, пытается отправить на Endpoint, но снова захватывает его фаерволом.",
		ActionSteps: []string{
			"В AWG Manager откройте вкладку «Маршрутизация».",
			"Убедитесь, что включено системное исключение для исходящего трафика роутера.",
			"Проверьте, чтобы в конфигурации туннеля параметр FwMark (например, 51820) не совпадал с меткой TProxy (обычно 0x1 или 0x2).",
			"Перезапустите движок маршрутизации.",
		},
		Tip:        "Всегда проверяйте таблицу `ip rule`: системное правило `not fwmark 0x... table ...` защищает от петель шифрования.",
		ExampleLog: "kernel: packet looping detected on interface opkgtun0 (fwmark 0x1)",
		Keywords:   []string{"fwmark loop", "петля шифрования", "packet looping", "looping detected on interface"},
		Match: func(text string) bool {
			return matchAny(text, "packet looping detected", "fwmark routing loop", "wireguard packet looping")
		},
	},
	{
		ID:       "awg_kmod_api_version_mismatch",
		Category: "AmneziaWG",
		Code:     "AWG_API_VERSION_MISMATCH",
		Title:    "Несовместимость версии драйвера kmod-amneziawg с утилитой awg",
		Summary:  "Установленная утилита командной строки awg не может договориться с модулем ядра из-за разных версий протокола Genl/Netlink.",
		Cause:    "Обновление пакета awg-tools без обновления модуля ядра kmod-amneziawg (или наоборот).",
		ActionSteps: []string{
			"Подключитесь к роутеру по SSH.",
			"Выполните обновление обоих пакетов согласованно: `opkg update && opkg upgrade kmod-amneziawg awg-tools`.",
			"Перезагрузите модуль ядра: `rmmod amneziawg && modprobe amneziawg` (или перезагрузите роутер).",
			"Проверьте вывод команды `awg --version`.",
		},
		Tip:        "Всегда обновляйте пакеты ядра и утилиты управления одновременно.",
		ExampleLog: "awg: kernel module version mismatch: expected API v2, got API v1",
		Keywords:   []string{"api version mismatch", "kernel module version", "версия модуля ядра", "kmod mismatch"},
		Match: func(text string) bool {
			return matchAny(text, "kernel module version mismatch", "expected api", "incompatible kernel module")
		},
	},
	{
		ID:       "awg_mac_address_unsupported",
		Category: "AmneziaWG",
		Code:     "AWG_MAC_ADDR_NOT_SUPPORTED",
		Title:    "WireGuard не поддерживает MAC-адреса (Сетевой уровень L3)",
		Summary:  "Попытка назначить MAC-адрес интерфейсу WireGuard/AmneziaWG завершилась ошибкой ядра.",
		Cause:    "Интерфейсы WireGuard работают строго на 3-м сетевом уровне (IP Layer, Layer 3). У них нет канального уровня (Ethernet, Layer 2) и физических MAC-адресов.",
		ActionSteps: []string{
			"Удалите директиву `MACAddress=` или параметры клонирования MAC из настроек интерфейса.",
			"В KeeneticOS не привязывайте статические DHCP-аренды по MAC-адресу к туннелю WireGuard.",
			"Используйте IP-маршрутизацию вместо мостовых соединений (Bridge/TAP).",
			"Перезапустите туннель.",
		},
		Tip:        "Если вам необходим полный Layer 2 с сохранением широковещательного трафика (Broadcast/mDNS), используйте OpenVPN в режиме TAP или VXLAN.",
		ExampleLog: "ip: link set dev opkgtun0 address: Operation not supported",
		Keywords:   []string{"operation not supported address", "mac address wireguard", "layer 3 only", "mac-адрес"},
		Match: func(text string) bool {
			return matchAny(text, "link set dev opkgtun", "address: operation not supported", "wireguard does not support mac")
		},
	},
	{
		ID:       "awg_ipv6_disabled_in_kernel",
		Category: "AmneziaWG",
		Code:     "AWG_IPV6_DISABLED",
		Title:    "IPv6 отключен в ядре роутера, но указан в конфигурации туннеля",
		Summary:  "Команда назначения IPv6-адреса туннелю отклонена, так как стек IPv6 деактивирован в настройках KeeneticOS.",
		Cause:    "В конфигурации туннеля в параметре `Address` указан IPv6 (например, `fd00::2/64`), но в прошивке роутера выключен компонент IPv6.",
		ActionSteps: []string{
			"В AWG Manager откройте настройки туннеля.",
			"В поле `Address` оставьте только IPv4 адрес (например, `10.8.0.2/32`), удалив IPv6 подсеть.",
			"В секции `AllowedIPs` удалите `::/0`.",
			"Либо включите поддержку IPv6 в веб-интерфейсе Keenetic: «Сетевые правила» → «Подключение по IPv6».",
		},
		Tip:        "Для большинства зарубежных VPN достаточно чистого IPv4 подключения.",
		ExampleLog: "RTNETLINK answers: Permission denied (ip -6 addr add fd00::2/64 dev opkgtun0)",
		Keywords:   []string{"ipv6 disabled", "ip -6 addr add", "permission denied ipv6", "ipv6 отключен"},
		Match: func(text string) bool {
			return matchAny(text, "ip -6 addr add", "cannot assign requested address", "permission denied (ip -6")
		},
	},
	{
		ID:       "awg_endpoint_port_zero",
		Category: "AmneziaWG",
		Code:     "AWG_INVALID_PORT_ZERO",
		Title:    "Указан недопустимый порт сервера (Порт 0)",
		Summary:  "WireGuard отклонил адрес эндпоинта, так как номер порта равен нулю или отсутствует.",
		Cause:    "Синтаксическая ошибка в параметре `Endpoint = host:port`. Номер порта не может быть 0 или больше 65535.",
		ActionSteps: []string{
			"Откройте конфигурацию туннеля на вкладке «Туннели».",
			"Проверьте поле `Endpoint`. Убедитесь, что после двоеточия указан корректный порт (например, `:51820` или `:443`).",
			"Убедитесь, что в адресе хоста нет лишних пробелов или случайных символов.",
			"Сохраните настройки туннеля.",
		},
		Tip:        "Если сервер использует доменное имя с IPv6, формат записи должен быть: `[2001:db8::1]:51820`.",
		ExampleLog: "awg: invalid endpoint port: 0",
		Keywords:   []string{"invalid endpoint port", "порт 0", "port zero", "endpoint port"},
		Match: func(text string) bool {
			return matchAny(text, "invalid endpoint port", "endpoint port cannot be 0", "port: 0")
		},
	},
	{
		ID:       "awg_client_ip_netmask_invalid",
		Category: "AmneziaWG",
		Code:     "AWG_NETMASK_SLASH_24_WARNING",
		Title:    "Некорректная маска адреса клиента (/24 вместо /32)",
		Summary:  "Клиентский адрес туннеля указан с маской /24. Это может приводить к перехвату всей виртуальной подсети и потере связи с сервером.",
		Cause:    "В WireGuard интерфейс клиента должен иметь точечный адрес хоста `/32` (для IPv4) или `/128` (для IPv6), а не целую широковещательную сеть `/24`.",
		ActionSteps: []string{
			"В свойствах туннеля в поле `Address` замените маску `/24` на `/32` (например: `10.8.0.5/32`).",
			"Сохраните конфигурацию и перезапустите интерфейс.",
			"Убедитесь, что связь с пиром восстановилась.",
		},
		Tip:        "Маска `/24` используется только на самом сервере, где к интерфейсу подключаются множество клиентов.",
		ExampleLog: "wireguard: address 10.8.0.2/24 assigned without gateway peer",
		Keywords:   []string{"slash 24", "маска /24", "netmask /32", "address client mask"},
		Match: func(text string) bool {
			return matchAny(text, "assigned without gateway peer", "address /24 on client wireguard", "invalid client netmask")
		},
	},
	{
		ID:       "awg_multi_tunnel_table_conflict",
		Category: "AmneziaWG",
		Code:     "AWG_TABLE_COLLISION",
		Title:    "Конфликт таблиц маршрутизации при двух активных туннелях",
		Summary:  "Два разных туннеля пытаются использовать один и тот же номер таблицы маршрутизации Linux (Table ID).",
		Cause:    "Каждый активный туннель с собственным шлюзом должен направлять маршруты в изолированную таблицу маршрутизации.",
		ActionSteps: []string{
			"В AWG Manager перейдите на вкладку «Маршрутизация».",
			"Проверьте список активных политик и привязку интерфейсов к таблицам.",
			"В свойствах второго туннеля выберите уникальный номер таблицы маршрутизации (например, 1001 и 1002).",
			"Перезапустите оба туннеля.",
		},
		Tip:        "AWG Manager автоматически назначает уникальные идентификаторы таблиц при создании туннелей через графический мастер.",
		ExampleLog: "ip route add default: File exists (table 1000 already has default via opkgtun0)",
		Keywords:   []string{"table already has default", "table conflict", "конфликт таблиц маршрутизации", "table collision"},
		Match: func(text string) bool {
			return matchAny(text, "table already has default", "table collision between tunnels", "file exists in routing table")
		},
	},
	{
		ID:       "awg_tx_queue_len_dropped",
		Category: "AmneziaWG",
		Code:     "AWG_TX_DROPS",
		Title:    "Потери пакетов из-за переполнения очереди передачи (TX Queue Drops)",
		Summary:  "Процессор роутера не успевает шифровать исходящие пакеты на высокой скорости, вызывая рост счетчика TX Drops.",
		Cause:    "Размер очереди `txqueuelen` по умолчанию (обычно 500-1000) недостаточен при пиковых всплесках сетевого трафика (например, при скачивании через торрент).",
		ActionSteps: []string{
			"Подключитесь к роутеру по SSH.",
			"Увеличьте длину очереди интерфейса туннеля: `ifconfig opkgtun0 txqueuelen 2000`.",
			"В AWG Manager в настройках туннеля включите аппаратную разгрузку, если она поддерживается процессором роутера.",
			"Уменьшите максимальную скорость скачивания в торрент-клиенте на 10-15%.",
		},
		Tip:        "Счетчик дропов можно отслеживать на вкладке «Статистика» или командой `ip -s link show opkgtun0`.",
		ExampleLog: "net_ratelimit: 45 callbacks suppressed, opkgtun0: tx drop limit reached",
		Keywords:   []string{"tx drop", "txqueuelen", "очередь передачи", "дропы пакетов"},
		Match: func(text string) bool {
			return matchAny(text, "tx drop limit reached", "txqueuelen overflow", "callbacks suppressed, opkgtun")
		},
	},
	{
		ID:       "awg_packet_mac2_cookie_required",
		Category: "AmneziaWG",
		Code:     "AWG_COOKIE_REQUIRED",
		Title:    "Сервер перегружен и запрашивает Cookie защиты от DoS (MAC2)",
		Summary:  "Сервер WireGuard находится под высокой нагрузкой и требует вычисления криптографического cookie (MAC2) перед установкой сессии.",
		Cause:    "Встроенный в протокол механизм защиты от DoS-атак. Если ответ с cookie теряется на DPI провайдера, сессия не устанавливается.",
		ActionSteps: []string{
			"Подождите 30-60 секунд для снижения нагрузки на стороне VPN-сервера.",
			"Если туннель не поднимается, перезапустите его в панели управления.",
			"Проверьте, не находится ли IP-адрес вашего сервера под распределенной атакой (DDoS).",
			"При наличии нескольких серверов переключитесь на резервный узел.",
		},
		Tip:        "Наличие параметров обфускации AmneziaWG (Jc, S1, S2) снижает вероятность ложного срабатывания защиты сервером.",
		ExampleLog: "wireguard: received cookie response, calculating mac2 for handshake",
		Keywords:   []string{"cookie response", "mac2 required", "under load cookie", "dos protection wireguard"},
		Match: func(text string) bool {
			return matchAny(text, "received cookie response", "cookie required for handshake", "under load, dropping handshake without mac2")
		},
	},

	// =========================================================================
	// 3. MIHOMO (CLASH.META ДВИЖОК, ПРОВАЙДЕРЫ, ПОРТЫ)
	// =========================================================================
	{
		ID:       "mihomo_mixed_port_in_use",
		Category: "Mihomo",
		Code:     "MIHOMO_PORT_7890_IN_USE",
		Title:    "Входящий порт 7890 (mixed-port) уже занят другим процессом",
		Summary:  "Ядро Mihomo не может запуститься, так как порт 7890 (или 10808) уже слушает другой сервис на роутере.",
		Cause:    "Одновременный запуск Sing-box и Mihomo на одинаковых портах, либо незавершенный предыдущий экземпляр mihomo.",
		ActionSteps: []string{
			"В боковой панели статуса проверьте, не запущен ли Sing-box: остановите его перед стартом Mihomo.",
			"Перейдите в меню «Инструменты» -> «Система» -> «Порты».",
			"Найдите порт 7890 и проверьте, какой процесс его занимает.",
			"При необходимости измените mixed-port в YAML-конфигурации Mihomo (например, на 7895).",
		},
		Tip:        "Mihomo и Sing-box не должны использовать одинаковые входящие порты прокси.",
		ExampleLog: "level=error msg=\"Start Mixed(mixed-in) server error: listen tcp :7890: bind: address already in use\"",
		Keywords:   []string{"7890", "mixed-port", "address already in use", "порт уже занят mihomo"},
		Match: func(text string) bool {
			return matchAny(text, "listen tcp :7890: bind: address already in use", "start mixed(mixed-in) server error")
		},
	},
	{
		ID:       "mihomo_redir_tproxy_port_in_use",
		Category: "Mihomo",
		Code:     "MIHOMO_TPROXY_PORT_BUSY",
		Title:    "Порт перехвата TProxy / Redir (7892 / 7895) уже занят",
		Summary:  "Mihomo не может открыть сокет перехвата прозрачного проксирования трафика роутера.",
		Cause:    "Другой демон прозрачного прокси (redsocks, sing-box tproxy или v2ray) уже прослушивает порт tproxy/redir.",
		ActionSteps: []string{
			"Перейдите в раздел «Маршрутизация» -> «Параметры ядра».",
			"Остановите Sing-box или другие сторонние прокси-демоны.",
			"Проверьте настройки tproxy-port в конфигурации Mihomo.",
			"Перезапустите службу ядра Mihomo.",
		},
		Tip:        "В режиме TProxy ядро должно монопольно владеть сокетом перехвата пакетов.",
		ExampleLog: "level=fatal msg=\"Start TProxy server error: listen udp :7892: bind: address already in use\"",
		Keywords:   []string{"tproxy-port", "start tproxy server error", "7892", "redir-port"},
		Match: func(text string) bool {
			return matchAny(text, "start tproxy server error", "start redir server error", ":7892: bind: address already in use")
		},
	},
	{
		ID:       "mihomo_api_port_in_use",
		Category: "Mihomo",
		Code:     "MIHOMO_API_PORT_BUSY",
		Title:    "Порт REST API управления Mihomo (external-controller 9090) занят",
		Summary:  "Веб-интерфейс и панели управления (Yacd/MetaCubeXD) не могут подключиться к API ядра.",
		Cause:    "Порт 9090 занят другой службой, либо старый процесс mihomo завис.",
		ActionSteps: []string{
			"В «Инструменты» -> «Система» -> «Процессы» завершите зависшие процессы mihomo.",
			"В конфигурации Mihomo (external-controller) проверьте порт: при конфликте замените 9090 на 9095.",
			"Перезапустите движок.",
		},
		Tip:        "External-controller необходим для проверки задержек и переключения прокси-групп.",
		ExampleLog: "level=error msg=\"External controller listen error: listen tcp :9090: bind: address already in use\"",
		Keywords:   []string{"external-controller", "9090", "api port busy", "external controller listen error"},
		Match: func(text string) bool {
			return matchAny(text, "external controller listen error", ":9090: bind: address already in use")
		},
	},
	{
		ID:       "mihomo_yaml_tab_error",
		Category: "Mihomo",
		Code:     "MIHOMO_YAML_TAB_CHAR",
		Title:    "Синтаксическая ошибка YAML: символ табуляции вместо пробелов",
		Summary:  "Парсер Mihomo завершился с ошибкой: стандарт YAML категорически запрещает использование символов табуляции (Tab).",
		Cause:    "При ручной правке config.yaml или копировании из текстового редактора в отступы попали символы табуляции (\\t).",
		ActionSteps: []string{
			"В меню «Маршрутизация» нажмите кнопку «Конфиг» (YAML редактор).",
			"Найдите указанную в ошибке строку.",
			"Замените все символы табуляции (клавиша Tab) на стандартные 2 пробела.",
			"Сохраните конфигурационный файл.",
		},
		Tip:        "В YAML для отступов разрешено использовать только пробелы, табуляции запрещены спецификацией.",
		ExampleLog: "level=fatal msg=\"Parse config error: yaml: line 42: found character that cannot start any token\"",
		Keywords:   []string{"cannot start any token", "yaml: line", "табуляция в yaml", "ошибка yaml конфига"},
		Match: func(text string) bool {
			return matchAny(text, "found character that cannot start any token", "yaml: line", "found unexpected tab")
		},
	},
	{
		ID:       "mihomo_yaml_mapping_error",
		Category: "Mihomo",
		Code:     "MIHOMO_YAML_MAPPING",
		Title:    "Ошибка разметки YAML: нарушена структура вложенности (mapping values)",
		Summary:  "Парсер конфигурации Mihomo не смог разобрать структуру словарей и списков.",
		Cause:    "Пропущен пробел после двоеточия (например, 'port:7890' вместо 'port: 7890') или неверное количество пробелов в начале строки.",
		ActionSteps: []string{
			"Откройте «Конфиг» в разделе «Маршрутизация».",
			"Перейдите к строке, номер которой указан в логе ошибки.",
			"Убедитесь, что после каждого двоеточия ':' обязательно стоит пробел перед значением.",
			"Проверьте, что вложенные свойства имеют ровный отступ ровно в 2 пробела.",
		},
		Tip:        "В YAML пробел после двоеточия обязателен для всех ключей словаря.",
		ExampleLog: "level=fatal msg=\"Parse config error: yaml: line 18: mapping values are not allowed in this context\"",
		Keywords:   []string{"mapping values are not allowed", "ошибка отступов yaml", "структура yaml"},
		Match: func(text string) bool {
			return matchAny(text, "mapping values are not allowed in this context", "did not find expected key")
		},
	},
	{
		ID:       "mihomo_provider_download_failed",
		Category: "Mihomo",
		Code:     "MIHOMO_PROVIDER_DOWNLOAD_FAIL",
		Title:    "Сбой автоматической загрузки proxy-provider или rule-provider",
		Summary:  "Mihomo не смог скачать внешние правила или список прокси по указанной в конфигурации ссылке.",
		Cause:    "Удаленный сервер вернул HTTP 403/404, либо на роутере нет прямого доступа к серверу провайдера (блокировка провайдером или Cloudflare).",
		ActionSteps: []string{
			"В разделе «Маршрутизация» проверьте статус ваших внешних провайдеров правил.",
			"Попробуйте открыть URL провайдера в браузере с ПК для проверки доступности ссылки.",
			"Если провайдер требует прокси для скачивания, укажите для него параметр 'proxy: DIRECT' или выберите рабочий прокси-узел.",
			"Нажмите «Обновить провайдеры».",
		},
		Tip:        "Провайдеры правил могут кешироваться локально на USB-диске для работы в офлайне.",
		ExampleLog: "level=error msg=\"[RuleProvider] download error: Get https://cdn.example.com/rules: dial tcp: i/o timeout\"",
		Keywords:   []string{"ruleprovider download error", "proxyprovider download error", "сбой загрузки провайдера", "provider download failed"},
		Match: func(text string) bool {
			return matchAny(text, "[ruleprovider] download error", "[proxyprovider] download error", "provider download error")
		},
	},
	{
		ID:       "mihomo_geoip_corrupted",
		Category: "Mihomo",
		Code:     "MIHOMO_GEOIP_CORRUPTED",
		Title:    "Поврежден или отсутствует файл базы данных GeoIP (geoip.dat / Country.mmdb)",
		Summary:  "Mihomo не смог загрузить базу гео-локации IP-адресов для работы правил GEOIP,CN / GEOIP,RU.",
		Cause:    "Файл базы данных скачался не полностью из-за обрыва соединения или повреждения файловой системы на USB-диске.",
		ActionSteps: []string{
			"В интерфейсе перейдите в «Маршрутизация» -> «Базы Geodata».",
			"Нажмите кнопку «Обновить базы GeoIP / GeoSite».",
			"Система скачает свежие эталонные базы MetaCubeX и сохранит их в /opt/etc/mihomo/.",
			"Перезапустите Mihomo.",
		},
		Tip:        "Без корректной базы GeoIP правила маршрутизации по странам перестают работать.",
		ExampleLog: "level=fatal msg=\"Initial GeoIP error: load geoip database error: corrupted data\"",
		Keywords:   []string{"geoip database error", "corrupted geoip", "битая база geoip", "initial geoip error"},
		Match: func(text string) bool {
			return matchAny(text, "load geoip database error", "initial geoip error", "corrupted geoip")
		},
	},
	{
		ID:       "mihomo_geosite_corrupted",
		Category: "Mihomo",
		Code:     "MIHOMO_GEOSITE_CORRUPTED",
		Title:    "Поврежден или отсутствует файл базы данных GeoSite (geosite.dat)",
		Summary:  "Mihomo не может сопоставить домены по категориям GEOSITE (youtube, telegram, openai).",
		Cause:    "Бинарный файл geosite.dat имеет нулевой размер или поврежден.",
		ActionSteps: []string{
			"В меню «Маршрутизация» -> «Базы Geodata» нажмите «Переустановить GeoSite».",
			"Дождитесь завершения скачивания базы.",
			"Перезапустите движок маршрутизации.",
		},
		Tip:        "Для экономии оперативной памяти роутера рекомендуется использовать облегченную базу domain-list-community.",
		ExampleLog: "level=fatal msg=\"Initial GeoSite error: load geosite database error: unexpected EOF\"",
		Keywords:   []string{"geosite database error", "geosite error", "битая база geosite", "initial geosite error"},
		Match: func(text string) bool {
			return matchAny(text, "load geosite database error", "initial geosite error", "corrupted geosite")
		},
	},
	{
		ID:       "mihomo_fakeip_pool_exhausted",
		Category: "Mihomo",
		Code:     "MIHOMO_FAKEIP_EXHAUSTED",
		Title:    "Исчерпан пул виртуальных адресов Fake-IP (198.18.0.0/16)",
		Summary:  "DNS-модуль Mihomo исчерпал таблицу соответствия доменов виртуальным IP-адресам Fake-IP.",
		Cause:    "Огромное количество уникальных доменных запросов (торренты, сканеры сети, боты), переполнивших локальное хранилище сопоставлений.",
		ActionSteps: []string{
			"В настройках DNS Mihomo очистите кеш Fake-IP.",
			"В конфигурации расширьте диапазон fake-ip-range (например, до 198.18.0.0/15).",
			"Включите параметр fake-ip-filter для исключения служебных доменов локальной сети (lan, local, keenetic).",
			"Перезапустите службу.",
		},
		Tip:        "Используйте fake-ip-filter для предотвращения засорения таблицы локальными именами устройств.",
		ExampleLog: "level=error msg=\"[DNS] fake-ip pool exhausted, unable to allocate new address\"",
		Keywords:   []string{"fake-ip pool exhausted", "исчерпан fake-ip", "пул fakeip переполнен", "unable to allocate fake-ip"},
		Match: func(text string) bool {
			return matchAny(text, "fake-ip pool exhausted", "fakeip pool exhausted", "unable to allocate new address")
		},
	},
	{
		ID:       "mihomo_dialer_proxy_missing",
		Category: "Mihomo",
		Code:     "MIHOMO_DIALER_PROXY_NOT_FOUND",
		Title:    "Указанный в цепочке узел dialer-proxy не найден в списке прокси",
		Summary:  "Попытка запуска прокси-цепочки (Proxy Chaining), ссылающейся на несуществующий сервер.",
		Cause:    "В описании прокси указан параметр 'dialer-proxy: ProxyA', но узел с именем 'ProxyA' был переименован или удален из конфигурации.",
		ActionSteps: []string{
			"Откройте конфигурацию прокси в AWG Manager.",
			"Проверьте имена узлов в цепочке проксирования.",
			"Убедитесь, что родительский прокси-сервер с точно таким же именем существует в списке активных серверов.",
		},
		Tip:        "Цепочки прокси требуют строгого соответствия имен промежуточных узлов.",
		ExampleLog: "level=error msg=\"Proxy [ChainNode] dialer-proxy [BaseNode] not found\"",
		Keywords:   []string{"dialer-proxy not found", "узел цепочки не найден", "proxy chain missing", "dialer-proxy"},
		Match: func(text string) bool {
			return matchAny(text, "dialer-proxy", "not found")
		},
	},
	{
		ID:       "mihomo_url_test_all_dead",
		Category: "Mihomo",
		Code:     "MIHOMO_URL_TEST_ALL_DEAD",
		Title:    "Все узлы в группе авто-выбора (url-test) недоступны",
		Summary:  "Группа автоматического выбора самого быстрого сервера не может работать, так как ни один из серверов не ответил на проверку задержки.",
		Cause:    "Серверы подписки заблокированы провайдером, истек срок действия подписки, либо тестовый URL проверки задержки (generate_204) недоступен.",
		ActionSteps: []string{
			"В разделе «Маршрутизация» перейдите в список групп прокси.",
			"Нажмите «Проверить задержки» для ручного тестирования серверов.",
			"Если все серверы показывают таймаут, проверьте интернет-соединение на роутере.",
			"В настройках группы измените тестовый URL на http://cp.cloudflare.com/generate_204.",
		},
		Tip:        "Используйте надежный тестовый URL, не блокируемый в вашем регионе.",
		ExampleLog: "level=warn msg=\"[URLTest] All proxies in group [AUTO] failed healthcheck\"",
		Keywords:   []string{"all proxies in group failed", "urltest all dead", "все узлы недоступны", "сбой проверки задержки"},
		Match: func(text string) bool {
			return matchAny(text, "all proxies in group", "failed healthcheck", "no alive proxies")
		},
	},
	{
		ID:       "mihomo_tun_dev_missing",
		Category: "Mihomo",
		Code:     "MIHOMO_TUN_DEV_MISSING",
		Title:    "Не удалось создать сетевое устройство TUN (/dev/net/tun missing)",
		Summary:  "Mihomo не может запустить TUN-режим, так как в операционной системе роутера отсутствует драйвер TUN.",
		Cause:    "В ядре KeeneticOS отключен или не загружен модуль tun.ko, либо отсутствует символическая ссылка /dev/net/tun.",
		ActionSteps: []string{
			"В AWG Manager перейдите во вкладку «Терминал».",
			"Выполните: mkdir -p /dev/net && mknod /dev/net/tun c 10 200 && chmod 666 /dev/net/tun.",
			"В настройках маршрутизации вместо режима TUN выберите режим TProxy (рекомендуется для Keenetic).",
			"Перезапустите движок.",
		},
		Tip:        "Режим TProxy является нативным и наиболее производительным для роутеров Keenetic.",
		ExampleLog: "level=fatal msg=\"Create Tun interface error: open /dev/net/tun: no such file or directory\"",
		Keywords:   []string{"/dev/net/tun", "create tun interface error", "отсутствует tun устройство", "tun no such file"},
		Match: func(text string) bool {
			return matchAny(text, "create tun interface error", "open /dev/net/tun", "no such file or directory: /dev/net/tun")
		},
	},
	{
		ID:       "mihomo_vless_flow_udp_reject",
		Category: "Mihomo",
		Code:     "MIHOMO_VLESS_VISION_UDP",
		Title:    "VLESS Vision отклонил UDP трафик (xtls-rprx-vision only supports TCP)",
		Summary:  "UDP-пакеты (игры, голосовые звонки, DNS) не проходят через узел с протоколом VLESS и включенным режимом Vision.",
		Cause:    "Протокол шифрования Vision (xtls-rprx-vision) разработан только для TCP. Для UDP требуется отдельная инкапсуляция или fallback-узел.",
		ActionSteps: []string{
			"В настройках маршрутизации убедитесь, что UDP-трафик направляется через узел без Vision, либо включите директиву 'udp: true'.",
			"Для DNS-запросов используйте протоколы DoH/DoT через TCP.",
			"В карточке сервера проверьте поддержку UDP.",
		},
		Tip:        "Режим Vision работает только с TCP-потоками TLS 1.3.",
		ExampleLog: "level=warn msg=\"[VLESS] xtls-rprx-vision does not support UDP traffic, packet dropped\"",
		Keywords:   []string{"xtls-rprx-vision does not support udp", "vless vision udp", "сброс udp пакетов vision"},
		Match: func(text string) bool {
			return matchAny(text, "xtls-rprx-vision does not support udp", "vision does not support udp")
		},
	},
	{
		ID:       "mihomo_memory_oom",
		Category: "Mihomo",
		Code:     "MIHOMO_OOM_KILLED",
		Title:    "Mihomo аварийно завершен ядром из-за нехватки RAM (Out of Memory)",
		Summary:  "Процесс ядра Mihomo был принудительно убит механизмом Linux OOM Killer из-за переполнения оперативной памяти роутера.",
		Cause:    "Загрузка огромных наборов правил (rule-providers с сотнями тысяч доменов), тяжелых баз GeoSite или утечка буферов соединений.",
		ActionSteps: []string{
			"В разделе «Маршрутизация» отключите избыточные внешние наборы правил.",
			"В «Инструменты» -> «Система» создайте и подключите файл подкачки (Swap) на USB-диске (512 МБ - 1 ГБ).",
			"Используйте компактные бинарные списки правил вместо текстовых списков на сотни тысяч строк.",
			"Перезапустите службу Mihomo.",
		},
		Tip:        "Для роутеров с 128-256 МБ RAM наличие файла подкачки (Swap) на USB-накопителе обязательно.",
		ExampleLog: "Kernel: Out of memory: Kill process 12458 (mihomo) score 680 or sacrifice child",
		Keywords:   []string{"kill process mihomo", "out of memory mihomo", "нехватка ram mihomo", "oom killer mihomo"},
		Match: func(text string) bool {
			return matchAny(text, "mihomo: out of memory", "fatal: out of memory (mihomo)", "mihomo: memory allocation failed")
		},
	},
	{
		ID:       "mihomo_dns_nameserver_loop",
		Category: "Mihomo",
		Code:     "MIHOMO_DNS_LOOP",
		Title:    "Петля разрешения DNS в конфигурации Mihomo",
		Summary:  "Mihomo зациклился при попытке разрешить адрес собственного upstream DNS-сервера или перехватываемого трафика.",
		Cause:    "В секции `dns.nameserver` указан DNS-сервер (например, 127.0.0.1:53 или Fake-IP адрес), трафик которого заворачивается обратно в TProxy/TUN перехватчик.",
		ActionSteps: []string{
			"Откройте конфигурацию Mihomo (кнопка «Конфиг» на вкладке «Маршрутизация»).",
			"В блоке `dns:` проверьте список `nameserver:` и `fallback:`.",
			"Укажите прямые публичные IP-адреса DNS (например: `77.88.8.8`, `1.1.1.1`, `8.8.8.8`).",
			"В секции `rules:` убедитесь, что трафик к этим IP идет через `DIRECT`.",
			"Сохраните конфигурацию и перезапустите движок.",
		},
		Tip:        "Всегда направляйте DNS-запросы к upstream серверам напрямую через `DIRECT`, чтобы избежать блокировок и рекурсивных петель.",
		ExampleLog: "level=error msg=\"[DNS] resolve loop detected for domain: dns.quad9.net\"",
		Keywords:   []string{"dns loop detected", "петля dns mihomo", "resolve loop", "nameserver loop"},
		Match: func(text string) bool {
			return matchAny(text, "resolve loop detected", "dns loop detected", "infinite loop in dns resolver")
		},
	},
	{
		ID:       "mihomo_mode_unsupported",
		Category: "Mihomo",
		Code:     "MIHOMO_INVALID_MODE",
		Title:    "Неподдерживаемый режим маршрутизации в параметре mode",
		Summary:  "Mihomo отклонил значение параметра mode. Разрешены только режимы rule, global или direct.",
		Cause:    "Опечатка в конфигурационном файле в первой строке `mode: ...` (например, указано `script`, `manual` или заглавные буквы).",
		ActionSteps: []string{
			"Откройте конфигурационный файл Mihomo.",
			"Найдите строку `mode:` в начале файла.",
			"Установите значение: `mode: rule` (для маршрутизации по правилам) или `mode: global`.",
			"Сохраните файл и перезапустите ядро.",
		},
		Tip:        "Для роутера всегда рекомендуется режим `rule`, так как он разделяет российский и зарубежный трафик.",
		ExampleLog: "fatal: unmarshal config failed: unsupported mode 'script'",
		Keywords:   []string{"unsupported mode", "invalid mode", "режим маршрутизации", "mode: rule"},
		Match: func(text string) bool {
			return matchAny(text, "unsupported mode", "invalid mode in config", "mode must be rule, global or direct")
		},
	},
	{
		ID:       "mihomo_sub_ruleset_parse_fail",
		Category: "Mihomo",
		Code:     "MIHOMO_RULESET_PARSE_ERROR",
		Title:    "Ошибка синтаксиса внешнего набора правил (Rule-Set Parse Error)",
		Summary:  "Mihomo не смог прочитать скачанный rule-provider из-за поврежденной разметки YAML или неверного типа формата.",
		Cause:    "Провайдер правил вернул HTML-страницу ошибки 404/Cloudflare вместо чистого YAML списка доменов/IP.",
		ActionSteps: []string{
			"На вкладке «Маршрутизация» проверьте статус загрузки провайдеров правил.",
			"Откройте ссылку на rule-provider в браузере и проверьте, что она отдает чистый список доменов.",
			"Убедитесь, что в конфиге правильно указан `behavior: domain` или `behavior: ipcidr`.",
			"Принудительно очистите кэш правил в `/opt/etc/mihomo/rules/`.",
		},
		Tip:        "Используйте проверенные наборы правил (например, Loyalsoldier или MetaCubeX) с прямыми ссылками на raw GitHub.",
		ExampleLog: "level=error msg=\"[Rule] parse rule-provider error: yaml: unmarshal errors\"",
		Keywords:   []string{"parse rule-provider", "ruleset parse error", "rule provider yaml error", "rule-set"},
		Match: func(text string) bool {
			return matchAny(text, "parse rule-provider error", "ruleset parse error", "failed to load rule provider")
		},
	},
	{
		ID:       "mihomo_sniff_tls_sni_empty",
		Category: "Mihomo",
		Code:     "MIHOMO_SNIFF_TLS_EMPTY",
		Title:    "Сбой сниффинга доменного имени TLS SNI",
		Summary:  "Клиентское приложение отправило зашифрованный HTTPS-запрос без поля Server Name Indication (SNI) или с ECH.",
		Cause:    "Технологии Encrypted Client Hello (ECH) шифруют поле домена, не позволяя прозрачному прокси определить целевой сайт.",
		ActionSteps: []string{
			"В конфигурации Mihomo в блоке `sniffer:` добавьте исключение или отключите форсированный сброс.",
			"В браузере на клиентских ПК можно временно отключить ECH (`chrome://flags/#encrypted-client-hello`).",
			"Убедитесь, что в правилах маршрутизации предусмотрено резервное правило по IP (GEOIP, RU).",
			"Сохраните конфигурацию.",
		},
		Tip:        "Mihomo умеет использовать обратное разрешение Fake-IP для восстановления домена даже при включенном ECH.",
		ExampleLog: "level=warning msg=\"[Sniffer] failed to sniff TLS domain: empty SNI\"",
		Keywords:   []string{"empty sni", "failed to sniff tls", "сниффинг sni", "ech blocked"},
		Match: func(text string) bool {
			return matchAny(text, "failed to sniff tls", "empty sni in tls handshake", "sniffer domain empty")
		},
	},
	{
		ID:       "mihomo_tls_cert_invalid",
		Category: "Mihomo",
		Code:     "MIHOMO_CERT_UNTRUSTED",
		Title:    "Сбой проверки SSL/TLS сертификата прокси-сервера",
		Summary:  "Mihomo разорвал соединение с удаленным узлом, потому что его сертификат шифрования не прошел проверку подлинности.",
		Cause:    "Истекший срок действия сертификата, самоподписанный сертификат без доверенного CA или подмена трафика на оборудовании провайдера (MITM).",
		ActionSteps: []string{
			"В настройках ноды в секции `tls:` временно установите `skip-cert-verify: true` (только для тестирования!).",
			"Обновите корневые сертификаты роутера в Entware: `opkg update && opkg install ca-certificates`.",
			"Проверьте системное время роутера: при сбитых часах любой сертификат считается недействительным.",
			"Свяжитесь с поставщиком вашего VPN-сервера для обновления SSL-сертификата.",
		},
		Tip:        "Всегда держите системные часы роутера синхронизированными по NTP, иначе валидация TLS гарантированно откажет.",
		ExampleLog: "x509: certificate signed by unknown authority (dial tcp)",
		Keywords:   []string{"certificate signed by unknown authority", "x509 cert", "skip-cert-verify", "tls cert invalid"},
		Match: func(text string) bool {
			return matchAny(text, "certificate signed by unknown authority", "x509: certificate has expired", "certificate is not valid")
		},
	},
	{
		ID:       "mihomo_hysteria2_auth_failed",
		Category: "Mihomo",
		Code:     "MIHOMO_HY2_AUTH_FAILED",
		Title:    "Сбой авторизации в протоколе Hysteria 2",
		Summary:  "Сервер Hysteria 2 отклонил подключение из-за неверного пароля или несовпадения параметров обфускации.",
		Cause:    "В конфигурации прокси указан неверный пароль в поле `password:` или не совпадают типы обфускации `obfs:`.",
		ActionSteps: []string{
			"На вкладке «Подписки» обновите список серверов.",
			"Если узел добавлен вручную, проверьте пароль и секрет обфускации `obfs-password`.",
			"Убедитесь, что UDP трафик не блокируется вашим интернет-провайдером (Hysteria 2 работает строго по UDP).",
			"Перезапустите движок маршрутизации.",
		},
		Tip:        "В Hysteria 2 используется порт-хоппинг: убедитесь, что диапазон UDP портов открыт на фаерволе сервера.",
		ExampleLog: "hysteria2: connection closed by server: auth failed (code 401)",
		Keywords:   []string{"hysteria2 auth failed", "hy2 auth", "hysteria 401", "авторизация hysteria"},
		Match: func(text string) bool {
			return matchAny(text, "hysteria2: connection closed by server", "auth failed (code 401)", "hysteria auth failed")
		},
	},
	{
		ID:       "mihomo_tuic_congestion_control",
		Category: "Mihomo",
		Code:     "MIHOMO_TUIC_CC_UNSUPPORTED",
		Title:    "Неподдерживаемый алгоритм контроля перегрузки в узле TUIC",
		Summary:  "Сервер или клиент TUIC отклонил выбранный алгоритм управления сетевым потоком (congestion-controller).",
		Cause:    "В конфигурации ноды TUIC указан алгоритм `bbr` или `cubic`, не поддерживаемый данной версией библиотеки QUIC.",
		ActionSteps: []string{
			"Откройте конфигурацию ноды TUIC.",
			"В параметре `congestion-controller:` установите значение `bbr` или `cubic`.",
			"Попробуйте переключить параметр `udp-relay-mode:` в значение `native`.",
			"Сохраните настройки и перезапустите ядро.",
		},
		Tip:        "Алгоритм BBR обычно обеспечивает наилучшую скорость на каналах с потерями пакетов.",
		ExampleLog: "tuic: unknown congestion control algorithm: 'bbr2'",
		Keywords:   []string{"tuic congestion", "congestion control algorithm", "bbr", "tuic quic"},
		Match: func(text string) bool {
			return matchAny(text, "unknown congestion control algorithm", "tuic: unsupported cc", "tuic handshake error")
		},
	},
	{
		ID:       "mihomo_ss_cipher_unsupported",
		Category: "Mihomo",
		Code:     "MIHOMO_SS_CIPHER_UNSUPPORTED",
		Title:    "Устаревший или неподдерживаемый шифр Shadowsocks",
		Summary:  "Mihomo отказался запускать сервер Shadowsocks из-за небезопасного потокового шифра.",
		Cause:    "Устаревшие шифры (например, `rc4-md5`, `bf-cfb`, `aes-256-cfb`) уязвимы и исключены из современных ядер. Требуется AEAD шифрование.",
		ActionSteps: []string{
			"В настройках узла замените шифр на современный стандарт AEAD.",
			"Рекомендуемые шифры: `chacha20-ietf-poly1305` или `2022-blake3-aes-128-gcm`.",
			"Обновите конфигурацию на стороне Shadowsocks сервера.",
			"Перезапустите подключение.",
		},
		Tip:        "Для роутеров с аппаратным ускорением AES (ARMv8 / AArch64) выбирайте `aes-128-gcm` — он дает максимальную скорость при минимальном нагреве процессора.",
		ExampleLog: "shadowsocks: unsupported cipher: rc4-md5 (use modern AEAD ciphers)",
		Keywords:   []string{"unsupported cipher", "shadowsocks cipher", "rc4-md5", "шифр shadowsocks"},
		Match: func(text string) bool {
			return matchAny(text, "unsupported cipher", "cipher not supported in shadowsocks", "use modern aead ciphers")
		},
	},
	{
		ID:       "mihomo_ebpf_program_attach_fail",
		Category: "Mihomo",
		Code:     "MIHOMO_EBPF_ATTACH_FAILED",
		Title:    "Сбой загрузки eBPF программы перехвата трафика",
		Summary:  "Mihomo не смог прикрепить фильтр eBPF к сетевому сокету роутера.",
		Cause:    "В ядре KeeneticOS отсутствует подсистема eBPF (CONFIG_BPF / CONFIG_BPF_SYSCALL) либо превышен лимит памяти под eBPF карты.",
		ActionSteps: []string{
			"В настройках перехвата трафика переключите режим с eBPF на проверенный `TProxy` или `TUN`.",
			"В AWG Manager на вкладке «Настройки» выберите режим «TProxy (Netfilter)».",
			"Сохраните параметры и перезапустите движок маршрутизации.",
		},
		Tip:        "Режим TProxy является стандартом де-факто для роутеров Keenetic и обеспечивает 100% совместимость.",
		ExampleLog: "ebpf: attach TC program failed: operation not supported by kernel",
		Keywords:   []string{"attach tc program", "ebpf failed", "ebpf attach", "bpf not supported"},
		Match: func(text string) bool {
			return matchAny(text, "attach tc program failed", "ebpf: operation not supported", "bpf program load error")
		},
	},
	{
		ID:       "mihomo_rule_payload_type_mismatch",
		Category: "Mihomo",
		Code:     "MIHOMO_RULE_PAYLOAD_MISMATCH",
		Title:    "Несоответствие типа правила переданному значению",
		Summary:  "Mihomo обнаружил ошибку в правиле маршрутизации: значение не соответствует типу фильтра.",
		Cause:    "Например, в правиле `IP-CIDR` указано доменное имя (например, `IP-CIDR,google.com,PROXY`) или в `DOMAIN-SUFFIX` указан IP-адрес с маской.",
		ActionSteps: []string{
			"Откройте конфигурацию Mihomo или правила на вкладке «Маршрутизация».",
			"Для доменных имен используйте типы: `DOMAIN`, `DOMAIN-SUFFIX`, `DOMAIN-KEYWORD`.",
			"Для IP-адресов используйте: `IP-CIDR` (например: `10.0.0.0/8,DIRECT,no-resolve`).",
			"Исправьте синтаксис правила и сохраните файл.",
		},
		Tip:        "В конце правил `IP-CIDR` всегда добавляйте параметр `no-resolve`, чтобы роутер не выполнял лишних DNS-запросов.",
		ExampleLog: "level=error msg=\"[Rule] IP-CIDR parse error: invalid CIDR address 'example.com'\"",
		Keywords:   []string{"ip-cidr parse error", "invalid cidr address", "тип правила", "payload mismatch"},
		Match: func(text string) bool {
			return matchAny(text, "invalid cidr address", "ip-cidr parse error", "rule payload mismatch")
		},
	},
	{
		ID:       "mihomo_proxy_group_empty",
		Category: "Mihomo",
		Code:     "MIHOMO_PROXY_GROUP_EMPTY",
		Title:    "Прокси-группа не содержит ни одного рабочего сервера",
		Summary:  "Группа выбора серверов (select, url-test, fallback) пуста, поэтому запросы некуда перенаправлять.",
		Cause:    "В секции `proxy-groups:` в списке `proxies:` нет ни одного узла, либо ни один сервер из провайдера подписки не загрузился.",
		ActionSteps: []string{
			"На вкладке «Подписки» проверьте, что подписка успешно скачана и содержит активные серверы.",
			"В конфигурации Mihomo добавьте узел в группу вручную или укажите имя провайдера подписок в `use:`.",
			"В качестве запасного узла всегда добавляйте `DIRECT` в конец списка группы.",
			"Перезапустите движок.",
		},
		Tip:        "Добавление `DIRECT` в селектор гарантирует, что интернет не пропадет даже при падении всех прокси.",
		ExampleLog: "level=error msg=\"proxy group 'Auto' is empty, dropping traffic\"",
		Keywords:   []string{"proxy group is empty", "group is empty", "пустая группа", "нет серверов в группе"},
		Match: func(text string) bool {
			return matchAny(text, "is empty, dropping traffic", "proxy group has no proxies", "empty proxy group")
		},
	},
	{
		ID:       "mihomo_udp_nat_type_blocked",
		Category: "Mihomo",
		Code:     "MIHOMO_SYMMETRIC_NAT_BLOCK",
		Title:    "Симметричный NAT блокирует голосовую связь и P2P игры",
		Summary:  "Mihomo транслирует UDP через симметричный NAT, из-за чего не работает STUN и прямые соединения в играх и мессенджерах.",
		Cause:    "Входящие UDP сокеты в TUN/TProxy маппируются на разные порты для каждого удаленного адреса.",
		ActionSteps: []string{
			"В настройках Mihomo в секции `tun:` включите опцию: `endpoint-independent-nat: true` (Full Cone NAT).",
			"Для игр (Discord, Steam, PlayStation, Xbox) добавьте правила исключения в `DIRECT`.",
			"Проверьте, включен ли TProxy для UDP трафика на порту 7895.",
			"Перезапустите сетевой движок.",
		},
		Tip:        "Опция Full Cone NAT (`endpoint-independent-nat: true`) обеспечивает статус NAT Type B / Moderate на игровых консолях.",
		ExampleLog: "level=warning msg=\"[UDP] Symmetric NAT detected, STUN hole punching may fail\"",
		Keywords:   []string{"symmetric nat", "endpoint-independent-nat", "stun failed", "голос в discord", "nat type"},
		Match: func(text string) bool {
			return matchAny(text, "symmetric nat detected", "endpoint-independent-nat", "nat hole punching failed")
		},
	},
	{
		ID:       "mihomo_clash_core_version_old",
		Category: "Mihomo",
		Code:     "MIHOMO_SYNTAX_REQUIRES_META",
		Title:    "Конфигурация требует функций Mihomo (Meta-ядра)",
		Summary:  "Файл конфигурации содержит директивы (VLESS Reality, Hysteria 2, Rule-Set), недоступные в классическом Clash.",
		Cause:    "Попытка запуска старого классического бинарника Clash с конфигурационным файлом нового поколения Mihomo.",
		ActionSteps: []string{
			"Убедитесь, что в AWG Manager выбрано актуальное ядро `Mihomo`.",
			"Проверьте путь к бинарному файлу: `/opt/bin/mihomo`.",
			"Проверьте версию ядра командой в терминале: `mihomo -v`.",
			"Перезапустите движок через шторку управления.",
		},
		Tip:        "AWG Manager использует самое свежее ядро Mihomo с полной поддержкой всех современных протоколов обхода блокировок.",
		ExampleLog: "fatal: unknown field 'reality-opts' in proxy configuration",
		Keywords:   []string{"unknown field", "reality-opts", "требуется meta", "mihomo syntax"},
		Match: func(text string) bool {
			return matchAny(text, "unknown field reality-opts", "unknown field rule-providers", "requires mihomo meta core")
		},
	},
	{
		ID:       "mihomo_external_ui_not_found",
		Category: "Mihomo",
		Code:     "MIHOMO_EXTERNAL_UI_MISSING",
		Title:    "Не найдена папка внешней веб-панели (external-ui)",
		Summary:  "Mihomo не может отобразить встроенный веб-интерфейс (Yacd / Razord / Metacubexd), так как каталог не существует.",
		Cause:    "Параметр `external-ui:` в config.yaml указывает на несуществующий путь (например, `/opt/etc/mihomo/ui`).",
		ActionSteps: []string{
			"Создайте необходимый каталог в терминале: `mkdir -p /opt/etc/mihomo/ui`.",
			"Распакуйте архив панели управления в эту папку.",
			"Или используйте встроенный веб-интерфейс AWG Manager на порту 2222, не требующий сторонних панелей.",
			"В файле конфигурации закомментируйте строку `external-ui:`.",
		},
		Tip:        "Веб-интерфейс AWG Manager полностью заменяет сторонние панели управления и оптимизирован для мобильных телефонов.",
		ExampleLog: "level=error msg=\"[External Controller] external-ui directory does not exist: /opt/etc/mihomo/ui\"",
		Keywords:   []string{"external-ui directory", "ui does not exist", "yacd missing", "панель управления"},
		Match: func(text string) bool {
			return matchAny(text, "external-ui directory does not exist", "failed to load external-ui", "ui path not found")
		},
	},
	{
		ID:       "mihomo_secret_unauthorized",
		Category: "Mihomo",
		Code:     "MIHOMO_UNAUTHORIZED_401",
		Title:    "Ошибка 401: Неверный секретный токен REST API Mihomo",
		Summary:  "Веб-интерфейс или модуль мониторинга не может подключиться к REST API Mihomo (порт 9090) из-за несовпадения секрета.",
		Cause:    "В конфигурации задан параметр `secret: ...`, а запрос отправляется с пустым или неверным заголовком Authorization: Bearer.",
		ActionSteps: []string{
			"В файле `/opt/etc/mihomo/config.yaml` проверьте строку `secret:`.",
			"В AWG Manager на вкладке «Настройки» укажите тот же токен API.",
			"Если защита внешнего контроллера не требуется (порт слушает только локальный 127.0.0.1), очистите поле: `secret: \"\"`.",
			"Перезапустите Mihomo.",
		},
		Tip:        "При привязке REST API к 127.0.0.1:9090 оставлять секрет пустым безопасно, так как доступ извне роутера закрыт.",
		ExampleLog: "level=warning msg=\"[API] unauthorized request from 127.0.0.1:45122 to /traffic\"",
		Keywords:   []string{"unauthorized request", "secret mismatch", "401 api", "секретный токен"},
		Match: func(text string) bool {
			return matchAny(text, "unauthorized request from", "invalid secret token", "401 unauthorized on /api")
		},
	},
	{
		ID:       "mihomo_process_sniffing_unsupported",
		Category: "Mihomo",
		Code:     "MIHOMO_PROCESS_SNIFF_UNSUPPORTED",
		Title:    "Сниффинг имени клиентских процессов недоступен на роутере",
		Summary:  "Опция `find-process-mode` не может определить имена программ на смартфонах и ПК клиентов домашней сети.",
		Cause:    "Роутер маршрутизирует транзитный сетевой трафик других устройств через TProxy/TUN. Имена запущенных процессов известны только локально на самом клиенте.",
		ActionSteps: []string{
			"В файле конфигурации Mihomo установите: `find-process-mode: off`.",
			"В секции `rules:` не используйте правила `PROCESS-NAME` для клиентов домашней сети.",
			"Маршрутизируйте трафик по IP-адресам устройств (`SRC-IP-CIDR`) или доменам (`DOMAIN-SUFFIX`).",
			"Сохраните конфигурацию.",
		},
		Tip:        "Правила `PROCESS-NAME` работают только тогда, когда клиент Mihomo запущен непосредственно на самом компьютере с Windows или macOS.",
		ExampleLog: "level=warning msg=\"find-process-mode is only available for local traffic, disabled for transit\"",
		Keywords:   []string{"find-process-mode", "process-name transit", "имя процесса", "сниффинг процесса"},
		Match: func(text string) bool {
			return matchAny(text, "find-process-mode is only available", "process-name unsupported on router", "cannot sniff remote process name")
		},
	},

	// =========================================================================
	// 4. SING-BOX (ЯДРО, REALITY, VLESS, TPROXY)
	// =========================================================================
	{
		ID:       "singbox_reality_utls_missing",
		Category: "Sing-box",
		Code:     "SINGBOX_REALITY_UTLS_MISSING",
		Title:    "Отсутствует отпечаток uTLS в конфигурации Reality клиента",
		Summary:  "Sing-box отказался запускать VLESS Reality клиент, так как не указан отпечаток TLS браузера (uTLS fingerprint).",
		Cause:    "Протокол VLESS с расширением Reality требует обязательной маскировки отпечатка ClientHello (например, 'chrome', 'firefox', 'safari'). Без этого провайдер мгновенно распознает нестандартный TLS-клиент.",
		ActionSteps: []string{
			"Откройте конфигурацию исходящего узла (Outbound) в Sing-box.",
			"В секции 'tls' -> 'utls' добавьте поле: \"enabled\": true, \"fingerprint\": \"chrome\".",
			"Сохраните конфигурацию и перезапустите Sing-box.",
		},
		Tip:        "Для Reality всегда используйте отпечаток 'chrome' — он наиболее популярен и не вызывает подозрений у систем фильтрации трафика.",
		ExampleLog: "sing-box: fatal error: uTLS is required by reality client: tls.utls.enabled must be true",
		Keywords:   []string{"utls is required by reality", "tls.utls.enabled", "отпечаток reality", "reality fingerprint"},
		Match: func(text string) bool {
			return matchAny(text, "utls is required by reality client", "tls.utls.enabled must be true")
		},
	},
	{
		ID:       "singbox_xt_tproxy_missing",
		Category: "Sing-box",
		Code:     "SINGBOX_XT_TPROXY_MISSING",
		Title:    "Отсутствует модуль ядра Linux xt_TPROXY",
		Summary:  "Входящее подключение типа TProxy не может запуститься, так как ядро роутера не поддерживает перехват TProxy.",
		Cause:    "В установленной прошивке или ядре отсутствует модуль netfilter xt_TPROXY.ko, либо он не был загружен.",
		ActionSteps: []string{
			"В веб-интерфейсе перейдите во вкладку «Терминал» и выполните: modprobe xt_TPROXY.",
			"Если модуль не найден, переключите Sing-box в режим перехвата 'redirect' или используйте виртуальный интерфейс TUN.",
			"Убедитесь, что установлена актуальная версия KeeneticOS с поддержкой Netfilter.",
		},
		Tip:        "Режим TUN работает на уровне виртуального сетевого адаптера и не требует модуля xt_TPROXY.",
		ExampleLog: "sing-box: fatal: inbound/tproxy[tproxy-in]: create listener: setsockopt IP_TRANSPARENT failed: protocol not available",
		Keywords:   []string{"ip_transparent failed", "xt_tproxy", "tproxy protocol not available", "модуль xt_tproxy отсутствует"},
		Match: func(text string) bool {
			return matchAny(text, "ip_transparent failed", "setsockopt ip_transparent", "xt_tproxy")
		},
	},
	{
		ID:       "singbox_srs_version_mismatch",
		Category: "Sing-box",
		Code:     "SINGBOX_SRS_VERSION_MISMATCH",
		Title:    "Несовместимость версии скомпилированных правил SRS",
		Summary:  "Sing-box не смог прочитать бинарный файл rule-set (.srs) из-за несовпадения формата компилятора.",
		Cause:    "Файл .srs был скомпилирован более новой (или старой) версией sing-box rule-set compiler (например, формат версии 2 при установленном sing-box версии 1.8).",
		ActionSteps: []string{
			"В AWG Manager перейдите в «Маршрутизация» -> «Наборы правил».",
			"Нажмите «Перекомпилировать правила» или «Обновить наборы SRS».",
			"AWG Manager скачает правила, совместимые с текущей установленной версией ядра sing-box.",
			"Перезапустите Sing-box.",
		},
		Tip:        "Бинарные правила SRS строго привязаны к мажорной версии ядра Sing-box.",
		ExampleLog: "sing-box: fatal: read rule-set /opt/etc/sing-box/rules/geosite-youtube.srs: unsupported format version 2 (current binary only supports 1)",
		Keywords:   []string{"unsupported format version", ".srs", "rule-set version mismatch", "ошибка версии srs"},
		Match: func(text string) bool {
			return matchAny(text, "unsupported format version", "rule-set: unsupported format", "read rule-set")
		},
	},
	{
		ID:       "singbox_vless_uuid_invalid",
		Category: "Sing-box",
		Code:     "SINGBOX_INVALID_UUID",
		Title:    "Некорректный UUID пользователя в конфигурации VLESS / VMess",
		Summary:  "Ядро Sing-box не смогло распарсить поле 'uuid' в исходящем соединении.",
		Cause:    "Строка UUID содержит опечатки, пробелы или не соответствует формату 8-4-4-4-12 hex (например: c1234567-89ab-cdef-0123-456789abcdef).",
		ActionSteps: []string{
			"Откройте карточку сервера в разделе «Серверы» или «Подписки».",
			"Проверьте значение поля «UUID» (User ID).",
			"Убедитесь, что идентификатор содержит ровно 32 шестнадцатеричных символа и 4 дефиса.",
			"Скопируйте UUID заново из личного кабинета VPN-провайдера.",
		},
		Tip:        "Валидный UUID выглядит так: 12345678-1234-1234-1234-123456789abc.",
		ExampleLog: "sing-box: fatal: parse outbound[vless-out]: parse uuid: invalid UUID length: 35",
		Keywords:   []string{"parse uuid", "invalid uuid", "ошибка uuid", "битый uuid vless"},
		Match: func(text string) bool {
			return matchAny(text, "parse uuid: invalid uuid", "invalid uuid length", "failed to parse uuid")
		},
	},
	{
		ID:       "singbox_reality_pubkey_len",
		Category: "Sing-box",
		Code:     "SINGBOX_REALITY_PUBKEY_INVALID",
		Title:    "Неверный публичный ключ Reality (длина != 32 байта)",
		Summary:  "Sing-box отклонил параметр 'public_key' в блоке Reality.",
		Cause:    "Публичный ключ Reality сервера скопирован с ошибкой, содержит невалидные символы Base64 или не равен 32 байтам (43-44 символа Base64).",
		ActionSteps: []string{
			"В карточке сервера найдите параметр «Reality Public Key».",
			"Проверьте отсутствие начальных и конечных пробелов.",
			"Скопируйте ключ заново из конфигурационной ссылки vless://.",
			"Сохраните сервер и перезапустите маршрутизацию.",
		},
		Tip:        "Публичный ключ Reality всегда является ключом кривой Curve25519 длиной ровно 32 байта.",
		ExampleLog: "sing-box: fatal: parse reality config: invalid public_key: bad curve25519 key length",
		Keywords:   []string{"invalid public_key", "reality public_key", "bad curve25519 key", "ключ reality неверный"},
		Match: func(text string) bool {
			return matchAny(text, "invalid public_key", "bad curve25519 key length", "reality: invalid public key")
		},
	},
	{
		ID:       "singbox_shadowtls_handshake_fail",
		Category: "Sing-box",
		Code:     "SINGBOX_SHADOWTLS_HANDSHAKE_FAIL",
		Title:    "Сбой рукопожатия ShadowTLS (неверный пароль или маскировочный домен)",
		Summary:  "Протокол ShadowTLS не смог установить защищенную сессию с маскировочным веб-сервером.",
		Cause:    "Маскировочный домен (SNI) заблокирован, сервер перестал поддерживать TLS 1.3, либо не совпадает пароль ShadowTLS.",
		ActionSteps: []string{
			"В карточке подключения ShadowTLS проверьте параметр «Пароль» (password).",
			"Проверьте маскировочный домен (например, gateway.icloud.com): он должен отвечать по TLS 1.3.",
			"Попробуйте сменить маскировочный домен на другой популярный CDN-домен.",
		},
		Tip:        "ShadowTLS требует обязательной поддержки TLS 1.3 маскировочным сайтом.",
		ExampleLog: "sing-box: [outbound/shadowtls-out] handshake failed: tls: invalid challenge response from server",
		Keywords:   []string{"shadowtls handshake failed", "shadowtls", "challenge response", "ошибка shadowtls"},
		Match: func(text string) bool {
			return matchAny(text, "shadowtls] handshake failed", "shadowtls: invalid challenge", "shadowtls handshake")
		},
	},
	{
		ID:       "singbox_outbound_tag_duplicate",
		Category: "Sing-box",
		Code:     "SINGBOX_DUPLICATE_TAG",
		Title:    "Дублирующееся имя (tag) в исходящих или входящих соединениях",
		Summary:  "Sing-box не запустился, так как два разных блока Outbound или Inbound имеют одинаковый идентификатор 'tag'.",
		Cause:    "В конфигурации добавлены два сервера с одинаковым именем (например, 'proxy' или 'direct').",
		ActionSteps: []string{
			"В разделе «Маршрутизация» или «Конфиг» найдите блоки outbounds.",
			"Убедитесь, что каждый сервер имеет уникальное имя в поле \"tag\".",
			"Переименуйте повторяющийся тег (например, 'proxy-1' и 'proxy-2').",
			"Сохраните конфигурацию.",
		},
		Tip:        "Поле tag является уникальным ключом для ссылки в правилах маршрутизации.",
		ExampleLog: "sing-box: fatal: parse config: duplicate outbound tag: 'proxy'",
		Keywords:   []string{"duplicate outbound tag", "duplicate inbound tag", "дублирующийся тег", "повтор tag sing-box"},
		Match: func(text string) bool {
			return matchAny(text, "duplicate outbound tag", "duplicate inbound tag", "duplicate tag:")
		},
	},
	{
		ID:       "singbox_json_syntax_error",
		Category: "Sing-box",
		Code:     "SINGBOX_JSON_SYNTAX",
		Title:    "Синтаксическая ошибка в JSON-конфигурации Sing-box",
		Summary:  "Ядро не смогло распарсить config.json из-за синтаксической ошибки JSON (пропущенная запятая, лишняя скобка или кавычка).",
		Cause:    "Ручное редактирование конфигурационного файла без соблюдения формата JSON.",
		ActionSteps: []string{
			"Откройте «Конфиг Sing-box» в разделе «Маршрутизация».",
			"Обратите внимание на номер строки и смещение (offset), указанные в ошибке.",
			"Проверьте наличие закрывающих фигурных скобок '}' и запятых между элементами списков.",
			"Используйте кнопку «Проверить синтаксис» перед сохранением.",
		},
		Tip:        "В JSON последняя запись в словаре не должна оканчиваться запятой перед закрывающей скобкой.",
		ExampleLog: "sing-box: fatal error: decode config: invalid character '}' looking for beginning of value at offset 1420",
		Keywords:   []string{"invalid character looking for beginning of value", "decode config: invalid character", "ошибка json sing-box", "json syntax error"},
		Match: func(text string) bool {
			return matchAny(text, "decode config: invalid character", "invalid character '", "looking for beginning of value")
		},
	},
	{
		ID:       "singbox_ntp_time_drift",
		Category: "Sing-box",
		Code:     "SINGBOX_NTP_TIME_DRIFT",
		Title:    "Рассинхронизация времени роутера (TLS / Reality отклонен сервером)",
		Summary:  "Протоколы VLESS Reality, VMess и Hysteria2 не могут подключиться к серверу из-за расхождения системных часов роутера.",
		Cause:    "На роутере сбились системные часы (расхождение больше 60-90 секунд). Протоколы защиты от атак воспроизведения (Replay Attack) отклоняют пакеты с устаревшим временным штампом.",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic перейдите в «Управление» -> «Общие настройки» -> «Время и дата».",
			"Убедитесь, что включена «Синхронизация по протоколу NTP».",
			"Укажите серверы времени: pool.ntp.org, time.google.com, ntp1.stratum2.ru.",
			"Нажмите «Синхронизировать сейчас».",
			"После установки точного времени перезапустите Sing-box.",
		},
		Tip:        "Reality и VMess требуют точности часов роутера до 30 секунд.",
		ExampleLog: "sing-box: [outbound/vless-reality] tls: handshake failed: server rejected authentication ticket (timestamp expired)",
		Keywords:   []string{"timestamp expired", "time drift reality", "часы роутера сбились", "рассинхронизация времени ntp"},
		Match: func(text string) bool {
			return matchAny(text, "timestamp expired", "authentication ticket expired", "time drift detected")
		},
	},
	{
		ID:       "singbox_memory_oom",
		Category: "Sing-box",
		Code:     "SINGBOX_OOM_KILLED",
		Title:    "Sing-box завершен ядром роутера из-за нехватки RAM",
		Summary:  "Процесс sing-box аварийно закрылся из-за исчерпания свободной оперативной памяти роутера.",
		Cause:    "Одновременная работа нескольких тяжелых служб, большое количество одновременных TCP-соединений или включенный сбор подробной статистики.",
		ActionSteps: []string{
			"Создайте Swap-файл подкачки на USB-диске в меню «Инструменты» -> «Система».",
			"В настройках Sing-box отключите параметры детального логирования (debug -> info/warn).",
			"Уменьшите размер кеша правил DNS.",
			"Перезапустите службу.",
		},
		Tip:        "Для стабильной работы Sing-box на роутерах с 128 МБ RAM файл подкачки жизненно необходим.",
		ExampleLog: "Kernel: Out of memory: Kill process 18920 (sing-box) score 720",
		Keywords:   []string{"kill process sing-box", "out of memory sing-box", "падение sing-box oom"},
		Match: func(text string) bool {
			return matchAny(text, "sing-box: out of memory", "fatal: out of memory (sing-box)", "sing-box: memory allocation failed")
		},
	},
	{
		ID:       "singbox_route_rule_no_outbound",
		Category: "Sing-box",
		Code:     "SINGBOX_OUTBOUND_NOT_FOUND",
		Title:    "Правило маршрутизации ссылается на несуществующий outbound",
		Summary:  "Sing-box не может запуститься, потому что в правиле маршрутизации указан тег исходящего соединения (outbound), которого нет в конфигурации.",
		Cause:    "Опечатка в поле `outbound: ...` в секции `route.rules`. Имя тега должно в точности совпадать с полем `tag` одного из узлов в секции `outbounds`.",
		ActionSteps: []string{
			"Откройте конфигурацию Sing-box в AWG Manager.",
			"Проверьте список исходящих узлов в блоке `\"outbounds\"` и запишите их теги (например, `\"proxy-wg\"`, `\"direct\"`, `\"block\"`).",
			"В секции `\"route\": { \"rules\": [...] }` найдите правило с ошибочным именем.",
			"Замените имя тега на правильное или создайте недостающий outbound.",
			"Сохраните конфигурацию и перезапустите Sing-box.",
		},
		Tip:        "Используйте встроенные теги `direct` и `block` для исключений и блокировок рекламы.",
		ExampleLog: "FATAL[0000] parse route rule[3]: outbound 'my-vpn' not found",
		Keywords:   []string{"outbound not found", "parse route rule", "несуществующий outbound", "outbound tag missing"},
		Match: func(text string) bool {
			return matchAny(text, "outbound not found", "parse route rule", "outbound does not exist")
		},
	},
	{
		ID:       "singbox_inbound_tproxy_port_conflict",
		Category: "Sing-box",
		Code:     "SINGBOX_TPROXY_ADDR_IN_USE",
		Title:    "Входящий порт TProxy (10808) уже занят другим процессом",
		Summary:  "Sing-box не смог открыть сокет перехвата TProxy, так как порт 10808 удерживается другой запущенной службой (Mihomo, Xray или другим экземпляром).",
		Cause:    "Предыдущий процесс Sing-box не завершился корректно либо одновременно запущен Mihomo в режиме полного перехвата.",
		ActionSteps: []string{
			"В верхней панели управления нажмите кнопку «Остановить движок».",
			"Подождите 3 секунды, чтобы ядро освободило сетевой порт 10808.",
			"Если порт завис, в терминале найдите процесс: `netstat -tlpn | grep 10808` и завершите его: `kill -9 <PID>`.",
			"Нажмите «Запустить движок».",
		},
		Tip:        "Всегда останавливайте текущий движок перед переключением между Sing-box и Mihomo.",
		ExampleLog: "FATAL[0000] start inbound/tproxy[0]: listen tcp :10808: bind: address already in use",
		Keywords:   []string{"tproxy address already in use", "10808", "bind: address already in use", "порт tproxy занят"},
		Match: func(text string) bool {
			return matchAny(text, "start inbound/tproxy", ":10808: bind: address already in use")
		},
	},
	{
		ID:       "singbox_tun_auto_route_conflict",
		Category: "Sing-box",
		Code:     "SINGBOX_AUTO_ROUTE_COLLISION",
		Title:    "Конфликт auto_route с существующей таблицей маршрутизации",
		Summary:  "Опция `auto_route: true` попыталась переписать шлюз по умолчанию роутера и вступила в конфликт с правилами KeeneticOS.",
		Cause:    "KeeneticOS управляет маршрутизацией через свои таблицы (NDMS RCI). Прямое вмешательство Sing-box в главную таблицу main нарушает работу роутера.",
		ActionSteps: []string{
			"В секции `inbounds` для TUN адаптера отключите: `\"auto_route\": false`.",
			"Включите `\"strict_route\": false`.",
			"Позвольте AWG Manager управлять правилами маршрутизации через системный демон.",
			"Перезапустите движок Sing-box.",
		},
		Tip:        "На роутерах Keenetic маршрутизация настраивается через политики KeeneticOS и TProxy, а не через прямой takeover шлюза.",
		ExampleLog: "FATAL[0000] configure tun interface: route add default dev tun0: File exists",
		Keywords:   []string{"auto_route conflict", "route add default dev tun", "file exists tun", "strict_route"},
		Match: func(text string) bool {
			return matchAny(text, "route add default dev tun", "configure tun interface: route", "auto_route failed")
		},
	},
	{
		ID:       "singbox_experimental_v2ray_api_fail",
		Category: "Sing-box",
		Code:     "SINGBOX_V2RAY_API_ERROR",
		Title:    "Сбой инициализации V2Ray Stats API",
		Summary:  "Sing-box не смог запустить сервис сбора статистики трафика на локальном сокете.",
		Cause:    "Порт сервиса статистики уже занят или нарушена структура блока `experimental.v2ray_api` в конфигурации.",
		ActionSteps: []string{
			"Проверьте блок `\"experimental\"` в файле config.json.",
			"Убедитесь, что порт статистики (например, 10085) не конфликтует со сторонними пакетами.",
			"Если статистика не требуется в реальном времени, удалите блок `v2ray_api`.",
			"Перезапустите Sing-box.",
		},
		Tip:        "AWG Manager собирает статистику интерфейсов через системный netlink без необходимости тяжелого gRPC API.",
		ExampleLog: "FATAL[0000] initialize service/v2ray-api: listen tcp 127.0.0.1:10085: bind: address already in use",
		Keywords:   []string{"v2ray-api", "v2ray api listen", "experimental stats", "10085"},
		Match: func(text string) bool {
			return matchAny(text, "service/v2ray-api", "initialize service/v2ray-api", "v2ray-api: listen tcp")
		},
	},
	{
		ID:       "singbox_vmess_security_zero",
		Category: "Sing-box",
		Code:     "SINGBOX_VMESS_SECURITY_REJECTED",
		Title:    "Небезопасный режим VMess security: zero отклонен",
		Summary:  "Sing-box отказался устанавливать соединение по протоколу VMess без шифрования.",
		Cause:    "Режим `security: \"zero\"` или `\"none\"` в VMess без внешнего TLS шифрования запрещен в целях безопасности.",
		ActionSteps: []string{
			"В конфигурации узла VMess найдите поле `\"security\"`.",
			"Установите современный шифр: `\"security\": \"auto\"` или `\"aes-128-gcm\"`.",
			"Если сервер работает без шифрования, включите поверх TLS: `\"tls\": { \"enabled\": true }`.",
			"Сохраните настройки узла.",
		},
		Tip:        "VMess с TLS и шифрованием `auto` обеспечивает надежную маскировку и оптимальную скорость.",
		ExampleLog: "FATAL[0000] parse outbound[0]: vmess security 'zero' is insecure and rejected",
		Keywords:   []string{"vmess security zero", "security zero", "vmess insecure", "шифрование vmess"},
		Match: func(text string) bool {
			return matchAny(text, "vmess security zero", "is insecure and rejected")
		},
	},
	{
		ID:       "singbox_trojan_password_empty",
		Category: "Sing-box",
		Code:     "SINGBOX_TROJAN_NO_PASSWORD",
		Title:    "В конфигурации узла Trojan не указан пароль",
		Summary:  "Sing-box отклонил запуск исходящего узла Trojan, так как обязательное поле пароля пустое.",
		Cause:    "Пароль в протоколе Trojan служит ключом SHA-224 для аутентификации в первом пакете сессии.",
		ActionSteps: []string{
			"Откройте параметры узла Trojan в списке подключений.",
			"Заполните поле «Пароль (Password)» актуальным ключом из вашей подписки.",
			"Убедитесь, что в пароле нет случайных пробелов в начале или конце.",
			"Сохраните изменения.",
		},
		Tip:        "При импорте Trojan ссылок проверяйте, чтобы ссылка имела вид `trojan://password@host:port`.",
		ExampleLog: "FATAL[0000] parse outbound[1]: trojan: password is required",
		Keywords:   []string{"trojan password is required", "trojan password", "пароль trojan", "trojan empty"},
		Match: func(text string) bool {
			return matchAny(text, "trojan: password is required", "trojan password missing", "empty trojan password")
		},
	},
	{
		ID:       "singbox_wireguard_reserved_invalid",
		Category: "Sing-box",
		Code:     "SINGBOX_WG_RESERVED_INVALID",
		Title:    "Некорректный формат поля reserved в WireGuard (требуется 3 байта)",
		Summary:  "Sing-box не смог разобрать байты обфускации reserved для встроенного WireGuard клиента.",
		Cause:    "Параметр `reserved` (используется Cloudflare WARP и другими провайдерами) должен быть строго массивом из трех чисел: `[x, y, z]` (от 0 до 255).",
		ActionSteps: []string{
			"В конфигурации outbound WireGuard найдите строку `\"reserved\"`.",
			"Убедитесь, что значение имеет вид: `\"reserved\": [0, 0, 0]`.",
			"Если провайдер не требует обхода по reserved, удалите это поле целиком.",
			"Сохраните конфиг.",
		},
		Tip:        "Cloudflare WARP часто выдает reserved в формате Base64, который нужно декодировать в 3 байта.",
		ExampleLog: "FATAL[0000] parse outbound[2]: wireguard: invalid reserved length: expected 3, got 4",
		Keywords:   []string{"invalid reserved length", "wireguard reserved", "reserved singbox", "cloudflare reserved"},
		Match: func(text string) bool {
			return matchAny(text, "invalid reserved length", "wireguard: invalid reserved", "reserved length: expected 3")
		},
	},
	{
		ID:       "singbox_multiplex_brutal_unsupported",
		Category: "Sing-box",
		Code:     "SINGBOX_BRUTAL_UNSUPPORTED",
		Title:    "Алгоритм TCP Brutal не поддерживается ядром роутера",
		Summary:  "Sing-box сообщил о невозможности активировать алгоритм агрессивного контроля перегрузки Brutal.",
		Cause:    "TCP Brutal требует наличия специального модуля ядра Linux `tcp_brutal.ko`, которого нет в стандартной прошивке KeeneticOS.",
		ActionSteps: []string{
			"В свойствах исходящего узла откройте блок `\"multiplex\"`.",
			"Отключите опцию `\"brutal\"` либо замените протокол на стандартный `\"h2mux\"` или `\"smux\"`.",
			"Для протоколов TUIC и Hysteria 2 используйте стандартный алгоритм BBR.",
			"Примените изменения.",
		},
		Tip:        "Стандартный мультиплексор Smux работает на любых роутерах стабильно и без патчей ядра.",
		ExampleLog: "WARN[0000] multiplex: tcp brutal is not supported by kernel, fallback to standard",
		Keywords:   []string{"tcp brutal is not supported", "multiplex brutal", "brutal singbox", "tcp brutal"},
		Match: func(text string) bool {
			return matchAny(text, "tcp brutal is not supported", "tcp_brutal not found", "brutal is not available")
		},
	},
	{
		ID:       "singbox_dns_strategy_prefer_ipv6_broken",
		Category: "Sing-box",
		Code:     "SINGBOX_PREFER_IPV6_BROKEN",
		Title:    "Зависание соединений из-за стратегии prefer_ipv6 без внешнего IPv6",
		Summary:  "Sing-box выбирает IPv6-адреса сайтов, но интернет-провайдер или туннель не маршрутизирует IPv6.",
		Cause:    "Опция `strategy: \"prefer_ipv6\"` заставляет резолвер возвращать AAAA записи первыми. Пакеты уходят в «черную дыру».",
		ActionSteps: []string{
			"В секции `\"dns\"` конфигурации Sing-box найдите поле `\"strategy\"`.",
			"Измените значение на `\"strategy\": \"prefer_ipv4\"` или `\"ipv4_only\"`.",
			"Сохраните конфигурацию и перезапустите движок.",
			"Очистите DNS-кэш на клиентских устройствах.",
		},
		Tip:        "Установка `\"strategy\": \"prefer_ipv4\"` полностью решает проблему зависания сайтов Google, Telegram и Discord.",
		ExampleLog: "WARN[0005] dial tcp6 [2a00:1450:4010:c08::65]:443: connect: network is unreachable",
		Keywords:   []string{"prefer_ipv6", "network is unreachable ipv6", "dial tcp6", "стратегия dns"},
		Match: func(text string) bool {
			return matchAny(text, "connect: network is unreachable", "dial tcp6", "prefer_ipv6 broken")
		},
	},
	{
		ID:       "singbox_clash_api_secret_mismatch",
		Category: "Sing-box",
		Code:     "SINGBOX_CLASH_API_AUTH_FAIL",
		Title:    "Ошибка авторизации во встроенном Clash API Sing-box",
		Summary:  "Панель управления не может подключиться к контроллеру Sing-box (порт 9090) из-за неверного секрета.",
		Cause:    "Токен в заголовке запроса к REST API не совпадает со значением `\"secret\"` в блоке `\"experimental\": { \"clash_api\": ... }`.",
		ActionSteps: []string{
			"В файле config.json найдите блок `\"clash_api\"`.",
			"Сверьте значение `\"secret\"` с настройками вашей панели управления.",
			"Если доступ осуществляется только с локального роутера, установите `\"secret\": \"\"`.",
			"Перезапустите Sing-box.",
		},
		Tip:        "AWG Manager автоматически подставляет актуальный токен при опросе состояния ядра.",
		ExampleLog: "WARN[0002] clash-api: unauthorized access from 127.0.0.1:53210",
		Keywords:   []string{"clash-api unauthorized", "secret singbox", "clash api secret", "ошибка clash api"},
		Match: func(text string) bool {
			return matchAny(text, "clash-api: unauthorized", "unauthorized access to clash-api", "clash api token mismatch")
		},
	},
	{
		ID:       "singbox_reality_short_id_invalid",
		Category: "Sing-box",
		Code:     "SINGBOX_REALITY_SHORT_ID_INVALID",
		Title:    "Некорректный Short ID в параметрах Reality",
		Summary:  "Sing-box отклонил значение параметра short_id. Длина строки должна быть четным шестнадцатеричным числом (HEX) до 16 символов.",
		Cause:    "В поле `short_id` переданы невалидные символы (не 0-9, a-f) или нечетная длина строки (например, 7 символов вместо 8 или 16).",
		ActionSteps: []string{
			"Откройте параметры узла Reality.",
			"Проверьте поле «Short ID». Допустимы только четные HEX строки: 2, 4, 6, 8, 16 символов (например: `a1b2c3d4`).",
			"Если сервер не требует short_id, оставьте поле пустым: `\"short_id\": \"\"`.",
			"Сохраните конфигурацию.",
		},
		Tip:        "Short ID генерируется на сервере утилитой `xray/sing-box generate reality-keypair`.",
		ExampleLog: "FATAL[0000] parse outbound[0]: reality: invalid short_id length: odd length hex string",
		Keywords:   []string{"invalid short_id", "short id reality", "odd length hex", "short_id singbox"},
		Match: func(text string) bool {
			return matchAny(text, "invalid short_id", "odd length hex string", "reality: invalid short_id")
		},
	},
	{
		ID:       "singbox_reality_server_name_mismatch",
		Category: "Sing-box",
		Code:     "SINGBOX_REALITY_SNI_MISMATCH",
		Title:    "Несоответствие домена маскировки (SNI) сертификату сервера Reality",
		Summary:  "Целевой сервер маскировки вернул SSL-сертификат, не соответствующий указанному домену в `server_name`.",
		Cause:    "Домен маскировки (например, `dl.google.com` или `yahoo.com`) сменил сертификат, либо провайдер подменил DNS ответ.",
		ActionSteps: []string{
			"В свойствах подключения Reality проверьте поле «Server Name (SNI)».",
			"Убедитесь, что сервер маскировки доступен напрямую из России без VPN.",
			"Обновите узел из подписки или запросите новый домен маскировки у администратора сервера.",
			"Сохраните настройки.",
		},
		Tip:        "Выбирайте в качестве домена маскировки крупные зарубежные CDN и сервисы (Apple, Microsoft, Cloudflare), поддерживающие TLS 1.3.",
		ExampleLog: "FATAL[0001] reality: server name 'speedtest.net' does not match target certificate",
		Keywords:   []string{"server name does not match", "reality sni", "tls sni mismatch", "маскировочный домен"},
		Match: func(text string) bool {
			return matchAny(text, "does not match target certificate", "reality server name mismatch", "sni mismatch in reality")
		},
	},
	{
		ID:       "singbox_rule_set_headless_compile_fail",
		Category: "Sing-box",
		Code:     "SINGBOX_SRS_COMPILE_FAIL",
		Title:    "Ошибка локальной компиляции набора правил SRS",
		Summary:  "Sing-box не смог скомпилировать текстовый JSON список правил в бинарный формат SRS из-за нехватки оперативной памяти роутера.",
		Cause:    "Огромные списки доменов (десятки тысяч записей) при компиляции на процессоре MIPS/ARM роутера вызывают перегрузку RAM.",
		ActionSteps: []string{
			"Используйте уже скомпилированные бинарные файлы `.srs` вместо тяжелых текстовых `.json` списков.",
			"В секции `\"rule_set\"` укажите `\"format\": \"binary\"` со ссылкой на готовый SRS файл.",
			"Включите файл подкачки (SWAP) на роутере на вкладке «Система».",
			"Перезапустите движок.",
		},
		Tip:        "Бинарные правила SRS загружаются мгновенно и потребляют в 5 раз меньше оперативной памяти роутера.",
		ExampleLog: "FATAL[0000] compile rule-set: out of memory during compilation",
		Keywords:   []string{"compile rule-set", "srs compilation error", "бинарные правила", "формат srs"},
		Match: func(text string) bool {
			return matchAny(text, "compile rule-set", "srs compile error", "failed to compile binary rule-set")
		},
	},
	{
		ID:       "singbox_inbound_mixed_bind_address",
		Category: "Sing-box",
		Code:     "SINGBOX_MIXED_ADDR_IN_USE",
		Title:    "Входящий порт Mixed (SOCKS5/HTTP 2080) уже занят",
		Summary:  "Sing-box не смог открыть порт локального прокси 2080, так как он занят другим процессом.",
		Cause:    "Порт mixed прокси удерживается предыдущим процессом либо совпадает с портом другой сетевой утилиты.",
		ActionSteps: []string{
			"В AWG Manager нажмите «Остановить движок».",
			"Проверьте, какой процесс слушает порт: `netstat -tlpn | grep 2080`.",
			"В файле конфигурации измените порт mixed на другой свободный (например, 2081).",
			"Запустите движок заново.",
		},
		Tip:        "Порт mixed используется для ручной настройки прокси в браузерах или Telegram.",
		ExampleLog: "FATAL[0000] start inbound/mixed[0]: listen tcp 0.0.0.0:2080: bind: address already in use",
		Keywords:   []string{"start inbound/mixed", ":2080: bind: address already in use", "mixed port", "порт mixed занят"},
		Match: func(text string) bool {
			return matchAny(text, "start inbound/mixed", ":2080: bind: address already in use", "listen tcp :2080")
		},
	},
	{
		ID:       "singbox_tls_ech_unsupported",
		Category: "Sing-box",
		Code:     "SINGBOX_ECH_REJECTED",
		Title:    "Зашифрованный ECH (Encrypted Client Hello) отклонен сервером",
		Summary:  "Удаленный сервер или промежуточный DPI заблокировал сессию с включенным расширением TLS ECH.",
		Cause:    "Некоторые провайдеры связи в РФ целенаправленно сбрасывают все TLS соединения с расширением ECH.",
		ActionSteps: []string{
			"В конфигурации исходящего узла отключите ECH: `\"ech\": { \"enabled\": false }`.",
			"Убедитесь, что используется стандартный TLS 1.3 с валидным маскировочным SNI.",
			"Перезапустите Sing-box.",
		},
		Tip:        "В настоящее время для стабильного обхода блокировок рекомендуется использовать Reality или ShadowTLS вместо чистого ECH.",
		ExampleLog: "WARN[0002] tls: failed to decrypt encrypted client hello, connection dropped",
		Keywords:   []string{"encrypted client hello", "ech rejected", "ech singbox", "блокировка ech"},
		Match: func(text string) bool {
			return matchAny(text, "failed to decrypt encrypted client hello", "ech handshake failed", "tls: ech rejected")
		},
	},
	{
		ID:       "singbox_hysteria2_port_hopping_blocked",
		Category: "Sing-box",
		Code:     "SINGBOX_HY2_PORT_HOPPING_FAIL",
		Title:    "Диапазон портов Hysteria 2 заблокирован сетевым фаерволом",
		Summary:  "Клиент Hysteria 2 не может переключать UDP порты (Port Hopping), трафик блокируется провайдером.",
		Cause:    "В конфигурации указан диапазон портов (например, `40000-50000`), но промежуточный роутер или фаервол блокирует исходящий UDP выше 1024.",
		ActionSteps: []string{
			"В параметрах узла Hysteria 2 укажите один фиксированный порт (например, 443 или 8443).",
			"Отключите диапазон портов в поле `server_port`.",
			"Проверьте, разрешен ли исходящий UDP в веб-интерфейсе Keenetic.",
			"Сохраните настройки узла.",
		},
		Tip:        "Использование стандартного порта 443 для UDP (QUIC) обеспечивает наилучшую проходимость в сетях любых операторов.",
		ExampleLog: "WARN[0004] hysteria2: port hopping packet to port 48123 timed out",
		Keywords:   []string{"hysteria2 port hopping", "port hopping timed out", "скачущие порты", "quic timeout"},
		Match: func(text string) bool {
			return matchAny(text, "port hopping packet", "port hopping timed out", "hysteria2: port hopping failed")
		},
	},
	{
		ID:       "singbox_dialer_bind_interface_down",
		Category: "Sing-box",
		Code:     "SINGBOX_BIND_IFACE_DOWN",
		Title:    "Сетевой интерфейс привязки исходящего соединения выключен",
		Summary:  "Sing-box не может отправить сетевые пакеты, так как интерфейс, указанный в `bind_interface`, неактивен.",
		Cause:    "В секции `dialer` исходящего соединения жестко прописано имя сетевого интерфейса (например, `nwg0` или `eth0`), который отключен.",
		ActionSteps: []string{
			"В конфигурации узла удалите параметр `\"bind_interface\"` или укажите активный интерфейс выхода в интернет.",
			"Если используется резервный провайдер, настройте маршрутизацию через системные политики KeeneticOS.",
			"Перезапустите Sing-box.",
		},
		Tip:        "Если не указывать `bind_interface`, ядро Linux автоматически выберет оптимальный интерфейс по таблице маршрутизации.",
		ExampleLog: "FATAL[0000] dial tcp: bind to interface 'nwg0': Network is down",
		Keywords:   []string{"bind to interface", "network is down dial", "bind_interface", "интерфейс выключен"},
		Match: func(text string) bool {
			return matchAny(text, "bind to interface", "bind: network is down", "cannot bind to interface")
		},
	},
	{
		ID:       "singbox_uot_v2_unsupported",
		Category: "Sing-box",
		Code:     "SINGBOX_UOT_REJECTED",
		Title:    "Сервер не поддерживает протокол UDP over TCP (UoT v2)",
		Summary:  "Sing-box попытался упаковать UDP-трафик в TCP-соединение через протокол UoT, но сервер отклонил поток.",
		Cause:    "Опция `\"udp_over_tcp\": true` включена на клиенте, но не поддерживается или отключена на удаленном сервере.",
		ActionSteps: []string{
			"В настройках исходящего соединения отключите: `\"udp_over_tcp\": false`.",
			"Разрешите прямую передачу нативного UDP на сервере.",
			"Сохраните конфигурацию и перезапустите движок.",
		},
		Tip:        "UoT полезен только на мобильных операторах с жесткой блокировкой чистого UDP трафика.",
		ExampleLog: "WARN[0003] uot: server rejected udp over tcp handshake",
		Keywords:   []string{"udp over tcp handshake", "uot rejected", "udp_over_tcp", "uot v2"},
		Match: func(text string) bool {
			return matchAny(text, "server rejected udp over tcp", "uot handshake failed", "udp over tcp error")
		},
	},
	{
		ID:       "singbox_tcp_fast_open_fail",
		Category: "Sing-box",
		Code:     "SINGBOX_TFO_UNSUPPORTED",
		Title:    "Сбой включения TCP Fast Open (TFO)",
		Summary:  "Sing-box не смог активировать быстрый старт TCP соединений, так как TFO отключен в ядре роутера.",
		Cause:    "Системный параметр Linux `net.ipv4.tcp_fastopen` равен 0 или не поддерживается оборудованием провайдера.",
		ActionSteps: []string{
			"В настройках узлов Sing-box отключите: `\"tcp_fast_open\": false`.",
			"Или включите поддержку TFO в ядре через терминал: `sysctl -w net.ipv4.tcp_fastopen=3`.",
			"Перезапустите движок Sing-box.",
		},
		Tip:        "Некоторые провайдеры сбрасывают SYN-пакеты с данными TFO. При нестабильной связи надежнее держать TFO выключенным.",
		ExampleLog: "WARN[0000] dialer: enable tcp fast open failed: protocol not available",
		Keywords:   []string{"enable tcp fast open failed", "tcp fast open", "tfo", "tcp_fastopen"},
		Match: func(text string) bool {
			return matchAny(text, "enable tcp fast open failed", "tcp fast open: protocol not available", "tfo error")
		},
	},
	{
		ID:       "singbox_tproxy_rule_order_inverted",
		Category: "Sing-box",
		Code:     "SINGBOX_TPROXY_ORDER_INVERTED",
		Title:    "Неправильный порядок цепочек TProxy в таблице iptables",
		Summary:  "Трафик не перехватывается, потому что правила TProxy в цепочке PREROUTING расположены после правил сопоставления состояний.",
		Cause:    "Нарушение последовательности команд iptables при ручной настройке фаервола роутера.",
		ActionSteps: []string{
			"В AWG Manager перейдите на вкладку «Маршрутизация».",
			"Нажмите «Остановить движок», затем «Запустить движок» — AWG Manager автоматически пересоздаст правила iptables в правильном порядке.",
			"Убедитесь, что нет сторонних конфликтующих скриптов в `/opt/etc/ndm/netfilter.d/`.",
		},
		Tip:        "AWG Manager гарантирует установку правила `-j TPROXY` в самом начале цепочки mangle PREROUTING.",
		ExampleLog: "iptables: tproxy target must be placed before conntrack accept in mangle prerouting",
		Keywords:   []string{"tproxy target must be placed", "tproxy order", "цепочка prerouting", "порядок правил tproxy"},
		Match: func(text string) bool {
			return matchAny(text, "tproxy target must be placed", "tproxy rule order", "iptables mangle prerouting order")
		},
	},

	// =========================================================================
	// 5. ПОДПИСКИ И ССЫЛКИ (ФОРМАТЫ, HTTP СТАТУСЫ, КЭШ)
	// =========================================================================
	{
		ID:       "sub_http_401_unauthorized",
		Category: "Подписки и прокси",
		Code:     "SUB_HTTP_401",
		Title:    "Ошибка 401: Доступ к подписке запрещен (Неверный токен)",
		Summary:  "Сервер провайдера подписки отклонил запрос, так как переданный токен авторизации или ключ подписки недействителен.",
		Cause:    "Истек срок действия ключа, ссылка была сброшена в личном кабинете провайдера или допущена опечатка при копировании URL.",
		ActionSteps: []string{
			"Войдите в личный кабинет вашего VPN/прокси сервиса.",
			"Скопируйте актуальную ссылку на подписку (Clash/Mihomo или Sing-box/Base64).",
			"В AWG Manager на вкладке «Подписки» нажмите «Редактировать» и вставьте новую ссылку.",
			"Нажмите «Обновить подписку».",
		},
		Tip:        "При перевыпуске подписки в личном кабинете старая ссылка моментально блокируется сервером провайдера.",
		ExampleLog: "subscription: download failed: HTTP 401 Unauthorized for URL https://sub.provider.com/link",
		Keywords:   []string{"401 unauthorized", "подписка 401", "sub 401", "токен подписки"},
		Match: func(text string) bool {
			return matchAny(text, "http 401", "401 unauthorized", "subscription 401")
		},
	},
	{
		ID:       "sub_http_403_forbidden",
		Category: "Подписки и прокси",
		Code:     "SUB_HTTP_403",
		Title:    "Ошибка 403: Провайдер подписки заблокировал доступ (Forbidden)",
		Summary:  "Сервер подписки заблокировал запрос от роутера. Обычно это связано с блокировкой по IP-адресу или некорректным User-Agent.",
		Cause:    "Защита Cloudflare/WAF на сервере провайдера блокирует стандартный заголовок curl/Go-http-client, либо ваш IP-адрес попал под фильтр стран.",
		ActionSteps: []string{
			"В настройках подписки в AWG Manager проверьте заголовок «User-Agent» (рекомендуется: `clash.meta` или `v2rayN`).",
			"Попробуйте открыть ссылку на подписку в обычном браузере на компьютере или телефоне.",
			"Если ссылка открывается в браузере — скачайте файл вручную и загрузите его как локальный файл конфигурации.",
			"Обратитесь в поддержку сервиса подписки.",
		},
		Tip:        "Многие зарубежные сервисы подписок блокируют российские IP-адреса, требуя обновления через уже работающий VPN.",
		ExampleLog: "subscription: fetch failed: HTTP 403 Forbidden (Cloudflare bot detection)",
		Keywords:   []string{"403 forbidden", "подписка 403", "sub 403", "cloudflare bot detection"},
		Match: func(text string) bool {
			return matchAny(text, "http 403", "403 forbidden", "subscription 403")
		},
	},
	{
		ID:       "sub_http_404_not_found",
		Category: "Подписки и прокси",
		Code:     "SUB_HTTP_404",
		Title:    "Ошибка 404: Ссылка на подписку не найдена (Not Found)",
		Summary:  "Сервер ответил, что запрошенный адрес подписки не существует.",
		Cause:    "Удален файл подписки, изменена структура URL у провайдера или в адрес закрался лишний пробел / оборван хвост ссылки.",
		ActionSteps: []string{
			"Внимательно проверьте URL адрес подписки от начала до конца (`https://...`).",
			"Убедитесь, что в конце URL нет лишних символов, пробелов или знаков препинания.",
			"Скопируйте ссылку заново из личного кабинета или Telegram-бота вашего поставщика VPN.",
			"Сохраните и повторите загрузку.",
		},
		Tip:        "Копируйте ссылку нажатием кнопки «Копировать» в личном кабинете, не выделяя текст мышкой вручную.",
		ExampleLog: "subscription: download failed: HTTP 404 Not Found",
		Keywords:   []string{"404 not found", "подписка 404", "sub 404", "ссылка не найдена"},
		Match: func(text string) bool {
			return matchAny(text, "http 404", "404 not found", "subscription 404")
		},
	},
	{
		ID:       "sub_http_429_too_many_requests",
		Category: "Подписки и прокси",
		Code:     "SUB_HTTP_429",
		Title:    "Ошибка 429: Превышен лимит запросов к подписке (Rate Limit)",
		Summary:  "Сервер подписки временно ограничил запросы из-за слишком частых обновлений.",
		Cause:    "Слишком частый интервал автообновления (например, каждые несколько минут) либо множественные клики по кнопке «Обновить всё».",
		ActionSteps: []string{
			"Подождите 15-30 минут — лимит сбросится сервером автоматически.",
			"В параметрах подписки установите разумный интервал автообновления: 12 или 24 часа.",
			"Не нажимайте повторное обновление несколько раз подряд.",
			"Используйте уже загруженные узлы из локального кэша.",
		},
		Tip:        "Большинство провайдеров разрешают обновлять подписку не чаще 1 раза в час.",
		ExampleLog: "subscription: download error: HTTP 429 Too Many Requests (Retry-After: 900)",
		Keywords:   []string{"429 too many requests", "rate limit", "лимит запросов", "retry-after"},
		Match: func(text string) bool {
			return matchAny(text, "http 429", "too many requests", "rate limit exceeded")
		},
	},
	{
		ID:       "sub_http_502_bad_gateway",
		Category: "Подписки и прокси",
		Code:     "SUB_HTTP_502",
		Title:    "Ошибка 502 / 503 / 504: Сбой на сервере поставщика подписки",
		Summary:  "Сервер поставщика VPN или его балансировщик временно не отвечает на запросы.",
		Cause:    "Технические работы на стороне VPN-провайдера, падение базы данных подписок или авария в дата-центре.",
		ActionSteps: []string{
			"Подождите 10-20 минут, пока провайдер восстановит работу сервиса.",
			"Проверьте информационный канал или чат технической поддержки вашего сервиса.",
			"Роутер продолжит использовать ранее сохраненные узлы, пока сервер не поднимется.",
			"Попробуйте обновить подписку позже.",
		},
		Tip:        "AWG Manager сохраняет локальную копию серверов, поэтому временное падение сайта провайдера не прерывает интернет.",
		ExampleLog: "subscription: fetch failed: HTTP 502 Bad Gateway from upstream server",
		Keywords:   []string{"502 bad gateway", "503 service unavailable", "504 gateway timeout", "сбой сервера подписки"},
		Match: func(text string) bool {
			return matchAny(text, "http 502", "http 503", "http 504", "bad gateway", "service unavailable")
		},
	},
	{
		ID:       "sub_ssl_cert_expired",
		Category: "Подписки и прокси",
		Code:     "SUB_SSL_CERT_EXPIRED",
		Title:    "Истек SSL-сертификат сайта подписки (x509: certificate expired)",
		Summary:  "Роутер отказался скачивать подписку по HTTPS, так как сертификат безопасности веб-сайта просрочен.",
		Cause:    "Владелец сайта подписки забыл обновить сертификат Let's Encrypt, либо на роутере сбиты системные часы.",
		ActionSteps: []string{
			"Первым делом проверьте дату и время на роутере на вкладке «Система».",
			"Если часы показывают 1970 или 2023 год, нажмите «Синхронизировать время по NTP».",
			"Если дата на роутере верная — сертификат действительно просрочен у провайдера.",
			"Сообщите в поддержку сервиса подписки об ошибке сертификата.",
		},
		Tip:        "При несинхронизированном времени роутер бракует даже абсолютно валидные сертификаты.",
		ExampleLog: "subscription: TLS handshake failed: x509: certificate has expired or is not yet valid",
		Keywords:   []string{"x509: certificate has expired", "certificate expired", "истек сертификат", "tls handshake failed"},
		Match: func(text string) bool {
			return matchAny(text, "certificate has expired", "x509: certificate has expired") && !matchAny(text, "clock skew")
		},
	},
	{
		ID:       "sub_base64_decode_corrupted",
		Category: "Подписки и прокси",
		Code:     "SUB_BASE64_CORRUPTED",
		Title:    "Поврежденные данные Base64 в ответе подписки",
		Summary:  "Роутер не смог расшифровать список серверов: строка Base64 содержит недопустимые символы или оборвана.",
		Cause:    "Сбой передачи данных, обрыв соединения при скачивании или сервер вернул текстовую ошибку вместо бинарного потока Base64.",
		ActionSteps: []string{
			"Нажмите «Обновить подписку» повторно для перезагрузки файла.",
			"Проверьте, включен ли VPN на роутере во время обновления подписки.",
			"Если подписка содержит Clash YAML, переключите тип парсера подписки на «Clash/Mihomo YAML».",
			"Убедитесь, что провайдер отдает ссылки в кодировке UTF-8.",
		},
		Tip:        "Если вставить ссылку на подписку в адресную строку браузера, вы сможете увидеть, что именно возвращает сервер.",
		ExampleLog: "subscription: base64 decode error: illegal base64 data at input byte 1420",
		Keywords:   []string{"illegal base64 data", "base64 decode error", "битый base64", "ошибка декодирования"},
		Match: func(text string) bool {
			return matchAny(text, "illegal base64 data", "base64 decode error", "corrupted base64 payload")
		},
	},
	{
		ID:       "sub_empty_proxies_list",
		Category: "Подписки и прокси",
		Code:     "SUB_NO_NODES_FOUND",
		Title:    "В подписке не найдено ни одного рабочего сервера (0 узлов)",
		Summary:  "Подписка успешно загружена, но парсер не обнаружил в ней ни одного поддерживаемого прокси-сервера.",
		Cause:    "Формат ссылок не поддерживается выбранным ядром, либо у провайдера закончились доступные серверы на данном тарифе.",
		ActionSteps: []string{
			"Проверьте тип вашей ссылки: для Sing-box требуются ссылки VLESS/Reality/Shadowsocks, для Mihomo — Clash YAML.",
			"В мастере добавления подписки попробуйте переключить движок обработки (Sing-box ↔ Mihomo).",
			"Проверьте в личном кабинете, что ваш тариф активен и к нему привязаны серверные локации.",
			"Попробуйте импортировать узел как одиночную ссылку (`vless://...`).",
		},
		Tip:        "AWG Manager поддерживает автоматическое определение формата и конвертацию популярных подписок.",
		ExampleLog: "subscription: parse completed: 0 proxies found in config",
		Keywords:   []string{"0 proxies found", "empty proxies list", "нет серверов", "0 узлов"},
		Match: func(text string) bool {
			return matchAny(text, "0 proxies found", "no proxies found", "proxies list is empty in subscription")
		},
	},
	{
		ID:       "sub_subscription_expired",
		Category: "Подписки и прокси",
		Code:     "SUB_TRAFFIC_EXHAUSTED",
		Title:    "Срок действия подписки истек или исчерпан лимит трафика",
		Summary:  "Провайдер заблокировал работу узлов: трафик исчерпан (0 GB остатка) либо закончилась оплата тарифа.",
		Cause:    "Завершение оплаченного расчетного периода или исчерпание ежемесячной квоты гигабайт.",
		ActionSteps: []string{
			"Войдите в личный кабинет или Telegram-бот вашего VPN-сервиса.",
			"Проверьте остаток трафика и дату окончания подписки.",
			"Продлите тариф или пополните баланс.",
			"После оплаты нажмите «Обновить подписку» в AWG Manager.",
		},
		Tip:        "В карточке подписки AWG Manager отображает остаток трафика и дату экспирации (если поддерживается провайдером).",
		ExampleLog: "subscription: provider returned: account expired or traffic exhausted",
		Keywords:   []string{"account expired", "traffic exhausted", "подписка истекла", "трафик исчерпан"},
		Match: func(text string) bool {
			return matchAny(text, "account expired", "traffic exhausted", "subscription expired", "quota exceeded")
		},
	},
	{
		ID:       "sub_clash_yaml_invalid_header",
		Category: "Подписки и прокси",
		Code:     "SUB_HTML_INSTEAD_OF_YAML",
		Title:    "Сервер вернул HTML-страницу вместо конфигурации (Cloudflare / Ошибка)",
		Summary:  "Вместо файла конфигурации с серверами скачалась веб-страница (HTML код), которую парсер не может разобрать.",
		Cause:    "Капча Cloudflare, страница входа в Wi-Fi сеть отеля/провайдера (Captive Portal) или ошибка 500 на сайте провайдера.",
		ActionSteps: []string{
			"Проверьте, есть ли на роутере прямой доступ в интернет (без VPN).",
			"Если вы находитесь в сети с авторизацией по SMS (отель, кафе), пройдите авторизацию в браузере.",
			"Откройте ссылку подписки в браузере — если там капча Cloudflare («Verify you are human»), роутер не сможет скачать её автоматически.",
			"В таком случае скачайте конфигурационный файл в браузере и загрузите вручную.",
		},
		Tip:        "Загрузка файла вручную в формате `.yaml` или `.json` полностью решает проблему с капчами Cloudflare.",
		ExampleLog: "subscription: parse error: expected YAML, got HTML document '<!DOCTYPE html>'",
		Keywords:   []string{"expected yaml, got html", "doctype html", "html вместо yaml", "cloudflare captcha"},
		Match: func(text string) bool {
			return matchAny(text, "expected yaml, got html", "got html document", "<!doctype html>")
		},
	},
	{
		ID:       "sub_unsupported_link_scheme",
		Category: "Подписки и прокси",
		Code:     "SUB_SCHEME_UNSUPPORTED",
		Title:    "Неподдерживаемый протокол в ссылке (Unsupported URI scheme)",
		Summary:  "Ссылка использует нестандартную схему протокола, которую не умеет обрабатывать выбранный движок.",
		Cause:    "Использование устаревших или проприетарных протоколов (например, `ssconf://`, `tg://`, `clash://`, `surge://`).",
		ActionSteps: []string{
			"Используйте универсальные ссылки: `vless://`, `vmess://`, `trojan://`, `ss://`, `tuic://`, `hysteria2://`.",
			"Если провайдер выдает ссылку вида `clash://install-config?url=...`, скопируйте внутренний адрес из параметра `url=`.",
			"Вставьте очищенный HTTP/HTTPS адрес в поле подписки.",
			"Повторите попытку импорта.",
		},
		Tip:        "Ссылки вида `clash://install-config?url=https://...` предназначены для десктопного приложения; роутеру нужен только сам URL.",
		ExampleLog: "subscription: unsupported link scheme: ssconf://proxy.example.com",
		Keywords:   []string{"unsupported link scheme", "неподдерживаемый протокол", "ssconf://", "clash://install-config"},
		Match: func(text string) bool {
			return matchAny(text, "unsupported link scheme", "unknown uri scheme", "cannot parse link scheme")
		},
	},
	{
		ID:       "sub_vless_link_query_missing",
		Category: "Подписки и прокси",
		Code:     "SUB_VLESS_PARAMS_MISSING",
		Title:    "В ссылке VLESS отсутствуют обязательные параметры",
		Summary:  "Ссылка на узел VLESS оформлена с нарушением стандарта: пропущены параметры безопасности или типа транспорта.",
		Cause:    "В ссылке отсутствует `?security=reality` (или tls) либо параметр `type=tcp` (или grpc/ws).",
		ActionSteps: []string{
			"Сверьте ссылку с эталонным форматом: `vless://UUID@host:port?security=reality&sni=...&pbk=...&type=tcp`.",
			"Экспортируйте узел заново из приложения v2rayN, Nekobox или из панели 3X-UI.",
			"Проверьте, чтобы в ссылке не были обрезаны параметры после знака вопроса `?`.",
			"Сохраните узел заново.",
		},
		Tip:        "Панель 3X-UI позволяет скопировать ссылку в один клик по значку QR-кода.",
		ExampleLog: "subscription: vless parse error: missing security parameter in query",
		Keywords:   []string{"missing security parameter", "vless parse error", "параметры vless", "битая ссылка vless"},
		Match: func(text string) bool {
			return matchAny(text, "missing security parameter", "vless parse error", "invalid vless link format")
		},
	},
	{
		ID:       "sub_ss_sip002_corrupted",
		Category: "Подписки и прокси",
		Code:     "SUB_SS_SIP002_ERROR",
		Title:    "Ошибка разбора ссылки Shadowsocks (Нарушен стандарт SIP002)",
		Summary:  "Ссылка `ss://` имеет некорректный формат кодирования реквизитов (метод шифрования и пароль).",
		Cause:    "В ссылке нарушена кодировка Base64 в части `method:password` перед знаком `@`.",
		ActionSteps: []string{
			"Убедитесь, что ссылка соответствует стандарту SIP002: `ss://BASE64(method:password)@hostname:port#name`.",
			"Если ссылка старого формата (без знака `@`), декодируйте ее или запросите обновленную ссылку.",
			"Вставьте корректную ссылку в мастер добавления.",
			"Сохраните узел.",
		},
		Tip:        "Современные клиенты используют стандарт SIP002 с явным разделением адреса сервера и учетных данных.",
		ExampleLog: "subscription: invalid shadowsocks URI: failed to decode userinfo base64",
		Keywords:   []string{"invalid shadowsocks uri", "sip002", "failed to decode userinfo", "ошибка shadowsocks ссылки"},
		Match: func(text string) bool {
			return matchAny(text, "invalid shadowsocks uri", "failed to decode userinfo", "ss:// parse error")
		},
	},
	{
		ID:       "sub_trojan_link_no_sni",
		Category: "Подписки и прокси",
		Code:     "SUB_TROJAN_NO_SNI",
		Title:    "В ссылке Trojan не указан SNI для проверки сертификата",
		Summary:  "Узел Trojan не может установить защищенное соединение, так как неизвестно имя домена для TLS рукопожатия.",
		Cause:    "В ссылке `trojan://` отсутствует параметр `sni=domain.com` или `peer=domain.com`, а в качестве хоста указан чистый IP-адрес.",
		ActionSteps: []string{
			"Откройте параметры узла Trojan в списке подключений.",
			"В поле «Server Name (SNI)» впишите доменное имя вашего сервера.",
			"Либо добавьте к ссылке параметр `?sni=yourdomain.com`.",
			"Сохраните конфигурацию узла.",
		},
		Tip:        "Протокол Trojan маскируется под обычный HTTPS трафик и требует строгого совпадения SNI с сертификатом.",
		ExampleLog: "subscription: trojan outbound: sni is empty and remote address is an IP",
		Keywords:   []string{"sni is empty trojan", "trojan no sni", "server name trojan", "sni пропущен"},
		Match: func(text string) bool {
			return matchAny(text, "sni is empty and remote", "trojan: sni is required", "missing sni in trojan")
		},
	},
	{
		ID:       "sub_tuic_link_version_missing",
		Category: "Подписки и прокси",
		Code:     "SUB_TUIC_VERSION_ERROR",
		Title:    "В ссылке TUIC не указана версия протокола (version=5)",
		Summary:  "Клиент TUIC не может определить версию протокола передачи данных.",
		Cause:    "Протоколы TUIC v4 и TUIC v5 несовместимы между собой. В ссылке должен присутствовать параметр `version=5`.",
		ActionSteps: []string{
			"Добавьте параметр `&version=5` в конец ссылки TUIC.",
			"Убедитесь, что серверная часть также обновлена до TUIC v5.",
			"В свойствах узла проверьте режим контроля перегрузки (рекомендуется: BBR).",
			"Сохраните узел.",
		},
		Tip:        "Подавляющее большинство современных серверов используют версию TUIC v5.",
		ExampleLog: "subscription: tuic: unknown protocol version in URI",
		Keywords:   []string{"unknown protocol version tuic", "tuic version", "tuic v5", "версия tuic"},
		Match: func(text string) bool {
			return matchAny(text, "unknown protocol version in uri", "tuic version missing", "unsupported tuic version")
		},
	},
	{
		ID:       "sub_hysteria2_link_obfs_password",
		Category: "Подписки и прокси",
		Code:     "SUB_HY2_OBFS_MISMATCH",
		Title:    "Несовпадение параметров обфускации в ссылке Hysteria 2",
		Summary:  "Ссылка Hysteria 2 задает пароль обфускации, но не указывает поддерживаемый тип обфускатора (obfs-type).",
		Cause:    "В ссылке указан `obfs-password=...`, но отсутствует `obfs=salamander` (единственный официально поддерживаемый тип).",
		ActionSteps: []string{
			"В параметрах узла Hysteria 2 установите тип обфускации: `salamander`.",
			"Проверьте, чтобы пароль обфускации в точности совпадал с настройками сервера.",
			"Если обфускация на сервере не настроена, удалите параметр `obfs` из ссылки.",
			"Сохраните конфигурацию.",
		},
		Tip:        "Обфускация Salamander в Hysteria 2 полностью защищает UDP-трафик от распознавания сигнатурными DPI.",
		ExampleLog: "subscription: hysteria2: unsupported obfs type '' with password",
		Keywords:   []string{"unsupported obfs type", "hysteria2 obfs", "salamander", "обфускация hysteria"},
		Match: func(text string) bool {
			return matchAny(text, "unsupported obfs type", "hysteria2: invalid obfs", "obfs password without obfs type")
		},
	},
	{
		ID:       "sub_auto_update_dns_fail",
		Category: "Подписки и прокси",
		Code:     "SUB_DNS_RESOLVE_FAILED",
		Title:    "Не удалось обновить подписку: DNS-имя сервера не разрешается",
		Summary:  "Роутер не смог узнать IP-адрес сервера подписки по его доменному имени.",
		Cause:    "Отсутствие интернета на роутере, сбой DNS-сервера провайдера или блокировка домена подписки на уровне РКН.",
		ActionSteps: []string{
			"Проверьте доступность интернета на главной странице AWG Manager.",
			"На вкладке «Маршрутизация» проверьте настройки upstream DNS (попробуйте переключить на `77.88.8.8` или `1.1.1.1`).",
			"Попробуйте открыть сайт подписки со смартфона через мобильный интернет.",
			"Если домен заблокирован в РФ, включите обновление подписок через работающий туннель.",
		},
		Tip:        "В AWG Manager можно настроить маршрутизацию трафика обновления подписок через активный VPN.",
		ExampleLog: "subscription: lookup sub.domain.com on 127.0.0.1:53: no such host",
		Keywords:   []string{"no such host sub", "lookup sub.domain", "dns resolve failed sub", "не разрешается домен"},
		Match: func(text string) bool {
			return matchAny(text, "no such host", "lookup failed for subscription", "temporary failure in name resolution")
		},
	},
	{
		ID:       "sub_traffic_limit_header_missing",
		Category: "Подписки и прокси",
		Code:     "SUB_NO_USERINFO_HEADER",
		Title:    "Провайдер не передает данные об остатке трафика (Userinfo Header)",
		Summary:  "В карточке подписки не отображаются шкала трафика и дата окончания, хотя серверы работают нормально.",
		Cause:    "Сервер провайдера не возвращает стандартный HTTP-заголовок `Subscription-Userinfo: upload=...; download=...; total=...; expire=...`.",
		ActionSteps: []string{
			"Это информационное предупреждение — сами прокси-серверы при этом работают исправно.",
			"Для проверки остатка гигабайт воспользуйтесь личным кабинетом на сайте вашего провайдера.",
			"Никаких действий с роутером выполнять не требуется.",
		},
		Tip:        "Многие самописные панели не поддерживают заголовок Subscription-Userinfo, это не является ошибкой подключения.",
		ExampleLog: "subscription: response headers do not contain 'Subscription-Userinfo'",
		Keywords:   []string{"subscription-userinfo", "остаток трафика", "userinfo header", "нет шкалы трафика"},
		Match: func(text string) bool {
			return matchAny(text, "subscription-userinfo", "missing userinfo header", "no traffic statistics provided")
		},
	},
	{
		ID:       "sub_vless_grpc_servicename_missing",
		Category: "Подписки и прокси",
		Code:     "SUB_GRPC_SERVICENAME_MISSING",
		Title:    "В узле VLESS gRPC не указано обязательное имя службы (serviceName)",
		Summary:  "Соединение gRPC отклонено сервером: клиент не передал имя целевого сервиса.",
		Cause:    "Для протокола gRPC параметр `serviceName` является аналогом пути URI. Без него сервер не знает, какому обработчику передать поток.",
		ActionSteps: []string{
			"В свойствах узла найдите параметры gRPC транспорта.",
			"Заполните поле «Имя сервиса (Service Name)» значением из конфигурации сервера.",
			"В строке ссылки это параметр `&serviceName=my-service`.",
			"Сохраните узел и перезапустите движок.",
		},
		Tip:        "Если имя службы неизвестно, попробуйте стандартные значения, используемые автонастройщиками: `grpc` или `vless-grpc`.",
		ExampleLog: "FATAL[0000] parse outbound: grpc: serviceName is required for gRPC transport",
		Keywords:   []string{"servicename is required", "grpc servicename", "имя службы grpc", "vless grpc"},
		Match: func(text string) bool {
			return matchAny(text, "servicename is required", "missing grpc service name", "grpc: serviceName")
		},
	},
	{
		ID:       "sub_reality_spider_x_invalid",
		Category: "Подписки и прокси",
		Code:     "SUB_SPIDER_X_INVALID",
		Title:    "Некорректный путь SpiderX в параметрах Reality",
		Summary:  "Клиент Reality передал недопустимый начальный путь краулера SpiderX.",
		Cause:    "Параметр `spider_x` должен начинаться со слэша `/` (например, `/` или `/search?q=test`).",
		ActionSteps: []string{
			"Откройте конфигурацию узла Reality.",
			"В поле «SpiderX» убедитесь, что значение начинается с косой черты: `/`.",
			"Если параметр не требуется, оставьте его равным просто `/`.",
			"Сохраните настройки.",
		},
		Tip:        "Параметр SpiderX заставляет клиент маскироваться под поискового робота при первом обращении к сайту маскировки.",
		ExampleLog: "FATAL[0000] parse outbound: reality: spider_x must start with '/'",
		Keywords:   []string{"spider_x must start with", "spider_x reality", "краулер reality", "spiderx"},
		Match: func(text string) bool {
			return matchAny(text, "spider_x must start with", "invalid spider_x", "reality spiderx error")
		},
	},
	{
		ID:       "sub_duplicate_node_names",
		Category: "Подписки и прокси",
		Code:     "SUB_DUPLICATE_NAMES",
		Title:    "В подписке обнаружены узлы с одинаковыми именами (Коллизия тегов)",
		Summary:  "Два или более сервера в подписке имеют абсолютно идентичные названия (например, «Германия 01»).",
		Cause:    "Ядра Sing-box и Mihomo требуют уникальности тега каждого исходящего узла. Дубликаты приводят к сбою запуска конфигурации.",
		ActionSteps: []string{
			"AWG Manager автоматически переименовывает дубликаты, добавляя суффиксы `#2`, `#3`.",
			"Если ошибка сохраняется в кастомном файле, переименуйте повторяющиеся узлы вручную.",
			"Сохраните файл и перезапустите движок.",
		},
		Tip:        "Всегда проверяйте список серверов после ручного объединения нескольких подписок в один файл.",
		ExampleLog: "level=error msg=\"proxy 'Germany-Fast' already exists, duplicate name\"",
		Keywords:   []string{"already exists duplicate name", "duplicate proxy", "дубликаты серверов", "одинаковые имена"},
		Match: func(text string) bool {
			return matchAny(text, "already exists, duplicate name", "duplicate proxy name", "outbound tag already exists")
		},
	},
	{
		ID:       "sub_sni_blocked_by_isp",
		Category: "Подписки и прокси",
		Code:     "SUB_SNI_BLOCKED_RKN",
		Title:    "Домен маскировки (SNI) из подписки заблокирован провайдером",
		Summary:  "Роутер не может подключиться к серверу Reality или Trojan, так как маскировочный домен находится в черном списке ТСПУ / РКН.",
		Cause:    "Провайдер подписки выбрал в качестве SNI домен, который заблокирован в РФ по сигнатуре TLS ClientHello.",
		ActionSteps: []string{
			"В настройках узла замените домен маскировки (SNI) на незаблокированный популярный зарубежный ресурс.",
			"Подходящие домены для проверки: `gateway.icloud.com`, `swdist.apple.com`, `www.microsoft.com`, `dl.google.com`.",
			"Убедитесь, что сервер маскировки настроен на поддержку выбранного домена.",
			"Проверьте задержку и сохраните узел.",
		},
		Tip:        "Выбирайте домены крупных корпораций, которые провайдеры связи никогда не блокируют во избежание сбоев в работе операционных систем.",
		ExampleLog: "dial tcp: connection reset by peer during TLS handshake (SNI blocked)",
		Keywords:   []string{"sni blocked", "connection reset during tls handshake", "блокировка sni", "тспу sni"},
		Match: func(text string) bool {
			return matchAny(text, "connection reset by peer during tls", "sni blocked by dpi", "handshake reset by peer")
		},
	},
	{
		ID:       "sub_dynamic_dns_port_shift",
		Category: "Подписки и прокси",
		Code:     "SUB_PORT_SHIFT_OUTDATED",
		Title:    "У провайдера сменился порт подключения к серверу",
		Summary:  "Соединение с сервером обрывается: старый порт закрыт, так как поставщик VPN выполнил плановую ротацию портов.",
		Cause:    "Многие сервисы периодически меняют порты подключения для защиты серверов от сканирования и блокировок.",
		ActionSteps: []string{
			"На вкладке «Подписки» нажмите кнопку «Обновить всё».",
			"AWG Manager скачает свежий список серверов с актуальными номерами портов.",
			"Проверьте задержку серверов после обновления.",
			"Активируйте обновленный сервер.",
		},
		Tip:        "Включите автообновление подписки раз в 24 часа, чтобы ротация портов происходила незаметно для вас.",
		ExampleLog: "dial tcp 198.51.100.4:34512: connect: connection refused (port closed by host)",
		Keywords:   []string{"port closed by host", "ротация портов", "смена порта", "порт сменился"},
		Match: func(text string) bool {
			return matchAny(text, "port closed by host", "connection refused on dynamic port", "port changed on server")
		},
	},
	{
		ID:       "sub_gzip_decompression_error",
		Category: "Подписки и прокси",
		Code:     "SUB_GZIP_DECOMPRESS_FAIL",
		Title:    "Ошибка распаковки сжатого ответа подписки (Gzip / Deflate)",
		Summary:  "Роутер получил поврежденный сжатый архив при скачивании ссылки с заголовком `Content-Encoding: gzip`.",
		Cause:    "Обрыв передачи данных на медленном или нестабильном интернет-канале до завершения загрузки архива.",
		ActionSteps: []string{
			"Повторите обновление подписки.",
			"Если ошибка повторяется, отключите заголовок `Accept-Encoding: gzip` в расширенных параметрах подписки.",
			"Проверьте стабильность основного интернет-канала роутера.",
			"Попробуйте открыть ссылку через браузер.",
		},
		Tip:        "Отключение gzip заставляет сервер отдавать чистый текст, устойчивый к неполной передаче.",
		ExampleLog: "subscription: gzip: invalid header: unexpected EOF",
		Keywords:   []string{"gzip: invalid header", "decompression error", "ошибка gzip", "сжатие подписки"},
		Match: func(text string) bool {
			return matchAny(text, "gzip: invalid header", "unexpected eof in gzip", "decompression error")
		},
	},
	{
		ID:       "sub_file_write_permission_denied",
		Category: "Подписки и прокси",
		Code:     "SUB_CACHE_WRITE_DENIED",
		Title:    "Отказано в доступе при сохранении кэша подписки на диск",
		Summary:  "AWG Manager не смог записать скачанный файл конфигурации в каталог `/opt/etc/awg-manager/subs/`.",
		Cause:    "Файловая система USB-диска перешла в режим только чтения (Read-Only) из-за сбоя накопителя, либо нарушены права доступа (chmod/chown).",
		ActionSteps: []string{
			"Проверьте состояние USB-диска на вкладке «Система».",
			"Убедитесь, что накопитель не перешел в режим `ro` (read-only): выполните в терминале команду `mount | grep /opt`.",
			"Восстановите права доступа: `chmod -R 755 /opt/etc/awg-manager`.",
			"Если диск в режиме только чтения, перезагрузите роутер или выполните проверку диска `fsck`.",
		},
		Tip:        "Регулярно проверяйте здоровье USB-накопителя, чтобы предотвратить внезапную потерю настроек.",
		ExampleLog: "subscription: save cache failed: open /opt/etc/awg-manager/subs/1.yaml: Read-only file system",
		Keywords:   []string{"save cache failed", "read-only file system sub", "права записи подписки", "cache write denied"},
		Match: func(text string) bool {
			return matchAny(text, "save cache failed", "cannot write subscription cache", "subs/: read-only file system")
		},
	},

	// =========================================================================
	// 6. DNS, TPROXY, МАРШРУТИЗАЦИЯ И ФАЕРВОЛ
	// =========================================================================
	{
		ID:       "dns_port_53_in_use",
		Category: "DNS и Маршрутизация",
		Code:     "DNS_EADDRINUSE_53",
		Title:    "Порт 53 (DNS) уже занят системным dnsmasq роутера",
		Summary:  "Mihomo или Sing-box не могут запустить встроенный DNS-сервер на стандартном порту 53, так как на нем уже работает штатный DNS-сервер KeeneticOS.",
		Cause:    "Конфликт за системный порт 53. В роутерах Keenetic порт 53 всегда закреплен за внутренним сервисом dnsmasq/ndm.",
		ActionSteps: []string{
			"В AWG Manager перейдите на вкладку «Маршрутизация».",
			"Убедитесь, что встроенный DNS-сервер движка настроен на нестандартный порт (например: 1053 или 7874).",
			"Для перехвата DNS запросов используйте правила iptables redirect, а не прямую привязку к порту 53.",
			"Нажмите «Применить» и перезапустите движок.",
		},
		Tip:        "Роутеры Keenetic перенаправляют локальные DNS-запросы на порт прокси через iptables DNAT без конфликта с портом 53.",
		ExampleLog: "dns: listen udp 0.0.0.0:53: bind: address already in use",
		Keywords:   []string{"dns port 53", "address already in use 53", "порт 53 занят", "dnsmasq конфликт"},
		Match: func(text string) bool {
			return matchAny(text, "listen udp 0.0.0.0:53", ":53: bind: address already in use", "dns: port 53 already in use")
		},
	},
	{
		ID:       "dns_leak_detected",
		Category: "DNS и Маршрутизация",
		Code:     "DNS_LEAK_WARNING",
		Title:    "Обнаружена утечка DNS (DNS Leak) мимо защищенного туннеля",
		Summary:  "DNS-запросы с ваших устройств уходят напрямую на серверы вашего интернет-провайдера в незашифрованном виде.",
		Cause:    "На клиентском устройстве жестко задан статический DNS, либо роутер отдает свой WAN DNS по протоколу DHCP без перехвата.",
		ActionSteps: []string{
			"В AWG Manager на вкладке «Маршрутизация» включите опцию «Принудительный перехват DNS (DNS Hijack)».",
			"Проверьте, чтобы на роутере в таблице iptables было активно правило перенаправления UDP/TCP 53 в туннель.",
			"На компьютере в свойствах сетевого адаптера установите «Получать адрес DNS автоматически».",
			"Проверьте отсутствие утечек на сайте dnsleaktest.com.",
		},
		Tip:        "Включение DNS Hijack принудительно заворачивает абсолютно все запросы на порт 53 в защищенный DNS движка.",
		ExampleLog: "dns: warning: plaintext DNS request from 192.168.1.50 bypassed secure upstream",
		Keywords:   []string{"dns leak", "утечка dns", "dns leak detected", "dns hijack"},
		Match: func(text string) bool {
			return matchAny(text, "dns leak detected", "plaintext dns request", "dns bypassed secure upstream")
		},
	},
	{
		ID:       "dns_loop_detected",
		Category: "DNS и Маршрутизация",
		Code:     "DNS_RECURSION_LOOP",
		Title:    "Обнаружена петля рекурсии DNS (DNS Loop)",
		Summary:  "DNS-запросы зациклились: прокси-сервер отправляет DNS-запрос роутеру, а роутер пересылает его обратно в прокси.",
		Cause:    "В качестве вышестоящего (upstream) DNS в конфиге указан локальный адрес `127.0.0.1` или `192.168.1.1`, который сам заворачивается в TProxy.",
		ActionSteps: []string{
			"Откройте настройки DNS в конфигурации активного ядра (Sing-box или Mihomo).",
			"Замените upstream серверы на прямые внешние публичные IP (например: `77.88.8.8`, `1.1.1.1`).",
			"Убедитесь, что для этих IP-адресов создано правило прямого выхода (`DIRECT`).",
			"Перезапустите движок маршрутизации.",
		},
		Tip:        "Upstream DNS серверы всегда должны опрашиваться напрямую без захода в петлю перехватчика трафика.",
		ExampleLog: "dns: loop detected: query for google.com forwarded back to self",
		Keywords:   []string{"dns loop", "loop detected query", "петля dns", "зацикливание dns"},
		Match: func(text string) bool {
			return matchAny(text, "dns: loop detected", "forwarded back to self", "dns recursion loop")
		},
	},
	{
		ID:       "dns_servfail_dnssec",
		Category: "DNS и Маршрутизация",
		Code:     "DNS_SERVFAIL_BOGUS",
		Title:    "Ошибка DNSSEC (SERVFAIL): Ответ заблокирован как неподлинный",
		Summary:  "DNS-резолвер вернул ошибку SERVFAIL, так как цифровая подпись домена не прошла валидацию DNSSEC.",
		Cause:    "Рассинхронизация системных часов роутера (проверка подписи зависит от точного времени) либо подмена DNS провайдером.",
		ActionSteps: []string{
			"Проверьте системное время роутера на вкладке «Система» и выполните синхронизацию по NTP.",
			"Если время точное, возможно, DNS-сервер провайдера подменяет записи. Переключитесь на DoH (DNS over HTTPS).",
			"В конфигурации DNS временно отключите жесткую проверку: `\"dnssec\": false`.",
			"Перезапустите DNS-службу.",
		},
		Tip:        "Использование DoH (например, `https://dns.google/dns-query`) исключает ошибки подмены записей на сети провайдера.",
		ExampleLog: "dns: validating example.com: bogus response, SERVFAIL generated",
		Keywords:   []string{"servfail", "dnssec bogus", "валидация dnssec", "ошибка dnssec"},
		Match: func(text string) bool {
			return matchAny(text, "bogus response, servfail", "dnssec validation failed", "servfail generated")
		},
	},
	{
		ID:       "dns_upstream_unreachable",
		Category: "DNS и Маршрутизация",
		Code:     "DNS_UPSTREAM_TIMEOUT",
		Title:    "Ни один из внешних DNS-серверов (Upstream DNS) не отвечает",
		Summary:  "Роутер не может открыть ни один сайт по доменному имени, так как все настроенные серверы имен недоступны.",
		Cause:    "Пропало интернет-соединение у провайдера, блокировка UDP 53 на ТСПУ или указаны недоступные IP-адреса DNS.",
		ActionSteps: []string{
			"Проверьте статус интернет-подключения на главной странице роутера.",
			"В настройках DNS добавьте надежный отечественный DNS для резерва: `77.88.8.8` (Яндекс.DNS).",
			"Попробуйте переключить транспорт DNS с UDP на защищенный DoH или DoT (TCP порт 853).",
			"Сохраните настройки и нажмите «Проверить DNS».",
		},
		Tip:        "В РФ некоторые провайдеры блокируют публичные зарубежные DNS по UDP (8.8.8.8, 1.1.1.1). Используйте DoH.",
		ExampleLog: "dns: all upstream servers failed to respond (timeout after 2000ms)",
		Keywords:   []string{"all upstream servers failed", "dns timeout", "не отвечают dns серверы", "upstream unreachable"},
		Match: func(text string) bool {
			return matchAny(text, "all upstream servers failed", "all dns servers failed to respond", "no upstream dns reachable")
		},
	},
	{
		ID:       "dns_dot_doh_tls_fail",
		Category: "DNS и Маршрутизация",
		Code:     "DNS_DOH_TLS_ERROR",
		Title:    "Сбой защищенного соединения DoH / DoT (TLS Handshake Failed)",
		Summary:  "Роутер не смог установить безопасный зашифрованный канал к серверу DNS over HTTPS или DNS over TLS.",
		Cause:    "Блокировка TLS рукопожатия на оборудовании ТСПУ либо устаревший пакет корневых сертификатов `ca-certificates` в системе.",
		ActionSteps: []string{
			"В терминале роутера обновите сертификаты: `opkg update && opkg install ca-certificates`.",
			"Если используется DoT на порту 853 (блокируется многими операторами), переключитесь на DoH (порт 443).",
			"Попробуйте альтернативный адрес DoH: `https://cloudflare-dns.com/dns-query` или `https://common.dot.dns.yandex.net`.",
			"Перезапустите движок.",
		},
		Tip:        "DoH на порту 443 неотличим от обычного веб-трафика и практически не поддается выборочной блокировке.",
		ExampleLog: "dns: DoH request to https://dns.quad9.net/dns-query failed: tls handshake error",
		Keywords:   []string{"doh request failed", "tls handshake error doh", "dot handshake failed", "ошибка doh"},
		Match: func(text string) bool {
			return matchAny(text, "doh request to", "failed: tls handshake error", "dot connection failed")
		},
	},
	{
		ID:       "dns_edns_client_subnet_drop",
		Category: "DNS и Маршрутизация",
		Code:     "DNS_ECS_DROPPED",
		Title:    "Провайдер сбрасывает запросы с опцией EDNS Client Subnet (ECS)",
		Summary:  "DNS-запросы зависают, так как оборудование провайдера отбрасывает нестандартные пакеты с опциями EDNS0.",
		Cause:    "Некоторые корпоративные и мобильные шлюзы некорректно обрабатывают расширение ECS, используемое CDN для гео-оптимизации.",
		ActionSteps: []string{
			"В конфигурации DNS ядра (Mihomo/Sing-box) найдите блок настроек EDNS.",
			"Отключите опцию `use-hosts` или удалите параметр `client-subnet`.",
			"Установите размер буфера EDNS0 равным 1232 байта.",
			"Перезапустите движок маршрутизации.",
		},
		Tip:        "Размер буфера 1232 байта гарантирует прохождение DNS-пакетов без фрагментации в любых мобильных сетях.",
		ExampleLog: "dns: packet with EDNS0 option dropped by gateway",
		Keywords:   []string{"edns0 option dropped", "client-subnet", "edns client subnet", "ecs drop"},
		Match: func(text string) bool {
			return matchAny(text, "edns0 option dropped", "client subnet dropped", "edns buffer overflow")
		},
	},
	{
		ID:       "dns_fakeip_leak_to_wan",
		Category: "DNS и Маршрутизация",
		Code:     "DNS_FAKEIP_WAN_LEAK",
		Title:    "Утечка виртуальных адресов Fake-IP (198.18.x.x) в сеть провайдера",
		Summary:  "Пакеты с фейковыми IP-адресами пула 198.18.0.0/16 уходят в незашифрованный интерфейс WAN провайдера.",
		Cause:    "Маршрут или правило для фейковой подсети отсутствует в таблице iptables, либо сайт не попал под правила перехвата TProxy.",
		ActionSteps: []string{
			"В AWG Manager перейдите на вкладку «Маршрутизация».",
			"Убедитесь, что в правилах фаервола присутствует перехват подсети `198.18.0.0/15` (или 198.18.0.0/16).",
			"Нажмите «Остановить движок», затем «Запустить движок» для восстановления системных правил iptables.",
			"Проверьте таблицу маршрутизации командой `ip route show table 100`.",
		},
		Tip:        "Подсеть Fake-IP предназначена исключительно для внутреннего взаимодействия ядра роутера и никогда не должна покидать WAN.",
		ExampleLog: "firewall: WAN packet dropped: destination 198.18.23.45 is a private fake-ip range",
		Keywords:   []string{"destination 198.18", "fake-ip range", "утечка fake-ip", "fakeip wan leak"},
		Match: func(text string) bool {
			return matchAny(text, "destination 198.18", "fake-ip leak", "private fake-ip range")
		},
	},
	{
		ID:       "tproxy_fwmark_collision",
		Category: "DNS и Маршрутизация",
		Code:     "TPROXY_FWMARK_COLLISION",
		Title:    "Коллизия битовой маски метки трафика (FWMARK Collision)",
		Summary:  "Правила iptables перетирают метки пакетов другого сервиса, вызывая рассинхронизацию маршрутизации.",
		Cause:    "AWG Manager использует fwmark (например, `0x1`), а другой установленный в Entware сервис (zapret, byeDPI, xray) использует ту же метку.",
		ActionSteps: []string{
			"Отключите конкурирующие утилиты обхода блокировок в Entware (например, службы `/opt/etc/init.d/S*zapret`).",
			"В AWG Manager на вкладке «Настройки» выберите уникальную маску маркировки трафика (например, `0x1000/0x1000`).",
			"Перезапустите сетевой стек роутера.",
			"Проверьте правила: `iptables -t mangle -L -n -v`.",
		},
		Tip:        "Не запускайте несколько утилит перехвата трафика (Zapret + AWG Manager) одновременно во избежание конфликтов меток.",
		ExampleLog: "iptables: fwmark 0x1 overwritten by rule in table mangle chain PREROUTING",
		Keywords:   []string{"fwmark collision", "overwritten by rule", "коллизия fwmark", "конфликт меток трафика"},
		Match: func(text string) bool {
			return matchAny(text, "fwmark collision", "overwritten by rule in table mangle", "mark collision detected")
		},
	},
	{
		ID:       "tproxy_ip_rule_missing",
		Category: "DNS и Маршрутизация",
		Code:     "TPROXY_IP_RULE_MISSING",
		Title:    "Отсутствует правило маршрутизации ip rule для перехвата TProxy",
		Summary:  "Пакеты помечаются фаерволом меткой fwmark, но ядро Linux не знает, в какую таблицу маршрутизации их направить.",
		Cause:    "Сброс правил политик маршрутизации демоном NDMS при переподключении интернет-провайдера.",
		ActionSteps: []string{
			"В панели AWG Manager нажмите «Перезапустить движок».",
			"AWG Manager автоматически выполнит команду: `ip rule add fwmark 0x1 table 100`.",
			"Проверьте список правил в терминале: `ip rule show`.",
			"Убедитесь, что правило присутствует перед стандартными правилами lookup main.",
		},
		Tip:        "Демон AWG Manager автоматически отслеживает события сети и восстанавливает правила `ip rule` при смене WAN.",
		ExampleLog: "routing: marked packet has no matching ip rule, falling back to main table",
		Keywords:   []string{"marked packet has no matching ip rule", "ip rule missing", "правило ip rule", "fwmark lookup"},
		Match: func(text string) bool {
			return matchAny(text, "no matching ip rule", "ip rule add fwmark", "missing tproxy routing rule")
		},
	},
	{
		ID:       "tproxy_rp_filter_strict",
		Category: "DNS и Маршрутизация",
		Code:     "TPROXY_RP_FILTER_BLOCK",
		Title:    "Строгий режим rp_filter блокирует транзитные пакеты TProxy",
		Summary:  "Ядро Linux отбрасывает перехваченные пакеты как поддельные (Spoofed), так как их обратный маршрут не совпадает с интерфейсом.",
		Cause:    "Параметр ядра `net.ipv4.conf.all.rp_filter` установлен в значение `1` (Strict Reverse Path Filtering). Для TProxy необходим режим `0` или `2` (Loose).",
		ActionSteps: []string{
			"В терминале роутера выполните: `sysctl -w net.ipv4.conf.all.rp_filter=2` и `sysctl -w net.ipv4.conf.default.rp_filter=2`.",
			"В AWG Manager на вкладке «Система» нажмите «Оптимизировать сетевые параметры ядра».",
			"Убедитесь, что настройка сохранена в `/opt/etc/sysctl.conf` для применения при перезагрузке.",
			"Перезапустите движок маршрутизации.",
		},
		Tip:        "Режим Loose (`rp_filter=2`) полностью безопасен и необходим для корректной работы любой асимметричной маршрутизации.",
		ExampleLog: "kernel: IPv4: martian source 1.1.1.1 from 192.168.1.33, on dev br0 (rp_filter drop)",
		Keywords:   []string{"martian source", "rp_filter drop", "rp_filter=1", "rp_filter strict"},
		Match: func(text string) bool {
			return matchAny(text, "martian source", "rp_filter drop", "reverse path filtering drop")
		},
	},
	{
		ID:       "tproxy_nf_conntrack_untracked",
		Category: "DNS и Маршрутизация",
		Code:     "TPROXY_NOTRACK_CONFLICT",
		Title:    "Флаг NOTRACK нарушает работу механизма перехвата TProxy",
		Summary:  "Сетевые пакеты, помеченные правилом `-j NOTRACK`, не могут быть перехвачены сокетом TProxy.",
		Cause:    "TProxy фундаментально опирается на таблицу состояний conntrack для восстановления оригинального адреса назначения (ORIGINAL_DST).",
		ActionSteps: []string{
			"Проверьте таблицу raw командой: `iptables -t raw -L -n -v`.",
			"Удалите сторонние правила `-j NOTRACK` или `-j CT --notrack` для портов, перехватываемых прокси.",
			"Перезапустите движок в AWG Manager.",
			"Проверьте статус соединений.",
		},
		Tip:        "Правила NOTRACK полезны для высоконагруженных веб-серверов, но несовместимы с прозрачным проксированием на роутере.",
		ExampleLog: "tproxy: cannot retrieve original destination for untracked connection",
		Keywords:   []string{"untracked connection", "tproxy notrack", "original destination untracked", "notrack conflict"},
		Match: func(text string) bool {
			return matchAny(text, "untracked connection", "cannot retrieve original destination", "notrack breaks tproxy")
		},
	},
	{
		ID:       "tproxy_ipv6_not_supported_chain",
		Category: "DNS и Маршрутизация",
		Code:     "TPROXY_IPV6_MISSING",
		Title:    "Цепочка TProxy IPv6 не создана (Утечка IPv6 трафика)",
		Summary:  "IPv4 трафик успешно перехватывается, но IPv6 соединения идут в обход туннеля через обычный интернет.",
		Cause:    "В системе отсутствуют правила `ip6tables` для маркировки и перехвата пакетов IPv6.",
		ActionSteps: []string{
			"Если провайдер не предоставляет IPv6, отключите IPv6 в веб-интерфейсе Keenetic для устранения утечек.",
			"Если вам нужен полноценный IPv6 через прокси, включите опцию «Перехват IPv6 TProxy» в AWG Manager.",
			"Убедитесь, что прокси-сервер поддерживает протокол IPv6.",
			"Перезапустите движок.",
		},
		Tip:        "Самый простой способ избежать утечек трафика — отключить IPv6 в роутере, если ваш VPN-сервер работает только по IPv4.",
		ExampleLog: "routing: IPv6 traffic detected on WAN without ip6tables tproxy rules",
		Keywords:   []string{"ip6tables tproxy", "утечка ipv6", "ipv6 bypass", "tproxy ipv6 missing"},
		Match: func(text string) bool {
			return matchAny(text, "ipv6 traffic detected on wan", "missing ip6tables", "ip6tables: no chain")
		},
	},
	{
		ID:       "route_table_default_gateway_lost",
		Category: "DNS и Маршрутизация",
		Code:     "ROUTE_NO_DEFAULT_GW",
		Title:    "Пропал маршрут по умолчанию (Default Gateway) в таблице main",
		Summary:  "Роутер полностью потерял связь с интернетом: удален шлюз выхода в глобальную сеть.",
		Cause:    "Некорректная команда удаления маршрутов или сбой получения аренды DHCP от интернет-провайдера.",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic проверьте статус подключения «Проводной» или «Мобильный интернет».",
			"Нажмите «Переподключить» интернет-соединение.",
			"В терминале проверьте маршруты: `ip route show` — должна присутствовать строка `default via <IP_шлюза>`.",
			"Если роутер не восстановил шлюз, перезагрузите устройство.",
		},
		Tip:        "AWG Manager никогда не модифицирует системный шлюз по умолчанию таблицы main, изолируя весь VPN-трафик в отдельные таблицы.",
		ExampleLog: "RTNETLINK answers: Network is unreachable (no default route in table main)",
		Keywords:   []string{"network is unreachable default", "no default route", "пропал шлюз", "default via missing"},
		Match: func(text string) bool {
			return matchAny(text, "network is unreachable", "no default route in table main", "default route missing")
		},
	},
	{
		ID:       "route_mtu_blackhole",
		Category: "DNS и Маршрутизация",
		Code:     "ROUTE_MTU_BLACKHOLE",
		Title:    "Блэкхол пакетов из-за завышенного MTU (PMTUD Failure)",
		Summary:  "Мелкие страницы и пинги работают, но тяжелые сайты, авторизация и видеоролики зависают при открытии.",
		Cause:    "Размер сетевого пакета превышает допустимый лимит канала, а промежуточный маршрутизатор сбрасывает сообщения ICMP Fragmentation Needed.",
		ActionSteps: []string{
			"В AWG Manager перейдите на вкладку «Маршрутизация».",
			"Включите опцию «TCP MSS Clamping (Зажим MSS)» на значение `1360` или `1280`.",
			"В настройках туннеля уменьшите MTU до `1280`.",
			"Сохраните настройки и проверьте открытие проблемных сайтов.",
		},
		Tip:        "MTU 1280 — это гарантированный минимальный размер пакета по спецификации IPv6, проходящий через абсолютно любые сети.",
		ExampleLog: "kernel: packet larger than MTU and DF set, ICMP dropped by upstream",
		Keywords:   []string{"mtu blackhole", "pmtud failure", "зажим mss", "зависание сайтов mtu"},
		Match: func(text string) bool {
			return matchAny(text, "packet larger than mtu", "mtu blackhole", "pmtud failure", "df set, icmp dropped")
		},
	},
	{
		ID:       "iptables_chain_already_exists",
		Category: "DNS и Маршрутизация",
		Code:     "IPTABLES_CHAIN_EXISTS",
		Title:    "Цепочка iptables уже создана (Chain already exists)",
		Summary:  "Скрипт инициализации правил фаервола попытался создать существующую цепочку правил.",
		Cause:    "Повторный вызов скрипта запуска без предварительной очистки предыдущих правил (iptables -N вместо проверки существования).",
		ActionSteps: []string{
			"Это неопасная ошибка: цепочка уже активна в системе.",
			"Для чистой переинициализации нажмите кнопку «Очистить и восстановить правила фаервола» в AWG Manager.",
			"Или в терминале выполните сброс цепочки: `iptables -t mangle -F AWGM_PREROUTING`.",
			"Перезапустите движок.",
		},
		Tip:        "Скрипты AWG Manager проверяют наличие цепочки перед созданием, исключая подобные предупреждения.",
		ExampleLog: "iptables: Chain already exists. (iptables -t mangle -N AWGM_TPROXY)",
		Keywords:   []string{"chain already exists", "цепочка уже создана", "iptables chain exists"},
		Match: func(text string) bool {
			return matchAny(text, "chain already exists", "iptables: chain already exists")
		},
	},
	{
		ID:       "iptables_table_lock_busy",
		Category: "DNS и Маршрутизация",
		Code:     "IPTABLES_LOCK_BUSY",
		Title:    "Блокировка iptables занята другим процессом (xtables lock)",
		Summary:  "Команда изменения правил фаервола не смогла выполниться, так как системный файл блокировки удерживается другой программой.",
		Cause:    "Одновременный вызов iptables из нескольких скриптов (например, демон KeeneticOS обновляет NAT одновременно с запуском AWG Manager).",
		ActionSteps: []string{
			"Подождите 2-3 секунды: блокировка xtables снимается автоматически по завершении параллельной операции.",
			"AWG Manager автоматически повторяет попытку с флагом ожидания `-w 5`.",
			"Если блокировка зависла намертво, проверьте процессы: `ps | grep iptables`.",
			"При необходимости перезапустите движок.",
		},
		Tip:        "Параметр `-w` заставляет iptables терпеливо ждать освобождения блокировки, предотвращая сбои при параллельной работе.",
		ExampleLog: "iptables: Another app is currently holding the xtables lock. Perhaps you want to use the -w option?",
		Keywords:   []string{"holding the xtables lock", "xtables lock busy", "блокировка iptables", "another app is holding"},
		Match: func(text string) bool {
			return matchAny(text, "holding the xtables lock", "xtables lock", "resource temporarily unavailable (iptables)")
		},
	},
	{
		ID:       "firewall_wan_forward_drop",
		Category: "DNS и Маршрутизация",
		Code:     "FIREWALL_FORWARD_DROP",
		Title:    "Фаервол роутера сбрасывает транзитный трафик интерфейса туннеля",
		Summary:  "Пакеты из локальной сети не могут пройти через туннель: цепочка FORWARD фаервола блокирует их передачу.",
		Cause:    "В KeeneticOS для интерфейса туннеля не включен уровень доверия (Security level) или отсутствует разрешение транзита между зонами.",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic откройте «Сетевые правила» → «Межсетевой экран».",
			"Убедитесь, что для интерфейса туннеля разрешен входящий и исходящий трафик домашней сети.",
			"Или в AWG Manager включите автоматическую настройку разрешений фаервола.",
			"Проверьте счетчик сброшенных пакетов: `iptables -L FORWARD -v -n`.",
		},
		Tip:        "В KeeneticOS каждый новый сетевой интерфейс по умолчанию помещается в недоверенную зону безопасности.",
		ExampleLog: "kernel: DROP FORWARD: IN=br0 OUT=opkgtun0 SRC=192.168.1.150 DST=1.1.1.1",
		Keywords:   []string{"drop forward", "forward drop", "сброс forward", "фаервол forward"},
		Match: func(text string) bool {
			return matchAny(text, "drop forward:", "in=br0 out=opkgtun", "forward dropped by policy")
		},
	},
	{
		ID:       "nftables_compatibility_fail",
		Category: "DNS и Маршрутизация",
		Code:     "NFTABLES_NOT_SUPPORTED",
		Title:    "Модули ядра не поддерживают nftables (Требуется iptables-legacy)",
		Summary:  "Попытка применить правила через современный интерфейс nftables завершилась ошибкой несовместимости ядра.",
		Cause:    "Стабильные прошивки KeeneticOS на базе ядер Linux 4.9 / 4.14 используют классический стек netfilter (iptables-legacy).",
		ActionSteps: []string{
			"Используйте классические утилиты `iptables` и `ip6tables` вместо `nft`.",
			"В настройках перехвата AWG Manager режим Netfilter iptables включен по умолчанию.",
			"Не удаляйте пакет `iptables` из Entware.",
			"Перезапустите движок маршрутизации.",
		},
		Tip:        "Стек iptables-legacy полностью оптимизирован производителем для максимальной аппаратной разгрузки на роутерах Keenetic.",
		ExampleLog: "nft: table ip filter: Operation not supported (kernel module nf_tables missing)",
		Keywords:   []string{"nf_tables missing", "nftables not supported", "operation not supported nft", "iptables legacy"},
		Match: func(text string) bool {
			return matchAny(text, "nf_tables missing", "nftables not supported", "nft: table ip filter")
		},
	},
	{
		ID:       "route_metric_tie",
		Category: "DNS и Маршрутизация",
		Code:     "ROUTE_METRIC_COLLISION",
		Title:    "Одинаковая метрика у двух интерфейсов выхода в сеть (Конфликт шлюзов)",
		Summary:  "В таблице маршрутизации два разных интерфейса имеют одинаковый приоритет (метрику), вызывая случайные обрывы сессий.",
		Cause:    "Подключение резервного провайдера или второго туннеля с одинаковой метрикой метки (например, metric 0).",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic перейдите в «Сетевые правила» → «Приоритеты подключений».",
			"Распределите подключения по приоритету (Основной провайдер выше Резервного).",
			"Для туннелей AWG Manager задайте метрику вручную (например: 100 для основного, 200 для запасного).",
			"Сохраните настройки.",
		},
		Tip:        "Интерфейс с меньшим числовым значением метрики имеет более высокий приоритет передачи трафика.",
		ExampleLog: "ip route: duplicate default gateway with equal metric detected",
		Keywords:   []string{"equal metric detected", "duplicate default gateway", "конфликт метрик", "одинаковая метрика"},
		Match: func(text string) bool {
			return matchAny(text, "equal metric detected", "duplicate default gateway", "metric collision in routing")
		},
	},
	{
		ID:       "tcp_syn_retransmit_timeout",
		Category: "DNS и Маршрутизация",
		Code:     "TCP_SYN_RETRANSMIT",
		Title:    "Таймаут повторной отправки пакетов TCP SYN (Блокировка узла)",
		Summary:  "Роутер отправляет стартовые запросы на установку TCP соединения, но удаленный узел не отвечает (TCP Retransmission).",
		Cause:    "IP-адрес или порт удаленного сервера заблокирован провайдером на ТСПУ через технологию «Blackholing / Drop» без ответа RST.",
		ActionSteps: []string{
			"Проверьте доступность IP-адреса целевого сервера утилитой ping на вкладке «Диагностика».",
			"Если ping не проходит, а сервер в сети — смените IP-адрес сервера или порт подключения.",
			"Переключитесь на резервный узел или другую локацию из вашей подписки.",
			"Проверьте, не упал ли сам VPS сервер хостинга.",
		},
		Tip:        "ТСПУ в РФ часто глушат трафик «в тишину» (drop), вызывая накопление SYN retransmit в сетевом стеке.",
		ExampleLog: "tcp: connection to 198.51.100.2:443 timed out after 5 syn retransmits",
		Keywords:   []string{"syn retransmits", "tcp syn timeout", "таймаут syn", "блокировка тспу"},
		Match: func(text string) bool {
			return matchAny(text, "syn retransmits", "timed out after syn", "tcp: syn connection timeout")
		},
	},
	{
		ID:       "ip_forward_disabled",
		Category: "DNS и Маршрутизация",
		Code:     "SYSCTL_FORWARD_DISABLED",
		Title:    "Отключена транзитная маршрутизация пакетов в ядре (ip_forward = 0)",
		Summary:  "Роутер перестал пропускать трафик подключенных устройств (компьютеров, телефонов) наружу в интернет.",
		Cause:    "Системный параметр `net.ipv4.ip_forward` сброшен в 0 сторонним скриптом или при аварийном завершении сетевой службы.",
		ActionSteps: []string{
			"Включите форвардинг в терминале командой: `sysctl -w net.ipv4.ip_forward=1`.",
			"Для IPv6: `sysctl -w net.ipv6.conf.all.forwarding=1`.",
			"В AWG Manager на вкладке «Система» нажмите «Исправить системные параметры».",
			"Перезапустите движок маршрутизации.",
		},
		Tip:        "Роутер по определению является маршрутизатором, параметр `ip_forward` обязан всегда быть равен 1.",
		ExampleLog: "kernel: IPv4: packet forwarding is disabled, dropping transit packet",
		Keywords:   []string{"packet forwarding is disabled", "ip_forward=0", "ip_forward disabled", "форвардинг отключен"},
		Match: func(text string) bool {
			return matchAny(text, "packet forwarding is disabled", "ip_forward is disabled", "net.ipv4.ip_forward = 0")
		},
	},
	{
		ID:       "udp_nat_timeout_too_short",
		Category: "DNS и Маршрутизация",
		Code:     "UDP_NAT_TIMEOUT_SHORT",
		Title:    "Слишком короткий таймаут UDP сессий (Разрывы звонков и игр)",
		Summary:  "Голосовые звонки в Telegram, Discord и сессии онлайн-игр внезапно обрываются каждые 30 секунд тишины.",
		Cause:    "Системный таймаут таблицы NAT для UDP пакетов по умолчанию (30 сек) слишком мал для удержания сессии за файрволом.",
		ActionSteps: []string{
			"В терминале Entware увеличьте таймаут: `sysctl -w net.netfilter.nf_conntrack_udp_timeout=60`.",
			"Увеличьте таймаут установленного UDP: `sysctl -w net.netfilter.nf_conntrack_udp_timeout_stream=180`.",
			"В настройках туннелей WireGuard включите `PersistentKeepalive = 25`.",
			"Сохраните настройки.",
		},
		Tip:        "Параметр `PersistentKeepalive = 25` в WireGuard шлет легкие пакеты каждые 25 секунд, поддерживая сессию открытой вечно.",
		ExampleLog: "conntrack: udp session expired after 30 seconds of inactivity",
		Keywords:   []string{"udp session expired", "conntrack udp timeout", "разрыв звонков", "разрывы в играх"},
		Match: func(text string) bool {
			return matchAny(text, "udp session expired", "udp timeout expired", "conntrack_udp_timeout")
		},
	},
	{
		ID:       "policy_routing_table_overflow",
		Category: "DNS и Маршрутизация",
		Code:     "ROUTE_TABLE_ID_OVERFLOW",
		Title:    "Превышен допустимый номер таблицы маршрутизации (Table ID)",
		Summary:  "Система отклонила создание таблицы маршрутизации из-за недопустимого номера идентификатора.",
		Cause:    "Идентификаторы пользовательских таблиц маршрутизации в Linux должны находиться в диапазоне от 1 до 252 (таблицы 253-255 зарезервированы системой).",
		ActionSteps: []string{
			"В свойствах туннеля или политики маршрутизации выберите Table ID в диапазоне от 100 до 200.",
			"Не используйте номера 253 (default), 254 (main) и 255 (local).",
			"Сохраните настройки туннеля.",
			"Перезапустите маршрутизацию.",
		},
		Tip:        "AWG Manager по умолчанию использует проверенные свободные идентификаторы таблиц (1001-1050).",
		ExampleLog: "ip route: invalid table ID: table 256 is out of range",
		Keywords:   []string{"invalid table id", "table is out of range", "номер таблицы маршрутизации", "table id overflow"},
		Match: func(text string) bool {
			return matchAny(text, "invalid table id", "table is out of range", "routing table id invalid")
		},
	},
	{
		ID:       "ip_local_port_range_exhausted",
		Category: "DNS и Маршрутизация",
		Code:     "EADDRNOTAVAIL_PORTS_FULL",
		Title:    "Исчерпан диапазон локальных эфемерных портов",
		Summary:  "Роутер не может открыть новое исходящее соединение, так как все локальные порты заняты активными сессиями.",
		Cause:    "Утечка сокетов в состоянии `TIME_WAIT` или слишком узкий диапазон `ip_local_port_range` при большом количестве клиентов.",
		ActionSteps: []string{
			"В терминале роутера расширьте диапазон портов: `sysctl -w net.ipv4.ip_local_port_range=\"10240 65535\"`.",
			"Разрешите повторное использование сокетов: `sysctl -w net.ipv4.tcp_tw_reuse=1`.",
			"В AWG Manager на вкладке «Система» нажмите «Оптимизировать сетевые параметры».",
			"Перезапустите активный сетевой движок.",
		},
		Tip:        "Включение `tcp_tw_reuse=1` безопасно освобождает закрывающиеся соединения для новых запросов.",
		ExampleLog: "connect: cannot assign requested address (local ephemeral port range exhausted)",
		Keywords:   []string{"cannot assign requested address", "port range exhausted", "порты исчерпаны", "нет свободных портов"},
		Match: func(text string) bool {
			return matchAny(text, "cannot assign requested address", "port range exhausted", "ephemeral port range")
		},
	},

	// =========================================================================
	// 7. СИСТЕМА, ENTWARE И ОБОРУДОВАНИЕ РОУТЕРА
	// =========================================================================
	{
		ID:       "sys_oom_killer",
		Category: "Система и Entware",
		Code:     "KERNEL_OOM_KILLER",
		Title:    "Процесс аварийно завершен ядром роутера (Out of Memory)",
		Summary:  "На роутере полностью закончилась оперативная память (RAM), и ядро Linux принудительно убило самый прожорливый процесс (Mihomo, Sing-box или AWG Manager).",
		Cause:    "Тяжелые наборы правил (Rule-Set), большие кэши DNS или отсутствие файла подкачки (SWAP) на роутере с 128-256 МБ памяти.",
		ActionSteps: []string{
			"Подключите USB-флешку к роутеру и создайте раздел подкачки (SWAP) размером 512 МБ - 1 ГБ.",
			"В AWG Manager на вкладке «Система» проверьте график использования оперативной памяти.",
			"В настройках Mihomo/Sing-box сократите количество внешних правил или используйте бинарные форматы SRS/MMDB.",
			"Перезапустите движок.",
		},
		Tip:        "Наличие SWAP-раздела на роутере с небольшим объемом памяти полностью предотвращает аварийные падения ядра.",
		ExampleLog: "kernel: Out of memory: Kill process 1234 (mihomo) score 250 or sacrifice child",
		Keywords:   []string{"out of memory", "oom killer", "kill process", "нехватка памяти", "завершен ядром"},
		Match: func(text string) bool {
			return matchAny(text, "out of memory: kill process", "oom-killer", "invoked oom-killer")
		},
	},
	{
		ID:       "sys_disk_full_opt",
		Category: "Система и Entware",
		Code:     "ENOSPC_DISK_FULL",
		Title:    "Закончилось свободное место на диске (/opt переполнен)",
		Summary:  "Раздел `/opt` на USB-накопителе заполнен на 100%, программы не могут сохранять конфигурации, логи и базы данных GeoIP.",
		Cause:    "Накопление старых логов, разрастание дампов или использование слишком маленькой флешки.",
		ActionSteps: []string{
			"В AWG Manager на вкладке «Система» проверьте остаток свободного места на диске.",
			"В терминале выполните очистку кэша пакетов: `opkg clean`.",
			"Удалите старые журналы: `rm -f /opt/var/log/*.1 /opt/var/log/*.gz /opt/var/log/*.log`.",
			"При необходимости замените USB-накопитель на более вместительный (рекомендуется от 8 ГБ).",
		},
		Tip:        "Всегда оставляйте не менее 200 МБ свободного места на диске для корректной записи обновлений GeoIP баз.",
		ExampleLog: "write /opt/etc/mihomo/config.yaml: no space left on device",
		Keywords:   []string{"no space left on device", "диск переполнен", "enospc", "память диска"},
		Match: func(text string) bool {
			return matchAny(text, "no space left on device", "enospc", "disk full on /opt")
		},
	},
	{
		ID:       "sys_inodes_exhausted",
		Category: "Система и Entware",
		Code:     "ENOSPC_INODES_FULL",
		Title:    "Закончились индексные дескрипторы (Inodes Full)",
		Summary:  "На диске есть свободные мегабайты, но создать новые файлы невозможно, так как исчерпана таблица дескрипторов (inodes) файловой системы.",
		Cause:    "Слишком много мелких временных файлов в папках `/opt/tmp`, `/opt/var/run` или `/opt/var/spool`.",
		ActionSteps: []string{
			"Проверьте состояние счетчика дескрипторов в терминале: `df -i /opt`.",
			"Очистите временные каталоги: `rm -rf /opt/tmp/*`.",
			"Проверьте, не плодит ли сторонний скрипт миллионы пустых сессионных файлов.",
			"При форматировании диска в Ext4 используйте стандартный размер inode.",
		},
		Tip:        "Команда `df -i` показывает процент занятых inode независимо от оставшихся гигабайт на диске.",
		ExampleLog: "mkdir: cannot create directory '/opt/tmp/cache': No space left on device (inodes full)",
		Keywords:   []string{"inodes full", "дескрипторы исчерпаны", "no space left on device inodes"},
		Match: func(text string) bool {
			return matchAny(text, "inodes full", "no space left on device (inodes", "out of inodes")
		},
	},
	{
		ID:       "sys_ntp_clock_skew_1970",
		Category: "Система и Entware",
		Code:     "NTP_CLOCK_SKEW",
		Title:    "Рассинхронизация системного времени роутера (NTP)",
		Summary:  "Часы роутера сбросились на 1970 год (или показывают неверную дату). Все TLS-рукопожатия, подписки и сертификаты блокируются.",
		Cause:    "В роутерах отсутствует встроенная батарейка RTC (Real-Time Clock). При включении роутер не знает времени, пока не получит ответ от NTP сервера.",
		ActionSteps: []string{
			"На вкладке «Система» нажмите кнопку «Синхронизировать время с NTP».",
			"В веб-интерфейсе Keenetic проверьте раздел «Сетевые правила» → «Сетевые службы» → «Время (NTP)».",
			"Укажите надежные NTP серверы: `pool.ntp.org` и `time.google.com`.",
			"После корректной синхронизации времени перезапустите сетевые движки.",
		},
		Tip:        "Если у провайдера заблокирован протокол NTP (UDP 123), используйте утилиту `tlsdate` или `ntpdate -u`.",
		ExampleLog: "x509: certificate is not yet valid: clock skew detected (system time: 1970-01-01)",
		Keywords:   []string{"clock skew", "system time: 1970", "рассинхронизация времени", "ntp", "время роутера"},
		Match: func(text string) bool {
			return matchAny(text, "clock skew detected", "system time: 1970", "certificate is not yet valid", "clock skew")
		},
	},
	{
		ID:       "sys_opkg_lock_busy",
		Category: "Система и Entware",
		Code:     "OPKG_LOCK_BUSY",
		Title:    "Менеджер пакетов opkg заблокирован другим процессом",
		Summary:  "Невозможно установить или обновить пакеты в Entware: процесс сообщает, что файл `/opt/var/lock/opkg.lock` заблокирован.",
		Cause:    "В фоне уже выполняется другая операция `opkg install / update` либо предыдущая команда была аварийно прервана.",
		ActionSteps: []string{
			"Подождите 30 секунд — фоновое обновление может завершиться самостоятельно.",
			"Проверьте список запущенных процессов: `ps | grep opkg`.",
			"Если зависших процессов нет, удалите зависший файл блокировки: `rm -f /opt/var/lock/opkg.lock`.",
			"Повторите установку пакета.",
		},
		Tip:        "Никогда не перезагружайте роутер прямо во время выполнения команды `opkg upgrade`.",
		ExampleLog: "opkg: Could not lock /opt/var/lock/opkg.lock: Resource temporarily unavailable",
		Keywords:   []string{"could not lock opkg.lock", "opkg lock", "блокировка opkg", "opkg busy"},
		Match: func(text string) bool {
			return matchAny(text, "could not lock /opt/var/lock/opkg.lock", "opkg.lock: resource temporarily unavailable")
		},
	},
	{
		ID:       "entware_feed_404_offline",
		Category: "Система и Entware",
		Code:     "ENTWARE_FEED_UNREACHABLE",
		Title:    "Репозиторий пакетов Entware недоступен (Ошибка 404 / 503)",
		Summary:  "Команда `opkg update` не может загрузить списки пакетов с официального зеркала Entware.",
		Cause:    "Временная недоступность главного сервера `bin.entware.net` либо провайдер блокирует доступ к архивам.",
		ActionSteps: []string{
			"Попробуйте повторить обновление через 10-15 минут.",
			"В файле `/opt/etc/opkg.conf` временно переключите зеркало на альтернативное (например, российское зеркало Яндекса или Keenetic).",
			"Проверьте, есть ли на роутере прямой доступ в интернет без прокси.",
			"Повторите команду `opkg update`.",
		},
		Tip:        "Локальное зеркало пакетов Keenetic часто работает быстрее и стабильнее зарубежного сервера.",
		ExampleLog: "opkg_download: Failed to download http://bin.entware.net/aarch64-k3.10/Packages.gz, wget returned 4",
		Keywords:   []string{"failed to download bin.entware.net", "packages.gz 404", "зеркало entware", "opkg update failed"},
		Match: func(text string) bool {
			return matchAny(text, "failed to download http://bin.entware.net", "packages.gz, wget returned", "cannot download feed")
		},
	},
	{
		ID:       "entware_usb_filesystem_readonly",
		Category: "Система и Entware",
		Code:     "USB_FS_READONLY",
		Title:    "Файловая система флешки перешла в режим только чтения (Read-Only)",
		Summary:  "Ядро Linux заблокировало запись на USB-накопитель для защиты данных от повреждения после обнаружения ошибок ввода-вывода.",
		Cause:    "Внезапное отключение питания роутера, износ ячеек памяти флешки или небезопасное извлечение накопителя.",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic выполните безопасное отключение накопителя.",
			"Подключите накопитель к ПК и выполните проверку диска: в Linux `fsck.ext4 -f /dev/sdX`, в Windows `chkdsk`.",
			"После исправления файловой системы подключите флешку обратно к роутеру.",
			"Проверьте статус монтирования: в выводе `mount` раздел `/opt` должен иметь флаг `rw` (а не `ro`).",
		},
		Tip:        "Для роутеров всегда используйте качественные SSD или флешки с поддержкой wear leveling (контролем износа ячеек).",
		ExampleLog: "kernel: EXT4-fs (sda1): Remounting filesystem read-only after error",
		Keywords:   []string{"remounting filesystem read-only", "read-only after error", "диск только чтение", "ext4 read-only"},
		Match: func(text string) bool {
			return matchAny(text, "remounting filesystem read-only", "filesystem read-only", "remounting fs ro")
		},
	},
	{
		ID:       "entware_curl_ca_certificates_missing",
		Category: "Система и Entware",
		Code:     "ENTWARE_CA_CERTS_MISSING",
		Title:    "Отсутствуют корневые сертификаты SSL (ca-certificates)",
		Summary:  "Консольные утилиты curl, wget и python выдают ошибку проверки SSL при обращении к любым сайтам по HTTPS.",
		Cause:    "В минимальной установке Entware пакет корневых доверенных сертификатов не всегда устанавливается автоматически.",
		ActionSteps: []string{
			"Установите пакет доверенных сертификатов: `opkg update && opkg install ca-certificates`.",
			"Убедитесь, что файл сертификатов присутствует в системе: `/opt/etc/ssl/certs/ca-certificates.crt`.",
			"В AWG Manager нажмите «Проверить системные зависимости».",
			"Перезапустите сетевые службы.",
		},
		Tip:        "Пакет `ca-certificates` необходим для защищенного скачивания подписок и взаимодействия с облачными сервисами.",
		ExampleLog: "curl: (60) SSL certificate problem: unable to get local issuer certificate",
		Keywords:   []string{"unable to get local issuer certificate", "ca-certificates missing", "ssl certificate problem", "curl 60"},
		Match: func(text string) bool {
			return matchAny(text, "unable to get local issuer certificate", "certificate problem: unable to get", "ca-certificates not found")
		},
	},
	{
		ID:       "system_swap_missing",
		Category: "Система и Entware",
		Code:     "SYSTEM_NO_SWAP_WARNING",
		Title:    "На роутере отсутствует файл подкачки (SWAP)",
		Summary:  "Роутер работает исключительно на физической RAM. При пиковых нагрузках Sing-box или Mihomo могут быть внезапно закрыты ядром.",
		Cause:    "В роутере с объемом памяти 128 МБ или 256 МБ не подключен и не активирован swap-раздел на накопителе.",
		ActionSteps: []string{
			"В AWG Manager на вкладке «Система» нажмите кнопку «Создать файл подкачки (SWAP 512MB)».",
			"Либо в терминале выполните команды:",
			"`dd if=/dev/zero of=/opt/swap bs=1M count=512 && mkswap /opt/swap && swapon /opt/swap`.",
			"Добавьте команду `swapon /opt/swap` в автозагрузку `/opt/etc/init.d/S01swap`.",
		},
		Tip:        "Файл подкачки размером всего 512 МБ гарантирует абсолютную стабильность работы тяжелых ядер прокси 24/7.",
		ExampleLog: "system: warning: zero swap space detected on router with < 256MB RAM",
		Keywords:   []string{"zero swap space", "swap missing", "нет swap", "файл подкачки"},
		Match: func(text string) bool {
			return matchAny(text, "zero swap space", "swap space is 0", "no swap partition")
		},
	},
	{
		ID:       "system_cpu_throttling_overheat",
		Category: "Система и Entware",
		Code:     "CPU_THERMAL_THROTTLING",
		Title:    "Перегрев процессора роутера (Thermal Throttling)",
		Summary:  "Температура чипа превысила критический порог, и роутер принудительно снизил частоту ядер, из-за чего упала скорость интернета.",
		Cause:    "Плохая вентиляция корпуса роутера (установка в закрытом слаботочном щитке), запыление или длительное скачивание на максимальной скорости.",
		ActionSteps: []string{
			"Обеспечьте свободную циркуляцию воздуха вокруг корпуса роутера.",
			"Не ставьте роутер вплотную к нагревающимся приборам или на полку закрытого шкафа.",
			"Проверьте температуру в веб-интерфейсе Keenetic или в терминале: `cat /sys/class/thermal/thermal_zone0/temp`.",
			"При постоянной нагрузке установите роутер вертикально на штатную подставку/стену.",
		},
		Tip:        "Вертикальное крепление роутера на стену улучшает естественную конвекцию воздуха на 10-15°C.",
		ExampleLog: "kernel: thermal thermal_zone0: critical temperature reached, throttling CPU",
		Keywords:   []string{"critical temperature reached", "thermal throttling", "перегрев процессора", "температура роутера"},
		Match: func(text string) bool {
			return matchAny(text, "critical temperature reached", "thermal throttling", "throttling cpu")
		},
	},
	{
		ID:       "system_ulimit_nofile_too_low",
		Category: "Система и Entware",
		Code:     "EMFILE_TOO_MANY_FILES",
		Title:    "Превышен лимит открытых файловых дескрипторов (ulimit -n)",
		Summary:  "Процесс прокси или веб-сервер не может открыть новое сетевое соединение (Too many open files).",
		Cause:    "Стандартный лимит на число открытых файлов для непривилегированного процесса (обычно 1024) исчерпан тысячами одновременных TCP-сессий.",
		ActionSteps: []string{
			"В терминале роутера проверьте текущий лимит: `ulimit -n`.",
			"Увеличьте лимит до 16384 в скрипте автозапуска `/opt/etc/init.d/S99awg-manager`: добавьте строку `ulimit -n 16384`.",
			"Увеличьте глобальный лимит системы: `sysctl -w fs.file-max=65536`.",
			"Перезапустите сетевой сервис.",
		},
		Tip:        "В Linux каждый сетевой сокет является файловым дескриптором, поэтому высокий ulimit критичен для прокси.",
		ExampleLog: "accept tcp: accept4: too many open files in system",
		Keywords:   []string{"too many open files", "ulimit -n", "emfile", "лимит файлов исчерпан"},
		Match: func(text string) bool {
			return matchAny(text, "too many open files", "accept4: too many open files", "emfile")
		},
	},
	{
		ID:       "system_bad_sectors_flash",
		Category: "Система и Entware",
		Code:     "STORAGE_BAD_BLOCKS",
		Title:    "Обнаружены аппаратные сбои на флешке (I/O error / Bad sectors)",
		Summary:  "Флеш-накопитель выходит из строя: ядро Linux фиксирует ошибки контрольной суммы блоков данных и сбросы шины USB.",
		Cause:    "Выработка ресурса ячеек памяти NAND флеш-накопителя (Bad Blocks) после долгой непрерывной записи логов.",
		ActionSteps: []string{
			"Срочно сделайте резервную копию конфигураций AWG Manager на компьютер.",
			"Подготовьте новый надежный USB-накопитель.",
			"Скопируйте рабочие файлы со старой флешки.",
			"Не используйте поврежденный накопитель во избежание внезапной потери настроек.",
		},
		Tip:        "Для круглосуточной работы Entware лучше всего использовать компактный внешний SSD накопитель.",
		ExampleLog: "kernel: Buffer I/O error on dev sda1, logical block 20480, async page read",
		Keywords:   []string{"buffer i/o error", "bad blocks", "сбойные сектора", "ошибка диска i/o"},
		Match: func(text string) bool {
			return matchAny(text, "buffer i/o error on dev", "i/o error, dev sda", "bad block detected")
		},
	},
	{
		ID:       "system_dmesg_segfault",
		Category: "Система и Entware",
		Code:     "KERNEL_SEGFAULT",
		Title:    "Аварийное завершение программы (Segmentation fault)",
		Summary:  "Бинарный файл упал с ошибкой нарушения защиты памяти (Segfault).",
		Cause:    "Повреждение бинарного файла на диске, несовпадение архитектуры процессора (например, сборка armv7 вместо aarch64) или аппаратный сбой RAM.",
		ActionSteps: []string{
			"Переустановите проблемный пакет через `opkg install --force-reinstall <имя_пакета>`.",
			"Убедитесь, что установлена версия для точной архитектуры вашего роутера (mips / mipsel / aarch64).",
			"В AWG Manager нажмите «Обновить бинарные файлы ядер».",
			"Перезапустите службу.",
		},
		Tip:        "Всегда проверяйте соответствие архитектуры роутера перед ручной установкой скачанных бинарников.",
		ExampleLog: "kernel: mihomo[4512]: segfault at 0 ip 0000005580 sp 0000007fe0 error 4",
		Keywords:   []string{"segfault at", "segmentation fault", "ошибка сегментации", "segfault"},
		Match: func(text string) bool {
			return matchAny(text, "segfault at", "segmentation fault", "general protection fault")
		},
	},
	{
		ID:       "system_kernel_panic_softlockup",
		Category: "Система и Entware",
		Code:     "KERNEL_SOFT_LOCKUP",
		Title:    "Зависание ядра роутера (Soft lockup / CPU stuck)",
		Summary:  "Одно из ядер процессора роутера зависло в бесконечном цикле обработки сетевого прерывания или драйвера.",
		Cause:    "Баг в стороннем модуле ядра или аппаратный сбой сетевого свитча при шторме широковещательных пакетов (Broadcast Storm).",
		ActionSteps: []string{
			"Перезагрузите роутер по питанию.",
			"Проверьте кабельные подключения к портам роутера на наличие сетевых петель (Loop).",
			"Обновите прошивку KeeneticOS до актуальной стабильной версии.",
			"Если в системе установлены экспериментальные модули ядра, отключите их.",
		},
		Tip:        "Включение аппаратного сторожевого таймера Watchdog в KeeneticOS помогает роутеру автоматически перезагружаться при софтлоках.",
		ExampleLog: "kernel: BUG: soft lockup - CPU#0 stuck for 22s! [swapper/0:0]",
		Keywords:   []string{"soft lockup", "cpu stuck", "зависание ядра", "kernel bug"},
		Match: func(text string) bool {
			return matchAny(text, "soft lockup - cpu", "stuck for 22s", "kernel panic - not syncing")
		},
	},
	{
		ID:       "entware_init_service_failed",
		Category: "Система и Entware",
		Code:     "INIT_SCRIPT_EXEC_FAILED",
		Title:    "Сбой выполнения стартового скрипта службы (/opt/etc/init.d/)",
		Summary:  "Служба не смогла запуститься при старте роутера из-за ошибки в init-скрипте или отсутствия прав на исполнение.",
		Cause:    "У скрипта снят флаг исполняемости `chmod +x` либо файл сохранен с Windows-переносами строк (CRLF вместо LF).",
		ActionSteps: []string{
			"Установите права на исполнение в терминале: `chmod +x /opt/etc/init.d/S*`.",
			"Преобразуйте переносы строк из Windows формата в UNIX: `dos2unix /opt/etc/init.d/S99awg-manager`.",
			"Запустите скрипт вручную для просмотра ошибок: `/opt/etc/init.d/S99awg-manager restart`.",
			"Проверьте статус службы.",
		},
		Tip:        "Редактируйте файлы конфигурации Linux только в редакторах с поддержкой UNIX-окончаний строк (LF), например VS Code или nano.",
		ExampleLog: "/opt/etc/init.d/S99awg-manager: line 1: syntax error: unexpected newline (dos format)",
		Keywords:   []string{"syntax error: unexpected newline", "init.d failed", "ошибка скрипта автозапуска", "chmod +x init.d"},
		Match: func(text string) bool {
			return matchAny(text, "syntax error: unexpected newline", "/opt/etc/init.d/", "failed to execute init script")
		},
	},
	{
		ID:       "system_load_average_critical",
		Category: "Система и Entware",
		Code:     "SYSTEM_LOAD_CRITICAL",
		Title:    "Критическая перегрузка процессора роутера (Load Average > 8.0)",
		Summary:  "Очередь задач к процессору роутера критически выросла, роутер медленно реагирует на команды и теряет пакеты.",
		Cause:    "Высокая скорость шифрования тяжелых протоколов без аппаратной поддержки, фоновая компиляция или майнинг/сканирование.",
		ActionSteps: []string{
			"В терминале проверьте активные процессы командой `top` или `htop` (сортировка по CPU).",
			"Определите процесс, потребляющий более 80% процессора.",
			"В AWG Manager переключитесь на протокол с меньшей нагрузкой на CPU (например, WireGuard/AmneziaWG или VLESS вместо heavy shadowsocks).",
			"Ограничьте скорость торрент-клиента на роутере.",
		},
		Tip:        "Протоколы с аппаратным ускорением ChaCha20 / AES-GCM создают в 3-4 раза меньше нагрузки на двухъядерные роутеры.",
		ExampleLog: "system: critical load average: 8.45, 6.12, 4.33 (2 cores)",
		Keywords:   []string{"critical load average", "перегрузка процессора", "load average", "процессор 100%"},
		Match: func(text string) bool {
			return matchAny(text, "critical load average", "high load average", "load average: 8.")
		},
	},
	{
		ID:       "system_usb_power_insufficient",
		Category: "Система и Entware",
		Code:     "USB_POWER_SURGE",
		Title:    "Просадка по питанию USB-порта роутера (USB Power Surge)",
		Summary:  "USB-накопитель внезапно отключается под нагрузкой, так как порт роутера не может выдать достаточный ток.",
		Cause:    "Подключение внешних жестких дисков 2.5\" HDD или нескольких устройств через пассивный USB-хаб без внешнего питания.",
		ActionSteps: []string{
			"Используйте активный USB-хаб с собственным отдельным блоком питания от розетки 220V.",
			"Замените механический жесткий диск на энергоэффективный твердотельный накопитель SSD или USB-флешку.",
			"Проверьте состояние комплектного блока питания роутера (со временем конденсаторы теряют емкость).",
			"Переподключите накопитель.",
		},
		Tip:        "USB-порт роутера обычно рассчитан максимум на 500-1000 мА, чего недостаточно для пикового старта механического HDD.",
		ExampleLog: "kernel: usb 1-1: over-current condition on port 1, power disabled",
		Keywords:   []string{"over-current condition", "power disabled", "просадка питания usb", "usb power"},
		Match: func(text string) bool {
			return matchAny(text, "over-current condition on port", "usb power disabled", "power surge on usb")
		},
	},
	{
		ID:       "entware_libc_version_mismatch",
		Category: "Система и Entware",
		Code:     "GLIBC_VERSION_NOT_FOUND",
		Title:    "Несовместимость системной библиотеки C (GLIBC / MUSL)",
		Summary:  "Сторонний бинарный файл не запускается, требуя более новую версию библиотеки libc.so или glibc.",
		Cause:    "Попытка запуска бинарного файла, скомпилированного для обычного дистрибутива Linux (Ubuntu/Debian) на встраиваемой системе Entware (musl/uClibc).",
		ActionSteps: []string{
			"Используйте бинарные файлы, специально скомпилированные со статической линковкой (CGO_ENABLED=0).",
			"Все официальные сборки AWG Manager, Mihomo и Sing-box скомпилированы статически и не зависят от версии libc роутера.",
			"Не копируйте бинарники напрямую из десктопных дистрибутивов Linux.",
			"Устанавливайте системные утилиты только через менеджер пакетов `opkg`.",
		},
		Tip:        "Статическая компиляция Go (`-tags netgo -ldflags '-s -w'`) гарантирует работу на любых версиях Linux.",
		ExampleLog: "/opt/bin/custom-app: /opt/lib/libc.so.1: version 'GLIBC_2.34' not found",
		Keywords:   []string{"version 'glibc_", "not found libc.so", "несовместимость libc", "glibc version"},
		Match: func(text string) bool {
			return matchAny(text, "version glibc_", "not found (required by", "libc.so: version")
		},
	},
	{
		ID:       "system_entropy_pool_exhausted",
		Category: "Система и Entware",
		Code:     "URANDOM_ENTROPY_LOW",
		Title:    "Низкий уровень системной энтропии (/dev/urandom)",
		Summary:  "Генерация криптографических ключей (WireGuard, SSL, SSH) зависает из-за нехватки случайных чисел в ядре роутера.",
		Cause:    "В роутере нет аппаратного генератора случайных чисел (TRNG), а отсутствие клавиатуры и мыши замедляет накопление энтропии.",
		ActionSteps: []string{
			"Установите в Entware демон накопления энтропии: `opkg update && opkg install haveged`.",
			"Запустите службу: `/opt/etc/init.d/S02haveged start`.",
			"Проверьте доступную энтропию командой: `cat /proc/sys/kernel/random/entropy_avail` (значение должно быть > 1000).",
			"Повторите генерацию ключей.",
		},
		Tip:        "Установка демона Haveged раз и навсегда устраняет любые задержки при старте криптографических служб.",
		ExampleLog: "kernel: random: crng init done (entropy pool was exhausted)",
		Keywords:   []string{"entropy pool was exhausted", "haveged", "генерация ключей зависла", "энтропия"},
		Match: func(text string) bool {
			return matchAny(text, "entropy pool was exhausted", "crng init done", "low entropy in system")
		},
	},
	{
		ID:       "system_proc_sys_sysctl_readonly",
		Category: "Система и Entware",
		Code:     "SYSCTL_WRITE_PERM_DENIED",
		Title:    "Отказано в доступе при изменении параметров ядра sysctl",
		Summary:  "Команда оптимизации сетевого стека `sysctl -w` завершилась ошибкой прав доступа.",
		Cause:    "Запуск команды из-под непривилегированного пользователя или работа внутри изолированного chroot-контейнера.",
		ActionSteps: []string{
			"Убедитесь, что команды настройки ядра выполняются под пользователем `root`.",
			"В AWG Manager системные оптимизации применяются от имени системного демона роутера с максимальными привилегиями.",
			"Проверьте права доступа к файловой системе `/proc/sys`.",
			"При необходимости перезагрузите роутер.",
		},
		Tip:        "Только суперпользователь root имеет право модифицировать сетевые параметры подсистемы ядра Linux.",
		ExampleLog: "sysctl: error setting key 'net.ipv4.ip_forward': Permission denied",
		Keywords:   []string{"sysctl permission denied", "error setting key sysctl", "права sysctl"},
		Match: func(text string) bool {
			return matchAny(text, "sysctl: error setting key", "permission denied (sysctl)", "cannot write to /proc/sys")
		},
	},
	{
		ID:       "entware_wget_ssl_support_missing",
		Category: "Система и Entware",
		Code:     "BUSYBOX_WGET_NO_SSL",
		Title:    "Штатный wget не поддерживает защищенный протокол HTTPS",
		Summary:  "Попытка скачать файл по ссылке `https://` через утилиту wget завершилась ошибкой несовместимости протокола.",
		Cause:    "В базовой прошивке установлен упрощенный wget из набора BusyBox без поддержки SSL шифрования.",
		ActionSteps: []string{
			"Установите полноценную утилиту wget с поддержкой SSL: `opkg update && opkg install wget-ssl`.",
			"Или используйте curl: `opkg install curl`.",
			"В AWG Manager все загрузки выполняются встроенным HTTP-клиентом с поддержкой TLS 1.3.",
			"Повторите попытку скачивания.",
		},
		Tip:        "Пакет `wget-ssl` заменяет системную заглушку и позволяет скачивать файлы с любых защищенных ресурсов.",
		ExampleLog: "wget: not an http or ftp url: https://site.com/file (SSL support not compiled in)",
		Keywords:   []string{"ssl support not compiled in", "not an http or ftp url", "wget-ssl", "wget https"},
		Match: func(text string) bool {
			return matchAny(text, "ssl support not compiled in", "not an http or ftp url", "wget: https not supported")
		},
	},
	{
		ID:       "system_cron_daemon_inactive",
		Category: "Система и Entware",
		Code:     "CRON_DAEMON_STOPPED",
		Title:    "Демон планировщика cron остановлен (Автообновления не работают)",
		Summary:  "Фоновые периодические задачи (обновление подписок, ротация логов, проверка туннелей) не запускаются по расписанию.",
		Cause:    "Служба планировщика cron отключена в каталоге `/opt/etc/init.d/` или файл расписания crontab содержит ошибки синтаксиса.",
		ActionSteps: []string{
			"Запустите службу планировщика в терминале: `/opt/etc/init.d/S10cron start`.",
			"Убедитесь, что скрипт имеет права на запуск: `chmod +x /opt/etc/init.d/S10cron`.",
			"Проверьте задания планировщика: `crontab -l`.",
			"В AWG Manager автоматические фоновые задачи выполняются встроенным диспетчером даже без внешнего cron.",
		},
		Tip:        "Встроенный планировщик AWG Manager работает независимо от системного cron роутера.",
		ExampleLog: "cron: can't lock /opt/var/run/cron.pid, otherpid may be 0: No such file or directory",
		Keywords:   []string{"cron stopped", "cron.pid", "планировщик cron", "crontab не работает"},
		Match: func(text string) bool {
			return matchAny(text, "cant lock /opt/var/run/cron.pid", "cron daemon not running", "crond: stopped")
		},
	},
	{
		ID:       "system_tmp_tmpfs_full",
		Category: "Система и Entware",
		Code:     "TMPFS_MEMORY_FULL",
		Title:    "Переполнение виртуального каталога /tmp (RAM-диск tmpfs)",
		Summary:  "Временный каталог `/tmp` заполнен на 100%. Программы не могут создавать сокеты блокировок и временные файлы.",
		Cause:    "Каталог `/tmp` размещается в оперативной памяти (tmpfs). Сюда по ошибке был скачан большой файл (дамп, лог или бэкап).",
		ActionSteps: []string{
			"Проверьте размер файлов в `/tmp`: `ls -lh /tmp | sort -k 5 -h`.",
			"Удалите тяжелые файлы из `/tmp`.",
			"Для скачивания файлов подписок и баз данных используйте пути на постоянном диске `/opt/tmp/`.",
			"При необходимости увеличьте размер tmpfs: `mount -o remount,size=128M /tmp`.",
		},
		Tip:        "Никогда не направляйте постоянную запись логов в `/tmp`, так как это расходует оперативную память роутера.",
		ExampleLog: "write to /tmp/download.bin: No space left on device (tmpfs full)",
		Keywords:   []string{"tmpfs full", "папка /tmp переполнена", "no space left on device tmp"},
		Match: func(text string) bool {
			return matchAny(text, "/tmp: no space left on device", "tmpfs full", "no space in /tmp")
		},
	},
	{
		ID:       "system_reboot_spontaneous_watchdog",
		Category: "Система и Entware",
		Code:     "HARDWARE_WATCHDOG_REBOOT",
		Title:    "Самопроизвольная перезагрузка роутера аппаратным таймером Watchdog",
		Summary:  "Роутер внезапно перезагрузился без предупреждения из-за длительного зависания одного из системных процессов.",
		Cause:    "Аппаратный сторожевой таймер процессора (Hardware Watchdog) сработал, так как демон NDM не успел сбросить счетчик за отведенное время.",
		ActionSteps: []string{
			"В журнале сообщений Keenetic найдите причину перезагрузки (причина отображается в первой строке после запуска).",
			"Проверьте, не совпадает ли момент перезагрузки со временем тяжелых задач (скачивание больших торрентов на высокой скорости).",
			"Убедитесь в стабильности блока питания роутера.",
			"Снизьте общую нагрузку на процессор, отключив неиспользуемые компоненты.",
		},
		Tip:        "При стабильном питании и отсутствии софтлоков современные роутеры Keenetic работают месяцами без единой перезагрузки.",
		ExampleLog: "syslog: system restarted by hardware watchdog reset",
		Keywords:   []string{"hardware watchdog", "watchdog reset", "самопроизвольная перезагрузка", "перезагрузка роутера"},
		Match: func(text string) bool {
			return matchAny(text, "hardware watchdog reset", "restarted by watchdog", "watchdog triggered reboot")
		},
	},
	{
		ID:       "entware_symlink_broken",
		Category: "Система и Entware",
		Code:     "BROKEN_SYMLINK_BIN",
		Title:    "Битые символические ссылки в каталогах /opt/bin или /opt/lib",
		Summary:  "Команда не найдена (No such file or directory) при попытке запуска утилиты, ссылка на которую указывает на удаленный файл.",
		Cause:    "Некорректное удаление пакета, сбой распаковки архива или перенос каталога `/opt` на другой носитель.",
		ActionSteps: []string{
			"Найдите битые ссылки в терминале: `find /opt/bin -xtype l`.",
			"Удалите поврежденные ссылки и переустановите пакет через `opkg install --force-reinstall <пакет>`.",
			"Проверьте переменную окружения PATH: в ней обязательно должен присутствовать `/opt/bin:/opt/sbin`.",
			"Восстановите целостность среды Entware.",
		},
		Tip:        "Для проверки всех установленных пакетов Entware выполните скрипт проверки целостности opkg.",
		ExampleLog: "/opt/bin/awg: line 1: /opt/bin/awg: No such file or directory (broken symlink)",
		Keywords:   []string{"broken symlink", "битая ссылка", "no such file or directory symlink", "find -xtype l"},
		Match: func(text string) bool {
			return matchAny(text, "broken symlink", "no such file or directory (broken", "invalid symlink in /opt")
		},
	},

	// =========================================================================
	// 8. ПОЛНЫЙ КАТАЛОГ ОШИБОК ЯДРА LINUX (POSIX ERRNO 1-133)
	// =========================================================================
	{
		ID:       "errno_eperm_1",
		Category: "Ядро Linux",
		Code:     "EPERM",
		Title:    "Операция запрещена системой безопасности или правами ядра",
		Summary:  "Операция запрещена системой безопасности или правами ядра",
		Cause:    "Процесс пытается изменить сетевые правила (iptables/ip rule) или сокет без необходимых прав администратора root (CAP_NET_ADMIN / CAP_NET_RAW).",
		ActionSteps: []string{
			"Проверьте права запуска службы: она должна запускаться от пользователя root.",
			"В веб-интерфейсе перейдите в «Инструменты» -> «Службы».",
			"Найдите нужный процесс и нажмите кнопку перезапуска.",
		},
		Tip:        "В Entware и KeeneticOS системные бинарники маршрутизации обязаны иметь права superuser (UID 0).",
		ExampleLog: "iptables: Permission denied (you must be root)",
		Keywords:   []string{"eperm", "1", "operation not permitted"},
		Match: func(text string) bool {
			return matchAny(text, "EPERM", "errno 1", "error 1", "operation not permitted")
		},
	},
	{
		ID:       "errno_enoent_2",
		Category: "Ядро Linux",
		Code:     "ENOENT",
		Title:    "Файл, исполняемый бинарник или каталог не найден",
		Summary:  "Файл, исполняемый бинарник или каталог не найден",
		Cause:    "Система пытается обратиться к файлу конфигурации, сокету или бинарнику, которого нет по указанному пути.",
		ActionSteps: []string{
			"Перейдите во вкладку «Файлы» в разделе «Система».",
			"Проверьте существование указанного пути (например, /opt/etc/amnezia/ или /opt/bin/).",
			"Если файл отсутствует, переустановите пакет через «Пакеты opkg».",
		},
		Tip:        "Убедитесь, что накопитель /opt примонтирован и доступен.",
		ExampleLog: "/opt/bin/mihomo: No such file or directory",
		Keywords:   []string{"enoent", "2", "no such file or directory"},
		Match: func(text string) bool {
			return matchAny(text, "ENOENT", "errno 2", "error 2", "no such file or directory")
		},
	},
	{
		ID:       "errno_esrch_3",
		Category: "Ядро Linux",
		Code:     "ESRCH",
		Title:    "Указанный процесс не существует",
		Summary:  "Указанный процесс не существует",
		Cause:    "Попытка отправить сигнал (kill, reload) или опросить статус процесса по PID, который уже завершился или упал.",
		ActionSteps: []string{
			"Перейдите во вкладку «Процессы» в меню «Система».",
			"Проверьте, запущен ли целевой процесс.",
			"Если процесс упал, проверьте системный журнал в разделе «Журнал» для выяснения причины падения.",
		},
		Tip:        "Падение процесса часто связано с OOM (нехваткой памяти) или ошибкой синтаксиса конфига.",
		ExampleLog: "kill: (28194) - No such process",
		Keywords:   []string{"esrch", "3", "no such process"},
		Match: func(text string) bool {
			return matchAny(text, "ESRCH", "errno 3", "error 3", "no such process")
		},
	},
	{
		ID:       "errno_eintr_4",
		Category: "Ядро Linux",
		Code:     "EINTR",
		Title:    "Системный вызов был прерван сигналом",
		Summary:  "Системный вызов был прерван сигналом",
		Cause:    "Блокирующий системный вызов ядра (например, ожидание сетевого пакета или I/O) был прерван поступившим сигналом (SIGCHLD, SIGALRM).",
		ActionSteps: []string{
			"Обычно это временное событие, сетевой демон автоматически повторяет попытку соединения.",
			"Если ошибка повторяется циклически, перезапустите соответствующую службу в «Службы».",
		},
		Tip:        "В стабильных демонах предусмотрен автоматический перезапуск прерванного системного вызова (SA_RESTART).",
		ExampleLog: "epoll_wait: Interrupted system call",
		Keywords:   []string{"eintr", "4", "interrupted system call"},
		Match: func(text string) bool {
			return matchAny(text, "EINTR", "errno 4", "error 4", "interrupted system call")
		},
	},
	{
		ID:       "errno_eio_5",
		Category: "Ядро Linux",
		Code:     "EIO",
		Title:    "Аппаратная ошибка ввода-вывода накопителя или устройства",
		Summary:  "Аппаратная ошибка ввода-вывода накопителя или устройства",
		Cause:    "Сбой чтения или записи данных на USB-накопитель, сбой флеш-памяти роутера или повреждение файловой системы.",
		ActionSteps: []string{
			"Перейдите в веб-интерфейс Keenetic -> «Управление» -> «Диагностика».",
			"Проверьте журнал роутера на наличие записей 'EXT4-fs error' или 'I/O error'.",
			"Безопасно извлеките USB-накопитель и проверьте его на ПК утилитой chkdsk / fsck.",
		},
		Tip:        "Никогда не выдергивайте USB-флешку из роутера под нагрузкой без безопасного отключения.",
		ExampleLog: "EXT4-fs error (device sda1): ext4_lookup: deleted inode referenced: I/O error",
		Keywords:   []string{"eio", "5", "input/output error"},
		Match: func(text string) bool {
			return matchAny(text, "EIO", "errno 5", "error 5", "input/output error")
		},
	},
	{
		ID:       "errno_enxio_6",
		Category: "Ядро Linux",
		Code:     "ENXIO",
		Title:    "Устройство или адрес не найдены",
		Summary:  "Устройство или адрес не найдены",
		Cause:    "Обращение к специальному файлу устройства (например, /dev/net/tun), для которого в ядре отсутствует драйвер или аппаратный модуль.",
		ActionSteps: []string{
			"Проверьте наличие модуля ядра TUN: в терминале выполните 'ls -l /dev/net/tun'.",
			"Если устройство отсутствует, перезагрузите роутер через «Управление» -> «Перезагрузка».",
		},
		Tip:        "Виртуальные сетевые устройства TUN/TAP создаются ядром при загрузке сетевых служб.",
		ExampleLog: "/dev/net/tun: No such device or address",
		Keywords:   []string{"enxio", "6", "no such device or address"},
		Match: func(text string) bool {
			return matchAny(text, "ENXIO", "errno 6", "error 6", "no such device or address")
		},
	},
	{
		ID:       "errno_e2big_7",
		Category: "Ядро Linux",
		Code:     "E2BIG",
		Title:    "Слишком длинный список аргументов команды",
		Summary:  "Слишком длинный список аргументов команды",
		Cause:    "Попытка передать через CLI или скрипт список аргументов, превышающий системный лимит ядра ARG_MAX.",
		ActionSteps: []string{
			"Если передаётся длинный список IP-адресов или доменов, разбейте их на группы или используйте файловые списки (ipset / rule-set).",
			"В AWG Manager используйте списки подсетей или внешние наборы правил SRS/MRS.",
		},
		Tip:        "Для передачи сотен тысяч адресов используйте бинарные SRS-наборы правил или ipset hash:net.",
		ExampleLog: "execve: Argument list too long",
		Keywords:   []string{"e2big", "7", "argument list too long"},
		Match: func(text string) bool {
			return matchAny(text, "E2BIG", "errno 7", "error 7", "argument list too long")
		},
	},
	{
		ID:       "errno_enoexec_8",
		Category: "Ядро Linux",
		Code:     "ENOEXEC",
		Title:    "Неверный формат исполняемого файла (не та архитектура CPU)",
		Summary:  "Неверный формат исполняемого файла (не та архитектура CPU)",
		Cause:    "Попытка запустить бинарник, скомпилированный под другую процессорную архитектуру (например, x86_64 вместо MIPS или ARM64 роутера).",
		ActionSteps: []string{
			"Узнайте точную архитектуру процессора роутера: выполните 'uname -m' в терминале.",
			"Скачайте бинарник под правильную архитектуру (aarch64, mips, mipsel, armv7).",
			"В AWG Manager используйте встроенную функцию обновления компонентов.",
		},
		Tip:        "Для роутеров Keenetic на базе MediaTek MT7622/MT7981 используется aarch64 (ARM64), для MT7621 — mipsel.",
		ExampleLog: "fork/exec /opt/bin/sing-box: exec format error",
		Keywords:   []string{"enoexec", "8", "exec format error"},
		Match: func(text string) bool {
			return matchAny(text, "ENOEXEC", "errno 8", "error 8", "exec format error")
		},
	},
	{
		ID:       "errno_ebadf_9",
		Category: "Ядро Linux",
		Code:     "EBADF",
		Title:    "Некорректный файловый дескриптор",
		Summary:  "Некорректный файловый дескриптор",
		Cause:    "Попытка чтения или записи в закрытый или несуществующий файловый дескриптор сокета.",
		ActionSteps: []string{
			"Перезапустите проблемный туннель или службу через интерфейс AWG Manager.",
			"Если ошибка повторяется, обновите бинарник службы.",
		},
		Tip:        "Обычно указывает на программную гонку при асинхронном закрытии сокета.",
		ExampleLog: "write: bad file descriptor",
		Keywords:   []string{"ebadf", "9", "bad file descriptor"},
		Match: func(text string) bool {
			return matchAny(text, "EBADF", "errno 9", "error 9", "bad file descriptor")
		},
	},
	{
		ID:       "errno_echild_10",
		Category: "Ядро Linux",
		Code:     "ECHILD",
		Title:    "Нет дочерних процессов",
		Summary:  "Нет дочерних процессов",
		Cause:    "Процесс ожидал завершения дочернего процесса (waitpid), но дочерний процесс уже завершился и был очищен.",
		ActionSteps: []string{
			"Служба обычно обрабатывает это событие штатно.",
			"Если служба зависла, выполните её перезапуск в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартное поведение системных диспетчеров процессов.",
		ExampleLog: "waitpid: No child processes",
		Keywords:   []string{"echild", "10", "no child processes"},
		Match: func(text string) bool {
			return matchAny(text, "ECHILD", "errno 10", "error 10", "no child processes")
		},
	},
	{
		ID:       "errno_eagain_11",
		Category: "Ядро Linux",
		Code:     "EAGAIN",
		Title:    "Ресурс временно недоступен (повторите попытку)",
		Summary:  "Ресурс временно недоступен (повторите попытку)",
		Cause:    "Неблокирующий сокет не может немедленно передать данные или достигнут лимит потоков/сокетов.",
		ActionSteps: []string{
			"Подождите несколько секунд: сетевой стек автоматически повторит передачу.",
			"Если ошибка возникает под высокой нагрузкой торрентов, уменьшите количество соединений в торрент-клиенте.",
		},
		Tip:        "В неблокирующем I/O код EAGAIN (EWOULDBLOCK) является штатным сигналом ожидания готовности сокета.",
		ExampleLog: "resource temporarily unavailable: EAGAIN",
		Keywords:   []string{"eagain", "11", "resource temporarily unavailable"},
		Match: func(text string) bool {
			return matchAny(text, "EAGAIN", "errno 11", "error 11", "resource temporarily unavailable")
		},
	},
	{
		ID:       "errno_enomem_12",
		Category: "Ядро Linux",
		Code:     "ENOMEM",
		Title:    "Недостаточно оперативной памяти для выделения ресурса",
		Summary:  "Недостаточно оперативной памяти для выделения ресурса",
		Cause:    "Ядро роутера исчерпало свободную оперативную RAM-память и не может выделить буфер для сокета или процесса.",
		ActionSteps: []string{
			"Перейдите в «Инструменты» -> «Процессы» и проверьте потребление памяти.",
			"Остановите неиспользуемые ресурсоёмкие сервисы.",
			"Подключите файл подкачки (SWAP) на USB-накопителе.",
		},
		Tip:        "На роутерах со 128-256 МБ RAM обязательно создание SWAP-файла размером 512 МБ.",
		ExampleLog: "fork: cannot allocate memory",
		Keywords:   []string{"enomem", "12", "cannot allocate memory"},
		Match: func(text string) bool {
			return matchAny(text, "ENOMEM", "errno 12", "error 12", "cannot allocate memory")
		},
	},
	{
		ID:       "errno_eacces_13",
		Category: "Ядро Linux",
		Code:     "EACCES",
		Title:    "Отказано в доступе к файлу или порту",
		Summary:  "Отказано в доступе к файлу или порту",
		Cause:    "Недостаточно прав файловой системы (chmod/chown) или попытка открыть защищенный порт ниже 1024 без прав root.",
		ActionSteps: []string{
			"В терминале проверьте права на файл командой 'ls -la <путь>'.",
			"Установите права на исполнение: 'chmod +x <файл>'.",
			"Убедитесь, что служба запущена от пользователя root.",
		},
		Tip:        "В Entware файлы в /opt/etc/ и /opt/bin/ должны иметь владельца root:root.",
		ExampleLog: "open /opt/etc/amnezia/awg0.conf: Permission denied",
		Keywords:   []string{"eacces", "13", "permission denied"},
		Match: func(text string) bool {
			return matchAny(text, "EACCES", "errno 13", "error 13", "permission denied")
		},
	},
	{
		ID:       "errno_efault_14",
		Category: "Ядро Linux",
		Code:     "EFAULT",
		Title:    "Некорректный адрес памяти",
		Summary:  "Некорректный адрес памяти",
		Cause:    "Системный вызов получил указатель на недопустимую область адресного пространства памяти.",
		ActionSteps: []string{
			"Указывает на сбой в коде бинарника или несовместимость версий ядра.",
			"Обновите версию AWG Manager или ядра KeeneticOS.",
		},
		Tip:        "Регулярно проверяйте обновления в «Параметры» -> «Обновление ПО».",
		ExampleLog: "sys_call: Bad address",
		Keywords:   []string{"efault", "14", "bad address"},
		Match: func(text string) bool {
			return matchAny(text, "EFAULT", "errno 14", "error 14", "bad address")
		},
	},
	{
		ID:       "errno_enotblk_15",
		Category: "Ядро Linux",
		Code:     "ENOTBLK",
		Title:    "Требуется блочное устройство",
		Summary:  "Требуется блочное устройство",
		Cause:    "Попытка смонтировать файл как диск без использования петлевого устройства loopback.",
		ActionSteps: []string{
			"При монтировании файлов образов используйте флаг '-o loop'.",
			"Для обычных накопителей используйте пути /dev/sda1, /dev/sdb1.",
		},
		Tip:        "Блочные устройства накопителей располагаются в каталоге /dev/sd*.",
		ExampleLog: "mount: /dev/sda: Block device required",
		Keywords:   []string{"enotblk", "15", "block device required"},
		Match: func(text string) bool {
			return matchAny(text, "ENOTBLK", "errno 15", "error 15", "block device required")
		},
	},
	{
		ID:       "errno_ebusy_16",
		Category: "Ядро Linux",
		Code:     "EBUSY",
		Title:    "Устройство или ресурс заняты",
		Summary:  "Устройство или ресурс заняты",
		Cause:    "Попытка размонтировать занятую файловую систему или переконфигурировать активный сетевой интерфейс.",
		ActionSteps: []string{
			"Остановите службы, использующие данный ресурс, перед его размонтированием или удалением.",
			"В интерфейсе AWG Manager выключите туннель перед его перенастройкой.",
		},
		Tip:        "Команда 'lsof' или 'fuser -m /opt' покажет, какие процессы удерживают ресурс.",
		ExampleLog: "umount: /opt: Device or resource busy",
		Keywords:   []string{"ebusy", "16", "device or resource busy"},
		Match: func(text string) bool {
			return matchAny(text, "EBUSY", "errno 16", "error 16", "device or resource busy")
		},
	},
	{
		ID:       "errno_eexist_17",
		Category: "Ядро Linux",
		Code:     "EEXIST",
		Title:    "Файл, маршрут или правило уже существуют",
		Summary:  "Файл, маршрут или правило уже существуют",
		Cause:    "Попытка добавить дублирующий маршрут в таблицу ядра (RTNETLINK answers: File exists) или создать уже существующий файл.",
		ActionSteps: []string{
			"Проверьте список маршрутов в ядре командой 'ip route show'.",
			"В интерфейсе AWG Manager убедитесь, что одинаковые подсети не назначены разным туннелям.",
			"Удалите конфликтующий старый маршрут перед добавлением нового.",
		},
		Tip:        "Ядро Linux запрещает наличие двух абсолютно идентичных маршрутов в одной таблице с одинаковой метрикой.",
		ExampleLog: "RTNETLINK answers: File exists",
		Keywords:   []string{"eexist", "17", "file exists"},
		Match: func(text string) bool {
			return matchAny(text, "EEXIST", "errno 17", "error 17", "file exists")
		},
	},
	{
		ID:       "errno_exdev_18",
		Category: "Ядро Linux",
		Code:     "EXDEV",
		Title:    "Попытка создать жесткую ссылку между разными файловыми системами",
		Summary:  "Попытка создать жесткую ссылку между разными файловыми системами",
		Cause:    "Попытка перемещения файла (rename/link) между разными точками монтирования (например, из /tmp в /opt).",
		ActionSteps: []string{
			"Используйте копирование с последующим удалением ('cp' + 'rm') вместо прямой ссылки или перемещения ('mv').",
			"В AWG Manager менеджер файлов автоматически использует потоковое копирование между томами.",
		},
		Tip:        "Жесткие ссылки (hard links) могут указывать только на inodes в пределах одного раздела диска.",
		ExampleLog: "link: Invalid cross-device link",
		Keywords:   []string{"exdev", "18", "invalid cross-device link"},
		Match: func(text string) bool {
			return matchAny(text, "EXDEV", "errno 18", "error 18", "invalid cross-device link")
		},
	},
	{
		ID:       "errno_enodev_19",
		Category: "Ядро Linux",
		Code:     "ENODEV",
		Title:    "Сетевой интерфейс или устройство не найдено",
		Summary:  "Сетевой интерфейс или устройство не найдено",
		Cause:    "Попытка привязать сокет или маршрут к сетевому интерфейсу, который не зарегистрирован в ядре.",
		ActionSteps: []string{
			"Перейдите на вкладку «Туннели» и убедитесь, что туннель включен и создан.",
			"Проверьте список интерфейсов командой 'ip link show' в Терминале.",
			"Если интерфейс пропал, перезапустите туннель.",
		},
		Tip:        "Интерфейсы WireGuard/Amnezia создаются динамически при старте туннеля.",
		ExampleLog: "Cannot find device \"awg0\": No such device",
		Keywords:   []string{"enodev", "19", "no such device"},
		Match: func(text string) bool {
			return matchAny(text, "ENODEV", "errno 19", "error 19", "no such device")
		},
	},
	{
		ID:       "errno_enotdir_20",
		Category: "Ядро Linux",
		Code:     "ENOTDIR",
		Title:    "Указанный путь не является каталогом",
		Summary:  "Указанный путь не является каталогом",
		Cause:    "В пути к файлу один из промежуточных элементов является обычным файлом, а не каталогом.",
		ActionSteps: []string{
			"Проверьте структуру каталогов в «Инструменты» -> «Файлы».",
			"Убедитесь, что по указанному пути не создан обычный файл с именем каталога.",
		},
		Tip:        "Типичная ошибка при случайном сохранении конфига поверх каталога конфигураций.",
		ExampleLog: "/opt/etc/amnezia/config: Not a directory",
		Keywords:   []string{"enotdir", "20", "not a directory"},
		Match: func(text string) bool {
			return matchAny(text, "ENOTDIR", "errno 20", "error 20", "not a directory")
		},
	},
	{
		ID:       "errno_eisdir_21",
		Category: "Ядро Linux",
		Code:     "EISDIR",
		Title:    "Указанный путь является каталогом, а не файлом",
		Summary:  "Указанный путь является каталогом, а не файлом",
		Cause:    "Попытка открыть каталог на чтение как обычный файл.",
		ActionSteps: []string{
			"Укажите точное имя файла внутри каталога (например, /opt/etc/config.yaml, а не /opt/etc/).",
			"В настройках проверьте правильность указанного пути к конфигурационному файлу.",
		},
		Tip:        "Для чтения каталогов в Linux используется opendir/readdir, а не обычный open/read.",
		ExampleLog: "read /opt/etc/mihomo: Is a directory",
		Keywords:   []string{"eisdir", "21", "is a directory"},
		Match: func(text string) bool {
			return matchAny(text, "EISDIR", "errno 21", "error 21", "is a directory")
		},
	},
	{
		ID:       "errno_einval_22",
		Category: "Ядро Linux",
		Code:     "EINVAL",
		Title:    "Недопустимый параметр или некорректный аргумент",
		Summary:  "Недопустимый параметр или некорректный аргумент",
		Cause:    "Передан недопустимый параметр в системный вызов ядра, некорректный MTU, неверный флаг сокета или ошибка синтаксиса IP-адреса.",
		ActionSteps: []string{
			"Проверьте значения параметров туннеля: MTU (от 1280 до 1500), маску подсети (например, /24 или /32).",
			"В интерфейсе AWG Manager нажмите карандаш у туннеля и проверьте адрес интерфейса.",
		},
		Tip:        "Слишком маленький MTU (< 1280 для IPv6) или неверная маска вызывают ошибку EINVAL.",
		ExampleLog: "ip link set dev awg0 mtu 9000: Invalid argument",
		Keywords:   []string{"einval", "22", "invalid argument"},
		Match: func(text string) bool {
			return matchAny(text, "EINVAL", "errno 22", "error 22", "invalid argument")
		},
	},
	{
		ID:       "errno_enfile_23",
		Category: "Ядро Linux",
		Code:     "ENFILE",
		Title:    "Переполнение общесистемной таблицы файлов ядра",
		Summary:  "Переполнение общесистемной таблицы файлов ядра",
		Cause:    "В ядре исчерпан глобальный лимит открытых файловых дескрипторов (fs.file-max).",
		ActionSteps: []string{
			"В терминале проверьте лимит: 'sysctl fs.file-max'.",
			"Проверьте количество открытых дескрипторов: 'cat /proc/sys/fs/file-nr'.",
			"Перезапустите службу, создающую утечку дескрипторов.",
		},
		Tip:        "Для роутеров с активным прокси рекомендуется установить 'sysctl -w fs.file-max=65535'.",
		ExampleLog: "open: File table overflow",
		Keywords:   []string{"enfile", "23", "file table overflow"},
		Match: func(text string) bool {
			return matchAny(text, "ENFILE", "errno 23", "error 23", "file table overflow")
		},
	},
	{
		ID:       "errno_emfile_24",
		Category: "Ядро Linux",
		Code:     "EMFILE",
		Title:    "Превышен лимит открытых файлов/сокетов для процесса",
		Summary:  "Превышен лимит открытых файлов/сокетов для процесса",
		Cause:    "Процесс достиг своего персонального лимита ulimit -n на количество открытых сетевых сокетов.",
		ActionSteps: []string{
			"Увеличьте лимит открытых файлов в стартовом скрипте службы: 'ulimit -n 16384'.",
			"В настройках Sing-box или Mihomo уменьшите количество одновременных соединений.",
		},
		Tip:        "Каждое сетевое TCP/UDP соединение в Linux занимает один файловый дескриптор.",
		ExampleLog: "accept4: too many open files",
		Keywords:   []string{"emfile", "24", "too many open files"},
		Match: func(text string) bool {
			return matchAny(text, "EMFILE", "errno 24", "error 24", "too many open files")
		},
	},
	{
		ID:       "errno_enotty_25",
		Category: "Ядро Linux",
		Code:     "ENOTTY",
		Title:    "Неподдерживаемая операция ioctl для устройства",
		Summary:  "Неподдерживаемая операция ioctl для устройства",
		Cause:    "Попытка выполнить терминальную операцию управления на сокете или обычном файле.",
		ActionSteps: []string{
			"Штатно возникает, когда скрипт запускается в фоновом режиме без TTY-терминала.",
			"Если служба работает нормально, эту запись можно игнорировать.",
		},
		Tip:        "Демоны в Linux работают без управляющего терминала (setsid).",
		ExampleLog: "ioctl: Inappropriate ioctl for device",
		Keywords:   []string{"enotty", "25", "inappropriate ioctl for device"},
		Match: func(text string) bool {
			return matchAny(text, "ENOTTY", "errno 25", "error 25", "inappropriate ioctl for device")
		},
	},
	{
		ID:       "errno_etxtbsy_26",
		Category: "Ядро Linux",
		Code:     "ETXTBSY",
		Title:    "Исполняемый файл занят работающим процессом",
		Summary:  "Исполняемый файл занят работающим процессом",
		Cause:    "Попытка перезаписать бинарник, который в данный момент запущен и исполняется ядром.",
		ActionSteps: []string{
			"Остановите службу перед обновлением бинарника: «Инструменты» -> «Службы» -> «Остановить».",
			"После остановки выполните замену или обновление файла.",
			"Запустите службу снова.",
		},
		Tip:        "Ядро Linux блокирует запись в бинарник, находящийся в памяти (vma exec).",
		ExampleLog: "/opt/bin/awg-manager: Text file busy",
		Keywords:   []string{"etxtbsy", "26", "text file busy"},
		Match: func(text string) bool {
			return matchAny(text, "ETXTBSY", "errno 26", "error 26", "text file busy")
		},
	},
	{
		ID:       "errno_efbig_27",
		Category: "Ядро Linux",
		Code:     "EFBIG",
		Title:    "Размер файла превышает лимит файловой системы",
		Summary:  "Размер файла превышает лимит файловой системы",
		Cause:    "Размер лога или базы данных превысил максимально допустимый размер для файловой системы (например, 2 ГБ или 4 ГБ для FAT32).",
		ActionSteps: []string{
			"Очистите переполненный лог-файл в «Инструменты» -> «Файлы».",
			"Включите ротацию журналов (logrotate) для предотвращения разрастания логов.",
		},
		Tip:        "Храните системные разделы Entware на файловой системе Ext4, а не FAT32/NTFS.",
		ExampleLog: "write: File too large",
		Keywords:   []string{"efbig", "27", "file too large"},
		Match: func(text string) bool {
			return matchAny(text, "EFBIG", "errno 27", "error 27", "file too large")
		},
	},
	{
		ID:       "errno_enospc_28",
		Category: "Ядро Linux",
		Code:     "ENOSPC",
		Title:    "Закончилось свободное место на накопителе",
		Summary:  "Закончилось свободное место на накопителе",
		Cause:    "Раздел накопителя /opt или память /tmp переполнены на 100%, запись новых данных невозможна.",
		ActionSteps: []string{
			"Перейдите во вкладку «Система» -> «Файлы».",
			"Удалите старые логи в /opt/var/log/ и ненужные архивы пакетов в /opt/var/cache/opkg/.",
			"Проверьте свободное место командой 'df -h /opt'.",
		},
		Tip:        "Если свободные мегабайты есть, но ошибка ENOSPC остаётся — закончились inodes ('df -i /opt').",
		ExampleLog: "/opt/etc/amnezia: No space left on device",
		Keywords:   []string{"enospc", "28", "no space left on device"},
		Match: func(text string) bool {
			return matchAny(text, "ENOSPC", "errno 28", "error 28", "no space left on device")
		},
	},
	{
		ID:       "errno_espipe_29",
		Category: "Ядро Linux",
		Code:     "ESPIPE",
		Title:    "Недопустимая операция позиционирования в сокете или пайпе",
		Summary:  "Недопустимая операция позиционирования в сокете или пайпе",
		Cause:    "Попытка выполнить fseek в сокете, FIFO или канале, где перемещение указателя невозможно.",
		ActionSteps: []string{
			"Штатное системное предупреждение при попытке чтения сокета как файла.",
		},
		Tip:        "Сетевые сокеты поддерживают только последовательное чтение и запись потока.",
		ExampleLog: "lseek: Illegal seek",
		Keywords:   []string{"espipe", "29", "illegal seek"},
		Match: func(text string) bool {
			return matchAny(text, "ESPIPE", "errno 29", "error 29", "illegal seek")
		},
	},
	{
		ID:       "errno_erofs_30",
		Category: "Ядро Linux",
		Code:     "EROFS",
		Title:    "Файловая система смонтирована только для чтения",
		Summary:  "Файловая система смонтирована только для чтения",
		Cause:    "USB-накопитель заблокирован ядром в режиме защиты (Read-Only) из-за повреждения файловой системы после внезапного отключения питания.",
		ActionSteps: []string{
			"Перейдите в интерфейс Keenetic -> «Управление» -> «Диагностика».",
			"Перезагрузите роутер через веб-панель для автоматической проверки диска fsck.",
			"Если диск не вернулся в чтение/запись, проверьте его на ПК утилитой chkdsk / fsck.",
		},
		Tip:        "Чтобы защитить USB-диск от перевода в Read-Only, используйте качественный блок питания и безопасное извлечение.",
		ExampleLog: "/opt/bin: Read-only file system",
		Keywords:   []string{"erofs", "30", "read-only file system"},
		Match: func(text string) bool {
			return matchAny(text, "EROFS", "errno 30", "error 30", "read-only file system")
		},
	},
	{
		ID:       "errno_emlink_31",
		Category: "Ядро Linux",
		Code:     "EMLINK",
		Title:    "Превышен лимит ссылок на каталог",
		Summary:  "Превышен лимит ссылок на каталог",
		Cause:    "Каталог содержит слишком много подкаталогов (лимит Ext4 обычно 64 000).",
		ActionSteps: []string{
			"Очистите переполненный каталог от устаревших подпапок.",
		},
		Tip:        "Используйте вложенную структуру каталогов при сохранении сотен тысяч файлов.",
		ExampleLog: "mkdir: Too many links",
		Keywords:   []string{"emlink", "31", "too many links"},
		Match: func(text string) bool {
			return matchAny(text, "EMLINK", "errno 31", "error 31", "too many links")
		},
	},
	{
		ID:       "errno_epipe_32",
		Category: "Ядро Linux",
		Code:     "EPIPE",
		Title:    "Обрыв сетевого канала связи",
		Summary:  "Обрыв сетевого канала связи",
		Cause:    "Процесс попытался отправить данные в сокет или пайп, удалённый конец которого уже был закрыт собеседником.",
		ActionSteps: []string{
			"Обычно свидетельствует о разрыве интернет-соединения или сбросе сессии удалённым сервером.",
			"Проверьте стабильность интернет-подключения и статус туннелей в AWG Manager.",
		},
		Tip:        "При получении SIGPIPE сетевые демоны закрывают соединение и инициируют переподключение.",
		ExampleLog: "write: broken pipe",
		Keywords:   []string{"epipe", "32", "broken pipe"},
		Match: func(text string) bool {
			return matchAny(text, "EPIPE", "errno 32", "error 32", "broken pipe")
		},
	},
	{
		ID:       "errno_edom_33",
		Category: "Ядро Linux",
		Code:     "EDOM",
		Title:    "Математический аргумент вне допустимой области определения",
		Summary:  "Математический аргумент вне допустимой области определения",
		Cause:    "Передано недопустимое числовое значение в математическую функцию ядра или библиотеки.",
		ActionSteps: []string{
			"Проверьте числовые параметры в конфигурации (порты, таймауты, лимиты).",
		},
		Tip:        "Связан с некорректным значением в низкоуровневой математической операции.",
		ExampleLog: "math: domain error",
		Keywords:   []string{"edom", "33", "numerical argument out of domain"},
		Match: func(text string) bool {
			return matchAny(text, "EDOM", "errno 33", "error 33", "numerical argument out of domain")
		},
	},
	{
		ID:       "errno_erange_34",
		Category: "Ядро Linux",
		Code:     "ERANGE",
		Title:    "Результат вычислений выходит за пределы диапазона",
		Summary:  "Результат вычислений выходит за пределы диапазона",
		Cause:    "Переполнение числового диапазона типа данных при вычислении системных метрик или таймеров.",
		ActionSteps: []string{
			"Проверьте корректность числовых диапазонов в настройках туннелей (MTU, Keepalive, порты).",
		},
		Tip:        "Keepalive должен быть от 0 до 65535, MTU от 576 до 65535.",
		ExampleLog: "result out of range",
		Keywords:   []string{"erange", "34", "numerical result out of range"},
		Match: func(text string) bool {
			return matchAny(text, "ERANGE", "errno 34", "error 34", "numerical result out of range")
		},
	},
	{
		ID:       "errno_edeadlk_35",
		Category: "Ядро Linux",
		Code:     "EDEADLK",
		Title:    "Предотвращена взаимная блокировка ресурсов (Deadlock)",
		Summary:  "Предотвращена взаимная блокировка ресурсов (Deadlock)",
		Cause:    "Два процесса или потока пытались заблокировать одни и те же мьютексы в противоположном порядке.",
		ActionSteps: []string{
			"Ядро Linux успешно предотвратило зависание, сбросив вызов.",
			"Если процесс завис, перезапустите его в «Инструменты» -> «Службы».",
		},
		Tip:        "Deadlock указывает на программную гонку в многопоточном приложении.",
		ExampleLog: "lock: Resource deadlock avoided",
		Keywords:   []string{"edeadlk", "35", "resource deadlock avoided"},
		Match: func(text string) bool {
			return matchAny(text, "EDEADLK", "errno 35", "error 35", "resource deadlock avoided")
		},
	},
	{
		ID:       "errno_enametoolong_36",
		Category: "Ядро Linux",
		Code:     "ENAMETOOLONG",
		Title:    "Слишком длинное имя файла или пути",
		Summary:  "Слишком длинное имя файла или пути",
		Cause:    "Имя файла превышает 255 символов или полный путь превышает лимит PATH_MAX (4096 символов).",
		ActionSteps: []string{
			"Сократите имя конфигурационного файла или название туннеля.",
			"В AWG Manager используйте краткие и понятные имена туннелей (например, 'de-vps', 'wg-nl').",
		},
		Tip:        "Максимальная длина одного имени файла в Ext4 составляет 255 байт.",
		ExampleLog: "open: File name too long",
		Keywords:   []string{"enametoolong", "36", "file name too long"},
		Match: func(text string) bool {
			return matchAny(text, "ENAMETOOLONG", "errno 36", "error 36", "file name too long")
		},
	},
	{
		ID:       "errno_enolck_37",
		Category: "Ядро Linux",
		Code:     "ENOLCK",
		Title:    "Нет доступных файловых блокировок в ядре",
		Summary:  "Нет доступных файловых блокировок в ядре",
		Cause:    "Исчерпана системная таблица файловых блокировок ядра (flock/fcntl).",
		ActionSteps: []string{
			"Перезапустите процессы, интенсивно блокирующие файлы (базы данных, логгеры).",
			"При необходимости перезагрузите роутер.",
		},
		Tip:        "Обычно возникает при сетевом монтировании NFS или переполнении lockd.",
		ExampleLog: "fcntl: No locks available",
		Keywords:   []string{"enolck", "37", "no locks available"},
		Match: func(text string) bool {
			return matchAny(text, "ENOLCK", "errno 37", "error 37", "no locks available")
		},
	},
	{
		ID:       "errno_enosys_38",
		Category: "Ядро Linux",
		Code:     "ENOSYS",
		Title:    "Системный вызов не реализован в ядре роутера",
		Summary:  "Системный вызов не реализован в ядре роутера",
		Cause:    "Программа пытается вызвать системный вызов ядра Linux, который отсутствует в версии ядра вашего роутера (например, слишком старое ядро Linux 4.9/3.10).",
		ActionSteps: []string{
			"Убедитесь, что используете сборку софта, совместимую с версией ядра вашего Keenetic.",
			"В Sing-box/Mihomo выберите режим Tun Stack: 'gvisor' или 'system' вместо устаревших режимов.",
		},
		Tip:        "Роутеры Keenetic используют стабильные ядра Linux (от 4.9 до 5.15 в зависимости от модели).",
		ExampleLog: "syscall: Function not implemented",
		Keywords:   []string{"enosys", "38", "function not implemented"},
		Match: func(text string) bool {
			return matchAny(text, "ENOSYS", "errno 38", "error 38", "function not implemented")
		},
	},
	{
		ID:       "errno_enotempty_39",
		Category: "Ядро Linux",
		Code:     "ENOTEMPTY",
		Title:    "Каталог не пуст",
		Summary:  "Каталог не пуст",
		Cause:    "Попытка удалить каталог (rmdir), который содержит файлы или подкаталоги.",
		ActionSteps: []string{
			"Используйте рекурсивное удаление ('rm -rf') или удалите файлы внутри каталога перед его удалением.",
			"В файловом менеджере AWG Manager используйте подтвержденное удаление.",
		},
		Tip:        "Системный вызов rmdir по стандарту POSIX работает исключительно с пустыми папками.",
		ExampleLog: "rmdir: Directory not empty",
		Keywords:   []string{"enotempty", "39", "directory not empty"},
		Match: func(text string) bool {
			return matchAny(text, "ENOTEMPTY", "errno 39", "error 39", "directory not empty")
		},
	},
	{
		ID:       "errno_eloop_40",
		Category: "Ядро Linux",
		Code:     "ELOOP",
		Title:    "Зацикливание символических ссылок (петля Symlink)",
		Summary:  "Зацикливание символических ссылок (петля Symlink)",
		Cause:    "Символическая ссылка указывает сама на себя или образует бесконечную цепочку ссылок.",
		ActionSteps: []string{
			"В «Инструменты» -> «Терминал» выполните 'ls -l <путь>', чтобы увидеть, куда ведёт ссылка.",
			"Удалите повреждённую ссылку командой 'rm <ссылка>' и создайте корректную.",
		},
		Tip:        "Максимальная глубина резолвинга симлинков в ядре Linux обычно ограничена 40 уровнями.",
		ExampleLog: "open: Too many levels of symbolic links",
		Keywords:   []string{"eloop", "40", "too many levels of symbolic links"},
		Match: func(text string) bool {
			return matchAny(text, "ELOOP", "errno 40", "error 40", "too many levels of symbolic links")
		},
	},
	{
		ID:       "errno_enomsg_42",
		Category: "Ядро Linux",
		Code:     "ENOMSG",
		Title:    "Нет сообщения требуемого типа",
		Summary:  "Нет сообщения требуемого типа",
		Cause:    "Очередь сообщений IPC не содержит сообщения запрошенного типа.",
		ActionSteps: []string{
			"Штатная внутренняя ошибка межпроцессного взаимодействия POSIX IPC.",
		},
		Tip:        "Используется демонами очередей сообщений msgrcv.",
		ExampleLog: "msgrcv: No message of desired type",
		Keywords:   []string{"enomsg", "42", "no message of desired type"},
		Match: func(text string) bool {
			return matchAny(text, "ENOMSG", "errno 42", "error 42", "no message of desired type")
		},
	},
	{
		ID:       "errno_eidrm_43",
		Category: "Ядро Linux",
		Code:     "EIDRM",
		Title:    "Идентификатор ресурса IPC удален",
		Summary:  "Идентификатор ресурса IPC удален",
		Cause:    "Очередь сообщений, семафор или сегмент разделяемой памяти были удалены другим процессом.",
		ActionSteps: []string{
			"Перезапустите упавшую службу в «Инструменты» -> «Службы».",
		},
		Tip:        "Указывает на некорректное завершение одного из процессов в связке IPC.",
		ExampleLog: "msgget: Identifier removed",
		Keywords:   []string{"eidrm", "43", "identifier removed"},
		Match: func(text string) bool {
			return matchAny(text, "EIDRM", "errno 43", "error 43", "identifier removed")
		},
	},
	{
		ID:       "errno_echrng_44",
		Category: "Ядро Linux",
		Code:     "ECHRNG",
		Title:    "Системный код ошибки ядра Linux: ECHRNG (Channel number out of range)",
		Summary:  "Системный код ошибки ядра Linux: ECHRNG (Channel number out of range)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 44: ECHRNG).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 44 (ECHRNG).",
		ExampleLog: "system error: ECHRNG (44): Channel number out of range",
		Keywords:   []string{"echrng", "44", "channel number out of range"},
		Match: func(text string) bool {
			return matchAny(text, "ECHRNG", "errno 44", "error 44", "channel number out of range")
		},
	},
	{
		ID:       "errno_el2nsync_45",
		Category: "Ядро Linux",
		Code:     "EL2NSYNC",
		Title:    "Системный код ошибки ядра Linux: EL2NSYNC (Level 2 not synchronized)",
		Summary:  "Системный код ошибки ядра Linux: EL2NSYNC (Level 2 not synchronized)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 45: EL2NSYNC).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 45 (EL2NSYNC).",
		ExampleLog: "system error: EL2NSYNC (45): Level 2 not synchronized",
		Keywords:   []string{"el2nsync", "45", "level 2 not synchronized"},
		Match: func(text string) bool {
			return matchAny(text, "EL2NSYNC", "errno 45", "error 45", "level 2 not synchronized")
		},
	},
	{
		ID:       "errno_el3hlt_46",
		Category: "Ядро Linux",
		Code:     "EL3HLT",
		Title:    "Системный код ошибки ядра Linux: EL3HLT (Level 3 halted)",
		Summary:  "Системный код ошибки ядра Linux: EL3HLT (Level 3 halted)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 46: EL3HLT).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 46 (EL3HLT).",
		ExampleLog: "system error: EL3HLT (46): Level 3 halted",
		Keywords:   []string{"el3hlt", "46", "level 3 halted"},
		Match: func(text string) bool {
			return matchAny(text, "EL3HLT", "errno 46", "error 46", "level 3 halted")
		},
	},
	{
		ID:       "errno_el3rst_47",
		Category: "Ядро Linux",
		Code:     "EL3RST",
		Title:    "Системный код ошибки ядра Linux: EL3RST (Level 3 reset)",
		Summary:  "Системный код ошибки ядра Linux: EL3RST (Level 3 reset)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 47: EL3RST).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 47 (EL3RST).",
		ExampleLog: "system error: EL3RST (47): Level 3 reset",
		Keywords:   []string{"el3rst", "47", "level 3 reset"},
		Match: func(text string) bool {
			return matchAny(text, "EL3RST", "errno 47", "error 47", "level 3 reset")
		},
	},
	{
		ID:       "errno_elnrng_48",
		Category: "Ядро Linux",
		Code:     "ELNRNG",
		Title:    "Системный код ошибки ядра Linux: ELNRNG (Link number out of range)",
		Summary:  "Системный код ошибки ядра Linux: ELNRNG (Link number out of range)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 48: ELNRNG).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 48 (ELNRNG).",
		ExampleLog: "system error: ELNRNG (48): Link number out of range",
		Keywords:   []string{"elnrng", "48", "link number out of range"},
		Match: func(text string) bool {
			return matchAny(text, "ELNRNG", "errno 48", "error 48", "link number out of range")
		},
	},
	{
		ID:       "errno_eunatch_49",
		Category: "Ядро Linux",
		Code:     "EUNATCH",
		Title:    "Системный код ошибки ядра Linux: EUNATCH (Protocol driver not attached)",
		Summary:  "Системный код ошибки ядра Linux: EUNATCH (Protocol driver not attached)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 49: EUNATCH).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 49 (EUNATCH).",
		ExampleLog: "system error: EUNATCH (49): Protocol driver not attached",
		Keywords:   []string{"eunatch", "49", "protocol driver not attached"},
		Match: func(text string) bool {
			return matchAny(text, "EUNATCH", "errno 49", "error 49", "protocol driver not attached")
		},
	},
	{
		ID:       "errno_enocsi_50",
		Category: "Ядро Linux",
		Code:     "ENOCSI",
		Title:    "Системный код ошибки ядра Linux: ENOCSI (No CSI structure available)",
		Summary:  "Системный код ошибки ядра Linux: ENOCSI (No CSI structure available)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 50: ENOCSI).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 50 (ENOCSI).",
		ExampleLog: "system error: ENOCSI (50): No CSI structure available",
		Keywords:   []string{"enocsi", "50", "no csi structure available"},
		Match: func(text string) bool {
			return matchAny(text, "ENOCSI", "errno 50", "error 50", "no csi structure available")
		},
	},
	{
		ID:       "errno_el2hlt_51",
		Category: "Ядро Linux",
		Code:     "EL2HLT",
		Title:    "Системный код ошибки ядра Linux: EL2HLT (Level 2 halted)",
		Summary:  "Системный код ошибки ядра Linux: EL2HLT (Level 2 halted)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 51: EL2HLT).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 51 (EL2HLT).",
		ExampleLog: "system error: EL2HLT (51): Level 2 halted",
		Keywords:   []string{"el2hlt", "51", "level 2 halted"},
		Match: func(text string) bool {
			return matchAny(text, "EL2HLT", "errno 51", "error 51", "level 2 halted")
		},
	},
	{
		ID:       "errno_ebade_52",
		Category: "Ядро Linux",
		Code:     "EBADE",
		Title:    "Системный код ошибки ядра Linux: EBADE (Invalid exchange)",
		Summary:  "Системный код ошибки ядра Linux: EBADE (Invalid exchange)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 52: EBADE).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 52 (EBADE).",
		ExampleLog: "system error: EBADE (52): Invalid exchange",
		Keywords:   []string{"ebade", "52", "invalid exchange"},
		Match: func(text string) bool {
			return matchAny(text, "EBADE", "errno 52", "error 52", "invalid exchange")
		},
	},
	{
		ID:       "errno_ebadr_53",
		Category: "Ядро Linux",
		Code:     "EBADR",
		Title:    "Системный код ошибки ядра Linux: EBADR (Invalid request descriptor)",
		Summary:  "Системный код ошибки ядра Linux: EBADR (Invalid request descriptor)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 53: EBADR).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 53 (EBADR).",
		ExampleLog: "system error: EBADR (53): Invalid request descriptor",
		Keywords:   []string{"ebadr", "53", "invalid request descriptor"},
		Match: func(text string) bool {
			return matchAny(text, "EBADR", "errno 53", "error 53", "invalid request descriptor")
		},
	},
	{
		ID:       "errno_exfull_54",
		Category: "Ядро Linux",
		Code:     "EXFULL",
		Title:    "Системный код ошибки ядра Linux: EXFULL (Exchange full)",
		Summary:  "Системный код ошибки ядра Linux: EXFULL (Exchange full)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 54: EXFULL).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 54 (EXFULL).",
		ExampleLog: "system error: EXFULL (54): Exchange full",
		Keywords:   []string{"exfull", "54", "exchange full"},
		Match: func(text string) bool {
			return matchAny(text, "EXFULL", "errno 54", "error 54", "exchange full")
		},
	},
	{
		ID:       "errno_enoano_55",
		Category: "Ядро Linux",
		Code:     "ENOANO",
		Title:    "Системный код ошибки ядра Linux: ENOANO (No anode)",
		Summary:  "Системный код ошибки ядра Linux: ENOANO (No anode)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 55: ENOANO).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 55 (ENOANO).",
		ExampleLog: "system error: ENOANO (55): No anode",
		Keywords:   []string{"enoano", "55", "no anode"},
		Match: func(text string) bool {
			return matchAny(text, "ENOANO", "errno 55", "error 55", "no anode")
		},
	},
	{
		ID:       "errno_ebadrqc_56",
		Category: "Ядро Linux",
		Code:     "EBADRQC",
		Title:    "Системный код ошибки ядра Linux: EBADRQC (Invalid request code)",
		Summary:  "Системный код ошибки ядра Linux: EBADRQC (Invalid request code)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 56: EBADRQC).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 56 (EBADRQC).",
		ExampleLog: "system error: EBADRQC (56): Invalid request code",
		Keywords:   []string{"ebadrqc", "56", "invalid request code"},
		Match: func(text string) bool {
			return matchAny(text, "EBADRQC", "errno 56", "error 56", "invalid request code")
		},
	},
	{
		ID:       "errno_ebadslt_57",
		Category: "Ядро Linux",
		Code:     "EBADSLT",
		Title:    "Системный код ошибки ядра Linux: EBADSLT (Invalid slot)",
		Summary:  "Системный код ошибки ядра Linux: EBADSLT (Invalid slot)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 57: EBADSLT).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 57 (EBADSLT).",
		ExampleLog: "system error: EBADSLT (57): Invalid slot",
		Keywords:   []string{"ebadslt", "57", "invalid slot"},
		Match: func(text string) bool {
			return matchAny(text, "EBADSLT", "errno 57", "error 57", "invalid slot")
		},
	},
	{
		ID:       "errno_ebfont_59",
		Category: "Ядро Linux",
		Code:     "EBFONT",
		Title:    "Системный код ошибки ядра Linux: EBFONT (Bad font file format)",
		Summary:  "Системный код ошибки ядра Linux: EBFONT (Bad font file format)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 59: EBFONT).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 59 (EBFONT).",
		ExampleLog: "system error: EBFONT (59): Bad font file format",
		Keywords:   []string{"ebfont", "59", "bad font file format"},
		Match: func(text string) bool {
			return matchAny(text, "EBFONT", "errno 59", "error 59", "bad font file format")
		},
	},
	{
		ID:       "errno_enostr_60",
		Category: "Ядро Linux",
		Code:     "ENOSTR",
		Title:    "Системный код ошибки ядра Linux: ENOSTR (Device not a stream)",
		Summary:  "Системный код ошибки ядра Linux: ENOSTR (Device not a stream)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 60: ENOSTR).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 60 (ENOSTR).",
		ExampleLog: "system error: ENOSTR (60): Device not a stream",
		Keywords:   []string{"enostr", "60", "device not a stream"},
		Match: func(text string) bool {
			return matchAny(text, "ENOSTR", "errno 60", "error 60", "device not a stream")
		},
	},
	{
		ID:       "errno_enodata_61",
		Category: "Ядро Linux",
		Code:     "ENODATA",
		Title:    "Системный код ошибки ядра Linux: ENODATA (No data available)",
		Summary:  "Системный код ошибки ядра Linux: ENODATA (No data available)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 61: ENODATA).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 61 (ENODATA).",
		ExampleLog: "system error: ENODATA (61): No data available",
		Keywords:   []string{"enodata", "61", "no data available"},
		Match: func(text string) bool {
			return matchAny(text, "ENODATA", "errno 61", "error 61", "no data available")
		},
	},
	{
		ID:       "errno_etime_62",
		Category: "Ядро Linux",
		Code:     "ETIME",
		Title:    "Системный код ошибки ядра Linux: ETIME (Timer expired)",
		Summary:  "Системный код ошибки ядра Linux: ETIME (Timer expired)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 62: ETIME).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 62 (ETIME).",
		ExampleLog: "system error: ETIME (62): Timer expired",
		Keywords:   []string{"etime", "62", "timer expired"},
		Match: func(text string) bool {
			return matchAny(text, "ETIME", "errno 62", "error 62", "timer expired")
		},
	},
	{
		ID:       "errno_enosr_63",
		Category: "Ядро Linux",
		Code:     "ENOSR",
		Title:    "Системный код ошибки ядра Linux: ENOSR (Out of streams resources)",
		Summary:  "Системный код ошибки ядра Linux: ENOSR (Out of streams resources)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 63: ENOSR).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 63 (ENOSR).",
		ExampleLog: "system error: ENOSR (63): Out of streams resources",
		Keywords:   []string{"enosr", "63", "out of streams resources"},
		Match: func(text string) bool {
			return matchAny(text, "ENOSR", "errno 63", "error 63", "out of streams resources")
		},
	},
	{
		ID:       "errno_enonet_64",
		Category: "Ядро Linux",
		Code:     "ENONET",
		Title:    "Системный код ошибки ядра Linux: ENONET (Machine is not on the network)",
		Summary:  "Системный код ошибки ядра Linux: ENONET (Machine is not on the network)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 64: ENONET).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 64 (ENONET).",
		ExampleLog: "system error: ENONET (64): Machine is not on the network",
		Keywords:   []string{"enonet", "64", "machine is not on the network"},
		Match: func(text string) bool {
			return matchAny(text, "ENONET", "errno 64", "error 64", "machine is not on the network")
		},
	},
	{
		ID:       "errno_enopkg_65",
		Category: "Ядро Linux",
		Code:     "ENOPKG",
		Title:    "Системный код ошибки ядра Linux: ENOPKG (Package not installed)",
		Summary:  "Системный код ошибки ядра Linux: ENOPKG (Package not installed)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 65: ENOPKG).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 65 (ENOPKG).",
		ExampleLog: "system error: ENOPKG (65): Package not installed",
		Keywords:   []string{"enopkg", "65", "package not installed"},
		Match: func(text string) bool {
			return matchAny(text, "ENOPKG", "errno 65", "error 65", "package not installed")
		},
	},
	{
		ID:       "errno_eremote_66",
		Category: "Ядро Linux",
		Code:     "EREMOTE",
		Title:    "Системный код ошибки ядра Linux: EREMOTE (Object is remote)",
		Summary:  "Системный код ошибки ядра Linux: EREMOTE (Object is remote)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 66: EREMOTE).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 66 (EREMOTE).",
		ExampleLog: "system error: EREMOTE (66): Object is remote",
		Keywords:   []string{"eremote", "66", "object is remote"},
		Match: func(text string) bool {
			return matchAny(text, "EREMOTE", "errno 66", "error 66", "object is remote")
		},
	},
	{
		ID:       "errno_enolink_67",
		Category: "Ядро Linux",
		Code:     "ENOLINK",
		Title:    "Системный код ошибки ядра Linux: ENOLINK (Link has been severed)",
		Summary:  "Системный код ошибки ядра Linux: ENOLINK (Link has been severed)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 67: ENOLINK).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 67 (ENOLINK).",
		ExampleLog: "system error: ENOLINK (67): Link has been severed",
		Keywords:   []string{"enolink", "67", "link has been severed"},
		Match: func(text string) bool {
			return matchAny(text, "ENOLINK", "errno 67", "error 67", "link has been severed")
		},
	},
	{
		ID:       "errno_eadv_68",
		Category: "Ядро Linux",
		Code:     "EADV",
		Title:    "Системный код ошибки ядра Linux: EADV (Advertise error)",
		Summary:  "Системный код ошибки ядра Linux: EADV (Advertise error)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 68: EADV).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 68 (EADV).",
		ExampleLog: "system error: EADV (68): Advertise error",
		Keywords:   []string{"eadv", "68", "advertise error"},
		Match: func(text string) bool {
			return matchAny(text, "EADV", "errno 68", "error 68", "advertise error")
		},
	},
	{
		ID:       "errno_esrmnt_69",
		Category: "Ядро Linux",
		Code:     "ESRMNT",
		Title:    "Системный код ошибки ядра Linux: ESRMNT (Srmount error)",
		Summary:  "Системный код ошибки ядра Linux: ESRMNT (Srmount error)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 69: ESRMNT).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 69 (ESRMNT).",
		ExampleLog: "system error: ESRMNT (69): Srmount error",
		Keywords:   []string{"esrmnt", "69", "srmount error"},
		Match: func(text string) bool {
			return matchAny(text, "ESRMNT", "errno 69", "error 69", "srmount error")
		},
	},
	{
		ID:       "errno_ecomm_70",
		Category: "Ядро Linux",
		Code:     "ECOMM",
		Title:    "Ошибка связи при отправке сетевого пакета",
		Summary:  "Ошибка связи при отправке сетевого пакета",
		Cause:    "Сбой передачи данных по сетевому каналу.",
		ActionSteps: []string{
			"Проверьте физическое подключение кабеля Ethernet или уровень сигнала Wi-Fi.",
			"Перезапустите туннель в интерфейсе AWG Manager.",
		},
		Tip:        "Связано с аппаратными сбоями сетевого адаптера или сетевой подсистемы.",
		ExampleLog: "send: Communication error on send",
		Keywords:   []string{"ecomm", "70", "communication error on send"},
		Match: func(text string) bool {
			return matchAny(text, "ECOMM", "errno 70", "error 70", "communication error on send")
		},
	},
	{
		ID:       "errno_eproto_71",
		Category: "Ядро Linux",
		Code:     "EPROTO",
		Title:    "Ошибка протокола связи",
		Summary:  "Ошибка протокола связи",
		Cause:    "Получен пакет, нарушающий спецификацию сетевого протокола.",
		ActionSteps: []string{
			"Проверьте совпадение версий протоколов на клиенте и сервере (например, AmneziaWG или VLESS).",
			"Убедитесь, что параметры обфускации (H1-H4, Jmin-Jmax) строго совпадают.",
		},
		Tip:        "Любое несовпадение обфускационных заголовков приводит к отбрасыванию пакета.",
		ExampleLog: "protocol error: invalid frame header",
		Keywords:   []string{"eproto", "71", "protocol error"},
		Match: func(text string) bool {
			return matchAny(text, "EPROTO", "errno 71", "error 71", "protocol error")
		},
	},
	{
		ID:       "errno_emultihop_72",
		Category: "Ядро Linux",
		Code:     "EMULTIHOP",
		Title:    "Системный код ошибки ядра Linux: EMULTIHOP (Multihop attempted)",
		Summary:  "Системный код ошибки ядра Linux: EMULTIHOP (Multihop attempted)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 72: EMULTIHOP).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 72 (EMULTIHOP).",
		ExampleLog: "system error: EMULTIHOP (72): Multihop attempted",
		Keywords:   []string{"emultihop", "72", "multihop attempted"},
		Match: func(text string) bool {
			return matchAny(text, "EMULTIHOP", "errno 72", "error 72", "multihop attempted")
		},
	},
	{
		ID:       "errno_edotdot_73",
		Category: "Ядро Linux",
		Code:     "EDOTDOT",
		Title:    "Системный код ошибки ядра Linux: EDOTDOT (RFS specific error)",
		Summary:  "Системный код ошибки ядра Linux: EDOTDOT (RFS specific error)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 73: EDOTDOT).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 73 (EDOTDOT).",
		ExampleLog: "system error: EDOTDOT (73): RFS specific error",
		Keywords:   []string{"edotdot", "73", "rfs specific error"},
		Match: func(text string) bool {
			return matchAny(text, "EDOTDOT", "errno 73", "error 73", "rfs specific error")
		},
	},
	{
		ID:       "errno_ebadmsg_74",
		Category: "Ядро Linux",
		Code:     "EBADMSG",
		Title:    "Поврежденное сообщение или нарушение целостности данных",
		Summary:  "Поврежденное сообщение или нарушение целостности данных",
		Cause:    "Принятое сообщение не прошло проверку контрольной суммы, подписи или заголовка.",
		ActionSteps: []string{
			"Проверьте стабильность интернет-канала: возможны сильные потери пакетов у провайдера.",
			"Переподключите туннель.",
		},
		Tip:        "Часто свидетельствует о повреждении данных на физической линии связи.",
		ExampleLog: "read: Bad message",
		Keywords:   []string{"ebadmsg", "74", "bad message"},
		Match: func(text string) bool {
			return matchAny(text, "EBADMSG", "errno 74", "error 74", "bad message")
		},
	},
	{
		ID:       "errno_eoverflow_75",
		Category: "Ядро Linux",
		Code:     "EOVERFLOW",
		Title:    "Значение слишком велико для данного типа данных",
		Summary:  "Значение слишком велико для данного типа данных",
		Cause:    "Попытка поместить 64-битное значение (например, размер диска) в 32-битную переменную.",
		ActionSteps: []string{
			"Убедитесь, что используете пакеты, скомпилированные с поддержкой Large File Support (LFS).",
			"Обновите пакет через 'opkg upgrade'.",
		},
		Tip:        "В 32-битных системах MIPS файлы размером более 2 ГБ требуют сборки с -D_FILE_OFFSET_BITS=64.",
		ExampleLog: "stat: Value too large for defined data type",
		Keywords:   []string{"eoverflow", "75", "value too large for defined data type"},
		Match: func(text string) bool {
			return matchAny(text, "EOVERFLOW", "errno 75", "error 75", "value too large for defined data type")
		},
	},
	{
		ID:       "errno_enotuniq_76",
		Category: "Ядро Linux",
		Code:     "ENOTUNIQ",
		Title:    "Системный код ошибки ядра Linux: ENOTUNIQ (Name not unique on network)",
		Summary:  "Системный код ошибки ядра Linux: ENOTUNIQ (Name not unique on network)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 76: ENOTUNIQ).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 76 (ENOTUNIQ).",
		ExampleLog: "system error: ENOTUNIQ (76): Name not unique on network",
		Keywords:   []string{"enotuniq", "76", "name not unique on network"},
		Match: func(text string) bool {
			return matchAny(text, "ENOTUNIQ", "errno 76", "error 76", "name not unique on network")
		},
	},
	{
		ID:       "errno_ebadfd_77",
		Category: "Ядро Linux",
		Code:     "EBADFD",
		Title:    "Системный код ошибки ядра Linux: EBADFD (File descriptor in bad state)",
		Summary:  "Системный код ошибки ядра Linux: EBADFD (File descriptor in bad state)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 77: EBADFD).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 77 (EBADFD).",
		ExampleLog: "system error: EBADFD (77): File descriptor in bad state",
		Keywords:   []string{"ebadfd", "77", "file descriptor in bad state"},
		Match: func(text string) bool {
			return matchAny(text, "EBADFD", "errno 77", "error 77", "file descriptor in bad state")
		},
	},
	{
		ID:       "errno_eremchg_78",
		Category: "Ядро Linux",
		Code:     "EREMCHG",
		Title:    "Системный код ошибки ядра Linux: EREMCHG (Remote address changed)",
		Summary:  "Системный код ошибки ядра Linux: EREMCHG (Remote address changed)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 78: EREMCHG).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 78 (EREMCHG).",
		ExampleLog: "system error: EREMCHG (78): Remote address changed",
		Keywords:   []string{"eremchg", "78", "remote address changed"},
		Match: func(text string) bool {
			return matchAny(text, "EREMCHG", "errno 78", "error 78", "remote address changed")
		},
	},
	{
		ID:       "errno_elibacc_79",
		Category: "Ядро Linux",
		Code:     "ELIBACC",
		Title:    "Системный код ошибки ядра Linux: ELIBACC (Can not access a needed shared library)",
		Summary:  "Системный код ошибки ядра Linux: ELIBACC (Can not access a needed shared library)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 79: ELIBACC).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 79 (ELIBACC).",
		ExampleLog: "system error: ELIBACC (79): Can not access a needed shared library",
		Keywords:   []string{"elibacc", "79", "can not access a needed shared library"},
		Match: func(text string) bool {
			return matchAny(text, "ELIBACC", "errno 79", "error 79", "can not access a needed shared library")
		},
	},
	{
		ID:       "errno_elibbad_80",
		Category: "Ядро Linux",
		Code:     "ELIBBAD",
		Title:    "Повреждена динамическая библиотека (.so)",
		Summary:  "Повреждена динамическая библиотека (.so)",
		Cause:    "Файл системной библиотеки поврежден на диске или не совпадает контрольная сумма ELF.",
		ActionSteps: []string{
			"Переустановите повреждённый пакет через «Пакеты opkg».",
			"Проверьте целостность диска /opt.",
		},
		Tip:        "Возникает при сбое питания во время записи на флешку.",
		ExampleLog: "libssl.so: Accessing a corrupted shared library",
		Keywords:   []string{"elibbad", "80", "accessing a corrupted shared library"},
		Match: func(text string) bool {
			return matchAny(text, "ELIBBAD", "errno 80", "error 80", "accessing a corrupted shared library")
		},
	},
	{
		ID:       "errno_elibscn_81",
		Category: "Ядро Linux",
		Code:     "ELIBSCN",
		Title:    "Системный код ошибки ядра Linux: ELIBSCN (.lib section in a.out corrupted)",
		Summary:  "Системный код ошибки ядра Linux: ELIBSCN (.lib section in a.out corrupted)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 81: ELIBSCN).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 81 (ELIBSCN).",
		ExampleLog: "system error: ELIBSCN (81): .lib section in a.out corrupted",
		Keywords:   []string{"elibscn", "81", ".lib section in a.out corrupted"},
		Match: func(text string) bool {
			return matchAny(text, "ELIBSCN", "errno 81", "error 81", ".lib section in a.out corrupted")
		},
	},
	{
		ID:       "errno_elibmax_82",
		Category: "Ядро Linux",
		Code:     "ELIBMAX",
		Title:    "Системный код ошибки ядра Linux: ELIBMAX (Attempting to link in too many shared libraries)",
		Summary:  "Системный код ошибки ядра Linux: ELIBMAX (Attempting to link in too many shared libraries)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 82: ELIBMAX).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 82 (ELIBMAX).",
		ExampleLog: "system error: ELIBMAX (82): Attempting to link in too many shared libraries",
		Keywords:   []string{"elibmax", "82", "attempting to link in too many shared libraries"},
		Match: func(text string) bool {
			return matchAny(text, "ELIBMAX", "errno 82", "error 82", "attempting to link in too many shared libraries")
		},
	},
	{
		ID:       "errno_elibexec_83",
		Category: "Ядро Linux",
		Code:     "ELIBEXEC",
		Title:    "Системный код ошибки ядра Linux: ELIBEXEC (Cannot exec a shared library directly)",
		Summary:  "Системный код ошибки ядра Linux: ELIBEXEC (Cannot exec a shared library directly)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 83: ELIBEXEC).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 83 (ELIBEXEC).",
		ExampleLog: "system error: ELIBEXEC (83): Cannot exec a shared library directly",
		Keywords:   []string{"elibexec", "83", "cannot exec a shared library directly"},
		Match: func(text string) bool {
			return matchAny(text, "ELIBEXEC", "errno 83", "error 83", "cannot exec a shared library directly")
		},
	},
	{
		ID:       "errno_eilseq_84",
		Category: "Ядро Linux",
		Code:     "EILSEQ",
		Title:    "Недопустимая кодировка текста (ошибка UTF-8)",
		Summary:  "Недопустимая кодировка текста (ошибка UTF-8)",
		Cause:    "В конфигурационном файле обнаружены байты, недопустимые в кодировке UTF-8.",
		ActionSteps: []string{
			"Откройте конфигурационный файл во вкладке «Файлы».",
			"Сохраните файл строго в кодировке UTF-8 без BOM.",
		},
		Tip:        "Конфигурационные файлы YAML и JSON обязаны быть в валидной кодировке UTF-8.",
		ExampleLog: "utf8 decode: Invalid multibyte sequence",
		Keywords:   []string{"eilseq", "84", "invalid or incomplete multibyte or wide character"},
		Match: func(text string) bool {
			return matchAny(text, "EILSEQ", "errno 84", "error 84", "invalid or incomplete multibyte or wide character")
		},
	},
	{
		ID:       "errno_erestart_85",
		Category: "Ядро Linux",
		Code:     "ERESTART",
		Title:    "Системный код ошибки ядра Linux: ERESTART (Interrupted system call should be restarted)",
		Summary:  "Системный код ошибки ядра Linux: ERESTART (Interrupted system call should be restarted)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 85: ERESTART).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 85 (ERESTART).",
		ExampleLog: "system error: ERESTART (85): Interrupted system call should be restarted",
		Keywords:   []string{"erestart", "85", "interrupted system call should be restarted"},
		Match: func(text string) bool {
			return matchAny(text, "ERESTART", "errno 85", "error 85", "interrupted system call should be restarted")
		},
	},
	{
		ID:       "errno_estrpipe_86",
		Category: "Ядро Linux",
		Code:     "ESTRPIPE",
		Title:    "Системный код ошибки ядра Linux: ESTRPIPE (Streams pipe error)",
		Summary:  "Системный код ошибки ядра Linux: ESTRPIPE (Streams pipe error)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 86: ESTRPIPE).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 86 (ESTRPIPE).",
		ExampleLog: "system error: ESTRPIPE (86): Streams pipe error",
		Keywords:   []string{"estrpipe", "86", "streams pipe error"},
		Match: func(text string) bool {
			return matchAny(text, "ESTRPIPE", "errno 86", "error 86", "streams pipe error")
		},
	},
	{
		ID:       "errno_eusers_87",
		Category: "Ядро Linux",
		Code:     "EUSERS",
		Title:    "Системный код ошибки ядра Linux: EUSERS (Too many users)",
		Summary:  "Системный код ошибки ядра Linux: EUSERS (Too many users)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 87: EUSERS).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 87 (EUSERS).",
		ExampleLog: "system error: EUSERS (87): Too many users",
		Keywords:   []string{"eusers", "87", "too many users"},
		Match: func(text string) bool {
			return matchAny(text, "EUSERS", "errno 87", "error 87", "too many users")
		},
	},
	{
		ID:       "errno_enotsock_88",
		Category: "Ядро Linux",
		Code:     "ENOTSOCK",
		Title:    "Операция с сокетом на объекте, не являющемся сокетом",
		Summary:  "Операция с сокетом на объекте, не являющемся сокетом",
		Cause:    "Попытка вызвать сетевую операцию (bind, connect, accept) на обычном файловом дескрипторе.",
		ActionSteps: []string{
			"Указывает на ошибку в коде программы.",
			"Обновите бинарник службы.",
		},
		Tip:        "Файловые дескрипторы сокетов отличаются от файлов типом S_ISSOCK.",
		ExampleLog: "getsockname: Socket operation on non-socket",
		Keywords:   []string{"enotsock", "88", "socket operation on non-socket"},
		Match: func(text string) bool {
			return matchAny(text, "ENOTSOCK", "errno 88", "error 88", "socket operation on non-socket")
		},
	},
	{
		ID:       "errno_edestaddrreq_89",
		Category: "Ядро Linux",
		Code:     "EDESTADDRREQ",
		Title:    "Требуется адрес назначения",
		Summary:  "Требуется адрес назначения",
		Cause:    "Попытка отправки дейтаграммы без указания адреса получателя через сокет без соединения.",
		ActionSteps: []string{
			"Проверьте поле Endpoint в настройках пира WireGuard: адрес сервера и порт обязательны.",
		},
		Tip:        "UDP-сокеты без вызова connect() требуют указания адреса в sendto().",
		ExampleLog: "send: Destination address required",
		Keywords:   []string{"edestaddrreq", "89", "destination address required"},
		Match: func(text string) bool {
			return matchAny(text, "EDESTADDRREQ", "errno 89", "error 89", "destination address required")
		},
	},
	{
		ID:       "errno_emsgsize_90",
		Category: "Ядро Linux",
		Code:     "EMSGSIZE",
		Title:    "Размер пакета превышает допустимый MTU",
		Summary:  "Размер пакета превышает допустимый MTU",
		Cause:    "Попытка отправить UDP-пакет размером больше допустимого максимального блока передачи (MTU).",
		ActionSteps: []string{
			"В настройках туннеля уменьшите значение MTU (например, установите 1360 или 1280).",
			"Нажмите карандаш у туннеля в AWG Manager и измените MTU.",
		},
		Tip:        "Для туннелей AmneziaWG безопасным значением MTU является 1280–1360.",
		ExampleLog: "sendto: Message too long (errno 90)",
		Keywords:   []string{"emsgsize", "90", "message too long"},
		Match: func(text string) bool {
			return matchAny(text, "EMSGSIZE", "errno 90", "error 90", "message too long")
		},
	},
	{
		ID:       "errno_eprototype_91",
		Category: "Ядро Linux",
		Code:     "EPROTOTYPE",
		Title:    "Неверный тип протокола для сокета",
		Summary:  "Неверный тип протокола для сокета",
		Cause:    "Запрошен протокол, не поддерживаемый данным типом сокета.",
		ActionSteps: []string{
			"Проверьте настройки протокола в конфигурационном файле.",
		},
		Tip:        "Например, попытка открыть TCP-протокол на дейтаграммном сокете SOCK_DGRAM.",
		ExampleLog: "socket: Protocol wrong type for socket",
		Keywords:   []string{"eprototype", "91", "protocol wrong type for socket"},
		Match: func(text string) bool {
			return matchAny(text, "EPROTOTYPE", "errno 91", "error 91", "protocol wrong type for socket")
		},
	},
	{
		ID:       "errno_enoprotoopt_92",
		Category: "Ядро Linux",
		Code:     "ENOPROTOOPT",
		Title:    "Параметр протокола недоступен в ядре",
		Summary:  "Параметр протокола недоступен в ядре",
		Cause:    "Попытка включить опцию сокета (setsockopt), которая не поддерживается текущей версией ядра роутера.",
		ActionSteps: []string{
			"В настройках движка (Sing-box/Mihomo) отключите специфичные TCP-опции (TCP BBR, TCP Fast Open), если они не поддерживаются роутером.",
		},
		Tip:        "TCP Fast Open и BBR требуют поддержки со стороны ядра роутера.",
		ExampleLog: "setsockopt: Protocol not available",
		Keywords:   []string{"enoprotoopt", "92", "protocol not available"},
		Match: func(text string) bool {
			return matchAny(text, "ENOPROTOOPT", "errno 92", "error 92", "protocol not available")
		},
	},
	{
		ID:       "errno_eprotonosupport_93",
		Category: "Ядро Linux",
		Code:     "EPROTONOSUPPORT",
		Title:    "Протокол не поддерживается системой",
		Summary:  "Протокол не поддерживается системой",
		Cause:    "Ядро роутера не имеет встроенного модуля для запрошенного сетевого протокола.",
		ActionSteps: []string{
			"Убедитесь, что используете стандартные протоколы IPv4/UDP/TCP.",
			"Для IPv6 проверьте, включен ли компонент IPv6 в KeeneticOS.",
		},
		Tip:        "Убедитесь, что все необходимые сетевые компоненты установлены в системе.",
		ExampleLog: "socket: Protocol not supported",
		Keywords:   []string{"eprotonosupport", "93", "protocol not supported"},
		Match: func(text string) bool {
			return matchAny(text, "EPROTONOSUPPORT", "errno 93", "error 93", "protocol not supported")
		},
	},
	{
		ID:       "errno_esocktnosupport_94",
		Category: "Ядро Linux",
		Code:     "ESOCKTNOSUPPORT",
		Title:    "Тип сокета не поддерживается",
		Summary:  "Тип сокета не поддерживается",
		Cause:    "Запрошен неизвестный тип сокета (например, сырой сокет SOCK_RAW без прав).",
		ActionSteps: []string{
			"Проверьте настройки сетевой службы.",
		},
		Tip:        "Сырые сокеты SOCK_RAW требуют специальных привилегий суперпользователя.",
		ExampleLog: "socket: Socket type not supported",
		Keywords:   []string{"esocktnosupport", "94", "socket type not supported"},
		Match: func(text string) bool {
			return matchAny(text, "ESOCKTNOSUPPORT", "errno 94", "error 94", "socket type not supported")
		},
	},
	{
		ID:       "errno_eopnotsupp_95",
		Category: "Ядро Linux",
		Code:     "EOPNOTSUPP",
		Title:    "Операция не поддерживается сетевым интерфейсом",
		Summary:  "Операция не поддерживается сетевым интерфейсом",
		Cause:    "Попытка выполнить операцию, которую данный сетевой драйвер или интерфейс не поддерживает.",
		ActionSteps: []string{
			"Проверьте параметры туннеля.",
			"Некоторые виртуальные интерфейсы не поддерживают аппаратное ускорение offloading.",
		},
		Tip:        "Виртуальные туннели обрабатываются программным стеком ядра без аппаратного ускорения чипа.",
		ExampleLog: "ioctl: Operation not supported",
		Keywords:   []string{"eopnotsupp", "95", "operation not supported"},
		Match: func(text string) bool {
			return matchAny(text, "EOPNOTSUPP", "errno 95", "error 95", "operation not supported")
		},
	},
	{
		ID:       "errno_epfnosupport_96",
		Category: "Ядро Linux",
		Code:     "EPFNOSUPPORT",
		Title:    "Семейство протоколов не поддерживается",
		Summary:  "Семейство протоколов не поддерживается",
		Cause:    "Запрошено семейство протоколов, отключенное в конфигурации ядра.",
		ActionSteps: []string{
			"Проверьте сетевые параметры.",
		},
		Tip:        "Указывает на отсутствие поддержки специфичного стека протоколов.",
		ExampleLog: "socket: Protocol family not supported",
		Keywords:   []string{"epfnosupport", "96", "protocol family not supported"},
		Match: func(text string) bool {
			return matchAny(text, "EPFNOSUPPORT", "errno 96", "error 96", "protocol family not supported")
		},
	},
	{
		ID:       "errno_eafnosupport_97",
		Category: "Ядро Linux",
		Code:     "EAFNOSUPPORT",
		Title:    "Семейство адресов не поддерживается (IPv6 выключен)",
		Summary:  "Семейство адресов не поддерживается (IPv6 выключен)",
		Cause:    "Попытка подключиться к IPv6-адресу или открыть IPv6-сокет на роутере, где поддержка IPv6 отключена в ядре или настройках провайдера.",
		ActionSteps: []string{
			"В настройках туннеля замените IPv6-адрес Endpoint на стандартный IPv4-адрес.",
			"Либо включите поддержку IPv6 в веб-интерфейсе Keenetic: «Сетевые правила» -> «Интернет».",
			"В AWG Manager нажмите карандаш у туннеля и проверьте строку Endpoint.",
		},
		Tip:        "Если у провайдера нет IPv6, используйте только IPv4-адреса для подключения к серверам.",
		ExampleLog: "socket: Address family not supported by protocol",
		Keywords:   []string{"eafnosupport", "97", "address family not supported by protocol"},
		Match: func(text string) bool {
			return matchAny(text, "EAFNOSUPPORT", "errno 97", "error 97", "address family not supported by protocol")
		},
	},
	{
		ID:       "errno_eaddrinuse_98",
		Category: "Ядро Linux",
		Code:     "EADDRINUSE",
		Title:    "Сетевой порт или адрес уже заняты другой службой",
		Summary:  "Сетевой порт или адрес уже заняты другой службой",
		Cause:    "Служба не может открыться на порту (например, 7890, 10808, 53), потому что этот порт уже занят другим запущенным процессом.",
		ActionSteps: []string{
			"Перейдите во вкладку «Инструменты» -> «Порты».",
			"Найдите номер занятого порта и посмотрите PID и имя процесса, который его удерживает.",
			"Остановите конфликтующий процесс или смените порт в настройках.",
		},
		Tip:        "Частый конфликт: запуск двух прокси одновременно на одном mixed-port (7890).",
		ExampleLog: "listen tcp 0.0.0.0:7890: bind: address already in use",
		Keywords:   []string{"eaddrinuse", "98", "address already in use"},
		Match: func(text string) bool {
			return matchAny(text, "EADDRINUSE", "errno 98", "error 98", "address already in use")
		},
	},
	{
		ID:       "errno_eaddrnotavail_99",
		Category: "Ядро Linux",
		Code:     "EADDRNOTAVAIL",
		Title:    "Невозможно назначить запрашиваемый IP-адрес",
		Summary:  "Невозможно назначить запрашиваемый IP-адрес",
		Cause:    "Попытка привязать сокет к локальному IP-адресу, который отсутствует на сетевых интерфейсах роутера.",
		ActionSteps: []string{
			"Проверьте список локальных IP-адресов роутера в «Инструменты» -> «Сетевые интерфейсы».",
			"В конфигурации используйте '0.0.0.0' для прослушивания всех доступных адресов.",
		},
		Tip:        "Привязка к конкретному IP возможна только если этот IP назначен сетевой карте роутера.",
		ExampleLog: "bind: cannot assign requested address",
		Keywords:   []string{"eaddrnotavail", "99", "cannot assign requested address"},
		Match: func(text string) bool {
			return matchAny(text, "EADDRNOTAVAIL", "errno 99", "error 99", "cannot assign requested address")
		},
	},
	{
		ID:       "errno_enetdown_100",
		Category: "Ядро Linux",
		Code:     "ENETDOWN",
		Title:    "Сетевой интерфейс выключен",
		Summary:  "Сетевой интерфейс выключен",
		Cause:    "Попытка передачи пакета через интерфейс, находящийся в состоянии DOWN.",
		ActionSteps: []string{
			"Проверьте статус физического подключения интернет-кабеля провайдера.",
			"В интерфейсе AWG Manager убедитесь, что переключатель нужного туннеля включен.",
		},
		Tip:        "Интерфейс переходит в UP автоматически при успешной активации туннеля.",
		ExampleLog: "sendto: Network is down",
		Keywords:   []string{"enetdown", "100", "network is down"},
		Match: func(text string) bool {
			return matchAny(text, "ENETDOWN", "errno 100", "error 100", "network is down")
		},
	},
	{
		ID:       "errno_enetunreach_101",
		Category: "Ядро Linux",
		Code:     "ENETUNREACH",
		Title:    "Сеть недостижима (отсутствует маршрут)",
		Summary:  "Сеть недостижима (отсутствует маршрут)",
		Cause:    "У роутера нет маршрута для отправки пакета по указанному адресу (нет основного интернет-шлюза).",
		ActionSteps: []string{
			"Проверьте подключение к интернету: откройте веб-панель Keenetic и убедитесь, что статус провайдера 'Подключено'.",
			"Проверьте таблицу маршрутизации в «Инструменты» -> «Маршруты».",
			"Убедитесь, что туннель включен.",
		},
		Tip:        "При падении провайдера маршрут по умолчанию (default gateway 0.0.0.0/0) временно удаляется из ядра.",
		ExampleLog: "connect: network is unreachable",
		Keywords:   []string{"enetunreach", "101", "network is unreachable"},
		Match: func(text string) bool {
			return matchAny(text, "ENETUNREACH", "errno 101", "error 101", "network is unreachable")
		},
	},
	{
		ID:       "errno_enetreset_102",
		Category: "Ядро Linux",
		Code:     "ENETRESET",
		Title:    "Сеть разорвала соединение из-за сброса интерфейса",
		Summary:  "Сеть разорвала соединение из-за сброса интерфейса",
		Cause:    "Сетевой стек сбросил активные соединения из-за перезапуска сетевого адаптера или смены IP-адреса.",
		ActionSteps: []string{
			"Соединение восстановится автоматически при стабилизации канала.",
			"Если туннель не поднимается, выключите и включите его заново в списке туннелей.",
		},
		Tip:        "Происходит при переключении резервного провайдера (Multi-WAN failover).",
		ExampleLog: "read: Network dropped connection on reset",
		Keywords:   []string{"enetreset", "102", "network dropped connection on reset"},
		Match: func(text string) bool {
			return matchAny(text, "ENETRESET", "errno 102", "error 102", "network dropped connection on reset")
		},
	},
	{
		ID:       "errno_econnaborted_103",
		Category: "Ядро Linux",
		Code:     "ECONNABORTED",
		Title:    "Соединение прервано локальным программным обеспечением",
		Summary:  "Соединение прервано локальным программным обеспечением",
		Cause:    "Локальный процесс закрыл сокет до того, как удалённый узел завершил трёхстороннее рукопожатие TCP.",
		ActionSteps: []string{
			"Штатно возникает при закрытии вкладок в браузере или отмене загрузки пользователем.",
		},
		Tip:        "Не требует вмешательства, если не повторяется непрерывно.",
		ExampleLog: "accept: Software caused connection abort",
		Keywords:   []string{"econnaborted", "103", "software caused connection abort"},
		Match: func(text string) bool {
			return matchAny(text, "ECONNABORTED", "errno 103", "error 103", "software caused connection abort")
		},
	},
	{
		ID:       "errno_econnreset_104",
		Category: "Ядро Linux",
		Code:     "ECONNRESET",
		Title:    "Соединение сброшено удаленной стороной (RST пакет)",
		Summary:  "Соединение сброшено удаленной стороной (RST пакет)",
		Cause:    "Удалённый сервер или провайдерский ТСПУ/DPI принудительно разорвал TCP-соединение, отправив TCP RST флаг.",
		ActionSteps: []string{
			"Если ошибка возникает при открытии заблокированных сайтов — провайдер применяет DPI-фильтрацию.",
			"Включите обфускацию AmneziaWG (параметры H1-H4, Jc, Jmin, Jmax).",
			"Для Sing-box/Mihomo используйте протоколы Reality или VLESS.",
		},
		Tip:        "TCP RST — главный индикатор блокировки трафика цензурой или фильтрами провайдера.",
		ExampleLog: "read: connection reset by peer",
		Keywords:   []string{"econnreset", "104", "connection reset by peer"},
		Match: func(text string) bool {
			return matchAny(text, "ECONNRESET", "errno 104", "error 104", "connection reset by peer")
		},
	},
	{
		ID:       "errno_enobufs_105",
		Category: "Ядро Linux",
		Code:     "ENOBUFS",
		Title:    "Недостаточно буферной памяти сетевых сокетов",
		Summary:  "Недостаточно буферной памяти сетевых сокетов",
		Cause:    "Сетевая очередь передачи ядра Linux переполнена из-за огромного потока пакетов.",
		ActionSteps: []string{
			"Увеличьте лимит сетевой очереди txqueuelen: 'ip link set dev awg0 txqueuelen 1000'.",
			"В торрент-клиенте ограничьте скорость раздачи и количество пиров.",
		},
		Tip:        "Высокая нагрузка торрентов на слабых процессорах быстро исчерпывает буферы сетевых карт.",
		ExampleLog: "send: No buffer space available",
		Keywords:   []string{"enobufs", "105", "no buffer space available"},
		Match: func(text string) bool {
			return matchAny(text, "ENOBUFS", "errno 105", "error 105", "no buffer space available")
		},
	},
	{
		ID:       "errno_eisconn_106",
		Category: "Ядро Linux",
		Code:     "EISCONN",
		Title:    "Сокет уже подключен",
		Summary:  "Сокет уже подключен",
		Cause:    "Попытка вызвать connect() на уже установленном соединении.",
		ActionSteps: []string{
			"Штатное поведение в асинхронных сетевых клиентах.",
		},
		Tip:        "Программа продолжает работу по установленному соединению.",
		ExampleLog: "connect: Transport endpoint is already connected",
		Keywords:   []string{"eisconn", "106", "transport endpoint is already connected"},
		Match: func(text string) bool {
			return matchAny(text, "EISCONN", "errno 106", "error 106", "transport endpoint is already connected")
		},
	},
	{
		ID:       "errno_enotconn_107",
		Category: "Ядро Linux",
		Code:     "ENOTCONN",
		Title:    "Сокет не подключен к удаленному узлу",
		Summary:  "Сокет не подключен к удаленному узлу",
		Cause:    "Попытка отправки или чтения из сокета, для которого соединение ещё не было установлено или уже разорвано.",
		ActionSteps: []string{
			"Проверьте доступность удалённого сервера.",
			"Перезапустите туннель в интерфейсе AWG Manager.",
		},
		Tip:        "Указывает на отправку данных после разрыва связи.",
		ExampleLog: "send: Transport endpoint is not connected",
		Keywords:   []string{"enotconn", "107", "transport endpoint is not connected"},
		Match: func(text string) bool {
			return matchAny(text, "ENOTCONN", "errno 107", "error 107", "transport endpoint is not connected")
		},
	},
	{
		ID:       "errno_eshutdown_108",
		Category: "Ядро Linux",
		Code:     "ESHUTDOWN",
		Title:    "Отправка невозможна: сокет закрыт на передачу",
		Summary:  "Отправка невозможна: сокет закрыт на передачу",
		Cause:    "Попытка отправить данные после вызова shutdown(SHUT_WR).",
		ActionSteps: []string{
			"Программа штатно закрывает сетевой поток.",
		},
		Tip:        "Процесс должен закрыть сокет вызовом close().",
		ExampleLog: "send: Cannot send after transport endpoint shutdown",
		Keywords:   []string{"eshutdown", "108", "cannot send after transport endpoint shutdown"},
		Match: func(text string) bool {
			return matchAny(text, "ESHUTDOWN", "errno 108", "error 108", "cannot send after transport endpoint shutdown")
		},
	},
	{
		ID:       "errno_etoomanyrefs_109",
		Category: "Ядро Linux",
		Code:     "ETOOMANYREFS",
		Title:    "Слишком много ссылок на сокет",
		Summary:  "Слишком много ссылок на сокет",
		Cause:    "Превышен лимит ссылок при системном вызове splice().",
		ActionSteps: []string{
			"Внутреннее системное ограничение сетевой подсистемы ядра.",
		},
		Tip:        "Возникает при нулевом копировании данных между сокетами.",
		ExampleLog: "splice: Too many references",
		Keywords:   []string{"etoomanyrefs", "109", "too many references: cannot splice"},
		Match: func(text string) bool {
			return matchAny(text, "ETOOMANYREFS", "errno 109", "error 109", "too many references: cannot splice")
		},
	},
	{
		ID:       "errno_etimedout_110",
		Category: "Ядро Linux",
		Code:     "ETIMEDOUT",
		Title:    "Таймаут соединения (сервер не ответил вовремя)",
		Summary:  "Таймаут соединения (сервер не ответил вовремя)",
		Cause:    "Пакеты TCP SYN отправлены, но ответный SYN-ACK не поступил за отведённое время (сервер выключен, порт закрыт или заблокирован провайдером).",
		ActionSteps: []string{
			"Проверьте доступность вашего VPS-сервера (ping, SSH).",
			"Проверьте правильность порта и IP в конфигурации туннеля.",
			"Если сервер работает, но таймаут остаётся — порт или протокол блокируются ТСПУ/DPI провайдера.",
		},
		Tip:        "Попробуйте сменить порт туннеля на популярные (например, 443, 8443, 2083) или включить обфускацию.",
		ExampleLog: "dial tcp 198.51.100.1:443: i/o timeout (ETIMEDOUT)",
		Keywords:   []string{"etimedout", "110", "connection timed out"},
		Match: func(text string) bool {
			return matchAny(text, "ETIMEDOUT", "errno 110", "error 110", "connection timed out")
		},
	},
	{
		ID:       "errno_econnrefused_111",
		Category: "Ядро Linux",
		Code:     "ECONNREFUSED",
		Title:    "В соединении отказано (порт закрыт на удаленном узле)",
		Summary:  "В соединении отказано (порт закрыт на удаленном узле)",
		Cause:    "Удалённый сервер активен в сети, но на указанном порту не запущена никакая служба (сервер ответил TCP RST).",
		ActionSteps: []string{
			"Подключитесь к вашему VPS по SSH и проверьте статус службы (например, 'systemctl status amnezia-awg').",
			"Убедитесь, что фаервол на VPS (ufw / iptables) разрешает входящие соединения на этот порт.",
			"Проверьте правильность номера порта в настройках туннеля.",
		},
		Tip:        "ECONNREFUSED гарантирует, что интернет работает, но служба на самом сервере отключена.",
		ExampleLog: "dial tcp 198.51.100.1:51820: connect: connection refused",
		Keywords:   []string{"econnrefused", "111", "connection refused"},
		Match: func(text string) bool {
			return matchAny(text, "ECONNREFUSED", "errno 111", "error 111", "connection refused")
		},
	},
	{
		ID:       "errno_ehostdown_112",
		Category: "Ядро Linux",
		Code:     "EHOSTDOWN",
		Title:    "Удаленный узел выключен",
		Summary:  "Удаленный узел выключен",
		Cause:    "Сетевой маршрутизатор сообщил, что целевой сервер физически выключен или недоступен.",
		ActionSteps: []string{
			"Проверьте статус вашего виртуального сервера в панели хостинга.",
			"При необходимости выполните перезагрузку VPS в панели управления провайдера.",
		},
		Tip:        "Сообщение генерируется протоколом ICMP Destination Host Unreachable.",
		ExampleLog: "connect: Host is down",
		Keywords:   []string{"ehostdown", "112", "host is down"},
		Match: func(text string) bool {
			return matchAny(text, "EHOSTDOWN", "errno 112", "error 112", "host is down")
		},
	},
	{
		ID:       "errno_ehostunreach_113",
		Category: "Ядро Linux",
		Code:     "EHOSTUNREACH",
		Title:    "Нет маршрута к хосту",
		Summary:  "Нет маршрута к хосту",
		Cause:    "Промежуточный маршрутизатор в интернете не знает, куда отправить пакет к данному IP-адресу.",
		ActionSteps: []string{
			"Проверьте правильность IP-адреса сервера в настройках подключения.",
			"Проверьте трассировку маршрута в «Инструменты» -> «Диагностика» -> traceroute.",
		},
		Tip:        "Указывает на сбой маршрутизации в магистральной сети провайдера.",
		ExampleLog: "connect: no route to host",
		Keywords:   []string{"ehostunreach", "113", "no route to host"},
		Match: func(text string) bool {
			return matchAny(text, "EHOSTUNREACH", "errno 113", "error 113", "no route to host")
		},
	},
	{
		ID:       "errno_ealready_114",
		Category: "Ядро Linux",
		Code:     "EALREADY",
		Title:    "Операция уже выполняется",
		Summary:  "Операция уже выполняется",
		Cause:    "Попытка начать операцию на неблокирующем сокете, для которого предыдущая операция ещё не завершена.",
		ActionSteps: []string{
			"Дождитесь завершения подключения, служба автоматически обработает статус.",
		},
		Tip:        "Штатный код при неблокирующем подключении TCP.",
		ExampleLog: "connect: Operation already in progress",
		Keywords:   []string{"ealready", "114", "operation already in progress"},
		Match: func(text string) bool {
			return matchAny(text, "EALREADY", "errno 114", "error 114", "operation already in progress")
		},
	},
	{
		ID:       "errno_einprogress_115",
		Category: "Ядро Linux",
		Code:     "EINPROGRESS",
		Title:    "Операция выполняется в фоновом режиме",
		Summary:  "Операция выполняется в фоновом режиме",
		Cause:    "Неблокирующий сетевой сокет начал процедуру TCP handshake и ожидает ответа сервера.",
		ActionSteps: []string{
			"Это нормальное состояние при асинхронном сетевом программировании.",
		},
		Tip:        "О готовности сокета сигнализирует системный вызов epoll/poll.",
		ExampleLog: "connect: Operation now in progress",
		Keywords:   []string{"einprogress", "115", "operation now in progress"},
		Match: func(text string) bool {
			return matchAny(text, "EINPROGRESS", "errno 115", "error 115", "operation now in progress")
		},
	},
	{
		ID:       "errno_estale_116",
		Category: "Ядро Linux",
		Code:     "ESTALE",
		Title:    "Устаревший дескриптор файла",
		Summary:  "Устаревший дескриптор файла",
		Cause:    "Файл, открытый процессом, был удален или пересоздан на диске другой программой.",
		ActionSteps: []string{
			"Перезапустите службу, обратившуюся к устаревшему файлу.",
		},
		Tip:        "Типично для сетевых файловых систем NFS или ротации логов без повторного открытия.",
		ExampleLog: "open: Stale file handle",
		Keywords:   []string{"estale", "116", "stale file handle"},
		Match: func(text string) bool {
			return matchAny(text, "ESTALE", "errno 116", "error 116", "stale file handle")
		},
	},
	{
		ID:       "errno_euclean_117",
		Category: "Ядро Linux",
		Code:     "EUCLEAN",
		Title:    "Системный код ошибки ядра Linux: EUCLEAN (Structure needs cleaning)",
		Summary:  "Системный код ошибки ядра Linux: EUCLEAN (Structure needs cleaning)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 117: EUCLEAN).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 117 (EUCLEAN).",
		ExampleLog: "system error: EUCLEAN (117): Structure needs cleaning",
		Keywords:   []string{"euclean", "117", "structure needs cleaning"},
		Match: func(text string) bool {
			return matchAny(text, "EUCLEAN", "errno 117", "error 117", "structure needs cleaning")
		},
	},
	{
		ID:       "errno_enotnam_118",
		Category: "Ядро Linux",
		Code:     "ENOTNAM",
		Title:    "Системный код ошибки ядра Linux: ENOTNAM (Not a XENIX named type file)",
		Summary:  "Системный код ошибки ядра Linux: ENOTNAM (Not a XENIX named type file)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 118: ENOTNAM).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 118 (ENOTNAM).",
		ExampleLog: "system error: ENOTNAM (118): Not a XENIX named type file",
		Keywords:   []string{"enotnam", "118", "not a xenix named type file"},
		Match: func(text string) bool {
			return matchAny(text, "ENOTNAM", "errno 118", "error 118", "not a xenix named type file")
		},
	},
	{
		ID:       "errno_enavail_119",
		Category: "Ядро Linux",
		Code:     "ENAVAIL",
		Title:    "Системный код ошибки ядра Linux: ENAVAIL (No XENIX semaphores available)",
		Summary:  "Системный код ошибки ядра Linux: ENAVAIL (No XENIX semaphores available)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 119: ENAVAIL).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 119 (ENAVAIL).",
		ExampleLog: "system error: ENAVAIL (119): No XENIX semaphores available",
		Keywords:   []string{"enavail", "119", "no xenix semaphores available"},
		Match: func(text string) bool {
			return matchAny(text, "ENAVAIL", "errno 119", "error 119", "no xenix semaphores available")
		},
	},
	{
		ID:       "errno_eisnam_120",
		Category: "Ядро Linux",
		Code:     "EISNAM",
		Title:    "Системный код ошибки ядра Linux: EISNAM (Is a named type file)",
		Summary:  "Системный код ошибки ядра Linux: EISNAM (Is a named type file)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 120: EISNAM).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 120 (EISNAM).",
		ExampleLog: "system error: EISNAM (120): Is a named type file",
		Keywords:   []string{"eisnam", "120", "is a named type file"},
		Match: func(text string) bool {
			return matchAny(text, "EISNAM", "errno 120", "error 120", "is a named type file")
		},
	},
	{
		ID:       "errno_eremoteio_121",
		Category: "Ядро Linux",
		Code:     "EREMOTEIO",
		Title:    "Системный код ошибки ядра Linux: EREMOTEIO (Remote I/O error)",
		Summary:  "Системный код ошибки ядра Linux: EREMOTEIO (Remote I/O error)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 121: EREMOTEIO).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 121 (EREMOTEIO).",
		ExampleLog: "system error: EREMOTEIO (121): Remote I/O error",
		Keywords:   []string{"eremoteio", "121", "remote i/o error"},
		Match: func(text string) bool {
			return matchAny(text, "EREMOTEIO", "errno 121", "error 121", "remote i/o error")
		},
	},
	{
		ID:       "errno_edquot_122",
		Category: "Ядро Linux",
		Code:     "EDQUOT",
		Title:    "Превышена дисковая квота пользователя",
		Summary:  "Превышена дисковая квота пользователя",
		Cause:    "Пользователь исчерпал назначенную квоту дискового пространства.",
		ActionSteps: []string{
			"Удалите ненужные файлы в каталоге пользователя.",
			"Проверьте квоты диска.",
		},
		Tip:        "В Entware квоты обычно отключены, если не настроены специально.",
		ExampleLog: "write: Disk quota exceeded",
		Keywords:   []string{"edquot", "122", "disk quota exceeded"},
		Match: func(text string) bool {
			return matchAny(text, "EDQUOT", "errno 122", "error 122", "disk quota exceeded")
		},
	},
	{
		ID:       "errno_enomedium_123",
		Category: "Ядро Linux",
		Code:     "ENOMEDIUM",
		Title:    "Носитель данных не обнаружен",
		Summary:  "Носитель данных не обнаружен",
		Cause:    "Попытка обращения к пустому слоту карты памяти или отключенному USB-диску.",
		ActionSteps: []string{
			"Проверьте надежность подключения USB-накопителя в разъем роутера.",
			"В панели Keenetic убедитесь, что USB-диск распознан.",
		},
		Tip:        "Плохой контакт в разъеме USB или недостаток питания диска вызывают отмонтирование.",
		ExampleLog: "/dev/sda: No medium found",
		Keywords:   []string{"enomedium", "123", "no medium found"},
		Match: func(text string) bool {
			return matchAny(text, "ENOMEDIUM", "errno 123", "error 123", "no medium found")
		},
	},
	{
		ID:       "errno_emediumtype_124",
		Category: "Ядро Linux",
		Code:     "EMEDIUMTYPE",
		Title:    "Системный код ошибки ядра Linux: EMEDIUMTYPE (Wrong medium type)",
		Summary:  "Системный код ошибки ядра Linux: EMEDIUMTYPE (Wrong medium type)",
		Cause:    "Низкоуровневый системный сбой ядра или подсистемы ввода-вывода (код ошибки POSIX 124: EMEDIUMTYPE).",
		ActionSteps: []string{
			"Проверьте системный журнал роутера в «Инструменты» -> «Журнал».",
			"Если служба аварийно завершилась, перезапустите её в «Инструменты» -> «Службы».",
		},
		Tip:        "Стандартный код ошибки POSIX Errno 124 (EMEDIUMTYPE).",
		ExampleLog: "system error: EMEDIUMTYPE (124): Wrong medium type",
		Keywords:   []string{"emediumtype", "124", "wrong medium type"},
		Match: func(text string) bool {
			return matchAny(text, "EMEDIUMTYPE", "errno 124", "error 124", "wrong medium type")
		},
	},
	{
		ID:       "errno_ecanceled_125",
		Category: "Ядро Linux",
		Code:     "ECANCELED",
		Title:    "Асинхронная операция отменена",
		Summary:  "Асинхронная операция отменена",
		Cause:    "Асинхронный сетевой запрос или задача были отменены по таймауту или действию пользователя.",
		ActionSteps: []string{
			"Штатно возникает при отмене запроса или закрытии вкладки.",
		},
		Tip:        "Операция отменена приложением через context.Cancel() или aio_cancel().",
		ExampleLog: "operation canceled",
		Keywords:   []string{"ecanceled", "125", "operation canceled"},
		Match: func(text string) bool {
			return matchAny(text, "ECANCELED", "errno 125", "error 125", "operation canceled")
		},
	},
	{
		ID:       "errno_enokey_126",
		Category: "Ядро Linux",
		Code:     "ENOKEY",
		Title:    "Требуемый криптографический ключ недоступен",
		Summary:  "Требуемый криптографический ключ недоступен",
		Cause:    "В хранилище ключей ядра (kernel keyring) отсутствует запрошенный ключ шифрования.",
		ActionSteps: []string{
			"Проверьте настройки ключей шифрования в туннеле.",
		},
		Tip:        "Связано с модулями шифрования ядра Linux Crypto API.",
		ExampleLog: "keyctl: Required key not available",
		Keywords:   []string{"enokey", "126", "required key not available"},
		Match: func(text string) bool {
			return matchAny(text, "ENOKEY", "errno 126", "error 126", "required key not available")
		},
	},
	{
		ID:       "errno_ekeyexpired_127",
		Category: "Ядро Linux",
		Code:     "EKEYEXPIRED",
		Title:    "Срок действия криптографического ключа истек",
		Summary:  "Срок действия криптографического ключа истек",
		Cause:    "Истек срок действия ключа шифрования, SSL/TLS сертификата или токена авторизации.",
		ActionSteps: []string{
			"Обновите конфигурацию или файл подписки в разделе «Подписки».",
			"Убедитесь, что системное время на роутере установлено корректно.",
		},
		Tip:        "Если часы роутера сбросились на 1970 год, все ключи и сертификаты будут считаться просроченными.",
		ExampleLog: "tls: certificate has expired",
		Keywords:   []string{"ekeyexpired", "127", "key has expired"},
		Match: func(text string) bool {
			return matchAny(text, "EKEYEXPIRED", "errno 127", "error 127", "key has expired")
		},
	},
	{
		ID:       "errno_ekeyrevoked_128",
		Category: "Ядро Linux",
		Code:     "EKEYREVOKED",
		Title:    "Криптографический ключ был отозван",
		Summary:  "Криптографический ключ был отозван",
		Cause:    "Ключ или сертификат был внесен в список отозванных (CRL / OCSP Revocation).",
		ActionSteps: []string{
			"Выпустите новый ключ или запросите обновленный профиль подключения у администратора.",
		},
		Tip:        "Отозванные ключи отвергаются криптографическим стеком.",
		ExampleLog: "x509: certificate revoked",
		Keywords:   []string{"ekeyrevoked", "128", "key has been revoked"},
		Match: func(text string) bool {
			return matchAny(text, "EKEYREVOKED", "errno 128", "error 128", "key has been revoked")
		},
	},
	{
		ID:       "errno_ekeyrejected_129",
		Category: "Ядро Linux",
		Code:     "EKEYREJECTED",
		Title:    "Ключ отвергнут удаленным сервисом",
		Summary:  "Ключ отвергнут удаленным сервисом",
		Cause:    "Сервер отклонил предоставленный ключ авторизации как недействительный.",
		ActionSteps: []string{
			"Проверьте правильность открытого и закрытого ключа (PublicKey / PrivateKey) в настройках туннеля.",
			"Убедитесь, что в ключе нет лишних пробелов или символов перевода строки.",
		},
		Tip:        "В AWG Manager используйте встроенную проверку валидности base64-ключей.",
		ExampleLog: "key rejected by remote server",
		Keywords:   []string{"ekeyrejected", "129", "key was rejected by service"},
		Match: func(text string) bool {
			return matchAny(text, "EKEYREJECTED", "errno 129", "error 129", "key was rejected by service")
		},
	},
	{
		ID:       "errno_eownerdead_130",
		Category: "Ядро Linux",
		Code:     "EOWNERDEAD",
		Title:    "Процесс-владелец мьютекса аварийно завершился",
		Summary:  "Процесс-владелец мьютекса аварийно завершился",
		Cause:    "Процесс упал, удерживая робастный мьютекс ядра.",
		ActionSteps: []string{
			"Служба должна восстановить консистентность состояния или перезапуститься.",
		},
		Tip:        "Предотвращает бесконечные зависания при падении процессов в общей памяти.",
		ExampleLog: "pthread_mutex_lock: Owner died",
		Keywords:   []string{"eownerdead", "130", "owner died"},
		Match: func(text string) bool {
			return matchAny(text, "EOWNERDEAD", "errno 130", "error 130", "owner died")
		},
	},
	{
		ID:       "errno_enotrecoverable_131",
		Category: "Ядро Linux",
		Code:     "ENOTRECOVERABLE",
		Title:    "Состояние мьютекса не подлежит восстановлению",
		Summary:  "Состояние мьютекса не подлежит восстановлению",
		Cause:    "Разделяемый ресурс перешел в невосстановимое состояние после аварии процесса.",
		ActionSteps: []string{
			"Перезапустите службу в «Инструменты» -> «Службы».",
		},
		Tip:        "Требует полного сброса и перезапуска связки процессов.",
		ExampleLog: "pthread_mutex_lock: State not recoverable",
		Keywords:   []string{"enotrecoverable", "131", "state not recoverable"},
		Match: func(text string) bool {
			return matchAny(text, "ENOTRECOVERABLE", "errno 131", "error 131", "state not recoverable")
		},
	},
	{
		ID:       "errno_erfkill_132",
		Category: "Ядро Linux",
		Code:     "ERFKILL",
		Title:    "Беспроводной интерфейс аппаратно или программно заблокирован",
		Summary:  "Беспроводной интерфейс аппаратно или программно заблокирован",
		Cause:    "Попытка передачи пакета через Wi-Fi интерфейс, отключенный выключателем или командой rfkill.",
		ActionSteps: []string{
			"В веб-интерфейсе Keenetic перейдите в «Мои сети и Wi-Fi» и включите Wi-Fi сеть.",
		},
		Tip:        "Подсистема rfkill блокирует радиомодули для экономии энергии или безопасности.",
		ExampleLog: "wifi: Operation not possible due to RF-kill",
		Keywords:   []string{"erfkill", "132", "operation not possible due to rf-kill"},
		Match: func(text string) bool {
			return matchAny(text, "ERFKILL", "errno 132", "error 132", "operation not possible due to rf-kill")
		},
	},
	{
		ID:       "errno_ehwpoison_133",
		Category: "Ядро Linux",
		Code:     "EHWPOISON",
		Title:    "Аппаратный сбой ячейки оперативной памяти (ECC)",
		Summary:  "Аппаратный сбой ячейки оперативной памяти (ECC)",
		Cause:    "Ядро обнаружило неисправность микросхемы оперативной памяти роутера.",
		ActionSteps: []string{
			"Перезагрузите роутер.",
			"При регулярном повторении проверьте стабильность электропитания роутера.",
		},
		Tip:        "Аппаратный сбой чипа памяти требует диагностики оборудования.",
		ExampleLog: "kernel: memory failure: page is poisoned",
		Keywords:   []string{"ehwpoison", "133", "memory page has hardware error"},
		Match: func(text string) bool {
			return matchAny(text, "EHWPOISON", "errno 133", "error 133", "memory page has hardware error")
		},
	},
}

// LookupKnownError производит строгий поиск ошибки по сигнатурам и регулярным выражениям.
// Приоритет: сначала специфичные ошибки приложений и KeeneticOS, затем общие коды ядра Linux Errno.
func LookupKnownError(text string) *ErrorRecord {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}

	// 1. Поиск по специфичным ошибкам приложений (KeeneticOS, AmneziaWG, Mihomo, Sing-box, DNS, Подписки, System)
	for i := range FullErrorDatabase {
		if FullErrorDatabase[i].Category != "Ядро Linux" {
			if FullErrorDatabase[i].Match != nil && FullErrorDatabase[i].Match(trimmed) {
				return &FullErrorDatabase[i]
			}
		}
	}

	// 2. Поиск по общим кодам ядра Linux Errno
	for i := range FullErrorDatabase {
		if FullErrorDatabase[i].Category == "Ядро Linux" {
			if FullErrorDatabase[i].Match != nil && FullErrorDatabase[i].Match(trimmed) {
				return &FullErrorDatabase[i]
			}
		}
	}

	return nil
}

// FallbackHeuristicAnalysis выполняет интеллектуальный разбор неизвестных кодов ошибок.
func FallbackHeuristicAnalysis(text string) (string, bool) {
	// Распознавание неизвестного кода KeeneticOS 0xcffdXXXX
	if m := rxCFFD.FindStringSubmatch(text); len(m) > 1 {
		codeHex := strings.ToLower(m[1])
		return fmt.Sprintf("⚠️ **Обнаружен системный код KeeneticOS: `0xcffd%s`**\n\n"+
			"**🔍 Что произошло простыми словами:**\n"+
			"Внутренний диспетчер конфигурации роутера (Core::Configurator) отклонил сетевую операцию.\n\n"+
			"**⚠️ Причина:**\n"+
			"Команда настройки интерфейса, адреса или политики не смогла примениться в ядре роутера (код подсистемы NDMS: `%s`).\n\n"+
			"**🖱️ Куда тыкнуть мышкой (пошаговая инструкция):**\n"+
			"1. Перейдите на вкладку **«Туннели»** в AWG Manager.\n"+
			"2. Откройте настройки туннеля (иконка карандаша).\n"+
			"3. Проверьте, чтобы подсеть туннеля (например, `10.x.x.x`) не пересекалась с домашней сетью Keenetic.\n"+
			"4. Сохраните туннель и выключите/включите переключатель заново.\n\n"+
			"💡 **Совет:** Посмотреть подробности отказа можно в веб-интерфейсе Keenetic в меню *«Управление» -> «Диагностика» -> «Журнал сообщений»*.", codeHex, codeHex), true
	}

	// Распознавание общих сетевых ошибок Linux
	low := strings.ToLower(text)
	if strings.Contains(low, "connection refused") {
		return "⚠️ **В соединении отказано (Connection refused)**\n\n" +
			"**🔍 Что произошло простыми словами:**\n" +
			"Роутер успешно достучался до целевого сервера, но на указанном порту не запущена никакая служба.\n\n" +
			"**🖱️ Куда тыкнуть мышкой (пошаговая инструкция):**\n" +
			"1. Проверьте правильность номера порта в настройках туннеля или прокси.\n" +
			"2. Подключитесь к вашему VPS по SSH и проверьте статус службы (например, `systemctl status amnezia-awg`).\n" +
			"3. Убедитесь, что фаервол VPS разрешает входящие соединения на этот порт.", true
	}

	if strings.Contains(low, "timeout") || strings.Contains(low, "timed out") {
		return "⚠️ **Таймаут сетевого соединения (Timeout)**\n\n" +
			"**🔍 Что произошло простыми словами:**\n" +
			"Роутер отправил запрос на удаленный сервер, но ответ не пришел за отведенное время.\n\n" +
			"**🖱️ Куда тыкнуть мышкой (пошаговая инструкция):**\n" +
			"1. Проверьте, есть ли интернет на роутере в меню *«Сетевые правила»*.\n" +
			"2. Проверьте доступность IP-адреса вашего сервера (ping в разделе «Диагностика»).\n" +
			"3. Если IP пингуется, а туннель не отвечает — возможно, провайдер блокирует протокол или порт. Смените порт подключения или включите обфускацию.", true
	}

	if strings.Contains(low, "syntax") || strings.Contains(low, "parsing error") || strings.Contains(low, "unexpected token") {
		return "⚠️ **Синтаксическая ошибка в конфигурации**\n\n" +
			"**🔍 Что произошло простыми словами:**\n" +
			"Служба не смогла разобрать синтаксис конфигурационного файла из-за некорректных символов или структуры.\n\n" +
			"**🖱️ Куда тыкнуть мышкой (пошаговая инструкция):**\n" +
			"1. Перейдите на вкладку туннелей или маршрутизации.\n" +
			"2. Откройте редактирование конфигурации.\n" +
			"3. Проверьте правильность разметки и удалите недопустимые символы.\n" +
			"4. Сохраните конфигурацию.", true
	}

	return "", false
}

// FormatErrorCard форматирует карточку ошибки в наглядный Markdown для чата ассистента.
func FormatErrorCard(err *ErrorRecord, rawSnippet string) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("📖 **%s** `[%s]`\n\n", err.Title, err.Code))
	b.WriteString(fmt.Sprintf("**🔍 Что произошло простыми словами:**\n%s\n\n", err.Summary))
	b.WriteString(fmt.Sprintf("**⚠️ Причина сбоя:**\n%s\n\n", err.Cause))

	if len(err.ActionSteps) > 0 {
		b.WriteString("**🖱️ Куда тыкнуть мышкой (пошаговая инструкция):**\n")
		for i, step := range err.ActionSteps {
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, step))
		}
		b.WriteString("\n")
	}

	if err.Tip != "" {
		b.WriteString(fmt.Sprintf("💡 **Совет:** %s\n\n", err.Tip))
	}

	if rawSnippet != "" && rawSnippet != err.Code {
		b.WriteString(fmt.Sprintf("📋 *Строка из лога:* `%s`", rawSnippet))
	}

	return strings.TrimSpace(b.String())
}

// SearchErrors выполняет поиск по базе ошибок по текстовому запросу и опциональной категории.
func SearchErrors(query, category string) []ErrorRecord {
	q := strings.ToLower(strings.TrimSpace(query))
	cat := strings.TrimSpace(category)

	var results []ErrorRecord
	for _, rec := range FullErrorDatabase {
		if cat != "" && cat != "Все" && !strings.EqualFold(rec.Category, cat) {
			continue
		}
		if q == "" {
			results = append(results, rec)
			continue
		}

		matched := false
		if strings.Contains(strings.ToLower(rec.Code), q) ||
			strings.Contains(strings.ToLower(rec.Title), q) ||
			strings.Contains(strings.ToLower(rec.Summary), q) ||
			strings.Contains(strings.ToLower(rec.Cause), q) ||
			strings.Contains(strings.ToLower(rec.Tip), q) ||
			strings.Contains(strings.ToLower(rec.ExampleLog), q) {
			matched = true
		} else {
			for _, kw := range rec.Keywords {
				if strings.Contains(strings.ToLower(kw), q) {
					matched = true
					break
				}
			}
		}

		if matched {
			results = append(results, rec)
		}
	}

	return results
}

// GetAllCategories возвращает уникальный список доступных категорий ошибок.
func GetAllCategories() []string {
	return []string{
		"Все",
		"KeeneticOS (NDMS)",
		"AmneziaWG",
		"Mihomo",
		"Sing-box",
		"Подписки и прокси",
		"DNS и Маршрутизация",
		"Система и Entware",
		"Ядро Linux",
	}
}
