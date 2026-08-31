// Тесты блока «Абоненты» (спека §5): матрица кнопок §4.4, бейдж шапки от
// reload/running, обе ветки частичного успеха добавления, перечитывание после
// отказа, перевыпуск с частичным отказом, переименование.
// Заменяет `wdtt/WdttServerUsers.test.ts` — модель списка сменилась.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { WdttPanelUserEntry, WdttServerConfig } from '$lib/types';

// Баррель $lib/components/ui тянет theme-store: тот читает matchMedia на импорте.
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
	getWdttServerPanelUsers: vi.fn(),
	addWdttServerPanelUser: vi.fn(),
	removeWdttServerPanelUser: vi.fn(),
	renameWdttServerPanelUser: vi.fn(),
	generateWdttServerLink: vi.fn(),
	updateWdttServerInstance: vi.fn(),
	getWANIP: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({ api: apiMock }));

const notify = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
vi.mock('$lib/stores/notifications', () => ({ notifications: notify }));

import { render, screen, waitFor, within, fireEvent } from '@testing-library/svelte';
import ServerClients from './ServerClients.svelte';

function user(p: Partial<WdttPanelUserEntry> & { password: string }): WdttPanelUserEntry {
	return {
		comment: '',
		isAuto: false,
		...p,
	};
}

const MAIN = user({ password: 'mainpass0000', comment: 'Главный' });
const ALIVE = user({ password: 'p-alive', comment: 'Телефон Ивана' });
const OFF = user({ password: 'p-off', comment: 'Планшет' });
const EXPIRED = user({ password: 'p-old', comment: 'Гостевой' });

// Форма ПРОДОВОГО маппера: секрет наружу не уходит (`password: ''`), наличие
// пароля несёт отдельный признак. Фикстура со скрытым паролем прятала
// регрессию — гейт формы читал поле, которого в проде уже нет.
const SERVER: WdttServerConfig = {
	listen: '0.0.0.0:56000',
	wgPort: 51820,
};

const SERVER_NO_PASSWORD: WdttServerConfig = { ...SERVER };

/** Хеши, сохранённые сервером: ровно их ссылка подставит абоненту без своего. */
const SERVER_HASHES = 'srv-hash-aaa,srv-hash-bbb';
const SERVER_WITH_HASHES: WdttServerConfig = { ...SERVER, linkVkHashes: SERVER_HASHES };

function mount(users: WdttPanelUserEntry[], running = true, server: WdttServerConfig = SERVER) {
	apiMock.getWdttServerPanelUsers.mockResolvedValue({ available: true, users });
	return render(ServerClients, {
		props: {
			serverId: 'default',
			serverName: 'Раздача WDTT',
			server,
			running,
			locked: async (fn: () => Promise<void>) => {
				await fn();
			},
		},
	});
}

function rowOf(name: string) {
	return screen.getByText(name).closest('li') as HTMLElement;
}

beforeEach(() => {
	vi.clearAllMocks();
	apiMock.generateWdttServerLink.mockResolvedValue({ link: 'wdtt://x', linkQwdtt: 'qwdtt://x', peer: '' });
	apiMock.updateWdttServerInstance.mockResolvedValue({ config: SERVER });
});

