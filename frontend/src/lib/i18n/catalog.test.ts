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
	'/src/lib/components/warpscout/warpscoutUi.ts': { lines: 2, reason: 'сравнение с ошибкой бэкенда и имя профиля по умолчанию на роутере' },
	'/src/routes/tunnels/[id]/+page.svelte': { lines: 1, reason: 'подпись «Через …» сохраняется на бэкенде' },
	'/src/lib/api/clientFirc.ts': { lines: 3, reason: 'FIRC API ошибки' },
	'/src/lib/api/clientServers.ts': { lines: 1, reason: 'текст сертификата' },
	'/src/lib/components/firc/FircGroupsPanel.svelte': { lines: 214, reason: 'FIRC UI групп' },
	'/src/lib/components/firc/FircJournalPanel.svelte': { lines: 42, reason: 'FIRC UI журнала' },
	'/src/lib/components/firc/FircMigrationWizardModal.svelte': { lines: 248, reason: 'FIRC UI мастера миграции' },
	'/src/lib/components/firc/FircMonitorPanel.svelte': { lines: 87, reason: 'FIRC UI монитора' },
	'/src/lib/components/firc/FircResourceCard.svelte': { lines: 53, reason: 'FIRC UI ресурсов' },
	'/src/lib/components/firc/FircSettingsPanel.svelte': { lines: 153, reason: 'FIRC UI настроек' },
	'/src/lib/components/firc/FircTunnelEditModal.svelte': { lines: 113, reason: 'FIRC UI редактирования туннелей' },
	'/src/lib/components/servers/CDNDispatcherCard.svelte': { lines: 48, reason: 'Диспетчер CDN UI' },
	'/src/lib/components/servers/TelegramWebProxyCard.svelte': { lines: 198, reason: 'Telegram Proxy UI' },
	'/src/lib/components/servers/XrayAutoTunnelModal.svelte': { lines: 25, reason: 'Xray Auto Tunnel UI' },
	'/src/lib/components/servers/XrayServerCard.svelte': { lines: 239, reason: 'Xray Server UI' },
	'/src/lib/components/servers/XrayShareModal.svelte': { lines: 36, reason: 'Xray Share Modal UI' },
	'/src/lib/components/servers/reality/RealityFields.svelte': { lines: 21, reason: 'REALITY Preset Fields UI' },
	'/src/lib/components/servers/reality/RealityLinkResult.svelte': { lines: 6, reason: 'REALITY Link Result UI' },
	'/src/lib/components/servers/external-access/ExternalAccessSummary.svelte': { lines: 27, reason: 'Внешний доступ UI сводка' },
	'/src/lib/components/servers/external-access/ExternalAccessWizard.svelte': { lines: 214, reason: 'Внешний доступ UI мастер' },
	'/src/lib/components/servers/external-access/connectionLabels.ts': { lines: 65, reason: 'Внешний доступ подписи' },
	'/src/lib/components/servers/external-access/externalAccessModel.ts': { lines: 29, reason: 'Внешний доступ модель' },
	'/src/lib/components/servers/telegram-wizard/TelegramProxyWizard.svelte': { lines: 49, reason: 'Telegram Wizard UI' },
	'/src/lib/components/servers/wizard/ApplyProgress.svelte': { lines: 10, reason: 'Server Wizard прогресс' },
	'/src/lib/components/servers/wizard/ConnectionChecks.svelte': { lines: 10, reason: 'Server Wizard проверки' },
	'/src/lib/components/servers/wizard/ConnectionResult.svelte': { lines: 37, reason: 'Server Wizard результат' },
	'/src/lib/components/servers/wizard/PlanSummary.svelte': { lines: 4, reason: 'Server Wizard план' },
	'/src/lib/components/servers/wizard/PreflightList.svelte': { lines: 7, reason: 'Server Wizard preflight' },
	'/src/lib/components/servers/wizard/WizardShell.svelte': { lines: 5, reason: 'Server Wizard shell' },
	'/src/lib/components/testing/TunnelDiagnosticsModal.svelte': { lines: 1, reason: 'Диагностика туннелей' },
	'/src/lib/stores/firc.ts': { lines: 1, reason: 'FIRC store подписи' },
	'/src/routes/routing/FircRouterPanel.svelte': { lines: 50, reason: 'FIRC роутер панель' },
	'/src/routes/servers/+page.svelte': { lines: 3, reason: 'Серверы страница' },
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
