import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { Waypoints } from 'lucide-svelte';
import SideNavGroup from './SideNavGroup.svelte';
import type { NavGroup } from '$lib/data/navigation';
import type { NavBadgeValues } from '$lib/stores/navBadges';

// Группа-стенд, а не настоящая из NAV_TREE: здесь проверяется контракт
// разметки (разделитель, бейдж), а состав дерева — в navigation.test.ts.
const group: NavGroup = {
	kind: 'group',
	id: 'sb',
	label: 'Sing-box',
	icon: Waypoints,
	items: [
		{ id: 'x-engine', label: 'Движок', href: '/x/engine', badge: 'mode', match: () => false },
		{ kind: 'separator', id: 'x-sep', label: 'правила' },
		{ id: 'x-rules', label: 'Маршруты', href: '/x/rules', badge: 'rules', match: () => false },
		{ id: 'x-dns', label: 'DNS', href: '/x/dns', match: () => false },
	],
};

const mount = (badges: NavBadgeValues = {}) =>
	render(SideNavGroup, {
		props: {
			group,
			open: true,
			activeId: null,
			href: '/x/engine',
			badges,
			onPick: vi.fn(),
		},
	});

const linkOf = (label: string) => screen.getByText(label).closest('a');

describe('SideNavGroup: разделитель', () => {
	it('рендерится тихой подписью, а не пунктом меню', () => {
		mount();
		const separator = screen.getByText('правила');
		expect(separator.tagName).toBe('SPAN');
		expect(separator.closest('a')).toBeNull();
		expect(separator.closest('button')).toBeNull();
		// Не в фокусной цепочке и без роли пункта меню.
		expect(separator.getAttribute('tabindex')).toBeNull();
		expect(separator.getAttribute('role')).toBeNull();
	});

	it('не добавляет в группу лишней ссылки', () => {
		const { container } = mount();
		expect(container.querySelectorAll('.group-items a')).toHaveLength(3);
	});
});

describe('SideNavGroup: бейджи', () => {
	it('показывает значение источника рядом с пунктом', () => {
		mount({ mode: 'TPROXY', rules: '12' });
		expect(linkOf('Движок')?.querySelector('.nav-item-badge')?.textContent).toBe('TPROXY');
		expect(linkOf('Маршруты')?.querySelector('.nav-item-badge')?.textContent).toBe('12');
	});

	it('пункт без бейджа рендерится без пустого места', () => {
		mount({ mode: 'TPROXY', rules: '12' });
		expect(linkOf('DNS')?.querySelector('.nav-item-badge')).toBeNull();
	});

	it('источник без значения бейджа не рисует', () => {
		const { container } = mount();
		expect(container.querySelectorAll('.nav-item-badge')).toHaveLength(0);
	});
});
