import { describe, it, expect } from 'vitest';
import { computeRollup, engineIssueText, type RollupInput } from './overviewRollup';
import type { SingboxRouterStatus, Subscription, TunnelListItem } from '$lib/types';

const empty: RollupInput = {
	wan: null,
	routerStatus: null,
	awgTunnels: [],
	subscriptions: [],
	hydraroute: null,
	freeturn: null,
	wdtt: null,
	updateAvailable: false,
};

const routerStatus = (patch: Partial<SingboxRouterStatus>): SingboxRouterStatus =>
	({ enabled: true, installed: true, active: true, ...patch }) as SingboxRouterStatus;

const tunnel = (patch: Partial<TunnelListItem>): TunnelListItem =>
	({ id: 't', name: 't', status: 'running', enabled: true, ...patch }) as TunnelListItem;

const subscription = (patch: Partial<Subscription>): Subscription =>
	({ id: 's', label: 'DE', enabled: true, ...patch }) as Subscription;

describe('computeRollup', () => {
	it('нет данных — нет причин', () => {
		expect(computeRollup(empty)).toEqual({ ok: true, reasons: [] });
	});

	it('WAN лежит', () => {
		const r = computeRollup({ ...empty, wan: { interfaces: {}, anyWANUp: false } });
		expect(r.ok).toBe(false);
		expect(r.reasons).toContain('нет активного WAN-соединения');
	});

	it('WAN поднят — не причина', () => {
		expect(computeRollup({ ...empty, wan: { interfaces: {}, anyWANUp: true } }).ok).toBe(true);
	});

	it('маршрутизация включена, но не активна', () => {
		const r = computeRollup({ ...empty, routerStatus: routerStatus({ active: false }) });
		expect(r.reasons).toContain('маршрутизация sing-box включена, но не активна');
	});

	it('проблема движка попадает в причины', () => {
		const r = computeRollup({
			...empty,
			routerStatus: routerStatus({
				issues: [{ severity: 'error', kind: 'orphan-rule', message: 'rule-set download failed' }],
			}),
		});
		expect(r.reasons).toContain('движок sing-box: rule-set download failed');
	});

	it('broken-туннели AWG', () => {
		const r = computeRollup({
			...empty,
			awgTunnels: [tunnel({ status: 'broken' }), tunnel({ status: 'running' })],
		});
		expect(r.reasons).toContain('туннели AmneziaWG с ошибкой: 1');
	});

	it('подписка с ошибкой; выключенная — не считается', () => {
		expect(
			computeRollup({ ...empty, subscriptions: [subscription({ lastError: 'timeout' })] }).reasons,
		).toContain('подписка «DE»: ошибка обновления');
		expect(
			computeRollup({
				...empty,
				subscriptions: [subscription({ enabled: false, lastError: 'timeout' })],
			}).ok,
		).toBe(true);
	});

	it('HR Neo dead, FreeTurn/WDTT с lastError, обновление, память', () => {
		const r = computeRollup({
			...empty,
			hydraroute: { installed: true, running: false, processState: 'dead' },
			freeturn: {
				clients: [{ id: 'default', name: 'c', status: { running: false, lastError: 'bind failed', binary: '', binaryPresent: true } }],
				servers: [],
				client: { running: false, binary: '', binaryPresent: true },
				server: { running: false, binary: '', binaryPresent: true },
				installAvailable: false,
				installing: false,
			},
			wdtt: {
				clients: [{ id: 'default', name: 'c', status: { running: false, lastError: 'boom', binary: '', binaryPresent: true } }],
				servers: [],
				client: { running: false, binary: '', binaryPresent: true },
				server: { running: false, binary: '', binaryPresent: true },
				installAvailable: false,
				updateAvailable: false,
				installing: false,
			},
			updateAvailable: true,
			memoryUsedPercent: 93.4,
		});
		expect(r.reasons).toEqual([
			'HR Neo: процесс умер',
			'FreeTurn: ошибка процесса',
			'WDTT: ошибка процесса',
			'доступно обновление AWG Manager',
			'память роутера: 93%',
		]);
	});

	it('память ниже порога — не причина', () => {
		expect(computeRollup({ ...empty, memoryUsedPercent: 81 }).ok).toBe(true);
	});
});

describe('engineIssueText', () => {
	it('порядок: issues → lastError → crashCount', () => {
		expect(
			engineIssueText(
				routerStatus({
					issues: [{ severity: 'warning', kind: 'x', message: 'issue' }],
					lastError: 'err',
					crashCount: 3,
				}),
			),
		).toBe('issue');
		expect(engineIssueText(routerStatus({ lastError: 'err', crashCount: 3 }))).toBe('err');
		expect(engineIssueText(routerStatus({ crashCount: 3 }))).toBe(
			'падений sing-box за последние 10 минут: 3',
		);
		expect(engineIssueText(routerStatus({}))).toBe('');
		expect(engineIssueText(null)).toBe('');
	});
});
