import { describe, it, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, waitFor } from '@testing-library/svelte';
import type { WdttDiagResult } from '$lib/types';

vi.hoisted(() => {
	Object.defineProperty(globalThis, 'matchMedia', {
		writable: true,
		configurable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			onchange: null,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
			dispatchEvent: () => false,
		}),
	});
});

const apiMock = vi.hoisted(() => ({
	runWdttClientDiag: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api: apiMock }));

import WdttDiagModal from './WdttDiagModal.svelte';

function makeError(code: string, message: string, status = 409): Error {
	const err: Error & { status?: number; body?: unknown } = new Error(message);
	err.status = status;
	err.body = { error: true, message, code };
	return err;
}

const okResult: WdttDiagResult = {
	mode: 'dtls',
	transport: 'udp',
	durationMs: 420,
	exitCode: 0,
	steps: [
		{ step: 'turn', title: 'TURN VK', state: 'ok', text: 'udp:193.0.0.1:3478' },
		{ step: 'password', title: 'Проверка пароля', state: 'ok' },
	],
	verdict: {
		code: 'path_ok',
		sev: 'ok',
		title: 'Путь через VK TURN работает',
		text: 'Сервер отвечает, пароль принят.',
		actions: ['Перезапустите клиент'],
		facts: { turn: 'ok', password: 'ok', mtu: 'ok' },
	},
};

const errorVerdictResult: WdttDiagResult = {
	mode: 'raw',
	durationMs: 810,
	exitCode: 2,
	steps: [
		{ step: 'turn', title: 'TURN VK', state: 'ok' },
		{ step: 'password', title: 'Пароль', state: 'fail', text: 'unknown' },
	],
	verdict: {
		code: 'password_unknown',
		sev: 'error',
		title: 'Сервер не принял пароль',
		text: 'Пароль не найден или удалён на сервере.',
		actions: ['Перевыпустите ссылку на сервере'],
	},
};

describe('WdttDiagModal', () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	it('автоматически запускает диагностику при открытии и отображает OK вердикт, шаги и факты', async () => {
		apiMock.runWdttClientDiag.mockResolvedValueOnce(okResult);
		const { findByText } = render(WdttDiagModal, {
			props: {
				open: true,
				instanceId: 'nl',
				instanceName: 'Нидерланды',
				diagKeySet: true,
				onclose: () => {},
			},
		});

		await waitFor(() => expect(apiMock.runWdttClientDiag).toHaveBeenCalledWith('nl', {}));
		expect(await findByText('Путь через VK TURN работает')).toBeTruthy();
		expect(await findByText('Перезапустите клиент')).toBeTruthy();
		expect(await findByText('TURN VK')).toBeTruthy();
		expect(await findByText('turn=ok')).toBeTruthy();
		expect(await findByText('Сохранён из ссылки; оставьте пустым, чтобы использовать его')).toBeTruthy();
	});

	it('отображает ошибочный вердикт при exitCode=2 и пересылает выбранный транспорт при повторе', async () => {
		apiMock.runWdttClientDiag
			.mockResolvedValueOnce(errorVerdictResult)
			.mockResolvedValueOnce(okResult);

		const { findByText, findByRole } = render(WdttDiagModal, {
			props: {
				open: true,
				instanceId: 'nl',
				instanceName: 'Нидерланды',
				onclose: () => {},
			},
		});

		expect(await findByText('Сервер не принял пароль')).toBeTruthy();
		expect(await findByText('Перевыпустите ссылку на сервере')).toBeTruthy();

		await fireEvent.click(await findByRole('button', { name: 'TCP' }));
		await fireEvent.click(await findByRole('button', { name: 'Повторить' }));

		await waitFor(() =>
			expect(apiMock.runWdttClientDiag).toHaveBeenNthCalledWith(2, 'nl', { transport: 'tcp' }),
		);
		expect(await findByText('Путь через VK TURN работает')).toBeTruthy();
	});

	it('показывает понятные сообщения при WDTT_DIAG_UNSUPPORTED и WDTT_DIAG_BUSY', async () => {
		apiMock.runWdttClientDiag.mockRejectedValueOnce(makeError('WDTT_DIAG_UNSUPPORTED', 'flag undefined'));
		const first = render(WdttDiagModal, {
			props: { open: true, instanceId: 'nl', instanceName: 'Нидерланды', onclose: () => {} },
		});
		expect(
			await first.findByText('Установленный wt-client не поддерживает диагностику (-diag). Обновите бинарник WDTT.'),
		).toBeTruthy();
		first.unmount();

		apiMock.runWdttClientDiag.mockRejectedValueOnce(makeError('WDTT_DIAG_BUSY', 'busy'));
		const second = render(WdttDiagModal, {
			props: { open: true, instanceId: 'nl', instanceName: 'Нидерланды', onclose: () => {} },
		});
		expect(
			await second.findByText('Диагностика для этого инстанса уже выполняется. Дождитесь завершения.'),
		).toBeTruthy();
	});

	it('игнорирует устаревший ответ после закрытия модалки', async () => {
		let resolveStale!: (v: WdttDiagResult) => void;
		apiMock.runWdttClientDiag.mockImplementationOnce(
			() =>
				new Promise<WdttDiagResult>((resolve) => {
					resolveStale = resolve;
				}),
		);

		const view = render(WdttDiagModal, {
			props: { open: true, instanceId: 'nl', instanceName: 'Нидерланды', onclose: () => {} },
		});
		await waitFor(() => expect(apiMock.runWdttClientDiag).toHaveBeenCalledTimes(1));

		await view.rerender({
			open: false,
			instanceId: 'nl',
			instanceName: 'Нидерланды',
			onclose: () => {},
		});

		resolveStale(okResult);
		await new Promise((r) => setTimeout(r, 10));

		apiMock.runWdttClientDiag.mockResolvedValueOnce(errorVerdictResult);
		await view.rerender({
			open: true,
			instanceId: 'nl',
			instanceName: 'Нидерланды',
			onclose: () => {},
		});

		expect(await view.findByText('Сервер не принял пароль')).toBeTruthy();
	});
});
