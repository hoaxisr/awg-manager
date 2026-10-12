// Точка входа i18n. Компоненты импортируют сообщения отсюда, а не напрямую
// из $lib/paraglide: так модуль локали гарантированно подключён раньше первого
// m.foo(), и смена языка в настройках перерисовывает текст без перезагрузки.
export { m } from '$lib/paraglide/messages';
export {
	locale,
	formatLocale,
	autoFormatLocale,
	dateFormat,
	DATE_FORMATS,
	LOCALES,
	LOCALE_LABELS,
	type DateFormat,
	type Locale,
} from './locale.svelte';
export { uiText, type UiText } from './uiText';
