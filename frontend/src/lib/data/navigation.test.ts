import { describe, it, expect } from 'vitest';
import { NAV_TREE, activeItem, breadcrumbFor } from './navigation';

const u = (path: string) => new URL(`http://router${path}`);

describe('NAV_TREE hrefs', () => {
	// Контейнеров-с-вкладками в дереве больше нет: каждый пункт — свой путь.
	// `?tab=` в href означал бы, что раздел снова живёт вкладкой чужой страницы.
	it('ни один пункт не ссылается на вкладку', () => {
		const all = NAV_TREE.flatMap((e) => (e.kind === 'group' ? e.items : [e]));
		for (const item of all) {
			expect(item.href).not.toContain('?tab=');
		}
	});
});

describe('activeItem', () => {
	it('«Все туннели» на /tunnels', () => {
		expect(activeItem(u('/tunnels'))?.item.id).toBe('tunnels-all');
		expect(activeItem(u('/tunnels?detail=t1'))?.item.id).toBe('tunnels-all');
	});
	it('«Обзор» — первый пункт дерева, «Все туннели» — второй', () => {
		expect(NAV_TREE[0].id).toBe('overview');
		expect(NAV_TREE[1].id).toBe('tunnels-all');
	});
	it('корень → «Обзор»', () => {
		expect(activeItem(u('/'))?.item.id).toBe('overview');
		expect(activeItem(u('/?tab=awg'))?.item.id).toBe('overview');
	});
	it('«Обзор» матчится точным путём, а не префиксом', () => {
		// href '/' с префиксным матчером подсветил бы вообще всё дерево.
		expect(activeItem(u('/settings'))?.item.id).toBe('settings');
		expect(activeItem(u('/tunnels'))?.item.id).toBe('tunnels-all');
		expect(activeItem(u('/nope'))).toBeNull();
	});
	it('AWG-туннели на своём маршруте', () => {
		expect(activeItem(u('/awg/tunnels'))?.item.id).toBe('awg-tunnels');
		expect(activeItem(u('/awg/tunnels?detail=t1'))?.item.id).toBe('awg-tunnels');
	});
	it('детальные страницы туннелей → AWG Туннели', () => {
		expect(activeItem(u('/awg/tunnels/abc'))?.item.id).toBe('awg-tunnels');
		expect(activeItem(u('/awg/tunnels/new'))?.item.id).toBe('awg-tunnels');
		expect(activeItem(u('/awg/tunnels/system/nwg0'))?.item.id).toBe('awg-tunnels');
	});
	it('главная больше не подсвечивает AWG-туннели', () => {
		expect(activeItem(u('/'))?.item.id).not.toBe('awg-tunnels');
		expect(activeItem(u('/?tab=awg'))?.item.id).not.toBe('awg-tunnels');
	});
	it('sing-box туннели на своём маршруте', () => {
		expect(activeItem(u('/sb/tunnels'))?.item.id).toBe('sb-tunnels');
		expect(activeItem(u('/sb/tunnels/tag-1'))?.item.id).toBe('sb-tunnels');
		expect(activeItem(u('/sb/tunnels/new'))?.item.id).toBe('sb-tunnels');
	});
	it('AWG3 на своём маршруте', () => {
		expect(activeItem(u('/sb/awg3'))?.item.id).toBe('sb-awg3');
	});
	it('подписки на своём маршруте', () => {
		expect(activeItem(u('/sb/subscriptions'))?.item.id).toBe('sb-subs');
	});
	it('главная больше не подсвечивает sing-box туннели, AWG3 и подписки', () => {
		expect(activeItem(u('/?tab=singbox'))?.item.id).not.toBe('sb-tunnels');
		expect(activeItem(u('/?tab=awg3'))?.item.id).not.toBe('sb-awg3');
		expect(activeItem(u('/?tab=subscriptions'))?.item.id).not.toBe('sb-subs');
	});
	it('сервисы на своих маршрутах', () => {
		expect(activeItem(u('/services/freeturn'))?.item.id).toBe('svc-freeturn');
		expect(activeItem(u('/services/wdtt'))?.item.id).toBe('svc-wdtt');
	});
	it('главная больше не подсвечивает FreeTurn/WDTT', () => {
		expect(activeItem(u('/?tab=freeturn'))?.item.id).not.toBe('svc-freeturn');
		expect(activeItem(u('/?tab=wdtt'))?.item.id).not.toBe('svc-wdtt');
	});
	it('детальные sb-страницы → свои пункты', () => {
		expect(activeItem(u('/sb/subscriptions/5'))?.item.id).toBe('sb-subs');
	});
	it('разделы роутера на своих маршрутах', () => {
		expect(activeItem(u('/router/ndms'))?.item.id).toBe('router-ndms');
		expect(activeItem(u('/router/ip'))?.item.id).toBe('router-ip');
		expect(activeItem(u('/router/device-vpn'))?.item.id).toBe('router-device-vpn');
		expect(activeItem(u('/router/policies'))?.item.id).toBe('router-policies');
		// ?edit= из поиска — параметр поверхности, на подсветку не влияет.
		expect(activeItem(u('/router/ndms?edit=r1'))?.item.id).toBe('router-ndms');
		expect(activeItem(u('/router/ip?edit=r2'))?.item.id).toBe('router-ip');
	});
	it('HR Neo на своём маршруте', () => {
		expect(activeItem(u('/services/hrneo'))?.item.id).toBe('svc-hrneo');
		expect(activeItem(u('/services/hrneo?edit=r3'))?.item.id).toBe('svc-hrneo');
	});
	it('контейнер /routing мёртв — ничего не подсвечивает', () => {
		expect(activeItem(u('/routing'))).toBeNull();
		expect(activeItem(u('/routing?tab=dns'))).toBeNull();
		expect(activeItem(u('/routing?tab=ip'))).toBeNull();
		expect(activeItem(u('/routing?tab=clientvpn'))).toBeNull();
		expect(activeItem(u('/routing?tab=policy'))).toBeNull();
		expect(activeItem(u('/routing?tab=hrneo'))).toBeNull();
	});
	it('маршрутизация sing-box на своём маршруте', () => {
		expect(activeItem(u('/sb/routing'))?.item.id).toBe('sb-routing');
		// Локальный переключатель поверхности (?view=) на подсветку не влияет.
		expect(activeItem(u('/sb/routing?view=fakeip'))?.item.id).toBe('sb-routing');
		expect(activeItem(u('/sb/routing?add=1&mode=expert'))?.item.id).toBe('sb-routing');
	});
	it('гео-данные на своём маршруте', () => {
		expect(activeItem(u('/sb/geodata'))?.item.id).toBe('sb-geodata');
	});
	it('серверы, журнал и настройки', () => {
		expect(activeItem(u('/awg/servers'))?.item.id).toBe('awg-servers');
		expect(activeItem(u('/awg/servers/managed-asc?id=x'))?.item.id).toBe('awg-servers');
		expect(activeItem(u('/logs'))?.item.id).toBe('logs');
		expect(activeItem(u('/settings'))?.item.id).toBe('settings');
	});
	it('оперативные поверхности — пункты верхнего уровня', () => {
		expect(activeItem(u('/logs'))?.item.id).toBe('logs');
		expect(activeItem(u('/monitoring'))?.item.id).toBe('monitoring');
		expect(activeItem(u('/connections'))?.item.id).toBe('connections');
		expect(activeItem(u('/diagnostics'))?.item.id).toBe('diagnostics');
	});
	it('вкладки диагностики не меняют подсветку раздела', () => {
		expect(activeItem(u('/diagnostics?tab=about'))?.item.id).toBe('diagnostics');
		expect(activeItem(u('/diagnostics?tab=dns'))?.item.id).toBe('diagnostics');
	});
	it('раздела «Инструменты» в дереве больше нет', () => {
		const ids = NAV_TREE.flatMap((e) => (e.kind === 'group' ? e.items.map((i) => i.id) : [e.id]));
		expect(ids).not.toContain('tools');
		expect(activeItem(u('/tools'))).toBeNull();
	});
	it('AWG3-раздел называется Endpoints', () => {
		const sb = NAV_TREE.find((e) => e.kind === 'group' && e.id === 'sb');
		const item = sb?.kind === 'group' ? sb.items.find((i) => i.id === 'sb-awg3') : undefined;
		expect(item?.label).toBe('Endpoints');
	});
	it('неизвестный путь → null', () => {
		expect(activeItem(u('/nope'))).toBeNull();
	});
});