describe('матрица кнопок строки', () => {
	it('у рабочего абонента полный набор действий', async () => {
		mount([ALIVE, OFF, EXPIRED]);
		await waitFor(() => expect(screen.getByText('Телефон Ивана')).toBeTruthy());

		const alive = within(rowOf('Телефон Ивана'));
		expect(alive.getByRole('button', { name: 'Ссылка' }).hasAttribute('disabled')).toBe(false);
		// Живому абоненту ключ тоже меняется — прежде кнопку давала только
		// ветка просрочки, и сменить пароль было нечем.
		expect(alive.getByRole('button', { name: 'Перевыпустить' }).hasAttribute('disabled')).toBe(
			false,
		);
		expect(alive.getByRole('button', { name: 'Удалить' }).hasAttribute('disabled')).toBe(false);

		// Просрочка и деактивация — состояния, которые ставит только форк; у нас
		// их задать нечем, и отдельного набора кнопок под них больше нет.
		const guest = within(rowOf('Гостевой'));
		expect(guest.getByRole('button', { name: 'Ссылка' }).hasAttribute('disabled')).toBe(false);
		expect(guest.getByRole('button', { name: 'Удалить' }).hasAttribute('disabled')).toBe(false);

		// Карандаш есть у всех состояний.
		expect(screen.getAllByRole('button', { name: 'Переименовать абонента' })).toHaveLength(3);
	});

	it('последнего рабочего удалить нельзя', async () => {
		mount([ALIVE, user({ password: '  ', comment: 'Пустой' })]);
		await waitFor(() => expect(screen.getByText('Телефон Ивана')).toBeTruthy());
		expect(
			within(rowOf('Телефон Ивана'))
				.getByRole('button', { name: 'Удалить' })
				.hasAttribute('disabled'),
		).toBe(true);
		// Главный пароль рабочим не считается — рабочий тут ровно один.
		expect(screen.getByText('Абонентов: 2 · рабочих: 1')).toBeTruthy();
	});
});

describe('бейдж шапки', () => {
	it('без мутаций считается по running', async () => {
		mount([ALIVE], false);
		await waitFor(() => expect(screen.getByText('применится при следующем запуске')).toBeTruthy());
	});

	it('после мутации показывает судьбу её reload', async () => {
		mount([ALIVE, OFF], true);
		await waitFor(() => expect(screen.getByText('применено сейчас')).toBeTruthy());
		apiMock.removeWdttServerPanelUser.mockResolvedValue({
			available: true,
			users: [ALIVE],
			reload: 'serverStopped',
		});

		await fireEvent.click(within(rowOf('Планшет')).getByRole('button', { name: 'Удалить' }));
		const dialog = within(screen.getByRole('dialog'));
		expect(screen.getByText('Удалить абонента?')).toBeTruthy();
		await fireEvent.click(dialog.getByRole('button', { name: 'Удалить' }));

		await waitFor(() =>
			expect(screen.getByText('применится при следующем запуске')).toBeTruthy(),
		);
		expect(notify.success).toHaveBeenCalledWith('Абонент «Планшет» удалён');
	});
});

describe('панель ссылок', () => {
	it('смена абонента пересобирает ссылки на его пароль', async () => {
		const OLGA = user({ password: 'p-olga', comment: 'Ноутбук Ольги' });
		mount([ALIVE, OLGA]);
		await waitFor(() => expect(screen.getByText('Ноутбук Ольги')).toBeTruthy());

		await fireEvent.click(within(rowOf('Телефон Ивана')).getByRole('button', { name: 'Ссылка' }));
		await waitFor(() =>
			expect(apiMock.generateWdttServerLink).toHaveBeenCalledWith(
				'default',
				expect.objectContaining({ password: 'p-alive' }),
			),
		);

		// Панель уже открыта на другом абоненте: ссылки обязаны пересобраться,
		// иначе Ольге достанется доступ Ивана.
		await fireEvent.click(within(rowOf('Ноутбук Ольги')).getByRole('button', { name: 'Ссылка' }));
		await waitFor(() =>
			expect(apiMock.generateWdttServerLink).toHaveBeenCalledWith(
				'default',
				expect.objectContaining({ password: 'p-olga' }),
			),
		);
		expect(screen.getByText('Абонент: Ноутбук Ольги')).toBeTruthy();
	});
});

