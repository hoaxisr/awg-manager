import '@testing-library/jest-dom/vitest';
import { tick } from 'svelte';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, waitFor } from '@testing-library/svelte';
import type { ZapretHostlistContent, ZapretHostlistInfo, ZapretStatus, ZapretStrategies } from '$lib/types';

const mocks = vi.hoisted(() => ({
	getStrategies: vi.fn(), getHostlists: vi.fn(), getHostlist: vi.fn(), saveHostlist: vi.fn(), error: vi.fn(),
	setStatus: (_data: ZapretStatus) => {}
}));
vi.mock('$lib/api/client', () => ({ api: {
	getZapretStrategies: mocks.getStrategies,
	getZapretHostlists: mocks.getHostlists,
	getZapretHostlist: mocks.getHostlist,
	saveZapretHostlist: mocks.saveHostlist
} }));
vi.mock('$lib/stores/zapret', async () => {
	const { writable } = await import('svelte/store');
	const store = writable({ data: null as ZapretStatus | null, lastFetchedAt: 1, status: 'success' });
	mocks.setStatus = (data) => store.set({ data, lastFetchedAt: 1, status: 'success' });
	return { zapretStatus: { ...store, invalidate: vi.fn() } };
});
vi.mock('$lib/stores/notifications', () => ({ notifications: { success: vi.fn(), error: mocks.error } }));
import ZapretTab from './ZapretTab.svelte';

function status(base: string): ZapretStatus {
	return { installed: true, running: true, base, processState: 'running', strategyCount: 0,
		hostlistCount: 2, hasUserHostlist: true };
}
function lists(base: string): ZapretHostlistInfo[] {
	return (['user', 'exclude'] as const).map((id) => ({ id, path: `${base}/${id}.txt`,
		exists: true, editable: true, entries: 1, domains: 1, ips: 0, gzipped: false, generated: false }));
}
function content(id: 'user' | 'exclude', value: string): ZapretHostlistContent {
	return { id, path: `/opt/zapret/${id}.txt`, content: value, revision: value };
}
function strategies(base: string): ZapretStrategies {
	return { items: [], revision: base, optVar: 'NFQWS2_OPT', config: `${base}/config`, raw: '' };
}
function deferred<T>() {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((done) => { resolve = done; });
	return { promise, resolve };
}

beforeEach(() => {
	vi.clearAllMocks();
	mocks.setStatus(status('/opt/zapret'));
	mocks.getStrategies.mockResolvedValue(strategies('/opt/zapret'));
	mocks.getHostlists.mockResolvedValue(lists('/opt/zapret'));
});
afterEach(cleanup);

it('loads a newly selected hostlist while the previous request is pending', async () => {
	const old = deferred<ZapretHostlistContent>();
	mocks.getHostlist.mockImplementation((id) => id === 'user' ? old.promise : Promise.resolve(content('exclude', 'exclude.example')));
	const view = render(ZapretTab);
	await waitFor(() => expect(mocks.getHostlist).toHaveBeenCalledWith('user'));
	await fireEvent.click(view.getByRole('button', { name: 'Исключения' }));
	await waitFor(() => expect(view.getByRole('textbox')).toHaveValue('exclude.example'));
	old.resolve(content('user', 'old.example'));
	await tick();
	await waitFor(() => expect(view.getByRole('textbox')).toHaveValue('exclude.example'));
});

it('reloads the editors when installation changes and ignores old responses', async () => {
	const old = deferred<ZapretHostlistContent>();
	mocks.getHostlist.mockReturnValueOnce(old.promise).mockResolvedValue(content('user', 'new.example'));
	const view = render(ZapretTab);
	await waitFor(() => expect(mocks.getHostlist).toHaveBeenCalledTimes(1));
	mocks.getStrategies.mockResolvedValue(strategies('/opt/zapret22'));
	mocks.getHostlists.mockResolvedValue(lists('/opt/zapret22'));
	mocks.setStatus(status('/opt/zapret22'));
	await waitFor(() => expect(mocks.getStrategies).toHaveBeenCalledTimes(2));
	await waitFor(() => expect(view.getByRole('textbox')).toHaveValue('new.example'));
	expect(view.getByText('/opt/zapret22/user.txt')).toBeInTheDocument();
	old.resolve(content('user', 'old.example'));
	await tick();
	await waitFor(() => expect(view.getByRole('textbox')).toHaveValue('new.example'));
	mocks.setStatus(status('/opt/zapret22'));
	await tick();
	expect(mocks.getStrategies).toHaveBeenCalledTimes(2);
	expect(mocks.getHostlists).toHaveBeenCalledTimes(2);
});

