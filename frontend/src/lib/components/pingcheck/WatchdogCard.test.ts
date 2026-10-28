import { describe, it, expect, vi } from 'vitest';

// Сторы темы при импорте обращаются к matchMedia — в jsdom его нет.
vi.hoisted(() => {
	Object.defineProperty(window, 'matchMedia', {
		configurable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
		}),
	});
});
import { render } from '@testing-library/svelte';
import WatchdogCard from './WatchdogCard.svelte';
import type { PingLogEntry } from '$lib/types';

const entry = (over: Partial<PingLogEntry>): PingLogEntry => ({
	timestamp: '2026-10-28T19:14:49+03:00',
	tunnelId: 'awg10',
	tunnelName: 'warp',
	success: false,
	latency: 0,
	error: '',
	failCount: 2,
	threshold: 2,
	stateChange: '',
	...over,
});

// Провал и запись лечения в одну секунду: одинаковое время у двух строк
// «последних проверок» не должно ронять рендер (F751, стенд 28.10).
describe('WatchdogCard: последние проверки', () => {
	it('две записи с одинаковым временем рендерятся обе', () => {
		const recent = [entry({ failCount: 0, stateChange: 'link_toggle' }), entry({ error: 'timeout' })];
		const { container } = render(WatchdogCard, {
			props: {
				name: 'warp',
				backend: 'kernel',
				statusKind: 'recovering',
				hasPingcheck: true,
				isWatchdog: true,
				configLine: 'HTTP',
				stats: { avgMs: null, minMs: null, maxMs: null, lossPct: 100, failsLabel: '2/2', restarts: 1, history: [0, 0], recent },
				onConfigure: () => {},
				onCheckNow: () => {},
				onDisable: () => {},
				onEnable: () => {},
			},
		});
		expect(container.querySelectorAll('.wd-log-row')).toHaveLength(2);
	});
});
