import { describe, it, expect } from 'vitest';
import { UDP_TIMEOUT_OPTIONS, engineCrashInfo, engineDeps, engineIssues } from './healthRows';
import type { IssueEntry } from '$lib/components/sb-router/drawerData';
import type { SingboxRouterIssue, SingboxRouterStatus } from '$lib/types';
import type { EngineRoutingMode } from './engineRunState';

const MODES: EngineRoutingMode[] = ['tproxy', 'policy-tun', 'fakeip-tun'];

function status(patch: Partial<SingboxRouterStatus> = {}): SingboxRouterStatus {
	return {
		enabled: true,
		installed: true,
		active: true,
		netfilterAvailable: true,
		tproxyTargetAvailable: true,
		policyName: '',
		policyExists: true,
		deviceMode: 'policy',
		snifferEnabled: false,
		deviceCount: 0,
		ruleCount: 0,
		ruleSetCount: 0,
		outboundAwgCount: 0,
		outboundCompositeCount: 0,
		final: 'direct',
		...patch,
	};
}

const unbound: SingboxRouterIssue = {
	severity: 'warning',
	kind: 'policy-tun-unbound',
	message: 'Интерфейс OpkgTun0 не разрешён ни в одной политике доступа',
};
const orphan: SingboxRouterIssue = {
	severity: 'error',
	kind: 'orphan-rule',
	message: 'Правило ссылается на удалённый outbound',
};

describe('engineDeps', () => {
	it('в tproxy показывает обе зависимости', () => {
		expect(engineDeps(status(), 'tproxy').map((d) => d.id)).toEqual([
			'netfilter',
			'tproxy-target',
		]);
	});

	// xt_TPROXY участвует только в netfilter-перехвате tproxy: в policy-tun
	// трафик заводит политика доступа, в fakeip — маршрут на пул. Красная
	// строка «kmod не доступен» в этих режимах говорила бы чинить то, что не
	// используется.
	it.each(['policy-tun', 'fakeip-tun'] as const)('в %s зависимости TPROXY нет', (mode) => {
		const ids = engineDeps(status(), mode).map((d) => d.id);
		expect(ids).toEqual(['netfilter']);
	});

	it('netfilter остаётся во всех режимах и несёт свой тон', () => {
		for (const mode of MODES) {
			const nf = engineDeps(status({ netfilterAvailable: false }), mode).find(
				(d) => d.id === 'netfilter',
			);
			expect(nf?.tone).toBe('error');
		}
	});

	it('без статуса зависимостей нет', () => {
		expect(engineDeps(null, 'tproxy')).toEqual([]);
	});
});

describe('engineIssues', () => {
	// Дедупликация: policy-tun-unbound показывает PolicyTunCard внутри карточки
	// «Захват трафика», и обе карточки теперь на одной странице.
	it('в policy-tun замечание policy-tun-unbound не дублируется', () => {
		const out = engineIssues(status({ issues: [unbound, orphan] }), 'policy-tun');
		expect(out.map((i) => i.text)).toEqual([orphan.message]);
	});

	// В остальных режимах карточки policy-tun на странице нет — прятать
	// замечание не за чем, иначе оно исчезнет вовсе.
	it.each(['tproxy', 'fakeip-tun'] as const)('в %s замечание остаётся в списке', (mode) => {
		const out = engineIssues(status({ issues: [unbound] }), mode);
		expect(out.map((i) => i.text)).toEqual([unbound.message]);
	});

	it('прочие замечания в policy-tun не теряются и сохраняют порядок и тон', () => {
		const second: SingboxRouterIssue = { severity: 'warning', kind: 'x', message: 'B' };
		const out = engineIssues(status({ issues: [orphan, unbound, second] }), 'policy-tun');
		expect(out).toEqual([
			{ tone: 'error', kind: orphan.kind, text: orphan.message },
			{ tone: 'warning', kind: 'x', text: 'B' },
		]);
	});

	// Блок падений гасит свой дубликат по kind замечания — по тексту
	// бэкендовой прозы это не различить.
	it('протаскивает kind', () => {
		const dead: SingboxRouterIssue = {
			severity: 'error',
			kind: 'engine-dead-interception',
			message: 'Движок остановлен, но перехват трафика активен',
		};
		const out = engineIssues(status({ issues: [dead] }), 'tproxy');
		expect(out.map((i) => i.kind)).toEqual(['engine-dead-interception']);
	});

	// Режима «Эксперт» в nav-v3 нет: подпись отправляла бы пользователя туда,
	// чего не существует.
	it('не тащит ctaHint «(в Эксперт)» из шторки', () => {
		const out = engineIssues(status({ issues: [orphan] }), 'tproxy');
		expect(out[0]).not.toHaveProperty('ctaHint');
	});

	it('без статуса замечаний нет', () => {
		expect(engineIssues(null, 'policy-tun')).toEqual([]);
	});
});

