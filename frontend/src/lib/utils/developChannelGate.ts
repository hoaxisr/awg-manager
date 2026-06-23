export const DEVELOP_CHANNEL_LOCKOUT_KEY = 'awg-manager-develop-channel-lockout-until';
export const DEVELOP_CHANNEL_QUIZ_PASSED_KEY = 'awg-manager-develop-channel-quiz-passed';
export const DEVELOP_CHANNEL_QUIZ_SIZE = 7;
export const DEVELOP_CHANNEL_QUIZ_MAX_WRONG = 2;
/** @deprecated Use {@link DEVELOP_CHANNEL_QUIZ_SIZE} and {@link DEVELOP_CHANNEL_QUIZ_MAX_WRONG}. */
export const DEVELOP_CHANNEL_QUIZ_PASS_MIN =
	DEVELOP_CHANNEL_QUIZ_SIZE - DEVELOP_CHANNEL_QUIZ_MAX_WRONG;
export const DEVELOP_CHANNEL_QUIZ_QUESTION_MS = 30 * 1000;
export const DEVELOP_CHANNEL_LOCKOUT_MS = 30 * 60 * 1000;
/** Shorter lockout in `yarn dev:mock` so the quiz flow can be exercised locally. */
export const DEVELOP_CHANNEL_LOCKOUT_MOCK_MS = 30 * 1000;
export const DEVELOP_CHANNEL_DOCS_URL = 'https://awgm.hoaxisr.ru/';

/** Shown as the only answer after copy/Ctrl+C during the quiz. */
export const DEVELOP_CHANNEL_COPY_CHEAT_OPTIONS = [
	'Я попытался скопировать вопрос, чтобы считерить, простите меня',
	'Наверное, это не моё, я случайно сюда попал...',
	'Ctrl+C — мой любимый способ подготовки к альфе',
	'Копирую в блокнот для друга, который тоже на develop',
	'Сейчас загуглю, это займёт секунд тридцать',
	'Я не читер, я архиватор вопросов open source',
	'Думал, выделение текста даёт бонусные очки',
	'Ладно, поймали — но issue всё равно оформлю одной строкой',
	'Это была проверка буфера обмена, всё работает',
	'Шёл за стабильным каналом, промахнулся мимо кнопки',
] as const;

export function pickCopyCheatOption(
	pool: readonly string[] = DEVELOP_CHANNEL_COPY_CHEAT_OPTIONS,
): string {
	return pool[Math.floor(Math.random() * pool.length)] ?? pool[0];
}

export type DevelopQuizQuestion = {
	id: string;
	text: string;
	options: string[];
	correctIndex: number;
};

