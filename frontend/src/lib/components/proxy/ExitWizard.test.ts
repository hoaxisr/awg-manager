// Мастер «Вывести трафик»: вставленный руками .conf (WE-49) переживает разбор
// ТОЙ ЖЕ ссылки. Blur на нетронутом поле источником не считается — иначе
// возврат на шаг 1 стирал бы конфиг, который вставили на шаге 3 (фикс I-2).
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
	decodeFreeTurnLink: vi.fn(),
	decodeWdttLink: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api: apiMock }));

const notify = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock('$lib/stores/notifications', () => ({ notifications: notify }));

import { render, screen, waitFor, fireEvent } from '@testing-library/svelte';
import ExitWizard from './ExitWizard.svelte';

const LINK = 'freeturn://eyJjaWQiOiJhYmMifQ';
const CONF = '[Interface]\nPrivateKey = kkk\n\n[Peer]\nEndpoint = 127.0.0.1:9000';

/** Ссылка FreeTurn без WG-конфига: именно она просит вставить .conf руками. */
const PAYLOAD = {
	cid: 'abc123',
	peer: '203.0.113.5:56000',
	listen: '127.0.0.1:9000',
	n: 4,
	wg: '',
};

function mount() {
	return render(ExitWizard, {
		props: {
			policies: [],
			usedListens: { wdtt: [], freeturn: [] },
			onclose: () => {},
			ondone: () => {},
		},
	});
}

async function next() {
	await fireEvent.click(screen.getByRole('button', { name: 'Дальше' }));
}

/** Поле .conf свёрнуто под кнопкой WE-49 — на каждом заходе на шаг раскрываем. */
async function openConf(): Promise<HTMLTextAreaElement> {
	await fireEvent.click(screen.getByRole('button', { name: 'Вставить клиентский .conf' }));
	return screen.getByLabelText<HTMLTextAreaElement>('WireGuard-конфиг');
}

describe('ExitWizard: вставленный .conf и повторный разбор ссылки', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		apiMock.decodeFreeTurnLink.mockResolvedValue(PAYLOAD);
	});

	it('разбор той же ссылки не стирает .conf, вставленный на шаге 3', async () => {
		mount();
		const linkField = screen.getByLabelText('Ссылка или URL подписки');
		await fireEvent.input(linkField, { target: { value: LINK } });
		await fireEvent.change(linkField, { target: { value: LINK } });
		await waitFor(() => expect(apiMock.decodeFreeTurnLink).toHaveBeenCalled());

		await next();
		await next();
		const conf = await openConf();
		await fireEvent.input(conf, { target: { value: CONF } });
		expect(conf.value).toBe(CONF);

		// Назад к источнику и обратно: поле ссылки не трогали, blur разбирает
		// ТУ ЖЕ ссылку.
		await fireEvent.click(screen.getByRole('button', { name: 'Назад' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Назад' }));
		await fireEvent.change(screen.getByLabelText('Ссылка или URL подписки'), {
			target: { value: LINK },
		});
		await waitFor(() => expect(apiMock.decodeFreeTurnLink).toHaveBeenCalledTimes(2));
		await next();
		await next();

		expect((await openConf()).value).toBe(CONF);
	});

	it('другая ссылка .conf стирает: он относился к прежнему источнику', async () => {
		mount();
		const linkField = screen.getByLabelText('Ссылка или URL подписки');
		await fireEvent.input(linkField, { target: { value: LINK } });
		await fireEvent.change(linkField, { target: { value: LINK } });
		await waitFor(() => expect(apiMock.decodeFreeTurnLink).toHaveBeenCalled());

		await next();
		await next();
		await fireEvent.input(await openConf(), { target: { value: CONF } });

		await fireEvent.click(screen.getByRole('button', { name: 'Назад' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Назад' }));
		const other = `${LINK}ZZ`;
		await fireEvent.input(screen.getByLabelText('Ссылка или URL подписки'), {
			target: { value: other },
		});
		await fireEvent.change(screen.getByLabelText('Ссылка или URL подписки'), {
			target: { value: other },
		});
		await waitFor(() => expect(apiMock.decodeFreeTurnLink).toHaveBeenCalledTimes(2));
		await next();
		await next();

		expect((await openConf()).value).toBe('');
	});
});
