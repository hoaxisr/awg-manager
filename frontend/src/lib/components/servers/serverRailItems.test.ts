import { describe, expect, it } from 'vitest';
import type { CDNDispatcherConfig } from '$lib/types';
import { buildRailItems } from './serverRailItems';

function cfg(over: Partial<CDNDispatcherConfig>): CDNDispatcherConfig {
	return {
		enabled: false,
		listen_addr: ':9009',
		xray_target: '',
		tg_target: '',
		routes: [],
		...over,
	};
}

const hasDispatcher = (d: CDNDispatcherConfig | null, activeId = '') =>
	buildRailItems([], {}, [], null, null, d, activeId).some((i) => i.id === '__dispatcher__');

describe('buildRailItems: dispatcher item', () => {
	it('is hidden when stopped and without routes', () => {
		expect(hasDispatcher(cfg({}))).toBe(false);
		expect(hasDispatcher(null)).toBe(false);
	});

	it('is shown when running', () => {
		expect(hasDispatcher(cfg({ running: true }))).toBe(true);
	});

	it('is shown when it has routes', () => {
		const routes = [{ id: 'r' }] as unknown as CDNDispatcherConfig['routes'];
		expect(hasDispatcher(cfg({ routes }))).toBe(true);
	});

	it('stays reachable when selected via a link', () => {
		expect(hasDispatcher(cfg({}), '__dispatcher__')).toBe(true);
	});
});

describe('dispatcher labels', () => {
	it('no longer use the unclear "incoming connections" wording', async () => {
		const ru = (await import('../../../../messages/ru.json')).default as unknown as Record<string, string>;
		const en = (await import('../../../../messages/en.json')).default as unknown as Record<string, string>;
		const keys = [
			'servers_cdn_title',
			'servers_ext_lbl_dispatcher_rail_title',
			'servers_tg_settings_dispatcher',
			'servers_xray_incoming_connections',
		];
		for (const k of keys) {
			expect(ru[k]).toContain('Домены внешнего доступа');
			expect(en[k]).not.toMatch(/incoming/i);
		}
	});
});
