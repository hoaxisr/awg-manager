import { describe, it, expect } from 'vitest';
import { activeItem, breadcrumbFor } from './navigation';

const u = (path: string) => new URL(`http://router${path}`);

describe('activeItem', () => {
	it('/ и /?tab=awg → AWG Туннели', () => {
		expect(activeItem(u('/'))?.item.id).toBe('awg-tunnels');
		expect(activeItem(u('/?tab=awg'))?.item.id).toBe('awg-tunnels');
	});
	it('детальные страницы туннелей → AWG Туннели', () => {
		expect(activeItem(u('/tunnels/abc'))?.item.id).toBe('awg-tunnels');
		expect(activeItem(u('/system-tunnels/nwg0'))?.item.id).toBe('awg-tunnels');
	});
	it('вкладки главной → пункты Sing-box/Сервисы', () => {
		expect(activeItem(u('/?tab=singbox'))?.item.id).toBe('sb-tunnels');
		expect(activeItem(u('/?tab=awg3'))?.item.id).toBe('sb-awg3');
		expect(activeItem(u('/?tab=subscriptions'))?.item.id).toBe('sb-subs');
		expect(activeItem(u('/?tab=freeturn'))?.item.id).toBe('svc-freeturn');
		expect(activeItem(u('/?tab=wdtt'))?.item.id).toBe('svc-wdtt');
	});
	it('детальные sb-страницы → свои пункты', () => {
		expect(activeItem(u('/singbox/tag-1'))?.item.id).toBe('sb-tunnels');
		expect(activeItem(u('/subscriptions/5'))?.item.id).toBe('sb-subs');
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
		expect(activeItem(u('/routing'))?.item.id).toBe('sb-routing');
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
