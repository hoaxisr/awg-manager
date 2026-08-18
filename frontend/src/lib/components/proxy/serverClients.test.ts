import { describe, it, expect } from 'vitest';
import type { WdttPanelUserEntry } from '$lib/types';
import {
	addErrorText,
	addedPassword,
	autoCreateAfterRemove,
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
		isDeactivated: false,
		isExpired: false,
		isMainPassword: false,
		isAuto: false,
		...p,
	};
}

const main = user({ password: 'mainpass', comment: 'Главный', isMainPassword: true });
const alive = user({ password: 'p-alive', comment: 'Телефон' });
const second = user({ password: 'p-second', comment: 'Ноутбук' });
const off = user({ password: 'p-off', comment: 'Планшет', isDeactivated: true });
const expired = user({ password: 'p-old', comment: 'Гостевой', isExpired: true });

describe('предикат рабочего абонента', () => {
	it('повторяет бэкенд: пустой пароль, главный пароль и просрочка — не рабочие', () => {
		expect(isUsable(alive)).toBe(true);
		expect(isUsable(main)).toBe(false);
		expect(isUsable(expired)).toBe(false);
		expect(isUsable(user({ password: '   ' }))).toBe(false);
	});

	it('отключённый абонент считается РАБОЧИМ (оговорка SH-28/38)', () => {
		expect(isUsable(off)).toBe(true);
		expect(usableCount([main, alive, off, expired])).toBe(2);
	});

	it('SH-38: счётчик печатает оба числа', () => {
		expect(counterLabel([main, alive, off])).toBe('Абонентов: 3 · рабочих: 2');
	});
});

describe('матрица кнопок §4.4', () => {
	it('рабочий: ссылка есть, перевыпуска нет, удаление разрешено', () => {
		const a = rowActions(alive, [alive, second]);
		expect(a.link).toBe('yes');
		expect(a.reissue).toBe(false);
		expect(a.remove).toBe('yes');
		expect(a.removeHint).toBe('');
	});

	it('последний рабочий: удаление заблокировано с SH-37', () => {
		const a = rowActions(alive, [main, alive, expired]);
		expect(a.remove).toBe('blocked');
		expect(a.removeHint).toContain('Нельзя удалить последнего рабочего абонента');
	});

	it('отключённый держит стража: рядом с ним удаление рабочего разрешено', () => {
		expect(rowActions(alive, [alive, off]).remove).toBe('yes');
	});

	it('просроченный: ссылка заблокирована с SH-33, есть перевыпуск, удаление разрешено', () => {
		const a = rowActions(expired, [alive, expired]);
		expect(a.link).toBe('blocked');
		expect(a.linkHint).toContain('Абонент просрочен');
		expect(a.reissue).toBe(true);
		expect(a.remove).toBe('yes');
	});

	it('единственный просроченный удаляется — рабочих и так нет', () => {
		expect(rowActions(expired, [expired]).remove).toBe('yes');
	});

	it('главный пароль: ссылки и перевыпуска нет, удаление заблокировано с SH-36', () => {
		const a = rowActions(main, [main, alive]);
		expect(a.link).toBe('hidden');
		expect(a.reissue).toBe(false);
		expect(a.remove).toBe('blocked');
		expect(a.removeHint).toContain('Удаление в два хода');
	});

	it('просроченный главный пароль разбирается как главный', () => {
		const both = user({ password: 'mainpass', isMainPassword: true, isExpired: true });
		expect(rowActions(both, [both]).reissue).toBe(false);
		expect(rowActions(both, [both]).remove).toBe('blocked');
	});
});

describe('SH-77: предупреждение про «Абонент 1»', () => {
	it('показывается, когда после удаления рабочих не остаётся', () => {
		expect(autoCreateAfterRemove(expired, [expired])).toBe(true);
		expect(autoCreateAfterRemove(expired, [main, expired])).toBe(true);
	});

	it('молчит, когда рабочие остаются', () => {
		expect(autoCreateAfterRemove(expired, [alive, expired])).toBe(false);
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
		expect(addErrorText(code, 'сначала задайте пароль сервера')).toBe(
			'Сначала задайте главный пароль сервера',
		);
		expect(
			addErrorText(code, 'пароль совпадает с главным паролем сервера — задайте абоненту другой пароль'),
		).toContain('Это главный пароль сервера');
		expect(addErrorText(code, 'пароль принадлежит просроченному абоненту, задайте новый')).toBe(
			'Пароль принадлежит просроченному абоненту, задайте новый',
		);
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
