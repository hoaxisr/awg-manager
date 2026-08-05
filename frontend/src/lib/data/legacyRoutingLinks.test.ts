import { describe, it, expect } from 'vitest';
import { legacyRoutingTarget, ENGINE_PATH } from './legacyRoutingLinks';

/** Разбирает старый адрес целиком — так, как его увидит `+page.ts`. */
const target = (search: string) =>
	legacyRoutingTarget(new URL(`http://router/sb/routing${search}`).searchParams);

// Ожидания записаны литералами, а не выведены из таблиц модуля: тест, который
// перебирает ту же таблицу, что и код, доказывает только её существование.
describe('legacyRoutingTarget: ?sub=', () => {
	it.each([
		['connections', '/sb/connections'],
		['logs', '/sb/logs'],
		['rules', '/sb/rules'],
		['rulesets', '/sb/rule-sets'],
		['outbounds', '/sb/groups'],
		['dns', '/sb/dns'],
		['engine', '/sb/engine'],
		['deviceproxy', '/sb/engine'],
	])('?sub=%s → %s', (sub, expected) => {
		expect(target(`?sub=${sub}`)).toBe(expected);
	});
});

describe('legacyRoutingTarget: ?chip=', () => {
	it.each([
		['overview', '/sb/engine'],
		['inbounds', '/sb/inbounds'],
		['outbounds', '/sb/groups'],
		['rulesets', '/sb/rule-sets'],
		['dns', '/sb/dns'],
		['routes', '/sb/rules'],
		['devices', '/sb/engine'],
		['connections', '/sb/connections'],
		['logs', '/sb/logs'],
	])('?chip=%s → %s', (chip, expected) => {
		expect(target(`?chip=${chip}`)).toBe(expected);
	});
});

describe('legacyRoutingTarget: отброшенные параметры', () => {
	// Ни один из них не выбирает страницу: обе поверхности слились в «Движок»,
	// тумблер режима умер, состояние визарда и проверки адреса не переносится.
	it.each(['', '?view=tproxy', '?view=fakeip', '?mode=beginner', '?mode=expert',
		'?add=1', '?edit=3', '?trace=1&q=example.com'])(
		'«%s» → «Движок»',
		(search) => {
			expect(target(search)).toBe(ENGINE_PATH);
		},
	);

	it('неизвестное значение ведёт на «Движок», а не в 404', () => {
		expect(target('?sub=nosuchthing')).toBe(ENGINE_PATH);
		expect(target('?chip=nosuchthing')).toBe(ENGINE_PATH);
	});

	it('унаследованные ключи объекта не считаются значениями', () => {
		expect(target('?sub=constructor')).toBe(ENGINE_PATH);
		expect(target('?chip=toString')).toBe(ENGINE_PATH);
	});
});

describe('legacyRoutingTarget: приоритет', () => {
	it('?sub= сильнее ?chip=: подвид перекрывал страницу целиком', () => {
		expect(target('?chip=dns&sub=logs')).toBe('/sb/logs');
	});

	it('?view= не спорит с ?sub= и ?chip=', () => {
		expect(target('?view=fakeip&sub=connections')).toBe('/sb/connections');
		expect(target('?view=fakeip&chip=inbounds')).toBe('/sb/inbounds');
	});

	it('живой диплинк на журнал переживает разбор', () => {
		expect(target('?view=tproxy&sub=logs&mode=expert')).toBe('/sb/logs');
	});
});

describe('legacyRoutingTarget: форма ответа', () => {
	it('возвращает чистый путь — без параметров и хвостового слэша', () => {
		for (const search of ['?sub=logs', '?chip=outbounds', '?view=fakeip&add=1']) {
			const path = target(search);
			expect(path).toMatch(/^\/sb\/[a-z-]+$/);
		}
	});
});
