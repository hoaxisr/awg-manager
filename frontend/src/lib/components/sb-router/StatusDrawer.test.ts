// #730: под выбором режима захвата новичку должен быть блок и в policy-tun, и в TPROXY.
import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import type { SingboxRouterSettings, SingboxRouterStatus } from '$lib/types';

const { status, settings, empty, uiMode } = await vi.hoisted(async () => {
	const { writable } = await import('svelte/store');
	return {
		status: writable<Partial<SingboxRouterStatus>>({ enabled: false, active: false, ruleCount: 0 }),
		settings: writable<Partial<SingboxRouterSettings>>({
			routingMode: 'tproxy',
			deviceMode: 'policy',
			policyName: 'p1',
		}),
		empty: writable<never[]>([]),
		uiMode: writable<'beginner' | 'expert'>('beginner'),
	};
});

vi.mock('$lib/stores/singboxRouter', () => ({
	singboxRouter: {
		status: { subscribe: status.subscribe },
		settings: { subscribe: settings.subscribe },
		options: { subscribe: empty.subscribe },
		loadAll: vi.fn(),
		reloadStatus: vi.fn(),
	},
}));

vi.mock('./modeStore', () => ({
	mode: { subscribe: uiMode.subscribe },
	setMode: vi.fn(),
}));

vi.mock('$lib/api/client', () => ({
	api: new Proxy({}, { get: () => vi.fn().mockResolvedValue([]) }),
}));

vi.mock('./settingsActions', async (importOriginal) => {
	const actual = await importOriginal<typeof import('./settingsActions')>();
	return { ...actual, mergeAndSaveSettings: vi.fn() };
});

import { openDrawer } from './drawerStore';
import { mergeAndSaveSettings } from './settingsActions';
import StatusDrawer from './StatusDrawer.svelte';

const patchSpy = mergeAndSaveSettings as unknown as ReturnType<typeof vi.fn>;

describe('#730 простой режим: инфо под выбором режима захвата', () => {
	beforeEach(() => {
		uiMode.set('beginner');
		status.set({ enabled: false, active: false, ruleCount: 0 });
		settings.set({ routingMode: 'tproxy', deviceMode: 'policy', policyName: 'p1' });
		openDrawer();
	});

	it('главный тумблер включён, когда движок работает в FakeIP', () => {
		status.set({ enabled: true, active: true, ruleCount: 3 });
		settings.set({ routingMode: 'fakeip-tun', deviceMode: 'policy', policyName: 'p1' });
		render(StatusDrawer);

		const toggle = screen.getByRole('checkbox', {
			name: 'Включить или выключить движок маршрутизации',
		}) as unknown as HTMLInputElement;
		expect(toggle.checked).toBe(true);
		expect(screen.queryByText('Движок работает')).not.toBeNull();
	});

	it('legacy-настройки без routingMode считаются активным TPROXY', () => {
		status.set({ enabled: true, active: true, ruleCount: 1 });
		settings.set({ deviceMode: 'policy', policyName: 'p1' });
		render(StatusDrawer);

		const toggle = screen.getByRole('checkbox', {
			name: 'Включить или выключить движок маршрутизации',
		}) as unknown as HTMLInputElement;
		expect(toggle.checked).toBe(true);
	});

	it('policy-tun: блок режима виден новичку', () => {
		settings.set({ routingMode: 'policy-tun', deviceMode: 'policy', policyName: 'p1' });
		render(StatusDrawer);
		expect(screen.queryByText(/Режим «Политики \+ tun»/)).not.toBeNull();
	});

	it('эксперт+tproxy: блок есть (режим — несущий элемент)', () => {
		uiMode.set('expert');
		settings.set({ routingMode: 'tproxy', deviceMode: 'policy', policyName: 'p1' });
		render(StatusDrawer);
		expect(screen.queryByText(/Какой трафик обрабатывать/)).not.toBeNull();
	});

	it('tproxy: новичку виден блок источника с переходом в настройки', () => {
		settings.set({ routingMode: 'tproxy', deviceMode: 'policy', policyName: 'p1' });
		render(StatusDrawer);
		expect(screen.queryByText(/Источник трафика/)).not.toBeNull();
		expect(screen.queryByText(/Настроить источник/)).not.toBeNull();
	});
});