describe('страж гонки ответов (WU-08)', () => {
	it('отставший GET не затирает результат мутации', async () => {
		mount([ALIVE, OFF]);
		await waitFor(() => expect(screen.getByText('Планшет')).toBeTruthy());

		// «Обновить» уходит в полёт и зависает — ответ на него придёт последним.
		let releaseStale: (v: unknown) => void = () => {};
		apiMock.getWdttServerPanelUsers.mockReturnValueOnce(
			new Promise((resolve) => {
				releaseStale = resolve;
			}),
		);
		await fireEvent.click(screen.getByRole('button', { name: 'Обновить' }));

		// Пока GET висит, мутация успевает применить свежий состав.
		apiMock.removeWdttServerPanelUser.mockResolvedValue({
			available: true,
			users: [ALIVE],
			reload: 'delivered',
		});
		await fireEvent.click(within(rowOf('Планшет')).getByRole('button', { name: 'Удалить' }));
		await fireEvent.click(
			within(screen.getByRole('dialog')).getByRole('button', { name: 'Удалить' }),
		);
		await waitFor(() => expect(screen.queryByText('Планшет')).toBeNull());

		// Отставший ответ несёт состав ДО удаления — применить его нельзя.
		releaseStale({ available: true, users: [ALIVE, OFF] });
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'Обновить' }).hasAttribute('disabled')).toBe(
				false,
			),
		);
		expect(screen.queryByText('Планшет')).toBeNull();
		expect(screen.getByText('Абонентов: 1 · рабочих: 1')).toBeTruthy();
	});
});