/** Full question bank for the develop-channel gate quiz. */
export const DEVELOP_CHANNEL_QUIZ_QUESTIONS: DevelopQuizQuestion[] = [
	{
		id: 'tun-interface',
		text: 'Что такое tun-интерфейс?',
		options: [
			'Виртуальный сетевой интерфейс для туннелирования трафика на L3',
			'Нужно для блокировки рекламы на smart-чайнике',
			'Сокращение от «туннельный ундервеар»',
			'Драйвер для Wi-Fi антенн',
		],
		correctIndex: 0,
	},
	{
		id: 'wg-handshake',
		text: 'Что делает WireGuard при хендшейке?',
		options: [
			'Устанавливает эфемерные ключи Diffie-Hellman и согласует сессию',
			'Пожимает руку серверу и ждёт ответа',
			'Отправляет ping и ждёт 3 секунды',
			'Шифрует пакет и молится',
		],
		correctIndex: 0,
	},
	{
		id: 'allowed-ips-full-tunnel',
		text: 'Для чего нужен AllowedIPs = 0.0.0.0/0?',
		options: [
			'Направить весь трафик через туннель (full tunnel)',
			'Разрешить подключение с любого IP',
			'Потому что так написано в TikTok про VPN',
			'Отключить файрвол',
			'Это значит «разрешить всем всё» — ставь смело',
		],
		correctIndex: 0,
	},
	{
		id: 'cidr-24',
		text: 'Что такое CIDR нотация /24?',
		options: [
			'Маска подсети 255.255.255.0, 256 адресов в сети',
			'24-битный ключ шифрования',
			'Это включено по умолчанию в KeeneticOS 42 Ultimate',
			'Скорость канала 24 Мбит/с',
			'Версия протокола IPv6',
		],
		correctIndex: 0,
	},
	{
		id: 'dns-leak',
		text: 'Что такое DNS leak?',
		options: [
			'DNS-запросы утекают за пределы туннеля, раскрывая реальный провайдер',
			'Это когда роутер делает вид, что понял вопрос',
			'Утечка паролей через DNS',
			'Когда DNS-сервер слишком медленный',
			'Когда забыл оплатить домен',
		],
		correctIndex: 0,
	},
	{
		id: 'udp-vs-tcp',
		text: 'Чем отличается UDP от TCP?',
		options: [
			'UDP без гарантий доставки, TCP с подтверждением — WireGuard использует UDP',
			'UDP быстрее потому что американский',
			'TCP — для текста, UDP — для картинок',
			'Разные порты, всё остальное одинаково',
		],
		correctIndex: 0,
	},
	{
		id: 'mtu-tunnel',
		text: 'Что такое MTU и почему это важно для туннелей?',
		options: [
			'Максимальный размер пакета; туннель добавляет overhead, поэтому MTU нужно занижать',
			'Настройка скорости в мегабитах',
			'Потому что MTU расшифровывается как Maximum TikTok Usage',
			'Ничего важного, можно оставить дефолт',
		],
		correctIndex: 0,
	},
	{
		id: 'persistent-keepalive',
		text: 'Что означает PersistentKeepalive в WireGuard?',
		options: [
			'Периодические пакеты, чтобы NAT-сессия не истекала при простое UDP-туннеля',
			'Без этого WireGuard не шифрует эмодзи',
			'Автоматический перезапуск интерфейса каждые 25 секунд',
			'Режим «не отключать Wi-Fi ночью»',
		],
		correctIndex: 0,
	},
	{
		id: 'split-tunnel',
		text: 'Что такое split tunnel?',
		options: [
			'Часть трафика идёт через VPN, часть — напрямую через провайдера',
			'Различные политики для разных подсетей',
			'Два параллельных WireGuard на одном порту',
			'Режим, когда DNS и IP всегда идут разными путями без настройки',
		],
		correctIndex: 0,
	},
	{
		id: 'wg-peer',
		text: 'Что описывает секция [Peer] в конфиге WireGuard?',
		options: [
			'Удалённую сторону туннеля: ключ, endpoint, AllowedIPs и параметры сессии',
			'Только имя пользователя (пира) в VPN-сервисе',
			'Список заблокированных доменов',
			'Пароль администратора роутера',
		],
		correctIndex: 0,
	},
	{
		id: 'keepalive-behind-nat',
		text: 'Зачем на клиенте за домашним NAT иногда включают keepalive в WireGuard?',
		options: [
			'Чтобы роутер не закрыл «забытый» UDP-туннель при долгом простое',
			'Это настройка в «Эксперт» → «Магия» → «Ещё магия»',
			'Чтобы отключить IPv6 на роутере',
			'Потому что без этого WireGuard не шифрует трафик',
			'Чтобы ускорить Wi-Fi до 10 Гбит/с',
		],
		correctIndex: 0,
	},
	{
		id: 'kill-switch',
		text: 'Что делает kill switch в VPN-контексте?',
		options: [
			'Блокирует трафик вне туннеля при обрыве VPN, чтобы не утекал «в обход»',
			'Выключает роутер при первой ошибке handshake',
			'Включает режим «всегда full tunnel» для smart-чайника',
			'Удаляет все пиры из конфига',
			'Переводит канал обновлений в stable',
		],
		correctIndex: 0,
	},
	{
		id: 'public-key',
		text: 'PublicKey в WireGuard — это…',
		options: [
			'Публичная часть пары Curve25519 для аутентификации пира',
			'Так устроен develop-канал в параллельной вселенной',
			'Серийный номер роутера',
			'Открытый порт UDP, записанный как строка',
			'Пароль от веб-панели в Base64',
		],
		correctIndex: 0,
	},
	{
		id: 'endpoint',
		text: 'Endpoint в конфиге WireGuard — это…',
		options: [
			'Адрес и UDP-порт сервера, куда клиент отправляет пакеты туннеля',
			'Ответ подгружается через маршрут служебных загрузок',
			'Имя Wi-Fi сети',
			'Путь к файлу wg0.conf на диске',
			'URL страницы статуса в браузере',
		],
		correctIndex: 0,
	},
	{
		id: 'preshared-key',
		text: 'Зачем иногда добавляют PresharedKey (PSK)?',
		options: [
			'Дополнительный симметричный ключ поверх DH для защиты от будущих квантовых атак (опционально)',
			'Чтобы ускорить handshake в 10 раз — так дядя из ютуба сказал',
			'Чтобы заменить DNS на 1.1.1.1',
			'Чтобы отключить шифрование для отладки',
			'Роутер сам знает — лучше не трогать',
		],
		correctIndex: 0,
	},
	{
		id: 'handshake-rtt',
		text: 'Что показывает «latest handshake» в wg show?',
		options: [
			'Когда последний раз успешно согласовали сессию с пиром',
			'Скорость загрузки за последний час',
			'Время последнего пинга пира',
			'Количество ошибок DNS',
			'Версию прошивки роутера',
		],
		correctIndex: 0,
	},
	{
		id: 'awg-obfuscation',
		text: 'Чем AmneziaWG (AWG) принципиально дополняет классический WireGuard?',
		options: [
			'Добавляет обфускацию метаданных/пакетов поверх WG, усложняя DPI-блокировки',
			'Заменяет UDP на FTP',
			'Убирает необходимость в ключах',
			'Работает только без tun-интерфейса',
		],
		correctIndex: 0,
	},
	{
		id: 'issue-attachments',
		text: 'Что уместно приложить к GitHub Issue по багу AWGM?',
		options: [
			'Версию AWGM, шаги воспроизведения, логи/скриншоты; при сбоях UI — HAR или экспорт Network из DevTools',
			'Достаточно одной фразы «не работает» без деталей',
			'Пароль администратора роутера и полный wg0.conf с ключами',
			'Случайный скриншот рабочего стола — главное, что картинка есть',
		],
		correctIndex: 0,
	},
	{
		id: 'bad-situation-report',
		text: 'После обновления на develop «всё плохо». Что делаете в первую очередь?',
		options: [
			'Оформляете полноценный GitHub Issue: версия, шаги, ожидание/факт, вложения',
			'Пишете автору в личку Telegram — так быстрее, чем issue',
			'Ждёте, пока кто-то сам догадается по вашему сообщению в чате без ссылок',
			'Сразу только откат прошивки роутера, issue не нужен',
		],
		correctIndex: 0,
	},
	{
		id: 'devtools-network',
		text: 'Как открыть в браузере вкладку сетевых запросов для отладки AWGM UI?',
		options: [
			'F12 (или Ctrl+Shift+I / Cmd+Option+I) → вкладка Network',
			'Ctrl+P → печать страницы → там все запросы',
			'Настройки Keenetic → «Сетевые запросы браузера»',
			'Диспетчер задач → вкладка «Производительность»',
		],
		correctIndex: 0,
	},
	{
		id: 'issue-channel',
		text: 'Куда правильно отправить баг по develop-сборке AWGM?',
		options: [
			'В GitHub Issues репозитория проекта — структурированно и с вложениями',
			'В личные сообщения автору в Telegram',
			'В общий чат одним сообщением «почините» без шагов',
			'В комментарии к случайному посту в соцсетях',
		],
		correctIndex: 0,
	},
	{
		id: 'issue-minimum',
		text: 'Что обязательно должно быть в описании баг-репорта?',
		options: [
			'Что сломалось, что ожидали, версия AWGM и шаги «как воспроизвести»',
			'Только «Срочно!!!» и эмодзи 🔥',
			'Только модель роутера — остальное и так ясно',
			'Ничего: заголовок «баг» достаточен',
		],
		correctIndex: 0,
	},
	{
		id: 'awgm-usage-level-advanced',
		text: 'С какого уровня использования в AWGM доступны Sing-box, подписки, серверы и мониторинг?',
		options: [
			'С «Расширенного» (advanced)',
			'С «Базового»',
			'Только с «Продвинутого» (expert)',
			'Только после переключения на канал develop',
		],
		correctIndex: 0,
	},
	{
		id: 'awgm-download-route',
		text: 'Зачем в настройках AWGM задаётся маршрут «служебных загрузок»?',
		options: [
			'Чтобы обновления AWGM, списки DNSRoute и geo-файлы шли через выбранный outbound',
			'Только для скачивания прошивки Keenetic',
			'Чтобы весь трафик LAN автоматически шёл через VPN',
			'Чтобы отключить проверку обновлений',
		],
		correctIndex: 0,
	},
	{
		id: 'awgm-tunnels-home',
		text: 'Где в интерфейсе AWGM собраны AWG, системные WG, Sing-box и подписки?',
		options: [
			'На главной «Туннели» (/) — вкладками, без отдельных пунктов Sing-box/Подписки в шапке',
			'В разделе «Подписки» (Settings / Subscriptions)',
			'В шапке четыре отдельных раздела: AWG, Sing-box, Подписки и Система',
			'Только в «Настройки» → «Интеграции»',
			'В «Инструменты» → «Конфиг AWG»',
		],
		correctIndex: 0,
	},
	{
		id: 'awgm-ndms-proxy',
		text: 'Что делает переключатель «NDMS Proxy для sing-box» в настройках AWGM?',
		options: [
			'Включает привязку sing-box к интерфейсам ProxyN в NDMS (нужен компонент proxy)',
			'Переводит все AWG-туннели на ядро WireGuard Linux',
			'Иначе компонент proxy в NDMS обижается и Wi-Fi краснеет',
			'Заменяет sing-box на AmneziaWG',
			'Отключает DNS-маршруты NDMS',
		],
		correctIndex: 0,
	},
	{
		id: 'awgm-monitoring-pingcheck',
		text: 'Где в AWGM в основном настраивается ping-check для AWG-туннелей?',
		options: [
			'На странице «Мониторинг» — через матрицу (клик по ячейке/имени туннеля)',
			'Только одним глобальным тогглом в «Настройки»',
			'Только правкой wg0.conf на диске роутера',
			'В «Терминал» командой ping 8.8.8.8',
		],
		correctIndex: 0,
	},
	{
		id: 'awgm-ndms-dns-ipset',
		text: 'Почему NDMS DNS-маршрутизация на Keenetic считается ненадёжной в «тяжёлых» сценариях?',
		options: [
			'Роутер при перезагрузке или смене политик может сбрасывать ipset\'ы — доменные маршруты ломаются',
			'Так рекомендует сосед с форума, проверено на даче',
			'ipset на Keenetic никогда не очищается и всегда растёт без лимита',
			'NDMS DNS работает только если клиент в отдельной VLAN без политик',
		],
		correctIndex: 0,
	},
	{
		id: 'awgm-restart-init',
		text: 'Какой командой на роутере перезапустить сервис AWG Manager (Entware)?',
		options: [
			'/opt/etc/init.d/S99awg-manager restart',
			'systemctl restart awgm',
			'reboot',
			'killall -9 awg-manager && rm -rf /opt',
		],
		correctIndex: 0,
	},
];

