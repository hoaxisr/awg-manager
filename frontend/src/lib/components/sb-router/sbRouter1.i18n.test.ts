import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { locale } from '$lib/i18n';

vi.mock('$lib/api/client', () => ({
	api: new Proxy({}, { get: () => vi.fn().mockResolvedValue([]) }),
}));
vi.mock('$lib/stores/singboxRouter', () => ({
	singboxRouter: { loadAll: vi.fn(async () => {}) },
}));

import SimpleDnsPickerModal from './SimpleDnsPickerModal.svelte';
import QosHelpModal from './QosHelpModal.svelte';
import TrafficSourceSettings from './TrafficSourceSettings.svelte';
import type { SingboxRouterSettings } from '$lib/types';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('sb-router i18n (статус и настройки движка)', () => {
	it('SimpleDnsPickerModal: заголовок, пресет и кнопки переключаются на английский', () => {
		const server = { tag: 'dns-direct', type: 'udp' as const, server: '77.88.8.8' };
		render(SimpleDnsPickerModal, {
			props: { server, allowProtocol: true, onclose: vi.fn(), onsaved: vi.fn() },
		});
		expect(screen.getByText('Выходной DNS')).toBeTruthy();
		expect(screen.getByRole('radio', { name: /Яндекс/ })).toBeTruthy();
		expect(screen.getByText('Свой адрес')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Сохранить' })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Upstream DNS')).toBeTruthy();
		expect(screen.getByRole('radio', { name: /Yandex/ })).toBeTruthy();
		expect(screen.getByText('Custom address')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Save' })).toBeTruthy();
		expect(screen.queryByText('Выходной DNS')).toBeNull();
	});

	it('QosHelpModal: текст с разметкой <code> и пример-подсказка переключаются на английский', () => {
		const { baseElement } = render(QosHelpModal, {
			props: { open: true, classes: [], onclose: vi.fn() },
		});
		expect(screen.getByText('Как пометить трафик на ПК')).toBeTruthy();
		expect(baseElement.textContent).toContain('Замените app.exe на имя исполняемого файла');
		expect(baseElement.textContent).toContain('AWGM-Пример');

		locale.set('en');
		flushSync();

		expect(screen.getByText('How to mark traffic on a PC')).toBeTruthy();
		expect(baseElement.textContent).toContain('Replace app.exe with the executable name');
		expect(baseElement.textContent).toContain('AWGM-Example');
		expect(baseElement.textContent).not.toContain('Замените');
	});

	it('TrafficSourceSettings: плюрал «устройств» в <strong> и подписи следуют за языком', () => {
		const cfg = { deviceMode: 'policy', policyName: 'awgm-router' } as SingboxRouterSettings;
		const { container } = render(TrafficSourceSettings, {
			props: { cfg, deviceCount: 3, policyExists: true, variant: 'beginner', onPatch: vi.fn() },
		});
		expect(screen.getByText('Какой трафик обрабатывать')).toBeTruthy();
		expect(container.querySelector('p.hint strong')?.textContent).toBe('3 устройства');
		expect(screen.getByText('Устройства в политике')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Which traffic to process')).toBeTruthy();
		expect(container.querySelector('p.hint strong')?.textContent).toBe('3 devices');
		expect(screen.getByText('Devices in the policy')).toBeTruthy();
		expect(screen.queryByText('Устройства в политике')).toBeNull();
	});
});