describe('добавление', () => {
	/** Форма живёт в модалке (Дополнение №3): её открывает кнопка шапки. */
	async function openAddModal() {
		await fireEvent.click(screen.getByRole('button', { name: 'Добавить' }));
		return within(screen.getByRole('dialog'));
	}

	async function fillAndAdd(name: string, password?: string) {
		const dialog = await openAddModal();
		await fireEvent.input(dialog.getByLabelText('Имя абонента'), { target: { value: name } });
		if (password !== undefined) {
			await fireEvent.input(dialog.getByLabelText('Пароль'), { target: { value: password } });
		}
		await fireEvent.click(dialog.getByRole('button', { name: 'Добавить' }));
		return dialog;
	}

	it('успех со своим паролем даёт TS-05 и закрывает модалку', async () => {
		// Хеши у сервера есть: гейт VK-хеша не при чём, проверяется добавление.
		mount([ALIVE], true, SERVER_WITH_HASHES);
		await waitFor(() => expect(screen.getByText('Телефон Ивана')).toBeTruthy());
		apiMock.addWdttServerPanelUser.mockResolvedValue({
			available: true,
			users: [ALIVE, user({ password: 'mine1234', comment: 'Ноутбук' })],
			reload: 'delivered',
		});

		await fillAndAdd('Ноутбук', 'mine1234');
		await waitFor(() =>
			expect(notify.success).toHaveBeenCalledWith('Абонент «Ноутбук» добавлен'),
		);
		expect(apiMock.addWdttServerPanelUser).toHaveBeenCalledWith('default', {
			comment: 'Ноутбук',
			password: 'mine1234',
			vkHash: undefined,
		});
		await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
	});

	it('успех со сгенерированным паролем даёт TS-06', async () => {
		// Хеши у сервера есть: гейт VK-хеша не при чём, проверяется добавление.
		mount([ALIVE], true, SERVER_WITH_HASHES);
		await waitFor(() => expect(screen.getByText('Телефон Ивана')).toBeTruthy());
		apiMock.addWdttServerPanelUser.mockResolvedValue({
			available: true,
			users: [ALIVE, user({ password: 'gen1', comment: 'Ноутбук' })],
			reload: 'delivered',
		});

		await fillAndAdd('Ноутбук');
		await waitFor(() =>
			expect(notify.success).toHaveBeenCalledWith('Абонент «Ноутбук» добавлен, пароль сгенерирован'),
		);
		expect(screen.getByText('Ноутбук')).toBeTruthy();
	});

	it('ADD_NOT_APPLIED показывает SH-26 в модалке и перечитывает список', async () => {
		// Хеши у сервера есть: гейт VK-хеша не при чём, проверяется добавление.
		mount([ALIVE], true, SERVER_WITH_HASHES);
		await waitFor(() => expect(screen.getByText('Телефон Ивана')).toBeTruthy());
		const err: Error & { body?: unknown } = new Error(
			'абонент создан, но не записан в файл сервера: read-only file system',
		);
		err.body = { code: 'WDTT_SERVER_CLIENT_ADD_NOT_APPLIED' };
		apiMock.addWdttServerPanelUser.mockRejectedValue(err);

		const dialog = await fillAndAdd('Ноутбук');
		// Отказ печатается в открытой модалке, а не тостом за ней.
		await waitFor(() =>
			expect(
				dialog.getByText(
					'Абонент создан, но не записан в файл сервера: read-only file system. Сервер подхватит его при следующем запуске.',
				),
			).toBeTruthy(),
		);
		expect(notify.error).not.toHaveBeenCalled();
		expect(screen.getByRole('dialog')).toBeTruthy();
		// ИА §5 п.4: после отказа список перечитывается (первый GET — стартовый).
		await waitFor(() => expect(apiMock.getWdttServerPanelUsers).toHaveBeenCalledTimes(2));
	});

	it('MAIN_PASSWORD_NOT_SAVED показывает текст бэкенда дословно', async () => {
		// Хеши у сервера есть: гейт VK-хеша не при чём, проверяется добавление.
		mount([ALIVE], true, SERVER_WITH_HASHES);
		await waitFor(() => expect(screen.getByText('Телефон Ивана')).toBeTruthy());
		const msg = 'абонент создан, но пароль сервера не сохранён — задайте его в настройках сервера: read-only file system';
		const err: Error & { body?: unknown } = new Error(msg);
		err.body = { code: 'WDTT_SERVER_MAIN_PASSWORD_NOT_SAVED' };
		apiMock.addWdttServerPanelUser.mockRejectedValue(err);

		const dialog = await fillAndAdd('Ноутбук');
		await waitFor(() => expect(dialog.getByText(msg)).toBeTruthy());
		await waitFor(() => expect(apiMock.getWdttServerPanelUsers).toHaveBeenCalledTimes(2));
	});

	it('без пароля владельца управление НЕ заблокировано', async () => {
		// Пароль владельца форку не обязателен: он требует наличия хотя бы
		// одного пароля, и абонентский его закрывает. Прежний гейт (TS-13)
		// запирал единственный путь сделать сервер работоспособным после того,
		// как пароль владельца ушёл из UI.
		apiMock.getWdttServerPanelUsers.mockResolvedValue({ available: true, users: [] });
		render(ServerClients, {
			props: {
				serverId: 'default',
				serverName: 'Раздача WDTT',
				server: SERVER_NO_PASSWORD,
				running: true,
				locked: async (fn: () => Promise<void>) => {
					await fn();
				},
			},
		});
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'Добавить' }).hasAttribute('disabled')).toBe(false),
		);
		expect(screen.queryByText('Сначала задайте главный пароль сервера')).toBeNull();
	});

	it('у сервера С хешами поле необязательное, подпись называет сами хеши', async () => {
		mount([ALIVE], true, SERVER_WITH_HASHES);
		await waitFor(() => expect(screen.getByText('Телефон Ивана')).toBeTruthy());
		apiMock.addWdttServerPanelUser.mockResolvedValue({
			available: true,
			users: [ALIVE, user({ password: 'gen1', comment: 'Ноутбук' })],
			reload: 'delivered',
		});

		const dialog = await openAddModal();
		// Не обещание «подставятся серверные», а сама строка, которая подставится.
		expect(dialog.getByText(SERVER_HASHES)).toBeTruthy();
		expect(dialog.queryByText('Обязательно — без него ссылка не заработает')).toBeNull();

		await fireEvent.input(dialog.getByLabelText('Имя абонента'), { target: { value: 'Ноутбук' } });
		expect(dialog.getByRole('button', { name: 'Добавить' }).hasAttribute('disabled')).toBe(false);
		await fireEvent.click(dialog.getByRole('button', { name: 'Добавить' }));
		await waitFor(() =>
			expect(apiMock.addWdttServerPanelUser).toHaveBeenCalledWith('default', {
				comment: 'Ноутбук',
				password: undefined,
				vkHash: undefined,
			}),
		);
	});

	it('у сервера БЕЗ хешей поле обязательное: пустым не отправить', async () => {
		// У SERVER хешей нет — подставлять абоненту нечего.
		mount([ALIVE]);
		await waitFor(() => expect(screen.getByText('Телефон Ивана')).toBeTruthy());
		apiMock.addWdttServerPanelUser.mockResolvedValue({
			available: true,
			users: [ALIVE, user({ password: 'gen1', comment: 'Ноутбук' })],
			reload: 'delivered',
		});

		const dialog = await openAddModal();
		expect(dialog.getByText('Обязательно — без него ссылка не заработает')).toBeTruthy();
		expect(dialog.queryByText(SERVER_HASHES)).toBeNull();

		await fireEvent.input(dialog.getByLabelText('Имя абонента'), { target: { value: 'Ноутбук' } });
		expect(dialog.getByRole('button', { name: 'Добавить' }).hasAttribute('disabled')).toBe(true);
		await fireEvent.click(dialog.getByRole('button', { name: 'Добавить' }));
		expect(apiMock.addWdttServerPanelUser).not.toHaveBeenCalled();

		await fireEvent.input(dialog.getByLabelText('VK-хеш'), { target: { value: 'own-hash' } });
		expect(dialog.getByRole('button', { name: 'Добавить' }).hasAttribute('disabled')).toBe(false);
		await fireEvent.click(dialog.getByRole('button', { name: 'Добавить' }));
		await waitFor(() =>
			expect(apiMock.addWdttServerPanelUser).toHaveBeenCalledWith('default', {
				comment: 'Ноутбук',
				password: undefined,
				vkHash: 'own-hash',
			}),
		);
	});

	it('пустое имя отправить нельзя, «Отменить» закрывает модалку', async () => {
		// Хеши у сервера есть: гейт VK-хеша не при чём, проверяется добавление.
		mount([ALIVE], true, SERVER_WITH_HASHES);
		await waitFor(() => expect(screen.getByText('Телефон Ивана')).toBeTruthy());

		const dialog = await openAddModal();
		expect(dialog.getByRole('button', { name: 'Добавить' }).hasAttribute('disabled')).toBe(true);
		await fireEvent.click(dialog.getByRole('button', { name: 'Отменить' }));
		await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
		expect(apiMock.addWdttServerPanelUser).not.toHaveBeenCalled();
	});
});

