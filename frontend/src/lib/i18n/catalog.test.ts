import { describe, it, expect } from 'vitest';
import ru from '../../../messages/ru.json';
import en from '../../../messages/en.json';

// Страховка от регрессий i18n: словари совпадают по ключам, в них нет
// мёртвых ключей, а в коде интерфейса не появляется захардкоженный русский
// текст. Как переводить строки — CONTRIBUTING.md, «Локализация интерфейса».

const sources = import.meta.glob<string>(
	['/src/**/*.svelte', '/src/**/*.ts', '!/src/**/*.test.ts', '!/src/lib/paraglide/**', '!/src/vitest.setup.ts'],
	{ query: '?raw', import: 'default', eager: true },
);

const keys = (dict: Record<string, unknown>) => Object.keys(dict).filter((k) => k !== '$schema');

/** Код без комментариев и <style>: кириллица в комментариях допустима. */
function stripComments(path: string, src: string): string {
	let s = src;
	if (path.endsWith('.svelte')) {
		s = s.replace(/<style[\s\S]*?<\/style>/g, '').replace(/<!--[\s\S]*?-->/g, '');
	}
	s = s.replace(/\/\*[\s\S]*?\*\//g, '');
	return s.replace(/(^|[^:'"`])\/\/.*$/gm, '$1');
}

const CYRILLIC = /[Ѐ-ӿ]/;

/**
 * Файлы, где кириллица в коде намеренная: не текст интерфейса. Число — сколько
 * строк с кириллицей в файле сейчас; новая русская строка в таком файле тоже
 * уронит тест. Убрали кириллицу — уменьшите число или удалите запись.
 */
const ALLOWED: Record<string, { lines: number; reason: string }> = {
	'/src/lib/i18n/locale.svelte.ts': { lines: 1, reason: 'самоназвание «Русский» в переключателе' },
	'/src/lib/utils/resolve-icon-slug.ts': { lines: 14, reason: 'поисковые синонимы иконок для ввода пользователя' },
	'/src/lib/utils/service-icons.ts': { lines: 5, reason: 'поисковые синонимы иконок для ввода пользователя' },
	'/src/lib/utils/policy-icon.ts': { lines: 1, reason: 'ключевое слово для подбора иконки' },
	'/src/lib/utils/about-device.ts': { lines: 2, reason: 'регулярки по русскому тексту от бэкенда' },
	'/src/lib/components/singbox-routing/RouteInspector.svelte': { lines: 3, reason: 'регулярки по русскому тексту от бэкенда' },
	'/src/lib/components/proxy/serverClients.ts': { lines: 2, reason: 'сравнение с текстом ошибки бэкенда' },
	'/src/lib/components/settings/SettingsFooter.svelte': { lines: 8, reason: 'ники в благодарностях' },
	'/src/lib/components/subscriptions/SubscriptionSettingsTab.svelte': { lines: 2, reason: 'пример регулярки по именам серверов' },
	'/src/lib/components/subscriptions/SubscriptionGroupModal.svelte': { lines: 2, reason: 'пример регулярки по именам серверов' },
	'/src/lib/components/routing/singboxRouter/InlineRuleListEditor.svelte': { lines: 1, reason: 'пример домена *.рф' },
	'/src/lib/components/servers/PeerConfModal.svelte': { lines: 1, reason: 'регулярка очистки имени файла' },
	'/src/lib/components/dnsroutes/NdmsDisclaimerBanner.svelte': { lines: 2, reason: 'адреса тем форума' },
	'/src/lib/components/proxy/ShareWizard.svelte': { lines: 1, reason: 'имя абонента по умолчанию — данные на роутере' },
	'/src/lib/utils/deviceProxyInstance.ts': { lines: 1, reason: 'имя прокси по умолчанию — данные на роутере' },
	'/src/lib/components/sb-router/qosClasses.ts': { lines: 1, reason: 'имя класса по умолчанию — данные на роутере' },
	'/src/routes/tunnels/[id]/+page.svelte': { lines: 1, reason: 'подпись «Через …» сохраняется на бэкенде' },
};

/** \u0410-escape — та же кириллица, просто записанная кодами. */
const decodeEscapes = (s: string) =>
	s.replace(/\\u([0-9a-fA-F]{4})/g, (_, hex: string) => String.fromCharCode(parseInt(hex, 16)));

function cyrillicLines(path: string, src: string): string[] {
	return decodeEscapes(stripComments(path, src))
		.split('\n')
		.filter((l) => CYRILLIC.test(l))
		.map((l) => l.trim());
}

describe('словари i18n', () => {
	it('ключи ru.json и en.json совпадают', () => {
		const ruKeys = new Set(keys(ru));
		const enKeys = new Set(keys(en));
		expect(keys(ru).filter((k) => !enKeys.has(k)), 'есть в ru.json, нет в en.json').toEqual([]);
		expect(keys(en).filter((k) => !ruKeys.has(k)), 'есть в en.json, нет в ru.json').toEqual([]);
	});

	it('каждый ключ используется в коде', () => {
		const code = Object.values(sources).join('\n');
		const used = new Set(code.match(/\bm\.[a-z0-9_]+\b/g)?.map((s) => s.slice(2)));
		expect(keys(ru).filter((k) => !used.has(k)), 'неиспользуемые ключи — удалите их из обоих словарей').toEqual([]);
	});
});

describe('нет захардкоженного русского текста', () => {
	it('кириллица в коде только в файлах из списка исключений', () => {
		const offenders: string[] = [];
		for (const [path, src] of Object.entries(sources)) {
			if (path.startsWith('/src/routes/dev/')) continue; // демо-страницы, в прод не попадают
			const lines = cyrillicLines(path, src);
			const allowed = ALLOWED[path];
			if (allowed ? lines.length !== allowed.lines : lines.length > 0) {
				offenders.push(`${path} (${lines.length} стр.): ${lines.slice(0, 3).join(' | ')}`);
			}
		}
		expect(
			offenders,
			'Вынесите текст в messages/*.json (см. CONTRIBUTING.md) или, если это не текст интерфейса, добавьте файл в ALLOWED с причиной',
		).toEqual([]);
	});
});

describe('состояние компонентов', () => {
	it('обработчики не записывают готовый текст i18n в состояние', () => {
		// `error = m.foo()` фиксирует строку на языке момента присваивания.
		const re = /^\s*[A-Za-z_$][\w$.]*\s*=\s*m\.[a-z0-9_]+\(/m;
		const offenders: string[] = [];
		for (const [path, src] of Object.entries(sources)) {
			if (!path.endsWith('.svelte')) continue;
			for (const [i, line] of src.split('\n').entries()) {
				if (/^\s*(const|let|var)\b/.test(line)) continue;
				if (re.test(line)) offenders.push(`${path}:${i + 1}: ${line.trim()}`);
			}
		}
		expect(
			offenders,
			'Не кладите m.foo() в состояние готовой строкой: запишите () => m.foo() (тип UiText) и показывайте через uiText(...) из $lib/i18n — тогда текст следует за языком',
		).toEqual([]);
	});
});
