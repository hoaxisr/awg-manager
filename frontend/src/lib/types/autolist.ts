/**
 * awgmautolist — внешний сателлит-демон (автосписок неоткрывающихся
 * сайтов). Ставится пользователем отдельно, как zapret: AWGM не устанавливает
 * его и не управляет его жизненным циклом, только читает состояние через
 * loopback и передаёт dismiss.
 *
 * Типы зеркалят internal/autolistdetect/detect.go.
 */

/** Причина, по которой домен попал в кандидаты (awgmautolist Why). */
export type AutolistWhy = 'no-answer' | 'reset' | 'freeze' | 'no-connect' | '';

/** Состояние кандидата (awgmautolist State). */
export type AutolistCandidateState = 'candidate' | 'checking';

export interface AutolistCandidate {
	domain: string;
	/** Имя хоста, на котором наблюдались отказы. */
	host: string;
	ip?: string;
	why: AutolistWhy;
	state: AutolistCandidateState;
	fails: number;
	/** Откуда пришло: 'dial' (журнал sing-box) и/или 'stall' (соединения). */
	sources: string[];
	lastAt: string;
	/** Вердикт активной пробы, если была. */
	verdict?: 'blocked' | 'reachable' | 'unknown';
	probedAt?: string;
	stage?: 'queued' | 'direct' | 'tunnel';
	probeError?: string;
	/** Отдельный HTTP-результат через выбранный outbound. */
	tunnelVerdict?: 'blocked' | 'reachable' | 'unknown';
	tunnelProbedAt?: string;
	tunnelError?: string;
}

export type AutolistHistoryAction = 'dismissed' | 'excluded';
export type AutolistControlAction = 'enable' | 'disable';

export interface AutolistHistoryEntry {
	domain: string;
	host?: string;
	action: AutolistHistoryAction;
	why?: AutolistWhy;
	source: 'user' | 'auto' | 'reachable';
	at: string;
}

export interface AutolistStatus {
	/** Процесс сателлита жив и отвечает. */
	running: boolean;
	version?: string;
	statusUrl?: string;
	/** Режим авто-фонового добавления включён (см. предупреждение в UI). */
	auto?: boolean;
	/** Выбранный kernel-интерфейс tunnel-пробы и новых правил. */
	targetInterface?: string;
	/** Legacy target из старого конфига; не описывает новые пробы. */
	targetOutbound?: string;
	/** Использование и предел скользящего часового бюджета; limit=0 — выключен. */
	probesUsedHour?: number;
	probesLimit?: number;
	addsUsedHour?: number;
	addsLimit?: number;
	candidates: AutolistCandidate[];
	/** Постоянный журнал действий, новые записи первыми. */
	history: AutolistHistoryEntry[];
	/** Init script найден; вкладка остаётся доступной для повторного включения. */
	installed?: boolean;
	/** Пользовательское намерение запускать сателлит, в том числе после reboot. */
	enabled?: boolean;
	/** Причина, по которой живой процесс не ответил. */
	lastError?: string;
}

/** Конфиг сателлита (GET/POST /config awgmautolist). */
export interface AutolistConfig {
	autoAdd: boolean;
	/** Общий kernel-интерфейс полноценной tunnel-пробы и interface-bound правила. */
	targetInterface?: string;
	/** Legacy-поле для обратной совместимости конфигурации; новые пробы его не используют. */
	targetOutbound?: string;
	/** Откуда резолвить домены для проверки IP-покрытия: '' | 'awgm' — роутер, 'system', 'host:port'. */
	resolveVia?: string;
	/** Проверять ли покрытие по IP из резолва (nil = включено). */
	resolveIPEnabled?: boolean;
	/** Kernel interface для прямых curl-проб; пусто — default route. */
	probeInterface?: string;
	/** Подробный журнал сателлита. */
	verbose?: boolean;
	/** Пакетное наблюдение через AF_PACKET; false — только Clash/log fallback. */
	packetEnabled?: boolean;
	/** Первый доказанный отказ сразу создаёт кандидата и запускает пробу. */
	instant?: boolean;
	/** После direct blocked дополнительно проверить HTTP через outbound. */
	tunnelProbe?: boolean;
	/** Независимая WAN-проверка для no-connect; пусто/отсутствует — CF generate_204. */
	wanCheckUrl?: string;
	/** Ручные исключения из номинаций и автодобавления. */
	excludeDomains?: string[];
	/** Дополнительные rule-set/geosite, которые считаются исключениями. */
	excludeRuleSets?: string[];
}

export function emptyAutolistStatus(): AutolistStatus {
	return { running: false, candidates: [], history: [] };
}
