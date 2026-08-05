/**
 * Проводка бейджей: стор → сайдбар → пункт группы.
 *
 * Модель бейджа (`data/navigation.ts`), разбор источников в значения
 * (`stores/navBadges.ts`) и отрисовка чипа (`SideNavGroup.test.ts`) покрыты
 * порознь, а стык — нет: убери `badges={$navBadges}` из SideNav, и все три
 * набора остаются зелёными, пока бейджи исчезают во всём приложении.
 *
 * Стор здесь настоящий — подменены только его источники. Проверяется весь путь
 * значения, а не факт, что компоненту передали объект.
 */
import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { readable } from 'svelte/store';

vi.mock('$app/stores', () => ({
	// Открытой рисуется группа текущего маршрута — иначе пунктов в разметке нет.
	page: readable({ url: new URL('http://router/sb/rules') }),
}));

vi.mock('$lib/stores/singboxRouter', () => ({
	singboxRouter: {
		settings: readable({ routingMode: 'tproxy' }),
		status: readable({ ruleCount: 7, ruleSetCount: 3, outboundCompositeCount: 2 }),
	},
}));

vi.mock('$lib/components/sb-router/liveConnectionsStore', () => ({
	liveConnectionsSnapshot: readable({ connectionsTotal: 41 }),
}));

const { default: SideNav } = await import('./SideNav.svelte');

const badgeOf = (label: string) =>
	screen.getByText(label).closest('a')?.querySelector('.nav-item-badge')?.textContent;

describe('SideNav: бейджи доезжают до пунктов группы', () => {
	it('значения из стора видны в сайдбаре', () => {
		render(SideNav);
		expect(badgeOf('Движок')).toBe('TPROXY');
		expect(badgeOf('Маршруты')).toBe('7');
		expect(badgeOf('Rule sets')).toBe('3');
		expect(badgeOf('Группы')).toBe('2');
		expect(badgeOf('Соединения движка')).toBe('41');
	});

	it('пункт без источника бейджа остаётся без чипа', () => {
		render(SideNav);
		// DNS-счётчика в Status DTO движка нет — и чипа быть не должно.
		expect(badgeOf('DNS')).toBeUndefined();
	});
});
