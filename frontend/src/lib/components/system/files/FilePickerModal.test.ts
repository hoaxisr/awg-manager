import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, screen } from '@testing-library/svelte';
import FilePickerModal from './FilePickerModal.svelte';
import type { SystemFileEntry, SystemFileRoot } from '$lib/types/systemTools';

const systemFilesRoots = vi.fn();
const systemFilesList = vi.fn();

vi.mock('$lib/api/client', () => ({
	api: {
		systemFilesRoots: (...a: unknown[]) => systemFilesRoots(...a),
		systemFilesList: (...a: unknown[]) => systemFilesList(...a),
	},
}));

// Имена фикстур намеренно не пересекаются с подписями дерева и хлебных крошек:
// «Накопитель» встречается только как метка корня, «subscr.txt» — только внутри
// /opt/etc, поэтому появление текста на экране однозначно говорит, что показан
// нужный каталог.
const ROOTS: SystemFileRoot[] = [{ path: '/opt', label: 'Накопитель', readOnly: false }];

const file = (name: string, path: string): SystemFileEntry => ({
	name,
	path,
	isDir: false,
	size: 12,
	mode: '0644',
	modTime: '2026-09-21T10:00:00Z',
});

const dir = (name: string, path: string): SystemFileEntry => ({
	...file(name, path),
	isDir: true,
});

const LISTINGS: Record<string, SystemFileEntry[]> = {
	'/opt': [dir('etc', '/opt/etc'), file('zzz.conf', '/opt/zzz.conf')],
	'/opt/etc': [dir('..', '/opt'), file('subscr.txt', '/opt/etc/subscr.txt')],
};

beforeEach(() => {
	vi.clearAllMocks();
	systemFilesRoots.mockResolvedValue(ROOTS);
	systemFilesList.mockImplementation(async (path: string) => ({
		path,
		entries: LISTINGS[path] ?? [],
	}));
});

describe('FilePickerModal', () => {
	it('загружает корни и показывает каталог первого корня', async () => {
		render(FilePickerModal, { props: { open: true, onclose: vi.fn(), onpick: vi.fn() } });

		expect(await screen.findByText('Накопитель')).toBeTruthy();
		expect(await screen.findByText('zzz.conf')).toBeTruthy();
		expect(systemFilesList).toHaveBeenCalledWith('/opt');
	});

	it('открывает каталог по клику на записи-каталоге', async () => {
		render(FilePickerModal, { props: { open: true, onclose: vi.fn(), onpick: vi.fn() } });

		await fireEvent.click(await screen.findByText('etc'));

		expect(await screen.findByText('subscr.txt')).toBeTruthy();
	});

	it('отдаёт абсолютный путь выбранного файла в onpick', async () => {
		const onpick = vi.fn();
		render(FilePickerModal, { props: { open: true, onclose: vi.fn(), onpick } });

		const pick = await screen.findByRole('button', { name: 'Выбрать' });
		expect((pick as HTMLButtonElement).disabled).toBe(true);

		await fireEvent.click(await screen.findByText('zzz.conf'));
		expect((pick as HTMLButtonElement).disabled).toBe(false);

		await fireEvent.click(pick);
		expect(onpick).toHaveBeenCalledWith('/opt/zzz.conf');
	});

	it('«Отмена» закрывает пикер', async () => {
		const onclose = vi.fn();
		render(FilePickerModal, { props: { open: true, onclose, onpick: vi.fn() } });

		await fireEvent.click(await screen.findByRole('button', { name: 'Отмена' }));

		expect(onclose).toHaveBeenCalledOnce();
	});
});
