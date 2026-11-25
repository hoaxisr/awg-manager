import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import UplinkTab from './UplinkTab.svelte';
import { api } from '$lib/api/client';
import type { UplinkSnapshot } from '$lib/types/uplink';

vi.mock('$lib/api/client', () => ({ api: {
	uplinkSnapshot: vi.fn(), singboxListTunnels: vi.fn(), fircGetTunnels: vi.fn(), listSubscriptions: vi.fn(),
	uplinkSave: vi.fn(), uplinkSwitch: vi.fn(), uplinkDelete: vi.fn(),
	uplinkDNS: vi.fn(), uplinkSaveDNS: vi.fn(),
} }));

const snapshot: UplinkSnapshot = {
	providers: [
		{ id: 'ISP1', device: 'wan1', label: 'Основной', up: true },
		{ id: 'ISP2', device: 'wan2', label: 'Резервный', up: true },
	], automaticFailback: false,
	entries: [{ policy: {
		id: 'stable-id', revision: 7, target: { engine: 'firc', ref: 'tunnel-a' },
		providers: ['ISP1', 'ISP2'], enabled: true, intervalSeconds: 10, timeoutSeconds: 4,
		failureThreshold: 3, holdDownSeconds: 60,
	}, state: { phase: 'healthy', provider: 'ISP2' } }],
};

beforeEach(() => {
	vi.resetAllMocks();
	vi.mocked(api.uplinkSnapshot).mockResolvedValue(structuredClone(snapshot));
	vi.mocked(api.singboxListTunnels).mockResolvedValue([]);
	vi.mocked(api.fircGetTunnels).mockResolvedValue([]);
	vi.mocked(api.listSubscriptions).mockResolvedValue([]);
	vi.mocked(api.uplinkSwitch).mockResolvedValue([]);
});
afterEach(cleanup);