describe('переименование', () => {
	it('карандаш шлёт PATCH и перечитывает список', async () => {
		mount([ALIVE]);
		await waitFor(() => expect(screen.getByText('Телефон Ивана')).toBeTruthy());
		apiMock.renameWdttServerPanelUser.mockResolvedValue({ available: true, users: [ALIVE] });

		await fireEvent.click(screen.getByRole('button', { name: 'Переименовать абонента' }));
		const input = screen.getByLabelText('Имя абонента', { selector: 'input.rename-input' });
		await fireEvent.input(input, { target: { value: 'Телефон Ани' } });
		await fireEvent.keyDown(input, { key: 'Enter' });

		await waitFor(() =>
			expect(apiMock.renameWdttServerPanelUser).toHaveBeenCalledWith(
				'default',
				'p-alive',
				'Телефон Ани',
			),
		);
		await waitFor(() => expect(apiMock.getWdttServerPanelUsers).toHaveBeenCalledTimes(2));
	});

	it('бейдж шапки переименованием не двигается: состав не менялся', async () => {
		mount([ALIVE], false);
		await waitFor(() => expect(screen.getByText('применится при следующем запуске')).toBeTruthy());
		apiMock.renameWdttServerPanelUser.mockResolvedValue({ available: true, users: [ALIVE] });

		await fireEvent.click(screen.getByRole('button', { name: 'Переименовать абонента' }));
		const input = screen.getByLabelText('Имя абонента', { selector: 'input.rename-input' });
		await fireEvent.input(input, { target: { value: 'Телефон Ани' } });
		await fireEvent.keyDown(input, { key: 'Enter' });

		await waitFor(() => expect(apiMock.getWdttServerPanelUsers).toHaveBeenCalledTimes(2));
		// PATCH судьбу SIGHUP не заполняет — признак остаётся по running.
		expect(screen.getByText('применится при следующем запуске')).toBeTruthy();
		expect(screen.queryByText('применено сейчас')).toBeNull();
	});
});

