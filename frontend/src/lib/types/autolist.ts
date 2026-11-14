/**
 * awgmautolist 0.9.3 — внешний сателлит-демон (автосписок неоткрывающихся
 * сайтов). Ставится пользователем отдельно: AWGM читает его operational
 * статус через loopback и проксирует команды /config, /add, /forget,
 * /restore, /remove и /dismiss. Диагностических /probe и /exclude нет:
 * исключения редактируются в настройках.
 *
 * Типы зеркалят awgmautolist/internal/model (candidate.go, target.go)
 * и Status движка (awgmautolist/internal/engine/engine.go).
 */

/** Результат одной активной проверки. Ошибка никогда не доказывает блокировку. */
export type AutolistVerdict = 'not-checked' | 'reachable' | 'blocked' | 'error';

/** Транспорт, наблюдаемый AF_PACKET. */
export type AutolistNetwork = 'tcp' | 'udp';

/** Состояние карточки кандидата. */
export type AutolistCandidateState = 'checking' | 'waiting' | 'added' | 'rejected';

/** Действие POST /autolist/control. */
export type AutolistControlAction = 'enable' | 'disable';

/**
 * Результат batch-действия над кандидатами: сателлит применяет каждый id
 * отдельно и возвращает per-ID исход. HTTP 200 не скрывает частичный сбой:
 * неудачные id несут сырой текст ошибки сателлита.
 */
export interface AutolistBatchFailure {
	id: string;
	error: string;
}

export interface AutolistBatchResult {
	/** ID, применённые успешно. */
	ok: string[];
	/** ID с ошибкой записи/применения. */
	failed: AutolistBatchFailure[];
}

/**
 * Пути сателлита, которые проксирует AWGM: чтение/запись конфига, действия
 * кандидатов (одиночные и batch {ids}) и очистка журнала /history/clear.
 * /probe и /exclude в 0.9.3 не существуют.
 */
export type AutolistSatellitePath = '/config' | '/add' | '/forget' | '/restore' | '/remove' | '/dismiss' | '/history/clear';

/**
 * CheckEvidence — доказательства одной проверки: вердикт, объём ответа,
 * длительность, сырая техническая деталь и время проверки.
 */
export interface AutolistCheckEvidence {
	verdict: AutolistVerdict;
	bytes?: number;
	durationMs?: number;
	detail?: string;
	checkedAt?: string;
}

/** Результат проверки покрытия и исключений до активных проб. */
export interface AutolistCoverageEvidence {
	covered?: boolean;
	excluded?: boolean;
	detail?: string;
	durationMs?: number;
}

/**
 * Target — наблюдаемая цель. Domain — зарегистрированная зона правила
 * (www.example.co.uk → example.co.uk), Host — точный SNI пробы, IP — адрес,
 * сохранённый для direct/tunnel-проб. У IP-only цели Domain пуст.
 */
export interface AutolistTarget {
	domain?: string;
	host?: string;
	ip?: string;
	network: AutolistNetwork;
	port: number;
}

/** Кандидат — все доказательства одного решения. */
export interface AutolistCandidate {
	id: string;
	target: AutolistTarget;
	/** Причина AF_PACKET: tcp-rst, tls-no-response, connect-no-response, response-stall, udp-no-response. */
	reason: string;
	clientIP?: string;
	clientMAC?: string;
	state: AutolistCandidateState;
	firstAt: string;
	lastAt: string;
	coverage: AutolistCoverageEvidence;
	direct: AutolistCheckEvidence;
	wan: AutolistCheckEvidence;
	tunnel: AutolistCheckEvidence;
	targetInterface?: string;
	targetOutbound?: string;
	correlationID?: string;
	lastError?: string;
}

/**
 * История решений: ручные и автоматические add/remove с результатом записи.
 * Новые записи первыми; переживает рестарт сателлита.
 */
export interface AutolistHistoryEntry {
	at: string;
	domain?: string;
	host?: string;
	ip?: string;
	network?: AutolistNetwork;
	port?: number;
	action: string;
	/** user — ручное действие, auto — автоматическая запись. */
	source: string;
	interface?: string;
	outbound?: string;
	reason?: string;
	/** Результат записи/удаления: ok или сырой текст ошибки. */
	result?: string;
}

/** Operational статус сателлита для главного экрана. */
export interface AutolistStatus {
	running: boolean;
	version?: string;
	/** Init script найден; вкладка остаётся доступной для повторного включения. */
	installed: boolean;
	/** Пользовательское намерение запускать сателлит, в том числе после reboot. */
	enabled: boolean;
	/** Версия установленного opkg-пакета. */
	packageVersion?: string;
	/** Пакет старше панели: сателлит выпускается с её версией. */
	updateAvailable?: boolean;
	/** AF_PACKET принимает пакеты. */
	captureActive: boolean;
	/** Захват на паузе из-за недоступного WAN (периодический WAN-check включён). */
	capturePaused: boolean;
	/** Наблюдаемые протоколы, например ['tcp/443', 'dns/53', 'stun/turn']. */
	protocols?: string[];
	/** Интерфейс tunnel-пробы и новых правил; пусто — первый доступный туннель. */
	targetInterface?: string;
	/** Outbound, фактически выбранный для последних правил. */
	targetOutbound?: string;
	/** Откуда резолвить для проверки IP-покрытия: '' — dns.final, иначе sb:<tag>. */
	resolveVia?: string;
	/** Последняя проверка WAN: вердикт и время. */
	lastWAN?: AutolistCheckEvidence;
	activeChecks: number;
	queuedChecks: number;
	/** Использование скользящего часового бюджета добавлений; limit=0 — выключен. */
	addsUsedHour: number;
	addsLimit: number;
	candidates: AutolistCandidate[];
	history: AutolistHistoryEntry[];
	lastError?: string;
}

/**
 * Конфиг сателлита — ровно настройки 0.9.3 (awgmautolist/internal/config).
 * resolveVia — только '' (dns.final) или sb:<tag>; legacy-режимов нет.
 */
export interface AutolistConfig {
	autoAdd: boolean;
	/** Пусто — первый доступный туннель по списку AWGM. */
	targetInterface?: string;
	/** '' — dns.final из sing-box, иначе sb:<tag> из servers/list. */
	resolveVia?: string;
	/** Параллельная WAN-проверка; пусто — https://ya.ru/robots.txt. */
	wanCheckUrl?: string;
	/** Проверять WAN каждые 30 секунд и ставить захват на паузу при outage. */
	wanMonitorEnabled: boolean;
	/** Tunnel-проба после direct blocked; без неё автодобавление невозможно. */
	tunnelProbe: boolean;
	/** Ручные исключения: домены не проверяются и не добавляются. */
	excludeDomains?: string[];
	/** Rule-set/geosite-исключения для имён и IP. */
	excludeRuleSets?: string[];
	/** Наблюдать STUN/TURN на любых UDP-портах. */
	udpEnabled: boolean;
	/** Подробный журнал в /tmp/awgmautolist.log. */
	verbose: boolean;
	/**
	 * Бюджет добавлений за скользящий час, 1..500 (default 50). Статус
	 * показывает его как addsLimit; 0/unlimited бэкенд не поддерживает.
	 */
	addsPerHour?: number;
}