describe('UplinkTab', () => {
	it('saves provider DNS without mutating tunnel policies and restores automatic mode', async () => {
		vi.mocked(api.uplinkDNS).mockResolvedValue({revision:3,overrides:{}});
		vi.mocked(api.uplinkSaveDNS).mockResolvedValue({revision:4,overrides:{ISP1:['1.1.1.1']}});
		render(UplinkTab);
		await fireEvent.click((await screen.findAllByText('DNS'))[0]);
		await fireEvent.change(screen.getByLabelText('Источник DNS'),{target:{value:'true'}});
		await fireEvent.input(screen.getByLabelText('IP-адреса DNS-серверов'),{target:{value:'1.1.1.1, 8.8.8.8'}});
		await fireEvent.click(screen.getByText('Сохранить'));
		await waitFor(()=>expect(api.uplinkSaveDNS).toHaveBeenCalledWith('ISP1',3,['1.1.1.1','8.8.8.8']));
		expect(api.uplinkSave).not.toHaveBeenCalled();
		vi.mocked(api.uplinkDNS).mockResolvedValue({revision:4,overrides:{ISP1:['1.1.1.1']}});
		await waitFor(()=>expect(screen.queryByLabelText('Источник DNS')).toBeNull());
		await fireEvent.click(screen.getAllByText('DNS')[0]);
		await screen.findByLabelText('Источник DNS');
		const dnsInput = screen.getByLabelText('IP-адреса DNS-серверов');
		expect(dnsInput instanceof HTMLInputElement ? dnsInput.value : '').toBe('1.1.1.1');
		await fireEvent.change(screen.getByLabelText('Источник DNS'),{target:{value:'false'}});
		await fireEvent.click(screen.getByText('Сохранить'));
		await waitFor(()=>expect(api.uplinkSaveDNS).toHaveBeenLastCalledWith('ISP1',4,[]));
	});
	it('allows FIRC targets that prepare a local pool listener on save', async () => {
		const data=structuredClone(snapshot);
		data.targets=[{target:{engine:'firc',ref:'pool'},label:'FIRC pool',unavailableReason:'Listener will be prepared',setupRequired:true}];
		vi.mocked(api.uplinkSnapshot).mockResolvedValue(data);
		render(UplinkTab);
		const option=await screen.findByText('FIRC pool — Listener will be prepared');
		expect(option.hasAttribute('disabled')).toBe(false);
		await fireEvent.change(screen.getByLabelText('Туннель'),{target:{value:JSON.stringify(['firc','pool',''])}});
		await fireEvent.click(screen.getByText('Настроить'));
		expect(screen.getByText('Сохранить')).toBeTruthy();
	});
	it('offers controlled return without pretending an independent probe exists', async () => {
		const data=structuredClone(snapshot);
		data.entries[0].capabilities={independentProbe:false,automaticFailback:false,controlledFailback:true,restartScope:'target'};
		vi.mocked(api.uplinkSnapshot).mockResolvedValue(data);
		vi.mocked(api.uplinkSave).mockImplementation(async p=>structuredClone(p));
		render(UplinkTab);await fireEvent.click(await screen.findByText('Изменить'));
		const select=screen.getByLabelText('Возврат на основной');
		await fireEvent.change(select,{target:{value:'controlled'}});
		expect(screen.getByText(/После устойчивого появления основного WAN/)).toBeTruthy();
		await fireEvent.submit(screen.getByText('Сохранить').closest('form')!);
		await waitFor(()=>expect(api.uplinkSave).toHaveBeenCalledOnce());
		expect(vi.mocked(api.uplinkSave).mock.calls[0][0].failback).toBe('controlled');
	});
	it('keeps kernel AWG in the common editor and explains original routing on removal', async () => {
		const data = structuredClone(snapshot);
		data.entries[0].policy.target = {engine:'kernel-amneziawg',ref:'awg10'};
		data.targets = [{target:{engine:'kernel-amneziawg',ref:'awg10'},label:'Sel · kernel AmneziaWG'}];
		vi.mocked(api.uplinkSnapshot).mockResolvedValue(data);
		render(UplinkTab);
		await screen.findByText('Удаление этой политики вернёт привязку к провайдеру, которая была до резервирования.');
		await fireEvent.click(screen.getByText('Изменить'));
		expect(screen.getByText('После отключения или удаления политики вернётся исходная привязка к провайдеру.')).toBeTruthy();
		expect(screen.getByText('Автоматически после проверки').hasAttribute('disabled')).toBe(true);
	});
	it('uses one catalog for proxies, subscriptions and unavailable system targets', async () => {
		const data = structuredClone(snapshot);
		data.targets = [
			{target:{engine:'firc',ref:'proxy'},label:'Proxy catalog'},
			{target:{engine:'sing-box',ref:'sub',kind:'subscription'},label:'Subscription catalog'},
			{target:{engine:'system-wireguard',ref:'Wireguard1'},label:'System catalog',unavailableReason:'Adapter pending'},
		];
		vi.mocked(api.uplinkSnapshot).mockResolvedValue(data);
		render(UplinkTab);
		await screen.findByText('Proxy catalog');
		expect(screen.getByText('Subscription catalog')).toBeTruthy();
		expect(screen.getByText('System catalog — Adapter pending').hasAttribute('disabled')).toBe(true);
		expect(api.singboxListTunnels).not.toHaveBeenCalled();
		expect(api.fircGetTunnels).not.toHaveBeenCalled();
		expect(api.listSubscriptions).not.toHaveBeenCalled();
	});
	it('discovers native WireGuard targets from the authenticated snapshot', async () => {
		const data = structuredClone(snapshot);
		data.targets = [{ target: { engine: 'wireguard', ref: 'awg50' }, label: 'Native WG' }];
		data.capabilities = { wireguard: { independentEngine: true, independentProbe: false, automaticFailback: false, restartScope: 'target' } };
		vi.mocked(api.uplinkSnapshot).mockResolvedValue(data);
		vi.mocked(api.uplinkSave).mockImplementation(async (policy) => structuredClone(policy));
		render(UplinkTab);
		await screen.findByText('Native WG');
		await fireEvent.change(screen.getByLabelText('Туннель'), { target: { value: JSON.stringify(['wireguard', 'awg50', '']) } });
		await fireEvent.click(screen.getByText('Настроить'));
		expect(screen.getByText('Автоматически после проверки').hasAttribute('disabled')).toBe(true);
		await fireEvent.change(screen.getByLabelText('Основной провайдер'), { target: { value: 'ISP1' } });
		await fireEvent.change(screen.getByLabelText('Резерв 1'), { target: { value: 'ISP2' } });
		await fireEvent.submit(screen.getByText('Сохранить').closest('form')!);
		await waitFor(() => expect(api.uplinkSave).toHaveBeenCalledOnce());
		expect(vi.mocked(api.uplinkSave).mock.calls[0][0].target).toEqual({ engine: 'wireguard', ref: 'awg50' });
	});
	it('supports pinned mode without a hidden fallback', async () => {
		render(UplinkTab);
		await fireEvent.click(await screen.findByText('Изменить'));
		await fireEvent.change(screen.getByLabelText('Режим'), { target: { value: 'pinned' } });
		expect(screen.queryByLabelText('Резерв 1')).toBeNull();
		await fireEvent.submit(screen.getByText('Сохранить').closest('form')!);
		await waitFor(() => expect(api.uplinkSave).toHaveBeenCalledOnce());
		expect(vi.mocked(api.uplinkSave).mock.calls[0][0].providers).toEqual(['ISP1']);
		expect(vi.mocked(api.uplinkSave).mock.calls[0][0].mode).toBe('pinned');
	});
	it('disables automatic return without independent candidate support', async () => {
		render(UplinkTab);
		await fireEvent.click(await screen.findByText('Изменить'));
		expect(screen.getByText('Автоматически после проверки').hasAttribute('disabled')).toBe(true);
	});
	it('редактирует реактивную политику, сохраняя ID, revision и порядок WAN', async () => {
		render(UplinkTab);
		await fireEvent.click(await screen.findByText('Изменить'));
		await fireEvent.input(screen.getByLabelText('Интервал проверки, сек.'), { target: { value: '25' } });
		vi.mocked(api.uplinkSave).mockImplementation(async (policy) => structuredClone(policy));
		await fireEvent.submit(screen.getByText('Сохранить').closest('form')!);
		await waitFor(() => expect(api.uplinkSave).toHaveBeenCalledOnce());
		expect(api.uplinkSave).toHaveBeenCalledWith({ ...snapshot.entries[0].policy, intervalSeconds: 25 });
		await waitFor(() => expect(screen.queryByText('Сохранить')).toBeNull());
	});

	it('ручной возврат передаёт точный WAN и revision, ошибку показывает пользователю', async () => {
		vi.mocked(api.uplinkSwitch).mockRejectedValue(new Error('requested path did not pass verification'));
		render(UplinkTab);
		await fireEvent.click(await screen.findByText('Перейти на Основной'));
		await waitFor(() => expect(api.uplinkSwitch).toHaveBeenCalledWith('stable-id', 'ISP1', 7));
		expect((await screen.findByRole('alert')).textContent).toContain('requested path did not pass verification');
	});

	it('настраивает подписку отдельно от туннеля с таким же ref', async () => {
		const data = structuredClone(snapshot);
		data.targets = [
			{target:{engine:'sing-box',ref:'sub-id'},label:'sub-id · sing-box'},
			{target:{engine:'sing-box',ref:'sub-id',kind:'subscription'},label:'Моя подписка · подписка sing-box'},
		];
		vi.mocked(api.uplinkSnapshot).mockResolvedValue(data);
		data.entries[0].policy.target = { engine: 'sing-box', ref: 'sub-id' };
		vi.mocked(api.uplinkSnapshot).mockResolvedValue(data);
		vi.mocked(api.listSubscriptions).mockResolvedValue([{ id: 'sub-id', label: 'Моя подписка' }] as Awaited<ReturnType<typeof api.listSubscriptions>>);
		vi.mocked(api.uplinkSave).mockImplementation(async (policy) => structuredClone(policy));
		render(UplinkTab);
		await screen.findByText('Моя подписка · подписка sing-box');
		await fireEvent.change(screen.getByLabelText('Туннель'), { target: { value: JSON.stringify(['sing-box', 'sub-id', 'subscription']) } });
		await fireEvent.click(screen.getByText('Настроить'));
		await fireEvent.change(screen.getByLabelText('Основной провайдер'), { target: { value: 'ISP1' } });
		await fireEvent.change(screen.getByLabelText('Резерв 1'), { target: { value: 'ISP2' } });
		await fireEvent.submit(screen.getByText('Сохранить').closest('form')!);
		await waitFor(() => expect(api.uplinkSave).toHaveBeenCalledOnce());
		const saved = vi.mocked(api.uplinkSave).mock.calls[0][0];
		expect(saved.target).toEqual({ engine: 'sing-box', ref: 'sub-id', kind: 'subscription' });
		expect(saved.revision).toBe(0);
		expect(saved.id).not.toBe('stable-id');
	});

	it('requires confirmation before deleting a policy and updates policy count', async () => {
		const onCountChange = vi.fn();
		vi.mocked(api.uplinkDelete).mockResolvedValue(undefined);
		render(UplinkTab, { props: { onCountChange } });
		await screen.findByText('Каналы WAN роутера');
		expect((await screen.findAllByText('wan1')).length).toBeGreaterThanOrEqual(2);
		expect(onCountChange).toHaveBeenCalledWith(1);

		const removeBtn = await screen.findByRole('button', { name: 'Убрать резервирование' });
		await fireEvent.click(removeBtn);
		expect(api.uplinkDelete).not.toHaveBeenCalled();
		await screen.findByText('Удалить правило Мульти-WAN?');

		const updated = structuredClone(snapshot);
		updated.entries = [];
		vi.mocked(api.uplinkSnapshot).mockResolvedValue(updated);
		const confirmButtons = screen.getAllByRole('button', { name: 'Убрать резервирование' });
		await fireEvent.click(confirmButtons[confirmButtons.length - 1]);
		await waitFor(() => expect(api.uplinkDelete).toHaveBeenCalledWith('stable-id', 7));
		await waitFor(() => expect(onCountChange).toHaveBeenCalledWith(0));
		await screen.findByText('Правила Мульти-WAN пока не настроены');
	});

	it('renders configured WAN DNS override pill on WAN chip and validates server count in drawer', async () => {
		vi.mocked(api.uplinkDNS).mockResolvedValue({ revision: 5, overrides: { ISP1: ['1.1.1.1'] } });
		render(UplinkTab);
		await screen.findByText('1.1.1.1');

		await fireEvent.click((await screen.findAllByText('DNS'))[0]);
		await screen.findByLabelText('Источник DNS');
		await fireEvent.input(screen.getByLabelText('IP-адреса DNS-серверов'), {
			target: { value: '1.1.1.1, 1.0.0.1, 8.8.8.8, 8.8.4.4, 9.9.9.9, 149.112.112.112, 208.67.222.222, 208.67.220.220, 77.88.8.8' },
		});
		await fireEvent.click(screen.getByText('Сохранить'));
		expect(api.uplinkSaveDNS).not.toHaveBeenCalled();
		expect((await screen.findByRole('alert')).textContent).toContain('Укажите от 1 до 8 DNS-серверов.');
	});

	it('localizes known backend capability limitation notices in the policy drawer', async () => {
		const data = structuredClone(snapshot);
		data.entries[0].capabilities = {
			independentProbe: false,
			automaticFailback: false,
			controlledFailback: true,
			restartScope: 'target',
			limitation:
				'AWGM Route supports manual or controlled return with reconnection; independent candidate probe unavailable',
		};
		vi.mocked(api.uplinkSnapshot).mockResolvedValue(data);
		render(UplinkTab);
		await fireEvent.click(await screen.findByText('Изменить'));
		expect(
			await screen.findByText(
				'AWGM Route поддерживает ручной или контролируемый возврат с переподключением; фоновая проверка резервного канала без переключения недоступна',
			),
		).toBeTruthy();
	});
});
