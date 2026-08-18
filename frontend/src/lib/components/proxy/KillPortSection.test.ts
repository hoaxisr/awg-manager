// Секция «Освобождение порта» (EX-46..48 / SH-69..71). Подтверждение — SH-94:
// своя модалка страницы с НОМЕРОМ порта той строки, где нажали, а не браузерный
// confirm (его текст на странице никем не утверждён и в jsdom всегда «нет»).
import { describe, it, expect, vi, beforeEach } from 'vitest';

// Баррель $lib/components/ui тянет theme-store: matchMedia читается на импорте.
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
	lookupProxyListener: vi.fn(),
	killProxyListener: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api: apiMock }));

const notify = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock('$lib/stores/notifications', () => ({ notifications: notify }));

import { render, screen, waitFor, within, fireEvent } from '@testing-library/svelte';
import KillPortSection from './KillPortSection.svelte';

function mount(ports: { listen: string; proto?: 'udp' | 'tcp' }[]) {
	return render(KillPortSection, { props: { ports } });
}

/**
 * Кнопка строки — та, что рядом с текстом про нужный порт. Ждём именно
 * «занят»: до ответа lookup строка говорит «свободен», и её элемент к моменту
 * клика уже отвалится от документа.
 */
async function pressRow(port: number) {
	const el = await screen.findByText(new RegExp(`^Порт ${port} занят`));
	const row = el.closest('.kill-row') as HTMLElement;
	await fireEvent.click(within(row).getByRole('button', { name: 'Освободить порт' }));
	return screen.getByRole('dialog');
}

describe('KillPortSection: подтверждение освобождения порта (SH-94)', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		apiMock.lookupProxyListener.mockImplementation(async (_host: string, port: number) => ({
			open: true,
			pid: 15233,
			comm: port === 56000 ? 'wdtt-server' : 'freeturn-server',
		}));
		apiMock.killProxyListener.mockResolvedValue({ pid: 15233, message: 'PID 15233 остановлен' });
	});

	it('в тексте — номер порта той строки, где нажали', async () => {
		mount([{ listen: '0.0.0.0:56000' }, { listen: '0.0.0.0:56001' }]);

		const first = await pressRow(56000);
		expect(
			within(first).getByText('Освободить порт 56000? Процесс, занявший его, будет завершён.'),
		).toBeTruthy();
		await fireEvent.click(within(first).getByRole('button', { name: 'Отмена' }));
		await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

		const second = await pressRow(56001);
		expect(
			within(second).getByText('Освободить порт 56001? Процесс, занявший его, будет завершён.'),
		).toBeTruthy();
	});

	it('порт освобождается только после подтверждения', async () => {
		mount([{ listen: '0.0.0.0:56000' }]);

		const modal = await pressRow(56000);
		expect(apiMock.killProxyListener).not.toHaveBeenCalled();

		await fireEvent.click(within(modal).getByRole('button', { name: 'Освободить порт' }));
		await waitFor(() =>
			expect(apiMock.killProxyListener).toHaveBeenCalledWith('0.0.0.0', 56000, 'udp'),
		);
		await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
	});
});
