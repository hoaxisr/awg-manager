/**
 * Список маршрутов группы «Движок» сверяется с файловой структурой.
 *
 * Зачем. Состав группы записан ТРИЖДЫ и в трёх разных видах:
 *
 *  1. папки `routes/sb/(engine)/<имя>/+page.svelte` — что реально существует;
 *  2. `ENGINE_GROUP_PATHS` (`lib/data/engineDraftGuard.ts`) — что накрыто гардом
 *     непринятого черновика;
 *  3. `NAV_TREE` (`lib/data/navigation.ts`) — что видно в сайдбаре.
 *
 * Автоматической сверки между ними не было. Волна 5D2 добавляет страницу папкой
 * — и она молча остаётся без гарда: черновик при уходе с неё никто не помянет,
 * а поймать это можно только вручную. Симметрично строка в списке без папки даёт
 * пункт меню в 404.
 *
 * Файловая структура здесь — источник истины: маршрут задаёт SvelteKit, а списки
 * его лишь описывают. Скан по файлам, а не импорт: `+page.svelte` группы тянут
 * половину приложения, а нужен только факт существования.
 */
import { describe, it, expect } from 'vitest';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { ENGINE_GROUP_PATHS } from '$lib/data/engineDraftGuard';
import { REDIRECT_TARGETS } from '$lib/data/legacyRoutingLinks';
import { NAV_TREE, groupItems, type NavGroup } from '$lib/data/navigation';

const SB_ROUTES = resolve(process.cwd(), 'src/routes/sb');
const ENGINE_ROUTES = join(SB_ROUTES, '(engine)');

/** Страницы группы `(engine)` по файловой системе: `/sb/<папка>`. */
function enginePagesOnDisk(): string[] {
	return readdirSync(ENGINE_ROUTES, { withFileTypes: true })
		.filter((e) => e.isDirectory() && existsSync(join(ENGINE_ROUTES, e.name, '+page.svelte')))
		.map((e) => `/sb/${e.name}`)
		.sort();
}

/**
 * Четыре страницы раздела меню вне слоя `(engine)`: их 5D1 не трогает, гейта и
 * баннера черновика там нет, и в `ENGINE_GROUP_PATHS` их быть не должно.
 */
const OUTSIDE_GROUP = ['/sb/tunnels', '/sb/awg3', '/sb/subscriptions', '/sb/geodata'].sort();

const sbGroup = NAV_TREE.find((e): e is NavGroup => e.kind === 'group' && e.id === 'sb');

/** Адреса пунктов группы Sing-box в сайдбаре (без разделителей). */
function sidebarHrefs(): string[] {
	if (!sbGroup) throw new Error('в NAV_TREE нет группы Sing-box');
	return groupItems(sbGroup)
		.map((i) => i.href)
		.sort();
}

describe('состав группы «Движок» сверен с файловой структурой', () => {
	it('гард черновика накрывает ровно те страницы, что лежат в (engine)', () => {
		// `/sb/routing` в списке намеренно: своей страницы у него нет, он
		// редирект (routing/+page.ts), и напоминание на нём было бы ложным.
		const guarded = ENGINE_GROUP_PATHS.filter((p) => p !== '/sb/routing').sort();
		expect(guarded).toEqual(enginePagesOnDisk());
	});

	it('`/sb/routing` остаётся редиректом без своей страницы', () => {
		expect(existsSync(join(SB_ROUTES, 'routing/+page.ts'))).toBe(true);
		expect(existsSync(join(SB_ROUTES, 'routing/+page.svelte'))).toBe(false);
		expect(ENGINE_GROUP_PATHS).toContain('/sb/routing');
	});

	it('у каждой страницы группы есть свой пункт сайдбара', () => {
		const hrefs = sidebarHrefs();
		for (const path of enginePagesOnDisk()) {
			expect(hrefs).toContain(path);
		}
	});

	it('в сайдбаре нет пунктов, ведущих в несуществующий маршрут', () => {
		expect(sidebarHrefs()).toEqual([...enginePagesOnDisk(), ...OUTSIDE_GROUP].sort());
	});

	it('редирект легаси-закладок ведёт только на существующие страницы группы', () => {
		// Четвёртый список маршрутов — таблицы `legacyRoutingLinks.ts`. Без этой
		// сверки переименование страницы волной 5D2 уронит три списка выше, а
		// редирект молча уведёт старые закладки в 404.
		const pages = enginePagesOnDisk();
		for (const path of REDIRECT_TARGETS) {
			expect(ENGINE_GROUP_PATHS as readonly string[]).toContain(path);
			// Отдельно от списка выше: в нём есть '/sb/routing' — сам редирект,
			// и цель, указывающая на него, дала бы петлю, а не страницу.
			expect(pages).toContain(path);
		}
	});

	it('нетрогаемые страницы раздела лежат вне слоя (engine)', () => {
		for (const path of OUTSIDE_GROUP) {
			const dir = path.replace('/sb/', '');
			expect(existsSync(join(SB_ROUTES, dir))).toBe(true);
			expect(existsSync(join(ENGINE_ROUTES, dir))).toBe(false);
			expect(ENGINE_GROUP_PATHS as readonly string[]).not.toContain(path);
		}
	});
});

/**
 * Обещание заглушек и временное содержимое «Движка» живут и умирают вместе.
 *
 * Шесть заглушек (`GroupStub`) пишут: «до волны 5D2x это живёт на странице
 * „Движок“». Правда с датой истечения — «Движок» несёт содержимое старой
 * `/sb/routing` только до волны 5D2a. Проверять сам факт ссылки бесполезно:
 * `href` останется валидным и после переезда, а обещание станет ложью молча.
 *
 * Связка здесь — по исходникам, а не по импорту: `+page.svelte` «Движка» тянет
 * половину приложения, а нужен только факт наличия маркера.
 */
describe('заглушки не переживают временное содержимое «Движка»', () => {
	const TEMP_MARKER = 'TEMP_UNTIL_5D2A';
	const ENGINE_PAGE = join(ENGINE_ROUTES, 'engine', '+page.svelte');
	const GROUP_STUB = resolve(process.cwd(), 'src/lib/components/sb-group/GroupStub.svelte');

	it('маркер временного содержимого и ссылка заглушек сняты вместе', () => {
		const engineIsTemporary = readFileSync(ENGINE_PAGE, 'utf8').includes(TEMP_MARKER);
		// Ссылку рисует сам GroupStub — она одна на все шесть страниц.
		const stubsPointAtEngine = readFileSync(GROUP_STUB, 'utf8').includes('ENGINE_PATH');

		const why = [
			`Маркер ${TEMP_MARKER} в (engine)/engine/+page.svelte: ${engineIsTemporary ? 'есть' : 'снят'}.`,
			`Ссылка заглушек на «Движок» в GroupStub.svelte: ${stubsPointAtEngine ? 'есть' : 'снята'}.`,
			'',
			'Маркер снят, а ссылка осталась — так падает волна 5D2a: содержимое старой',
			'/sb/routing уехало из «Движка», и строка «до волны N это живёт на странице',
			'„Движок“» врёт. Перенаправь её туда, куда содержимое переехало, — либо',
			'убери вместе с наполнением заглушки, если наполняешь эту страницу.',
			'',
			'Ссылка снята, а маркер остался — заглушки снова тупик: функция жива на',
			'«Движке», но со страницы об этом не узнать. Верни строку или сними маркер,',
			'если временного содержимого там уже нет.',
		].join('\n');

		expect(stubsPointAtEngine, why).toBe(engineIsTemporary);
	});
});
