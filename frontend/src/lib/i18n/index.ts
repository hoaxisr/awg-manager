// Точка входа i18n. Компоненты импортируют сообщения отсюда, а не напрямую
// из $lib/paraglide: так модуль локали гарантированно подключён раньше первого
// m.foo(), и смена языка в настройках перерисовывает текст без перезагрузки.
export { m } from '$lib/paraglide/messages';
export { locale, LOCALES, LOCALE_LABELS, type Locale } from './locale.svelte';
