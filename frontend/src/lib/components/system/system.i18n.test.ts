import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { locale } from '$lib/i18n';
import FileToolbar from './files/FileToolbar.svelte';
import ProcessesToolbar from './processes/ProcessesToolbar.svelte';
import PonyBoardingPass from './ponies/PonyBoardingPass.svelte';
import type { TicketOrder } from './ponies/types';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

const toolbarProps = {
	enabled: true,
	loading: false,
	interval: 5,
	showKernelThreads: false,
	searchQuery: '',
	processCount: 1,
	ontoggleenabled: vi.fn(),
	onrefresh: vi.fn(),
	onintervalchange: vi.fn(),
	ontogglekernelthreads: vi.fn(),
	onsearchchange: vi.fn(),
};

const ticket: TicketOrder = {
	passengerName: null,
	destinationId: 'friday',
	serviceClass: 'business',
	options: ['vpn'],
	ticketNumber: 'PONY-123456',
	seat: '7A',
	priceGlitter: 500,
};

describe('system i18n', () => {
	it('FileToolbar: кнопки и плейсхолдер переключаются на английский', () => {
		render(FileToolbar, {
			props: {
				loading: false,
				readOnly: false,
				selected: null,
				searchQuery: '',
				onRefresh: vi.fn(),
				onMkdir: vi.fn(),
				onNewFile: vi.fn(),
				onUploadClick: vi.fn(),
				onDownload: vi.fn(),
			},
		});
		expect(screen.getByRole('button', { name: 'Обновить' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Папка' })).toBeTruthy();
		expect(screen.getByPlaceholderText('Поиск в папке…')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByRole('button', { name: 'Refresh' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Folder' })).toBeTruthy();
		expect(screen.getByPlaceholderText('Search in folder…')).toBeTruthy();
	});

	it('ProcessesToolbar: счётчик процессов склоняется по языку', () => {
		const { rerender } = render(ProcessesToolbar, { props: toolbarProps });
		expect(screen.getByText('1 процесс')).toBeTruthy();

		void rerender({ ...toolbarProps, processCount: 2 });
		flushSync();
		expect(screen.getByText('2 процесса')).toBeTruthy();

		void rerender({ ...toolbarProps, processCount: 5 });
		flushSync();
		expect(screen.getByText('5 процессов')).toBeTruthy();

		locale.set('en');
		flushSync();
		expect(screen.getByText('5 processes')).toBeTruthy();
		expect(screen.getByText('Monitoring is on')).toBeTruthy();

		void rerender({ ...toolbarProps, processCount: 1 });
		flushSync();
		expect(screen.getByText('1 process')).toBeTruthy();
	});

	it('PonyBoardingPass: имя, класс и направление хранятся кодами и следуют за языком', () => {
		render(PonyBoardingPass, { props: { ticket, onreset: vi.fn() } });
		expect(screen.getByText('Счастливый Пользователь')).toBeTruthy();
		expect(screen.getByText('🎠 Бизнес в Зефирной Карете')).toBeTruthy();
		expect(screen.getByText('🌴 Остров Вечной Пятницы (DPI выключен навсегда)')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Happy User')).toBeTruthy();
		expect(screen.getByText('🎠 Business in a Marshmallow Carriage')).toBeTruthy();
		expect(screen.getByText('🌴 Eternal Friday Island (DPI switched off forever)')).toBeTruthy();
		expect(screen.getByText(/Immunity from blocks and DPI/)).toBeTruthy();
	});
});
