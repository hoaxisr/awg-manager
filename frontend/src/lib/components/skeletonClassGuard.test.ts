import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

// Guard (F443, F444): имена утилит Skeleton (`label`, `input`, `table`, `chip`…)
// генерируются Tailwind из любого упоминания класса в разметке. Свой scoped-класс
// с таким именем молча наследует свойства утилиты, которых его правило не
// перекрывает (`.label` → width:100%/display:block, `.input` → отступы и
// кольцо фокуса). Уточнять общий класс app.css можно — в контексте
// (`.actions .chip`) или модификатором (`.chip.chip-danger`); определять свой —
// нет: ловим правило, селектор которого НАЧИНАЕТСЯ с этого класса.
//
// Список берётся из CSS Skeleton, а не держится руками. Исключения —
// btn/badge/card: общие классы app.css (правило 8 CLAUDE.md), их облик
// осознанно собран из утилиты и app.css, компоненты их дорабатывают.
const SHARED = new Set(['btn', 'badge', 'card']);
const skeletonCss = readFileSync(resolve(process.cwd(), 'node_modules/@skeletonlabs/skeleton/dist/index.css'), 'utf8');
const RESERVED = new Set(
	[...skeletonCss.matchAll(/@utility ([a-zA-Z][\w-]*)\s*\{/g)].map((m) => m[1]).filter((n) => !SHARED.has(n)),
);
// Модификатор вида `.chip.chip-danger` — уточнение общего класса app.css; для
// прочих имён Skeleton своего общего класса нет, и `.label.label-x` — тоже свой.
const appCss = readFileSync(resolve(process.cwd(), 'src/app.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
const IN_APP_CSS = new Set([...RESERVED].filter((n) => new RegExp(`\\.${n}(?![\\w-])`).test(appCss)));

const srcDir = resolve(process.cwd(), 'src');
const files = (readdirSync(srcDir, { recursive: true }) as string[]).filter((f) => f.endsWith('.svelte'));

function ownRuleSelectors(source: string): string[] {
	const found: string[] = [];
	for (const [, css] of source.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)) {
		const noComments = css.replace(/\/\*[\s\S]*?\*\//g, '');
		for (const m of noComments.matchAll(/(?:^|[{},])\s*([a-z0-9]*\.([a-zA-Z][\w-]*))((?:\.[\w-]+)*)/g)) {
			const [, sel, name, rest] = m;
			if (RESERVED.has(name) && !(IN_APP_CSS.has(name) && rest.startsWith(`.${name}-`))) found.push(sel);
		}
	}
	return found;
}

// Tailwind рождает утилиту из любого упоминания класса, поэтому имя Skeleton в
// разметке без своего правила — та же утечка (class="label" в чипе). Общие
// классы app.css (`chip`) в разметке законны — правило 8 CLAUDE.md.
function reservedInMarkup(source: string): string[] {
	const markup = source.replace(/<style[^>]*>[\s\S]*?<\/style>/g, '');
	const tokens = [
		...[...markup.matchAll(/\bclass="([^"]*)"/g)].flatMap((m) => m[1].split(/\s+/)),
		...[...markup.matchAll(/\bclass:([\w-]+)/g)].map((m) => m[1]),
	];
	return tokens.filter((t) => RESERVED.has(t) && !IN_APP_CSS.has(t));
}

describe('scoped-стили не определяют классы с именами утилит Skeleton', () => {
	it('компоненты и утилиты найдены', () => {
		expect(files.length).toBeGreaterThan(100);
		for (const n of ['chip', 'chip-icon', 'label', 'input', 'table', 'placeholder']) expect(RESERVED.has(n)).toBe(true);
		expect(RESERVED.has('btn')).toBe(false);
		expect(IN_APP_CSS.has('chip')).toBe(true);
	});

	it('детектор ловит своё определение и пропускает уточнение в контексте', () => {
		expect(ownRuleSelectors('<style>\n  .chip { color: red; }\n</style>')).toEqual(['.chip']);
		expect(ownRuleSelectors('<style>.a {}\n button.chip:hover {}</style>')).toEqual(['button.chip']);
		expect(ownRuleSelectors('<style>.x, .chip-icon {}</style>')).toEqual(['.chip-icon']);
		expect(ownRuleSelectors('<style>.actions .chip {} .a :global(.chip) {} .chip-label {}</style>')).toEqual([]);
		expect(ownRuleSelectors('<style>/* Не .chip: */ .tag-chip {}</style>')).toEqual([]);
		expect(ownRuleSelectors('<style>.chip.chip-danger {}</style>')).toEqual([]);
		expect(ownRuleSelectors('<style>.chip.active {}</style>')).toEqual(['.chip']);
		expect(ownRuleSelectors('<style>.label { font-size: 12px; }</style>')).toEqual(['.label']);
		expect(ownRuleSelectors('<style>.field-label {} .btn {} .card.x {}</style>')).toEqual([]);
		expect(ownRuleSelectors('<style>h1.label {}</style>')).toEqual(['h1.label']);
		expect(ownRuleSelectors('<style>.label.label-x {}</style>')).toEqual(['.label']);
		expect(reservedInMarkup('<span class="a label">x</span><i class:input={on}></i>')).toEqual(['label', 'input']);
		expect(reservedInMarkup('<span class="field-label btn chip">x</span><style>.label {}</style>')).toEqual([]);
	});

	it('ни один компонент не определяет класс с именем утилиты Skeleton', () => {
		const offenders = files.flatMap((f) =>
			ownRuleSelectors(readFileSync(join(srcDir, f), 'utf8')).map((sel) => `${f}: ${sel}`),
		);
		expect(offenders).toEqual([]);
	});

	it('разметка не использует имена утилит Skeleton как свои классы', () => {
		const offenders = files.flatMap((f) =>
			reservedInMarkup(readFileSync(join(srcDir, f), 'utf8')).map((c) => `${f}: class ${c}`),
		);
		expect(offenders).toEqual([]);
	});
});