describe('policy-tun: название интерфейса', () => {
	beforeEach(() => {
		patchSpy.mockClear();
		uiMode.set('beginner');
		settings.set({ routingMode: 'policy-tun', deviceMode: 'policy', policyName: 'p1' });
		openDrawer();
	});

	it('сохраняет имя по change через applyPatch({ policyTunDescription })', async () => {
		render(StatusDrawer);
		const input = screen.getByLabelText('Название интерфейса');
		await fireEvent.change(input, { target: { value: '  Awgmanager ' } });
		expect(patchSpy).toHaveBeenCalledWith({ policyTunDescription: 'Awgmanager' });
	});

	it('штатное имя сохраняется пустым', async () => {
		settings.set({
			routingMode: 'policy-tun',
			deviceMode: 'policy',
			policyName: 'p1',
			policyTunDescription: 'Awgmanager',
		});
		render(StatusDrawer);
		const input = screen.getByLabelText('Название интерфейса');
		await fireEvent.change(input, { target: { value: 'awgm policy-tun' } });
		expect(patchSpy).toHaveBeenCalledWith({ policyTunDescription: '' });
	});

	it('служебный префикс и длинное имя не уходят на бэкенд', async () => {
		render(StatusDrawer);
		const input = screen.getByLabelText('Название интерфейса');
		await fireEvent.change(input, { target: { value: 'awgm x' } });
		await fireEvent.change(input, { target: { value: 'x'.repeat(33) } });
		expect(patchSpy).not.toHaveBeenCalled();
	});

	it('набираемый текст переживает перечитывание настроек с тем же именем', async () => {
		render(StatusDrawer);
		const input = screen.getByLabelText<HTMLInputElement>('Название интерфейса');
		await fireEvent.input(input, { target: { value: 'Awg' } });
		// Автосохранение другой настройки: новый объект cfg, имя прежнее.
		settings.set({ routingMode: 'policy-tun', deviceMode: 'policy', policyName: 'p1' });
		await tick();
		expect(input.value).toBe('Awg');
	});

	it('отвергнутое бэкендом имя возвращается к сохранённому', async () => {
		patchSpy.mockRejectedValueOnce(new Error('policyTunDescription: control characters are not allowed'));
		settings.set({
			routingMode: 'policy-tun',
			deviceMode: 'policy',
			policyName: 'p1',
			policyTunDescription: 'Old',
		});
		render(StatusDrawer);
		const input = screen.getByLabelText<HTMLInputElement>('Название интерфейса');
		await fireEvent.input(input, { target: { value: 'New' } });
		await fireEvent.change(input, { target: { value: 'New' } });
		await waitFor(() => expect(input.value).toBe('Old'));
	});
});

describe('Анализ трафика: потолок UDP-NAT', () => {
	beforeEach(() => {
		patchSpy.mockClear();
		uiMode.set('expert');
		settings.set({ routingMode: 'tproxy', deviceMode: 'policy', policyName: 'p1' });
		openDrawer();
	});

	it('меняет потолок UDP-NAT через applyPatch({ udpNatMax })', async () => {
		render(StatusDrawer);
		const select = screen.getByLabelText('Потолок UDP-сессий');
		await fireEvent.change(select, { target: { value: '4096' } });
		expect(patchSpy).toHaveBeenCalledWith({ udpNatMax: 4096 });
		await fireEvent.change(select, { target: { value: '' } });
		expect(patchSpy).toHaveBeenCalledWith({ udpNatMax: undefined });
	});
});

describe('Семейства адресов (перехват IPv6)', () => {
	beforeEach(() => {
		patchSpy.mockClear();
		uiMode.set('expert');
		openDrawer();
	});

	it('TPROXY: без поля в настройках выбрано «Только IPv4», смена уходит через applyPatch', async () => {
		settings.set({ routingMode: 'tproxy', deviceMode: 'policy', policyName: 'p1' });
		render(StatusDrawer);
		const select = screen.getByLabelText<HTMLSelectElement>('Перехватывать');
		expect(select.value).toBe('ipv4_only');
		await fireEvent.change(select, { target: { value: 'both' } });
		expect(patchSpy).toHaveBeenCalledWith({ interceptFamily: 'both' });
	});

	it('TPROXY: показывает сохранённое значение', () => {
		settings.set({ routingMode: 'tproxy', deviceMode: 'policy', policyName: 'p1', interceptFamily: 'ipv6_only' });
		render(StatusDrawer);
		expect(screen.getByLabelText<HTMLSelectElement>('Перехватывать').value).toBe('ipv6_only');
	});

	it('policy-tun: выбора нет — семейства задаёт адресация tun', () => {
		settings.set({ routingMode: 'policy-tun', deviceMode: 'policy', policyName: 'p1' });
		render(StatusDrawer);
		expect(screen.queryByLabelText('Перехватывать')).toBeNull();
	});
});

describe('Семейства адресов: отказ бэкенда', () => {
	beforeEach(() => {
		patchSpy.mockClear();
		uiMode.set('expert');
		settings.set({ routingMode: 'tproxy', deviceMode: 'policy', policyName: 'p1' });
		openDrawer();
	});

	it('без пакета ip6tables выбор возвращается к сохранённому', async () => {
		patchSpy.mockRejectedValueOnce(new Error('для перехвата IPv6 нужен пакет ip6tables'));
		render(StatusDrawer);
		const select = screen.getByLabelText<HTMLSelectElement>('Перехватывать');
		await fireEvent.change(select, { target: { value: 'both' } });
		await waitFor(() => expect(select.value).toBe('ipv4_only'));
	});
});

