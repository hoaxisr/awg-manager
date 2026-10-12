import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { locale } from '$lib/i18n';

vi.mock('$lib/api/client', () => ({
	api: new Proxy({}, { get: () => vi.fn().mockResolvedValue([]) }),
}));

import NotEnabledScreen from './NotEnabledScreen.svelte';
import TunInboundCard from './inbounds/TunInboundCard.svelte';
import DeviceProxyInboundCard from './inbounds/DeviceProxyInboundCard.svelte';
import { stepDefsFor } from './switchSteps';
import { humanLabel, switchConsequences } from './switchConsequences';
import { formatDelay } from './outbounds/formatDelay';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('fakeip i18n', () => {
	it('NotEnabledScreen: заголовок и кнопка переключаются на английский', () => {
		render(NotEnabledScreen, { props: { onEnableRequested: vi.fn() } });
		expect(screen.getByText('Режим FakeIP не включён')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Включить FakeIP' })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('FakeIP mode is not enabled')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Enable FakeIP' })).toBeTruthy();
	});

	it('NotEnabledScreen: причина недоступности из пропа имеет приоритет над текстом по умолчанию', () => {
		render(NotEnabledScreen, {
			props: { onEnableRequested: vi.fn(), unavailableReason: 'Нужна прошивка 5.x' },
		});
		expect(screen.getByText('Нужна прошивка 5.x')).toBeTruthy();
		expect(screen.queryByText(/Сейчас активен другой режим/)).toBeNull();
	});

	it('TunInboundCard: подписи строк и бейдж переключаются', () => {
		render(TunInboundCard, { props: { iface: 'opkgtun10', live: true } });
		expect(screen.getByText('ядро fakeip')).toBeTruthy();
		expect(screen.getByText('стек · MTU')).toBeTruthy();
		expect(screen.getByText('управляется движком')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('fakeip core')).toBeTruthy();
		expect(screen.getByText('stack · MTU')).toBeTruthy();
		expect(screen.getByText('managed by the engine')).toBeTruthy();
	});

	it('DeviceProxyInboundCard: aria-label удаления берёт имя, а переключатель — состояние', () => {
		render(DeviceProxyInboundCard, {
			props: {
				name: 'lan-1',
				listen: '192.168.1.1:1080',
				authEnabled: true,
				enabled: true,
				alive: true,
				onEdit: vi.fn(),
				onToggle: vi.fn(),
				onDelete: vi.fn(),
			},
		});
		expect(screen.getByLabelText('Удалить inbound «lan-1»')).toBeTruthy();
		expect(screen.getByText('вкл')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByLabelText(/lan-1/)).toBeTruthy();
		expect(screen.getByText('on')).toBeTruthy();
		expect(screen.getByText('authentication')).toBeTruthy();
	});

	it('функции без компонента следуют за локалью при вызове', () => {
		expect(humanLabel('off')).toBe('Выключен');
		expect(formatDelay(0)).toBe('таймаут');
		expect(stepDefsFor('off', 'tproxy')[0].title).toBe('Снят предыдущий режим');
		expect(switchConsequences('fakeip-tun', 'off')[0]).toMatch(/^Снятие fakeip/);

		locale.set('en');
		flushSync();

		expect(humanLabel('off')).toBe('Off');
		expect(formatDelay(0)).toBe('timeout');
		expect(stepDefsFor('off', 'tproxy')[0].title).toBe('Previous mode removed');
		expect(switchConsequences('off', 'off')).toEqual(['Routing is disabled.']);
	});
});
