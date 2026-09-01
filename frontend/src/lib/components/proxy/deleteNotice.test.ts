import { describe, it, expect, vi, beforeEach } from 'vitest';

// Фабрика vi.mock поднимается наверх файла, поэтому шпионы создаются ВНУТРИ
// неё, а достаются импортом — верхнеуровневые переменные тут недоступны.
vi.mock('$lib/stores/notifications', () => ({
	notifications: { success: vi.fn(), error: vi.fn() }
}));

import { notifications } from '$lib/stores/notifications';
import { reportDeletedTunnelsFromError, tunnelErrorNames } from './deleteNotice';

const success = notifications.success as unknown as ReturnType<typeof vi.fn>;
const error = notifications.error as unknown as ReturnType<typeof vi.fn>;

describe('tunnelErrorNames', () => {
	it('отрезает id и текст ошибки', () => {
		expect(tunnelErrorNames(['Клиент wdtt (awg10): permission denied'])).toEqual(['Клиент wdtt']);
	});

	it('id raw-записи тоже отрезается', () => {
		expect(tunnelErrorNames(['Клиент wdtt (wdttraw-default): boom'])).toEqual(['Клиент wdtt']);
	});

	it('скобки внутри имени не обрезают его раньше времени', () => {
		expect(tunnelErrorNames(['Клиент (v2): тест wdtt (awg10): boom'])).toEqual([
			'Клиент (v2): тест wdtt',
		]);
		expect(tunnelErrorNames(['Резерв (быстрый) wdtt (awg11): boom'])).toEqual([
			'Резерв (быстрый) wdtt',
		]);
	});

	it('строка без хвоста остаётся как есть', () => {
		expect(tunnelErrorNames(['не удалось прочитать хранилище'])).toEqual([
			'не удалось прочитать хранилище',
		]);
	});

	it('пустые строки выбрасываются', () => {
		expect(tunnelErrorNames(['', '   '])).toEqual([]);
	});
});

// PF23: отказ удаления может прийти ПОСЛЕ уборки связей — отчёт лежит в теле
// ошибки, и без него карточка туннеля исчезает без объяснения.
describe('reportDeletedTunnelsFromError', () => {
	beforeEach(() => {
		success.mockClear();
		error.mockClear();
	});

	it('достаёт снятые туннели из тела ошибки', () => {
		const err = Object.assign(new Error('реестр отверг'), {
			body: { error: true, data: { deletedTunnels: ['awg10'], tunnelErrors: [] } }
		});
		reportDeletedTunnelsFromError(err);
		expect(success).toHaveBeenCalledWith('AWG-туннелей удалено: 1 — перезапустите клиент');
	});

	it('молчит на ошибке без тела: обычный сбой сети сообщать не о чем', () => {
		reportDeletedTunnelsFromError(new Error('сеть'));
		expect(success).not.toHaveBeenCalled();
		expect(error).not.toHaveBeenCalled();
	});
});
