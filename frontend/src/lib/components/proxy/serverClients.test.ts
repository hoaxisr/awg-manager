import { describe, it, expect } from 'vitest';
import type { WdttPanelUserEntry } from '$lib/types';
import {
	addErrorText,
	addedPassword,
	noUsableAfterRemove,
	counterLabel,
	groupDigits,
	headerApplied,
	isUsable,
	reissueName,
	rowActions,
	shortPassword,
	usableCount,
} from './serverClients';

function user(p: Partial<WdttPanelUserEntry> & { password: string }): WdttPanelUserEntry {
	return {
		comment: '',
		isAuto: false,
		...p,
	};
}

const alive = user({ password: 'p-alive', comment: 'Телефон' });
const second = user({ password: 'p-second', comment: 'Ноутбук' });
const off = user({ password: 'p-off', comment: 'Планшет' });
const expired = user({ password: 'p-old', comment: 'Гостевой' });

describe('предикат рабочего абонента', () => {
	// Непригодность у абонента осталась одна — пустой пароль. Срок действия и
	// деактивацию ставил только админ-путь форка, главный пароль сервера снят
	// как ненужный: ни в WRAP-ключах, ни в аутентификации он не участвовал.
	it('не рабочий — только пустой пароль', () => {
		expect(isUsable(alive)).toBe(true);
		expect(isUsable(user({ password: '   ' }))).toBe(false);
	});

	it('счётчик печатает оба числа', () => {
		expect(counterLabel([alive, off, user({ password: '  ' })])).toBe(
			'Абонентов: 3 · рабочих: 2',
		);
	});
});

describe('матрица кнопок §4.4', () => {
	it('рабочий: ссылка есть, перевыпуск есть, удаление разрешено', () => {
		const a = rowActions(alive, [alive, second]);
		expect(a.link).toBe('yes');
		expect(a.reissue).toBe(true);
		expect(a.remove).toBe('yes');
		expect(a.removeHint).toBe('');
	});

	// Смена ключа живому абоненту — обычная нужда, а не только починка
	// просрочки. Стража последнего рабочего перевыпуску не мешает: новый
	// абонент заводится ДО удаления старого.
	it('последний рабочий: удаление заблокировано, но перевыпуск доступен', () => {
		const a = rowActions(alive, [alive, user({ password: '  ' })]);
		expect(a.remove).toBe('blocked');
		expect(a.reissue).toBe(true);
	});

	it('последний рабочий: удаление заблокировано с SH-37', () => {
		const a = rowActions(alive, [alive, user({ password: '  ' })]);
		expect(a.remove).toBe('blocked');
		expect(a.removeHint).toContain('Нельзя удалить последнего рабочего абонента');
	});

	it('отключённый держит стража: рядом с ним удаление рабочего разрешено', () => {
		expect(rowActions(alive, [alive, off]).remove).toBe('yes');
	});
});

describe('SH-77: предупреждение «сервер нельзя будет запустить»', () => {
	it('показывается, когда после удаления рабочих не остаётся', () => {
		expect(noUsableAfterRemove(expired, [expired])).toBe(true);
	});

	it('молчит, когда рабочие остаются', () => {
		expect(noUsableAfterRemove(expired, [alive, expired])).toBe(false);
	});
});

describe('бейдж шапки', () => {
	it('судьбу решает reload мутации', () => {
		expect(headerApplied('delivered', false)).toBe(true);
		expect(headerApplied('serverStopped', true)).toBe(false);
		// failed — «применено сейчас» обещать нельзя.
		expect(headerApplied('failed', true)).toBe(false);
	});

	it('без reload считается по running', () => {
		expect(headerApplied(undefined, true)).toBe(true);
		expect(headerApplied(undefined, false)).toBe(false);
	});
});

describe('тексты отказов добавления', () => {
	it('ADD_NOT_APPLIED — SH-26 с ошибкой бэкенда в шаблоне', () => {
		expect(
			addErrorText(
				'WDTT_SERVER_CLIENT_ADD_NOT_APPLIED',
				'абонент создан, но не записан в файл сервера: read-only file system',
			),
		).toBe(
			'Абонент создан, но не записан в файл сервера: read-only file system. Сервер подхватит его при следующем запуске.',
		);
	});

	it('MAIN_PASSWORD_NOT_SAVED — текст бэкенда дословно', () => {
		const msg = 'абонент создан, но пароль сервера не сохранён — задайте его в настройках сервера: no space left';
		expect(addErrorText('WDTT_SERVER_MAIN_PASSWORD_NOT_SAVED', msg)).toBe(msg);
	});

	it('прочие отказы раскладываются в TS-13..TS-16 по тексту', () => {
		const code = 'WDTT_SERVER_CLIENT_ADD_FAILED';
		expect(addErrorText(code, 'пароль занят живым абонентом')).toBe('Пароль занят живым абонентом');
	});

	it('незнакомый отказ показывается как есть', () => {
		expect(addErrorText('WDTT_SERVER_CLIENT_ADD_FAILED', 'диск отвалился')).toBe('диск отвалился');
	});
});

describe('перевыпуск', () => {
	it('имя переносится, старая запись коллизией не считается', () => {
		expect(reissueName(expired, [alive, expired])).toBe('Гостевой');
	});

	it('чужой тёзка даёт «(2)», занятое «(2)» — «(3)»', () => {
		const twin = user({ password: 'p-twin', comment: 'Гостевой' });
		expect(reissueName(expired, [twin, expired])).toBe('Гостевой (2)');
		const twin2 = user({ password: 'p-twin2', comment: 'Гостевой (2)' });
		expect(reissueName(expired, [twin, twin2, expired])).toBe('Гостевой (3)');
	});

	it('пароль новой записи вычисляется разностью списков', () => {
		expect(addedPassword([alive], [alive, second])).toBe('p-second');
		expect(addedPassword([alive], [alive])).toBe('');
	});
});

describe('мелочи представления', () => {
	it('длинный пароль сокращается, короткий — нет', () => {
		expect(shortPassword('0123456789abcdef')).toBe('0123456789abcdef');
		expect(shortPassword('0123456789abcdefghij')).toBe('01234567…efghij');
	});

	it('LK-11: разряды тысяч разделяются пробелом', () => {
		expect(groupDigits(1340)).toBe('1 340');
		expect(groupDigits(940)).toBe('940');
	});
});
