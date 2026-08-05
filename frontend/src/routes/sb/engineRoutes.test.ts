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
import { existsSync, readdirSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { ENGINE_GROUP_PATHS } from '$lib/data/engineDraftGuard';
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

	it('нетрогаемые страницы раздела лежат вне слоя (engine)', () => {
		for (const path of OUTSIDE_GROUP) {
			const dir = path.replace('/sb/', '');
			expect(existsSync(join(SB_ROUTES, dir))).toBe(true);
			expect(existsSync(join(ENGINE_ROUTES, dir))).toBe(false);
			expect(ENGINE_GROUP_PATHS as readonly string[]).not.toContain(path);
		}
	});
});
