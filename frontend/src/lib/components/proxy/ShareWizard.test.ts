// Мастер «Настроить раздачу»: VK-хеш первого абонента обязателен (амендмент H).
// Сервер здесь создаётся с нуля — подставить в ссылку нечего, и пустое поле
// дало бы абоненту неработающую ссылку.
import { describe, it, expect, vi, beforeEach } from 'vitest';

// Баррель $lib/components/ui тянет theme-store (matchMedia на импорте), панель
// Dropdown — ResizeObserver; в jsdom нет ни того, ни другого.
vi.hoisted(() => {
	Object.defineProperty(globalThis, 'ResizeObserver', {
		writable: true,
		configurable: true,
		value: class {
			observe() {}
			unobserve() {}
			disconnect() {}
		},
	});
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
	getWANIP: vi.fn(),
	createWdttServer: vi.fn(),
	addWdttServerPanelUser: vi.fn(),
	getWdttServerPanelUsers: vi.fn(),
	updateWdttServerInstance: vi.fn(),
	startWdttServerInstance: vi.fn(),
	generateWdttServerLink: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api: apiMock }));

const notify = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock('$lib/stores/notifications', () => ({ notifications: notify }));

import { render, screen, fireEvent } from '@testing-library/svelte';
import ShareWizard from './ShareWizard.svelte';

function mount() {
	return render(ShareWizard, {
		props: {
			wdttServerExists: false,
			serverSupported: true,
			ftClientPort: 0,
			usedPorts: [],
			onclose: () => {},
			ondone: () => {},
		},
	});
}

function next() {
	return screen.getByRole('button', { name: 'Дальше' });
}

/** Шаг 1 — WDTT выбран по умолчанию; шаг 2 требует главный пароль. */
async function toClientStep() {
	await fireEvent.click(next());
	await fireEvent.input(screen.getByLabelText('Главный пароль'), {
		target: { value: 'main1234' },
	});
	await fireEvent.click(next());
}

beforeEach(() => {
	vi.clearAllMocks();
});

describe('шаг «Первый абонент»: VK-хеш', () => {
	it('пустое поле дальше не пускает, заполненное пускает', async () => {
		mount();
		await toClientStep();
		expect(screen.getByLabelText('VK-хеш')).toBeTruthy();
		expect(next().hasAttribute('disabled')).toBe(true);

		// Пробелы значением не считаются: ссылка с ними так же не заработает.
		await fireEvent.input(screen.getByLabelText('VK-хеш'), { target: { value: '  ' } });
		expect(next().hasAttribute('disabled')).toBe(true);

		await fireEvent.input(screen.getByLabelText('VK-хеш'), { target: { value: 'ab12cd34' } });
		expect(next().hasAttribute('disabled')).toBe(false);

		// Шаг действительно пройден: следующий экран — ссылка.
		await fireEvent.click(next());
		expect(screen.getByLabelText('Адрес сервера для абонента')).toBeTruthy();
	});

	it('подпись говорит, что поле обязательное, а не «по желанию»', async () => {
		mount();
		await toClientStep();
		expect(screen.getByText('Обязательно — без него ссылка не заработает')).toBeTruthy();
		expect(screen.queryByText('По желанию')).toBeNull();
	});
});
