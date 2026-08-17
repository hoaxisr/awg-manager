import { describe, it, expect } from 'vitest';
import { tunnelErrorNames } from './deleteNotice';

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