it('ignores strategies and list metadata returned by a previous installation', async () => {
	const oldStrategies = deferred<ZapretStrategies>();
	const oldLists = deferred<ZapretHostlistInfo[]>();
	mocks.getStrategies.mockReturnValueOnce(oldStrategies.promise).mockResolvedValue(strategies('/opt/zapret22'));
	mocks.getHostlists.mockReturnValueOnce(oldLists.promise).mockResolvedValue(lists('/opt/zapret22'));
	mocks.getHostlist.mockResolvedValue(content('user', 'new.example'));
	const view = render(ZapretTab);
	await waitFor(() => expect(mocks.getStrategies).toHaveBeenCalledTimes(1));
	mocks.setStatus(status('/opt/zapret22'));
	await waitFor(() => expect(view.getByRole('textbox')).toHaveValue('new.example'));
	oldStrategies.resolve(strategies('/opt/zapret'));
	oldLists.resolve(lists('/opt/zapret'));
	await tick();
	expect(view.getByText('/opt/zapret22/user.txt')).toBeInTheDocument();
	expect(view.queryByText('/opt/zapret/user.txt')).not.toBeInTheDocument();
	expect(view.getByRole('textbox')).toHaveValue('new.example');
});

it('keeps a successful save when only the daemon restart fails', async () => {
	mocks.getHostlist.mockResolvedValue(content('user', 'before.example'));
	mocks.saveHostlist.mockResolvedValue({ restartError: 'unsupported daemon restart' });
	const view = render(ZapretTab);
	await waitFor(() => expect(view.getByRole('textbox')).toHaveValue('before.example'));
	await fireEvent.input(view.getByRole('textbox'), { target: { value: 'after.example' } });
	mocks.getHostlist.mockResolvedValue(content('user', 'after.example'));
	await fireEvent.click(view.getByRole('button', { name: 'Сохранить' }));
	await waitFor(() => expect(mocks.saveHostlist).toHaveBeenCalledWith('user', 'after.example', 'before.example'));
	await waitFor(() => expect(mocks.error).toHaveBeenCalledWith(expect.stringContaining('unsupported daemon restart')));
	await waitFor(() => expect(view.getByRole('button', { name: 'Сохранить' })).toBeDisabled());
	expect(view.getByRole('textbox')).toHaveValue('after.example');
});

it('loads and creates an editable hostlist that does not exist yet', async () => {
	const absentLists = lists('/opt/zapret').map((host) =>
		host.id === 'user' ? { ...host, exists: false, entries: 0, domains: 0 } : host
	);
	mocks.getHostlists.mockResolvedValueOnce(absentLists).mockResolvedValue(lists('/opt/zapret'));
	mocks.getHostlist.mockResolvedValue({
		id: 'user', path: '/opt/zapret/user.txt', content: '', revision: 'absent-user-revision'
	});
	mocks.saveHostlist.mockResolvedValue({});

	const view = render(ZapretTab);
	await waitFor(() => expect(view.getByRole('textbox')).toHaveValue(''));
	await waitFor(() => expect(view.getByRole('textbox')).not.toBeDisabled());
	await fireEvent.input(view.getByRole('textbox'), { target: { value: 'new.example' } });
	mocks.getHostlist.mockResolvedValue(content('user', 'new.example\n'));
	await fireEvent.click(view.getByRole('button', { name: 'Сохранить' }));

	await waitFor(() => expect(mocks.saveHostlist).toHaveBeenCalledWith(
		'user', 'new.example', 'absent-user-revision'
	));
	await waitFor(() => expect(mocks.getHostlists).toHaveBeenCalledTimes(2));
	await waitFor(() => expect(view.getByRole('textbox')).toHaveValue('new.example\n'));
});

it('shows a retryable error and then an empty state when no hostlists are returned', async () => {
	mocks.getHostlists.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce([]);
	const view = render(ZapretTab);
	await waitFor(() => expect(view.getByRole('alert')).toHaveTextContent('Не удалось загрузить списки хостов'));
	expect(view.queryByText('Загрузка списков хостов…')).not.toBeInTheDocument();
	await fireEvent.click(view.getByRole('button', { name: 'Обновить' }));
	await waitFor(() => expect(view.getByText('Списки хостов недоступны.')).toBeInTheDocument());
	expect(mocks.getHostlists).toHaveBeenCalledTimes(2);
});

it('shows a retryable error when an editable hostlist cannot be read', async () => {
	mocks.getHostlist.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(content('user', 'recovered.example'));
	const view = render(ZapretTab);
	await waitFor(() => expect(view.getByRole('alert')).toHaveTextContent('Не удалось загрузить список хостов'));
	expect(view.queryByRole('textbox')).not.toBeInTheDocument();
	const refreshButtons = view.getAllByRole('button', { name: 'Обновить' });
	await fireEvent.click(refreshButtons.at(-1)!);
	await waitFor(() => expect(view.getByRole('textbox')).toHaveValue('recovered.example'));
});
