import { describe, it, expect } from 'vitest';
import { NAV_TREE, activeItem, breadcrumbFor } from './navigation';

const u = (path: string) => new URL(`http://router${path}`);

describe('NAV_TREE hrefs', () => {
	// Вкладка по умолчанию вычищается из URL, поэтому переход на голый путь
	// с ЧУЖОЙ вкладки не переключает Tabs (пункт становится мёртвым).
	// Такие пункты обязаны указывать вкладку явно.
	it.each([
		['awg-tunnels', '/?tab=awg'],
		['router-ndms', '/routing?tab=dns'],
	])('%s указывает вкладку явно', (id, href) => {
		const all = NAV_TREE.flatMap((e) => (e.kind === 'group' ? e.items : [e]));
		const item = all.find((i) => i.id === id);
		expect(item?.href).toBe(href);
		expect(item?.href).toContain('?tab=');
	});
});

describe('activeItem', () => {
	it('/ и /?tab=awg → AWG Туннели', () => {
		expect(activeItem(u('/'))?.item.id).toBe('awg-tunnels');
		expect(activeItem(u('/?tab=awg'))?.item.id).toBe('awg-tunnels');
	});
	it('детальные страницы туннелей → AWG Туннели', () => {
		expect(activeItem(u('/tunnels/abc'))?.item.id).toBe('awg-tunnels');
		expect(activeItem(u('/system-tunnels/nwg0'))?.item.id).toBe('awg-tunnels');
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
	it('вкладки /routing → пункты Роутер/Sing-box', () => {
		expect(activeItem(u('/routing?tab=dns'))?.item.id).toBe('router-ndms');
		expect(activeItem(u('/routing?tab=ip'))?.item.id).toBe('router-ip');
		expect(activeItem(u('/routing?tab=clientvpn'))?.item.id).toBe('router-device-vpn');
		expect(activeItem(u('/routing?tab=policy'))?.item.id).toBe('router-policies');
		expect(activeItem(u('/routing?tab=singbox'))?.item.id).toBe('sb-routing');
		expect(activeItem(u('/routing?tab=fakeip'))?.item.id).toBe('sb-routing');
		expect(activeItem(u('/routing?tab=geodata'))?.item.id).toBe('sb-geodata');
		expect(activeItem(u('/routing?tab=hrneo'))?.item.id).toBe('svc-hrneo');
		// Голый /routing = вкладка по умолчанию (NDMS): Tabs вычищает ?tab=dns.
		expect(activeItem(u('/routing'))?.item.id).toBe('router-ndms');
	});
	it('серверы и инструменты', () => {
		expect(activeItem(u('/awg/servers'))?.item.id).toBe('awg-servers');
		expect(activeItem(u('/awg/servers/managed-asc?id=x'))?.item.id).toBe('awg-servers');
		expect(activeItem(u('/tools?tab=logs'))?.item.id).toBe('tools');
		expect(activeItem(u('/settings'))?.item.id).toBe('settings');
	});
	it('неизвестный путь → null', () => {
		expect(activeItem(u('/nope'))).toBeNull();
	});
});

describe('breadcrumbFor', () => {
	it('пункт группы → группа + раздел', () => {
		expect(breadcrumbFor(u('/routing?tab=dns'))).toEqual({ group: 'Роутер', label: 'NDMS' });
	});
	it('сервис на своём маршруте → Сервисы + раздел', () => {
		expect(breadcrumbFor(u('/services/freeturn'))).toEqual({ group: 'Сервисы', label: 'FreeTurn' });
		expect(breadcrumbFor(u('/services/wdtt'))).toEqual({ group: 'Сервисы', label: 'WDTT' });
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
