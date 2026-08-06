import { describe, it, expect } from 'vitest';
import { MTU_MAX, MTU_MIN, mtuError, poolError } from './fakeipFields';

describe('poolError', () => {
	it('принимает корректные CIDR', () => {
		expect(poolError('10.128.0.0/10', 4)).toBeNull();
		expect(poolError('198.18.0.0/15', 4)).toBeNull();
		expect(poolError('3f80::/10', 6)).toBeNull();
		expect(poolError('fc00::/18', 6)).toBeNull();
	});

	// Пусто допустимо у обоих, но по разным причинам: v6 так выключают, пустой
	// v4 бэкенд заменит умолчанием при нормализации.
	it('пустое значение ошибкой не считает', () => {
		expect(poolError('', 4)).toBeNull();
		expect(poolError('   ', 6)).toBeNull();
	});

	it('отвергает мусор и адрес без маски', () => {
		expect(poolError('10.128.0.0', 4)).not.toBeNull();
		expect(poolError('не-адрес', 4)).not.toBeNull();
		expect(poolError('3f80::', 6)).not.toBeNull();
	});

	// Разные семейства — разные тексты: подсказка «пример: 10.128.0.0/10» в
	// поле v6 отправила бы пользователя вписывать IPv4.
	it('текст ошибки называет нужное семейство', () => {
		expect(poolError('x', 4)).toMatch(/IPv4/);
		expect(poolError('x', 6)).toMatch(/IPv6/);
		expect(poolError('x', 4)).not.toBe(poolError('x', 6));
	});

	// Проверка семейства не должна быть формальностью: IPv4 в поле v6 и
	// наоборот — самая вероятная ошибка ввода.
	it('IPv4-CIDR в поле v6 не проходит', () => {
		expect(poolError('10.128.0.0/10', 6)).not.toBeNull();
	});
});

describe('mtuError', () => {
	it('принимает границы диапазона', () => {
		expect(mtuError(String(MTU_MIN))).toBeNull();
		expect(mtuError(String(MTU_MAX))).toBeNull();
		expect(mtuError('1500')).toBeNull();
	});

	it('отвергает выход за границы', () => {
		expect(mtuError(String(MTU_MIN - 1))).not.toBeNull();
		expect(mtuError(String(MTU_MAX + 1))).not.toBeNull();
	});

	// MTU обязателен: пустое поле не «оставить как есть», а отсутствие значения
	// в PUT, который уносит настройки целиком.
	it('отвергает пустое, дробное и нечисловое', () => {
		expect(mtuError('')).not.toBeNull();
		expect(mtuError('   ')).not.toBeNull();
		expect(mtuError('1500.5')).not.toBeNull();
		expect(mtuError('много')).not.toBeNull();
	});

	it('текст ошибки называет диапазон', () => {
		expect(mtuError('1')).toContain(String(MTU_MIN));
		expect(mtuError('1')).toContain(String(MTU_MAX));
	});
});
