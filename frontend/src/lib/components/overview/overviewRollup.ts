/**
 * Сводный статус «Обзора» — чисто клиентский derived поверх данных, которые
 * страница и без того держит. Никаких дополнительных запросов: список причин
 * собирается из тех же сторов, что рисуют блоки.
 */
import type {
	FreeTurnStatus,
	HydraRouteStatus,
	SingboxRouterStatus,
	Subscription,
	TunnelListItem,
	WANStatus,
	WdttStatus,
} from '$lib/types';

/** Порог «памяти роутера», с которого плитка краснеет и статус портится. */
export const MEMORY_ALERT_PERCENT = 90;

export interface RollupInput {
	wan: WANStatus | null;
	routerStatus: SingboxRouterStatus | null;
	awgTunnels: TunnelListItem[];
	subscriptions: Subscription[];
	hydraroute: HydraRouteStatus | null;
	freeturn: FreeTurnStatus | null;
	wdtt: WdttStatus | null;
	updateAvailable: boolean;
	memoryUsedPercent?: number;
}

export interface Rollup {
	ok: boolean;
	reasons: string[];
}

/**
 * Проблема движка sing-box одной строкой: issues[0] → lastError → crashCount
 * (первый непустой). Тот же порядок использует полоса в карточке движка.
 */
export function engineIssueText(status: SingboxRouterStatus | null): string {
	if (!status) return '';
	const issue = status.issues?.[0];
	if (issue?.message) return issue.message;
	if (status.lastError) return status.lastError;
	if ((status.crashCount ?? 0) > 0) {
		return `падений sing-box за последние 10 минут: ${status.crashCount}`;
	}
	return '';
}

export function computeRollup(input: RollupInput): Rollup {
	const reasons: string[] = [];

	if (input.wan && !input.wan.anyWANUp) {
		reasons.push('нет активного WAN-соединения');
	}

	const router = input.routerStatus;
	if (router?.enabled && !router.active) {
		reasons.push('маршрутизация sing-box включена, но не активна');
	}
	const engineIssue = engineIssueText(router);
	if (engineIssue) reasons.push(`движок sing-box: ${engineIssue}`);

	const broken = input.awgTunnels.filter((t) => t.status === 'broken').length;
	if (broken > 0) reasons.push(`туннели AmneziaWG с ошибкой: ${broken}`);

	const failedSub = input.subscriptions.find((s) => s.enabled && s.lastError);
	if (failedSub) reasons.push(`подписка «${failedSub.label}»: ошибка обновления`);

	if (input.hydraroute?.processState === 'dead') {
		reasons.push('HR Neo: процесс умер');
	}

	const ftFailed = [...(input.freeturn?.clients ?? []), ...(input.freeturn?.servers ?? [])].some(
		(i) => i.status?.lastError,
	);
	if (ftFailed) reasons.push('FreeTurn: ошибка процесса');

	const wdttFailed = (input.wdtt?.clients ?? []).some((i) => i.status?.lastError);
	if (wdttFailed) reasons.push('WDTT: ошибка процесса');

	if (input.updateAvailable) reasons.push('доступно обновление AWG Manager');

	const mem = input.memoryUsedPercent;
	if (mem !== undefined && mem >= MEMORY_ALERT_PERCENT) {
		reasons.push(`память роутера: ${Math.round(mem)}%`);
	}

	return { ok: reasons.length === 0, reasons };
}
