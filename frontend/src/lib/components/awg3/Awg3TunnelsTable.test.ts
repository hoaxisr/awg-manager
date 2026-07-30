import { describe, it, expect, vi } from 'vitest';
import { readFileSync } from 'node:fs';

// Карточка импортирует barrel $lib/components/tunnels, который тянет theme-store
// (тот читает matchMedia на импорте). Заглушка нужна до импортов — vi.hoisted.
vi.hoisted(() => {
	Object.defineProperty(globalThis, 'matchMedia', {
		writable: true,
		configurable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			onchange: null,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
			dispatchEvent: () => false,
		}),
	});
});

import { render } from '@testing-library/svelte';
import Awg3TunnelCard from './Awg3TunnelCard.svelte';
import type { Awg3Tunnel } from '$lib/types';

const withTimers = {
	id: 'awg3-a',
	tag: 'amsterdam',
	host: 'vpn.example.com:51820',
	headerProtection: true,
	rekeyTimeout: '5',
	rekeyAfterTime: '120-150',
	rejectAfterTime: '180',
	keepaliveTimeout: '25',
	maxHandshakeAttempts: '5',
} as Awg3Tunnel;

const bare = {
	id: 'awg3-b',
	tag: 'berlin',
	host: '203.0.113.7:51820',
	headerProtection: false,
} as Awg3Tunnel;

function renderRow(tunnel: Awg3Tunnel) {
	return render(Awg3TunnelCard, {
		props: { tunnel, renderMode: 'table', layout: 'list' },
	});
}

describe('строка таблицы AWG3', () => {
	// Таймеры в узкой колонке «Защита» вылезали за неё и раздували высоту ряда.
	it('таймеры лежат в своей ячейке, а не в «Защите»', () => {
		const { container } = renderRow(withTimers);
		expect(container.querySelector('td.cell-timers .timers')).toBeTruthy();
		expect(container.querySelector('td.cell-hp .timers')).toBeNull();
	});

	it('без таймеров ячейка показывает прочерк', () => {
		const { container } = renderRow(bare);
		const cell = container.querySelector('td.cell-timers');
		expect(cell?.querySelector('.timers')).toBeNull();
		expect(cell?.textContent?.trim()).toBe('—');
	});

	it('число td в строке совпадает с числом col и th секции', () => {
		const section = readFileSync(
			'src/lib/components/awg3/Awg3TunnelsSection.svelte',
			'utf8',
		);
		const cols = section.match(/<col\s/g)?.length ?? 0;
		const ths = section.match(/<th[\s>]/g)?.length ?? 0;
		const colspan = Number(section.match(/colspan="(\d+)"/)?.[1] ?? 0);
		const tds = renderRow(withTimers).container.querySelectorAll('td').length;

		expect(cols).toBe(tds);
		expect(ths).toBe(tds);
		expect(colspan).toBe(tds);
	});
});