function shuffleInPlace<T>(items: T[]): T[] {
	for (let i = items.length - 1; i > 0; i--) {
		const j = Math.floor(Math.random() * (i + 1));
		[items[i], items[j]] = [items[j], items[i]];
	}
	return items;
}

export function pickDevelopQuizQuestions(
	count = DEVELOP_CHANNEL_QUIZ_SIZE,
	pool: DevelopQuizQuestion[] = DEVELOP_CHANNEL_QUIZ_QUESTIONS,
): DevelopQuizQuestion[] {
	const copy = [...pool];
	shuffleInPlace(copy);
	return copy.slice(0, Math.min(count, copy.length));
}

/** Shuffles answer options and updates {@link DevelopQuizQuestion.correctIndex}. */
export function shuffleQuestionOptions(question: DevelopQuizQuestion): DevelopQuizQuestion {
	const indexed = question.options.map((text, index) => ({ text, index }));
	shuffleInPlace(indexed);
	return {
		...question,
		options: indexed.map((entry) => entry.text),
		correctIndex: indexed.findIndex((entry) => entry.index === question.correctIndex),
	};
}

/**
 * Picks a random question set and shuffles both question order and options.
 * Call once per quiz start so each attempt gets a fresh layout.
 */
export function prepareDevelopQuizSession(
	count = DEVELOP_CHANNEL_QUIZ_SIZE,
	pool: DevelopQuizQuestion[] = DEVELOP_CHANNEL_QUIZ_QUESTIONS,
): DevelopQuizQuestion[] {
	const picked = pickDevelopQuizQuestions(count, pool);
	shuffleInPlace(picked);
	return picked.map(shuffleQuestionOptions);
}

