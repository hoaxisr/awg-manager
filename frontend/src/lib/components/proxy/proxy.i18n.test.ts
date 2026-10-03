import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import StatusStrip from '../freeturn/StatusStrip.svelte';
import BinaryStrip from './BinaryStrip.svelte';
import Topology from './Topology.svelte';
import { seedGateWarning } from './seedGate';
import { natModeOptions } from './shareConfig';
import type { FreeTurnProcessStatus } from '$lib/types';
import { locale, m } from '$lib/i18n';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('proxy i18n', () => {
	it('StatusStrip: подписи плиток, статус и aria-label переключаются на английский', () => {
		const startedAt = new Date(Date.now() - 14 * 60000).toISOString();
		const client = { running: true, pid: 123, startedAt, binaryPresent: true } as FreeTurnProcessStatus;
		const { container } = render(StatusStrip, {
			props: { client, server: undefined, onToggleClient: vi.fn(), onToggleServer: vi.fn() },
		});
		expect(screen.getByText('Клиент')).toBeTruthy();
		expect(screen.getByText('Сервер')).toBeTruthy();
		expect(screen.getByText('запущен · 14 мин · PID 123')).toBeTruthy();
		expect(screen.getByText('остановлен')).toBeTruthy();
		expect(container.querySelector('[aria-label="Клиент: запустить или остановить"]')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Client')).toBeTruthy();
		expect(screen.getByText('Server')).toBeTruthy();
		expect(screen.getByText('running · 14 min · PID 123')).toBeTruthy();
		expect(screen.getByText('stopped')).toBeTruthy();
		expect(container.querySelector('[aria-label="Client: start or stop"]')).toBeTruthy();
	});

	it('BinaryStrip: бейджи версий и кнопки установки переключаются на английский', () => {
		render(BinaryStrip, {
			props: {
				binaries: [
					{
						name: 'wdtt',
						binaryPresent: true,
						installAvailable: true,
						installing: false,
						updateAvailable: true,
						installedVersion: 'v1.0',
						installVersion: 'v1.1',
						oninstall: vi.fn(),
					},
					{
						name: 'freeturn',
						binaryPresent: false,
						installAvailable: true,
						installing: false,
						updateAvailable: false,
						oninstall: vi.fn(),
					},
				],
			},
		});
		expect(screen.getByText('установлен v1.0')).toBeTruthy();
		expect(screen.getByText('доступно обновление v1.1')).toBeTruthy();
		expect(screen.getByText('не установлен')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Обновить' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Установить' })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('installed v1.0')).toBeTruthy();
		expect(screen.getByText('update available v1.1')).toBeTruthy();
		expect(screen.getByText('not installed')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Update' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Install' })).toBeTruthy();
	});

	it('Topology: заголовки колонок переключаются на английский', () => {
		const { container } = render(Topology, {
			props: { inbound: [{ who: 'Alice', how: 'пароль' }], name: 'srv', routerLines: [] },
		});
		const titles = () => [...container.querySelectorAll('.col-title')].map((e) => e.textContent);
		expect(titles()).toEqual(['Абоненты', 'Этот роутер', 'Выход']);

		locale.set('en');
		flushSync();

		expect(titles()).toEqual(['Subscribers', 'This router', 'Exit']);
	});

	it('функции модулей и плюралы следуют за языком', () => {
		const seed = { seeded: true, certified: false, skipped: [{ file: 'old.json', reason: 'bad json' }] };
		expect(seedGateWarning(seed)).toContain('старый конфиг old.json не разобран');
		expect(natModeOptions().map((o) => o.label)).toEqual(['Полный', 'Интернет', 'Без NAT']);
		expect(m.proxy_captcha_waiting({ streams: 1 })).toBe('Ожидает подтверждения: 1 поток');
		expect(m.proxy_captcha_waiting({ streams: 3 })).toBe('Ожидает подтверждения: 3 потока');
		expect(m.proxy_captcha_waiting({ streams: 5 })).toBe('Ожидает подтверждения: 5 потоков');

		locale.set('en');

		expect(seedGateWarning(seed)).toContain('legacy config old.json could not be parsed');
		expect(natModeOptions().map((o) => o.label)).toEqual(['Full', 'Internet', 'No NAT']);
		expect(m.proxy_captcha_waiting({ streams: 1 })).toBe('Waiting for confirmation: 1 stream');
		expect(m.proxy_captcha_waiting({ streams: 3 })).toBe('Waiting for confirmation: 3 streams');
	});
});
