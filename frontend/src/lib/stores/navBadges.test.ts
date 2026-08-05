import { describe, it, expect } from 'vitest';
import { computeNavBadges } from './navBadges';
import type { SingboxRouterSettings, SingboxRouterStatus } from '$lib/types';

const settings = (patch: Partial<SingboxRouterSettings> = {}) =>
	({ routingMode: 'tproxy', ...patch }) as SingboxRouterSettings;

const status = (patch: Partial<SingboxRouterStatus> = {}) =>
	({ ruleCount: 7, ruleSetCount: 3, outboundCompositeCount: 2, ...patch }) as SingboxRouterStatus;

describe('computeNavBadges', () => {
	it('счётчики берутся из статуса движка, соединения — из живого снимка', () => {
		expect(computeNavBadges(settings(), status(), 41)).toEqual({
			mode: 'TPROXY',
			groups: '2',
			rules: '7',
			'rule-sets': '3',
			connections: '41',
		});
	});

	it('режим — подпись, общая с экраном смены режима', () => {
		expect(computeNavBadges(settings({ routingMode: 'fakeip-tun' }), null, 0).mode).toBe('FakeIP');
		expect(computeNavBadges(settings({ routingMode: 'policy-tun' }), null, 0).mode).toBe(
			'Политики + tun',
		);
	});

	// На проводе routingMode — строка: незнакомое значение обязано давать
	// отсутствие бейджа, а не пустой чип.
	it('незнакомый режим бейджа не даёт', () => {
		const unknown = { routingMode: 'wireguard-tun' } as unknown as SingboxRouterSettings;
		expect(computeNavBadges(unknown, null, 0).mode).toBeUndefined();
	});

	it('без данных бейджей нет', () => {
		expect(computeNavBadges(null, null, 0)).toEqual({
			mode: undefined,
			groups: undefined,
			rules: undefined,
			'rule-sets': undefined,
			connections: undefined,
		});
	});

	it('нулевой счётчик не рисуется', () => {
		const zeros = computeNavBadges(null, status({ ruleCount: 0, outboundCompositeCount: 0 }), 0);
		expect(zeros.rules).toBeUndefined();
		expect(zeros.groups).toBeUndefined();
		expect(zeros.connections).toBeUndefined();
		expect(zeros['rule-sets']).toBe('3');
	});
});