export function scoreDevelopQuiz(
	questions: DevelopQuizQuestion[],
	answers: Record<string, number | undefined>,
	cheatCaughtByQuestionId: Record<string, string> = {},
): number {
	let correct = 0;
	for (const q of questions) {
		if (cheatCaughtByQuestionId[q.id]) continue;
		if (answers[q.id] === q.correctIndex) correct++;
	}
	return correct;
}

export function isDevelopQuizPassed(
	correctCount: number,
	total = DEVELOP_CHANNEL_QUIZ_SIZE,
	maxWrong = DEVELOP_CHANNEL_QUIZ_MAX_WRONG,
): boolean {
	return correctCount >= total - maxWrong;
}

export function readDevelopChannelLockoutUntil(): number | null {
	if (typeof localStorage === 'undefined') return null;
	const raw = localStorage.getItem(DEVELOP_CHANNEL_LOCKOUT_KEY);
	if (!raw) return null;
	const until = Number(raw);
	return Number.isFinite(until) && until > 0 ? until : null;
}

export function getDevelopChannelLockoutRemainingMs(now = Date.now()): number {
	const until = readDevelopChannelLockoutUntil();
	if (!until) return 0;
	const remaining = until - now;
	if (remaining <= 0) {
		clearDevelopChannelLockout();
		return 0;
	}
	return remaining;
}