describe('перевыпуск', () => {
	it('обрыв на удалении старой записи показывает TS-09 и не обещает успех', async () => {
		mount([ALIVE, EXPIRED]);
		await waitFor(() => expect(screen.getByText('Гостевой')).toBeTruthy());
		apiMock.addWdttServerPanelUser.mockResolvedValue({
			available: true,
			users: [ALIVE, EXPIRED, user({ password: 'p-new', comment: 'Гостевой' })],
			reload: 'delivered',
		});
		apiMock.removeWdttServerPanelUser.mockRejectedValue(new Error('read-only file system'));

		await fireEvent.click(
			within(rowOf('Гостевой')).getByRole('button', { name: 'Перевыпустить' }),
		);
		const dialog = within(screen.getByRole('dialog'));
		expect(screen.getByText('Перевыпустить абонента?')).toBeTruthy();
		await fireEvent.click(dialog.getByRole('button', { name: 'Перевыпустить' }));

		await waitFor(() =>
			expect(notify.error).toHaveBeenCalledWith(
				'Новый абонент создан, старую запись удалить не удалось: read-only file system',
			),
		);
		expect(notify.success).not.toHaveBeenCalled();
		// Новый абонент заведён с тем же именем; старая запись осталась.
		expect(apiMock.addWdttServerPanelUser).toHaveBeenCalledWith('default', {
			comment: 'Гостевой',
			vkHash: undefined,
		});
	});

	it('успех проходит все четыре шага и даёт TS-08', async () => {
		mount([ALIVE, EXPIRED]);
		await waitFor(() => expect(screen.getByText('Гостевой')).toBeTruthy());
		apiMock.addWdttServerPanelUser.mockResolvedValue({
			available: true,
			users: [ALIVE, EXPIRED, user({ password: 'p-new', comment: 'Гостевой' })],
			reload: 'delivered',
		});
		apiMock.removeWdttServerPanelUser.mockResolvedValue({
			available: true,
			users: [ALIVE, user({ password: 'p-new', comment: 'Гостевой' })],
			reload: 'delivered',
		});

		await fireEvent.click(
			within(rowOf('Гостевой')).getByRole('button', { name: 'Перевыпустить' }),
		);
		await fireEvent.click(
			within(screen.getByRole('dialog')).getByRole('button', { name: 'Перевыпустить' }),
		);

		await waitFor(() =>
			expect(notify.success).toHaveBeenCalledWith(
				'Абонент «Гостевой» перевыпущен — выдайте новую ссылку',
			),
		);
		// Шаг 2: ссылка выдана на НОВЫЙ пароль.
		expect(apiMock.generateWdttServerLink).toHaveBeenCalledWith(
			'default',
			expect.objectContaining({ password: 'p-new' }),
		);
		expect(apiMock.removeWdttServerPanelUser).toHaveBeenCalledWith('default', 'p-old');
		// Шаг 4: список перечитан.
		await waitFor(() => expect(apiMock.getWdttServerPanelUsers).toHaveBeenCalledTimes(2));
	});
});
