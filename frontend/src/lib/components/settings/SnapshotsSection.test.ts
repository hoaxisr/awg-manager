import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/svelte';
import { writable, type Readable } from 'svelte/store';
import BackupRestoreCard from './BackupRestoreCard.svelte';
import PageStateTestHost from './PageStateTestHost.svelte';
import SnapshotsSection from './SnapshotsSection.svelte';
import { api } from '$lib/api/client';
import type { PollingState } from '$lib/stores/polling';
import type { UpdateSnapshotsData } from '$lib/types';

vi.mock('$lib/api/client', () => ({
	api: {
		importFullBackup: vi.fn(),
		deleteUpdateSnapshot: vi.fn(),
		restoreUpdateSnapshot: vi.fn(),
		downloadUpdateSnapshot: vi.fn()
	}
}));
// Ожидание перезапуска демона в тесте не нужно: сразу «не дождались».
vi.mock('$lib/restartRecovery', () => ({
	waitForBackendRestart: vi.fn().mockResolvedValue('timeout')
}));
vi.mock('$lib/stores/notifications', () => ({
	notifications: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }
}));

// Стор подменяется: модульный синглтон кешировал бы данные между тестами.
const { store, refetch, applyMutationResponse } = vi.hoisted(() => {
	const store: { current: Readable<unknown> | null } = { current: null };
	return { store, refetch: vi.fn(), applyMutationResponse: vi.fn() };
});
vi.mock('$lib/stores/updateSnapshots', () => ({
	updateSnapshots: {
		subscribe: (run: (v: unknown) => void) => store.current?.subscribe(run) ?? (() => {}),
		refetch,
		applyMutationResponse,
		invalidate: vi.fn()
	}
}));

function setState(s: Partial<PollingState<UpdateSnapshotsData>>) {
	store.current = writable<PollingState<UpdateSnapshotsData>>({
		data: null,
		status: 'fresh',
		error: null,
		lastFetchedAt: 1,
		consecutiveFailures: 0,
		...s
	});
}

const snap = {
	id: 'before-update-20261006-123045.tar.gz',
	createdAt: '2026-10-06T12:30:45Z',
	appVersion: '2.19.9',
	size: 204800
};

describe('SnapshotsSection: снимки перед обновлением', () => {
	beforeEach(() => vi.clearAllMocks());

	it('без снимков говорит, когда появится первый, и называет срок хранения', () => {
		setState({ data: { snapshots: [], keep: 3, ttlDays: 7 } });
		render(SnapshotsSection);
		expect(screen.getByText(/Снимков пока нет/)).toBeTruthy();
		expect(document.body.textContent).toMatch(/Хранятся 3 последних, каждый — не дольше 7 дн\./);
	});

	it('показывает версию снимка и удаляет его только после подтверждения', async () => {
		setState({ data: { snapshots: [snap], keep: 3, ttlDays: 7 } });
		vi.mocked(api.deleteUpdateSnapshot).mockResolvedValue({ snapshots: [], keep: 3, ttlDays: 7 });
		render(SnapshotsSection);
		expect(screen.getByText(/версия 2\.19\.9/)).toBeTruthy();

		await fireEvent.click(screen.getByRole('button', { name: 'Удалить' }));
		expect(api.deleteUpdateSnapshot).not.toHaveBeenCalled();
		expect(await screen.findByText('Удалить снимок?')).toBeTruthy();

		const buttons = screen.getAllByRole('button', { name: 'Удалить' });
		await fireEvent.click(buttons[buttons.length - 1]);
		expect(api.deleteUpdateSnapshot).toHaveBeenCalledWith(snap.id);
		await vi.waitFor(() =>
			expect(applyMutationResponse).toHaveBeenCalledWith({ snapshots: [], keep: 3, ttlDays: 7 })
		);
	});

	// Восстановление разрушительно: подтверждение обязано звать восстановление
	// именно этого снимка, а не загрузку файла.
	it('восстанавливает выбранный снимок только после подтверждения', async () => {
		setState({ data: { snapshots: [snap], keep: 3, ttlDays: 7 } });
		vi.mocked(api.restoreUpdateSnapshot).mockResolvedValue({ message: 'ok' });
		render(SnapshotsSection);

		const rowRestore = screen.getAllByRole('button', { name: 'Восстановить' });
		await fireEvent.click(rowRestore[rowRestore.length - 1]);
		expect(api.restoreUpdateSnapshot).not.toHaveBeenCalled();
		expect(await screen.findByText(/Данные будут возвращены к снимку/)).toBeTruthy();

		const confirm = screen.getAllByRole('button', { name: 'Восстановить' });
		await fireEvent.click(confirm[confirm.length - 1]);
		await vi.waitFor(() => expect(api.restoreUpdateSnapshot).toHaveBeenCalledWith(snap.id));
		expect(api.importFullBackup).not.toHaveBeenCalled();
	});

	it('ошибку загрузки не выдаёт за «снимков нет» и даёт повторить', async () => {
		setState({ status: 'error', error: 'boom' });
		render(SnapshotsSection);
		expect(screen.getByText(/Не удалось загрузить список снимков: boom/)).toBeTruthy();
		expect(screen.queryByText(/Снимков пока нет/)).toBeNull();
		await fireEvent.click(screen.getByRole('button', { name: 'Повторить' }));
		expect(refetch).toHaveBeenCalled();
	});

	it('пока список грузится, пустым его не показывает', () => {
		setState({ status: 'loading', lastFetchedAt: 0 });
		render(SnapshotsSection);
		expect(screen.queryByText(/Снимков пока нет/)).toBeNull();
	});

	// Восстановление поверх идущего смешало бы данные двух архивов.
	it('пока восстанавливается файл, кнопки снимков заперты', async () => {
		setState({ data: { snapshots: [snap], keep: 3, ttlDays: 7 } });
		vi.mocked(api.importFullBackup).mockReturnValue(new Promise(() => {}));
		vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')));
		const { container } = render(PageStateTestHost, {
			props: { shown: true, cards: [{ component: BackupRestoreCard }, { component: SnapshotsSection }] },
		});
		const row = container.querySelector<HTMLElement>('.snapshot-row')!;
		const rowDisabled = () => within(row).getAllByRole<HTMLButtonElement>('button').map((b) => b.disabled);
		expect(rowDisabled()).toEqual([false, false, false]);

		await fireEvent.change(container.querySelector('input[type="file"]')!, {
			target: { files: [new File(['x'], 'backup.tar.gz')] },
		});
		await fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Восстановить' }));
		await vi.waitFor(() => expect(api.importFullBackup).toHaveBeenCalledTimes(1));
		expect(rowDisabled()).toEqual([true, true, true]);
		vi.unstubAllGlobals();
	});
});
