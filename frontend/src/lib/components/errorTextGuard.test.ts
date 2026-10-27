import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

// Guard (F688): `.error-text` из app.css невидим (opacity: 0) без `.visible`.
// Элемент без `visible` / `class:visible` не показывает ошибку вовсе, а свой
// scoped `.error-text` (правило 8 CLAUDE.md) opacity не сбрасывает и только
// маскирует это в коде. Так молча пропадали ошибки в HrNeoEditModal,
// IconPickerModal и AdoptTunnelDialog.
const srcDir = resolve(process.cwd(), 'src');
const files = (readdirSync(srcDir, { recursive: true }) as string[]).filter((f) => f.endsWith('.svelte'));

function invisibleErrorTags(source: string): string[] {
	const markup = source.replace(/<style[^>]*>[\s\S]*?<\/style>/g, '');
	return [...markup.matchAll(/<[a-z][^>]*\bclass="[^"]*\berror-text(?![-\w])[^"]*"[^>]*>/g)]
		.map((m) => m[0])
		.filter((tag) => !/\bclass="[^"]*\bvisible\b/.test(tag) && !/\bclass:visible\b/.test(tag));
}

function ownErrorTextRules(source: string): string[] {
	const found: string[] = [];
	for (const [, css] of source.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)) {
		const noComments = css.replace(/\/\*[\s\S]*?\*\//g, '');
		for (const m of noComments.matchAll(/(?:^|[{},])\s*([a-z]*\.error-text)(?![-\w])/g)) found.push(m[1]);
	}
	return found;
}

describe('.error-text виден и не переопределяется', () => {
	it('компоненты найдены', () => {
		expect(files.length).toBeGreaterThan(100);
	});

	it('детекторы ловят невидимый тег и своё правило', () => {
		expect(invisibleErrorTags('<div class="error-text">x</div>')).toHaveLength(1);
		expect(invisibleErrorTags('<p class="error-text visible">x</p>')).toEqual([]);
		expect(invisibleErrorTags('<p class="error-text" class:visible={e}>x</p>')).toEqual([]);
		expect(invisibleErrorTags('<style>.x {}</style><span class="error-text-wide">x</span>')).toEqual([]);
		expect(ownErrorTextRules('<style>\n  .error-text { color: red; }\n</style>')).toEqual(['.error-text']);
		expect(ownErrorTextRules('<style>.form .error-text {} .error-text-x {}</style>')).toEqual([]);
	});

	it('ни один компонент не прячет ошибку и не определяет свой .error-text', () => {
		const offenders = files.flatMap((f) => {
			const src = readFileSync(join(srcDir, f), 'utf8');
			return [...invisibleErrorTags(src), ...ownErrorTextRules(src)].map((x) => `${f}: ${x}`);
		});
		expect(offenders).toEqual([]);
	});
});
