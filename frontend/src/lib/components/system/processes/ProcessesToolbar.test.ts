import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import ProcessesToolbar from './ProcessesToolbar.svelte';
import { formatTime } from '$lib/utils/format';

const props = {
	enabled: true,
	loading: false,
	interval: 5,
	showKernelThreads: false,
	searchQuery: '',
	processCount: 0,
	ontoggleenabled: vi.fn(),
	onrefresh: vi.fn(),
	onintervalchange: vi.fn(),
	ontogglekernelthreads: vi.fn(),
	onsearchchange: vi.fn(),
};

// Сервер пересчитывает не чаще раза в 4 с, и «Обновить» раньше отдаёт тот же
// снимок — время замера показывает пользователю, что данные те же.
describe('ProcessesToolbar время снимка', () => {
	it('показывает время замера', () => {
		const at = '2026-09-26T12:05:12Z';
		render(ProcessesToolbar, { props: { ...props, snapshotAt: at } });
		expect(screen.getByText(`обновлено ${formatTime(at)}`)).toBeTruthy();
	});

	it('без снимка подписи нет', () => {
		render(ProcessesToolbar, { props });
		expect(screen.queryByText(/обновлено/)).toBeNull();
	});

	it('интервалы только 5с и 30с', () => {
		render(ProcessesToolbar, { props });
		expect(screen.getByText('5с')).toBeTruthy();
		expect(screen.getByText('30с')).toBeTruthy();
		expect(screen.queryByText('1с')).toBeNull();
		expect(screen.queryByText('2с')).toBeNull();
	});
});
