import { describe, it, expect } from 'vitest';
import { UDP_TIMEOUT_OPTIONS, engineCrashInfo, engineDeps, engineIssues } from './healthRows';
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
			{ tone: 'error', text: orphan.message },
			{ tone: 'warning', text: 'B' },
		]);
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
		expect(view).toEqual({ count: 3, reason: 'OOM-kill', suppressedUntil: null });
	});

	// Серия неудачных стартов до grace-периода даёт паузу без записанных
	// падений: «Падений: 0» рядом с активной паузой только путает.
	it('пауза без падений — блок есть, счётчик нулевой', () => {
		const until = new Date(2026, 0, 2, 7, 5).toISOString();
		const view = engineCrashInfo(status({ crashCount: 0, restartSuppressedUntil: until }));
		expect(view).toEqual({ count: 0, reason: '', suppressedUntil: '07:05' });
	});

	it('битую дату паузы не показывает', () => {
		expect(engineCrashInfo(status({ restartSuppressedUntil: 'позавчера' }))).toBeNull();
	});

	it('пустую причину не протаскивает', () => {
		const view = engineCrashInfo(status({ crashCount: 1, lastCrashReason: '   ' }));
		expect(view?.reason).toBe('');
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
