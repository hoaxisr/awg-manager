/**
 * Хосты общих механик группы Sing-box смонтированы РОВНО ОДИН РАЗ.
 *
 * Почему это отдельный тест по исходникам, а не рендер. Ни один тест в проекте
 * не рендерит `SingboxRouterRedesignPage` и `FakeIPPageShell` — они тянут за
 * собой половину приложения. Значит вернуть `<StagingBanner />` на страницу
 * можно, и весь остальной прогон останется зелёным: двойные модалки, две пары
 * кнопок «Применить», два окна прогресса — и ничего в тестах не шелохнётся.
 *
 * Сканируются только `.svelte`: искомые строки лежат в этом файле как данные, и
 * скан по `.ts` считал бы сам себя.
 */
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';

const SRC = resolve(process.cwd(), 'src');

/** Хосты, у которых на всё приложение должна быть одна точка монтирования. */
const SINGLE_MOUNT = ['<StagingBanner', '<ModeSwitchHost', '<SelectiveRebuildModal'];

function svelteFiles(dir: string): string[] {
	const out: string[] = [];
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const full = join(dir, entry.name);
		if (entry.isDirectory()) out.push(...svelteFiles(full));
		else if (entry.name.endsWith('.svelte')) out.push(full);
	}
	return out;
}

/** Комментарии не монтируют — упоминание хоста в пояснении не должно ронять тест. */
function withoutComments(source: string): string {
	return source
		.replace(/<!--[\s\S]*?-->/g, '')
		.replace(/\/\*[\s\S]*?\*\//g, '')
		.replace(/\/\/[^\n]*/g, '');
}

/** Файлы с числом вхождений — в сообщении об ошибке видно ОБА места, а не факт. */
function mountSites(needle: string): string[] {
	const sites: string[] = [];
	for (const file of svelteFiles(SRC)) {
		const hits = withoutComments(readFileSync(file, 'utf8')).split(needle).length - 1;
		for (let i = 0; i < hits; i++) sites.push(relative(SRC, file));
	}
	return sites;
}

describe('хосты общих механик группы Sing-box', () => {
	it.each(SINGLE_MOUNT)('%s смонтирован ровно один раз на всё приложение', (needle) => {
		expect(mountSites(needle)).toHaveLength(1);
	});

	it('все три живут в layout группы движка, а не на странице', () => {
		for (const needle of SINGLE_MOUNT) {
			expect(mountSites(needle)).toEqual(['routes/sb/(engine)/+layout.svelte']);
		}
	});
});