describe('breadcrumbFor', () => {
	it('пункт группы → группа + раздел', () => {
		expect(breadcrumbFor(u('/router/ndms'))).toEqual({ group: 'Роутер', label: 'NDMS' });
		expect(breadcrumbFor(u('/services/hrneo'))).toEqual({ group: 'Сервисы', label: 'HR Neo' });
		expect(breadcrumbFor(u('/sb/routing?view=fakeip'))).toEqual({
			group: 'Sing-box',
			label: 'Маршрутизация',
		});
		expect(breadcrumbFor(u('/sb/geodata'))).toEqual({ group: 'Sing-box', label: 'Гео-данные' });
	});
	it('сервис на своём маршруте → Сервисы + раздел', () => {
		expect(breadcrumbFor(u('/services/freeturn'))).toEqual({ group: 'Сервисы', label: 'FreeTurn' });
		expect(breadcrumbFor(u('/services/wdtt'))).toEqual({ group: 'Сервисы', label: 'WDTT' });
	});
	it('«Все туннели» → плоский пункт без группы', () => {
		expect(breadcrumbFor(u('/tunnels'))).toEqual({ group: null, label: 'Все туннели' });
	});
	it('корень → «Обзор» без группы', () => {
		expect(breadcrumbFor(u('/'))).toEqual({ group: null, label: 'Обзор' });
	});
	it('плоский пункт → без группы', () => {
		expect(breadcrumbFor(u('/settings'))).toEqual({ group: null, label: 'Настройки' });
	});
	it('терминал (вне дерева) → метка без группы', () => {
		expect(breadcrumbFor(u('/terminal'))).toEqual({ group: null, label: 'Терминал' });
	});
	it('неизвестный путь → null', () => {
		expect(breadcrumbFor(u('/nope'))).toBeNull();
	});
});
