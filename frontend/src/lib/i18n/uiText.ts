/**
 * Текст для $state: строка от сервера — как есть, сообщение i18n — отложенным
 * вызовом, который вычисляется при показе и поэтому следует за языком.
 */
export type UiText = string | (() => string);

export function uiText(t: UiText | null | undefined): string {
	return typeof t === 'function' ? t() : (t ?? '');
}