// Бэкенд отвергает IPv6-подсети в исключениях, пока перехват IPv6 выключен
// (иначе запись сохранялась бы и молча ничего не делала). Поле ввода не шлёт
// заведомо отвергаемое и объясняет причину сразу.
describe('Исключения по подсетям: IPv6 только при перехвате IPv6', () => {
	beforeEach(() => {
		patchSpy.mockClear();
		uiMode.set('expert');
		openDrawer();
	});

	async function typeSubnet(value: string) {
		const input = screen.getByLabelText<HTMLInputElement>('Доп. подсети');
		await fireEvent.input(input, { target: { value } });
		await fireEvent.keyDown(input, { key: 'Enter' });
	}

	it('«Только IPv4»: IPv6-подсеть не принимается, запрос не уходит', async () => {
		settings.set({ routingMode: 'tproxy', deviceMode: 'policy', policyName: 'p1' });
		render(StatusDrawer);
		await typeSubnet('2001:db8::/48');
		expect(patchSpy).not.toHaveBeenCalled();
		expect(screen.queryByText(/IPv6-адреса действуют только при включённом перехвате IPv6/)).not.toBeNull();
	});

	it('«Только IPv4»: IPv4-подсеть принимается как раньше', async () => {
		settings.set({ routingMode: 'tproxy', deviceMode: 'policy', policyName: 'p1' });
		render(StatusDrawer);
		await typeSubnet('10.0.0.0/8');
		expect(patchSpy).toHaveBeenCalledWith({ bypassExtraSubnets: '10.0.0.0/8' });
	});

	it('«IPv4 и IPv6»: IPv6-подсеть принимается', async () => {
		settings.set({ routingMode: 'tproxy', deviceMode: 'policy', policyName: 'p1', interceptFamily: 'both' });
		render(StatusDrawer);
		await typeSubnet('2001:db8::/48');
		expect(patchSpy).toHaveBeenCalledWith({ bypassExtraSubnets: '2001:db8::/48' });
	});

	it('policy-tun: IPv6-подсеть не принимается даже при сохранённом «IPv4 и IPv6»', async () => {
		settings.set({ routingMode: 'policy-tun', deviceMode: 'policy', policyName: 'p1', interceptFamily: 'both' });
		render(StatusDrawer);
		await typeSubnet('2001:db8::/48');
		expect(patchSpy).not.toHaveBeenCalled();
	});

	// Дровер управляет только tproxy и policy-tun, и при сохранённом fakeip-tun
	// его целевой режим — tproxy. Бэкенд судит по сохранённому режиму: v6-слоя
	// в fakeip-tun нет, подсеть он отвергнет.
	it('fakeip-tun: IPv6-подсеть не принимается даже при сохранённом «IPv4 и IPv6»', async () => {
		settings.set({ routingMode: 'fakeip-tun', deviceMode: 'policy', policyName: 'p1', interceptFamily: 'both' });
		render(StatusDrawer);
		await typeSubnet('2001:db8::/48');
		expect(patchSpy).not.toHaveBeenCalled();
		// Отказ, а не потерянный ввод: поле объясняет причину.
		expect(screen.queryByText(/IPv6-адреса действуют только при включённом перехвате IPv6/)).not.toBeNull();
	});

	it('оставшаяся IPv6-подсеть при выключенном перехвате видна и помечена недействующей', () => {
		settings.set({
			routingMode: 'tproxy',
			deviceMode: 'policy',
			policyName: 'p1',
			bypassExtraSubnets: '10.0.0.0/8, 2001:db8::/48',
		});
		render(StatusDrawer);
		const inert = screen.getByText('2001:db8::/48', { exact: false }).closest('.port-chip');
		expect(inert?.classList.contains('inert')).toBe(true);
		expect(inert?.getAttribute('title')).toBe('Не действует: перехват IPv6 выключен');
		const live = screen.getByText('10.0.0.0/8', { exact: false }).closest('.port-chip');
		expect(live?.classList.contains('inert')).toBe(false);
	});
});

describe('Mihomo engine clean UI', () => {
	beforeEach(() => {
		uiMode.set('beginner');
		status.set({
			enabled: true,
			active: true,
			ruleCount: 0,
			issues: [
				{ severity: 'warning', kind: 'rule-set-not-found', message: "DNS-правило ссылается на несуществующий rule_set 'xai'" },
				{ severity: 'error', kind: 'engine-dead-interception', message: 'Движок остановлен, но перехват трафика активен' },
			],
		});
		settings.set({
			routingEngine: 'mihomo',
			routingMode: 'tproxy',
			deviceMode: 'policy',
			policyName: 'p1',
		});
		openDrawer();
	});

	it('скрывает блок «Кэш sing-box» при активном движке Mihomo', () => {
		render(StatusDrawer);
		expect(screen.queryByText('Кэш sing-box')).toBeNull();
		expect(screen.queryByText(/Хранилище cache\.db/)).toBeNull();
	});

	it('фильтрует предупреждения sing-box rule_set, но оставляет runtime issues', () => {
		render(StatusDrawer);
		expect(screen.queryByText(/DNS-правило ссылается на несуществующий rule_set/)).toBeNull();
		expect(screen.queryByText(/Движок остановлен, но перехват трафика активен/)).not.toBeNull();
	});
});
