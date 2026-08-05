/**
 * Предикат напоминания о непринятом черновике маршрутизации.
 *
 * Проверяется чистая функция, а не поднятый роутер: `$app/navigation` в проекте
 * не мокают ни в одном компонентном тесте, а само срабатывание beforeNavigate
 * смотрится глазами на мок-стенде.
 */
import { describe, it, expect } from 'vitest';
import { remindAboutDraft, ENGINE_GROUP_PATHS } from './engineDraftGuard';

/** Страница группы, с которой уходят. Любая — граница у них общая. */
const FROM = '/sb/rules';

describe('remindAboutDraft', () => {
	it('уход из группы при живом черновике — напоминаем', () => {
		expect(remindAboutDraft(FROM, '/awg/tunnels', true)).toBe(true);
	});

	it('уход на корень тоже наружу', () => {
		expect(remindAboutDraft(FROM, '/', true)).toBe(true);
	});

	it('страницы группы Sing-box вне движка — тоже наружу', () => {
		// /sb/tunnels, /sb/awg3, /sb/subscriptions и /sb/geodata лежат в разделе
		// меню, но вне слоя (engine): черновик маршрутизации там не применить.
		expect(remindAboutDraft(FROM, '/sb/tunnels', true)).toBe(true);
		expect(remindAboutDraft(FROM, '/sb/geodata', true)).toBe(true);
	});

	it('переход между маршрутами группы — молчим', () => {
		for (const to of ENGINE_GROUP_PATHS) {
			expect(remindAboutDraft(FROM, to, true)).toBe(false);
		}
	});

	it('навигация внутри самой страницы — молчим', () => {
		// Смена поверхности ?view=, визард ?add/?edit, ?trace/?q: pathname тот же.
		expect(remindAboutDraft('/sb/engine', '/sb/engine', true)).toBe(false);
	});

	it('редирект-адрес /sb/routing ведёт обратно в группу — молчим', () => {
		expect(remindAboutDraft(FROM, '/sb/routing', true)).toBe(false);
	});

	it('без черновика молчим всегда — и наружу тоже', () => {
		expect(remindAboutDraft(FROM, '/awg/tunnels', false)).toBe(false);
		expect(remindAboutDraft(FROM, '/sb/rules', false)).toBe(false);
	});

	it('уход из приложения оставляем браузеру', () => {
		// `to === null` в beforeNavigate: закрытие вкладки, внешняя ссылка.
		expect(remindAboutDraft(FROM, null, true)).toBe(false);
	});

	it('вне группы предикат не срабатывает вовсе', () => {
		// Слой смонтирован только внутри группы, но условие держим явным: без
		// него любой промах монтирования дал бы напоминание на чужой странице.
		expect(remindAboutDraft('/awg/tunnels', '/router/policies', true)).toBe(false);
		expect(remindAboutDraft(null, '/awg/tunnels', true)).toBe(false);
	});

	it('хвостовой слэш и вложенный путь не выносят страницу из группы', () => {
		expect(remindAboutDraft('/sb/rules/', '/sb/dns/', true)).toBe(false);
		expect(remindAboutDraft(FROM, '/sb/rules/42', true)).toBe(false);
	});

	it('чужой путь с тем же префиксом группой не считается', () => {
		expect(remindAboutDraft(FROM, '/sb/rules-archive', true)).toBe(true);
	});
});
