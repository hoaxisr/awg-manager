// Строки карточки «Здоровье» страницы «Движок» — чистые функции.
//
// ЗАВИСИМОСТЬ «TPROXY target» РЕЖИМНАЯ. `xt_TPROXY` нужен только
// netfilter-перехвату tproxy: в policy-tun трафик заводит политика доступа
// NDMS, в fakeip-tun — маршрут на fakeip-пул, и модуль там не участвует
// (config_policytun.go / config_fakeip.go его не требуют). В шторке гейта не
// было, потому что она открывалась только на TProxy-поверхности; на единой
// странице строка «не загружен — kmod не доступен» красным висела бы в двух
// режимах из трёх, где чинить нечего (находка N-10 инвентаря задачи 1).
//
// ДЕДУПЛИКАЦИЯ policy-tun-unbound. Замечание показывает `PolicyTunCard` —
// там же рядом кнопка «Политики доступа →», то есть действие по устранению.
// Раньше карточка и общий список замечаний жили в одной шторке и фильтр
// стоял в ней (StatusDrawer:81-87); теперь «Здоровье» и карточка захвата — на
// одной странице, и без фильтра строка задвоилась бы. Убрать её из
// `PolicyTunCard` вместо этого — хуже: замечание уехало бы от своей кнопки.
//
// Гейт по режиму, а не по факту «есть ли карточка на экране»: карточку захвата
// рисует ровно тот же `normalizeRoutingMode`, и второе условие разъехалось бы
// с первым при любой правке одного из них.
import {
	deriveDeps,
	deriveIssues,
	type DepEntry,
	type IssueEntry,
} from '$lib/components/sb-router/drawerData';
import { formatSuppressedUntil } from '$lib/components/sb-router/crashInfo';
import type { SingboxRouterStatus } from '$lib/types';
import type { EngineRoutingMode } from './engineRunState';

export interface UdpTimeoutOption {
	value: string;
	label: string;
}

/** Семь фиксированных значений шторки (StatusDrawer:215-223); пустое = дефолт бэкенда. */
export const UDP_TIMEOUT_OPTIONS: UdpTimeoutOption[] = [
	{ value: '', label: 'По умолчанию (5 мин)' },
	{ value: '5m0s', label: '5 минут' },
	{ value: '10m0s', label: '10 минут' },
	{ value: '15m0s', label: '15 минут' },
	{ value: '30m0s', label: '30 минут' },
	{ value: '1h0m0s', label: '1 час' },
	{ value: '3h0m0s', label: '3 часа' },
];

/** Зависимости движка для текущего режима захвата. */
export function engineDeps(
	status: SingboxRouterStatus | null,
	mode: EngineRoutingMode,
): DepEntry[] {
	return deriveDeps(status).filter((dep) => dep.id !== 'tproxy-target' || mode === 'tproxy');
}

/**
 * Замечания движка без строки, которую уже показывает карточка захвата.
 *
 * ctaHint «(в Эксперт)» из шторки не переносится: режима «Эксперт» в nav-v3
 * нет, и подпись отправляла бы пользователя туда, чего не существует.
 */
export function engineIssues(
	status: SingboxRouterStatus | null,
	mode: EngineRoutingMode,
): IssueEntry[] {
	const source =
		status && mode === 'policy-tun'
			? {
					...status,
					issues: (status.issues ?? []).filter((i) => i.kind !== 'policy-tun-unbound'),
				}
			: status;
	return deriveIssues(source).map((issue) => ({
		tone: issue.tone,
		kind: issue.kind,
		text: issue.text,
	}));
}

export interface CrashInfoView {
	/** Падений за окно backoff'а (10 мин). 0 — блок виден только из-за паузы. */
	count: number;
	/** Причина последнего падения; пусто — строку не показываем. */
	reason: string;
	/** «HH:MM» окончания паузы авто-перезапуска; null — не подавлен. */
	suppressedUntil: string | null;
	/**
	 * Движок мёртв при живом перехвате — рядом стоит замечание, и путь к кнопке
	 * «Перезапустить» уместен независимо от паузы.
	 */
	deadInterception: boolean;
	/**
	 * Счётчик и пауза уже напечатаны текстом замечания рядом — блок обязан их
	 * промолчать. Причины это не касается: её в тексте замечания нет.
	 */
	restated: boolean;
}

/**
 * Замечание про мёртвый движок при живом перехвате.
 *
 * Бэкенд собирает его прозой (service_lifecycle.go), и текст у него ДВА:
 * с паузой — «… Автоперезапуск приостановлен до 19:37 (падений за 10 мин: 3).»,
 * без паузы — «… Автоперезапуск: при следующей проверке (до 30 с).» Счётчик
 * есть только в первом. Разбирать саму строку, чтобы вычесть из неё факты, —
 * гонка с любой правкой формулировки; гейт стоит по kind ПЛЮС по тому же
 * условию, по которому бэкенд выбирает ветку (см. ниже).
 */
const RESTATING_ISSUE_KIND = 'engine-dead-interception';

/**
 * Блок падений (#456) или null, когда показывать нечего.
 *
 * Падения и пауза независимы: серия неудачных стартов до grace-периода даёт
 * паузу без записанных падений, а после восстановления счётчик ещё держится в
 * 10-минутном окне уже без паузы.
 *
 * `issues` — список, который карточка РИСУЕТ (а не сырой status.issues):
 * дубликат считается по тому, что реально попало на экран.
 */
export function engineCrashInfo(
	status: SingboxRouterStatus | null,
	issues: IssueEntry[] = [],
): CrashInfoView | null {
	const count = status?.crashCount ?? 0;
	const suppressedUntil = formatSuppressedUntil(status?.restartSuppressedUntil);
	if (count <= 0 && suppressedUntil === null) return null;
	const deadInterception = issues.some((i) => i.kind === RESTATING_ISSUE_KIND);
	// Пауза, как её видит БЭКЕНД: непустой restartSuppressedUntil — ровно
	// `!suppressedUntil.IsZero()` из service_lifecycle.go, то есть ровно то
	// условие, под которым счётчик и пауза попадают в текст замечания. Гейт по
	// сырому полю, а не по отформатированному: формат — дело показа, а
	// нечитаемую дату бэкенд в текст всё равно уже вписал.
	const backendPaused = Boolean(status?.restartSuppressedUntil);
	return {
		count: count > 0 ? count : 0,
		reason: (status?.lastCrashReason ?? '').trim(),
		suppressedUntil,
		deadInterception,
		restated: deadInterception && backendPaused,
	};
}