describe('engineCrashInfo', () => {
	it('нет ни падений, ни паузы — блока нет', () => {
		expect(engineCrashInfo(status())).toBeNull();
		expect(engineCrashInfo(status({ crashCount: 0, restartSuppressedUntil: '' }))).toBeNull();
		expect(engineCrashInfo(null)).toBeNull();
	});

	it('падения без паузы — блок есть, времени нет', () => {
		const view = engineCrashInfo(status({ crashCount: 3, lastCrashReason: 'OOM-kill' }));
		expect(view).toEqual({
			count: 3,
			reason: 'OOM-kill',
			suppressedUntil: null,
			restated: false,
			deadInterception: false,
		});
	});

	// Серия неудачных стартов до grace-периода даёт паузу без записанных
	// падений: «Падений: 0» рядом с активной паузой только путает.
	it('пауза без падений — блок есть, счётчик нулевой', () => {
		const until = new Date(2026, 0, 2, 7, 5).toISOString();
		const view = engineCrashInfo(status({ crashCount: 0, restartSuppressedUntil: until }));
		expect(view).toEqual({
			count: 0,
			reason: '',
			suppressedUntil: '07:05',
			restated: false,
			deadInterception: false,
		});
	});

	it('битую дату паузы не показывает', () => {
		expect(engineCrashInfo(status({ restartSuppressedUntil: 'позавчера' }))).toBeNull();
	});

	it('пустую причину не протаскивает', () => {
		const view = engineCrashInfo(status({ crashCount: 1, lastCrashReason: '   ' }));
		expect(view?.reason).toBe('');
	});
});

// Бэкенд вкладывает паузу и счётчик В ТЕКСТ замечания engine-dead-interception
// («Автоперезапуск приостановлен до 19:37 (падений за 10 мин: 3)»,
// service_lifecycle.go). Блок падений печатает те же два факта своими полями —
// в шторке их разделяли две секции, на странице они встык.
describe('engineCrashInfo · дубликат engine-dead-interception', () => {
	const dead: IssueEntry[] = [
		{ tone: 'error', kind: 'engine-dead-interception', text: 'Движок остановлен…' },
	];
	const other: IssueEntry[] = [{ tone: 'warning', kind: 'orphan-rule', text: 'Правило…' }];
	const until = new Date(2026, 0, 2, 19, 37).toISOString();
	const crashed = status({
		crashCount: 3,
		lastCrashReason: 'OOM-kill',
		restartSuppressedUntil: until,
	});

	it('замечание есть — счётчик и пауза помечены как уже сказанные', () => {
		expect(engineCrashInfo(crashed, dead)).toEqual({
			count: 3,
			reason: 'OOM-kill',
			suppressedUntil: '19:37',
			restated: true,
			deadInterception: true,
		});
	});

	it('замечание другого рода дубликатом не считается', () => {
		expect(engineCrashInfo(crashed, other)?.restated).toBe(false);
		expect(engineCrashInfo(crashed, other)?.deadInterception).toBe(false);
	});

	// C1. Текст замечания собирается ДВУМЯ ветками (service_lifecycle.go):
	// счётчик попадает в него только вместе с паузой («приостановлен до 19:37
	// (падений за 10 мин: 3)»), а без паузы там лишь «Автоперезапуск: при
	// следующей проверке (до 30 с)». Гейт по одному kind глушил счётчик и во
	// второй ветке — пользователь в crash-loop видел одно падение вместо трёх.
	it('замечание без паузы счётчик не глушит — его в тексте нет', () => {
		const noPause = status({ crashCount: 3, lastCrashReason: 'OOM-kill' });
		expect(engineCrashInfo(noPause, dead)).toEqual({
			count: 3,
			reason: 'OOM-kill',
			suppressedUntil: null,
			restated: false,
			deadInterception: true,
		});
	});

	// Гейт стоит по сырому restartSuppressedUntil — это ровно
	// `!suppressedUntil.IsZero()` бэкенда, то есть ровно то условие, под
	// которым он вложил оба факта в текст. Формат даты к этому отношения не
	// имеет: битую дату не показываем, но текст замечания её уже назвал.
	it('пауза сказана текстом даже при нечитаемой дате', () => {
		const broken = status({ crashCount: 3, restartSuppressedUntil: 'позавчера' });
		expect(engineCrashInfo(broken, dead)?.restated).toBe(true);
		expect(engineCrashInfo(broken, dead)?.suppressedUntil).toBeNull();
	});

	it('без замечаний блок печатает всё сам', () => {
		expect(engineCrashInfo(crashed, [])?.restated).toBe(false);
		expect(engineCrashInfo(crashed)?.restated).toBe(false);
	});

	// Причина падения в текст замечания НЕ входит — её блок обязан сохранить
	// в любом случае, иначе факт пропадает со страницы совсем.
	it('причину не теряет и при дубликате', () => {
		expect(engineCrashInfo(crashed, dead)?.reason).toBe('OOM-kill');
	});

	// Замечание без crash-статистики (ветка «Автоперезапуск: при следующей
	// проверке») блока не создаёт: печатать нечего.
	it('замечание без падений и паузы блока не рождает', () => {
		expect(engineCrashInfo(status(), dead)).toBeNull();
	});
});

describe('UDP_TIMEOUT_OPTIONS', () => {
	it('семь значений шторки, первое — дефолт бэкенда', () => {
		expect(UDP_TIMEOUT_OPTIONS).toHaveLength(7);
		expect(UDP_TIMEOUT_OPTIONS[0].value).toBe('');
		expect(UDP_TIMEOUT_OPTIONS.map((o) => o.value)).toEqual([
			'',
			'5m0s',
			'10m0s',
			'15m0s',
			'30m0s',
			'1h0m0s',
			'3h0m0s',
		]);
	});
});