export function resolveDevelopChannelLockoutMs(
	mockDevMode = false,
): number {
	return mockDevMode ? DEVELOP_CHANNEL_LOCKOUT_MOCK_MS : DEVELOP_CHANNEL_LOCKOUT_MS;
}

export function formatDevelopChannelLockoutDurationLabel(mockDevMode = false): string {
	return mockDevMode ? '30 секунд' : '30 минут';
}

export function setDevelopChannelLockout(
	durationMs = DEVELOP_CHANNEL_LOCKOUT_MS,
	now = Date.now(),
): void {
	if (typeof localStorage === 'undefined') return;
	localStorage.setItem(DEVELOP_CHANNEL_LOCKOUT_KEY, String(now + durationMs));
}

export function clearDevelopChannelLockout(): void {
	if (typeof localStorage === 'undefined') return;
	localStorage.removeItem(DEVELOP_CHANNEL_LOCKOUT_KEY);
}

export function hasDevelopChannelQuizPassed(): boolean {
	if (typeof localStorage === 'undefined') return false;
	try {
		return localStorage.getItem(DEVELOP_CHANNEL_QUIZ_PASSED_KEY) === 'true';
	} catch {
		return false;
	}
}

export function markDevelopChannelQuizPassed(): void {
	if (typeof localStorage === 'undefined') return;
	try {
		localStorage.setItem(DEVELOP_CHANNEL_QUIZ_PASSED_KEY, 'true');
	} catch {
		/* ignore quota / private mode */
	}
}

export function clearDevelopChannelQuizPassed(): void {
	if (typeof localStorage === 'undefined') return;
	try {
		localStorage.removeItem(DEVELOP_CHANNEL_QUIZ_PASSED_KEY);
	} catch {
		/* ignore private mode */
	}
}

export function formatQuizQuestionCountdown(remainingMs: number): string {
	const totalSec = Math.max(0, Math.ceil(remainingMs / 1000));
	const m = Math.floor(totalSec / 60);
	const s = totalSec % 60;
	return `${m}:${String(s).padStart(2, '0')}`;
}

export function formatLockoutCountdown(remainingMs: number): string {
	const totalSec = Math.max(0, Math.ceil(remainingMs / 1000));
	const h = Math.floor(totalSec / 3600);
	const m = Math.floor((totalSec % 3600) / 60);
	const s = totalSec % 60;
	if (h > 0) {
		return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
	}
	return `${m}:${String(s).padStart(2, '0')}`;
}
