// Единые настройки компиляции Paraglide: их импортирует vite.config.ts
// (vite-плагин для dev/build/vitest), а `npm run i18n:compile` запускает этот
// файл напрямую — для svelte-check, который vite не поднимает (`npm run check`
// и `prepare` после npm install).
import { fileURLToPath } from 'node:url';
import { compile } from '@inlang/paraglide-js';

const root = fileURLToPath(new URL('..', import.meta.url));

/** @type {import('@inlang/paraglide-js').CompilerOptions} */
export const paraglideOptions = {
	project: `${root}project.inlang`,
	outdir: `${root}src/lib/paraglide`,
	// Ручной выбор пользователя → русский. Язык браузера не учитываем: по
	// умолчанию интерфейс русский, как и раньше. Без префиксов в URL и без cookie.
	strategy: ['localStorage', 'baseLocale'],
	localStorageKey: 'awg-manager-locale',
	emitReadme: false,
	// По модулю на язык, а не на сообщение: с тысячами сообщений модуль на
	// каждое не укладывает прод-сборку в стандартный предел памяти Node.
	outputStructure: 'locale-modules',
};

if (process.argv[1] === fileURLToPath(import.meta.url)) {
	await compile(paraglideOptions);
}
