import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

// Guard (F443): `.chip` и `.chip-icon` заняты утилитами Skeleton и глобальным
// `.chip` из app.css. Свой scoped-чип с таким именем молча наследует их
// nowrap/центровку/шрифт — длинный текст вылезает за карточку. Уточнять
// глобальный чип можно — в контексте (`.actions .chip`) или модификатором из
// app.css (`.chip.chip-danger`); определять свой — нет: ловим правило,
// селектор которого НАЧИНАЕТСЯ с этого класса.
const RESERVED = ['chip', 'chip-icon'];

const srcDir = resolve(process.cwd(), 'src');
const files = (readdirSync(srcDir, { recursive: true }) as string[]).filter((f) => f.endsWith('.svelte'));

function ownRuleSelectors(source: string): string[] {
	const found: string[] = [];
	for (const [, css] of source.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)) {
		const noComments = css.replace(/\/\*[\s\S]*?\*\//g, '');
		const re = new RegExp(`(?:^|[{},])\\s*([a-z]*\\.(?:${RESERVED.join('|')}))(?![-\\w]|\\.chip-)`, 'g');
		for (const m of noComments.matchAll(re)) found.push(m[1]);
	}
	return found;
}

describe('scoped-стили не определяют свой .chip', () => {
	it('компоненты найдены', () => {
		expect(files.length).toBeGreaterThan(100);
	});

	it('детектор ловит своё определение и пропускает уточнение в контексте', () => {
		expect(ownRuleSelectors('<style>\n  .chip { color: red; }\n</style>')).toEqual(['.chip']);
		expect(ownRuleSelectors('<style>.a {}\n button.chip:hover {}</style>')).toEqual(['button.chip']);
		expect(ownRuleSelectors('<style>.x, .chip-icon {}</style>')).toEqual(['.chip-icon']);
		expect(ownRuleSelectors('<style>.actions .chip {} .a :global(.chip) {} .chip-label {}</style>')).toEqual([]);
		expect(ownRuleSelectors('<style>/* Не .chip: */ .tag-chip {}</style>')).toEqual([]);
		expect(ownRuleSelectors('<style>.chip.chip-danger {}</style>')).toEqual([]);
		expect(ownRuleSelectors('<style>.chip.active {}</style>')).toEqual(['.chip']);
	});

	it('ни один компонент не определяет .chip / .chip-icon', () => {
		const offenders = files.flatMap((f) =>
			ownRuleSelectors(readFileSync(join(srcDir, f), 'utf8')).map((sel) => `${f}: ${sel}`),
		);
		expect(offenders).toEqual([]);
	});
});
